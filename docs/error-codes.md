# opencode2api — Danh mục mã lỗi & chuỗi lỗi trả về khi gọi request

Nguồn: phân tích tĩnh toàn repo (không sửa file nguồn) — `internal/protocol`, `internal/gateway`,
`internal/admin`, `internal/httpx`, `internal/models`, `internal/rotation`, `internal/identity`,
`internal/telemetry`, `internal/config`, `cmd/opencode2api`.

---

## 0. Ba envelope lỗi (rất quan trọng)

| Nhóm route | Envelope JSON | Ghi chú |
|---|---|---|
| Anthropic `POST /v1/messages` | `{"type":"error","error":{"type":<kind>,"message":<msg>}}` | không có field `code`; mã nằm ở `error.type` |
| OpenAI / Responses / SystemOne (`/v1/chat/completions`, `/v1/responses`, `/v1/systemone`) | `{"error":{"message":<msg>,"type":<kind>,"param":null,"code":null}}` | **`code` luôn = `null`**; mã nằm ở `error.type` |
| Admin `/api/*` | `{"error":{"code":<code>,"message":<msg>}}` | **nơi duy nhất có `code` thật** |

Nguồn: `internal/protocol/errors.go:14`, `internal/admin/server.go:451`.

> **Kết luận quan trọng:** ở API inference **không tồn tại** mã lỗi dạng chuỗi riêng.
> Biến `kind` (được ghi vào `error.type`) mới là định danh máy-đọc-được. Không có hằng số
> `ErrCodeX` nào trong toàn repo — tất cả là literal inline tại call site.

Header phụ: `x-request-id` chỉ được set khi `requestID != ""` (mọi lỗi 400/401/500 đều không có).

## 0.1 Chế độ đơn giản (`errors.simple_body`)

Mặc định API inference trả lỗi theo envelope của từng giao thức (bảng ở §0).
Bật `errors.simple_body` (trong `config.json`, qua `PUT /api/config`, hoặc bằng ô
đánh dấu **简化错误响应 / Simple error body** trong trang Config của WebUI) thì
mọi giao thức — kể cả `/v1/messages` — trả cùng một dạng rút gọn:

```json
{
  "error": {
    "code": "invalid_request_error",
    "message": "model is required"
  }
}
```

`code` là cùng bộ phân loại mà envelope gốc đặt trong `type` (`invalid_request_error`,
`authentication_error`, `upstream_error`, …). HTTP status, header `x-request-id` và
mọi message trong bảng dưới đây không đổi. Khi upstream báo mã lỗi riêng của nó,
mã đó được dùng làm `code` (xem §0.2).

Phạm vi: chỉ áp dụng cho **body lỗi** của API inference. Frame lỗi giữa luồng SSE
(§3) vẫn theo shape riêng của từng giao thức, vì chúng nằm trong stream đã commit
HTTP 200. Admin API (`/api/*`) luôn dùng envelope `{"error":{"code","message"}}` của riêng nó.
Lỗi do panic (500) cũng theo chế độ đang bật.

## 0.2 Mã lỗi do upstream báo (`code`)

Khi upstream trả lỗi non-2xx, mã lỗi của chính nó được giữ lại thay vì bị bỏ qua:

- Lấy từ `error.code`, rồi `error.type` của body upstream. Chỉ nhận giá trị là mã
  thật: chữ, số, `.`, `-`, `_`, bắt đầu bằng chữ, tối đa 64 ký tự. Free text không
  phải mã thì bị bỏ qua (`upstreamErrorCode`, `isErrorCode`, gateway.go).
- **Envelope OpenAI** (`{"error":{message,type,param,code}}`): `error.code` = mã
  upstream (trước đây luôn là `null`), `error.type` vẫn là `upstream_error` như cũ.
- **Chế độ đơn giản** (`errors.simple_body`): `error.code` = mã upstream nếu có,
  không thì là `kind` của gateway (`upstream_error`).
- **Envelope Anthropic**: không có slot `code`, nên mã upstream không xuất hiện riêng —
  nó vẫn nằm trong `message` mà upstream trả về.

Ví dụ upstream trả
`{"error":{"message":"invalid request body: json: unknown field \"max_output_tokens\"","type":"unknown_parameter"}}`
thì client nhận `error.code = "unknown_parameter"` thay vì `null`.

