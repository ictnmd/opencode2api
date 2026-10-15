# Kế hoạch merge `upstream/main` (v1.4.0) vào fork cá nhân

**Trạng thái:** ĐÃ THỰC HIỆN trên nhánh `merge/upstream-v1.4.0` (2 commit). **Chưa push** — chờ duyệt.
**Ngày lập:** phiên merge `52a1fad` × `25afadd`.
**Người lập:** Lead, tổng hợp từ 10 subagent điều tra + kiểm chứng độc lập.

> ## Kết quả thực hiện
>
> | Hạng mục | Kết quả |
> | --- | --- |
> | Branch làm việc | `merge/upstream-v1.4.0` (tách từ `main` @ `52a1fad`) |
> | Backup | tag `backup/pre-upstream-25afadd`, branch `backup/pre-upstream-25afadd-52a1fad` |
> | Commit 1 | `72c204b` — merge commit, resolve 6/6 hunk |
> | Commit 2 | `f510b01` — 4 việc bù (env overlay, alias metadata, telemetry, test) |
> | Verify | `go vet` / `go build` / `go test ./...` / `gofmt -l .` / `node --check` / `sh -n` / `docker build` — **tất cả PASS** |
> | Smoke test runtime | `/healthz` 200; `free_only` lọc **85 → 9** model; alias free trả **200** với response thật; alias trả phí **400** |
> | Push | **CHƯA** — `main` vẫn ở `52a1fad`, không đụng |
>
> **Rollback:** `git switch main` là đủ (mọi thay đổi nằm trên branch riêng).

---

## 1. Tóm tắt điều hành

| Hạng mục | Kết quả |
| --- | --- |
| Remote | `origin` = `ictnmd/opencode2api` (fork), `upstream` = `jasonxu114514/opencode2api` (gốc) |
| HEAD (fork) | `52a1fad` — đồng bộ với `origin/main`, working tree sạch |
| Đích merge | `upstream/main` = `25afadd` = **tag `v1.4.0`** |
| Merge-base | `77818be` |
| Chênh lệch | upstream **+22 commit**, fork **+8 commit** riêng |
| Conflict | **5 file / 6 hunk** — tất cả đều là "hai bên cùng chèn thêm" → **giữ cả hai** |
| Bản chất conflict | **Cơ học, không có xung đột ngữ nghĩa.** Không hunk nào cần chọn phe |
| Kiểm chứng | Đã merge thử + resolve + **build/vet/test/gofmt/`sh -n` PASS (exit 0)** |
| Rủi ro tổng | **THẤP** cho merge; **VỪA** cho 2 defect ngữ nghĩa có sẵn (không chặn) |

**Khuyến nghị: tiến hành merge.** Toàn bộ 6 hunk đã được resolve và kiểm chứng thực thi trên một git worktree tạm (không đụng working tree chính), cho kết quả build/vet/test sạch.

---

## 2. Phạm vi thay đổi

### 2.1 Fork có gì riêng (8 commit)
- **Docker/env overlay**: cấu hình container bằng biến `O2A_*` (`docker-entrypoint.sh`, `.env.example`, `docker-compose.yml`, `docs/env-overlay.md`, `Dockerfile` +`jq`).
- **Model aliases**: map tên client-facing → model ID thật (`internal/protocol/modelname.go`, `internal/config/config.go`, `internal/gateway/health.go`, `gateway.go`).
- **Capture tooling**: `capture-proxy.js`, `capture-config.json`, `header-capture-report.md`.
- Fork đã **xóa** `compose.yaml` (upstream không đụng → không conflict).

