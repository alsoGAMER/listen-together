# Deployment

listen-together is a single static Go binary with no database and no state on
disk. Run it next to Navidrome (or anywhere reachable by your clients).

## Configuration

| Env | Default | Meaning |
|-----|---------|---------|
| `LT_PORT` | `4040` | HTTP/WS listen port |
| `LT_ALLOWED_SERVERS` | (none) | Comma-separated allowlist of server base URLs clients may authenticate against. **Empty = any server accepted (open relay).** |
| `LT_ALLOWED_ORIGINS` | (none) | Comma-separated allowlist of **browser** `http(s)` origins for the WS upgrade. **Empty = any origin.** Only `http(s)` origins are gated; native/desktop clients (no `Origin`, `null`, or a non-web scheme such as `file://` from an Electron app) are always allowed. |
| `LT_MAX_ROOMS` | `0` | Cap on concurrent rooms. `0` = unlimited. Bounds memory on a public instance. |
| `LT_MAX_MEMBERS_PER_ROOM` | `0` | Cap on members per room. `0` = unlimited. Bounds broadcast fan-out. |
| `LT_STATS_TOKEN` | (none) | If set, enables `GET /stats` protected by this bearer token. **Empty = endpoint disabled.** |

Endpoints: `GET /ws` (WebSocket), `GET /healthz` (liveness), `GET /stats` (load counters, when `LT_STATS_TOKEN` is set).

## Run from source

```sh
LT_PORT=4040 LT_ALLOWED_SERVERS="https://music.example.com" go run ./cmd/listen-together
# or
make run PORT=4040
```

## Docker

```sh
docker build -t listen-together:latest .
docker run -p 4040:4040 \
  -e LT_ALLOWED_SERVERS="https://music.example.com" \
  listen-together:latest
```

The image is built `FROM scratch`-style (distroless static, nonroot) — a few MB,
no shell, runs as an unprivileged user.

## docker-compose (with Navidrome)

See [`../docker-compose.example.yml`](../docker-compose.example.yml). The sidecar
only reaches Navidrome over HTTP for `/rest/ping`; it never touches Navidrome's
database or music files.

```yaml
services:
  navidrome:
    image: deluan/navidrome:latest
    ports: ["4533:4533"]
  listen-together:
    build: .
    ports: ["4040:4040"]
    environment:
      LT_ALLOWED_SERVERS: "http://navidrome:4533,https://music.example.com"
```

If clients authenticate using a public Navidrome URL, put that URL (not the
internal `http://navidrome:4533`) in the allowlist — the `serverUrl` clients send
must match an allowlisted entry, and the sidecar calls exactly that URL.

## TLS / reverse proxy

Browsers on an HTTPS page can only open `wss://` (secure) WebSockets. Terminate
TLS at a reverse proxy and forward the upgrade. Example nginx:

```nginx
location /ws {
    proxy_pass http://127.0.0.1:4040;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
    proxy_read_timeout 1h;            # keep idle sockets alive
}
```

Caddy:
```
party.example.com {
    reverse_proxy /ws 127.0.0.1:4040
    reverse_proxy /healthz 127.0.0.1:4040
}
```

Clients then connect to `wss://party.example.com/ws`.

## Security

- **Set `LT_ALLOWED_SERVERS` in production.** Without it the service will perform
  outbound `ping` requests to any URL a client supplies (SSRF / open-relay risk)
  and lets anyone use your service to coordinate against arbitrary servers.
- **For a public instance, set `LT_MAX_ROOMS` / `LT_MAX_MEMBERS_PER_ROOM`** to
  bound memory and broadcast fan-out. `LT_ALLOWED_ORIGINS` is optional
  defense-in-depth against cross-site WebSocket hijacking from a **browser**; it
  is low-value here because the real guard is per-message Subsonic auth (there are
  no ambient credentials to hijack). It restricts only `http(s)` browser origins —
  native/desktop clients (`file://`, `null`, no `Origin`) are always allowed, so
  it won't lock out the desktop app.
- **`/stats` is opt-in and token-protected.** It is only registered when
  `LT_STATS_TOKEN` is set; the token is checked in constant time and accepted via
  `?token=` or `Authorization: Bearer`. Keep it off (or behind your proxy) if you
  don't need it.
- Credentials are only ever sent outward to the user's own (allowlisted) server
  for validation, and only a SHA-256 fingerprint of a *successful* credential set
  is cached in memory (5 min TTL) — raw credentials are not retained.
- Stream URLs and audio never pass through the sidecar; it only relays track ids
  and positions.