---

## 1. API Inference — 10 mã lỗi (`kind`)

| `kind` | HTTP | Nguồn |
|---|---|---|
| `authentication_error` | 401 | `internal/gateway/gateway.go:190` |
| `invalid_request_error` | 400 | `internal/gateway/gateway.go:276,281,290,316,329,343,352,362,533,538,546,559,566,570,578` |
| `client_canceled` | 499 | `internal/gateway/gateway.go:393,634` |
| `proxy_error` | 503 | `internal/gateway/gateway.go:401,639` |
| `upstream_timeout` | 504 | `internal/gateway/gateway.go:407,643` |
| `free_tier_ip_blocked` | 503 | `internal/gateway/gateway.go:415` |
| `free_tier_lane_paused` | 503 | `internal/gateway/gateway.go:422` |
| `upstream_error` | 502 / status của upstream | `internal/gateway/gateway.go:425,484,495,510,646,677,768` |
| `rotation_unavailable` | 503 / 504 | `internal/gateway/rotation.go:191` |
| `server_error` | 500 | `internal/telemetry/recovery.go:17` |

### 1.1 Bảng đầy đủ message của API inference

| `kind` | Message (verbatim) | HTTP | Vị trí | Điều kiện |
|---|---|---|---|---|
| `authentication_error` | `invalid local API key` | 401 | gateway.go:190 | `x-api-key` / `Bearer` không khớp server key nào đang bật |
| `invalid_request_error` | `request body is too large or unreadable` | 400 | gateway.go:276 / :533 | body > 32 MiB (`maxRequestBody`, gateway.go:38) hoặc đọc lỗi |
| `invalid_request_error` | `request body must be a JSON object` | 400 | gateway.go:281 / :538 | body không phải JSON object |
| `invalid_request_error` | `model is required` | 400 | gateway.go:290 / :546 | thiếu `model`; `param:"model"` |
| `invalid_request_error` | `the model uses an upstream protocol that opencode2api does not expose` | 400 | gateway.go:316 / :559 | model không có trong catalog; `param:"model"` |
| `invalid_request_error` | *text lỗi routing của catalog* (xem §1.2) | 400 | gateway.go:329 / :566 | `catalog.Route` thất bại; `param:"model"` |
| `invalid_request_error` | `this model uses System One decisions, not chat; send state and typed questions to /v1/systemone` | 400 | gateway.go:343 | model là SystemOne nhưng gọi endpoint chat |
| `invalid_request_error` | `the model does not use the systemone protocol` | 400 | gateway.go:570 | model không phải SystemOne nhưng gọi `/v1/systemone` |
| `invalid_request_error` | `request contains unsupported JSON values` | 400 | gateway.go:352 / :578 | re-marshal alias payload lỗi |
| `invalid_request_error` | `no usable upstream tier` | 400 | gateway.go:362 (nguồn :711) | route không cho tier zen/go |
| `invalid_request_error` | `prepare zen upstream request: <cause>` / `prepare go upstream request: <cause>` | 400 | gateway.go:362 (nguồn :730) | bridge không encode được body (chỉ khi **cross-protocol**) |
| `invalid_request_error` | `request contains unsupported JSON values` | 400 | gateway.go:362 (nguồn :737) | prepared body không marshal được |
| `client_canceled` | `client closed request` | 499 | gateway.go:393 / :634 | client ngắt kết nối trước khi upstream trả lời |
| `proxy_error` | `all proxies are unavailable; the upstream could not be reached` | 503 | gateway.go:401 / :639 | lỗi cuối là `errProxyUnavailable` |
| `upstream_timeout` | `upstream request timed out` | 504 | gateway.go:407 / :643 | hết request budget (`context.DeadlineExceeded`) |
| `free_tier_ip_blocked` | `OpenCode is blocking free-model traffic from the IP this gateway leaves from, so retrying now cannot succeed. Route the request through another exit or wait for the block to expire.` | 503 | gateway.go:415 (const abuse.go:24) | mọi exit bị OpenCode chặn IP free-tier |
| `free_tier_lane_paused` | `The anonymous free tier is paused after repeated upstream failures. Configure an authenticated key, wait for the pause to expire, or restart the gateway after confirming the upstream block has lifted.` | 503 | gateway.go:422 (const abuse.go:35) | circuit breaker của anonymous lane mở |
| `upstream_error` | `all upstream attempts failed` | 502 | gateway.go:425 / :646 | mọi attempt thất bại |
| `upstream_error` | `failed to read upstream response` | 502 | gateway.go:484 / :677 | đọc body upstream lỗi |
| `upstream_error` | `unsupported upstream response` | 502 | gateway.go:495 / :510 | collapse SSE thất bại, hoặc convert response thất bại |
| `upstream_error` | *message của chính upstream* | status upstream | gateway.go:768 (`copyErrorResponse`) | upstream trả non-2xx |
| `rotation_unavailable` | `该分组暂无可用模型（已暂停、下架、额度不足或重试次数已用尽），请稍后重试` | 503 | gateway/rotation.go:186 | nhóm rotation hết model dùng được |
| `rotation_unavailable` | `分组请求整体超时` | 504 | gateway/rotation.go:188 | hết budget trong lúc duyệt nhóm |
| `server_error` | `internal server error` | 500 | telemetry/recovery.go:17 | panic trong handler |
| *(plain text)* | `service unavailable` | 503 | gateway/runtime.go:210 | chưa có runtime (không phải JSON) |
| *(net/http)* | `404 page not found` / `Method Not Allowed` | 404 / 405 | stdlib | path lạ / sai method |

