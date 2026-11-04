# Runtime PHP Process RPC v1 契约

## 1. 状态与范围

| 项目 | 内容 |
|---|---|
| 状态 | v1 实现 |
| 日期 | 2026-11-04 |
| 传输 | 仅 `127.0.0.1` HTTP/1.1 |
| 编码 | 严格 JSON |
| Schema | `1` |
| SDK | `<install-root>/lib/php/runtime-process.php` |

本契约定义应用 Web 代码与 Runtime 监管的常驻 PHP CLI 进程之间的应用级 RPC。
它不提供持久化、自动重试、消息回放、跨节点转发或业务方法白名单。这些能力不得
通过扩展本契约实现；可靠任务继续使用 Queue Bridge。

## 2. 传输与鉴权

Runtime 每次启动创建随机 `127.0.0.1` TCP 端口，基础路径为：

```text
/internal/runtime/processes/v1
```

所有端点只接受 `POST`，请求头必须包含：

```text
X-Zentao-Process-Schema: 1
X-Zentao-Process-Token: <runtime-generated-token>
```

Web PHP 使用应用级 call Token，只能访问本应用的进程。常驻 PHP CLI 使用当前
实例的 worker Token 和 `instanceId`，只能登记、领取和回复自己的请求。Token
不出现在 URL、查询参数或日志中。

请求体和 worker reply body 最大各 1 MiB；call 响应只包含同一 result 和固定
envelope。媒体类型必须为 JSON。未知字段、错误 schema、超限 body、跨应用 Token
和旧实例一律拒绝。

## 3. 应用侧 Call

```text
POST /apps/{appID}/{processName}/call
```

请求：

```json
{
  "schema": 1,
  "requestId": "8f8a9b1c",
  "method": "message.send",
  "payload": {"topic":"example"},
  "timeoutMs": 5000
}
```

- `requestId` 和 `method` 长度为 1 至 128 个可打印 ASCII 字符。
- `timeoutMs` 可省略；Runtime 以进程配置的 `requestTimeout` 为上限。
- `payload` 对 Runtime 不透明。
- Runtime 不自动重试。

成功：

```json
{
  "schema": 1,
  "requestId": "8f8a9b1c",
  "result": {"accepted":true}
}
```

失败：

```json
{
  "schema": 1,
  "requestId": "8f8a9b1c",
  "code": "outcome_unknown",
  "message": "process request deadline exceeded",
  "delivery": "unknown"
}
```

`delivery` 取值：

- `not_delivered`：请求未交给进程，可确认进程没有执行该方法。
- `delivered`：进程返回了结构化业务错误。
- `unknown`：请求可能已交给进程，调用方不得盲目重试非幂等操作。

## 4. Worker Ready

```text
POST /apps/{appID}/{processName}/ready
```

```json
{"schema":1,"instanceId":"<instance>"}
```

成功后进程进入 Ready。Ready 之前 Runtime 不接受 call。重复 Ready 幂等；旧实例
返回 `409 stale_instance`。

## 5. Worker Next

```text
POST /apps/{appID}/{processName}/next
```

```json
{"schema":1,"instanceId":"<instance>"}
```

Runtime 最多等待约 20 秒。无请求返回 `204 No Content`。

有请求：

```json
{
  "schema": 1,
  "type": "request",
  "request": {
    "schema": 1,
    "requestId": "8f8a9b1c",
    "method": "message.send",
    "payload": {"topic":"example"},
    "deadline": "2026-11-04T02:00:05.000000Z"
  }
}
```

Runtime 停止时：

```json
{"schema":1,"type":"shutdown"}
```

一个常驻进程同时最多持有一个 delivered 请求。后续请求在 Runtime 内有界排队，
超过 `maxPending` 返回 `429 process_busy`。

## 6. Worker Reply

```text
POST /apps/{appID}/{processName}/reply
```

成功结果：

```json
{
  "schema": 1,
  "instanceId": "<instance>",
  "requestId": "8f8a9b1c",
  "result": {"accepted":true}
}
```

结构化业务错误：

```json
{
  "schema": 1,
  "instanceId": "<instance>",
  "requestId": "8f8a9b1c",
  "error": {
    "code": "validation_failed",
    "message": "invalid topic"
  }
}
```

`result` 和 `error` 最多出现一个。未知、过期或不属于当前实例的请求返回
`409 unknown_request` 或 `409 stale_instance`。

## 7. 生命周期与结果语义

- 进程退出前未交付的 call 返回 `503 process_restarted`，`delivery=not_delivered`。
- 进程退出前已交付但未回复的 call 返回 `504 outcome_unknown`，`delivery=unknown`。
- 调用方取消或超时时，未交付请求从队列移除并返回 `deadline_exceeded`。
- 已交付请求超时后 Runtime 不重放，结果只能标记为 unknown。
- Worker 自身抛出的 PHP `Throwable` 映射为 `handler_error`；SDK 不传堆栈和完整
  敏感消息，错误消息最多保留 256 字节。
- Runtime 主动重启或停止时，进程通过 Next 的 shutdown frame 协作退出；超过
  `stopTimeout` 后 Runtime 终止进程树。

## 8. SDK 环境

Web PHP：

```text
ZENTAO_PROCESS_BRIDGE_URL
ZENTAO_PROCESS_APP_ID
ZENTAO_PROCESS_CALL_TOKEN
ZENTAO_PROCESS_SDK
ZENTAO_PROCESS_SCHEMA=1
```

常驻 PHP CLI：

```text
ZENTAO_PROCESS_BRIDGE_URL
ZENTAO_PROCESS_SDK
ZENTAO_PROCESS_SCHEMA=1
ZENTAO_PROCESS_APP_ID
ZENTAO_PROCESS_NAME
ZENTAO_PROCESS_INSTANCE_ID
ZENTAO_PROCESS_WORKER_TOKEN
```

Web 进程不获得 worker Token；常驻进程不获得 call Token。所有变量由 Runtime
最终设置，应用 `env` 不能覆盖安全字段。

## 9. 兼容规则

- 路径版本和 `schema` 必须同时匹配。
- 新增可选字段不改变 v1；删除字段、改变字段含义或错误语义必须发布 v2。
- Runtime 和 SDK 必须来自同一 manifest，不能单独替换其中一个。
- SDK 文件通过 `ZENTAO_PROCESS_SDK` 绝对路径加载，不依赖 Composer。
