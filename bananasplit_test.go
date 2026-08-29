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

func TestJoinLeaseBindsOneConnectionAndConsumesToken(t *testing.T) {
	script, err := os.ReadFile("application/bananasplit.lua")
	if err != nil {
		t.Fatal(err)
	}
	records := map[string]map[string]any{}
	caller := orchestrator.CallFunc(func(_ string, provider string, payload []byte) ([]byte, error) {
		var request map[string]any
		if err := msgpack.Unmarshal(payload, &request); err != nil {
			return nil, err
		}
		switch provider {
		case "coordination.v1.directory.put":
			records[request["key"].(string)] = request["record"].(map[string]any)
			return msgpack.Marshal(map[string]any{"found": true})
		case "coordination.v1.directory.get":
			record, found := records[request["key"].(string)]
			return msgpack.Marshal(map[string]any{"found": found, "record": record})
		case "coordination.v1.directory.remove":
			key := request["key"].(string)
			record, found := records[key]
			delete(records, key)
			return msgpack.Marshal(map[string]any{"found": found, "record": record})
		case "engine.http-json.v1.request":
			return msgpack.Marshal(map[string]any{"status": int64(200), "value": map[string]any{
				"id": "server-a", "host": "198.51.100.8", "port": int64(25565),
			}})
		default:
			t.Fatalf("unexpected provider %s", provider)
			return nil, nil
		}
	})
	runtime, err := orchestrator.New(orchestrator.Options{Script: string(script), Caller: caller, Config: map[string]any{
		"bananagine_url": "http://bananagine:3000",
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if _, err := runtime.Dispatch(orchestrator.DispatchRequest{Event: "bananasplit.http.join-lease.issue.v1", Payload: map[string]any{
		"id": "issue-1", "lease_id": "lease-1", "token_digest": "digest-1",
		"principal_id": "principal-1", "device_id": "device-1", "destination_id": "server-a",
		"expires_at": int64(200),
	}}); err != nil {
		t.Fatal(err)
	}
	result, err := runtime.Dispatch(orchestrator.DispatchRequest{Event: "bananasplit.http.connection.resolve.v1", Payload: map[string]any{
		"id": "resolve-1", "connection_id": "connection-1", "token_digest": "digest-1",
		"transport": "tcp", "source_endpoint": "203.0.113.10:50001", "now": int64(100),
	}})
	if err != nil {
		t.Fatal(err)
	}
	resolved, ok := result.Value.(map[string]any)
	if !ok {
		t.Fatalf("resolved type = %T", result.Value)
	}
	if resolved["backend"] != "198.51.100.8:25565" || resolved["principal_id"] != "principal-1" {
		t.Fatalf("resolved = %#v", resolved)
	}
	second, err := runtime.Dispatch(orchestrator.DispatchRequest{Event: "bananasplit.http.connection.resolve.v1", Payload: map[string]any{
		"id": "resolve-2", "connection_id": "connection-2", "token_digest": "digest-1",
		"transport": "tcp", "source_endpoint": "203.0.113.10:50002", "now": int64(101),
	}})
	if err != nil {
		t.Fatal(err)
	}
	consumed, ok := second.Value.(map[string]any)
	if !ok {
		t.Fatalf("consumed type = %T", second.Value)
	}
	if consumed["http_status"] != int64(401) {
		t.Fatalf("consumed lease result = %#v", consumed)
	}
}
