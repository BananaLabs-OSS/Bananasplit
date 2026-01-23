package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/bananalabs-oss/bananasplit/internal/matcher"
	"github.com/bananalabs-oss/bananasplit/internal/players"
	"github.com/bananalabs-oss/bananasplit/internal/queue"
	"github.com/bananalabs-oss/bananasplit/internal/referrals"
	"github.com/bananalabs-oss/potassium/peel"
	"github.com/gin-gonic/gin"
)

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if value := os.Getenv(key); value != "" {
		if i, err := strconv.Atoi(value); err == nil {
			return i
		}
	}
	return fallback
}

func main() {
	// Config from environment
	peelURL := getEnv("PEEL_URL", "")
	bananagineURL := getEnv("BANANAGINE_URL", "http://localhost:3000")
	relayHost := getEnv("RELAY_HOST", "hycraft.net")
	relayPort := getEnvInt("RELAY_PORT", 5520)
	listenAddr := getEnv("LISTEN_ADDR", ":3001")

	// Create queue manager
	queues, err := queue.NewManager()
	if err != nil {
		return
	}

	// Create player registry and referral queue
	playerRegistry := players.NewRegistry()
	referralQueue := referrals.NewQueue()

	// Create peel client (optional)
	var peelClient *peel.Client
	if peelURL != "" {
		peelClient = peel.NewClient(peelURL)
		fmt.Printf("Peel enabled: %s\n", peelURL)
	} else {
		fmt.Println("Peel disabled (no PEEL_URL)")
	}

	// Create matcher
	m := matcher.New(
		matcher.Config{
			RegistryURL: bananagineURL,
			TickRate:    500 * time.Millisecond,
			RelayHost:   relayHost,
			RelayPort:   relayPort,
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

	// Join queue
	r.POST("/queue/join", func(c *gin.Context) {
		var req struct {
			UID         string `json:"uid"`
			Mode        string `json:"mode"`
			LobbyServer string `json:"lobbyServer"`
		}

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}

		queues.Join(req.Mode, queue.QueueEntry{
			UUID:        req.UID,
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
			UID  string `json:"uid"`
			Mode string `json:"mode"`
		}

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}

		removed := queues.Leave(req.Mode, req.UID)
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
				UUID   string `json:"uid"`
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
							Host:       relayHost,
							Port:       relayPort,
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

	fmt.Printf("Bananasplit running on %s\n", listenAddr)
	r.Run(listenAddr)
}
