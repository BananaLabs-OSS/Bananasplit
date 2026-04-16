package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/BananaLabs-OSS/Fiber/pulp"
)

// MatcherConfig holds the matcher's runtime parameters.
type MatcherConfig struct {
	RegistryURL string
	TickRate    time.Duration
	RelayHost   string
	RelayPort   int
}

// Matcher pairs queued players with ready matches and hands them off
// to the relevant game and lobby servers. Tick is driven from the
// plugin step loop — no background goroutine (WASM plugins cannot
// spawn independently-scheduled goroutines).
type Matcher struct {
	config    MatcherConfig
	queues    *QueueManager
	players   *PlayerRegistry
	referrals *ReferralQueue
	peel      *PeelClient

	lastTick    uint64
	lastCleanup uint64
}

func NewMatcher(cfg MatcherConfig, q *QueueManager, p *PlayerRegistry, r *ReferralQueue, peel *PeelClient) *Matcher {
	return &Matcher{
		config:    cfg,
		queues:    q,
		players:   p,
		referrals: r,
		peel:      peel,
	}
}

// TickIfDue runs one matching cycle if the configured interval has
// elapsed since the last tick. Queue timeout cleanup runs every 30
// seconds regardless. Called from the plugin's OnStep handler with
// the envelope's wall time.
func (m *Matcher) TickIfDue(wallNanos uint64) {
	tickNanos := uint64(m.config.TickRate)
	if m.lastTick == 0 || wallNanos-m.lastTick >= tickNanos {
		m.lastTick = wallNanos
		for _, mode := range m.queues.Modes() {
			m.tryMatch(mode)
		}
	}
	const cleanupNanos = 30 * uint64(time.Second)
	if m.lastCleanup == 0 || wallNanos-m.lastCleanup >= cleanupNanos {
		m.lastCleanup = wallNanos
		m.queues.Cleanup()
	}
}

func (m *Matcher) tryMatch(mode string) {
	server, matchID, found := m.findReadyMatch(mode)
	if !found {
		return
	}
	match := server.Matches[matchID]
	needed := match.Need
	if m.queues.Size(mode) < needed {
		return
	}
	players := m.queues.Pop(mode, needed)
	if players == nil {
		return
	}

	fmt.Printf("[bananasplit] matched %d players for %s on %s/%s\n", len(players), mode, server.ID, matchID)

	uuids := make([]string, len(players))
	for i, p := range players {
		uuids[i] = p.UUID
	}

	m.sendExpect(server, matchID, uuids)
	m.updateMatchStatus(server.ID, matchID, StatusBusy, uuids)
	m.notifyLobbies(players, server, matchID, mode)
}

func (m *Matcher) findReadyMatch(mode string) (ServerInfo, string, bool) {
	url := fmt.Sprintf("%s/registry/servers?type=game&mode=%s&hasReadyMatch=true", m.config.RegistryURL, mode)
	resp, err := pulp.HTTP.Fetch(pulp.HTTPFetchRequest{Method: "GET", URL: url})
	if err != nil || resp.Status != 200 {
		return ServerInfo{}, "", false
	}
	var servers []ServerInfo
	if err := json.Unmarshal(resp.Body, &servers); err != nil {
		return ServerInfo{}, "", false
	}
	for _, server := range servers {
		for matchID, match := range server.Matches {
			if match.Status == StatusReady {
				return server, matchID, true
			}
		}
	}
	return ServerInfo{}, "", false
}

// FindLobby returns a lobby server with spare capacity, or ok=false
// when the registry reports no availability. Exposed for HTTP handlers
// that need a lobby directly (e.g. /route-request, /assign).
func (m *Matcher) FindLobby() (ServerInfo, bool) {
	url := fmt.Sprintf("%s/registry/servers?type=lobby&hasCapacity=true", m.config.RegistryURL)
	resp, err := pulp.HTTP.Fetch(pulp.HTTPFetchRequest{Method: "GET", URL: url})
	if err != nil || resp.Status != 200 {
		return ServerInfo{}, false
	}
	var servers []ServerInfo
	if err := json.Unmarshal(resp.Body, &servers); err != nil {
		return ServerInfo{}, false
	}
	if len(servers) == 0 {
		return ServerInfo{}, false
	}
	return servers[0], true
}

func (m *Matcher) sendExpect(server ServerInfo, matchID string, uuids []string) {
	url := fmt.Sprintf("http://%s:%d/expect", server.Host, server.WebhookPort)
	body, _ := json.Marshal(ExpectRequest{MatchID: matchID, UUIDs: uuids})
	resp, err := pulp.HTTP.Fetch(pulp.HTTPFetchRequest{
		Method:  "POST",
		URL:     url,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    body,
	})
	if err != nil {
		fmt.Printf("[bananasplit] send expect to %s: %v\n", server.ID, err)
		return
	}
	_ = resp
	fmt.Printf("[bananasplit] sent expect to %s for match %s\n", server.ID, matchID)
}

func (m *Matcher) updateMatchStatus(serverID, matchID string, status MatchStatus, players []string) {
	url := fmt.Sprintf("%s/registry/servers/%s/matches/%s", m.config.RegistryURL, serverID, matchID)
	body, _ := json.Marshal(MatchInfo{Status: status, Need: len(players), Players: players})
	if _, err := pulp.HTTP.Fetch(pulp.HTTPFetchRequest{
		Method:  "PUT",
		URL:     url,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    body,
	}); err != nil {
		fmt.Printf("[bananasplit] update match status: %v\n", err)
	}
}

func (m *Matcher) notifyLobbies(players []QueueEntry, server ServerInfo, matchID, mode string) {
	lobbies := map[string][]string{}
	for _, p := range players {
		lobbies[p.LobbyServer] = append(lobbies[p.LobbyServer], p.UUID)
	}

	backend := fmt.Sprintf("%s:%d", server.Host, server.Port)

	for lobbyID, uuids := range lobbies {
		lobbyURL := fmt.Sprintf("%s/registry/servers/%s", m.config.RegistryURL, lobbyID)
		resp, err := pulp.HTTP.Fetch(pulp.HTTPFetchRequest{Method: "GET", URL: lobbyURL})
		if err != nil || resp.Status != 200 {
			fmt.Printf("[bananasplit] get lobby %s: %v\n", lobbyID, err)
			continue
		}
		var lobby ServerInfo
		if err := json.Unmarshal(resp.Body, &lobby); err != nil {
			fmt.Printf("[bananasplit] decode lobby %s: %v\n", lobbyID, err)
			continue
		}

		webhookURL := fmt.Sprintf("http://%s:%d/match", lobby.Host, lobby.WebhookPort)
		payload := MatchReadyRequest{
			MatchID:    matchID,
			Mode:       mode,
			Players:    uuids,
			GameServer: backend,
		}
		body, _ := json.Marshal(payload)
		if _, err := pulp.HTTP.Fetch(pulp.HTTPFetchRequest{
			Method:  "POST",
			URL:     webhookURL,
			Headers: map[string]string{"Content-Type": "application/json"},
			Body:    body,
		}); err != nil {
			fmt.Printf("[bananasplit] notify lobby %s: %v\n", lobbyID, err)
			continue
		}
		fmt.Printf("[bananasplit] notified lobby %s — %d players → %s\n", lobbyID, len(uuids), backend)
	}
}
