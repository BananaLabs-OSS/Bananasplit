package main

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/BananaLabs-OSS/Fiber/pulp"
)

// safeWebhookTarget validates a registry-supplied host:port before the
// matcher dials it for an outbound webhook (sendExpect, notifyLobbies).
// The host data originates from the trusted Bananagine registry rather
// than raw end-user input, so this is a confused-deputy guard, not a full
// untrusted-input SSRF filter: it rejects an empty host, a non-positive /
// out-of-range port, and any literal-IP host that is loopback,
// link-local, or in the cloud metadata range (169.254.0.0/16) so a
// poisoned or self-registered registry entry cannot make the cell POST
// attacker-chosen JSON to those internal targets. DNS names are allowed
// through (resolution happens host-side in pulp.HTTP.Fetch); tightening to
// an explicit CIDR allow-list is the next step if the registry trust
// boundary weakens.
func safeWebhookTarget(host string, port int) error {
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("empty host")
	}
	if port <= 0 || port > 65535 {
		return fmt.Errorf("invalid port %d", port)
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("disallowed address %s", host)
		}
	}
	return nil
}

// MatcherConfig holds the matcher's runtime parameters.
type MatcherConfig struct {
	RegistryURL string
	TickRate    time.Duration
	RelayHost   string
	RelayPort   int
}

// matcherFetchTimeout matches native matcher.New's &http.Client{Timeout: 5 * time.Second}.
// Every outbound call from the matcher — registry reads, expect/match status updates,
// lobby webhook fan-out — runs under this budget so a slow or dead peer doesn't stall
// the tick and back up the queue behind it.
const matcherFetchTimeout = 5 * time.Second

// Matcher pairs queued players with ready matches and hands them off
// to the relevant game and lobby servers. Tick is driven from the
// cell step loop — no background goroutine (WASM cells cannot
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
// seconds regardless. Called from the cell's OnStep handler with
// the envelope's wall time.
//
// Native Bananasplit uses time.NewTicker, which fires for the first
// time AFTER the interval — not immediately. Mirror that here: on the
// very first step the wall-time baseline is recorded and no work runs.
// Subsequent steps run the work only once the interval has elapsed.
func (m *Matcher) TickIfDue(wallNanos uint64) {
	tickNanos := uint64(m.config.TickRate)
	if m.lastTick == 0 {
		m.lastTick = wallNanos
	} else if wallNanos-m.lastTick >= tickNanos {
		m.lastTick = wallNanos
		for _, mode := range m.queues.Modes() {
			m.tryMatch(mode)
		}
	}
	const cleanupNanos = 30 * uint64(time.Second)
	if m.lastCleanup == 0 {
		m.lastCleanup = wallNanos
	} else if wallNanos-m.lastCleanup >= cleanupNanos {
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

	fmt.Printf("[Matcher] Matched %d players for %s on %s/%s\n", len(players), mode, server.ID, matchID)

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
	resp, err := pulp.HTTP.Fetch(pulp.HTTPFetchRequest{Method: "GET", URL: url, Timeout: matcherFetchTimeout})
	if err != nil {
		fmt.Printf("[Matcher] Registry error: %v\n", err)
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
	resp, err := pulp.HTTP.Fetch(pulp.HTTPFetchRequest{Method: "GET", URL: url, Timeout: matcherFetchTimeout})
	if err != nil {
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
	if err := safeWebhookTarget(server.Host, server.WebhookPort); err != nil {
		fmt.Printf("[Matcher] Refusing expect to %s: %v\n", server.ID, err)
		return
	}
	url := fmt.Sprintf("http://%s:%d/expect", server.Host, server.WebhookPort)
	body, _ := json.Marshal(ExpectRequest{MatchID: matchID, UUIDs: uuids})
	resp, err := pulp.HTTP.Fetch(pulp.HTTPFetchRequest{
		Method:  "POST",
		URL:     url,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    body,
		Timeout: matcherFetchTimeout,
	})
	if err != nil {
		fmt.Printf("[Matcher] Failed to send expect to %s: %v\n", server.ID, err)
		return
	}
	_ = resp
	fmt.Printf("[Matcher] Sent expect to %s for match %s\n", server.ID, matchID)
}

func (m *Matcher) updateMatchStatus(serverID, matchID string, status MatchStatus, players []string) {
	url := fmt.Sprintf("%s/registry/servers/%s/matches/%s", m.config.RegistryURL, serverID, matchID)
	body, _ := json.Marshal(MatchInfo{Status: status, Need: len(players), Players: players})
	if _, err := pulp.HTTP.Fetch(pulp.HTTPFetchRequest{
		Method:  "PUT",
		URL:     url,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    body,
		Timeout: matcherFetchTimeout,
	}); err != nil {
		fmt.Printf("[Matcher] Failed to update match status: %v\n", err)
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
		resp, err := pulp.HTTP.Fetch(pulp.HTTPFetchRequest{Method: "GET", URL: lobbyURL, Timeout: matcherFetchTimeout})
		if err != nil {
			fmt.Printf("[Matcher] Failed to get lobby %s: %v\n", lobbyID, err)
			continue
		}
		var lobby ServerInfo
		if err := json.Unmarshal(resp.Body, &lobby); err != nil {
			fmt.Printf("[Matcher] Failed to decode lobby %s: %v\n", lobbyID, err)
			continue
		}

		if err := safeWebhookTarget(lobby.Host, lobby.WebhookPort); err != nil {
			fmt.Printf("[Matcher] Refusing notify to lobby %s: %v\n", lobbyID, err)
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
			Timeout: matcherFetchTimeout,
		}); err != nil {
			fmt.Printf("[Matcher] Failed to notify lobby %s: %v\n", lobbyID, err)
			continue
		}
		fmt.Printf("[Matcher] Notified lobby %s to transfer %d players to %s\n", lobbyID, len(uuids), backend)
	}
}