### 1.2 Message lỗi routing của catalog (`internal/models/catalog.go`)

Được ghi nguyên văn vào 400 `invalid_request_error`, với tên model thay bằng **alias của client** (`substituteModelName`, gateway.go:846):

- `model %q is not free and models.free_only is enabled` — catalog.go:174
- `model %q is disabled after a failed availability check; restore it in the WebUI or wait for the next probe` — catalog.go:272
- `model %q is not available in the configured Zen or Go pools` — catalog.go:292
- `model %q is disabled after a failed availability check` — catalog.go:380 (Playground)
- `model %q is not available in the selected %s key tier` — catalog.go:391
- `model %q uses an upstream protocol that is not available on the selected %s key tier` — catalog.go:394
- `no %s key is configured` — catalog.go:375
- `selected key tier must be zen or go` — catalog.go:368

`%s` = `zen` hoặc `go`.

### 1.3 Cách lỗi upstream được parse (`copyErrorResponse`, gateway.go:755–769)

1. Đọc tối đa 4 MiB body upstream.
2. Giữ nguyên status upstream **và** header `Retry-After`.
3. Message = first non-empty của: `error.message` → `message` (top-level) → **`http.StatusText(status)`**.
4. Mã local vẫn là `upstream_error` (nằm ở `error.type`); mã của upstream được giữ lại ở `error.code` (§0.2).
5. Alias được thay thế để không lộ model ID thật.

---

## 2. Lỗi validation của protocol (đều ra HTTP 400, `invalid_request_error`)

Chỉ phát sinh khi **cross-protocol** (client protocol ≠ protocol của model upstream); cùng protocol thì body được pass-through nguyên vẹn (`internal/protocol/bridge.go:149`).
Tất cả đều bị prefix `prepare zen upstream request: ` / `prepare go upstream request: `.

### 2.1 Chat / Responses / Anthropic request (`internal/protocol/request.go`)

| Message | Vị trí |
|---|---|
| `messages[%d] must be an object` | request.go:448, :567 |
| `messages[%d]: %w` (bọc lỗi content block) | request.go:453, :512, :577, :585, :595 |
| `messages[%d].tool_calls[%d] must be an object` | request.go:467 |
| `messages[%d] has unsupported role %q` | request.go:491, :591 |
| `tools[%d] must be a function tool` | request.go:497 |
| `instructions: %w` | request.go:524 |
| `input[%d] must be an object` | request.go:534 |
| `input[%d] has unsupported Responses item type %q` | request.go:568 |
| `input must be a string or array` | request.go:572 |
| `tools[%d] must be an object` | request.go:577, :602 |
| `system: %w` | request.go:607 |
| `unsupported input protocol %q` | request.go:659 |
| `messages[%d].tool_result[%d]: %w` | request.go:669 |
| `messages[%d]: tool image result requires a call ID` | request.go:672 |
| `unsupported output protocol %q` | request.go:725 |
| `file content is not valid inside Responses instructions; send it as a message input item` | request.go:736 |
| `file %q cannot be represented by Anthropic Messages without file data or a URL` | request.go:748 |

