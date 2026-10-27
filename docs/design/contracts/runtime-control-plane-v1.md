# Runtime Alpha 配置与 Control Plane 契约

## 1. 范围

本文定义 Runtime Alpha 已实现的基础契约：版本化配置、生命周期状态、健康检查和本地 Control Plane。它不定义 PHP Queue Bridge、Scheduler、MySQL 管理、BI/日志查询协议或禅道业务健康检查协议；这些能力只通过对应 Private Bridge 和设计文档约束。

Windows 使用相同 JSON 请求响应模型，传输层为 Named Pipe（`run-service` 已接入 Windows Service）；Linux 和 Docker 使用 Unix domain socket。

## 2. 配置文件

默认路径是 Runtime 根目录下的 `config/runtime.toml`（Linux 标准示例为 `/opt/zentao/config/runtime.toml`）。可复制 [配置样例](../../../config/runtime.example.toml)，配置格式为严格 TOML，未知字段和不支持的 `schemaVersion` 会阻止启动。

```toml
schemaVersion = 1

[runtime]
controlSocket = "/run/zentao/runtime.sock"
pidFile = "/run/zentao/runtime.pid"
drainTimeout = "30s"
logPath = "logs/runtime.log"
logMaxBytes = 16777216
logMaxBackups = 5
nodeID = "node-a"
clusterID = "cluster-prod-1"

[web]
listen = "0.0.0.0:8080"
threads = 4
readHeaderTimeout = "10s"
idleTimeout = "30s"
maxHeaderBytes = 16384

[[apps]]
id = "zentao"
path = "/"
dir = "apps/zentao"
webRoot = "www"
enabled = true

[bi]
enabled = false
bridgePort = 8083

[observability]
enabled = true
datasetRoot = "data/observability"
spoolPath = "spool/observability"
flushInterval = "60s"
metricsDays = 30
logDays = 30
jsonlConvertInterval = "1h"
jsonlConvertSources = ["access", "runtime"]
jsonlKeepDays = 7
```

应用、日志和观测目录等路径默认按安装根目录解析，通常使用 `apps/...`、`logs/...`、`observability/...` 等相对路径；系统级 socket/PID 或其他特殊目录可以填写绝对路径。应用只能通过 `[[apps]]` 声明，允许配置零个应用。`web.threads` 的范围为 2 至 256，Header 限制范围为 1 KiB 至 1 MiB，全部超时必须为正的 Go duration 字符串。

环境变量只覆盖明确字段：

- `ZENTAO_RUNTIME_LISTEN`
- `ZENTAO_RUNTIME_THREADS`
- `ZENTAO_RUNTIME_CONTROL_SOCKET`
- `ZENTAO_RUNTIME_PID_FILE`
- `ZENTAO_RUNTIME_DRAIN_TIMEOUT`
- `ZENTAO_RUNTIME_AUDIT_LOG`
- `ZENTAO_RUNTIME_LOG_PATH`
- `ZENTAO_RUNTIME_LOG_MAX_BYTES`
- `ZENTAO_RUNTIME_LOG_MAX_BACKUPS`
- `ZENTAO_RUNTIME_NODE_ID`
- `ZENTAO_RUNTIME_CLUSTER_ID`
- `ZENTAO_RUNTIME_ACCESS_LOG`
- `ZENTAO_QUEUE_ENABLED`
- `ZENTAO_QUEUE_BRIDGE_PORT`
- `ZENTAO_OBSERVABILITY_ENABLED`
- `ZENTAO_OBSERVABILITY_BRIDGE_PORT`
- `ZENTAO_OBSERVABILITY_DATASET_ROOT`
- `ZENTAO_OBSERVABILITY_SPOOL_PATH`
- `ZENTAO_BI_ENABLED`
- `ZENTAO_BI_BRIDGE_PORT`
- `ZENTAO_BI_QUERY_THREADS`
- `ZENTAO_BI_QUERY_MEMORY_LIMIT`
- `ZENTAO_BI_QUERY_TIMEOUT`
- `ZENTAO_BI_MAX_ROWS`
- `ZENTAO_BI_MAX_CONCURRENT_QUERIES`

CLI 的 `serve --listen/--threads/--control-socket/--pid-file` 优先级高于配置文件。配置文件是必需文件；应用只能通过 `[[apps]]` 声明，允许配置零个应用。

`bi` 段当前默认关闭。启用后 Runtime 提供应用隔离的 Parquet 发布和 DuckDB
查询 Bridge；同步调度、表级水位和周期配置属于 PHP 应用，不在 Runtime
Control Plane 中配置。完整条件见
[BI Parquet 同步与 DuckDB 查询方案](../11-bi-parquet-query-plan.md)。

