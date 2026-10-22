# Env overlay cho Docker (phương án B: `O2A_*` phủ lên config JSON)

Tài liệu này giải thích cách cấu hình container **chỉ bằng biến môi trường**,
không cần sửa `config.json` trên host.

## Cơ chế hoạt động

```
.env  →  `docker compose` environment  →  entrypoint merge bằng jq
       →  /var/lib/opencode2api/config.json  →  app Go đọc config
```

1. Copy `.env.example` thành `.env` rồi điền giá trị. `docker compose` tự đọc
   file `.env` cùng thư mục (không cần khai báo `env_file`).
2. `docker-compose.yml` truyền toàn bộ biến `O2A_*` vào container dạng
   `${O2A_XXX:-}` — unset/rỗng trong `.env` thành chuỗi rỗng trong container.
3. **Mỗi lần container start**, `docker-entrypoint.sh` dùng `jq` merge các
   `O2A_*` đang **được set và khác rỗng** vào config runtime
   `/var/lib/opencode2api/config.json` (file nằm trên volume
   `opencode2api-state`, persist giữa các lần restart).
   - Không chỉ lần seed đầu: lần restart nào env cũng phủ lên lại.
   - Lần đầu chưa có config runtime thì entrypoint seed từ
     `/app/config.example.json` **có sẵn trong image**, rồi mới merge env.
     Compose cố tình **không** đặt `CONFIG_SEED_PATH` và **không** bind mount
     file seed: `configs:` của compose là bind mount nên file nguồn phải tồn
     tại trên host, mà `config.json` bị `.gitignore` — trên clone sạch
     (Dokploy, CI, máy mới) điều đó gây lỗi
     `bind source path does not exist` và container không tạo được.
4. App Go đọc config runtime như bình thường, không biết gì về env.

## Seed bằng config.json riêng (tùy chọn)

Mặc định container tự seed từ `config.example.json` trong image rồi áp `O2A_*`,
nên **không cần file nào trên host**. Nếu muốn seed bằng config riêng thì chọn
một trong hai cách:

- **Mount read-only + trỏ `CONFIG_SEED_PATH`** (đường dẫn phải tồn tại trước khi
  container start — trên Dokploy hãy dùng volume/secret của nền tảng thay vì
  file trong repo):

  ```yaml
  environment:
    CONFIG_SEED_PATH: /run/config/opencode2api.json
  volumes:
    - ./config.json:/run/config/opencode2api.json:ro
  ```

- **Ghi đè sau khi container chạy** (không cần file lúc tạo container):

  ```bash
  docker compose cp config.json opencode2api:/var/lib/opencode2api/config.json
  docker compose restart opencode2api
  ```

Lưu ý: cách này chỉ ghi đè được khi volume đã có config; lần restart sau, `O2A_*`
vẫn tiếp tục phủ lên những trường mà env đang set.

## Thứ tự ưu tiên

Với mỗi trường config:

```
biến _FILE  >  biến thường  >  giá trị trong JSON
```

- **Unset hoặc rỗng = không override**, giữ nguyên giá trị trong JSON.
- Nhóm `_FILE` (`O2A_SERVER_KEYS_FILE`, `O2A_ZEN_KEYS_FILE`,
  `O2A_GO_KEYS_FILE`, `O2A_PROXIES_FILE`, `O2A_WEBUI_PASSWORD_FILE`) đọc nguyên
  nội dung file theo **đường dẫn trong container** (cần mount volume/secret cho
  path đó). Khi `_FILE` được set và đọc được, nó **thắng** biến thường cùng nhóm
  kể cả khi biến thường cũng được set.
- Không có chuyện env "xoá" giá trị JSON bằng chuỗi rỗng: chuỗi rỗng luôn bị bỏ qua.

## Bảng biến đầy đủ

Tất cả đều optional.

Bảng dưới đây liệt kê **mọi trường** của `config.example.json`, nên overlay `O2A_*`
giờ phủ kín toàn bộ config: không còn trường nào buộc phải sửa `config.json` bằng
tay trong container.

