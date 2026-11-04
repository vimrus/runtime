# Queue Bridge 对接指南

这篇文档面向应用对接人员。应用负责任务数据、数据库操作和业务 Handler，
Runtime 的核心作用是周期调用应用接口，驱动队列任务产生和执行：

1. **生成定时任务**：Runtime 周期性调用 `schedule`，请求应用扫描到期的定时
   规则，由应用把新任务写入队列。
2. **执行队列任务**：Runtime 周期性调用 `claim` 从应用领取任务，再调用
   `execute` 请求应用执行对应的 Handler。
3. **保障执行可靠**：任务运行期间调用 `heartbeat` 续租；Worker 异常或租约过期
   后调用 `reap`，由应用决定重试或失败。

普通业务任务仍然由应用在自己的业务事务中创建，Runtime 不负责业务入队。
Runtime 不连接业务数据库，不接收业务 Payload，也不执行业务 SQL。

## 1. 任务生命周期

队列任务使用以下状态：

```text
queued/retrying -> running -> succeeded
                              -> retrying
                              -> failed
                              -> canceled
```

完整状态机：

```text
queued   --claim--> running
retrying --claim--> running

running --execute(success)--> succeeded
running --execute(retry)----> retrying
running --execute(failed)---> failed
running --execute(canceled)-> canceled

running --control(cancel)--> running[cancelRequested=1]
running[cancelRequested=1] --execute completion--> canceled

running --heartbeat---------> running
running --lease expired-----> retrying/failed

failed   --control(retry)--> queued
canceled --control(retry)--> queued
```

| 状态 | 含义 |
| --- | --- |
| `queued` | 可以领取 |
| `retrying` | 到达可领取时间后可以再次领取 |
| `running` | 已被 Worker 领取，存在有效或过期租约 |
| `succeeded` | 成功终态 |
| `failed` | 失败终态；人工重试后回到 `queued` |
| `canceled` | 取消终态；人工重试后回到 `queued` |

`cancelRequested` 是运行中任务的取消标记，不是独立状态。任务完成时如果该标记
存在，最终状态写为 `canceled`。

暂停和恢复不是任务状态，而是 Channel 级别的领取开关。

### 示例：发送邮件任务

应用写入一条任务：

```text
uuid    = task-01
channel = mail
handler = mail.send
payload = {"to":"user@example.com"}
status  = queued
attempt = 0
```

之后：

1. Runtime 调用 `claim`。
2. 应用把任务从 `queued` 改成 `running`，生成 `leaseToken`，把 `attempt`
   改成 1。
3. Runtime 调用 `execute`。
4. 应用按 UUID 读取 Payload，执行 `mail.send`，把任务改成 `succeeded`。

如果 Handler 返回 `retry`，任务改为 `retrying` 并设置 `availableDate`。如果
Worker 崩溃，租约过期后由 `reap` 把任务改为 `retrying`；达到最大尝试次数则改为
`failed`。

## 2. 状态迁移与应用接口

对接相关的应用接口有两类：

- Queue Bridge HTTP 端点：Runtime 驱动任务生命周期。
- Handler Registry：执行具体业务任务。

业务任务入队属于应用内部实现，不属于 Runtime 对接协议。

| 生命周期事件 | 状态迁移 | 应用侧接口 |
| --- | --- | --- |
| 业务创建任务 | 无 → `queued` | 应用内部实现，不属于 Runtime 接口 |
| 定时规则到期 | 无 → `queued` | Bridge `schedule` |
| Worker 领取 | `queued/retrying` → `running` | Bridge `claim` |
| Handler 成功 | `running` → `succeeded` | Bridge `execute` + Handler |
| Handler 要求重试 | `running` → `retrying` | Bridge `execute` + Handler |
| Handler 永久失败 | `running` → `failed` | Bridge `execute` + Handler |
| Handler 取消 | `running` → `canceled` | Bridge `execute` + Handler |
| 运行中续租 | `running` → `running` | Bridge `heartbeat` |
| Worker 崩溃后续租失败 | `running` → `retrying/failed` | Bridge `reap` |
| 人工取消 | `running` → `canceled` | Bridge `control/cancel` + 后续 `execute` |
| 人工重试 | `failed/canceled` → `queued` | Bridge `control/retry` |
| 暂停或恢复 Channel | 不改变任务状态 | Bridge `control/pause/resume` |
| 查看积压和状态 | 不改变任务状态 | Bridge `stats` |
| 启动能力协商 | 不改变任务状态 | Bridge `capabilities` |

## 3. 建立 Bridge 连接

应用在 Runtime 配置中声明 Bridge 路由：

```toml
[[apps]]
id = "zentao"
path = "/"
dir = "apps/zentao"
webRoot = "www"
enabled = true
queue = { enabled = true, bridgeUrlTemplate = "/bridge.php?endpoint={endpoint}" }
```

模板必须是应用内路径，以 `/` 开头、恰好包含一个 `{endpoint}`，且不能包含协议、
空白或 `..`。