## 3. 生命周期和健康

状态机按以下顺序运行：

```text
created -> config_loaded -> dependencies_starting -> caddy_starting -> ready
ready -> reloading -> ready|degraded
ready|degraded -> draining -> stopped
任意启动阶段 -> failed
```

`/_runtime/healthz` 和 `/_runtime/liveness` 是轻量存活探针。`/_runtime/readiness` 仅在 Caddy/FrankenPHP 成功启动后提供。完整状态通过本地 Control Plane 的 `health` 获取；`health --deep` 只允许本地调用，但当前仅返回 Runtime 生命周期派生状态，用于冻结协议和访问边界。PHP 应用深探针契约已落地
（`zentaopms/www/health.php`，见禅道适配计划 Z-HEALTH-01），Runtime
`health --deep` 已注册 `app` 深探针并通过同一 Web Listener 调用
`/health.php`，将 `ok|degraded|failed` 映射为组件状态；
`queue-direct-smoke.sh` 已断言 deep health 中 app 组件为 ok。

## 4. Control Plane

Linux 默认 Socket 为 `/run/zentao/runtime.sock`，创建后权限为 `0600`。协议使用单行 JSON，最大请求为 64 KiB：

```json
{"version":1,"operation":"status"}
```

成功响应：

```json
{"version":1,"ok":true,"result":{"lifecycle":{"state":"ready"}}}
```

失败响应：

```json
{"version":1,"ok":false,"error":{"code":"invalid_configuration","message":"..."}}
```

当前支持 `status`、`version`、`health`、`diagnose`、`reload`、`stop`、
`upgrade`、`logs`、`metrics`、`observability-flush`、
`observability-clean`、`convert-jsonl` 和 `collect-logs`；入口同时提供
`start`（等价于 `serve`）以及跨平台的 `service install` / `service uninstall`。
Windows 的 `run-service` 仅作为 Service Control Manager 的进程入口。CLI 使用同一二进制：

```bash
zentao-runtime status --control-socket /run/zentao/runtime.sock
zentao-runtime health --deep --control-socket /run/zentao/runtime.sock
zentao-runtime reload --control-socket /run/zentao/runtime.sock
zentao-runtime stop --control-socket /run/zentao/runtime.sock
```

`php-cli -- <args>` 使用与 Web 相同的 PHP Runtime、扩展和 ionCube Loader
执行命令行脚本，例如：

```bash
zentao-runtime php-cli -- -r 'echo PHP_VERSION;'
zentao-runtime php-cli -- /opt/zentao/bin/zentao-cron.php
```

PHP 可执行文件按 `ZENTAO_PHP_BIN`、`--php` 或二进制同目录/默认安装路径
顺序查找。

`stop` 发起优雅排空，不会伪造任务完成或强杀 PHP。PID 文件只用于 `status` 与 `stop` 在 Socket 不可达时的兼容诊断回退，正常管理命令不直接修改运行实例文件。

Linux Control Plane 通过 `SO_PEERCRED` 校验调用者必须与 Runtime 同属一个
effective user 或 root，Socket 权限固定为 `0600`。Windows Named Pipe 使用
仅 SYSTEM 和管理员可访问的 ACL。每次控制操作写入审计日志（JSON Lines），
审计记录包含操作、调用者身份、结果和时间，不包含凭据或配置明文。
未配置 `runtime.auditLog` 时，审计日志默认写入
`<logs_dir>/audit-<nodeID>.jsonl`，与 Runtime/访问日志一样按节点区分。

健康探针由 Runtime Host 动态提供：

- `/_runtime/healthz` 和 `/_runtime/liveness`：存活探针，Host event loop
  存活时返回 200 `{"status":"ok"}`。
- `/_runtime/readiness`：Caddy/FrankenPHP 与必需组件就绪时返回 200
  `{"status":"ready"}`，否则 503 `{"status":"unavailable", ...}`。

`health --deep` 通过本地 Control Plane 返回分层组件结果（runtime、php、
app-root 以及未来的数据库、Session、NFS 探针），组件状态为
`ok|degraded|failed`，深探针失败不会影响存活探针。

## 6. Queue 配置

`queue` 段默认禁用。启用时 `bridgePort` 必须在 1 至 65535 之间，Runtime
固定监听 `127.0.0.1:<bridgePort>`，Worker 必须至少一个。每个应用通过
`apps[].queue` 启用自己的 Bridge；Token 由
`apps[].queue.token`、`apps[].env.ZENTAO_QUEUE_TOKEN` 提供，或由 Runtime
每次启动随机生成并注入该应用，不写入日志和审计。Queue 启动时先
执行 `capabilities` 协商，失败只标记 Queue 组件降级，不影响 Web Ready。
批量 Claim、Heartbeat、Reap 和租约参数见 [`design/03-message-queue.md`](../03-message-queue.md)。

