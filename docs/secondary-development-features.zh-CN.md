# 二开功能保留清单

更新时间：2026-07-31

本文记录当前 `master`（及未合入但已落地的本地改动）中，相对 `upstream/main`（`router-for-me/CLIProxyAPI`）需要在后续同步上游时保留的二开能力。口径以“用户可感知的行为 + 联调契约”为主，不逐行记录重构。

## 维护约定

- 每次同步上游、恢复二开能力、发现遗漏入口，或新增二开功能后，都必须同步更新本文。
- 记录功能名称、行为、关键位置和验证方式；不记录密钥、cookie、代理密码等敏感信息。
- 提交说明中应明确是否更新了本清单；若未更新需说明原因。
- 排查“功能不见了”时，先对照本文检查，不要只凭当前页面表现判断。

相关上游远程：

- `origin`：`funtionalcode/CLIProxyAPIPlus`
- `upstream`：`router-for-me/CLIProxyAPI`

## 当前应保留的二开功能

### 1. 采用网关入站 Request ID（与 new-api 联调）

**目标：** new-api → CPA 链路使用同一 request id 写日志文件名，可用 new-api 的 `X-Oneapi-Request-Id` 直接查 CPA 请求日志。

**行为：**

- AI API 路径（`/v1`、`/v1beta`、`/openai/v1`、`/backend-api/codex` 等）生成日志 request id 时：
  1. 优先使用入站头 `X-Client-Request-Id`
  2. 其次 `X-Oneapi-Request-Id`
  3. 都没有时再本地生成 8 位 hex（`GenerateRequestID`）
- 外部 id 经 `SanitizeRequestID` 规范化：仅保留 `A-Za-z0-9._-`，最长 128，非法字符折叠为 `-`
- `GET /v0/management/request-log-by-id/:id` 查询前同样 sanitize，保证与磁盘文件名后缀 `-{id}.log` 一致
- 日志文件命名仍为 `error-|success-|{model}-{requestID}.log`；采用网关长 id 后文件名中的 id 段会变长，属预期

**关键位置：**

- `internal/logging/requestid.go`（`SanitizeRequestID`、`ResolveIncomingRequestID`、header 列表）
- `internal/logging/gin_logger.go`（AI 路径优先外部 id）
- `internal/api/handlers/management/logs.go`（`GetRequestLogByID` sanitize）
- `internal/logging/requestid_test.go`

**同步风险：** 上游若改写 `GinLogrusLogger` 的 request id 生成逻辑，或去掉 `request-log-by-id` 路由，本能力会丢。合并时必须保留“入站头优先 + sanitize + 查询同源 sanitize”。

**验证：**

```bash
go test ./internal/logging -count=1
go test ./internal/api/handlers/management -count=1 -run 'RequestLog|RequestID|Log'
go build -o /tmp/cli-proxy-api-test ./cmd/server && rm /tmp/cli-proxy-api-test
```

联调：用带 `X-Oneapi-Request-Id: <id>` 的请求打 CPA AI 路径后，调用 `request-log-by-id/<id>` 应命中对应日志。

### 2. 请求日志列表筛选与大文件格式化下载

**行为：**

- 错误/成功请求日志列表支持按 **模型名**（文件名中的 model 段，大小写不敏感子串）、**请求 ID**（`request_id`/`id`，经 `SanitizeRequestID` 后匹配文件名后缀或子串）与 **修改时间**（`from`/`to`，unix 秒或 RFC3339）筛选
- 超大请求日志格式化下载：**截断**输出并成功返回，而不是 413 直接失败（上限见 `formattedRequestLogMaxSize`）
- 成功请求日志管理路由必须存在（`success-request-log` 列表/下载等），与错误请求日志对称

**关键位置：**

- `internal/api/handlers/management/logs.go`
- `internal/api/handlers/management/logs_test.go`
- `internal/api/handlers/management/request_log_formatter_test.go`
- `internal/api/server_management.go`（成功日志相关路由注册）
- `internal/api/server_options.go` / `internal/api/server_reload.go`（配置热更路径上的路由/开关）

**相关提交（参考）：**

- `8c805d62` 请求日志支持模型/时间筛选并增强大文件格式化下载
- `0626438b` 超大请求日志格式化下载改为截断而非 413
- `77c04a38` 补齐成功请求日志管理路由

**同步风险：** 上游重写 management logs handler 时，容易丢掉 query 过滤与截断策略；成功日志路由也常在 merge 中被漏挂。

