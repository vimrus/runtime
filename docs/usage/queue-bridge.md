# Queue Bridge 接入

Runtime 提供 Queue Worker，但任务数据和业务 Handler 留在应用侧。两者通过本机 Queue Bridge v1 交互。

这篇文档说明应用如何配置和实现该 Bridge，以及 Runtime 会怎样领取、执行和回收任务。

## 调用方向

```text
应用写入并持久化任务
          |
          v
     应用数据库
          ^
          |
Runtime Worker --claim--> Queue Bridge
       |                    |
       +----execute-------->应用 Handler
       |                    |
       +----heartbeat------>任务租约
       |                    |
       +----reap---------->过期任务
```

Runtime 不向应用提供业务投递 API。应用应在自己的事务中写入任务，再由 Runtime 领取。

职责边界：

| Runtime 负责 | 应用负责 |
| --- | --- |
| Worker Pool、并发和轮询退避 | 队列表和业务事务 |
| 领取节奏、租约续期和回收触发 | Handler 注册和执行 |
| Bridge 本机路由、Token 和 Schema 校验 | Bridge v1 端点实现 |
| 优雅停止和 Worker 生命周期 | 任务完成状态和业务幂等 |

## 配置

全局 Worker 配置：

```toml
[queue]
enabled = true
bridgePort = 8081
requestTimeout = "5s"
claimBatch = 8
leaseSeconds = 60
heartbeatInterval = "20s"
reapInterval = "10s"
reapBatch = 100
drainTimeout = "30s"

[[queue.workers]]
name = "default"
concurrency = 4
minPoll = "50ms"
maxPoll = "5s"
```

应用声明自己的 Bridge：

```toml
[[apps]]
id = "demo"
path = "/"
dir = "apps/demo-1.0.0"
webRoot = "www"
enabled = true
queue = { enabled = true, bridgeUrlTemplate = "/bridge.php?endpoint={endpoint}" }
```

`bridgeUrlTemplate` 必须是应用内路径：

- 以 `/` 开头。
- 恰好包含一个 `{endpoint}`。
- 不包含协议、空白或 `..`。

`bridgePort` 只配置端口。Runtime 固定监听 `127.0.0.1:<bridgePort>`，不接受其他监听地址。该端口必须与 `web.listen` 和 `observability.bridgePort` 不同。

每个 Queue 应用使用独立的本机命名空间和 Token：

```text
/internal/runtime/queue/v1/apps/<app-id>/<endpoint>
```

Runtime 监听这个回环地址，完成 Token 校验后，把请求重写到应用模板。应用通过 `ZENTAO_QUEUE_TOKEN` 读取自己的 Token。

## 启动协商

Runtime 启动 Queue 引擎后先调用 `capabilities`：

```json
{"schema":1,"nodeID":"node-a","instanceID":"boot-01"}
```

应用返回：

```json
{
  "schema": 1,
  "queueSchema": 1,
  "driver": "mysql",
  "claimMode": "portable_cas",
  "maxClaimBatch": 128
}
```

字段说明：

| 字段 | 说明 |
| --- | --- |
| `driver` | 应用使用的数据库 Driver 名称，不能为空 |
| `claimMode` | `portable_cas` 或 `skip_locked` |
| `maxClaimBatch` | 1 至 128 |
| `queueSchema` | 必须大于等于 1 |

Runtime 最多重试协商 10 次，每次间隔 500 毫秒。协商失败不会阻止 Web readiness，但该 Queue 引擎不会开始消费。检查：

```bash
./bin/zentao health --deep
```

## 通用传输约束

所有 Queue Bridge 操作：

- 使用 `POST`。
- 使用 JSON。
- 路径以 `/internal/runtime/queue/v1/` 开头。
- 请求和成功响应根对象包含 `schema: 1`。
- 请求和响应不超过 64 KiB。
- 未知字段会被拒绝。
- 只接受一个 JSON 根值。
- 携带 `X-Zentao-Queue-Token`。
- 携带 `X-Zentao-Queue-Schema: 1`。
- 时间使用 UTC RFC 3339，精确到微秒。

HTTP 错误体：

```json
{
  "error": {
    "code": "unavailable",
    "message": "queue storage unavailable",
    "retryable": true
  }
}
```

稳定错误码：

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

错误消息必须脱敏并限制长度，不得包含 Payload、数据库连接信息或认证凭据。

## 端点

| 端点 | 当前用途 |
| --- | --- |
| `capabilities` | 启动时协商协议、Driver 和领取方式 |
| `schedule` | Runtime 触发应用扫描到期定时规则 |
| `claim` | 批量领取任务租约 |
| `execute` | 请求应用执行领取到的任务 |
| `heartbeat` | 批量延长运行中任务的租约 |
| `reap` | 回收过期租约 |
| `stats` | 返回低频队列快照 |
| `control` | 暂停、恢复、取消和人工重试 |

