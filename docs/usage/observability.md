# 可观测性接入

Runtime 接收应用发送的结构化日志和度量，将事件发布为 Parquet，并提供受限的日志查询接口。

应用不需要接触 DuckDB、Parquet 文件或 spool 目录。应用只需要向 Runtime 的本机端点发送符合 Schema v1 的事件。

## 工作方式

```text
应用
  -> POST /internal/runtime/observability/v1/events
  -> Runtime 校验、脱敏、缓冲
  -> Parquet

应用日志查看器
  -> POST /internal/runtime/observability/v1/query
  -> Runtime 固定查询模板
  -> 应用自己的 Parquet 数据集
```

每个应用使用独立 Token 和数据集。一个应用不能使用自己的 Token 查询另一个应用。

Runtime 自身还会生成访问日志、运行日志和自监控指标。它们写入 Runtime 自己的数据集，不与应用日志混在一起。

## 启用可观测性

```toml
[observability]
enabled = true
bridgePort = 8082
datasetRoot = "data/observability"
spoolPath = "spool/observability"
maxSpoolBytes = 1073741824
maxBatchRows = 10000
maxBatchBytes = 16777216
minBatchRows = 1000
minBatchBytes = 1048576
flushInterval = "60s"
maxFlushInterval = "10m"
metricsDays = 30
logDays = 30
jsonlConvertInterval = "1h"
jsonlConvertSources = ["access", "runtime"]
jsonlKeepDays = 7
jsonlDeleteAfterConvert = true
```

`bridgePort` 只配置端口，Runtime 固定监听 `127.0.0.1:<bridgePort>`。该端口必须与 `web.listen` 和 `queue.bridgePort` 不同。Private Bridge 不提供 TLS，安全边界是“仅本机访问 + 每应用 Token”。

启用后，Runtime 向应用注入：

| 环境变量 | 用途 |
| --- | --- |
| `ZENTAO_OBSERVABILITY_URL` | 事件写入地址 |
| `ZENTAO_OBSERVABILITY_TOKEN` | 当前应用 Token |
| `ZENTAO_OBSERVABILITY_SCHEMA` | Schema 版本，当前为 `1` |
| `ZENTAO_OBSERVABILITY_QUERY_URL` | 日志查询地址，查询桥可用时注入 |
| `ZENTAO_OBSERVABILITY_QUERY_TOKEN` | 查询 Token |
| `ZENTAO_LOG_MODE` | 查询桥可用时为 `flush` |

应用显式配置的环境变量优先。应用应从环境读取这些值，不应把端口、Token 或数据集路径写死。

## 发送事件

```http
POST /internal/runtime/observability/v1/events
Content-Type: application/json
X-Zentao-Observability-Token: <per-application-token>
X-Zentao-Observability-Schema: 1
```

一个日志事件：

```json
{
  "schemaVersion": 1,
  "events": [
    {
      "clusterID": "cluster-prod",
      "nodeID": "node-a",
      "bootID": "boot-01",
      "eventID": "evt-0001",
      "source": "demo",
      "kind": "logs",
      "level": "error",
      "message": "request timed out",
      "durationMs": 30120,
      "statusCode": 504,
      "fields": {
        "topic": "http_client",
        "requestID": "req-01",
        "url": "https://example.test/callback"
      }
    }
  ]
}
```

成功响应：

```json
{"schemaVersion":1,"accepted":1}
```

发送约束：

- 单请求最大 256 KiB。
- 单批 1 至 100 个事件。
- 任一事件不合法时，整批拒绝。
- `clusterID`、`nodeID`、`bootID`、`source` 和 `kind` 必填。
- `eventID` 缺失时由 Runtime 生成。
- `eventTime` 缺失时使用 Runtime 接收时间。
- `ingestTime` 由 Runtime 写入。

常见错误：

| HTTP 状态 | 错误码 | 处理方式 |
| --- | --- | --- |
| `400` | `invalid_request` | 修正 JSON 或批次结构 |
| `400` | `invalid_event` | 检查事件字段、指标名和类型 |
| `400` | `unsupported_schema` | 使用当前 Schema 版本 |
| `401` | `unauthenticated` | 使用当前应用注入的 Token |
| `413` | `request_too_large` | 拆分事件批次 |
| `503` | `unavailable` | Runtime 管道不可用，快速失败 |

应用应设置短超时。可观测性发送失败不能阻塞业务事务，也不能导致已经提交的业务操作回滚。

## 事件字段

所有事件共用一个信封：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `schemaVersion` | integer | 当前为 `1` |
| `eventTime` | timestamp | 事件发生时间，UTC |
| `ingestTime` | timestamp | Runtime 接收时间 |
| `clusterID` | string | 集群 ID |
| `nodeID` | string | 节点 ID |
| `bootID` | string | Runtime 启动实例 ID |
| `eventID` | string | 事件 ID |
| `source` | string | 事件来源 |
| `kind` | string | `logs` 或 `metrics` |

日志事件使用：

```json
{
  "kind": "logs",
  "level": "warning",
  "message": "callback failed",
  "traceID": "trace-01",
  "durationMs": 1200,
  "statusCode": 502,
  "fields": {"topic": "http_client"}
}
```