- Terminate TLS in front of it; do not expose plain `:4040` to the internet.

## Operations

- **Liveness:** `GET /healthz` → `200 ok`. Wire it to your orchestrator.
- **Load counters:** with `LT_STATS_TOKEN` set, `GET /stats` returns
  `{"rooms","members","clients"}` as JSON for scraping or a quick status check.
- **Logs:** plain text to stdout (connection drops, dropped-message warnings for
  slow clients, auth failures).
- **Resource use:** ~2 goroutines + a small buffer per connection; rooms are tiny
  in-memory structs. A single instance handles thousands of listeners easily.
- **Restarts** clear all rooms (ephemeral by design). Members simply rejoin by
  code. Plan restarts for quiet periods.

## Scaling

Rooms live in the memory of one process, so **all members of a room must hit the
same instance.** For a single self-hosted deployment, run one instance — it's
more than enough. If you ever need horizontal scale, either:

- pin a room to an instance via consistent hashing on the room code at the proxy, or
- add a shared pub/sub backplane (e.g. Redis) so instances relay `roomState` —
  this is intentionally **not** in v1.

## Troubleshooting

### Step 1 — probe the endpoints directly

Narrow the problem to reachability, the WebSocket upgrade, or the app:

```sh
# Liveness — should print "ok"
curl -sS https://party.example.com/healthz

# WebSocket handshake. Must be HTTP/1.1 — the Upgrade/Connection headers are
# illegal in HTTP/2, so test tools have to force 1.1 (browsers do this for you).
curl -sS --http1.1 -o /dev/null -D - \
  -H "Connection: Upgrade" -H "Upgrade: websocket" \
  -H "Sec-WebSocket-Version: 13" -H "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==" \
  https://party.example.com/ws
```

Read the first line of the handshake response:

| Response | Meaning |
|---|---|
| `101 Switching Protocols` | OK — the upgrade works. |
| `403 Forbidden` | Origin rejected → see *Upgrade returns 403*. |
| `4xx` / `5xx` / timeout | Reverse proxy isn't forwarding the upgrade, or the server is down. |

### Upgrade returns `403` (desktop app won't connect)

- **Cause:** `LT_ALLOWED_ORIGINS` is set and rejecting the client. Desktop apps (Electron loaded from disk) send `Origin: file://`, which is not a web origin.
- **Fix:** upgrade to **v1.0.4+** (native/desktop origins are always allowed there), **or** remove `LT_ALLOWED_ORIGINS` and restart.
- **Confirm:** re-run the handshake — no `Origin` header gives `101`, but adding `-H "Origin: file://"` gives `403` → the allowlist is the culprit.

### Upgrade fails behind a reverse proxy (`4xx`/`5xx`, or hangs)

- **Cause:** the proxy isn't forwarding the WebSocket upgrade.
- **Fix:** set the `Upgrade`/`Connection` headers and use HTTP/1.1 to the origin (see the nginx/Caddy examples above).
- **Cloudflare:** proxies WebSockets fine — just keep the route uncached and out of "Under Attack" mode.

### `authentication failed` / `server not allowed`

- **Cause:** the `serverUrl` the client sent isn't reachable from the sidecar, the credentials are wrong, or the URL isn't in `LT_ALLOWED_SERVERS`.
- **Fix:** list the **public** URL clients actually use, not an internal address. The allowlist match is exact after normalization (trailing slash, query, and fragment are stripped).

### `too many authentication attempts; slow down`

- **Cause:** the connection hit the auth backoff after repeated failures (exponential; the socket is dropped after 10).
- **Fix:** correct the credentials / `serverUrl`, then reconnect to reset.

### `server is at capacity` / `room ... is full`

- **Cause:** `LT_MAX_ROOMS` or `LT_MAX_MEMBERS_PER_ROOM` was reached.
- **Fix:** raise the limit (or set it to `0` for unlimited) and restart.

### `/stats` returns `404` or `401`

- **`404`:** the endpoint is only registered when `LT_STATS_TOKEN` is set.
- **`401`:** wrong or missing token — pass it as `?token=` or `Authorization: Bearer`.

### Members in the same room never see each other

- **Cause:** rooms are in-process with no backplane, so all members must hit the **same** instance.
- **Fix:** run a single instance, or pin by room code behind multiple replicas (see [Scaling](#scaling)).

### A room vanished

- **Expected:** rooms are ephemeral — deleted when the last member leaves, and all cleared on restart. Members just rejoin by code.
