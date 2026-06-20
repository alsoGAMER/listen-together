# Client guide

How to build a client that joins a listen-together room. The reference client is
a Feishin fork (mapped at the end), but everything here applies to any client.

See [PROTOCOL.md](PROTOCOL.md) for the exact message shapes.

## 1. Connect & authenticate

```js
const ws = new WebSocket("wss://party.example.com/ws");
const send = (event, data) => ws.send(JSON.stringify({ event, data }));

ws.onopen = () => {
  // Reuse the credentials your client already has for its Subsonic server.
  send("authenticate", { serverUrl, username, token, salt });
};
```

Keep the `memberId` from the `authenticated` reply. You are the **host** when
`roomState.hostMemberId === memberId`.

## 2. Create or join a room

```js
send("createRoom");                       // become host
send("joinRoom", { roomId: "G7KQ2M" });   // join an existing room
```

## 3. Clock synchronization

You can't apply a remote position directly — the message took time to arrive and
the two clocks differ. Estimate a **clock offset** with periodic pings:

```js
// Send every ~10s, keep the sample with the smallest round-trip time.
let best = { rtt: Infinity, offset: 0 };

function ping() { send("ping", { t0: Date.now() }); }

function onPong({ t0, serverTimeMs }) {
  const t2 = Date.now();
  const rtt = t2 - t0;
  const offset = serverTimeMs - (t0 + t2) / 2;  // serverTime ≈ localTime + offset
  if (rtt < best.rtt) best = { rtt, offset };
}
```

Send a few pings right after joining to converge quickly, then every ~10 s.

## 4. Apply `roomState` (followers)

```js
let lastSeq = -1;

function onRoomState(rs) {
  if (rs.seq <= lastSeq) return;        // ignore stale / reordered frames
  lastSeq = rs.seq;

  isHost = rs.hostMemberId === memberId;
  renderMembers(rs.members);
  if (isHost) return;                   // host doesn't follow itself

  applyTransport(rs.transport);
}

function applyTransport(t) {
  applyingRemote = true;                // suppress echo (see §5)
  try {
    // Track / queue change?
    if (t.trackId !== currentTrackId) {
      const songs = resolveQueue(t.queue);   // your API: ids -> song metadata
      setQueue(songs, t.queueIndex);
    }

    // Where should we be right now?
    const expected = t.playing
      ? t.positionMs + ((Date.now() + best.offset) - t.serverTimeMs)
      : t.positionMs;

    if (Math.abs(currentPositionMs() - expected) > 250) {
      seekTo(expected / 1000);          // hard seek past the drift threshold
    }
    t.playing ? play() : pause();
  } finally {
    applyingRemote = false;
  }
}
```

Notes:
- **Stream URLs are never sent.** `resolveQueue` turns the synced track ids into
  playable songs **using the client's own server session** — each member streams
  with their own credentials.
- 250 ms is the v1 "listening party" threshold. Lower it later only alongside
  per-device latency calibration (see ARCHITECTURE non-goals).
- For paused rooms, just match `positionMs` (no clock math needed).

## 5. Emit `transport` (host only) + echo suppression

Watch your own player and forward changes — but only when you're the host, and
never while you're applying a remote update (or you'd ping-pong forever):

```js
function onLocalPlayerChange() {
  if (!isHost || applyingRemote) return;
  debounce(() => {
    send("transport", {
      playing, positionMs, trackId, queue: queueIds, queueIndex,
    });
  }, 150);
}
```

Followers never emit `transport`. The `applyingRemote` guard + the host-only rule
are what keep the loop from feeding back on itself.

## 6. Control handoff

```js
send("requestControl");                          // follower asks
// host receives: { event: "controlRequested", data: { fromMemberId, fromUsername } }
send("passControl", { toMemberId: fromMemberId }); // host grants
```

## 7. Reconnect

WebSockets drop. Reconnect with backoff, re-`authenticate`, and re-`joinRoom`
with the same room code; the first `roomState` you receive resnaps you. Reset
`lastSeq = -1` on reconnect.

---

## Feishin integration map

Feishin already implements this exact pattern for its **remote-control** feature
(a `ws` server in the main process that relays transport commands to the renderer
and the player store). We mirror it, pointed at listen-together instead.

Reference files to copy patterns from:
- `src/remote/store/index.ts` — `StatefulWebSocket` (natural-close flag, reconnect,
  authenticate-on-open, `{event}` switch). Model the sync client on this.
- `src/main/features/core/remote/index.ts` — `{event,data}` envelope conventions.

Drive playback **only** through existing `player.store.ts` actions (works for both
the mpv desktop engine and the WaveSurfer web engine):

| Sync action | Feishin store action |
|---|---|
| play / pause | `mediaPlay()` / `mediaPause()` |
| seek to absolute | `mediaSeekToTimestamp(seconds)` |
| set queue + index | `setQueue(items, index, position)` |
| jump to queue index | `mediaPlayByIndex(index)` |
| volume (optional) | `setVolume(0..100)` |

Resolve `queue` (track ids) → `items` via the existing API controllers
(`src/renderer/api/navidrome/navidrome-controller.ts` /
`subsonic-controller.ts`); each client builds its own stream URLs locally.

New files to add in the fork:
- `src/renderer/api/sync/sync-client.ts` — WS client (connect/auth/reconnect,
  ping/pong clock sync), modeled on `src/remote/store/index.ts`.
- `src/renderer/store/sync.store.ts` — Zustand store
  `{ enabled, sidecarUrl, connected, roomId, hostMemberId, isHost, members, clockOffsetMs }`.
- `src/renderer/features/sync/use-sync-session.ts` — the glue hook: applies
  inbound `roomState` (with the `applyingRemote` guard) and emits host transport.
- `src/renderer/features/sync/` — Mantine UI to create/join by code, list members,
  show the host badge, and "take control".
- Settings entry for the sidecar URL + enable toggle; pull credentials from
  Feishin's existing auth store to populate `authenticate`.