`queue.bridgePort` 和 `observability.bridgePort` 不能与 `web.listen` 的端口相同，两者同时启用时也不能互相冲突。

## 7. 可观测性

`observability` 段启用时需要 DuckDB 支持（正式构建带 `duckdb` tag）。事件
缓冲在节点本地，按 `flushInterval` 或行数/字节阈值封闭批次，经
`.parquet.tmp` 原子发布到 `datasetRoot` 的 Hive 分区；NFS 不可用时写入
有界 `spoolPath`，运行中每个刷新周期都会按原 Batch ID 重试补发，不等待
进程重启。可观测性故障只标记 deep health 中的 `observability` 组件，
不影响 Web Ready。

管理命令：

```bash
zentao-runtime flush-observability --control-socket /run/zentao/runtime.sock
zentao-runtime clean-observability --control-socket /run/zentao/runtime.sock
zentao-runtime convert-jsonl --control-socket /run/zentao/runtime.sock
zentao-runtime collect-logs --control-socket /run/zentao/runtime.sock
zentao-runtime logs --since 30m --level error --node node-a --control-socket /run/zentao/runtime.sock
zentao-runtime metrics --since 1h --metric-name http.request.duration --control-socket /run/zentao/runtime.sock
```

查询只使用固定模板和受控参数：时间范围、节点、日志级别或指标名、行数上限；
不接收任意 SQL 或任意路径。

`convert-jsonl` 立即把本节点已封闭的 JSONL 段（访问日志和 Runtime 日志）
同步为 Parquet，返回 `{"segments":N,"events":N,"skipped":N,"malformed":N}`；
运行中每 `jsonlConvertInterval`（默认 1 小时）自动执行一次，重复执行幂等。
原始 JSONL 段按 `jsonlKeepDays`（默认 7 天）保留后由轮转器清理。

`collect-logs` 生成受大小限制的诊断包（Runtime/Caddy/PHP 日志、审计、
版本与脱敏配置摘要），不包含凭据、Token、请求内容或业务数据。

## 8. 自动请求日志

Runtime 启动后自动记录每次请求（无需额外配置路径）：

- `request.uri`：请求 URL（含查询串）；
- `duration`：响应时间（秒，浮点）；
- `status`：HTTP 状态码；
- `size`：响应字节数；
- `method`、`host`、`remote_ip`、`proto`；
- `logger`：`http.log.access.access` 表示访问日志；
  `http.log.error.*` 表示 Caddy 处理器错误（含 `error` 字段）。

日志为 JSON Lines，写入 `<runtime.logPath 目录>/access-<nodeID>.jsonl`；
每个节点独立文件，适合 NFS 多节点共享日志目录。Caddy 每小时整点
（`roll_minutes: [0]`）把追加文件轮转为
`access-<nodeID>-<时间戳>-time.jsonl`（大小触发时为
`-size.jsonl`），按 `jsonlKeepDays` 保留。
默认不记录 Cookie、Authorization 等凭据头。PHP 侧错误消息由 `php.ini`
的 `error_log` 写入 `logs/php-error.log`，与访问日志按时间戳关联。

Runtime 自身结构化日志写入 `runtime-<nodeID>.jsonl`，按
`jsonlConvertInterval` 轮转为 `runtime-<nodeID>.jsonl-<时间戳>.log`。
两者均通过 `convert-jsonl` 或自动调度转换为 Parquet（见第 7 节）。

## 9. Reload 边界

`web.readHeaderTimeout`、`web.idleTimeout` 和 `web.maxHeaderBytes` 可以通过 Caddy Library 热加载。候选配置会先严格解析和校验，Caddy 拒绝候选配置时实例保持旧配置并进入 `degraded`。

以下字段返回 `restartRequired: true`，不在运行中修改：

- `web.listen`
- `web.threads`
- `runtime.controlSocket`
- `runtime.pidFile`
- `runtime.auditLog`
- `runtime.logPath`
- `runtime.logMaxBytes`
- `runtime.logMaxBackups`
- `runtime.nodeID`
- `runtime.clusterID`
- `[[apps]]`
- `observability.jsonlConvertInterval`
- `observability.jsonlConvertSources`
- `observability.jsonlKeepDays`

PHP 版本、ionCube、Zend extension、PHP 初始化参数和 Runtime 二进制也属于必须重启的范围。