可选的 `level`、`message`、`traceID`、`durationMs`、`statusCode` 和 `fields` 都应使用稳定字段名。

度量事件使用：

```json
{
  "kind": "metrics",
  "metricName": "zentao_queue_backlog",
  "metricKind": "gauge",
  "value": 12,
  "labels": {
    "channel": "default",
    "status": "queued"
  }
}
```

指标限制：

- `metricName` 必须在固定注册表中。
- `metricKind` 只能是 `counter` 或 `gauge`。
- `value` 必须是数值。
- `labels` 最多 16 个，且不能使用用户 ID、Session ID、完整 URL 或错误原文等高基数字段。
- 当前注册表见[速查参考](reference.md#度量注册表)。

## 脱敏和大小限制

Runtime 在进入缓冲区前会：

- 将 `message` 限制在 16384 字节。
- 将 `fields` 限制为 32 个。
- 将 `labels` 限制为 16 个。
- 替换敏感键，例如 password、token、cookie、session、authorization 和 credential。
- 替换以 Bearer 或 Basic 开头的凭据值。

这是第二道防线。应用必须在发送前完成业务字段筛选，不能把请求体、响应体、SQL 参数、上传内容或数据库凭据放入事件。

## 查询日志

应用日志查看器使用查询桥：

```http
POST /internal/runtime/observability/v1/query
Content-Type: application/json
X-Zentao-Observability-Token: <per-application-token>
X-Zentao-Observability-Schema: 1
```

示例：

```json
{
  "schemaVersion": 1,
  "filters": {
    "fromDate": "20261020",
    "toDate": "20261026",
    "level": "error",
    "topic": "http_client",
    "message": "timeout",
    "offset": 0,
    "limit": 50
  }
}
```

支持：

| 字段 | 说明 |
| --- | --- |
| `start` / `end` | RFC 3339 时间范围 |
| `fromDate` / `toDate` | `YYYYMMDD`，按 UTC 解释 |
| `node` | 节点 ID |
| `level` | 日志级别 |
| `eventID` | 事件 ID |
| `topic`、`module`、`method`、`account`、`requestID` | `fields` 中的精确匹配 |
| `message` | 大小写不敏感子串 |
| `fields` | 其他允许字段的精确匹配 |
| `offset` | 最大 100000 |
| `limit` | 最大 200 |

响应：

```json
{
  "schemaVersion": 1,
  "list": [
    {
      "eventTime": "2026-10-26T01:02:03Z",
      "eventID": "evt-0001",
      "node": "node-a",
      "source": "demo",
      "level": "error",
      "message": "request timed out",
      "traceID": "trace-01",
      "fields": {"topic": "http_client"}
    }
  ],
  "total": 1,
  "truncated": false
}
```

查询桥的能力边界：

- 不接收任意 SQL、文件路径或 DuckDB Extension。
- 默认查询最近 7 天。
- 最大跨度由 `observability.logDays` 控制。
- 单次查询有 10 秒超时。
- DuckDB 限制线程数、内存和返回行数。
- 只能看到已经原子发布的 Parquet 文件。
- 刚写入内存缓冲或本地 spool 的事件不会立即可见。

## 发布和保留

事件按 `logs` 和 `metrics` 分开缓冲。达到行数或字节阈值、`flushInterval` 到期或达到 `maxFlushInterval` 时，Runtime 将事件写入不可变 Parquet 文件。

数据集按日期、小时和节点分区：

```text
<dataset>/logs/schema=v1/date=YYYY-MM-DD/hour=HH/node=<node-id>/part-*.parquet
<dataset>/metrics/schema=v1/date=YYYY-MM-DD/hour=HH/node=<node-id>/part-*.parquet
```

NFS 或数据集目录暂时不可写时，事件进入本地有界 spool。Runtime 恢复后按原批次重试。spool 超过 `maxSpoolBytes` 后丢弃新事件，并增加丢弃计数。

保留期：

- 日志由 `observability.logDays` 控制，默认 30 天。
- 指标由 `observability.metricsDays` 控制，默认 30 天。
- 原始 Runtime JSONL 由 `jsonlKeepDays` 控制，默认 7 天。

节点的保留任务只清理自己的分区。

## CLI 查询说明

`zentao logs` 和 `zentao metrics` 查询 Runtime 自身数据集：

```bash
./bin/zentao logs --since 30m --level error --node node-a --limit 200
./bin/zentao metrics --since 1h --metric-name http.request.duration --limit 100
```

它们不会自动扫描各应用的数据集。应用日志应由应用使用注入的查询桥读取。

## 扩展指标

新增指标必须同时完成：

1. 在 Runtime 的指标注册表中登记名称。
2. 确认指标类型为 `counter` 或 `gauge`。
3. 更新所有发送该指标的应用实现。
4. 增加 Runtime 和发送端契约测试。
5. 评审标签基数和脱敏规则。

不能通过用户输入动态创建指标名或标签键。

## 相关实现

- [Observability Schema](../../internal/observability/events.go)
- [Metric Registry](../../internal/observability/registry.go)
- [事件接收](../../internal/web/observability.go)
- [日志查询](../../internal/web/observability_query.go)
- [可观测性设计](../design/05-observability.md)
