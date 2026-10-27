# 速查参考

这篇文档集中列出跨多个能力复用的端点、环境变量、限制、错误码和契约入口。

具体使用流程见：

- [应用装载](application-hosting.md)
- [可观测性接入](observability.md)
- [Queue Bridge 接入](queue-bridge.md)
- [运维与健康检查](operations.md)

## 访问端点

| 端点 | 监听范围 | 鉴权 | 用途 |
| --- | --- | --- | --- |
| `/_runtime/healthz` | 公共 | 无 | Host 存活 |
| `/_runtime/liveness` | 公共 | 无 | Host 存活 |
| `/_runtime/readiness` | 公共 | 无 | 是否接收业务流量 |
| `/internal/runtime/observability/v1/events` | 回环 | Observability Token | 写入日志和度量 |
| `/internal/runtime/observability/v1/query` | 回环 | Observability Token | 查询应用日志 |
| `/internal/runtime/bi/v1/publish` | 回环 | BI Token | 发布应用 BI Parquet 数据集 |
| `/internal/runtime/bi/v1/query` | 回环 | BI Token | 只读查询应用 BI 数据集 |
| `/internal/runtime/queue/v1/apps/<id>/<endpoint>` | 回环 | Queue Token | 调用应用 Queue Bridge |

Queue Bridge 端点：

```text
capabilities
schedule
claim
execute
heartbeat
reap
stats
control
```

Control Plane 不通过 HTTP 暴露，使用 Unix Socket 或 Windows Named Pipe。

Runtime 不提供缓存 HTTP 端点、Cache Bridge、`ZENTAO_CACHE_*` 环境变量或 `[cache]` 配置段。发行包只包含 APCu/Redis PHP 扩展。

私有 Bridge 端口：

| 配置 | 默认值 | 监听地址 |
| --- | --- | --- |
| `queue.bridgePort` | `8081` | 固定 `127.0.0.1` |
| `observability.bridgePort` | `8082` | 固定 `127.0.0.1` |
| `bi.bridgePort` | `8083` | 固定 `127.0.0.1`，默认关闭 |

这些端口必须互不相同，也不能与 `web.listen` 的端口相同。

## Runtime 注入的环境

| 环境变量 | 注入条件 | 含义 |
| --- | --- | --- |
| `ZT_WEB_ROOT` | 所有应用 | 公共 URL 前缀 |
| `ZT_APP_ID` | 所有应用 | 应用 ID |
| `ZT_DATA_ROOT` | 所有应用 | 持久数据根 |
| `ZT_TMP_ROOT` | 所有应用 | 临时目录 |
| `ZT_SESSION_PATH` | 所有应用 | 文件 Session 目录 |
| `ZT_APISESSION_PATH` | 所有应用 | API Session 目录 |
| `ZT_BACKUP_PATH` | 所有应用 | 备份目录 |
| `ZENTAO_LOG_DIR` | 所有应用 | 本地日志目录 |
| `ZENTAO_QUEUE_TOKEN` | 应用 Queue 启用 | Queue Bridge Token |
| `ZENTAO_OBSERVABILITY_URL` | 可观测性启用 | 事件写入地址 |
| `ZENTAO_OBSERVABILITY_TOKEN` | 可观测性启用 | 事件和查询 Token |
| `ZENTAO_OBSERVABILITY_SCHEMA` | 可观测性启用 | 事件 Schema 版本 |
| `ZENTAO_OBSERVABILITY_QUERY_URL` | 查询桥可用 | 应用日志查询地址 |
| `ZENTAO_OBSERVABILITY_QUERY_TOKEN` | 查询桥可用 | 应用日志查询 Token |
| `ZENTAO_LOG_MODE` | 查询桥可用 | 当前为 `flush` |
| `ZENTAO_BI_TOKEN` | BI 启用 | 应用 BI 发布和查询 Token |
| `ZENTAO_BI_STAGING_DIR` | BI 启用 | 应用 NDJSON staging 目录 |
| `ZENTAO_BI_PUBLISH_URL` | BI 启用 | BI Manifest 发布地址 |
| `ZENTAO_BI_QUERY_URL` | BI 启用 | BI 查询地址 |
| `ZENTAO_BI_SCHEMA` | BI 启用 | BI Bridge Schema 版本 |
| `ZENTAO_BI_MAX_ROWS` | BI 启用 | 单次 BI 查询的最大返回行数 |