### 2.2 Ghép tool-call / tool-result (request.go)

| Message | Vị trí |
|---|---|
| `messages[%d]: assistant message appears before tool results for %s` | request.go:855 |
| `messages[%d]: assistant message contains tool results` | request.go:858 |
| `messages[%d]: tool call must contain id and name` | request.go:872 |
| `messages[%d]: duplicate tool call id %q` | request.go:875 |
| `messages[%d]: user message contains tool calls` | request.go:898 |
| `messages[%d]: duplicate tool result %q` | request.go:902 |
| `messages[%d]: tool result %q has no pending tool call` | request.go:905 |
| `tool calls are missing results for %s` | request.go:940 |

### 2.3 Content block (`internal/protocol/content.go`, `tool_images.go`)

| Message | Vị trí |
|---|---|
| `OpenAI content block must be an object` | content.go:32 |
| `unsupported OpenAI content block type %q` | content.go:51 |
| `unsupported OpenAI content value` | content.go:59 |
| `Anthropic content block must be an object` | content.go:78 |
| `unsupported Anthropic content block type %q` | content.go:126 |
| `content[%d] must be an object` | tool_images.go:39 |
| `content[%d] requires text` | tool_images.go:44 |
| `content[%d] has an unsupported or conflicting image source` | tool_images.go:57 |
| `content[%d] requires an image URL without a file reference` | tool_images.go:60 |
| `content[%d] contains an unsupported block alongside tool images` | tool_images.go:63 |
| `content[%d] has an invalid image data URI` | tool_images.go:81 |

---

## 3. Lỗi giữa stream (SSE, HTTP 200 đã commit)

| errorType | Message | Vị trí |
|---|---|---|
| `upstream_error` | *message của upstream* → fallback `upstream Chat stream error` | stream_parser.go:63, :66 |
| `upstream_error` | *`choice.error.message`* → fallback `upstream Chat stream failed` | stream_parser.go:112 |
| `upstream_error` | `upstream Anthropic stream failed` | stream_parser.go:191 |
| *(upstream `error.type`)* → `upstream_error` | *`error.message`* → fallback `upstream Anthropic stream error` | stream_parser.go:200 |
| *(upstream `error.type`/`error.code`)* → `upstream_error` | fallback `upstream Responses stream error` | stream_parser.go:220 |
| *(upstream `response.error.code`)* → `upstream_error` | fallback `upstream Responses request failed` | stream_parser.go:282 |
| `upstream_error` | `invalid upstream SSE JSON: %w` / `unexpected [DONE] terminator for %s stream` | stream_parser.go:34, :40 |
| `upstream_error` | `upstream SSE stream ended before a terminal event` | stream.go:156 |
| `upstream_error` | `upstream SSE stream failed: %v` | stream.go:158 |
| `upstream_error` | `当前上游模型的输出中断；本次响应已结束，请重新发起请求。` | rotation_stream.go:126 |
| `upstream_error` | `upstream stream failed` | stream_emitter.go:176, collapse.go:46 |

Shape frame: Anthropic = `event: error` + `{"type":"error","error":{"type","message"}}`;
Responses = `event: response.failed` + `response.error.code` = errorType;
Chat = `{"error":{"message","type","param":null,"code":null}}` + `data: [DONE]`.

---

## 4. Admin API — 32 mã lỗi (`error.code`)

