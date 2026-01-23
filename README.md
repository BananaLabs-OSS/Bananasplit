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
# Optional: Set Peel URL for route updates
export PEEL_URL=http://localhost:8080

go run ./cmd/server
```

Runs on `:3001`.

## API Reference

### Queue

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/queue/join` | Join matchmaking queue |
| `POST` | `/queue/leave` | Leave queue |
| `GET` | `/queue/:mode/size` | Get queue size for mode |

**Join Queue:**
```json
{
  "uid": "player-uuid",
  "mode": "skywars",
  "lobbyServer": "lobby-1"
}
```

### Match Complete

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/match-complete` | Report match finished |

**Match Complete:**
```json
{
  "serverId": "skywars-1",
  "matchId": "match-1",
  "players": [
    {"uid": "player-AAA", "action": "lobby"},
    {"uid": "player-BBB", "action": "lobby"}
  ]
}
```

Actions: `lobby` (return to lobby), `requeue` (queue again)

### Players

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/players/register` | Register player location |
| `DELETE` | `/players/:uuid` | Unregister player |

**Register Player:**
```json
{
  "player_uuid": "player-AAA",
  "player_ip": "192.168.1.50",
  "server_id": "lobby-1"
}
```

### Referrals

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/referrals?server=:id` | Get pending transfers for server |

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
3. Update Peel routes (player IP → game server)
4. Queue referrals for lobby servers

## Dependencies

- [Bananagine](https://github.com/bananalabs-oss/bananagine) - Registry queries
- [Peel](https://github.com/bananalabs-oss/peel) - Route updates (optional)

## License

MIT