package main

import (
	"fmt"
	"time"

	"github.com/bananalabs-oss/bananasplit/internal/matcher"
	"github.com/bananalabs-oss/bananasplit/internal/queue"
	"github.com/gin-gonic/gin"
)

func main() {
	// Create queue manager
	queues, err := queue.NewManager()
	if err != nil {
		return
	}

	// Create matcher
	m := matcher.New(matcher.Config{
		RegistryURL: "http://localhost:3000", // Bananagine
		TickRate:    500 * time.Millisecond,
	}, queues)

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
				UID    string `json:"uid"`
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
				// Put back in queue - need to know what mode
				// For now, skip requeue (would need mode in request)
				fmt.Printf("[Bananasplit] Player %s wants requeue (not implemented)\n", player.UID)
			} else {
				// Send to lobby
				if hasLobby {
					fmt.Printf("[Bananasplit] Player %s returning to lobby %s\n", player.UID, lobby.ID)
					// Would send transfer to game server here
				}
			}
		}

		c.JSON(200, gin.H{"status": "processed"})
	})

	fmt.Println("Bananasplit running on :3001")
	r.Run(":3001")
}
