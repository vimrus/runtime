# PHP Queue Bridge v1 契约

## 1. 状态与范围

本文件冻结 Runtime Alpha 的 PHP Queue Bridge v1 数据契约。Bridge 是 `zentao-runtime`
与禅道 PHP Queue Service 之间的私有接口，不是公共 Web API，也不属于 APIv2。

实施状态（2026-08-19）：Go 侧 DTO、Fake Bridge、HTTP 客户端、Worker/租约/Reaper
与 Host 集成已完成；真实 PHP 路由、Caddy 专用 loopback Listener、每次启动随机
凭据及端到端鉴权属于应用侧 `Z-QUEUE-03` 的联合工作，尚未实施。

Go Runtime 不连接数据库、不导入数据库 Driver、不保存数据库凭据，也不包含业务 SQL。

## 2. 传输约束

- 所有操作使用 `POST` 和 JSON，路径固定在 `/internal/runtime/queue/v1/` 下。
- 内部 Listener 只能绑定 loopback；公共站点不得路由到这些路径。
- 每次 Runtime 启动生成内部认证凭据。凭据通过专用 Header 传递，绝不记录到日志。
- 每个请求与响应根对象均包含整数 `schema`，v1 固定为 `1`。
- JSON 必须只有一个根值，且拒绝未知字段。
- 请求和响应 Body 上限均为 64 KiB；批量 Claim、Heartbeat、Stats 的单次数量上限为 128。
- 时间统一使用 UTC RFC 3339 字符串，并要求微秒精度；PHP 负责与数据库字段的精度适配。
- 字段命名与禅道队列表列名保持一致（`uuid`、`channel`、`leaseEnd`、
  `heartbeatTime` 等），PHP 不进行字段名映射，只做时间格式与精度转换。

## 3. 端点

| 路径 | 请求 | 响应 | 目的 |
|---|---|---|---|
| `/capabilities` | `CapabilitiesRequest` | `CapabilitiesResponse` | 启动时确认 Schema 与 DAO 能力 |
| `/claim` | `ClaimRequest` | `ClaimResponse` | 批量领取租约 |
| `/execute` | `ExecuteRequest` | `ExecuteResponse` | 执行注册 Handler 并持久化结果 |
| `/heartbeat` | `HeartbeatRequest` | `HeartbeatResponse` | 批量延长租约 |
| `/reap` | `ReapRequest` | `ReapResponse` | 回收过期租约 |
| `/stats` | `StatsRequest` | `StatsResponse` | 获取低频队列快照 |
| `/control` | `ControlRequest` | `ControlResponse` | 暂停、恢复、取消和人工重试 |

完整 Go 定义是 [bridge.go](../internal/queue/bridge/bridge.go) 的唯一实现来源；PHP 实现和跨仓夹具必须与其字段一致。

## 4. 租约与 fencing

`ClaimResponse.leases` 只包含 `uuid`、`channel`、`handler`、`attempt`、`leaseToken`、
`leaseEnd`、`timeoutSeconds` 和 `traceID`，**不包含业务 Payload**。

`ExecuteRequest` 必须携带 `uuid`、正整数 `attempt`、`leaseToken` 和 `traceID`。
Heartbeat 中每个 `LeaseRef` 也必须携带 `uuid`、`attempt`、`leaseToken`。取消、人工重试等对运行中任务有状态影响的 Control 操作同样要求 `leaseToken`。

PHP 只能使用 `uuid + leaseToken + running` 的条件更新 ACK、Retry、Failed、Canceled 和
Heartbeat。条件更新未命中意味着租约已失效；旧执行结果只能作为 `stale_result` 记录，不能覆盖新租约。

## 5. 结果与错误

`ExecuteResponse.result` 只允许 `success`、`retry`、`failed`、`canceled`。`retry` 可以提供非负的 `retryAfterSeconds`；其他结果必须为零。

Heartbeat 对每条租约独立返回 `extended`、`stale`、`not_found` 或 `error`，因此一个批次可以部分成功。`extended` 必须返回新的 `leaseEnd`。

可跨实现稳定识别的错误码为：

- `unauthenticated`
- `unsupported_schema`
- `invalid_request`
- `request_too_large`
- `invalid_response`
- `unavailable`
- `conflict`
- `internal`

错误对象为 `{ "code": "...", "message": "...", "retryable": false }`。错误消息必须脱敏并限制长度；不得包含 Payload、认证凭据或数据库连接信息。

## 6. Fake Bridge

`internal/queue/fake` 提供可编程的内存 Fake Bridge，会记录各端点请求并返回预设响应。它用于 Runtime Worker 生命周期、批处理与错误路径测试，可以模拟部分 Heartbeat 失败和桥接层错误。

Fake Bridge 不执行 SQL，不模拟数据库并发，也不能替代 MySQL、PostgreSQL 或信创数据库上的 PHP DAO 并发、租约恢复和 fencing 契约测试。

## 7. 兼容规则

未知请求字段、错误的 Schema、超过大小或批次限制的请求必须在触发业务操作前拒绝。新增必填字段或改变字段语义必须升级为 `/v2`；v1 只能新增可选字段，并同时更新 Go/PHP 共享夹具和契约测试。

## 8. 实施状态（2026-08-19）

- Go 侧 DTO、Fake Bridge、HTTP 客户端、Worker/租约/Reaper 与 Host 集成
  已实现并通过单测；`internal/queue/bridge` 是本契约的唯一 Go 实现来源。
- PHP 侧 Bridge 路由、鉴权与 Queue Service（`Z-QUEUE-03` 等）尚未实施，
  实施时必须以本文件与 adaptation plan 第 17.2 节为准，并补齐跨仓夹具。