### 3. 单账号 OAuth 模型别名（auth 文件 metadata）

**说明：** 运行时已支持从 OAuth JSON 读取 `model_aliases` / `model-aliases`，合成到 auth attributes；管理面 Patch 字段需能持久化该 metadata。前端编辑入口在 CPAMP（见对端清单）。

**行为：**

- 认证文件 JSON 支持 `model_aliases`（或 `model-aliases`）数组，结构与全局 `oauth-model-alias` 条目兼容
- `FileSynthesizer` / `SynthesizeAuthFile` 提取后写入 auth，并与全局别名策略配合
- `PatchAuthFileFields` 等写回路径不得静默丢弃 `model_aliases`

**关键位置：**

- `internal/watcher/synthesizer/file.go`（`extractOAuthModelAliasesFromMetadata`）
- `internal/api/handlers/management/auth_files.go`（fields patch / list 暴露）
- 相关 `*_test.go`

**同步风险：** 上游若只保留全局 `oauth-model-alias` 而清理 per-auth metadata，或 patch 白名单未包含 `model_aliases`，单账号别名会失效。

### 4. 认证文件权重 `weight`（后端）

**行为：**

- 认证文件 metadata 支持 `weight`（正整数）；synthesizer 写入 `Attributes["weight"]`
- 列表/详情 API 暴露 `weight`；`PatchAuthFileFields` 可更新并 `syncAuthFileWeightAttribute`（`<=0` 清理 attribute）
- 与 `priority` 并存：优先级先筛，同优先级再按权重分流（调度逻辑以上游加权轮询为准，二开侧不得回退“忽略 weight”）

**关键位置：**

- `internal/watcher/synthesizer/file.go`
- `internal/api/handlers/management/auth_files.go`（list 暴露、patch sync）

**同步风险：** 上游若去掉 weight 属性同步或列表字段，管理中心 UI 会“有入口写不进去/读不回”。

### 5. 其它已在本 fork 保留、同步时需 diff 核对的能力

下列能力多数已体现在近期 commit 与业务代码中；同步上游时用 `git log` / diff 核对，勿被整文件覆盖：

| 能力 | 提示位置 |
| --- | --- |
| 成功请求日志 `success-request-log` | management routes + logging 开关 |
| 请求日志按调用模型命名 | logging / handlers |
| X-CPA-TRACE-ID 响应头 | `internal/logging/cpa_trace.go` 等 |
| 账号加权轮询 / 混合提供商权重粘性 | auth scheduler 相关 |
| OAuth 模型别名 force-mapping、display name | oauth / registry |

（上表为同步核对提示，细节以代码与 commit 为准；新增稳定二开点后应升格为独立小节。）

## 与兄弟项目的联调契约

| 方向 | 契约 |
| --- | --- |
| new-api → CPA | 上游请求带 `X-Client-Request-Id` 与 `X-Oneapi-Request-Id`（同值，均为 new-api 本地 request id）；CPA 用作日志 request id |
| CPA → new-api | 响应可含 `X-CPA-TRACE-ID`；new-api 写入 `UpstreamRequestIdKey` 供管理日志展示 |
| CPAMP → CPA | 认证文件 patch 支持 `priority`、`weight`、`model_aliases`、`headers`、`proxy_url` 等字段 |

## 同步上游后的检查清单

1. AI 路径是否仍优先外部 request id；无头时是否仍生成 8 hex。
2. `SanitizeRequestID` 与 `request-log-by-id` 是否同源规范化。
3. 错误/成功请求日志列表的 `model`/`request_id`/`from`/`to` 筛选是否有效。
4. 超大日志格式化下载是否截断成功而非 413。
5. 成功请求日志管理路由是否注册。
6. 认证文件 `weight` / `model_aliases` 读写是否仍生效。
7. 对 new-api / CPAMP 的联调契约是否未破坏。

建议验证：

```bash
gofmt -w .
go test ./internal/logging ./internal/api/handlers/management ./internal/watcher/synthesizer -count=1
go build -o /tmp/cli-proxy-api-test ./cmd/server && rm /tmp/cli-proxy-api-test
```

## 变更记录

| 日期 | 说明 |
| --- | --- |
| 2026-07-31 | 初建清单：入站 request id、请求日志筛选/大文件下载、单账号别名与 weight 同步要点、三端联调契约 |
| 2026-07-31 | 请求日志列表补充 `request_id`/`id` 筛选（与 request-log-by-id 同源 sanitize） |