| `code` | Message | HTTP | Vị trí | Trigger | Endpoint |
|---|---|---|---|---|---|
| `rate_limited` | `too many login attempts; try again later` | 429 | admin/auth.go:47 | >5 lần login sai / IP / 5 phút | POST /api/auth/login |
| `invalid_request` | *text lỗi decode JSON* / `multiple JSON values` | 400 | admin/auth.go:55, server.go:210/273, api_keys.go:114/181, availability.go:23/42, rotation.go:25/65, debug.go:81 | body không hợp lệ | nhiều |
| `invalid_credentials` | `invalid username or password` | 401 | admin/auth.go:64 | sai user/pass | POST /api/auth/login |
| `internal_error` | `could not create session` | 500 | admin/auth.go:69, :74 | `crypto/rand` lỗi | POST /api/auth/login |
| `authentication_required` | `login required` | 401 | admin/auth.go:122 | thiếu cookie session | mọi route cần auth |
| `authentication_required` | `session expired or invalid` | 401 | admin/auth.go:134 | session không tồn tại/hết hạn | mọi route cần auth |
| `authentication_required` | `session is no longer valid` | 401 | admin/auth.go:139 | user/pass đổi sau khi login | mọi route cần auth |
| `csrf_failed` | `invalid CSRF token` | 403 | admin/auth.go:161 | thiếu/sai `X-CSRF-Token` | route mutating |
| `origin_failed` | `request origin does not match this server` | 403 | admin/auth.go:171 | Origin lệch | route mutating |
| `verification_failed` | `current password is incorrect` | 403 | admin/auth.go:194 | sai mật khẩu hiện tại | PUT /api/account |
| `verification_failed` | `password verification failed` | 403 | admin/server.go:278 | sai mật khẩu khi reveal | POST /api/config/reveal |
| `account_update_failed` | *`err.Error()`* | 400 | admin/auth.go:204 | Apply lỗi | PUT /api/account |
| `not_found` | `management endpoint not found` | 404 | admin/server.go:92 | path `/api/*` lạ | bất kỳ |
| `invalid_server_keys` | `unknown or stale secret id %q` / `each item must contain id or value` | 400 | admin/server.go:216 | secret id cũ | PUT /api/config |
| `invalid_zen_keys` | *hai message trên* | 400 | admin/server.go:221 | như trên, cho `zen_keys` | PUT /api/config |
| `invalid_go_keys` | *hai message trên* | 400 | admin/server.go:226 | như trên, cho `go_keys` | PUT /api/config |
| `invalid_proxies` | *hai message trên* | 400 | admin/server.go:231 | như trên, cho `proxies` | PUT /api/config |
| `configuration_rejected` | *`err.Error()`* (validate config) | 400 | admin/server.go:249 | Apply từ chối config | PUT /api/config |
| `reload_failed` | *`err.Error()`* | 400 | admin/server.go:260 | không reload được config.json | POST /api/config/reload |
| `stream_unsupported` | `streaming is unavailable` | 500 | admin/server.go:312 | ResponseWriter không phải Flusher | GET /api/logs/stream |
| `generation_failed` | `无法生成密钥` | 500 | admin/api_keys.go:121 | `crypto/rand` lỗi | POST /api/api-keys |
| `key_create_failed` | `名称需为 1–80 个字符` | 400 | admin/api_keys.go:68 | tên rỗng/quá 80 rune | POST /api/api-keys |
| `key_create_failed` | `自定义 Key 需为 16–256 位` | 400 | admin/api_keys.go:81 | độ dài custom key sai | POST /api/api-keys |
| `key_create_failed` | `Key 仅支持英文字母、数字、短横线、下划线和点` | 400 | admin/api_keys.go:85 | charset sai | POST /api/api-keys |
| `key_create_failed` | `最多创建 200 个 API Key` | 400 | admin/api_keys.go:89 | đã có 200 key | POST /api/api-keys |
| `key_create_failed` | `此 API Key 已存在` | 400 | admin/api_keys.go:94 | trùng fingerprint | POST /api/api-keys |
| `key_update_failed` | `API Key 不存在，请刷新列表` | 400 | admin/api_keys.go:147 | id không tồn tại | PATCH /api/api-keys/{id} |
| `key_update_failed` | `名称需为 1–80 个字符` | 400 | admin/api_keys.go:68 | rename sai | PATCH /api/api-keys/{id} |
| `key_update_failed` | `至少保留一个启用的 API Key，请先创建替代密钥` | 400 | admin/api_keys.go:170 | sẽ không còn key bật | PATCH /api/api-keys/{id} |
| `key_delete_failed` | `API Key 不存在，请刷新列表` | 400 | admin/api_keys.go:147 | id không tồn tại | DELETE /api/api-keys/{id} |
| `key_delete_failed` | `至少保留一个启用的 API Key，请先创建替代密钥` | 400 | admin/api_keys.go:170 | sẽ không còn key bật | DELETE /api/api-keys/{id} |
| `restore_failed` | `gateway runtime is unavailable` | 400 | admin/availability.go:27 (gateway/availability.go:38) | chưa có runtime | POST /api/models/restore |
| `restore_failed` | `unknown free model` | 400 | admin/availability.go:27 (gateway/availability.go:45) | không phải free model | POST /api/models/restore |
| `restore_failed` | *lỗi ghi file availability* | 400 | models/availability.go:134 | file không ghi được | POST /api/models/restore |
| `invalid_request` | `manual is required` | 400 | admin/availability.go:46 | thiếu field `manual` | POST /api/models/manual |
| `manual_failed` | `gateway runtime is unavailable` / `unknown free model` / *lỗi ghi file* | 400 | admin/availability.go:50 | — | POST /api/models/manual |
| `rotation_rejected` | `首个有效输出超时需为 1–300 秒` | 400 | config/rotation.go:60 | `first_output_seconds` sai | PUT /api/rotation |
| `rotation_rejected` | `两个分组的别名不能相同` | 400 | config/rotation.go:63 | alias trùng nhau | PUT /api/rotation |
| `rotation_rejected` | `别名需为 1–64 位英文字母、数字、点、短横线或下划线` | 400 | config/rotation.go:68 | sai pattern alias | PUT /api/rotation |
| `rotation_rejected` | `别名不能与真实模型 ID 相同` | 400 | config/rotation.go:71 | alias che model thật | PUT /api/rotation |
| `rotation_rejected` | `模型顺序包含重复、空值或不属于该分组的模型` | 400 | config/rotation.go:76 | order sai | PUT /api/rotation |
| `rotation_rejected` | `SOTA 组必须包含指定的五个模型` | 400 | config/rotation.go:81 | order SOTA sai | PUT /api/rotation |
| `rotation_rejected` | `模型列表已更新，请刷新后重新排序` | 400 | admin/rotation.go:39 | độ dài order ≠ hiện tại | PUT /api/rotation |
| `rotation_rejected` | `只能调整组内模型的顺序` | 400 | admin/rotation.go:43 | model không thuộc nhóm | PUT /api/rotation |
| `rotation_rejected` | `请选择该组当前可用的 Go 模型` | 400 | rotation/state.go:313 | model không khả dụng | POST /api/rotation/current |
| `rotation_rejected` | `轮转状态保存失败` | 400 | rotation/state.go:319 | không ghi được rotation state | POST /api/rotation/current |
| `debug_rate_limited` | `too many Playground requests; retry in one minute` | 429 | admin/debug.go:76 | >12 req/IP/phút | POST /api/debug/inference |
| `invalid_protocol` | `protocol must be chat, responses, or anthropic` | 400 | admin/debug.go:85 | protocol sai | POST /api/debug/inference |
| `invalid_request` | `request must be a JSON object` | 400 | admin/debug.go:89 | `request` không phải object | POST /api/debug/inference |
| `invalid_request` | `request.model is required` | 400 | admin/debug.go:96 | thiếu model | POST /api/debug/inference |
| `invalid_request` | `request contains unsupported JSON values` | 400 | admin/debug.go:125 | marshal lỗi | POST /api/debug/inference |
| `invalid_key_mode` | `key.mode must be auto or selected` | 400 | admin/debug.go:103 | mode sai | POST /api/debug/inference |
| `unknown_key_id` | `selected key is not configured` | 400 | admin/debug.go:110 | fingerprint lạ | POST /api/debug/inference |
| `model_unavailable_for_key_tier` | `selected key tier must be zen or go` | 400 | admin/debug.go:118 (catalog.go:368) | tier sai | POST /api/debug/inference |
| `model_unavailable_for_key_tier` | `no %s key is configured` | 400 | catalog.go:375 | thiếu key của tier | POST /api/debug/inference |
| `model_unavailable_for_key_tier` | `model %q is disabled after a failed availability check` | 400 | catalog.go:380 | bị probe disable | POST /api/debug/inference |
| `model_unavailable_for_key_tier` | `model %q is not free and models.free_only is enabled` | 400 | catalog.go:174 | free_only | POST /api/debug/inference |
| `model_unavailable_for_key_tier` | `model %q is not available in the selected %s key tier` | 400 | catalog.go:391 | tier không có model | POST /api/debug/inference |
| `model_unavailable_for_key_tier` | `model %q uses an upstream protocol that is not available on the selected %s key tier` | 400 | catalog.go:394 | protocol không có ở tier | POST /api/debug/inference |
| `model_unavailable_for_key_tier` | `gateway runtime is unavailable` | 400 | gateway/runtime.go:514 | chưa có runtime | POST /api/debug/inference |
| `debug_request_failed` | `could not construct Gateway request` | 500 | admin/debug.go:133 | `http.NewRequestWithContext` lỗi | POST /api/debug/inference |
| `debug_unavailable` | `no local server key is configured` | 503 | admin/debug.go:138 | mọi server key bị tắt | POST /api/debug/inference |

