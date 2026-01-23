package matcher

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/bananalabs-oss/bananasplit/internal/peel"
	"github.com/bananalabs-oss/bananasplit/internal/players"
	"github.com/bananalabs-oss/bananasplit/internal/queue"
	"github.com/bananalabs-oss/bananasplit/internal/referrals"
	"github.com/bananalabs-oss/potassium/registry"
)

// Config holds matcher configuration
type Config struct {
	RegistryURL string // Bananagine registry URL
	TickRate    time.Duration

	RelayHost string
	RelayPort int
}

// Matcher checks queues and assigns players to servers
type Matcher struct {
	config Config
	queues *queue.Manager
	client *http.Client

	players   *players.Registry
	referrals *referrals.Queue
	peel      *peel.Client
}

// TransferRequest is sent to lobby servers
type TransferRequest struct {
	UUID    string                 `json:"uuid"`
	Target  string                 `json:"target"` // host:port
	Payload map[string]interface{} `json:"payload"`
}

// ExpectRequest is sent to game servers
type ExpectRequest struct {
	MatchID string   `json:"matchId"`
	UUIDs   []string `json:"uuids"`
}

// New creates a new matcher
func New(
	config Config,
	queues *queue.Manager,
	playerRegistry *players.Registry,
	referralQueue *referrals.Queue,
	peelClient *peel.Client) *Matcher {
	return &Matcher{
		config:    config,
		queues:    queues,
		players:   playerRegistry,
		referrals: referralQueue,
		peel:      peelClient,
		client:    &http.Client{Timeout: 5 * time.Second},
	}
}

// Start begins the matching loop
func (m *Matcher) Start() {
	ticker := time.NewTicker(m.config.TickRate)
	go func() {
		for range ticker.C {
			m.tick()
		}
	}()
}

// tick runs one matching cycle
func (m *Matcher) tick() {
	modes := m.queues.Modes()

	for _, mode := range modes {
		m.tryMatch(mode)
	}
}

// tryMatch attempts to match players for a game mode
func (m *Matcher) tryMatch(mode string) {
	// Find a ready server/match
	server, matchID, found := m.findReadyMatch(mode)
	if !found {
		return
	}

	// Check queue size
	match := server.Matches[matchID]
	needed := match.Need
	queueSize := m.queues.Size(mode)

	if queueSize < needed {
		return
	}

	// Pop players from queue
	players := m.queues.Pop(mode, needed)
	if players == nil {
		return
	}

	fmt.Printf("[Matcher] Matched %d players for %s on %s/%s\n", len(players), mode, server.ID, matchID)

	// Collect UIDs
	uuids := make([]string, len(players))
	for i, p := range players {
		uuids[i] = p.UUID
	}

	// Tell game server to expect players
	m.sendExpect(server, matchID, uuids)

	// Queue referrals for plugin polling
	backend := fmt.Sprintf("%s:%d", server.Host, server.Port)
	for _, p := range players {
		m.queueReferral(p.UUID, backend)
	}

	// Update match status to busy
	m.updateMatchStatus(server.ID, matchID, registry.StatusBusy, uuids)
}

// findReadyMatch queries registry for a ready match
func (m *Matcher) findReadyMatch(mode string) (registry.ServerInfo, string, bool) {
	url := fmt.Sprintf("%s/registry/servers?type=game&mode=%s&hasReadyMatch=true", m.config.RegistryURL, mode)

	resp, err := m.client.Get(url)
	if err != nil {
		fmt.Printf("[Matcher] Registry error: %v\n", err)
		return registry.ServerInfo{}, "", false
	}
	defer resp.Body.Close()

	var servers []registry.ServerInfo
	if err := json.NewDecoder(resp.Body).Decode(&servers); err != nil {
		return registry.ServerInfo{}, "", false
	}

	// Find first server with a ready match
	for _, server := range servers {
		for matchID, match := range server.Matches {
			if match.Status == "ready" {
				return server, matchID, true
			}
		}
	}

	return registry.ServerInfo{}, "", false
}

func (m *Matcher) queueReferral(playerUUID string, backend string) {
	player, found := m.players.GetByUUID(playerUUID)
	if !found {
		fmt.Printf("[Matcher] Player %s not in registry\n", playerUUID)
		return
	}

	// Update Peel route (if enabled)
	if m.peel != nil {
		if err := m.peel.SetRoute(player.IP, backend); err != nil {
			fmt.Printf("[Matcher] Peel error for %s: %v\n", playerUUID, err)
		}
	}

	// Determine referral target
	host := m.config.RelayHost
	port := m.config.RelayPort

	// Queue referral for origin server to poll
	m.referrals.Add(player.ServerID, referrals.Referral{
		PlayerUUID: playerUUID,
		Host:       host,
		Port:       port,
	})

	fmt.Printf("[Matcher] Queued referral: %s on %s → %s\n", playerUUID, player.ServerID, backend)
}

// sendExpect tells game server to expect players
func (m *Matcher) sendExpect(server registry.ServerInfo, matchID string, uuids []string) {
	url := fmt.Sprintf("http://%s:%d/expect", server.Host, server.Port)

	req := ExpectRequest{
		MatchID: matchID,
		UUIDs:   uuids,
	}

	body, _ := json.Marshal(req)
	resp, err := m.client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Printf("[Matcher] Failed to send expect to %s: %v\n", server.ID, err)
		return
	}
	defer resp.Body.Close()

	fmt.Printf("[Matcher] Sent expect to %s for match %s\n", server.ID, matchID)
}

// updateMatchStatus updates match in registry
func (m *Matcher) updateMatchStatus(serverID string, matchID string, status registry.MatchStatus, players []string) {
	url := fmt.Sprintf("%s/registry/servers/%s/matches/%s", m.config.RegistryURL, serverID, matchID)

	match := registry.MatchInfo{
		Status:  status,
		Need:    len(players),
		Players: players,
	}

	body, _ := json.Marshal(match)
	req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		fmt.Printf("[Matcher] Failed to update match status: %v\n", err)
		return
	}
	defer resp.Body.Close()
}

// FindLobby finds a lobby with capacity
func (m *Matcher) FindLobby() (registry.ServerInfo, bool) {
	url := fmt.Sprintf("%s/registry/servers?type=lobby&hasCapacity=true", m.config.RegistryURL)

	resp, err := m.client.Get(url)
	if err != nil {
		return registry.ServerInfo{}, false
	}
	defer resp.Body.Close()

	var servers []registry.ServerInfo
	if err := json.NewDecoder(resp.Body).Decode(&servers); err != nil {
		return registry.ServerInfo{}, false
	}

	if len(servers) > 0 {
		return servers[0], true
	}
	return registry.ServerInfo{}, false
}