| Biến môi trường                               | Ghi vào config                            | Format / giá trị hợp lệ                                                                      | Ví dụ                                                      |
| --------------------------------------------- | ----------------------------------------- | -------------------------------------------------------------------------------------------- | ---------------------------------------------------------- |
| `O2A_SERVER_KEYS`                             | `server_keys`                             | Comma-separated, mỗi key một chuỗi                                                           | `sk-local-aaa111,sk-local-bbb222`                          |
| `O2A_SERVER_KEYS_FILE`                        | `server_keys` (thắng trên)                | Path trong container; nội dung comma-separated hoặc mỗi dòng một key                         | `/run/secrets/server_keys`                                 |
| `O2A_ZEN_KEYS`                                | `zen_keys`                                | Comma-separated                                                                              | `zen-key-aaa111,zen-key-bbb222`                            |
| `O2A_ZEN_KEYS_FILE`                           | `zen_keys` (thắng trên)                   | Path trong container, format như trên                                                        | `/run/secrets/zen_keys`                                    |
| `O2A_GO_KEYS`                                 | `go_keys`                                 | Comma-separated                                                                              | `go-key-aaa111,go-key-bbb222`                              |
| `O2A_GO_KEYS_FILE`                            | `go_keys` (thắng trên)                    | Path trong container, format như trên                                                        | `/run/secrets/go_keys`                                     |
| `O2A_ANONYMOUS`                               | `anonymous`                               | `true` / `false`                                                                             | `true`                                                     |
| `O2A_UPSTREAM_ZEN`                            | `upstream.zen`                            | URL                                                                                          | `https://opencode.ai/zen`                                  |
| `O2A_UPSTREAM_GO`                             | `upstream.go`                             | URL                                                                                          | `https://opencode.ai/zen/go`                               |
| `O2A_UPSTREAM_ANONYMOUS`                      | `upstream.anonymous`                      | URL của lane anonymous                                                                       | `https://opencode.ai/inference`                            |
| `O2A_PROXIES`                                 | `proxies`                                 | Comma-separated: `direct`, `http(s)://`, `socks5(h)://`                                      | `direct,http://user:pass@127.0.0.1:7890`                   |
| `O2A_PROXIES_FILE`                            | `proxies` (thắng trên)                    | Path trong container; mỗi dòng một entry, hỗ trợ comment `#`, `;`, `//`                      | `/run/config/proxies.txt`                                  |
| `O2A_PROXYFILE`                               | `proxyfile`                               | Path (container phải đọc được)                                                               | `/var/lib/opencode2api/proxies.txt`                        |
| `O2A_RETRY_MAX_ATTEMPTS`                      | `retry.max_attempts`                      | Số nguyên                                                                                    | `3`                                                        |
| `O2A_RETRY_TIMEOUT_SECONDS`                   | `retry.timeout_seconds`                   | Số (giây)                                                                                    | `300`                                                      |
| `O2A_MODELS_REFRESH_SECONDS`                  | `models.refresh_seconds`                  | Số nguyên, tối thiểu 1                                                                       | `300`                                                      |
| `O2A_MODEL_ALIASES`                           | `models.aliases`                          | **JSON object** `{"tên-client":"model-thật"}`; sai định dạng bị bỏ qua                       | `{"muse-spark-1.3":"muse-spark-1.3-contributor-free"}`     |
| `O2A_MODELS_PROTOCOLS`                        | `models.protocols`                        | **JSON object** `{"tên-model":"protocol"}`; protocol: `chat`, `responses`, `anthropic`       | `{"custom-model":"chat"}`                                  |
| `O2A_MODELS_FREE_ONLY`                        | `models.free_only`                        | `true` / `false`; chỉ expose và route model miễn phí                                         | `false`                                                    |
| `O2A_LOG_LEVEL`                               | `logging.level`                           | `debug` / `info` / `warn` / `error`                                                          | `info`                                                     |
| `O2A_LOGGING_RING_SIZE`                       | `logging.ring_size`                       | Số nguyên 100–50000 (số dòng log giữ trong ring buffer)                                      | `2000`                                                     |
| `O2A_LOGGING_DUMP_REQUEST_BODIES`             | `logging.dump_request_bodies`             | `true` / `false`; cần log level `debug` mới ghi body ra ngoài                                | `false`                                                    |
| `O2A_PREFER`                                  | `prefer`                                  | `go` / `zen`                                                                                 | `go`                                                       |
| `O2A_REASONING_EFFORT`                        | `reasoning.effort`                        | `minimal`, `low`, `medium`, `high`, `xhigh`, `max`, `none`                                   | `high`                                                     |
| `O2A_REASONING_EFFORT_BY_MODEL`               | `reasoning.effort_by_model`               | **JSON object** `{"tên-model":"level"}`; model không có trong object dùng `reasoning.effort` | `{"claude-opus-5":"max"}`                                  |
| `O2A_ERRORS_SIMPLE_BODY`                       | `errors.simple_body`                      | `true` / `false`; lỗi API suy luận dùng `{"error":{"code","message"}}` cho mọi giao thức         | `false`                                                    |
| `O2A_PERFORMANCE_MAX_IDLE_CONNS`              | `performance.max_idle_conns`              | Số nguyên; idle connection giữ trong pool                                                    | `2048`                                                     |
| `O2A_PERFORMANCE_MAX_IDLE_CONNS_PER_HOST`     | `performance.max_idle_conns_per_host`     | Số nguyên; idle connection cho mỗi host                                                      | `256`                                                      |
| `O2A_PERFORMANCE_MAX_CONNS_PER_HOST`          | `performance.max_conns_per_host`          | Số nguyên; `0` = không giới hạn                                                              | `0`                                                        |
| `O2A_PERFORMANCE_IDLE_CONN_TIMEOUT_SECONDS`   | `performance.idle_conn_timeout_seconds`   | Số nguyên (giây); thời gian giữ idle connection                                              | `120`                                                      |
| `O2A_PERFORMANCE_CONNECT_TIMEOUT_SECONDS`     | `performance.connect_timeout_seconds`     | Số nguyên (giây); timeout thiết lập kết nối                                                  | `5`                                                        |
| `O2A_PERFORMANCE_FAILURE_COOLDOWN_SECONDS`    | `performance.failure_cooldown_seconds`    | Số nguyên (giây); cooldown cơ sở sau lỗi                                                     | `15`                                                       |
| `O2A_PERFORMANCE_ATTEMPT_TIMEOUT_SECONDS`     | `performance.attempt_timeout_seconds`     | Số nguyên (giây); `0` = dùng timeout của request                                             | `0`                                                        |
| `O2A_PERFORMANCE_FIRST_EVENT_TIMEOUT_SECONDS` | `performance.first_event_timeout_seconds` | Số nguyên (giây); `0` = tắt, tối đa 86400                                                    | `0`                                                        |
| `O2A_WEBUI_ENABLED`                           | `webui.enabled`                           | `true` / `false`                                                                             | `true`                                                     |
| `O2A_WEBUI_USERNAME`                          | `webui.username`                          | Chuỗi                                                                                        | `admin`                                                    |
| `O2A_WEBUI_PASSWORD`                          | `webui.password`                          | Chuỗi, tối thiểu 10 ký tự (sẽ được hash lúc start)                                           | `change-me-min-10-chars`                                   |
| `O2A_WEBUI_PASSWORD_FILE`                     | `webui.password` (thắng trên)             | Path trong container chứa password                                                           | `/run/secrets/webui_password`                              |
| `O2A_WEBUI_SESSION_TTL_MINUTES`               | `webui.session_ttl_minutes`               | Số nguyên 5–10080 (phút)                                                                     | `720`                                                      |
| `O2A_SOTA_ALIAS`                              | `rotation.sota.alias`                     | Chuỗi                                                                                        | `sota`                                                     |
| `O2A_SOTA_ORDER`                              | `rotation.sota.order`                     | Comma-separated, **phải đủ đúng 5 model SOTA**                                               | `deepseek-v4.1-flash,glm-5.3,grok-4.6,kimi-k3,qwen3.8-max` |
| `O2A_SWEET_ALIAS`                             | `rotation.sweet.alias`                    | Chuỗi                                                                                        | `sweet`                                                    |
| `O2A_SWEET_ORDER`                             | `rotation.sweet.order`                    | Comma-separated (để trống = giữ JSON)                                                        | `fast-a,fast-b`                                            |
| `O2A_FIRST_OUTPUT_SECONDS`                    | `rotation.first_output_seconds`           | Số nguyên 1–300 (0 sẽ về mặc định 30)                                                        | `15`                                                       |
| `O2A_LISTEN`                                  | `listen`                                  | `host:port`                                                                                  | `0.0.0.0:8080`                                             |
| `O2A_WEBUI_LISTEN`                            | `webui.listen`                            | `host:port`                                                                                  | `0.0.0.0:8081`                                             |

