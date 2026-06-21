# Protocol reference

Transport: a single **WebSocket** connection per client to `GET /ws`.

Every message is a JSON **envelope**:

```json
{ "event": "<name>", "data": { ... } }
```

`data` may be omitted for events with no fields. The types below correspond
directly to the Go structs in [`internal/protocol`](../internal/protocol/protocol.go).

All timestamps are **Unix milliseconds** (`Date.now()` in JS). Server timestamps
(`serverTimeMs`) share one monotonic-ish epoch and are the basis for clock sync.

---

## Lifecycle

```
client ── authenticate ─────────────▶ server
client ◀──── authenticated ──────────  server
client ── createRoom | joinRoom ─────▶ server
client ◀──── roomState (broadcast) ──  server
            ... transport / ping ...
client ── leaveRoom | (disconnect) ──▶ server
```

A client **must** `authenticate` before any other event; otherwise the server
replies with `error` and ignores it.

---

## Client → Server

### `authenticate` (required first)
Validate the connection using the user's own Subsonic credentials. Provide either
`token`+`salt` (preferred) or `password`.

```json
{ "event": "authenticate",
  "data": { "serverUrl": "https://music.example.com",
            "username": "alice", "token": "26719a...", "salt": "c19b2d" } }
```
| field | type | notes |
|---|---|---|
| `serverUrl` | string | base URL of the user's own Subsonic/Navidrome server |
| `username` | string | |
| `token` | string | `md5(password + salt)` |
| `salt` | string | random per Subsonic spec |
| `password` | string | optional fallback if token/salt absent |

Reply: `authenticated` on success, `error` on failure.

### `createRoom`
```json
{ "event": "createRoom" }
```
The caller becomes the host of a new room. The room code is returned in the
following `roomState`. Reply: `roomState`, or `error` if the server is at its
configured room capacity (`LT_MAX_ROOMS`).

### `joinRoom`
```json
{ "event": "joinRoom", "data": { "roomId": "G7KQ2M" } }
```
Reply: `roomState` broadcast to all members, or `error` if the room is unknown or
full (`LT_MAX_MEMBERS_PER_ROOM`). Re-joining a room you're already in (e.g. after
a reconnect) never trips the member cap.

### `leaveRoom`
```json
{ "event": "leaveRoom" }
```
Disconnecting has the same effect.

### `transport` (host only)
Sets the authoritative playback state. Ignored (silently) if the sender is not
the room host.

```json
{ "event": "transport",
  "data": { "playing": true, "positionMs": 1500, "trackId": "tr-123",
            "queue": ["tr-123","tr-124","tr-125"], "queueIndex": 0 } }
```
| field | type | notes |
|---|---|---|
| `playing` | bool | |
| `positionMs` | int | playback position of the current track |
| `trackId` | string | server track id (Subsonic id) |
| `queue` | string[] \| null | ordered list of track ids. **Omit (or send `null`) to leave the room's current queue unchanged** — useful to avoid resending the full list on every play/pause/seek. A present list (including `[]`) replaces it. |
| `queueIndex` | int | index of the current track within `queue` |
| `clientTimeMs` | int | optional monotonic clock; the server drops transports whose value is lower than the last one applied (out-of-order). Omit/0 to disable. |

The server stamps `serverTimeMs` and increments `seq` before broadcasting. The
broadcast `roomState.transport.queue` is always the full current list.

### `requestControl`
Ask the current host to hand over control. The host receives `controlRequested`.
```json
{ "event": "requestControl" }
```

### `passControl` (host only)
Transfer host to another member.
```json
{ "event": "passControl", "data": { "toMemberId": "9af1c0b2..." } }
```

### `ping`
Clock-sync probe. Echoes back in `pong`.
```json
{ "event": "ping", "data": { "t0": 1718900000000 } }
```

---

## Server → Client

### `authenticated`
```json
{ "event": "authenticated", "data": { "memberId": "9af1c0b2...", "username": "alice" } }
```
Store `memberId` — compare it to `roomState.hostMemberId` to know if you're host.

### `roomState`
The full snapshot, broadcast on **any** change and sent to a client right after
it joins.
```json
{ "event": "roomState",
  "data": { "roomId": "G7KQ2M",
            "hostMemberId": "9af1c0b2...",
            "members": [ { "id": "9af1c0b2...", "username": "alice" },
                         { "id": "1b2c3d4e...", "username": "bob" } ],
            "seq": 7,
            "transport": { "playing": true, "positionMs": 1500, "trackId": "tr-123",
                           "queue": ["tr-123","tr-124"], "queueIndex": 0,
                           "serverTimeMs": 1718900000123 } } }
```
`seq` increases monotonically per room. Clients should **ignore** any `roomState`
whose `seq` is not greater than the last applied one (drops stale/reordered frames).

### `controlRequested` (to host only)
```json
{ "event": "controlRequested", "data": { "fromMemberId": "1b2c3d4e...", "fromUsername": "bob" } }
```

### `pong`
```json
{ "event": "pong", "data": { "t0": 1718900000000, "serverTimeMs": 1718900000042 } }
```

### `error`
```json
{ "event": "error", "data": { "message": "authentication failed: ..." } }
```

---

## Rules summary

- Authenticate first; everything else requires it.
- Only the host's `transport` is honored; followers' are ignored.
- `roomState` is the single source of truth; apply by increasing `seq` only.
- Room codes use an unambiguous alphabet (no `I L O 0 1`), 6 chars.
- Rooms are ephemeral; if everyone leaves, the room (and its code) is gone.

A full quick start for writing a client — including the clock-sync math, queue
resolution, and echo-suppression — is in [CLIENT_GUIDE.md](CLIENT_GUIDE.md).
