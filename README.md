# listen-together

A small, standalone **synchronized-playback** ("listening party") server for
Navidrome and any other Subsonic-compatible music server. One person hosts a
room; everyone else's player follows their play / pause / seek / track changes in
near-real-time.

The reference client is a fork of [Feishin](https://github.com/jeffvli/feishin),
but the protocol is plain WebSocket + JSON, so any client (web page, CLI, mobile)
can join a room.

```
  Feishin A (host)  ──ws──┐
                          ├──►  listen-together  ──(validate creds)──►  Navidrome /rest/ping
  Feishin B (follower)─ws─┤        rooms (in-mem)
  any custom client ──ws──┘        host transport authority
                                   roomState broadcast + clock sync
```

## Why a sidecar (and not a Navidrome plugin)

Navidrome's plugin system only allows plugins to be *outbound* WebSocket
**clients** — they cannot host a server that many clients connect *into*. A sync
coordinator must accept inbound connections and broadcast authoritative state, so
it can't be a plugin. This is a tiny separate service instead, decoupled from
Navidrome internals so any client can speak its protocol and it works against any
Subsonic server. See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Key properties

- **Auth via your own server.** Clients send their own Subsonic credentials,
  validated against *their own* server's `/rest/ping`. Reuses existing accounts;
  works with Navidrome and any Subsonic server.
- **Minimal, privacy-preserving sync.** Only `trackId`, queue (list of IDs),
  index, position, and play/pause are shared. **Stream URLs are never shared** —
  each client builds its own from its own session.
- **One host per room** holds transport authority; followers are read-only and
  can request / be handed control. Host is reassigned automatically on leave.
- **Clock sync** via `ping`/`pong`: followers hard-seek when drift exceeds the
  threshold (~250 ms target).
- **Ephemeral** in-memory rooms — no database.

## Quick start

```sh
# local
make run                                  # or: LT_PORT=4040 go run ./cmd/listen-together
# docker
docker compose -f docker-compose.example.yml up --build
```

| Env | Default | Meaning |
|-----|---------|---------|
| `LT_PORT` | `4040` | HTTP/WS listen port |
| `LT_ALLOWED_SERVERS` | (none) | Comma-separated allowlist of server base URLs. Empty = **any** server accepted (open relay; fine locally, not for production). |

Endpoints: `GET /ws` (WebSocket), `GET /healthz`.

## Project layout

```
cmd/listen-together/   entrypoint, env config, HTTP wiring
internal/protocol/     wire types: envelope, events, payloads (no deps)
internal/auth/         Subsonic-ping credential validation + cache
internal/room/         ephemeral session state (WebSocket-agnostic, id-based)
internal/hub/          WebSocket transport: clients, dispatch, broadcast
docs/                  architecture, protocol, client guide, deployment
```

## Documentation

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — components, data flow, design decisions
- [docs/PROTOCOL.md](docs/PROTOCOL.md) — full WebSocket protocol reference + examples
- [docs/CLIENT_GUIDE.md](docs/CLIENT_GUIDE.md) — how to build a client (clock sync, queue resolution, echo suppression); Feishin integration notes
- [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) — env, Docker, TLS/reverse proxy, security, scaling

## Develop

```sh
make test     # go test ./...
make race     # with the race detector
make lint     # gofmt check + go vet
```