应用需要实现：

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

Runtime 按模板把 `<endpoint>` 替换为对应接口路径。Runtime 的内部 Listener
地址和路径属于实现细节，应用不需要配置或感知。

应用只需要从 `ZENTAO_QUEUE_TOKEN` 读取本应用 Token，并校验请求中的
`X-Zentao-Queue-Token`。

### 公共协议

所有 Bridge 接口必须：

- 只接受 `POST` 和 `application/json`。
- 校验请求头：

```text
X-Zentao-Queue-Token: <ZENTAO_QUEUE_TOKEN>
X-Zentao-Queue-Schema: 1
```

- JSON 根对象包含 `"schema": 1`。
- 限制请求和响应不超过 64 KiB。
- 批量接口最多处理 128 项。
- 拒绝未知字段、多个 JSON 根值和错误 Schema。
- 使用 UTC RFC 3339，精确到微秒。
- 拒绝公共 Web 请求绕过 Runtime 直接访问 Bridge。

错误响应：

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

### capabilities：启动协商

Runtime 启动时调用 `capabilities`，确认应用 Schema 和数据库领取能力。

请求：

```json
{"schema":1,"nodeID":"node-a","instanceID":"boot-01"}
```

响应：

```json
{
  "schema": 1,
  "queueSchema": 1,
  "driver": "mysql",
  "claimMode": "portable_cas",
  "maxClaimBatch": 128
}
```

`claimMode` 只能是：

- `portable_cas`：通用条件更新领取。
- `skip_locked`：只有对应数据库适配器完成能力验证后才能声明。

`maxClaimBatch` 必须在 1 到 128 之间。

## 4. 创建任务：无状态 → queued

Runtime 不提供业务任务投递接口。普通业务任务如何产生、使用什么业务 API、采用
什么事务边界和幂等键，都由应用自行设计，不属于 Queue Bridge 协议。

为了后续能够被 Worker 领取，应用内部产生的任务至少需要保存 `uuid`、
`channel`、`handler`、Payload、`status`、`availableDate` 和 `attempt`。应用
应自行保证入队幂等。

Runtime 会周期性调用 `schedule`。应用在这里扫描到期规则，并为每个“规则 + 本次
计划时间”创建一条 `queued` 任务。`schedule` 不执行任务。

请求：

```json
{"schema":1,"nodeID":"node-a","instanceID":"boot-01"}
```

响应：

```json
{"schema":1,"scheduled":true}
```

同一规则可能被重复扫描。例如规则 ID 为 `12`，计划执行时间是
`2026-11-04 10:00:00`，应用应保证这组值只生成一条任务；通常可以使用业务唯一
约束或幂等键实现。

## 5. 领取任务：queued/retrying → running

Runtime 调用 `claim`：

```json
{
  "schema": 1,
  "nodeID": "node-a",
  "instanceID": "boot-01",
  "workerID": "worker-01",
  "channels": ["mail"],
  "limit": 8,
  "leaseSeconds": 60
}
```

应用按 `availableDate`、优先级和 Channel 查找候选任务，然后执行事务：

1. 校验任务仍是 `queued` 或 `retrying`。
2. 使用 `status + version` 条件更新。
3. 把状态改为 `running`。
4. 增加 `attempt`。
5. 写入 `leaseOwner`、`leaseToken`、`leaseEnd` 和 `heartbeatTime`。
6. 提交事务后返回租约。

响应：