Runtime 进程还提供：

| 环境变量 | 含义 |
| --- | --- |
| `ZENTAO_NODE_ID` | 节点 ID |
| `ZENTAO_CLUSTER_ID` | 集群 ID；未配置时为 `standalone` |
| `ZENTAO_BOOT_ID` | 当前 Runtime 启动实例 ID |

应用显式配置的环境变量优先。Runtime 不生成数据库凭据。

## 大小和批次限制

| 接口 | 限制 |
| --- | --- |
| Control Plane 请求 | 64 KiB |
| Queue Bridge 请求 | 64 KiB |
| Queue Bridge 响应 | 64 KiB |
| Queue Bridge 批量 | 128 |
| Observability 事件请求 | 256 KiB |
| Observability 事件批次 | 100 |
| Observability `message` | 16384 字节 |
| Observability `fields` | 32 个 |
| Observability `labels` | 16 个 |
| Observability 查询 `limit` | 200 |
| Observability 查询 `offset` | 100000 |
| BI Bridge 请求 | 256 KiB |
| BI 查询 `maxRows` | Runtime 默认上限 10000 |
| BI 查询 `offset` | 1000000 |
| 应用深探针响应 | 64 KiB |

## 超时和轮询

| 场景 | 默认值 |
| --- | --- |
| Runtime 请求排空 | 30 秒 |
| Queue Bridge 单请求 | 5 秒 |
| Queue 心跳 | 20 秒 |
| Queue Reaper | 10 秒 |
| Queue 调度触发 | 20 秒 |
| Observability 事件发送 | 由应用客户端控制，应使用短超时 |
| Observability 查询 | 10 秒 |
| BI Manifest 发布客户端 | 300 秒 |
| BI DuckDB 查询 | 30 秒，可由 `bi.queryTimeout` 覆盖 |
| 应用深探针 | 5 秒 |
| Control Plane 服务端处理 | 5 秒 |

这些值可能由配置覆盖。以当前 `runtime.toml` 和实现为准。

## HTTP 错误

Observability：

| 状态 | 错误码 |
| --- | --- |
| `400` | `invalid_request` |
| `400` | `invalid_event` |
| `400` | `invalid_batch` |
| `400` | `unsupported_schema` |
| `401` | `unauthenticated` |
| `413` | `request_too_large` |
| `503` | `unavailable` |

BI：

| 状态 | 错误码 |
| --- | --- |
| `400` | `invalid_request` |
| `400` | `unsupported_schema` |
| `400` | `publish_failed` |
| `400` | `query_failed` |
| `401` | `unauthenticated` |
| `503` | `unavailable` |
| `503` | `query_failed`（数据集不可用） |
| `504` | `query_failed`（查询超时） |

BI 查询连接仅允许访问当前应用 `<dataDir>/bi/` 数据集；Runtime 随后关闭
DuckDB 外部访问并锁定配置，不读取数据集之外的本地文件。

Queue Bridge：

```text
unauthenticated
unsupported_schema
invalid_request
request_too_large
invalid_response
unavailable
conflict
execution_mode_conflict
internal
```

Control Plane：

```text
forbidden
invalid_request
request_too_large
unsupported_version
unknown_operation
invalid_configuration
reload_failed
unavailable
```

## 度量注册表

当前允许的指标名：