**Danh sách 32 code admin:** `account_update_failed`, `authentication_required`, `configuration_rejected`,
`csrf_failed`, `debug_rate_limited`, `debug_request_failed`, `debug_unavailable`, `generation_failed`,
`internal_error`, `invalid_credentials`, `invalid_go_keys`, `invalid_key_mode`, `invalid_protocol`,
`invalid_proxies`, `invalid_request`, `invalid_server_keys`, `invalid_zen_keys`, `key_create_failed`,
`key_delete_failed`, `key_update_failed`, `manual_failed`, `model_unavailable_for_key_tier`, `not_found`,
`origin_failed`, `rate_limited`, `reload_failed`, `restore_failed`, `rotation_rejected`,
`stream_unsupported`, `unknown_key_id`, `verification_failed`.

---

## 5. Lỗi Admin trong response 200 (không phải envelope lỗi)

- `GET /api/model-quotas` → `accounts[].error` (mỗi key một chuỗi), `status` = `ok`/`unavailable`/`stale`:
  `OpenCode 返回的额度格式无法识别`, `OpenCode 返回的额度窗口不完整`, `额度查询地址无效`,
  `暂时无法连接 OpenCode，请稍后刷新`, `Go API Key 无效或已被撤销`, `此 Key 未获准读取 Go 套餐额度`,
  `额度查询过于频繁，请稍后刷新`, `OpenCode 额度接口返回 HTTP %d` — `internal/admin/model_quotas.go:67–112`.
