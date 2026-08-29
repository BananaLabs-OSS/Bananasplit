package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BananaLabs-OSS/Fiber/pulp"
	"github.com/BananaLabs-OSS/Fiber/pulp/cellconfig"
	pulpgin "github.com/BananaLabs-OSS/Fiber/pulp/gin"
	"github.com/BananaLabs-OSS/Fiber/pulp/gin/middleware"
	"github.com/BananaLabs-OSS/Fiber/pulp/workflow"
)

const orchestratorCell = "lua-orchestrator"

type config struct {
	TickRate     time.Duration
	QueueTimeout time.Duration
	RelayHost    string
	RelayPort    int
	ServiceToken string
}

type referral struct {
	PlayerUUID string `json:"player_uuid" msgpack:"player_uuid"`
	Host       string `json:"host" msgpack:"host"`
	Port       int    `json:"port" msgpack:"port"`
}

type referralResult struct {
	Items []referral `msgpack:"items"`
	Empty bool       `msgpack:"empty"`
}

func main() {}

func init() { pulp.OnInit(bootstrap) }

func parseConfig(data []byte) (config, error) {
	if len(data) == 0 {
		return config{}, fmt.Errorf("missing [config]")
	}
	var raw struct {
		TickRateMS      int    `json:"tick_rate_ms"`
		QueueTimeoutSec int    `json:"queue_timeout_sec"`
		RelayHost       string `json:"relay_host"`
		RelayPort       int    `json:"relay_port"`
		ServiceToken    string `json:"service_token"`
	}
	if err := cellconfig.Decode(data, &raw); err != nil {
		return config{}, err
	}
	if raw.TickRateMS == 0 {
		raw.TickRateMS = 500
	}
	if raw.QueueTimeoutSec == 0 {
		raw.QueueTimeoutSec = 300
	}
	if raw.RelayHost == "" {
		raw.RelayHost = "hycraft.net"
	}
	if raw.RelayPort == 0 {
		raw.RelayPort = 5520
	}
	if token := os.Getenv("SERVICE_TOKEN"); token != "" {
		raw.ServiceToken = token
	}
	timeout := time.Duration(raw.QueueTimeoutSec) * time.Second
	if raw.QueueTimeoutSec < 0 {
		timeout = 0
	}
	return config{
		TickRate:     time.Duration(raw.TickRateMS) * time.Millisecond,
		QueueTimeout: timeout, RelayHost: raw.RelayHost,
		RelayPort: raw.RelayPort, ServiceToken: raw.ServiceToken,
	}, nil
}

func bootstrap(configBytes []byte) error {
	cfg, err := parseConfig(configBytes)
	if err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	client := workflow.NewClient(orchestratorCell)
	engine := pulpgin.New()
	registerRoutes(engine, client, cfg)
	if err := engine.RegisterRoutes(); err != nil {
		return err
	}
	pulp.OnStep(func(event pulp.StepEvent) error {
		now := time.Unix(0, int64(event.WallTime)).UTC().Format(time.RFC3339Nano)
		_, err := client.Dispatch(workflow.DispatchRequest{
			Event: "bananasplit.tick.v1",
			Payload: map[string]any{
				"wall_nanos":            fmt.Sprint(event.WallTime),
				"tick_nanos":            fmt.Sprint(uint64(cfg.TickRate)),
				"cleanup_every_nanos":   fmt.Sprint(uint64(30 * time.Second)),
				"queue_timeout_seconds": int64(cfg.QueueTimeout / time.Second),
				"now":                   now,
			},
		})
		if err != nil {
			log.Printf("Matcher tick failed: %v", err)
		}
		return engine.Dispatch(event)
	})
	return nil
}

func commandID(c *pulpgin.Context, operation string) string {
	if key := c.GetHeader("Idempotency-Key"); key != "" {
		return operation + ":" + key
	}
	return operation + ":http-" + strconv.FormatUint(c.Request().ID, 10)
}

func dispatch[T any](client *workflow.Client, event string, payload any) (T, error) {
	result, err := client.Dispatch(workflow.DispatchRequest{Event: event, Payload: payload})
	if err != nil {
		var zero T
		return zero, err
	}
	return workflow.DecodeValue[T](result)
}

func httpStatus(result map[string]any) int {
	switch status := result["http_status"].(type) {
	case int8:
		return int(status)
	case int64:
		return int(status)
	case uint64:
		return int(status)
	case float64:
		return int(status)
	default:
		return http.StatusOK
	}
}

func respondMap(c *pulpgin.Context, result map[string]any) {
	status := httpStatus(result)
	delete(result, "http_status")
	c.JSON(status, result)
}

