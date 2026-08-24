# zentao Runtime 缓存设计

## 1. 文档信息

| 项目 | 内容 |
|---|---|
| 状态 | Runtime Cache Service 设计基线；PHP 客户端和首批业务接入待实施 |
| 服务归属 | Runtime Host 内嵌 Cache Service |
| 数据库访问 | Runtime 不连接禅道业务数据库 |
| 首版后端 | 进程内有界内存缓存，重启后可丢失 |
| 访问方式 | PHP/Runtime 组件通过本机私有、版本化 Cache Bridge |
| 关联设计 | [系统总设计](./00-system-overview.md)、[Runtime Host](./01-runtime-host.md)、[Runtime Cache Bridge v1](./contracts/runtime-cache-v1.md)、[可观测性](./05-observability.md) |

## 2. 核心结论

Runtime 提供统一的缓存服务和客户端协议。缓存不再使用禅道业务数据库缓存表，Go 不连接 MySQL、PostgreSQL、达梦或其他禅道业务数据库，也不复制 PHP DAO 的连接和方言体系。

首版缓存是 Runtime 进程内的有界内存存储：按 namespace/key 做摘要，支持显式 `get`、`set`、`delete`、TTL、容量限制、LRU/过期淘汰和故障降级。Runtime 重启、进程崩溃、节点切换或主动清理都可以清空缓存；业务正确性不能依赖缓存内容。

缓存访问必须经过本机私有 Bridge。公共 Web 请求不能直接访问管理端点，缓存协议拒绝任意 SQL、文件路径、序列化对象和未注册操作。多节点每个节点拥有独立缓存，不能用缓存实现跨节点锁、计数器或一致性广播。

## 3. 目标和非目标

### 3.1 目标

- 给 PHP 应用和 Runtime 组件提供低延迟、统一、可观测的显式缓存。
- 通过 Runtime 统一控制内存预算、TTL、淘汰、限流和降级。
- 将缓存访问从业务数据库连接和事务中隔离。
- 在单机上开箱即用，在多节点上允许独立失效并自动回源。
- 使用版本化协议，便于未来增加本地磁盘、外部 Redis 或其他后端。

### 3.2 非目标

- 不自动拦截 SQL，不透明缓存 DAO 查询。
- 不提供强一致性、分布式锁、事务消息、发布订阅或跨节点失效广播。
- 不把缓存当作业务主表、队列、搜索索引或许可证状态存储。
- 不提供 PHP `serialize/unserialize` 的跨请求或跨版本契约。
- 不因为缓存失败连接禅道数据库进行隐式回源，回源由业务调用方完成。

## 4. 逻辑模型

调用方提供 `namespace` 和 `cacheKey`。服务使用以下稳定摘要作为内部键：

```text
keyHash = SHA-256(namespace + "\0" + cacheKey)
```

约束：

- `namespace` 最长 64 个字符，只允许模块定义的稳定字符集。
- `cacheKey` 最长 512 字节；不得直接拼接未经限制的用户输入。
- 单个值默认不超过 256 KiB，硬上限 1 MiB；超限直接拒绝。
- namespace 是逻辑隔离和指标标签边界，不提供扫描或批量删除语义。
- 每个值包含 `valueType`、`expiresAt`、`sizeBytes` 和内部版本号；这些字段不由客户端任意覆盖。

支持的值类型：

| 类型 | 内容 |
|---|---|
| `string` | UTF-8 字符串或受控二进制内容 |
| `json` | 标量、数组和对象的 JSON 表示 |

禁止任意 PHP 对象、资源、闭包和不可信 `unserialize()` 数据。

## 5. 组件结构

```text
PHP Cache Client / Runtime component
              |
      Cache Bridge v1 (loopback IPC)
              |
      Runtime Cache Service
       |       |        |
   validator  shards   metrics
       |       |        |
  size/TTL   LRU+TTL  observability
```

### 5.1 Cache Client

客户端负责生成 namespace/key、编码和解码允许的值、请求超时、结果类型和本请求 L0。客户端不得暴露任意命令或绕过 Bridge。

PHP 接口建议为：

```php
interface RuntimeCache
{
    public function get(string $namespace, string $key): CacheResult;
    public function set(string $namespace, string $key, mixed $value, int $ttl): bool;
    public function delete(string $namespace, string $key): bool;
}
```

`CacheResult` 必须区分 miss 和命中空值。请求内 L0 只保存短生命周期的命中和 miss，不跨 PHP 请求，也不能替代 Runtime 的全局容量控制。

### 5.2 Cache Bridge

Bridge 是本机私有、版本化的 JSON 接口；推荐与 Queue Bridge 使用同一 loopback Listener 但使用独立路径、权限和认证范围。首版端点：

| 路径 | 作用 |
|---|---|
| `/internal/runtime/cache/v1/get` | 查询一个或一批键 |
| `/internal/runtime/cache/v1/set` | 写入一个或一批键 |
| `/internal/runtime/cache/v1/delete` | 删除指定键 |
| `/internal/runtime/cache/v1/capabilities` | 协商大小、类型和后端能力 |

请求和响应必须包含 `schema: 1`，拒绝未知字段、超大 Body、未知值类型和越过 namespace 配额的请求。认证凭据随机生成且永不写日志。

### 5.3 Cache Service

