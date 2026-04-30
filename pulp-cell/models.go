package main

import "time"

// ServerType and MatchStatus mirror the Potassium registry types. They
// are serialized verbatim across the Bananagine registry HTTP API.
type ServerType string

const (
	TypeLobby ServerType = "lobby"
	TypeGame  ServerType = "game"
)

type MatchStatus string

const (
	StatusReady    MatchStatus = "ready"
	StatusBusy     MatchStatus = "busy"
	StatusStarting MatchStatus = "starting"
)

type ServerInfo struct {
	ID          string     `json:"id"`
	Type        ServerType `json:"type"`
	Mode        string     `json:"mode"`
	Host        string     `json:"host"`
	Port        int        `json:"port"`
	WebhookPort int        `json:"webhookPort,omitempty"`

	Players    int `json:"players"`
	MaxPlayers int `json:"maxPlayers"`

	Matches map[string]MatchInfo `json:"matches"`

	// Metadata carries no JSON struct tag in the native
	// registry.ServerInfo, so it serializes under the Go field name
	// "Metadata" (capital M). Mirror that here; Bananasplit itself never
	// reads Metadata but the shape must round-trip identically.
	Metadata map[string]string
}

type MatchInfo struct {
	Status  MatchStatus `json:"status"`
	Need    int         `json:"need"`
	Players []string    `json:"players"`
}

// QueueEntry represents a player waiting in a specific mode queue.
type QueueEntry struct {
	UUID        string    `json:"uuid"`
	LobbyServer string    `json:"lobbyServer"`
	JoinedAt    time.Time `json:"joinedAt"`
}

// Player is an in-memory record of a connected player — UUID + IP +
// which server they are currently on.
type Player struct {
	UUID     string `json:"uuid"`
	IP       string `json:"ip"`
	ServerID string `json:"server_id"`
}

// Referral queues up server-relay mappings so origin servers can poll
// and transfer players.
type Referral struct {
	PlayerUUID string `json:"player_uuid"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
}

// Request / response shapes used by the matcher's outbound calls.

type ExpectRequest struct {
	MatchID string   `json:"matchId"`
	UUIDs   []string `json:"uuids"`
}

type MatchReadyRequest struct {
	MatchID    string   `json:"matchId"`
	Mode       string   `json:"mode"`
	Players    []string `json:"players"`
	GameServer string   `json:"gameServer"`
}