Runtime 当前 Worker 主链路使用 `capabilities`、`claim`、`execute`、`heartbeat`、`reap` 和 `schedule`。`stats` 和 `control` 是 v1 契约的一部分，供队列管理集成使用。

Runtime 启动后立即触发一次 `schedule`，之后每 20 秒触发一次。应用只负责扫描到期规则并持久化任务，不在这里执行任务。

## Claim

请求：

```json
{
  "schema": 1,
  "nodeID": "node-a",
  "instanceID": "boot-01",
  "workerID": "worker-01",
  "channels": ["default"],
  "limit": 8,
  "leaseSeconds": 60
}
```

响应：

```json
{
  "schema": 1,
  "leases": [
    {
      "uuid": "task-01",
      "channel": "default",
      "handler": "mail.send",
      "attempt": 1,
      "leaseToken": "lease-01",
      "leaseEnd": "2026-10-26T01:02:03.000000Z",
      "timeoutSeconds": 60,
      "traceID": "trace-01"
    }
  ]
}
```

租约不包含业务 Payload。应用必须根据 `uuid` 和 `handler` 从自己的持久化任务中取得 Payload。把完整 Payload 塞入 Bridge 会突破 64 KiB 限制，也会让 Runtime 意外承担业务数据边界。

## Execute

Runtime 对每个领取成功的租约调用：

```json
{
  "schema": 1,
  "uuid": "task-01",
  "attempt": 1,
  "leaseToken": "lease-01",
  "traceID": "trace-01"
}
```

应用返回：

```json
{"schema":1,"result":"success"}
```

`result` 只允许：

| 结果 | 含义 |
| --- | --- |
| `success` | 成功完成 |
| `retry` | 稍后重试 |
| `failed` | 终止失败 |
| `canceled` | 已取消 |

`retry` 可以额外返回非负的 `retryAfterSeconds`。其他结果不得携带该字段。

Handler 必须幂等。任务可能因为进程崩溃、租约过期或响应丢失而重复执行。

## Heartbeat 和 Reaper

Heartbeat 对每条租约独立返回：

- `extended`
- `stale`
- `not_found`
- `error`

`extended` 必须返回新的 `leaseEnd`。一批 Heartbeat 可以部分成功，Runtime 会分别处理每条租约。

应用更新任务结果时必须使用：

```text
uuid + leaseToken + running
```

如果旧租约已经失效，只记录 `stale_result`，不能覆盖新租约。

Reaper 由 Runtime 按 `reapInterval` 触发。应用负责根据 `maxAttempts` 将过期任务转为 retrying 或 failed，并写入执行审计。

## 取消和人工控制

`control` 支持：

- `pause`
- `resume`
- `cancel`
- `retry`

`cancel` 和 `retry` 必须携带 `uuid` 与 `leaseToken`。`pause` 和 `resume` 按 `channel` 控制。

取消是协作式的。运行中任务先记录取消请求，应用在完成或下次领取时转为 canceled。

## 停止和故障

Runtime 停止时：

1. 停止领取新任务。
2. 在 `queue.drainTimeout` 内等待运行中的 Worker 返回。
3. 未完成的任务由租约过期和 Reaper 恢复。

Queue Bridge 暂时不可用时，Worker 按轮询间隔退避并记录 Runtime 日志。Bridge 故障不会改变 Web readiness。

应用必须保证：

- Runtime 访问的 Queue Bridge 只能经由回环 Listener 暴露；应用还必须拒绝公共 Web 对该 Bridge 路径的直接访问。
- Token 不写入日志或错误响应。
- 未知字段和错误 Schema 在执行任何业务操作前拒绝。
- Handler 和 Payload 都受应用白名单约束。

## 兼容规则

- v1 只能新增可选字段。
- 新增必填字段、改变现有字段语义或改变错误语义必须发布 v2。
- 每次修改 DTO 都要同步更新运行时实现、应用实现和跨端契约测试。
- PHP、Go 和其他语言实现都应以 [Queue Bridge v1 契约](../design/contracts/queue-bridge-v1.md)和 [Go DTO](../../internal/queue/bridge/bridge.go)为字段来源。

## 接入检查

- [ ] `bridgeUrlTemplate` 合法并能正确路由。
- [ ] `capabilities` 能完成启动协商。
- [ ] `claim` 不返回业务 Payload。
- [ ] `execute` 执行白名单内的 Handler。
- [ ] Heartbeat 支持部分成功。
- [ ] 完成更新使用租约 fencing。
- [ ] Handler 已验证重复执行。
- [ ] Queue Token 不出现在日志或诊断包中。
- [ ] 停止、租约过期和 Worker 崩溃已联合测试。