Ngoài ra compose vẫn giữ các biến cũ (không thuộc overlay `jq`, mà thành
`LISTEN_ADDRESS` / `WEBUI_LISTEN_ADDRESS` truyền thẳng cho entrypoint/binary):

| Biến compose                | Tác dụng                                          |
| --------------------------- | ------------------------------------------------- |
| `OPENCODE2API_LISTEN`       | Override API listen qua `LISTEN_ADDRESS`.         |
| `OPENCODE2API_WEBUI_LISTEN` | Override WebUI listen qua `WEBUI_LISTEN_ADDRESS`. |

## Lưu ý quan trọng

1. **Entrypoint chỉ validate được kiểu dữ liệu, không thay Go validate.** Với
   trường số nguyên và boolean, entrypoint tự kiểm tra: giá trị không hợp lệ
   (ví dụ `O2A_RETRY_MAX_ATTEMPTS=abc`, `O2A_ANONYMOUS=maybe`) bị **bỏ qua**
   (giữ JSON cũ) và log cảnh báo ra stderr, các trường khác vẫn được áp dụng.
   Nhưng với trường chuỗi/URL/`prefer`/`log level`, entrypoint ghi nguyên giá
   trị vào JSON — sai giá trị thì **app Go vẫn fail lúc start** khi validate
   config. Đừng trông chờ env tự "sửa" lỗi.