```text
http.request.duration
zentao_queue_backlog
zentao_queue_oldest_age_seconds
observability_events_received_total
observability_events_dropped_total
observability_buffer_items
observability_spool_bytes
observability_spool_batches
observability_batch_publish_total
observability_batch_rows
observability_batch_bytes
observability_last_published_timestamp
observability_parquet_files
observability_query_total
observability_nfs_available
```

指标类型只允许 `counter` 和 `gauge`。新增名称需要修改 Runtime 注册表并同步所有发送端。

## CLI

```text
zentao serve
zentao start
zentao status
zentao stop
zentao reload
zentao health [--deep]
zentao diagnose [--deep]
zentao logs [query flags]
zentao metrics [query flags]
zentao flush-observability
zentao clean-observability
zentao convert-jsonl
zentao collect-logs
zentao php-cli -- <args>
zentao version
zentao service install|uninstall
```

查询参数：

```text
--since <duration>
--until <RFC3339>
--node <node-id>
--level <level>
--metric-name <name>
--limit <rows>
--control-socket <path>
```

`diagnose` 默认执行 deep health。

## 配置类别

| 配置段 | 主要用途 |
| --- | --- |
| `[runtime]` | 控制面、PID、日志、节点身份和排空 |
| `[web]` | 公共监听、线程、Header 和超时，不声明应用 |
| `[[apps]]` | 应用代码、路由、数据目录、Session 和环境 |
| `[queue]` | Worker、租约、Bridge 和轮询 |
| `[observability]` | Bridge、批次、Parquet、保留和 JSONL |
| `[bi]` | BI Parquet 发布、DuckDB 查询资源和 Bridge 端口，当前默认关闭 |

完整字段和默认值以[配置样例](../../config/runtime.example.toml)和[配置校验](../../internal/config/config.go)为准。

`[[apps]]` 是唯一应用声明方式。允许配置零个应用，此时 Runtime 可以启动，但所有业务路径返回 503。`web.root`、`serve --root` 和 `ZENTAO_RUNTIME_ROOT` 已移除。

## 扩展约束

为 Runtime 新增能力时：

1. 先确认能力属于 Runtime 还是应用。
2. 明确接口是公共、回环 Private Bridge，还是本地 Control Plane。
3. 为接口定义版本、鉴权、大小限制、超时和稳定错误码。
4. 明确 Runtime 不可用或外部依赖失败时的降级行为。
5. 将必要组件加入 readiness 或 deep health，并区分两者。
6. 增加正常、鉴权失败、格式错误、超时、取消和部分成功测试。
7. 同步修改配置校验、配置模板、契约文档和发送端实现。

兼容规则：

- 配置 v1 不接受未知字段。
- Queue Bridge v1 只能新增可选字段。
- Observability Schema v1 不得改变现有字段类型和语义。
- Control Plane v1 继续使用严格 JSON 和显式错误码。
- 不兼容变化必须升级协议版本并提供迁移策略。

## 契约和源码

| 内容 | 位置 |
| --- | --- |
| 系统边界 | [系统总设计](../design/00-system-overview.md) |
| Runtime Host | [Runtime Host 设计](../design/01-runtime-host.md) |
| 配置加载 | [config.go](../../internal/config/config.go) |
| Web 和 Private Listener | [web/config.go](../../internal/web/config.go) |
| Control Plane | [control/control.go](../../internal/control/control.go) |
| Queue Bridge v1 | [Queue Bridge 契约](../design/contracts/queue-bridge-v1.md) |
| Queue DTO | [queue/bridge/bridge.go](../../internal/queue/bridge/bridge.go) |
| Observability Schema | [observability/events.go](../../internal/observability/events.go) |
| Metric Registry | [observability/registry.go](../../internal/observability/registry.go) |
| BI DTO、Manifest 和 Publisher | [bi/query.go](../../internal/bi/query.go)、[bi/manifest.go](../../internal/bi/manifest.go)、[bi/publisher.go](../../internal/bi/publisher.go) |
| BI Private Bridge | [web/bi.go](../../internal/web/bi.go) |
