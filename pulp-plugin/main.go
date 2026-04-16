// Bananasplit — Pulp plugin port.
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
	"time"

	"github.com/BananaLabs-OSS/Fiber/pulp"
	pulpgin "github.com/BananaLabs-OSS/Fiber/pulp/gin"
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

	r.POST("/route-request", func(c *pulpgin.Context) {
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
		if err != nil || resp.Status != 200 {
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
		_ = peel.SetRoute(req.PlayerIP, backend)

		c.JSON(http.StatusOK, pulpgin.H{"backend": backend, "server_id": target.ID})
	})

	r.POST("/queue/join", func(c *pulpgin.Context) {
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

	r.POST("/queue/leave", func(c *pulpgin.Context) {
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

	r.GET("/queue/:mode/size", func(c *pulpgin.Context) {
		mode := c.Param("mode")
		c.JSON(http.StatusOK, pulpgin.H{"mode": mode, "size": queues.Size(mode)})
	})

	r.POST("/match-complete", func(c *pulpgin.Context) {
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
				fmt.Printf("[bananasplit] player %s wants requeue (not implemented)\n", player.UUID)
				continue
			}
			if !hasLobby {
				continue
			}
			playerInfo, ok := registry.GetByUUID(player.UUID)
			if !ok {
				continue
			}
			backend := fmt.Sprintf("%s:%d", lobby.Host, lobby.Port)
			if err := peel.SetRoute(playerInfo.IP, backend); err != nil {
				fmt.Printf("[bananasplit] set route for %s: %v\n", player.UUID, err)
			}
			referrals.Add(req.ServerID, Referral{
				PlayerUUID: player.UUID,
				Host:       cfg.RelayHost,
				Port:       cfg.RelayPort,
			})
		}
		c.JSON(http.StatusOK, pulpgin.H{"status": "processed"})
	})

	r.GET("/assign", func(c *pulpgin.Context) {
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

	r.POST("/players/register", func(c *pulpgin.Context) {
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
		c.JSON(http.StatusOK, pulpgin.H{"status": "ok"})
	})

	r.DELETE("/players/:uuid", func(c *pulpgin.Context) {
		uuid := c.Param("uuid")
		if p, ok := registry.GetByUUID(uuid); ok {
			_ = peel.DeleteRoute(p.IP)
		}
		registry.Remove(uuid)
		c.JSON(http.StatusOK, pulpgin.H{"status": "ok"})
	})

	r.GET("/referrals", func(c *pulpgin.Context) {
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

	fmt.Printf("[bananasplit] ready — bananagine=%s peel=%s tick=%s timeout=%s\n",
		cfg.BananagineURL, cfg.PeelURL, cfg.TickRate, cfg.QueueTimeout)

	return nil
}

type config struct {
	BananagineURL string
	PeelURL       string
	RelayHost     string
	RelayPort     int
	TickRate      time.Duration
	QueueTimeout  time.Duration
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
	cfg.QueueTimeout = time.Duration(tmp.QueueTimeoutSec) * time.Second
	return cfg, nil
}