Cache Service 在 Runtime Host 中作为独立模块启动和停止，使用固定数量的分片减少锁竞争。每个分片维护哈希表、过期索引和近似 LRU 队列；淘汰和过期清理使用有界批次，不能阻塞 Web、Queue Worker 或 Control Plane。

首版不把值写入 DuckDB。DuckDB 只保存缓存命中率、延迟、拒绝、淘汰和错误等聚合观测数据，绝不保存完整缓存值。

## 6. 读取、写入和失效语义

### 6.1 `get`

1. 客户端先检查请求内 L0。
2. L0 未命中后通过 Bridge 查询 Runtime Cache Service。
3. 服务检查 namespace、摘要、过期时间和值类型。
4. 不存在、过期、解码失败或容量保护拒绝均返回 miss；读取路径不执行回源。
5. 命中空字符串、空数组、`false`、`0` 和 `null` 必须与 miss 区分。

### 6.2 `set`

- `ttl <= 0` 等同于 delete，不写入已过期值。
- TTL 可加入小幅随机抖动，避免热点同时失效。
- 编码、大小和配额检查在进入分片锁之前完成。
- 写入成功后更新本请求 L0；失败只返回错误并保留调用方已计算的业务结果。
- 不使用滑动过期，不因命中而写回或延长 TTL。

### 6.3 `delete`

删除是幂等的，先清本请求 L0，再删除 Runtime Cache 中的指定摘要。业务数据提交后由业务代码显式删除相关键；删除失败依靠 TTL 和后续回源兜底。禁止按前缀扫描删除，namespace 版本变更用于模块级自然失效。

## 7. 容量、隔离和资源保护

配置至少包含：总内存上限、单值上限、namespace 配额、分片数、默认 TTL、最大 TTL、Bridge 并发、单请求 Body 上限和淘汰批次。

建议初始策略：

- 总缓存预算默认不超过 Runtime 可用内存的 10%，并设置绝对上限。
- 每个 namespace 设置可配置软配额；超过配额只影响该 namespace。
- Bridge 请求有短超时和有界并发；缓存慢时快速返回 miss。
- 淘汰优先删除已过期项，再按 LRU 删除；淘汰失败不阻断业务。
- 缓存服务使用独立 goroutine 和内存预算，不能与 PHP 线程池共享未受控对象。

## 8. 多节点和服务生命周期

每个节点拥有自己的 Cache Service，节点间不复制值、不建立一致性协议、不要求 Sticky Session。负载均衡切换后允许短暂 miss 和回源。

Runtime 启动时创建空缓存；reload 默认保留缓存，后端或容量配置变化时可以清空并在状态中记录原因；stop/upgrade 在停止接收新请求后停止 Bridge，再释放缓存。缓存清空不需要备份或恢复。

## 9. 错误降级

| 错误 | 行为 |
|---|---|
| Bridge 未鉴权/协议错误 | 拒绝请求，记录限速指标 |
| Cache Service 不可用 | 客户端按 miss 处理 |
| 请求超时 | 立即返回 miss/error，不重试多次 |
| 值过大或 namespace 超配额 | 拒绝 set，保留业务结果 |
| 解码失败 | 按 miss 处理并删除坏条目 |
| 内存耗尽 | 进入保护模式，优先拒绝新写入，Web/Queue 继续运行 |
| Runtime 重启 | 缓存为空，业务回源 |

缓存错误不能改变业务事务、队列状态、权限判断或升级结果。

## 10. 可观测性

只记录聚合指标和有限标签：`cache_get_total`、`cache_set_total`、`cache_delete_total`、命中/未命中率、Bridge 延迟、淘汰数量、过期数量、拒绝原因、当前条目数、当前字节数和 namespace 配额使用率。不得记录完整 key、缓存值、Cookie、Token 或用户输入；namespace 必须来自注册表。

## 11. 安全

- Bridge 仅绑定 loopback 或本机 IPC，公共 Caddy 路由显式拒绝。
- 每次 Runtime 启动生成随机认证凭据，权限文件为最小可读范围。
- Cache Client 只允许注册的 namespace 和值类型。
- 不接受任意 SQL、路径、文件句柄、PHP 类名、Shell 命令或可执行代码。
- 诊断和日志默认脱敏，管理清空操作需要本地权限、审计和二次确认。

## 12. 测试和上线顺序

1. Go 单元测试：键摘要、编码、TTL、分片、LRU、配额、并发和错误降级。
2. Bridge 契约测试：PHP/Go 共享夹具、鉴权、大小限制、未知字段和版本协商。
3. Classic mode 联合测试：PHP Cache Client 在连续请求和多线程下不泄漏请求状态。
4. 故障测试：服务重启、Bridge 超时、内存压力、坏值、节点切换和滚动升级。
5. 性能测试：命中延迟、miss 回源开销、并发上限、淘汰抖动和 PHP 线程预算。
6. 业务灰度：只接入性能数据证明“计算成本显著高于缓存访问”的热点，不缓存权限、锁、许可证和关键状态。

## 13. 后续演进

可以增加本地磁盘后端或外部 Redis 适配，但必须保持 Cache Client 语义、失败按 miss、显式操作和多节点不依赖缓存一致性。后端切换通过 capability 和配置版本协商完成，不能让业务代码感知具体存储实现。
