# Runtime Alpha 配置与 Control Plane 契约

## 1. 范围

本文定义 Runtime Alpha 已实现的基础契约：版本化配置、生命周期状态、健康检查和本地 Control Plane。它不定义 PHP Queue Bridge、Scheduler、MySQL 管理、DuckDB 或禅道业务健康检查协议。

Windows 使用相同 JSON 请求响应模型，传输层为 Named Pipe（`run-service` 已接入 Windows Service）；Linux 和 Docker 使用 Unix domain socket。

## 2. 配置文件

默认路径是 `/opt/zentao/config/runtime.json`。可复制 [配置样例](../config/runtime.example.json)，配置格式为严格 JSON，未知字段和不支持的 `schemaVersion` 会阻止启动。

```json
{
  "schemaVersion": 1,
  "runtime": {
    "controlSocket": "/run/zentao/runtime.sock",
    "pidFile": "/run/zentao/runtime.pid",
    "drainTimeout": "30s",
    "auditLog": "/opt/zentao/logs/audit.log",
    "logPath": "/opt/zentao/logs/runtime.log",
    "logMaxBytes": 16777216,
    "logMaxBackups": 5,
    "nodeID": "node-a",
    "clusterID": "cluster-prod-1"
  },
  "web": {
    "root": "/opt/zentao/app/current/www",
    "listen": "0.0.0.0:8080",
    "threads": 4,
    "readHeaderTimeout": "10s",
    "idleTimeout": "30s",
    "maxHeaderBytes": 16384,
    "accessLog": "/opt/zentao/logs/access.log"
  }
}
```

`web.root`、`runtime.controlSocket` 和 `runtime.pidFile` 必须为绝对路径。`web.threads` 的范围为 2 至 256，Header 限制范围为 1 KiB 至 1 MiB，全部超时必须为正的 Go duration 字符串。

环境变量只覆盖明确字段：

- `ZENTAO_RUNTIME_ROOT`
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
- `ZENTAO_QUEUE_BRIDGE_URL`
- `ZENTAO_QUEUE_TOKEN`
- `ZENTAO_OBSERVABILITY_ENABLED`
- `ZENTAO_OBSERVABILITY_DATASET_ROOT`
- `ZENTAO_OBSERVABILITY_SPOOL_PATH`

CLI 的 `serve --root/--listen/--threads/--control-socket/--pid-file` 优先级高于配置文件。没有配置文件时，PoC 只允许通过 `serve --root <absolute-path>` 启动，便于兼容已有调用方式。

## 3. 生命周期和健康

状态机按以下顺序运行：

```text
created -> config_loaded -> dependencies_starting -> caddy_starting -> ready
ready -> reloading -> ready|degraded
ready|degraded -> draining -> stopped
任意启动阶段 -> failed
```

`/_runtime/healthz` 和 `/_runtime/liveness` 是轻量存活探针。`/_runtime/readiness` 仅在 Caddy/FrankenPHP 成功启动后提供。完整状态通过本地 Control Plane 的 `health` 获取；`health --deep` 只允许本地调用，但当前仅返回 Runtime 生命周期派生状态，用于冻结协议和访问边界。PHP 内部请求、数据库、Session、NFS 和 PHP 业务探针将在对应 PHP 契约完成后加入。

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

当前支持 `status`、`version`、`health`、`diagnose`、`reload` 和 `stop`；Linux 入口同时提供 `start`（等价于 `serve`），Windows 提供 `run-service`。CLI 使用同一二进制：

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

健康探针由 Runtime Host 动态提供：

- `/_runtime/healthz` 和 `/_runtime/liveness`：存活探针，Host event loop
  存活时返回 200 `{"status":"ok"}`。
- `/_runtime/readiness`：Caddy/FrankenPHP 与必需组件就绪时返回 200
  `{"status":"ready"}`，否则 503 `{"status":"unavailable", ...}`。

`health --deep` 通过本地 Control Plane 返回分层组件结果（runtime、php、
app-root 以及未来的数据库、Session、NFS 探针），组件状态为
`ok|degraded|failed`，深探针失败不会影响存活探针。

## 6. Queue 配置

`queue` 段默认禁用。启用时 `bridgeBaseURL` 必须为 loopback 地址，Worker
必须至少一个；Bridge Token 通过 `ZENTAO_QUEUE_TOKEN` 或
`queue.bridgeToken` 提供，每次部署轮换，不写入日志和审计。Queue 启动时先
执行 `capabilities` 协商，失败只标记 Queue 组件降级，不影响 Web Ready。
批量 Claim、Heartbeat、Reap 和租约参数见 `docs/message-queue-design.md`。

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
zentao-runtime collect-logs --control-socket /run/zentao/runtime.sock
zentao-runtime logs --since 30m --level error --node node-a --control-socket /run/zentao/runtime.sock
zentao-runtime metrics --since 1h --metric-name http.request.duration --control-socket /run/zentao/runtime.sock
```

查询只使用固定模板和受控参数：时间范围、节点、日志级别或指标名、行数上限；
不接收任意 SQL 或任意路径。

`collect-logs` 生成受大小限制的诊断包（Runtime/Caddy/PHP 日志、审计、
版本与脱敏配置摘要），不包含凭据、Token、请求内容或业务数据。

## 8. 自动请求日志

配置 `web.accessLog`（绝对路径）后，Runtime 自动记录每次请求：

- `request.uri`：请求 URL（含查询串）；
- `duration`：响应时间（秒，浮点）；
- `status`：HTTP 状态码；
- `size`：响应字节数；
- `method`、`host`、`remote_ip`、`proto`；
- `logger`：`http.log.access.access` 表示访问日志；
  `http.log.error.*` 表示 Caddy 处理器错误（含 `error` 字段）。

日志为 JSON Lines，写入后按 64 MB 轮转。默认不记录 Cookie、
Authorization 等凭据头。PHP 侧错误消息由 `php.ini` 的 `error_log` 写入
`logs/php-error.log`，与访问日志按时间戳关联。

## 5. Reload 边界

`web.readHeaderTimeout`、`web.idleTimeout` 和 `web.maxHeaderBytes` 可以通过 Caddy Library 热加载。候选配置会先严格解析和校验，Caddy 拒绝候选配置时实例保持旧配置并进入 `degraded`。

以下字段返回 `restartRequired: true`，不在运行中修改：

- `web.root`
- `web.listen`
- `web.threads`
- `web.accessLog`
- `runtime.controlSocket`
- `runtime.pidFile`
- `runtime.auditLog`
- `runtime.logPath`
- `runtime.logMaxBytes`
- `runtime.logMaxBackups`
- `runtime.nodeID`
- `runtime.clusterID`

PHP 版本、ionCube、Zend extension、PHP 初始化参数和 Runtime 二进制也属于必须重启的范围。