```json
{
  "schema": 1,
  "leases": [
    {
      "uuid": "task-01",
      "channel": "mail",
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

没有可领取任务时返回：

```json
{"schema":1,"leases":[]}
```

响应中不得包含业务 Payload。并发领取失败时，应用应跳过已被其他 Worker 领取的
任务，继续尝试其他候选任务。

## 6. 执行任务：running → succeeded/retrying/failed/canceled

Runtime 调用 `execute`：

```json
{
  "schema": 1,
  "uuid": "task-01",
  "attempt": 1,
  "leaseToken": "lease-01",
  "traceID": "trace-01"
}
```

应用按以下顺序处理：

1. 按 UUID 读取任务。
2. 校验 `attempt`、`leaseToken`、`status=running` 和租约未过期。
3. 从任务记录读取 Handler 名称和 Payload。
4. 从 Handler Registry 查找固定实现。
5. 执行 Handler 并记录本次 Attempt。
6. 使用租约 fencing 更新最终状态。

Handler Registry 示例：

```php
$handlers = array(
    'mail.send'       => function(array $payload): array {
        // 调用应用现有的邮件服务。
        return array('result' => 'success');
    },
    'webhook.deliver' => function(array $payload): array {
        // 调用应用现有的 Webhook 服务。
        return array('result' => 'retry', 'retryAfterSeconds' => 30);
    },
);
```

Handler 结果和状态迁移：

| Handler 结果 | 下一状态 |
| --- | --- |
| `success` | `succeeded` |
| `retry` | `retrying`，超过最大尝试次数时 `failed` |
| `failed` | `failed` |
| `canceled`、或存在 `cancelRequested` | `canceled` |

响应：

```json
{"schema":1,"result":"success"}
```

`retry` 可以携带非负的 `retryAfterSeconds`。其他结果不能携带该字段。

租约不匹配或已过期时，应用不能执行新的业务副作用。可以返回：

```json
{"schema":1,"result":"failed","code":"stale_lease"}
```

Handler 必须幂等。Handler 已经产生业务效果、但结果未写回数据库时，任务可能在
租约过期后再次执行。

## 7. 续租：running → running

Runtime 为运行中的任务批量调用 `heartbeat`：

```json
{
  "schema": 1,
  "nodeID": "node-a",
  "instanceID": "boot-01",
  "leases": [
    {"uuid":"task-01","attempt":1,"leaseToken":"lease-01"}
  ]
}
```

应用逐项返回：

```json
{
  "schema": 1,
  "results": [
    {
      "uuid": "task-01",
      "attempt": 1,
      "status": "extended",
      "leaseEnd": "2026-10-26T01:03:03.000000Z"
    }
  ]
}
```

每项状态允许：

| 状态 | 含义 |
| --- | --- |
| `extended` | 租约已延长 |
| `stale` | Token、attempt 或租约已失效 |
| `not_found` | 任务不存在 |
| `error` | 单项续租失败 |

一批 Heartbeat 可以部分成功。应用必须逐项处理，不能因为前一项失败而跳过其余
租约。

## 8. 回收过期任务：running → retrying/failed

Runtime 周期性调用 `reap`：

```json
{"schema":1,"nodeID":"node-a","instanceID":"boot-01","limit":100}
```

应用查找 `status=running` 且 `leaseEnd` 已过期的任务，并使用
`status + version + leaseToken + leaseEnd` 条件更新：

- 未达到 `maxAttempts`：改为 `retrying`，设置下一次 `availableDate`。
- 已达到 `maxAttempts`：改为 `failed`。

响应：

```json
{"schema":1,"retried":8,"failed":2}
```

应用还需要写入执行记录，例如 `lease_lost` 或 `attempts_exhausted`。

## 9. 管理任务：control

`control` 根据 Action 执行不同迁移：

| Action | 前置状态 | 处理结果 |
| --- | --- | --- |
| `pause` | Channel 正常 | 暂停该 Channel 的新领取 |
| `resume` | Channel 暂停 | 恢复该 Channel 的领取 |
| `cancel` | `running` 且租约匹配 | 设置 `cancelRequested=1` |
| `retry` | `failed/canceled` | 改为 `queued`，重置 attempts 和租约 |

`cancel` 和 `retry` 必须携带 `uuid` 与对应运行的 `leaseToken`。

取消是协作式的。`control/cancel` 不会强杀 Handler；当前 Handler 完成时，应用
看到 `cancelRequested=1` 后把最终状态写为 `canceled`。如果 Worker 在完成前
消失，任务会先由 Reaper 回收，下一次执行时再转为 `canceled`。

管理操作必须经过应用权限校验并写入审计日志。

## 10. 查询队列：stats

`stats` 不改变任务状态，返回各 Channel 的：

```text
queued
running
retrying
failed
oldestAvailableAt
```

这些数据用于管理界面、积压告警和运行诊断。高基数字段，例如 UUID、用户和完整
错误消息，不应作为统计维度。

## 11. 租约 fencing 和幂等

以下更新必须使用租约条件：

```text
uuid + attempt + leaseToken + running
```

旧租约不能覆盖新租约。条件更新未命中时，应用记录 `stale_result`，不能把任务
重新写成成功或失败。

队列语义是 At-least-once。以下情况都可能重复执行：

- Handler 已执行，但结果没有写回数据库。
- Runtime 或 PHP 进程崩溃。
- 租约过期后由其他 Worker 领取。
- `execute` 响应丢失。

Handler 必须使用唯一约束、状态检查或业务幂等键。

## 12. 对接验收

- [ ] 应用内部任务生产路径能写入 `queued`，且不依赖 Runtime。
- [ ] `schedule` 创建的定时任务具有稳定幂等键。
- [ ] `claim` 使用事务和 `status + version` 条件更新。
- [ ] `claim` 返回的租约不包含业务 Payload。
- [ ] `execute` 校验租约、读取 Payload，并只执行注册 Handler。
- [ ] `execute` 正确迁移到 `succeeded/retrying/failed/canceled`。
- [ ] `heartbeat` 支持批量部分成功。
- [ ] `reap` 能把过期任务转为 `retrying` 或 `failed`。
- [ ] `control/cancel` 和 `control/retry` 遵守状态迁移。
- [ ] 旧租约结果不能覆盖新 attempt。
- [ ] Handler 已通过重复执行测试。
- [ ] Bridge 拒绝公共请求，且 Token、Payload 和数据库凭据不进入日志。

完整字段、错误码和响应结构见
[PHP Queue Bridge v1 契约](../design/contracts/queue-bridge-v1.md)。
