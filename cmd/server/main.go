package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/bananalabs-oss/bananasplit/internal/matcher"
	"github.com/bananalabs-oss/bananasplit/internal/players"
	"github.com/bananalabs-oss/bananasplit/internal/queue"
	"github.com/bananalabs-oss/bananasplit/internal/referrals"
	"github.com/bananalabs-oss/potassium/registry"
	"github.com/bananalabs-oss/potassium/relay"
	"github.com/gin-gonic/gin"
)

type RouteRequest struct {
	PlayerIP string `json:"player_ip"`
}

type RouteResponse struct {
	Backend  string `json:"backend"`
	ServerID string `json:"server_id"`
}

func main() {
	// CLI flags
	peelURL := flag.String("peel", "", "Peel URL (optional)")
	bananagineURL := flag.String("bananagine", "", "Bananagine URL (default http://localhost:3000)")
	relayHost := flag.String("relay-host", "", "Relay host for referrals (default hycraft.net)")
	relayPort := flag.Int("relay-port", 0, "Relay port for referrals (default 5520)")
	listenAddr := flag.String("listen", "", "Listen address (default :3000)")
	tickRate := flag.Int("tick", 0, "Matcher tick rate in ms (default 500)")
	queueTimeout := flag.Int("queue-timeout", 0, "Queue timeout in seconds, 0 = disabled (default 300)")
	flag.Parse()

	// Resolve: CLI > Env > Default
	config := struct {
		PeelURL       string
		BananagineURL string
		RelayHost     string
		RelayPort     int
		ListenAddr    string
		TickRate      time.Duration
		QueueTimeout  time.Duration
	}{
		PeelURL:       resolve(*peelURL, getEnv("PEEL_URL", ""), ""),
		BananagineURL: resolve(*bananagineURL, getEnv("BANANAGINE_URL", ""), "http://localhost:3000"),
		RelayHost:     resolve(*relayHost, getEnv("RELAY_HOST", ""), "hycraft.net"),
		RelayPort:     resolveInt(*relayPort, getEnvInt("RELAY_PORT", 0), 5520),
		ListenAddr:    resolve(*listenAddr, getEnv("LISTEN_ADDR", ""), ":3000"),
		TickRate:      time.Duration(resolveInt(*tickRate, getEnvInt("TICK_RATE", 0), 500)) * time.Millisecond,
		QueueTimeout:  time.Duration(resolveInt(*queueTimeout, getEnvInt("QUEUE_TIMEOUT", 0), 300)) * time.Second,
	}

	// Log config
	fmt.Printf("Listen: %s\n", config.ListenAddr)
	fmt.Printf("Bananagine: %s\n", config.BananagineURL)
	fmt.Printf("Relay: %s:%d\n", config.RelayHost, config.RelayPort)
	fmt.Printf("Tick rate: %s\n", config.TickRate)
	if config.QueueTimeout > 0 {
		fmt.Printf("Queue timeout: %s\n", config.QueueTimeout)
	} else {
		fmt.Println("Queue timeout: disabled")
	}
	if config.PeelURL != "" {
		fmt.Printf("Peel: %s\n", config.PeelURL)
	} else {
		fmt.Println("Peel: disabled")
	}

	// Create queue manager
	queues, err := queue.NewManager(config.QueueTimeout)
	if err != nil {
		return
	}

	// Create player registry and referral queue
	playerRegistry := players.NewRegistry()
	referralQueue := referrals.NewQueue()

	// Create peel client (optional)
	var peelClient *relay.Client
	if config.PeelURL != "" {
		peelClient = relay.NewClient(config.PeelURL)
	}

	// Create matcher
	m := matcher.New(
		matcher.Config{
			RegistryURL: config.BananagineURL,
			TickRate:    config.TickRate,
			RelayHost:   config.RelayHost,
			RelayPort:   config.RelayPort,
		},
		queues,
		playerRegistry,
		referralQueue,
		peelClient,
	)

	// Start matching loop
	m.Start()
	fmt.Println("Matcher started")

	// HTTP server
	r := gin.Default()

	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	r.POST("/route-request", func(c *gin.Context) {
		var req struct {
			PlayerIP string `json:"player_ip"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}

		// Find lobby with capacity
		resp, err := http.Get(config.BananagineURL + "/registry/servers?type=lobby")
		if err != nil {
			c.JSON(500, gin.H{"error": "failed to query registry"})
			return
		}
		defer resp.Body.Close()

		var servers []registry.ServerInfo
		json.NewDecoder(resp.Body).Decode(&servers)

		var target *registry.ServerInfo

		for i := range servers {
			if servers[i].MaxPlayers == 0 || servers[i].Players < servers[i].MaxPlayers {
				target = &servers[i]
				break
			}
		}

		if target == nil {
			c.JSON(503, gin.H{"error": "no lobbies available"})
			return
		}

		backend := fmt.Sprintf("%s:%d", target.Host, target.Port)

		// Register player with existing registry
		playerRegistry.Register(req.PlayerIP, req.PlayerIP, target.ID)

		// Set Peel route
		if config.PeelURL != "" {
			routeBody, _ := json.Marshal(map[string]string{
				"player_ip": req.PlayerIP,
				"backend":   backend,
			})
			http.Post(config.PeelURL+"/routes", "application/json", bytes.NewReader(routeBody))
		}

		c.JSON(200, gin.H{
			"backend":   backend,
			"server_id": target.ID,
		})
	})

	// Join queue
	r.POST("/queue/join", func(c *gin.Context) {
		var req struct {
			UUID        string `json:"uuid"`
			Mode        string `json:"mode"`
			LobbyServer string `json:"lobbyServer"`
		}

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}

		queues.Join(req.Mode, queue.QueueEntry{
			UUID:        req.UUID,
			LobbyServer: req.LobbyServer,
		})

		c.JSON(200, gin.H{
			"status":   "queued",
			"mode":     req.Mode,
			"position": queues.Size(req.Mode),
		})
	})

	// Leave queue
	r.POST("/queue/leave", func(c *gin.Context) {
		var req struct {
			UUID string `json:"uuid"`
			Mode string `json:"mode"`
		}

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}

		removed := queues.Leave(req.Mode, req.UUID)
		c.JSON(200, gin.H{"removed": removed})
	})

	// Queue size
	r.GET("/queue/:mode/size", func(c *gin.Context) {
		mode := c.Param("mode")
		size := queues.Size(mode)
		c.JSON(200, gin.H{"mode": mode, "size": size})
	})

	// Match complete (game server reports back)
	r.POST("/match-complete", func(c *gin.Context) {
		var req struct {
			ServerID string `json:"serverId"`
			MatchID  string `json:"matchId"`
			Players  []struct {
				UUID   string `json:"uuid"`
				Action string `json:"action"` // "requeue" or "lobby"
			} `json:"players"`
		}

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}

		// Find a lobby for players going back
		lobby, hasLobby := m.FindLobby()

		for _, player := range req.Players {
			if player.Action == "requeue" {
				fmt.Printf("[Bananasplit] Player %s wants requeue (not implemented)\n", player.UUID)
			} else {
				if hasLobby {
					fmt.Printf("[Bananasplit] Player %s returning to lobby %s\n", player.UUID, lobby.ID)

					playerInfo, found := playerRegistry.GetByUUID(player.UUID)
					if found {
						backend := fmt.Sprintf("%s:%d", lobby.Host, lobby.Port)
						if err := peelClient.SetRoute(playerInfo.IP, backend); err != nil {
							fmt.Printf("[Bananasplit] Failed to set route for %s: %v\n", player.UUID, err)
						}

						referralQueue.Add(req.ServerID, referrals.Referral{
							PlayerUUID: player.UUID,
							Host:       config.RelayHost,
							Port:       config.RelayPort,
						})
					}
				}
			}
		}

		c.JSON(200, gin.H{"status": "processed"})
	})

	// Endpoint for "Peel Relay"
	r.GET("/assign", func(c *gin.Context) {
		ip := c.Query("ip")
		if ip == "" {
			c.JSON(400, gin.H{"error": "ip required"})
			return
		}

		lobby, found := m.FindLobby()
		if !found {
			c.JSON(503, gin.H{"error": "no lobby available"})
			return
		}

		backend := fmt.Sprintf("%s:%d", lobby.Host, lobby.Port)
		c.JSON(200, gin.H{"backend": backend})
	})

	// Endpoints for plugins
	r.POST("/players/register", func(c *gin.Context) {
		var req struct {
			PlayerUUID string `json:"player_uuid"`
			PlayerIP   string `json:"player_ip"`
			ServerID   string `json:"server_id"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}

		playerRegistry.Register(req.PlayerUUID, req.PlayerIP, req.ServerID)
		fmt.Printf("[Players] Registered %s on %s\n", req.PlayerUUID, req.ServerID)
		c.JSON(200, gin.H{"status": "ok"})
	})

	r.DELETE("/players/:uuid", func(c *gin.Context) {
		uuid := c.Param("uuid")

		player, found := playerRegistry.GetByUUID(uuid)
		if found && peelClient != nil {
			peelClient.DeleteRoute(player.IP)
		}

		playerRegistry.Remove(uuid)
		fmt.Printf("[Players] Removed %s\n", uuid)
		c.JSON(200, gin.H{"status": "ok"})
	})

	r.GET("/referrals", func(c *gin.Context) {
		serverID := c.Query("server")
		if serverID == "" {
			c.JSON(400, gin.H{"error": "server required"})
			return
		}

		refs := referralQueue.GetAndClear(serverID)
		if refs == nil {
			refs = []referrals.Referral{}
		}
		c.JSON(200, refs)
	})

	fmt.Printf("Bananasplit running on %s\n", config.ListenAddr)
	r.Run(config.ListenAddr)
}

// resolve returns first non-empty value: cli > env > fallback
func resolve(cli, env, fallback string) string {
	if cli != "" {
		return cli
	}
	if env != "" {
		return env
	}
	return fallback
}

func resolveInt(cli, env, fallback int) int {
	if cli != 0 {
		return cli
	}
	if env != 0 {
		return env
	}
	return fallback
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if val := os.Getenv(key); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return fallback
}
