# Outbound header capture — opencode2api v1.3.8

Goal: capture the five `x-opencode-*` / `User-Agent` headers that the gateway sends
**to the upstream** (these are request headers, not response headers).

Method: ran a capture proxy on `127.0.0.1:9090`, pointed a throwaway gateway
container at it via `upstream.zen = http://host.docker.internal:9090`, and recorded
every outbound request before forwarding it to the real `https://opencode.ai/zen`.

- Model: `muse-spark-1.3-contributor-free` (native protocol `responses`)
- Endpoint called downstream: `POST /v1/responses`
- Requests: 10
- Result: **10/10 HTTP 200**, all `status: completed`

## Headers sent upstream (per inference call)

| Header               | Value                                       | Notes                                      |
| -------------------- | ------------------------------------------- | ------------------------------------------ |
| `User-Agent`         | `opencode/1.18.31 (linux amd64; go1.24.13)` | hardcoded to impersonate the OpenCode CLI  |
| `x-opencode-client`  | `cli`                                       | constant                                   |
| `x-opencode-session` | `ses_364fd5fcbffe1vIMsrgVFkcTt7`            | stable across the 10 calls                 |
| `x-opencode-request` | `req_e9ccdc14…`                             | unique per call (10/10 distinct)           |
| `x-opencode-project` | `prj_6ab5f8f61e12fda7f7add161`              | constant                                   |
| `x-session-affinity` | `ses_364fd5fcbffe1vIMsrgVFkcTt7`            | same as session (OpenCode 1.18.x affinity) |
| `x-session-id`       | `ses_364fd5fcbffe1vIMsrgVFkcTt7`            | same as session (legacy alias)             |
| `Authorization`      | `Bearer public`                             | anonymous lane credential                  |
| `Content-Type`       | `application/json`                          |                                            |
| `Accept`             | `application/json, text/event-stream`       |                                            |
| `Accept-Encoding`    | `gzip`                                      | added by Go transport                      |
| `Content-Length`     | `695`                                       |                                            |

Raw JSON of one captured call is in `capture-headers.jsonl`.

## Observations

1. **The session ID is what fixed the 403.** It matches OpenCode's canonical format
   `ses_` + 12 lowercase hex + 14 Base62 (`ses_364fd5fcbffe` + `1vIMsrgVFkcTt7`).
   In v1.3.7 this was malformed and the Zen free tier answered `403 FreeTierError`.
   All 10 calls now succeed.
2. **Session affinity is deliberate.** All 10 calls reuse one `x-opencode-session`
   because the request bodies were identical, so the gateway derived the same
   conversation seed. This is intended — it preserves upstream prompt cache.
   Distinct conversations produce distinct session IDs.
3. **`x-opencode-request` is the downstream `X-Request-Id`.** Verified: the value
   returned to the client in the `X-Request-Id` response header is the exact same
   `req_…` string forwarded upstream, so a client can correlate a response with the
   upstream attempt.
4. **Three session headers carry the same value on purpose.** `x-opencode-session`,
   `x-session-affinity` and `x-session-id` are all set to the canonical session, so
   both new and older Zen deployments recognise it (comment in `upstream.go`).
5. `x-opencode-client: cli` and the `opencode/1.18.31` User-Agent are how the gateway
   passes the upstream's "must come from OpenCode" check.

## Source references (v1.3.8)

- `internal/gateway/upstream.go:799` — `newUpstreamRequest`, where every header is set
- `internal/identity/request.go:90` — `canonicalSessionPattern`, the format the free tier requires
- `internal/httpx/client.go` — `UserAgent()`
- `internal/models/discovery.go:264` — same `x-opencode-client: cli` used for model discovery

## Reproduce

```powershell
node capture-proxy.js                       # listens on :9090, logs to capture-headers.jsonl
docker run -d --name o2a-capture `
  -p 8090:8090 -p 8091:8091 `
  --add-host host.docker.internal:host-gateway `
  -e CONFIG_PATH=/var/lib/opencode2api/config.json `
  -e CONFIG_SEED_PATH=/run/config/opencode2api.json `
  -v "${PWD}\capture-config.json:/run/config/opencode2api.json:ro" `
  -v o2a-capture-state:/var/lib/opencode2api --tmpfs /tmp `
  ghcr.io/jasonxu114514/opencode2api:latest
```

Then call `http://127.0.0.1:8090/v1/responses` with model
`muse-spark-1.3-contributor-free`. Clean up with `docker rm -f o2a-capture`.
