# Deployment

listen-together is a single static Go binary with no database and no state on
disk. Run it next to Navidrome (or anywhere reachable by your clients).

## Configuration

| Env | Default | Meaning |
|-----|---------|---------|
| `LT_PORT` | `4040` | HTTP/WS listen port |
| `LT_ALLOWED_SERVERS` | (none) | Comma-separated allowlist of server base URLs clients may authenticate against. **Empty = any server accepted (open relay).** |

Endpoints: `GET /ws` (WebSocket), `GET /healthz` (liveness).

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
- Credentials are only ever sent outward to the user's own (allowlisted) server
  for validation, and only a SHA-256 fingerprint of a *successful* credential set
  is cached in memory (5 min TTL) — raw credentials are not retained.
- Stream URLs and audio never pass through the sidecar; it only relays track ids
  and positions.
- Terminate TLS in front of it; do not expose plain `:4040` to the internet.

## Operations

- **Liveness:** `GET /healthz` → `200 ok`. Wire it to your orchestrator.
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
