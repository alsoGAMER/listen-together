# Architecture

## Goal

Let several people listen to the same music **in sync** ("listening party"):
one host drives playback (play/pause/seek/track/queue changes) and every other
member's player follows within a small tolerance (~250 ms target).

## Why this is a sidecar, not a Navidrome plugin

Navidrome gained a WASM plugin system, but plugins can only act as **outbound
WebSocket clients** to allowlisted hosts — they cannot expose a server or HTTP
routes that external clients connect *into*, and they cannot hold the kind of
long-lived, fan-out connection state a coordinator needs.

A sync coordinator must:

1. accept inbound connections from many clients,
2. keep authoritative per-room playback state, and
3. broadcast that state to every member.

That is structurally impossible inside the plugin sandbox, so listen-together is
a small standalone service. Two further benefits of keeping it separate:

- **Client-agnostic.** The protocol is plain WebSocket + JSON and is not coupled
  to Navidrome's JWT/SSE/native API, so any client can implement it.
- **Server-agnostic.** Auth uses the standard Subsonic `ping`, so it works with
  Navidrome and any other Subsonic server.

## Components

```
                         ┌─────────────────────────────────────────────┐
                         │                  Hub (internal/hub)         │
  client ──ws── /ws ───▶│  upgrade → client{readPump,writePump}       │
                         │  dispatch(event)  ───────────────┐          │
                         │  clients: memberID → *client     │          │
                         └───────┬──────────────────────────┼──────────┘
                                 │ Validate(creds)          │ Create/Join/Leave
                                 ▼                          ▼ ApplyTransport/PassControl
                    ┌────────────────────┐      ┌────────────────────────────┐
                    │  auth (internal/   │      │  room (internal/room)      │
                    │  auth)             │      │  Manager → Room            │
                    │  Subsonic /rest/   │      │  members, host, transport, │
                    │  ping + cache      │      │  seq  (WebSocket-agnostic) │
                    └─────────┬──────────┘      └────────────────────────────┘
                              │ HTTP GET
                              ▼
                   user's own Subsonic/Navidrome server
```

| Package | Responsibility | Depends on |
|---|---|---|
| `internal/protocol` | Wire format: envelope, event names, payload structs. Copy-pasteable into clients. | (nothing) |
| `internal/auth` | Validate client credentials via Subsonic `ping`; short-TTL success cache; optional server allowlist. | `protocol` |
| `internal/room` | Ephemeral session state: membership, host authority, authoritative `Transport`, `seq`. **Knows nothing about WebSockets** — operates on member ids only, so it is trivially unit-testable. | `protocol` |
| `internal/hub` | WebSocket transport: connection lifecycle, client registry (`memberID → *client`), event dispatch, broadcast. Maps room member ids back to live connections. | `protocol`, `auth`, `room`, `gorilla/websocket` |
| `cmd/listen-together` | Process entrypoint: env config, HTTP mux (`/ws`, `/healthz`), graceful shutdown. | `auth`, `hub` |

The `room`↔`hub` split is the important one: session *semantics* (who's host,
what's playing) live in `room` with no I/O, and the `hub` only moves bytes. This
avoids an import cycle (room never imports hub) and keeps the testable core pure.

## Data flow

### Joining
1. Client connects to `/ws`; the hub assigns a random `memberID` and registers it.
2. Client sends `authenticate`; the hub validates via `auth` (Subsonic ping) and
   replies `authenticated{memberId}`.
3. Client sends `createRoom` or `joinRoom`; the hub updates `room` and broadcasts
   `roomState` to all members.

### Playback sync
1. The **host** sends `transport{playing, positionMs, trackId, queue, queueIndex}`.
2. `room.ApplyTransport` accepts it only if the sender is the host, stamps
   `serverTimeMs`, and bumps `seq`.
3. The hub broadcasts `roomState` to every member.
4. **Followers** compute their expected position from `positionMs`,
   `serverTimeMs`, and their measured clock offset, and hard-seek if drift > threshold.

### Clock sync
Each client periodically sends `ping{t0}` and the server replies
`pong{t0, serverTimeMs}`. The client derives a clock offset from the round trip
(see [CLIENT_GUIDE.md](CLIENT_GUIDE.md)). All `serverTimeMs` values use
`time.Now().UnixMilli()` on the server, so they share one epoch.

## Concurrency model

- One **readPump** and one **writePump** goroutine per connection. Only the
  writePump writes to the socket; everything else enqueues onto a buffered
  `sendCh`. Sends are non-blocking — if a client's buffer is full the message is
  dropped (the next `roomState` re-syncs it).
- `room.Manager` guards its room map with a mutex; each `Room` guards its own
  fields. Lock order is always Manager → Room. Snapshots are copied out under the
  lock and sent without holding it.
- The hub guards its `memberID → *client` registry with its own mutex.
- The whole test suite passes under `go test -race`.

## State & lifetime

Rooms are **in-memory and ephemeral** (like Navidrome's own events broker). When
the last member leaves, the room is deleted. There is no database and no
persistence — restarting the service clears all rooms. This keeps the service
stateless-to-deploy and trivial to scale vertically; see
[DEPLOYMENT.md](DEPLOYMENT.md) for the horizontal-scaling caveat.

## Non-goals (v1)

- Sub-100 ms "same room" sync (would need per-device latency calibration and
  continuous micro-correction).
- Collaborative "anyone can control" mode (would need last-writer-wins + conflict
  handling). v1 is single-host with explicit handoff.
- Persistence, chat, reactions.