2. **Đổi listen cần recreate container.** `O2A_LISTEN` / `O2A_WEBUI_LISTEN`
   (và `OPENCODE2API_LISTEN` / `OPENCODE2API_WEBUI_LISTEN`) đổi địa chỉ process
   lắng nghe — `docker compose restart` (giữ container cũ) là chưa đủ, phải
   `docker compose up -d` để recreate. Đổi port nội bộ cũng phải sửa thêm port
   mapping và healthcheck (đang dùng cổng 8080).
   Khi cả hai cùng được set, `LISTEN_ADDRESS` / `WEBUI_LISTEN_ADDRESS` (từ
   `OPENCODE2API_LISTEN` / `OPENCODE2API_WEBUI_LISTEN`) **thắng** `O2A_LISTEN` /
   `O2A_WEBUI_LISTEN`, vì flag dòng lệnh có độ ưu tiên cao nhất lúc runtime.
3. **WebUI sửa config sẽ bị env phủ lại.** WebUI ghi vào config runtime trên
   volume; lần restart sau, entrypoint lại merge `O2A_*` lên trên. Nghĩa là với
   trường nào đang set env, **env luôn thắng** — sửa cùng trường đó trên WebUI
   chỉ có tác dụng tới lần restart tiếp theo. Muốn WebUI quản trị hẳn một
   trường thì bỏ biến env tương ứng khỏi `.env` (unset/rỗng).
4. **`O2A_WEBUI_PASSWORD` được hash lại mỗi lần start.** Khi biến này được set,
   entrypoint ghi plaintext vào `webui.password` và **xoá `webui.password_hash`**
   để app Go hash lại. Vì salt Argon2id đổi mỗi lần, `password_hash` trong file
   sẽ khác nhau sau mỗi lần restart — đây là hành vi bình thường, không phải lỗi
   cấu hình. Mật khẩu đăng nhập không đổi.
5. **Config runtime luôn được đặt mode `600`.** File chứa upstream key và
   credential proxy, entrypoint `chmod 600` sau khi seed và giữ nguyên mode này
   khi ghi đè.
6. **O2A\_\* chỉ có tác dụng trong container.** Chạy binary trực tiếp
   (`./opencode2api -config config.json`) thì entrypoint không chạy, env bị bỏ
   qua — vẫn cấu hình bằng `config.json` như cũ.
7. **File `.env` chứa secret** — không commit (đã có trong `.gitignore`), chỉ
   commit `.env.example` với placeholder.

### Khôi phục khi giá trị sai đã bị ghi vào volume

Overlay **ghi thẳng vào config runtime trên volume**
(`/var/lib/opencode2api/config.json`), nên một giá trị chuỗi sai — hoặc một số
nguyên vượt giới hạn `int` của Go — **vẫn nằm lại trong file** sau khi bạn bỏ biến
khỏi `.env`. Lần start sau không còn gì để phủ lên, app Go đọc đúng giá trị cũ và
**fail lúc start**, container crash-loop. Ví dụ `O2A_PREFER=bogus` để lại
`"prefer": "bogus"`, log báo:

```
configuration error error="prefer must be \"zen\" or \"go\""
```

Số nguyên vượt giới hạn (ví dụ `O2A_LOGGING_RING_SIZE=99999999999999999999`) qua
được bước kiểm tra chữ số của entrypoint nhưng bị Go từ chối khi unmarshal. Xem log
để biết trường nào đang hỏng:

```bash
docker compose logs opencode2api
```

