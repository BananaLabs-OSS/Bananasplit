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

## Quick Start

```bash
go run ./cmd/server
```

## Configuration

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

Actions: `lobby` (return to lobby), `requeue` (queue again)

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

Background process runs every 500ms:

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

