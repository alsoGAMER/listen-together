# Troubleshooting

Common problems running or connecting to a listen-together instance, and how to
fix them. See [DEPLOYMENT.md](DEPLOYMENT.md) for configuration and reverse-proxy
setup.

## Probe the endpoints first

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
| `403 Forbidden` | Origin rejected → see *Upgrade returns 403* below. |
| `4xx` / `5xx` / timeout | Reverse proxy isn't forwarding the upgrade, or the server is down. |

## Upgrade returns `403` (desktop app won't connect)

- **Cause:** `LT_ALLOWED_ORIGINS` is set and rejecting the client. Desktop apps (Electron loaded from disk) send `Origin: file://`, which is not a web origin.
- **Fix:** upgrade to **v1.0.4+** (native/desktop origins are always allowed there), **or** remove `LT_ALLOWED_ORIGINS` and restart.
- **Confirm:** re-run the handshake — no `Origin` header gives `101`, but adding `-H "Origin: file://"` gives `403` → the allowlist is the culprit.

## Upgrade fails behind a reverse proxy (`4xx`/`5xx`, or hangs)

- **Cause:** the proxy isn't forwarding the WebSocket upgrade.
- **Fix:** set the `Upgrade`/`Connection` headers and use HTTP/1.1 to the origin (see the nginx/Caddy examples in [DEPLOYMENT.md](DEPLOYMENT.md)).
- **Cloudflare:** proxies WebSockets fine — just keep the route uncached and out of "Under Attack" mode.

## `authentication failed` / `server not allowed`

- **Cause:** the `serverUrl` the client sent isn't reachable from the sidecar, the credentials are wrong, or the URL isn't in `LT_ALLOWED_SERVERS`.
- **Fix:** list the **public** URL clients actually use, not an internal address. The allowlist match is exact after normalization (trailing slash, query, and fragment are stripped).

## `too many authentication attempts; slow down`

- **Cause:** the connection hit the auth backoff after repeated failures (exponential; the socket is dropped after 10).
- **Fix:** correct the credentials / `serverUrl`, then reconnect to reset.

## `server is at capacity` / `room ... is full`

- **Cause:** `LT_MAX_ROOMS` or `LT_MAX_MEMBERS_PER_ROOM` was reached.
- **Fix:** raise the limit (or set it to `0` for unlimited) and restart.

## `/stats` returns `404` or `401`

- **`404`:** the endpoint is only registered when `LT_STATS_TOKEN` is set.
- **`401`:** wrong or missing token — pass it as `?token=` or `Authorization: Bearer`.

## Members in the same room never see each other

- **Cause:** rooms are in-process with no backplane, so all members must hit the **same** instance.
- **Fix:** run a single instance, or pin by room code behind multiple replicas (see [Scaling](DEPLOYMENT.md#scaling)).

## A room vanished

- **Expected:** rooms are ephemeral — deleted when the last member leaves, and all cleared on restart. Members just rejoin by code.