func registerRoutes(engine *pulpgin.Engine, client *workflow.Client, cfg config) {
	engine.GET("/health", func(c *pulpgin.Context) {
		result, err := dispatch[map[string]any](client, "bananasplit.http.health.v1", map[string]any{})
		if err != nil {
			c.JSON(500, pulpgin.H{"error": err.Error()})
			return
		}
		respondMap(c, result)
	})

	var routes *pulpgin.RouterGroup
	if cfg.ServiceToken != "" {
		routes = engine.Group("", middleware.ServiceAuth(cfg.ServiceToken))
	} else {
		routes = engine.Group("")
	}

	routes.POST("/queue/join", func(c *pulpgin.Context) {
		var request struct {
			UUID        string `json:"uuid"`
			Mode        string `json:"mode"`
			LobbyServer string `json:"lobbyServer"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(400, pulpgin.H{"error": err.Error()})
			return
		}
		result, err := dispatch[map[string]any](client, "bananasplit.http.queue.join.v1", map[string]any{
			"id": commandID(c, "queue-join"), "uuid": request.UUID,
			"mode": request.Mode, "lobby_server": request.LobbyServer,
			"joined_at": time.Now().UTC().Format(time.RFC3339Nano),
		})
		if err != nil {
			c.JSON(500, pulpgin.H{"error": err.Error()})
			return
		}
		respondMap(c, result)
	})

	routes.POST("/queue/leave", func(c *pulpgin.Context) {
		var request struct {
			UUID string `json:"uuid"`
			Mode string `json:"mode"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(400, pulpgin.H{"error": err.Error()})
			return
		}
		result, err := dispatch[map[string]any](client, "bananasplit.http.queue.leave.v1", map[string]any{
			"id": commandID(c, "queue-leave"), "uuid": request.UUID, "mode": request.Mode,
		})
		if err != nil {
			c.JSON(500, pulpgin.H{"error": err.Error()})
			return
		}
		respondMap(c, result)
	})

	routes.GET("/queue/:mode/size", func(c *pulpgin.Context) {
		result, err := dispatch[map[string]any](client, "bananasplit.http.queue.size.v1", map[string]any{
			"mode": c.Param("mode"),
		})
		if err != nil {
			c.JSON(500, pulpgin.H{"error": err.Error()})
			return
		}
		respondMap(c, result)
	})

	routes.POST("/route-request", func(c *pulpgin.Context) {
		var request struct {
			PlayerIP string `json:"player_ip"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(400, pulpgin.H{"error": err.Error()})
			return
		}
		result, err := dispatch[map[string]any](client, "bananasplit.http.route-request.v1", map[string]any{
			"id": commandID(c, "route-request"), "player_ip": request.PlayerIP,
		})
		if err != nil {
			c.JSON(500, pulpgin.H{"error": "failed to query registry"})
			return
		}
		respondMap(c, result)
	})

	routes.GET("/assign", func(c *pulpgin.Context) {
		if c.Query("ip") == "" {
			c.JSON(400, pulpgin.H{"error": "ip required"})
			return
		}
		result, err := dispatch[map[string]any](client, "bananasplit.http.assign.v1", map[string]any{})
		if err != nil {
			c.JSON(500, pulpgin.H{"error": err.Error()})
			return
		}
		respondMap(c, result)
	})

	routes.POST("/join-leases", func(c *pulpgin.Context) {
		var request struct {
			PrincipalID           string `json:"principal_id"`
			DeviceID              string `json:"device_id"`
			DestinationID         string `json:"destination_id"`
			FallbackDestinationID string `json:"fallback_destination_id"`
			TTLSeconds            int64  `json:"ttl_seconds"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(400, pulpgin.H{"error": "invalid join lease request"})
			return
		}
		if request.PrincipalID == "" || request.DeviceID == "" || request.DestinationID == "" {
			c.JSON(400, pulpgin.H{"error": "principal_id, device_id, and destination_id required"})
			return
		}
		if request.TTLSeconds <= 0 || request.TTLSeconds > 300 {
			request.TTLSeconds = 60
		}
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			c.JSON(500, pulpgin.H{"error": "join lease generation failed"})
			return
		}
		token := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret))
		digest := sha256.Sum256([]byte(token))
		now := time.Now().Unix()
		leaseID := commandID(c, "join-lease")
		result, err := dispatch[map[string]any](client, "bananasplit.http.join-lease.issue.v1", map[string]any{
			"id": leaseID, "lease_id": leaseID,
			"token_digest": hex.EncodeToString(digest[:]),
			"principal_id": request.PrincipalID, "device_id": request.DeviceID,
			"destination_id":          request.DestinationID,
			"fallback_destination_id": request.FallbackDestinationID,
			"expires_at":              now + request.TTLSeconds,
		})
		if err != nil {
			c.JSON(500, pulpgin.H{"error": "join lease persistence failed"})
			return
		}
		result["token"] = token
		c.Header("Cache-Control", "no-store")
		respondMap(c, result)
	})

	routes.POST("/connections/resolve", func(c *pulpgin.Context) {
		var request struct {
			ConnectionID   string `json:"connection_id"`
			LeaseToken     string `json:"lease_token"`
			Transport      string `json:"transport"`
			SourceEndpoint string `json:"source_endpoint"`
		}
		if err := c.ShouldBindJSON(&request); err != nil || request.ConnectionID == "" || request.LeaseToken == "" {
			c.JSON(400, pulpgin.H{"error": "connection_id and lease_token required"})
			return
		}
		digest := sha256.Sum256([]byte(request.LeaseToken))
		result, err := dispatch[map[string]any](client, "bananasplit.http.connection.resolve.v1", map[string]any{
			"id": commandID(c, "connection-resolve"), "connection_id": request.ConnectionID,
			"token_digest": hex.EncodeToString(digest[:]), "transport": request.Transport,
			"source_endpoint": request.SourceEndpoint, "now": time.Now().Unix(),
		})
		if err != nil {
			c.JSON(500, pulpgin.H{"error": "connection resolution failed"})
			return
		}
		respondMap(c, result)
	})

	routes.GET("/connections/:id", func(c *pulpgin.Context) {
		result, err := dispatch[map[string]any](client, "bananasplit.http.connection.get.v1", map[string]any{
			"connection_id": c.Param("id"),
		})
		if err != nil {
			c.JSON(500, pulpgin.H{"error": "connection lookup failed"})
			return
		}
		respondMap(c, result)
	})

	routes.POST("/players/register", func(c *pulpgin.Context) {
		var request struct {
			PlayerUUID string `json:"player_uuid"`
			PlayerIP   string `json:"player_ip"`
			ServerID   string `json:"server_id"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(400, pulpgin.H{"error": err.Error()})
			return
		}
		result, err := dispatch[map[string]any](client, "bananasplit.http.player.register.v1", map[string]any{
			"id": commandID(c, "player-register"), "player_uuid": request.PlayerUUID,
			"player_ip": request.PlayerIP, "server_id": request.ServerID,
		})
		if err != nil {
			c.JSON(500, pulpgin.H{"error": err.Error()})
			return
		}
		respondMap(c, result)
	})

	routes.DELETE("/players/:uuid", func(c *pulpgin.Context) {
		result, err := dispatch[map[string]any](client, "bananasplit.http.player.remove.v1", map[string]any{
			"id": commandID(c, "player-remove"), "uuid": c.Param("uuid"),
		})
		if err != nil {
			c.JSON(500, pulpgin.H{"error": err.Error()})
			return
		}
		respondMap(c, result)
	})

	routes.GET("/referrals", func(c *pulpgin.Context) {
		serverID := c.Query("server")
		if serverID == "" {
			c.JSON(400, pulpgin.H{"error": "server required"})
			return
		}
		result, err := dispatch[referralResult](client, "bananasplit.http.referrals.v1", map[string]any{
			"id": commandID(c, "referral-take"), "server_id": serverID,
		})
		if err != nil {
			c.JSON(500, pulpgin.H{"error": err.Error()})
			return
		}
		if result.Empty || result.Items == nil {
			result.Items = []referral{}
		}
		c.JSON(200, result.Items)
	})

	routes.POST("/match-complete", func(c *pulpgin.Context) {
		var request struct {
			ServerID string `json:"serverId"`
			MatchID  string `json:"matchId"`
			Players  []struct {
				UUID   string `json:"uuid" msgpack:"uuid"`
				Action string `json:"action" msgpack:"action"`
			} `json:"players"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(400, pulpgin.H{"error": err.Error()})
			return
		}
		result, err := dispatch[map[string]any](client, "bananasplit.http.match-complete.v1", map[string]any{
			"id": commandID(c, "match-complete"), "server_id": request.ServerID,
			"match_id": request.MatchID, "players": request.Players,
			"relay_host": cfg.RelayHost, "relay_port": cfg.RelayPort,
		})
		if err != nil {
			c.JSON(500, pulpgin.H{"error": err.Error()})
			return
		}
		respondMap(c, result)
	})
}
