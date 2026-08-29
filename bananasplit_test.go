package bananasplit_test

import (
	"os"
	"testing"

	"github.com/BananaLabs-OSS/Pulp-Lua/orchestrator"
	"github.com/vmihailenco/msgpack/v5"
)

func TestPeelRouteMutationUsesDedicatedServiceToken(t *testing.T) {
	script, err := os.ReadFile("application/bananasplit.lua")
	if err != nil {
		t.Fatal(err)
	}
	var peelHeaders map[string]any
	caller := orchestrator.CallFunc(func(_ string, provider string, payload []byte) ([]byte, error) {
		switch provider {
		case "engine.http-json.v1.request":
			var request map[string]any
			if err := msgpack.Unmarshal(payload, &request); err != nil {
				return nil, err
			}
			if request["url"] == "http://peel:8080/routes" {
				peelHeaders, _ = request["headers"].(map[string]any)
				return msgpack.Marshal(map[string]any{"status": int64(201)})
			}
			return msgpack.Marshal(map[string]any{"status": int64(200), "value": []any{map[string]any{
				"id": "lobby-1", "host": "198.51.100.8", "port": int64(5520), "players": int64(0), "maxPlayers": int64(20),
			}}})
		case "coordination.v1.directory.put":
			return msgpack.Marshal(map[string]any{"ok": true})
		default:
			t.Fatalf("unexpected provider %s", provider)
			return nil, nil
		}
	})
	runtime, err := orchestrator.New(orchestrator.Options{Script: string(script), Caller: caller, Config: map[string]any{
		"bananagine_url": "http://bananagine:3000", "peel_url": "http://peel:8080", "peel_service_token": "peel-secret",
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if _, err := runtime.Dispatch(orchestrator.DispatchRequest{Event: "bananasplit.http.route-request.v1", Payload: map[string]any{
		"id": "route-auth", "player_ip": "203.0.113.9",
	}}); err != nil {
		t.Fatal(err)
	}
	if peelHeaders["X-Service-Token"] != "peel-secret" || peelHeaders["Content-Type"] != "application/json" {
		t.Fatalf("PEEL headers = %#v", peelHeaders)
	}
}