- `GET /api/rotation` → `persistence_error`: `轮转状态写入失败，请检查磁盘空间和文件权限` — `internal/rotation/state.go:345`.
- `GET /api/debug/models`, `GET /api/monitor` → `route_error` (text của catalog, xem §1.2).
- `POST /api/debug/inference` → luôn 200; verdict `key_test` ∈ `transport_error`, `usable`, `rejected`,
  `rate_limited`, `unavailable`, `upstream_error`, `request_error` — `internal/admin/debug.go:272–289`.
- `GET /api/logs/stream` → SSE `gap` event: `older log events have expired`, `slow client missed log events`.

---

## 6. Health / status identifiers

`GET /healthz` (200 hoặc 503, không dùng error envelope):
`model_catalog_pending`, `model_catalog_empty`, `model_catalog_stale`, `no_upstream_keys`,
`no_healthy_proxies` — `internal/gateway/health.go:66–82`.
`status` ∈ `ok`/`degraded`/`starting`; `models.status` ∈ `ready`/`pending`/`empty`/`stale`.

---

## 7. Nhận xét & lỗ hổng thiết kế

1. **Mã lỗi của upstream giờ được giữ lại** ở `error.code` (trước đây luôn `null`, xem §0.2),
   nhưng `error.type` của envelope OpenAI vẫn là `upstream_error` và envelope Anthropic vẫn
   không có slot `code` — client đọc được `unknown_parameter` hay `rate_limit_error` qua
   `code`, còn `type` thì không phân biệt được.
