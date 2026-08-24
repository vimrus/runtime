# Runtime Cache Bridge v1 契约

## 1. 范围

本文定义 PHP/Runtime 组件访问 `zentao-runtime` Cache Service 的本机契约。它不是公共 Web API，也不允许通过业务路由转发。缓存是可丢弃数据，服务重启、节点切换和主动清空都属于兼容行为。

## 2. 传输约束

- 所有操作使用 `POST` 和 JSON，路径固定在 `/internal/runtime/cache/v1/` 下。
- Listener 只绑定 loopback 或本机 IPC；公共 Caddy 配置必须拒绝这些路径。
- Runtime 每次启动生成随机认证凭据，凭据不写入日志、审计或诊断包。
- 请求和响应根对象包含整数 `schema`，v1 固定为 `1`；拒绝未知请求字段。
- 请求和响应 Body 默认上限 64 KiB，单批键数默认上限 128。
- namespace、key、TTL、值类型和编码大小均由服务校验。

## 3. 操作

| 路径 | 请求 | 响应 | 语义 |
|---|---|---|---|
| `/capabilities` | `CapabilitiesRequest` | `CapabilitiesResponse` | 协商支持的类型、大小和后端能力 |
| `/get` | `GetRequest` | `GetResponse` | 返回 `hit`、类型和值或 miss |
| `/set` | `SetRequest` | `SetResponse` | 写入 TTL 值，失败不影响调用方结果 |
| `/delete` | `DeleteRequest` | `DeleteResponse` | 按 namespace/key 幂等删除 |

业务客户端只使用显式操作；不提供 `remember`、自动回源、前缀扫描或任意批量清空。

## 4. 键和值

客户端发送原始 `namespace` 和 `key`，服务计算：

```text
keyHash = SHA-256(namespace + "\0" + key)
```

服务不接受客户端直接指定摘要，也不将完整 key 写入观测数据。值类型只允许 `string` 和 `json`，禁止 PHP 序列化对象、资源、闭包和可执行内容。命中空值必须通过 `hit=true` 与 miss 区分。

## 5. 错误与降级

稳定错误码：

- `unauthenticated`
- `unsupported_schema`
- `invalid_request`
- `request_too_large`
- `value_too_large`
- `quota_exceeded`
- `unavailable`
- `internal`

缓存客户端遇到 `unavailable`、超时或服务重启时按 miss/写入失败处理；禁止连接禅道业务数据库代替 Cache Service。错误消息必须脱敏、限长，不能包含完整 key、值、凭据或文件路径。

## 6. 兼容规则

v1 可以增加可选响应字段，但不能改变 `hit`、TTL、值类型或错误码语义。新增必填字段、改变编码或增加不兼容操作必须升级到 `/v2`。PHP/Go 共享夹具和 capability 响应必须同步更新。

## 7. 安全和资源限制

Cache Bridge 使用本机身份和随机启动凭据鉴权；每个 namespace 必须来自注册表。服务端设置总内存、单值、namespace、并发和 Body 上限，超限只拒绝缓存操作，不影响 Web、Queue 或升级。

## 8. 测试边界

Fake Cache 可以覆盖客户端协议、错误和生命周期测试，但不能替代真实多线程、内存压力、服务重启、节点切换和 PHP Classic mode 联合测试。缓存内容不进入业务数据库契约测试；只在 Runtime 和跨进程契约矩阵中验证。
