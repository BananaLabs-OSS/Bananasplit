// Bananasplit — Pulp cell port.
//
// Matchmaking + player routing service. In-memory queues, player
// registry, and referral queue; outbound HTTP to Bananagine (server
// registry) and Peel (packet relay) via pulp.HTTP.Fetch. The matcher
// tick — originally a time.NewTicker goroutine — runs from the step
// loop using wall-time math. Same shape as Hytale-Auth's OAuth poll.
//
// Build:
//
//	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o bananasplit.wasm .
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/BananaLabs-OSS/Fiber/pulp"
	pulpgin "github.com/BananaLabs-OSS/Fiber/pulp/gin"
	"github.com/BananaLabs-OSS/Fiber/pulp/gin/middleware"
)

func main() {}

func init() {
	pulp.OnInit(bootstrap)
}

func bootstrap(configBytes []byte) error {
	cfg, err := parseConfig(configBytes)
	if err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	queues := NewQueueManager(cfg.QueueTimeout)
	registry := NewPlayerRegistry()
	referrals := NewReferralQueue()
	peel := NewPeelClient(cfg.PeelURL)

	matcher := NewMatcher(
		MatcherConfig{
			RegistryURL: cfg.BananagineURL,
			TickRate:    cfg.TickRate,
			RelayHost:   cfg.RelayHost,
			RelayPort:   cfg.RelayPort,
		},
		queues, registry, referrals, peel,
	)

	r := pulpgin.New()

	r.GET("/health", func(c *pulpgin.Context) {
		c.JSON(http.StatusOK, pulpgin.H{"status": "ok"})
	})

	// Auth posture: auth-available-not-mandatory. Every mutating /
	// state-bearing endpoint rides a root group gated on X-Service-Token
	// ONLY when SERVICE_TOKEN is configured (non-empty). When the token is
	// empty (today's default) the routes are registered WITHOUT the auth
	// middleware so existing callers — which send no X-Service-Token — keep
	// working: no 401, no outage, the cell still starts. To ENABLE auth,
	// set SERVICE_TOKEN here AND have the callers send the same value as the
	// X-Service-Token header, in lockstep. The empty group prefix keeps the
	// paths identical to native Bananasplit; only the auth middleware (when
	// a token is configured) is interposed. /health stays open. Mirrors
	// Peel's reconciled control-API posture (commit 80e9fe2) — deliberately
	// NOT fail-closed.
	var rg *pulpgin.RouterGroup
	if cfg.ServiceToken != "" {
		rg = r.Group("", middleware.ServiceAuth(cfg.ServiceToken))
	} else {
		rg = r.Group("")
	}

	rg.POST("/route-request", func(c *pulpgin.Context) {
		var req struct {
			PlayerIP string `json:"player_ip"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, pulpgin.H{"error": err.Error()})
			return
		}

		resp, err := pulp.HTTP.Fetch(pulp.HTTPFetchRequest{
			Method: "GET",
			URL:    cfg.BananagineURL + "/registry/servers?type=lobby",
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, pulpgin.H{"error": "failed to query registry"})
			return
		}
		var servers []ServerInfo
		if err := json.Unmarshal(resp.Body, &servers); err != nil {
			c.JSON(http.StatusInternalServerError, pulpgin.H{"error": "failed to decode registry response"})
			return
		}

		var target *ServerInfo
		for i := range servers {
			if servers[i].MaxPlayers == 0 || servers[i].Players < servers[i].MaxPlayers {
				target = &servers[i]
				break
			}
		}
		if target == nil {
			c.JSON(http.StatusServiceUnavailable, pulpgin.H{"error": "no lobbies available"})
			return
		}

		backend := fmt.Sprintf("%s:%d", target.Host, target.Port)
		registry.Register(req.PlayerIP, req.PlayerIP, target.ID)
		// Native /route-request fires the Peel SetRoute and ignores the
		// outcome — preserve that behavior here. PeelClient no-ops when
		// the URL is empty, so the conditional is implicit.
		_ = peel.SetRoute(req.PlayerIP, backend)

		c.JSON(http.StatusOK, pulpgin.H{"backend": backend, "server_id": target.ID})
	})

	rg.POST("/queue/join", func(c *pulpgin.Context) {
		var req struct {
			UUID        string `json:"uuid"`
			Mode        string `json:"mode"`
			LobbyServer string `json:"lobbyServer"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, pulpgin.H{"error": err.Error()})
			return
		}
		queues.Join(req.Mode, QueueEntry{UUID: req.UUID, LobbyServer: req.LobbyServer})
		c.JSON(http.StatusOK, pulpgin.H{
			"status":   "queued",
			"mode":     req.Mode,
			"position": queues.Size(req.Mode),
		})
	})

	rg.POST("/queue/leave", func(c *pulpgin.Context) {
		var req struct {
			UUID string `json:"uuid"`
			Mode string `json:"mode"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, pulpgin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, pulpgin.H{"removed": queues.Leave(req.Mode, req.UUID)})
	})

	rg.GET("/queue/:mode/size", func(c *pulpgin.Context) {
		mode := c.Param("mode")
		c.JSON(http.StatusOK, pulpgin.H{"mode": mode, "size": queues.Size(mode)})
	})

	rg.POST("/match-complete", func(c *pulpgin.Context) {
		var req struct {
			ServerID string `json:"serverId"`
			MatchID  string `json:"matchId"`
			Players  []struct {
				UUID   string `json:"uuid"`
				Action string `json:"action"`
			} `json:"players"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, pulpgin.H{"error": err.Error()})
			return
		}

		lobby, hasLobby := matcher.FindLobby()
		for _, player := range req.Players {
			if player.Action == "requeue" {
				fmt.Printf("[Bananasplit] Player %s wants requeue (not implemented)\n", player.UUID)
				continue
			}
			if !hasLobby {
				continue
			}
			// Native cmd/server/main.go:249 prints the "returning to
			// lobby" line unconditionally once a lobby is known — before
			// checking the player registry. Preserve that ordering so
			// log output matches byte-for-byte on players who are not in
			// the registry.
			fmt.Printf("[Bananasplit] Player %s returning to lobby %s\n", player.UUID, lobby.ID)
			playerInfo, ok := registry.GetByUUID(player.UUID)
			if !ok {
				continue
			}
			backend := fmt.Sprintf("%s:%d", lobby.Host, lobby.Port)
			if err := peel.SetRoute(playerInfo.IP, backend); err != nil {
				fmt.Printf("[Bananasplit] Failed to set route for %s: %v\n", player.UUID, err)
			}
			referrals.Add(req.ServerID, Referral{
				PlayerUUID: player.UUID,
				Host:       cfg.RelayHost,
				Port:       cfg.RelayPort,
			})
		}
		c.JSON(http.StatusOK, pulpgin.H{"status": "processed"})
	})

	rg.GET("/assign", func(c *pulpgin.Context) {
		ip := c.Query("ip")
		if ip == "" {
			c.JSON(http.StatusBadRequest, pulpgin.H{"error": "ip required"})
			return
		}
		lobby, found := matcher.FindLobby()
		if !found {
			c.JSON(http.StatusServiceUnavailable, pulpgin.H{"error": "no lobby available"})
			return
		}
		c.JSON(http.StatusOK, pulpgin.H{
			"backend": fmt.Sprintf("%s:%d", lobby.Host, lobby.Port),
		})
	})

	rg.POST("/players/register", func(c *pulpgin.Context) {
		var req struct {
			PlayerUUID string `json:"player_uuid"`
			PlayerIP   string `json:"player_ip"`
			ServerID   string `json:"server_id"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, pulpgin.H{"error": err.Error()})
			return
		}
		registry.Register(req.PlayerUUID, req.PlayerIP, req.ServerID)
		fmt.Printf("[Players] Registered %s on %s\n", req.PlayerUUID, req.ServerID)
		c.JSON(http.StatusOK, pulpgin.H{"status": "ok"})
	})

	rg.DELETE("/players/:uuid", func(c *pulpgin.Context) {
		uuid := c.Param("uuid")
		if p, ok := registry.GetByUUID(uuid); ok {
			_ = peel.DeleteRoute(p.IP)
		}
		registry.Remove(uuid)
		fmt.Printf("[Players] Removed %s\n", uuid)
		c.JSON(http.StatusOK, pulpgin.H{"status": "ok"})
	})

	rg.GET("/referrals", func(c *pulpgin.Context) {
		serverID := c.Query("server")
		if serverID == "" {
			c.JSON(http.StatusBadRequest, pulpgin.H{"error": "server required"})
			return
		}
		refs := referrals.GetAndClear(serverID)
		if refs == nil {
			refs = []Referral{}
		}
		c.JSON(http.StatusOK, refs)
	})

	// Register routes but install a composed OnStep so the matcher's
	// tick runs alongside HTTP dispatch.
	if err := r.RegisterRoutes(); err != nil {
		return fmt.Errorf("register routes: %w", err)
	}
	pulp.OnStep(func(ev pulp.StepEvent) error {
		matcher.TickIfDue(ev.WallTime)
		return r.Dispatch(ev)
	})

	fmt.Printf("Bananagine: %s\n", cfg.BananagineURL)
	fmt.Printf("Relay: %s:%d\n", cfg.RelayHost, cfg.RelayPort)
	fmt.Printf("Tick rate: %s\n", cfg.TickRate)
	if cfg.QueueTimeout > 0 {
		fmt.Printf("Queue timeout: %s\n", cfg.QueueTimeout)
	} else {
		fmt.Println("Queue timeout: disabled")
	}
	if cfg.PeelURL != "" {
		fmt.Printf("Peel: %s\n", cfg.PeelURL)
	} else {
		fmt.Println("Peel: disabled")
	}
	if cfg.ServiceToken != "" {
		fmt.Println("Service auth ENABLED (X-Service-Token required on all routes except /health)")
	} else {
		fmt.Println("Service auth OFF (SERVICE_TOKEN empty); to enable, set SERVICE_TOKEN here AND have callers send X-Service-Token")
	}
	fmt.Println("Matcher started")

	return nil
}

type config struct {
	BananagineURL string
	PeelURL       string
	RelayHost     string
	RelayPort     int
	TickRate      time.Duration
	QueueTimeout  time.Duration
	ServiceToken  string
}

func parseConfig(data []byte) (config, error) {
	var cfg config
	if len(data) == 0 {
		return cfg, fmt.Errorf("missing [config]")
	}
	var raw map[string]any
	if err := decodeMsgpack(data, &raw); err != nil {
		return cfg, err
	}
	jbytes, _ := json.Marshal(raw)
	var tmp struct {
		BananagineURL   string `json:"bananagine_url"`
		PeelURL         string `json:"peel_url"`
		RelayHost       string `json:"relay_host"`
		RelayPort       int    `json:"relay_port"`
		TickRateMs      int    `json:"tick_rate_ms"`
		QueueTimeoutSec int    `json:"queue_timeout_sec"`
		ServiceToken    string `json:"service_token"`
	}
	if err := json.Unmarshal(jbytes, &tmp); err != nil {
		return cfg, fmt.Errorf("decode config: %w", err)
	}
	cfg.BananagineURL = tmp.BananagineURL
	if cfg.BananagineURL == "" {
		cfg.BananagineURL = "http://localhost:3000"
	}
	cfg.PeelURL = tmp.PeelURL
	cfg.RelayHost = tmp.RelayHost
	if cfg.RelayHost == "" {
		cfg.RelayHost = "hycraft.net"
	}
	cfg.RelayPort = tmp.RelayPort
	if cfg.RelayPort == 0 {
		cfg.RelayPort = 5520
	}
	tick := tmp.TickRateMs
	if tick == 0 {
		tick = 500
	}
	cfg.TickRate = time.Duration(tick) * time.Millisecond
	// Native cmd/server/main.go:58 defaults queue timeout to 300s when
	// the flag/env/default chain resolves to 0. Mirror that: a config
	// value of 0 (or missing) means "use the 300s default", not
	// "disabled". Callers who genuinely want timeouts disabled must set
	// a negative value, which ResolveInt preserves on the native side
	// and which parseConfig now also preserves.
	qts := tmp.QueueTimeoutSec
	if qts == 0 {
		qts = 300
	}
	if qts < 0 {
		cfg.QueueTimeout = 0
	} else {
		cfg.QueueTimeout = time.Duration(qts) * time.Second
	}
	// SERVICE_TOKEN env (set by the Pulp host) wins over the manifest so
	// the secret stays out of the committed pulp.cell.toml. Auth is gated
	// on this token ONLY when non-empty; an empty token leaves all routes
	// unauthenticated (no outage) and the cell still starts. See bootstrap
	// for the auth posture.
	cfg.ServiceToken = tmp.ServiceToken
	if st := os.Getenv("SERVICE_TOKEN"); st != "" {
		cfg.ServiceToken = st
	}
	return cfg, nil
}
