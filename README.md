# Bananasplit

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
| Pulp cell (canonical) | `pulp-cell/` | Running inside a Pulp host |
| Native binary | `cmd/server/` + Dockerfile | Standalone / containerized |

## Quick Start

**Pulp cell (canonical):**

Build (requires `GOOS=wasip1 GOARCH=wasm`):

```bash
cd pulp-cell
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o bananasplit.wasm .
```

Run via the deployment helper (loads the cell into Pulp):

```bash
cd pulp-deployment
go run . --cell ../pulp-cell/bananasplit.wasm
```

**Native binary:**

```bash
go run ./cmd/server
```

## Configuration

### Pulp cell

Configuration lives in `pulp-cell/pulp.cell.toml` (checked in) and is overridden by env vars at runtime. Key fields:

| Field (`pulp.cell.toml`) | Env var override | Default |
| ------------------------ | ---------------- | ------- |
| `bananagine_url` | — | `http://localhost:3000` |
| `peel_url` | — | (disabled) |
| `relay_host` | — | `hycraft.net` |
| `relay_port` | — | `5520` |
| `tick_rate_ms` | — | `500` |
| `queue_timeout_sec` | — | `300` (set negative to disable) |
| `service_token` | `SERVICE_TOKEN` | `""` (auth off when empty) |

`SERVICE_TOKEN` env wins over the toml field so the secret stays out of committed config.

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