### 2.2 Upstream v1.4.0 có gì mới (22 commit)
- `models.free_only` — chỉ expose/route model miễn phí (#66)
- Split usage theo server key (#58)
- Expose `cost` + `reasoning` levels trong `/v1/models` (#65)
- Per-session token burn trong log/monitor rings/usage snapshots (#77)
- Fix anonymous lane lockup sau quota/network (#59)
- Bound mỗi attempt bằng full attempt deadline (#64)
- Floor `max_output_tokens` = 16 cho responses (#63)
- Báo 499 `client_canceled` khi client ngắt ở header phase (#76)
- Typed `CloneMap` deep copy, bỏ marshal round-trip (#78)

### 2.3 File thay đổi sau merge
**22 file** (`+580 / −88` so với HEAD), trong đó **17 file auto-merge sạch** và **5 file conflict**.

---

## 3. Chi tiết 6 conflict hunk + nội dung resolve

Tất cả resolve theo nguyên tắc **union — giữ cả hai phía, thứ tự fork-trước-upstream**.

### 3.1 `config.example.json` (1 hunk, dòng ~21–25)
```json
    "protocols": {},
    "aliases": {},
    "free_only": false
```
> `"aliases": {}` **phải thêm dấu phẩy** vì `free_only` nối tiếp trong cùng object `models`.

### 3.2 `internal/config/config.go` (1 hunk, dòng ~72–82)
Giữ field `Aliases` (comment 4 dòng + `json:"aliases,omitempty"`) **rồi** field `FreeOnly` (comment 2 dòng + `json:"free_only"`).

> ⚠️ **BẮT BUỘC giữ cả hai.** `config.go` dùng `dec.DisallowUnknownFields()`, và `gateway.go:81` gọi `catalog.SetFreeOnly(cfg.Models.FreeOnly)`. Bỏ `FreeOnly` → **không biên dịch**; bỏ `Aliases` → container **từ chối khởi động** (config.json có key lạ).

### 3.3 `README.md` (1 hunk, dòng ~323–327)
Giữ 2 dòng bảng: `models.aliases` và `models.free_only` (cả hai đều 109 ký tự, pipes cột 0/30/108 — trùng khít, không cần chỉnh đệm).

### 3.4 `internal/gateway/health.go` (1 hunk, dòng ~5–9) — import
```go
import (
	"net/http"
	"slices"
	"sort"
	"time"

	"opencode2api/internal/buildinfo"
	"opencode2api/internal/config"
	"opencode2api/internal/httpx"
	modelcatalog "opencode2api/internal/models"
	wire "opencode2api/internal/protocol"
)
```
> **Phải giữ CẢ `slices` VÀ `sort`.** Bản merge dùng `slices.Index`/`slices.Clone` (upstream, dòng 135–139) **và** `sort.Strings` (fork, dòng 259). Bỏ một cái → lỗi biên dịch.
> **Thứ tự gofmt là `slices` TRƯỚC `sort`** — đã kiểm chứng: đặt `sort` trước làm `gofmt -l` báo lỗi và **CI fail**.

### 3.5 `webui/app.js` (2 hunk, dòng ~152–156 và ~223–227)
```js
  $("#c-aliases").value = JSON.stringify(value.models.aliases || {}, null, 2);
  $("#c-free-only").checked = !!value.models.free_only;
```
```js
        aliases: JSON.parse($("#c-aliases").value || "{}"),
        free_only: $("#c-free-only").checked,
```
> ⚠️ Giữ **dấu phẩy cuối dòng `aliases`** (hunk 2) — bỏ → `SyntaxError`.

### 3.6 `webui/index.html` — auto-merge SẠCH
Đã kiểm chứng bản merge có **đủ cả hai** input: `#c-free-only` (dòng 576) và `#c-aliases` (dòng 596). Đối xứng load/save **28/28 id**, không id lệch.

---

## 4. Bằng chứng kiểm chứng (đã chạy thật)

Thực hiện trên **git worktree tạm** (`%TEMP%\o2a-trial-merge`), **không đụng working tree chính**. Worktree đã được dọn sau khi xong; repo vẫn sạch.

| Bước | Lệnh | Kết quả |
| --- | --- | --- |
| Merge thử | `git merge --no-commit --no-ff upstream/main` | Tái hiện **đúng 5 file conflict / 6 hunk** |
| Resolve | `resolve_conflicts.py` | **6/6 hunk**, không còn conflict marker |
| JSON | parse `config.example.json` | **Hợp lệ**, `models` = `aliases, free_only, protocols, refresh_seconds` |
| JS | `node --check webui/app.js` | **exit 0** |
| Format | `gofmt -l .` | **rỗng = sạch** |
| Vet | `go vet ./...` | **VET_OK** |
| Build | `go build -trimpath ./cmd/opencode2api` | **BUILD_OK** |
| Test | `go test ./... -count=1` | **PASS** — `config`, `gateway`, `protocol` ok |
| Alias test | `go test ./internal/gateway/ -run TestAlias -v` | **5/5 PASS** |
| Shell | `sh -n docker-entrypoint.sh` | **SH_OK** |
| **Tổng** | script `verify.sh` | **ALL CHECKS PASSED, exit 0** |

**Lưu ý môi trường:** máy này **không có Go toolchain** (không ở PATH, không ở `GOROOT`/`GOPATH`/Program Files). Mọi lệnh Go ở trên chạy qua Docker `golang:1.24-alpine`. Docker 29.8.0 + Node v24.21.0 có sẵn.

**Kiểm chứng ngữ nghĩa bổ sung (do Lead thực hiện):**
- `Metadata.Cost` (upstream thêm) **sống sót** qua merge → `internal/models/discovery.go:77`. Rủi ro "chặn build" do subagent 02 nêu đã **được loại trừ**.
- Mọi symbol của **cả hai bên** trong `gateway.go` đều còn: fork (`aliased` 17 hit, `resolved` 19 hit, `RewriteResponseModel` 2 hit) + upstream (`SetFreeOnly`, `client_canceled` 5 hit, `attemptDeadline` 3 hit).
- `CloneMap` typed deep copy **KHÔNG** làm mất alias rewrite (alias ghi vào request trước khi clone; rewrite nằm ở response path, không có `CloneMap`). Chốt hồi quy: `alias_test.go` — **5/5 PASS**.
- **Thứ tự alias × free_only đúng:** alias resolve trước (`gateway.go:168`), `free_only` lọc trong catalog trên **model thật** (`catalog.go:141/228/328`). Alias → model trả phí khi bật `free_only` ⇒ 400 "not free", **không lách được**; cũng tự động bị ẩn khỏi `/v1/models`. Đây là hành vi **đúng thiết kế**, không phải bug.

---

## 5. Việc phải làm SAU merge (không nằm trong conflict)

### 5.1 Env overlay thiếu `free_only` — 4 file, commit RIÊNG
`docker-entrypoint.sh` là **whitelist cứng 23 biến** (`apply_* VAR '.json.path' 'label'`), không có cơ chế generic. `free_only` xuất hiện **0 lần** trong entrypoint/`.env.example`/`docs/env-overlay.md`.

| File | Sửa |
| --- | --- |
| `docker-entrypoint.sh` | thêm sau dòng 239: `apply_bool O2A_MODELS_FREE_ONLY '.models.free_only' 'models.free_only'` |
| `.env.example` | thêm `O2A_MODELS_FREE_ONLY=` |
| `docker-compose.yml` | thêm `O2A_MODELS_FREE_ONLY: ${O2A_MODELS_FREE_ONLY:-}` (compose không forward biến chưa khai báo) |
| `docs/env-overlay.md` | thêm 1 dòng bảng |

> Tên biến đề xuất **`O2A_MODELS_FREE_ONLY`**, theo convention `O2A_MODELS_REFRESH_SECONDS`.
> **Thiếu mapping KHÔNG gây lỗi runtime** (Go zero-value `bool` = `false`; key không map vẫn được `jq` deep-merge giữ nguyên) — nhưng phá vỡ mô hình "cấu hình chỉ bằng `.env`" của fork.

### 5.2 Hai defect ngữ nghĩa CÓ SẴN (không do merge)

**R-A — Alias entry thiếu metadata mới của upstream** (`internal/gateway/health.go`, mức VỪA)
`aliasModelEntries` (dòng 286–289) chỉ set `reasoning`/`supports_reasoning`, **thiếu** `cost`, `reasoning_efforts`, `compat`, `default_reasoning_effort` mà entry model thật có (dòng 173–202). Hệ quả: client discovery-driven nhận entry alias "hạng hai" — đúng cái lỗi mà comment upstream muốn tránh.
→ Đề xuất: rút helper dùng chung `modelEntry(model, route, now, extra)`; `aliasModelEntries` truyền `extra = {"upstream_model": model}`.

**R-B — `forwardSystemOne` gán nhãn sai model** (`internal/gateway/gateway.go:429`, mức THẤP–VỪA)
`meta.Model = model` ghi tên **alias** thay vì ID thật, lệch với per-session burn (#77) và server-key usage (#58). **Đã xác nhận có sẵn ở HEAD (dòng 391)** — không do merge.
→ Đề xuất: sửa 1 dòng thành `meta.Model = route.ID`.

### 5.3 Tài liệu
- `README.zh-CN.md`: fork chưa bao giờ ghi docs `models.aliases` (đếm: `free_only` = 2, `aliases` = 0) → **drift tài liệu**.
- `README.md:249`: nên bổ sung `session`, `server_key_id` vào mô tả log.

### 5.4 Test còn thiếu
- **Không có test nào** cho `free_only` (upstream cũng không thêm test nào trong 22 commit).
- **Không có test** cho tương tác `alias × free_only` (alias → paid ⇒ 400).
- **CI KHÔNG chạy `go test`** — `checks.yml` chỉ chạy `go vet`, `go build`, `gofmt`, `node --check`, `sh -n`. Nghĩa là regression ở tầng test (kể cả mất alias) **sẽ không bị CI bắt**.

---

## 6. Rủi ro & cách giảm thiểu

| # | Rủi ro | Khả năng | Tác động | Phát hiện | Giảm thiểu |
| --- | --- | --- | --- | --- | --- |
| R1 | Mất `Aliases` hoặc `FreeOnly` khi resolve `config.go` | Thấp | **Cao** (không build / không boot) | `go build`, smoke test | Resolve union; đã kiểm chứng bằng build thật |
| R2 | `import` sai ở `health.go` | Thấp | Cao (không build) | `go build` | Giữ cả `slices`+`sort`, đúng thứ tự gofmt |
| R3 | JSON `config.example.json` sai dấu phẩy | Thấp | Vừa | parse JSON | Đã parse kiểm chứng |
| R4 | `gofmt` lệch → **CI fail** | **Đã từng xảy ra** | Vừa | `gofmt -l` | Đã sửa thứ tự import; `gofmt -l` rỗng |
| R5 | Alias entry thiếu metadata (R-A) | **Cao** (chắc chắn xảy ra) | Thấp | Test tay `/v1/models` | Helper dùng chung (§5.2) |
| R6 | `free_only: true` + cold start → chỉ còn model tên chứa "free" | Vừa | Vừa | Smoke test có `free_only` | Ghi docs cảnh báo; không bật mặc định |
| R7 | `rotation.Validate` không áp `freeOnly` → nhóm `sota` rỗng lúc runtime | Vừa | Vừa | Smoke test | Kiểm tra `O2A_SOTA_ORDER` khi bật `free_only` |
| R8 | Regression tầng test không bị CI bắt | **Cao** | Vừa | — | **Chạy `go test ./...` tay** (đã làm) |
| R9 | Push đè remote | Thấp | Cao | `git push` output | Push **fast-forward**, không force |
| R10 | Merge sạch nhưng nuốt logic | Thấp | Cao | `git diff` đối chiếu | Đã đối chiếu: 53/53 dòng fork + 44/44 dòng upstream còn nguyên |

---

## 7. Quy trình thực hiện

### Bước 0 — Điểm an toàn
```powershell
git tag backup/pre-upstream-25afadd HEAD
git branch backup/pre-upstream-25afadd-52a1fad HEAD
git config merge.conflictstyle zdiff3
git config rerere.enabled true
```

### Bước 1 — Merge (dừng trước commit)
```powershell
git fetch upstream --tags --prune
git switch -c merge/upstream-v1.4.0      # làm trên branch riêng, main không bị đụng
git merge --no-commit --no-ff upstream/main
```

### Bước 2 — Resolve 6 hunk
Dùng script đã kiểm chứng:
```powershell
& "C:\Users\assas\.dsh\dsh-runtimes\dsh-primary-runtime\dependencies\python\python.exe" `
  ".git\o2a-merge-plan\resolve_conflicts.py" "."
```
Script tự động: giữ cả hai phía, thêm dấu phẩy JSON, sắp xếp lại import (`slices` trước `sort`), **validate JSON + assert không còn marker**. Hoặc resolve tay theo §3.

### Bước 3 — Verify TRƯỚC khi commit
```powershell
docker run --rm -v "${PWD}:/src" -w /src golang:1.24-alpine sh .git/o2a-merge-plan/verify.sh
node --check webui/app.js
```
Kỳ vọng: `ALL CHECKS PASSED`. **Không commit nếu chưa pass.**

### Bước 4 — Commit merge
```powershell
git add -A
git commit -F .git/o2a-merge-plan/merge-commit-msg.txt
```
(theo đúng convention lịch sử fork: `Merge upstream main (77818be) into main`)

### Bước 5 — Commit riêng cho phần bù
```powershell
git commit -F .git/o2a-merge-plan/followup-commit-msg.txt
```

### Bước 6 — Push (chỉ khi đã duyệt)
```powershell
git push origin merge/upstream-v1.4.0          # an toàn: chỉ tạo branch mới trên remote
# hoặc, nếu muốn cập nhật main:
git switch main && git merge --ff-only merge/upstream-v1.4.0 && git push origin main
```
Fast-forward, **không cần** `--force`.

---

## 8. Rollback

| Giai đoạn | Lệnh | Ghi chú |
| --- | --- | --- |
| Đang merge, chưa commit | `git merge --abort` | Sạch, an toàn nhất |
| Đã commit, chưa push | `git reset --hard backup/pre-upstream-25afadd` | An toàn, mất commit merge |
| Đã push | `git revert -m 1 <merge-commit>` | An toàn nhưng "đầu độc" merge sau |
| Đã push, muốn sạch lịch sử | `git push --force-with-lease origin main` | **Viết lại lịch sử** — chỉ khi chắc chắn không ai khác dùng |

---

## 9. Definition of Done

**Trước commit:**
- [ ] `gofmt -l .` rỗng
- [ ] `go vet ./...` exit 0
- [ ] `go build -trimpath ./cmd/opencode2api` exit 0
- [ ] `go test ./... -count=1` **PASS** (CI không chạy bước này — phải chạy tay)
- [ ] `node --check webui/app.js` exit 0
- [ ] `sh -n docker-entrypoint.sh` exit 0
- [ ] `config.example.json` parse hợp lệ, có cả `aliases` + `free_only`
- [ ] Không còn conflict marker trong toàn repo
- [ ] `index.html` có **đúng 2** hit: `c-aliases` + `c-free-only`
- [ ] `docker build` thành công
- [ ] Không file nào của fork bị mất (`modelname.go`, `docker-entrypoint.sh`, `docs/env-overlay.md`, …)

**Sau commit:**
- [ ] Commit message đúng convention
- [ ] `git diff HEAD~1 --stat` khớp 22 file
- [ ] Tag backup còn nguyên

**Sau push:**
- [ ] `git push origin main` fast-forward
- [ ] `origin/main` == local `main`
- [ ] Smoke test runtime: khởi động container, kiểm tra `/healthz`, alias hoạt động, stream hoạt động

---

## 10. Quyết định — đã chốt

| # | Quyết định | Trạng thái |
| --- | --- | --- |
| **D1** | Tiến hành merge | ✅ **Đã làm** — branch `merge/upstream-v1.4.0` (theo yêu cầu "tạo branch mới và merge vào đó trước") |
| **D2** | Thêm `O2A_MODELS_FREE_ONLY` vào env overlay | ✅ **Đã làm** — 4 file, commit `f510b01` |
| **D3** | Sửa R-A (helper `modelEntry`) + R-B (`meta.Model = route.ID`) | ✅ **Đã làm** — commit `f510b01` |
| **D4** | Bổ sung `models.aliases` vào `README.zh-CN.md` | ⬜ **Chưa** — chờ duyệt (drift tài liệu, không ảnh hưởng code) |
| **D5** | Thêm test cho `free_only` × alias | ✅ **Đã làm** — 7 test mới, có mutation test chứng minh bắt được lỗi |
| **D6** | Push lên `origin/main` | ⬜ **Chưa** — chờ duyệt |

---

## 11. Kiểm chứng runtime (trên image Docker đã build)

| Kiểm tra | Kết quả |
| --- | --- |
| `/healthz` | **200**, `total: 85`, `exposed: 9` → `free_only` lọc đúng |
| `/v1/models` khi `free_only=true` | **9** entry (khi `false`: **72**) |
| Alias trỏ model free (`cat-jam-free`) | **PRESENT**, có `upstream_model`, `context_window`, `cost` → refactor `modelEntry` hoạt động |
| Alias trỏ model trả phí (`deepseek-v4-flash`) | **ABSENT** khỏi `/v1/models` |
| `POST` qua alias free | **200** + response thật từ model |
| `POST` qua alias trả phí | **400** `model "deepseek-v4-flash" is not free and models.free_only is enabled` → alias **không lách được**, lỗi nêu đúng model thật |
| `O2A_MODELS_FREE_ONLY=true` | ghi `"free_only": true` (boolean thật, không phải chuỗi) |
| Mutation test (hoàn nguyên fix `health.go`) | Test **FAIL** đúng chỗ → test mới thực sự bảo vệ |

---

## 12. Phụ lục — artifact trong `.git/o2a-merge-plan/`

| File | Nội dung |
| --- | --- |
| `report-01` … `report-10` | 10 report điều tra chi tiết của subagent |
| `resolve_conflicts.py` | Script resolve 6 hunk (đã kiểm chứng, có validate) |
| `verify.sh` | Script verify khớp CI + `go test` |
| `verified-merge.patch` | Toàn bộ bản merge đã resolve + kiểm chứng (22 file) |
| `conflict-resolution.patch` | Chỉ 5 file conflict |
| `MERGED_*` / `OURS_*` / `THEIRS_*` | Bản dump 3 chiều của các file conflict |

> **Lưu ý:** các file `MERGED_*`/`OURS_*`/`THEIRS_*` do PowerShell ghi ra ở dạng **UTF-16LE**, nên tool đọc file có thể báo "binary". Dùng `git show`/`git diff` để đọc bản gốc UTF-8.
