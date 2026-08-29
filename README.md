# Bananasplit

The canonical Pulp application is composed by `application/bananasplit.lua`.
Lua owns matchmaking, lobby selection, route updates, and webhook policy. The
shared `http-json` engine performs application-neutral outbound requests; no
Bananasplit-specific effects engine is part of the composition.
Durable queues, group reservations, record bindings, mailboxes, and schedule
gates come from the shared `coordination-state` engine. BananaSplit maps its
players, lobbies, referrals, and matches onto those neutral contracts in Lua.
See [connection identity, global authorization, and fallback](docs/IDENTITY_ROUTING.md)
for the connection-scoped replacement for legacy IP routing.

Matchmaking and player tracking service.

From [BananaLabs OSS](https://github.com/bananalabs-oss).

## Overview

Bananasplit handles:

- **Queue**: Players join mode-specific queues
- **Matcher**: Finds available matches and assigns players
- **Player Registry**: Tracks player locations (UUID → IP → server)
- **Referrals**: Queues transfer instructions for game servers
- **Peel Integration**: Updates routing when players move

Two deployment targets:

| Target | Directory | Use when |
| ------ | --------- | -------- |
| Pulp application (canonical) | `application/` | Running inside a Pulp host |
| Native binary | `cmd/server/` + Dockerfile | Standalone / containerized |

## Quick Start

**Pulp application (canonical):**

Build the generic `coordination-state` and `http-json` engines, the thin API
adapter, and Pulp-Lua for WASI, then run the application manifest:

```bash
cd pulp-deployment
go run . -app ../application/pulp.app.toml
```

**Native binary:**

```bash
go run ./cmd/server
```

## Configuration

### Pulp application

Application policy lives in `application/lua-orchestrator.cell.toml`; transport
compatibility settings live in `api-cell/pulp.cell.toml`.

| Field (`pulp.cell.toml`) | Env var override | Default |
| ------------------------ | ---------------- | ------- |
| `bananagine_url` | — | `http://localhost:3000` |
| `peel_url` | — | (disabled) |
| `peel_service_token` | `PEEL_SERVICE_TOKEN` | `""` (legacy PEEL auth off) |
| `relay_host` | — | `hycraft.net` |
| `relay_port` | — | `5520` |
| `tick_rate_ms` | — | `500` |
| `queue_timeout_sec` | — | `300` (set negative to disable) |
| `service_token` | `SERVICE_TOKEN` | `""` (auth off when empty) |

`SERVICE_TOKEN` env wins over the toml field so the secret stays out of committed config.
`PEEL_SERVICE_TOKEN` is separate: it authenticates Bananasplit's outbound
route mutations to PEEL and must match PEEL's `SERVICE_TOKEN`. Pulp injects it
only into the Lua orchestrator's nested configuration.

When `service_token` / `SERVICE_TOKEN` is **empty**, all routes are served without auth (callers need no header). When set, `X-Service-Token: <value>` is required on all routes except `GET /health`. Both sides (cell + callers) must be updated in lockstep.

### Native binary

Configuration priority: CLI flags > Environment variables > Defaults

| Setting             | Env Var          | CLI Flag         | Default                 |
| ------------------- | ---------------- | ---------------- | ----------------------- |
| Listen address      | `LISTEN_ADDR`    | `-listen`        | `:3001`                 |
| Bananagine URL      | `BANANAGINE_URL` | `-bananagine`    | `http://localhost:3000` |
| Peel URL            | `PEEL_URL`       | `-peel`          | (disabled)              |
| Relay host          | `RELAY_HOST`     | `-relay-host`    | `hycraft.net`           |
| Relay port          | `RELAY_PORT`     | `-relay-port`    | `5520`                  |
| Tick rate (ms)      | `TICK_RATE`      | `-tick`          | `500`                   |
| Queue timeout (sec) | `QUEUE_TIMEOUT`  | `-queue-timeout` | `300`                   |

`SERVICE_TOKEN` is **required** for the native binary; the process fatals on startup if it is unset.

**CLI:**

```bash
./bananasplit -listen :3001 -bananagine http://localhost:3000 -tick 500 -queue-timeout 300
```

**Docker Compose:**

```yaml
bananasplit:
  image: localhost/bananasplit:local
  ports:
    - "3001:3001"
  environment:
    - BANANAGINE_URL=http://bananagine:3000
    - PEEL_URL=http://peel:8080
    - QUEUE_TIMEOUT=300
    - SERVICE_TOKEN=<secret>
    - PEEL_SERVICE_TOKEN=<same value as Peel SERVICE_TOKEN>
```

## API Reference

### Queue

| Method | Endpoint            | Description             |
| ------ | ------------------- | ----------------------- |
| `POST` | `/queue/join`       | Join matchmaking queue  |
| `POST` | `/queue/leave`      | Leave queue             |
| `GET`  | `/queue/:mode/size` | Get queue size for mode |

**Join Queue:**

```json
{
  "uuid": "player-uuid",
  "mode": "skywars",
  "lobbyServer": "lobby-1"
}
```

### Match Complete

| Method | Endpoint          | Description           |
| ------ | ----------------- | --------------------- |
| `POST` | `/match-complete` | Report match finished |

**Match Complete:**

```json
{
  "serverId": "skywars-1",
  "matchId": "match-1",
  "players": [
    { "uuid": "player-AAA", "action": "lobby" },
    { "uuid": "player-BBB", "action": "lobby" }
  ]
}
```

Actions: `lobby` (return to lobby). `requeue` is recognized but not yet implemented (logs a message, no-ops).

### Players

| Method   | Endpoint            | Description              |
| -------- | ------------------- | ------------------------ |
| `POST`   | `/players/register` | Register player location |
| `DELETE` | `/players/:uuid`    | Unregister player        |

**Register Player:**

```json
{
  "player_uuid": "player-AAA",
  "player_ip": "192.168.1.50",
  "server_id": "lobby-1"
}
```

### Connection identity

| Method | Endpoint | Description |
| ------ | -------- | ----------- |
| `POST` | `/join-leases` | Issue a one-use browser/device/destination capability |
| `POST` | `/connections/resolve` | Consume a lease and bind an edge connection |
| `GET` | `/connections/:id` | Inspect the durable connection binding |

The raw lease token is returned once and is never stored. A requested
destination may declare an authorized fallback. IP-based routing remains only
for compatibility with callers that have not adopted connection IDs.

### Referrals

| Method | Endpoint                | Description                      |
| ------ | ----------------------- | -------------------------------- |
| `GET`  | `/referrals?server=:id` | Get pending transfers for server |

**Response:**

```json
[
  {
    "player_uuid": "player-AAA",
    "host": "localhost",
    "port": 5520
  }
]
```

Game servers poll this endpoint to know which players to send to relay.

## Matcher

Runs every 500ms (configurable):

1. For each queue, find servers with ready matches
2. Assign players to matches
3. Notify lobby servers via POST /match webhook

### Webhook: /match (to lobby)

Matcher sends to each lobby's webhook port:

```json
{
  "matchId": "arena-1",
  "mode": "skywars",
  "players": ["uuid-1", "uuid-2"],
  "gameServer": "10.99.0.10:5520"
}
```

## Dependencies

- [Bananagine](https://github.com/bananalabs-oss/bananagine) - Registry queries
- [Peel](https://github.com/bananalabs-oss/peel) - Route updates (optional)

## License

MIT