**Phải bỏ (hoặc sửa) biến sai trong `.env` trước.** Overlay chạy lại mỗi lần
container start, nên nếu biến vẫn còn thì mọi chỉnh sửa file bên dưới sẽ bị ghi đè
ngược lại giá trị sai. Sau đó chọn một trong ba cách:

1. **Sửa/xoá đúng key trong file trên volume.** Khi container đang crash-loop,
   `docker compose exec` **không dùng được** (báo
   `is restarting, wait until the container is running`), nên dùng
   `docker compose run`:

   ```bash
   # xoá key hỏng (đổi .prefer thành trường mà log chỉ ra)
   docker compose run --rm --no-deps --entrypoint sh opencode2api -c \
     'f=/var/lib/opencode2api/config.json; jq "del(.prefer)" "$f" > /tmp/c.json && cat /tmp/c.json > "$f" && chown opencode2api:opencode2api "$f" && chmod 600 "$f"'
   ```

   Key bị xoá sẽ quay về mặc định của app (`prefer` mặc định là `go`), nên cách này
   dùng được cả khi bạn không nhớ giá trị hợp lệ.

2. **Ghi đè bằng file đã sửa** (nếu đã có sẵn `config.json` đúng):

   ```bash
   docker compose cp config.json opencode2api:/var/lib/opencode2api/config.json
   docker compose restart opencode2api
   ```

   `docker compose cp` chạy bằng root nên file có thể bị đổi chủ/mode; nếu app báo
   `permission denied` khi đọc config, hãy trả lại quyền bằng
   `chown opencode2api:opencode2api` + `chmod 600`.

3. **Xoá volume để seed lại từ đầu** (mất toàn bộ state: config runtime, cache
   model, rotation):

   ```bash
   docker compose down -v   # xoá luôn volume opencode2api_opencode2api-state
   docker compose up -d
   ```

   Container seed lại từ `/app/config.example.json` trong image rồi áp `O2A_*` như
   lần đầu.

Gọn nhất là tránh hẳn tình huống này bằng cách đặt **giá trị hợp lệ** cho biến
(`O2A_PREFER=go`): overlay sẽ ghi đè giá trị sai trong file, không cần sửa tay.

## Ví dụ deploy

### A. Anonymous lane + proxy direct, chỉ bằng `.env` (không sửa config.json)

```dotenv
OPENCODE2API_VERSION=latest
OPENCODE2API_PORT=8909
OPENCODE2API_WEBUI_PORT=8901

O2A_SERVER_KEYS=sk-local-aaa111
O2A_ANONYMOUS=true
O2A_PROXIES=direct
O2A_PREFER=zen
O2A_WEBUI_ENABLED=true
O2A_WEBUI_USERNAME=admin
O2A_WEBUI_PASSWORD=change-me-min-10-chars
O2A_LOG_LEVEL=info
```

```bash
cp .env.example .env   # rồi điền giá trị như trên
docker compose up -d --build
docker compose logs -f
```

`zen_keys` / `go_keys` để trống + `anonymous=true` là đủ điều kiện chạy lane
public (model `free` / zero-cost, xem README phần anonymous).

### B. Production: keys qua file (docker secret / mount read-only)

```dotenv
OPENCODE2API_VERSION=v1.2.3
O2A_SERVER_KEYS_FILE=/run/secrets/server_keys
O2A_ZEN_KEYS_FILE=/run/secrets/zen_keys
O2A_GO_KEYS_FILE=/run/secrets/go_keys
O2A_WEBUI_PASSWORD_FILE=/run/secrets/webui_password
O2A_ANONYMOUS=false
O2A_PREFER=go
O2A_RETRY_MAX_ATTEMPTS=3
O2A_RETRY_TIMEOUT_SECONDS=300
O2A_MODELS_REFRESH_SECONDS=300
O2A_LOG_LEVEL=warn
O2A_WEBUI_ENABLED=true
O2A_WEBUI_USERNAME=admin
```

Kèm thêm mount secret vào service (ngoài phạm vi compose mặc định), ví dụ:

```yaml
secrets:
  - server_keys
  - zen_keys
```

File secret chứa comma-separated keys hoặc mỗi key một dòng.

## Kiểm tra overlay có ăn không

```bash
# Xem entrypoint đã merge gì (log lúc start):
docker compose logs opencode2api | head -50
# So sánh config runtime thực tế:
docker compose exec opencode2api cat /var/lib/opencode2api/config.json
```

Secret trong config runtime hiển thị plaintext (đây là file nội bộ trên volume,
không commit). Không `docker compose cp` ngược file này ra host nếu không cần.