2. **`code` của admin không được WebUI dùng** — `webui/js/api.js:39` chỉ đọc `data.error.message`;
   grep `error.code` trong `webui/` không có kết quả. `webui/js/i18n.js` không có entry per-code.
   UI chỉ đặc biệt xử lý HTTP 401 và 204.
3. **Nhiều nguyên nhân gộp chung một code** (`key_create_failed`, `key_update_failed`,
   `key_delete_failed`, `rotation_rejected`, `restore_failed`, `manual_failed`) — chỉ message phân biệt được.
4. **`invalid_protocol` sai lệch**: message nói "chat, responses, or anthropic" nhưng
   `protocol.Valid` (`internal/protocol/protocol.go:34`) cũng nhận `systemone`.
5. **Lỗi conversion response không lộ ra client** — client luôn thấy 502 `unsupported upstream response`,
   text chi tiết chỉ vào log (`gateway.go:495, :510`).
6. **Validation chỉ chạy khi cross-protocol** — cùng protocol thì upstream tự reject. Ngoại lệ
   duy nhất là giới hạn output token: nó luôn được chuyển sang tên đúng của protocol đích
   (`applyTokenLimitAlias`, `internal/protocol/request.go:46`), nên body pass-through không
   mang field của protocol khác.
7. **Panic luôn trả envelope Chat** kể cả trên `/v1/messages` và `/api/*` (`protocol.Chat` hard-code,
   `internal/telemetry/recovery.go:17`).
8. **`httpx.WriteJSONStatus` nuốt lỗi encode** (`_ = json.NewEncoder(w).Encode(value)`) — body không
   serialize được sẽ bị cắt cụt mà không có mã lỗi nào.
9. **Không có literal `model not found`** trong repo — text "model not found" chỉ có thể đến từ upstream
   qua `copyErrorResponse`; gateway tự nói `model %q is disabled after a failed availability check…`.
10. **Alias không bao giờ lộ model ID thật** trong message (`substituteModelName`, gateway.go:846).

---

## 8. Hằng số & sentinel liên quan

| Tên | Giá trị | Khai báo |
|---|---|---|
| `statusClientClosedRequest` | `499` | internal/gateway/gateway.go:57 |
| `maxRequestBody` | `32 << 20` (32 MiB) | internal/gateway/gateway.go:38 |
| `proxyUnavailableMessage` | `all proxies are unavailable; the upstream could not be reached` | internal/gateway/gateway.go:65 |
| `freeTierBlockedMessage` | (xem §1.1) | internal/gateway/abuse.go:24 |
| `anonymousLanePausedMessage` | (xem §1.1) | internal/gateway/abuse.go:35 |
| `freeTierBlockTTL` | `15 * time.Minute` | internal/gateway/abuse.go:41 |
| `abuseProbeLimit` | `16 << 10` | internal/gateway/abuse.go:52 |
| `defaultMaxAnonymousExits` | `8` | internal/gateway/upstream.go:337 |
| `errFreeTierIPBlocked` | `free tier blocked the egress IP` | internal/gateway/abuse.go:18 |
| `errAnonymousLanePaused` | `anonymous lane paused by its circuit breaker` | internal/gateway/abuse.go:30 |
| `errDeterministicClientRejection` | `upstream rejected the request deterministically` | internal/gateway/abuse.go:110 |
| `errProxyUnavailable` | `proxy unavailable` | internal/gateway/first_event.go:84 |
| `errFirstEventTimeout` | `upstream first SSE event timed out: %w` | internal/gateway/first_event.go:25 |
| `errAttemptTimeout` | `upstream attempt timed out: %w` | internal/gateway/first_event.go:26 |
| `errStreamUpstreamFailure` | `upstream stream failure delivered` | internal/protocol/stream.go:20 |
| `errStreamNormalTermination` | `upstream stream terminated normally` | internal/protocol/stream.go:24 |
| `errSSEUnexpectedEOF` | `unexpected end of SSE stream` | internal/protocol/stream.go:27 |
| `Chat` / `Responses` / `Anthropic` / `SystemOne` | `chat` / `responses` / `anthropic` / `systemone` | internal/protocol/protocol.go:21–30 |

Route: `/v1/chat/completions`, `/v1/responses`, `/v1/messages`, `/v1/systemone`, `/v1/models`, `/healthz`.
