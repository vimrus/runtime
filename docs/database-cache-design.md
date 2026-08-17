# 禅道数据库显式缓存详细设计

## 1. 文档信息

| 项目 | 内容 |
|---|---|
| 状态 | 详细设计讨论稿 |
| 日期 | 2026-08-17 |
| 缓存后端 | 禅道业务数据库中的独立缓存表 |
| 访问方式 | PHP 业务代码显式 `get/set/delete` |
| Go 数据库访问 | 不允许，全部由 PHP DAO 执行 |
| 支持数据库 | MySQL、PostgreSQL、达梦 DM8，以及禅道现有 DAO 适配的数据库 |
| 关联文档 | [集成环境技术方案](./frankenphp-integration-technical-plan.md)、[Runtime Host 详细设计](./runtime-host-library-design.md) |

## 2. 核心结论

第一版不引入 Redis、Sentinel、Ristretto 或 bbolt 作为缓存后端，也不建设独立的缓存主节点。缓存内容直接保存在禅道数据库的独立表中，由 PHP 代码通过统一 Cache Client 显式访问。

正式边界如下：

1. 缓存表只保存业务代码主动写入的计算结果、聚合结果或外部接口结果。
2. 不拦截 SQL，不自动缓存任意数据库查询，不修改 DAO 的普通查询语义。
3. 业务代码只能通过 `get`、`set`、`delete` 使用缓存；缓存未命中时由调用方计算并显式 `set`。
4. Go Runtime 不连接禅道数据库，不打包数据库 Driver、厂商客户端或数据库凭据。
5. 数据库缓存失败按未命中处理，不能阻断业务请求。
6. 缓存是可丢弃数据。数据库故障转移、缓存表清空或缓存内容损坏都只能影响性能，不能影响业务正确性。
7. 默认物理实现为：MySQL InnoDB、PostgreSQL UNLOGGED 普通表、达梦普通行存储表加聚集主键。
8. 不缓存权限、锁、许可证、工作流状态和其他必须强一致的数据。

该方案的目标是减少高成本计算，而不是减少数据库连接次数。一次缓存读取仍然需要一次数据库主键点查，因此不能将它当作 Redis 的等价替代品。

## 3. 适用场景和非目标

### 3.1 适用场景

- 多表聚合和统计结果。
- 复杂权限之外的页面计算结果。
- 外部服务返回结果。
- 解析、转换或渲染成本较高的结果。
- 结果生成成本显著高于一次缓存表主键查询的数据。

### 3.2 非目标

首期不提供以下能力：

- SQL 查询自动缓存或透明查询代理。
- 分布式锁、计数器、队列、发布订阅和事务消息。
- 类 Redis 的 Hash、List、Set、Sorted Set 等数据结构。
- `remember()`、自动回源和自动写回。
- 通过缓存保证业务数据的强一致性。
- 使用缓存表替代业务主表、搜索索引或消息队列。

## 4. 逻辑数据模型

### 4.1 缓存键

调用方提供两个字符串：`namespace` 和 `cacheKey`。

```text
namespace + "\0" + cacheKey
```

经过 SHA-256 计算得到 32 字节 `keyHash`。数据库主键使用 `(namespace, keyHash)`，原始 `cacheKey` 仍保存在表中，用于哈希冲突校验、诊断和运维排查。

约束建议：

- `namespace` 最长 64 个字符，只允许模块定义的稳定字符集。
- `cacheKey` 最长 512 个字节。
- 单个缓存值默认不超过 256 KB，硬上限为 1 MB。
- 不允许把用户可控的任意长字符串直接作为键或值写入缓存表。

`namespace` 是业务模块之间的隔离边界，不承担批量删除语义。首期批量清理只由维护任务执行，业务代码仍然显式删除具体键。

### 4.2 值格式

缓存 Client 只接受以下可移植值：

| `valueType` | 内容 |
|---|---|
| `string` | UTF-8 字符串或调用方指定的原始二进制内容 |
| `json` | 标量、数组和对象的 JSON 表示 |

默认使用 JSON。禁止对不可信缓存内容调用 PHP `unserialize()`，也不允许把任意 PHP 对象作为跨版本缓存契约。缓存值应当能够在同一禅道版本的 PHP 请求之间稳定解码。

### 4.3 表字段

逻辑字段如下：

| 字段 | 说明 |
|---|---|
| `namespace` | 模块命名空间 |
| `keyHash` | SHA-256 二进制摘要 |
| `cacheKey` | 原始键，做冲突校验和诊断 |
| `value` | 编码后的缓存内容 |
| `valueType` | `string` 或 `json` |
| `expiresAt` | Unix 时间戳，秒 |
| `sizeBytes` | 编码后的值大小，用于配额和监控 |
| `createdAt` | 首次写入时间，Unix 时间戳 |
| `updatedAt` | 最近写入时间，Unix 时间戳 |

不保存 `lastAccessAt`、访问计数或命中计数。读取路径不能产生额外写入。

## 5. PHP Cache Client 契约

### 5.1 接口

```php
interface CacheClient
{
    public function get(string $namespace, string $key, mixed $default = null): CacheResult;

    public function set(string $namespace, string $key, mixed $value, int $ttl): bool;

    public function delete(string $namespace, string $key): bool;
}
```

`get()` 必须能够区分“缓存未命中”和“命中了空值”，因此 `CacheResult` 至少包含 `hit` 和 `value` 两个字段：

```php
$result = $cache->get('dashboard', $key);
if(!$result->hit) {
    $value = buildDashboard($key);
    $cache->set('dashboard', $key, $value, 60);
} else {
    $value = $result->value;
}
```

### 5.2 `get`

执行一次带参数的主键等值查询：

```sql
SELECT cacheKey, value, valueType, expiresAt
  FROM zt_cache
 WHERE namespace = :namespace
   AND keyHash = :keyHash
 LIMIT 1;
```

PHP 在读取后执行以下检查：

1. 原始 `cacheKey` 与请求键一致；不一致视为未命中并记录冲突告警。
2. `expiresAt <= 当前时间` 视为未命中。
3. 按 `valueType` 解码；解码失败视为未命中并异步删除该键。

过期判断不在 `get` 中执行删除，避免一次读取变成读写事务。数据库时间和 PHP 时间存在偏差时，允许产生短暂的提前或延后过期；严格时间语义不是缓存的目标。

### 5.3 `set`

`set` 的行为：

- `ttl <= 0` 等同于 `delete`，不写入已过期记录。
- TTL 写入时加入小幅随机抖动，避免大量键同一秒失效。
- 序列化、大小检查和哈希计算在执行数据库写入之前完成。
- 写入使用预编译语句和数据库方言适配的 UPSERT。
- 缓存写入失败只记录指标和日志，不回滚已经完成的业务计算。

不使用滑动过期。`get` 不延长 TTL，否则高命中键将永不释放并增加写入压力。

### 5.4 `delete`

`delete` 使用 `(namespace, keyHash)` 主键删除，操作是幂等的。业务数据更新后应显式删除相关缓存键；推荐在业务事务提交成功后执行，删除失败时依赖 TTL 兜底。

不允许通过 `LIKE`、前缀扫描或无条件清空完成业务请求中的失效操作。管理员维护任务可以单独执行命名空间或版本级清理。

### 5.5 错误降级

| 缓存错误 | 行为 |
|---|---|
| 连接失败 | 按未命中处理，业务回源 |
| 查询超时 | 按未命中处理，记录超时指标 |
| 解码失败 | 删除坏值，按未命中处理 |
| `set` 失败 | 保留业务结果，不影响响应 |
| `delete` 失败 | 依赖 TTL，记录失效失败 |
| 数据库连接池耗尽 | 直接跳过缓存，不能继续等待 |

缓存操作应设置独立的短超时；缓存延迟不能拖慢正常业务请求。

## 6. 三种数据库的物理实现

### 6.1 MySQL：InnoDB

推荐使用 InnoDB，而不是 MEMORY。

MEMORY 在极小的固定长度、低并发只读场景可能有优势，但不适合作为通用生产缓存：

- MEMORY 表更新涉及表级锁，混合读写并发下可能退化。
- 不支持 `BLOB` 和 `TEXT`，难以容纳可变长度缓存值。
- 受 `max_heap_table_size` 限制，默认容量很小。
- MySQL 重启后表定义保留但数据清空。
- 主从复制中的 MEMORY 表需要额外处理，切换后容易出现空表和陈旧数据差异。

推荐建表示意：

```sql
CREATE TABLE zt_cache (
    namespace  VARCHAR(64)  NOT NULL,
    keyHash    BINARY(32)   NOT NULL,
    cacheKey   VARBINARY(512) NOT NULL,
    value      MEDIUMBLOB   NOT NULL,
    valueType  TINYINT UNSIGNED NOT NULL,
    expiresAt  BIGINT UNSIGNED NOT NULL,
    sizeBytes  INT UNSIGNED NOT NULL,
    createdAt  BIGINT UNSIGNED NOT NULL,
    updatedAt  BIGINT UNSIGNED NOT NULL,
    PRIMARY KEY (namespace, keyHash),
    KEY idx_cache_expires (expiresAt)
) ENGINE=InnoDB ROW_FORMAT=DYNAMIC;
```

`set` 使用 `INSERT ... ON DUPLICATE KEY UPDATE`。不使用 MyISAM，因为它没有事务和行级并发保护。

### 6.2 PostgreSQL：UNLOGGED 普通表

缓存可重建时推荐 `UNLOGGED`。其数据不写 WAL，因此写入和更新比普通 LOGGED 表更轻；异常关闭后表会被清空，备用节点也不会复制表内容。这正好符合缓存“可以丢失”的语义。

推荐建表示意：

```sql
CREATE UNLOGGED TABLE zt_cache (
    namespace  VARCHAR(64)  NOT NULL,
    "keyHash"  BYTEA       NOT NULL,
    "cacheKey" BYTEA       NOT NULL,
    value      BYTEA       NOT NULL,
    "valueType" SMALLINT   NOT NULL,
    "expiresAt" BIGINT     NOT NULL,
    "sizeBytes" INTEGER    NOT NULL,
    "createdAt" BIGINT     NOT NULL,
    "updatedAt" BIGINT     NOT NULL,
    PRIMARY KEY (namespace, "keyHash")
) WITH (fillfactor = 80);

CREATE INDEX idx_cache_expires ON zt_cache ("expiresAt");
```

`set` 使用 `INSERT ... ON CONFLICT (namespace, "keyHash") DO UPDATE`。

注意事项：

- 主备切换后缓存表可能为空，业务必须能够正常回源。
- 逻辑复制场景需要单独处理表结构迁移。
- 频繁 UPSERT/DELETE 会产生死元组，应为该表设置独立的 autovacuum 参数。
- 如果客户要求缓存内容跟随 PostgreSQL 备用节点复制，则使用 LOGGED 普通表，而不是 UNLOGGED。

PostgreSQL 默认堆表和 B-tree 主键已经适合点查；不额外建立 HASH 索引，避免重复索引维护。

### 6.3 达梦 DM8：普通行存储表

达梦没有与 PostgreSQL UNLOGGED 完全等价、且能保持完整 DAO 行为的通用表类型。默认使用普通行存储表，并将缓存键设置为聚集主键：

```sql
CREATE TABLE zt_cache (
    namespace  VARCHAR(64)   NOT NULL,
    keyHash    BINARY(32)    NOT NULL,
    cacheKey   VARBINARY(512) NOT NULL,
    value      BLOB          NOT NULL,
    valueType  SMALLINT      NOT NULL,
    expiresAt  BIGINT        NOT NULL,
    sizeBytes  INTEGER       NOT NULL,
    createdAt  BIGINT        NOT NULL,
    updatedAt  BIGINT        NOT NULL,
    CONSTRAINT pk_zt_cache PRIMARY KEY (namespace, keyHash)
);

CREATE INDEX idx_cache_expires ON zt_cache(expiresAt);
```

实际 DM 建表 SQL 由 DM DAO 生成，若需要聚集主键，应使用 DM 支持的 `CLUSTER PRIMARY KEY` 语法，而不能假设普通 `PRIMARY KEY` 自动聚集。禅道现有 DM DAO 默认不应修改为全局 `PK_WITH_CLUSTER=1`，避免影响业务表；缓存表单独指定。

达梦普通表使用 B-tree 管理，聚集主键适合按缓存键点查。它还支持堆表，堆表以物理 ROWID 存储并能通过分支提高并发插入效率；但堆表的点查需要先通过二级索引取得 ROWID，再读取数据，因此只在压测确认 `set` 明显多于 `get` 时作为备选：

```text
堆表 + keyHash 唯一二级索引 + expiresAt 清理索引
```

不使用达梦 HUGE 表。HUGE 表主要面向海量列存储和分析；频繁更新、删除会使用 RAUX、DAUX、UAUX 辅助结构，不适合小对象缓存的主键点查。

达梦 FAST Pool 和 `LOAD_TABLE` 可以把指定普通表预装入缓冲区，但需要修改 `dm.ini` 并占用固定内存。它们是部署调优项，不作为 Runtime 自动修改数据库配置的默认行为。

## 7. SQL 操作与数据库适配

### 7.1 统一操作

```text
get:    SELECT by (namespace, keyHash)
set:    database-specific UPSERT
delete: DELETE by (namespace, keyHash)
gc:     select expired primary keys, then delete exact keys in small batches
```

### 7.2 UPSERT 方言

| 数据库 | 首选写入方式 |
|---|---|
| MySQL | `INSERT ... ON DUPLICATE KEY UPDATE` |
| PostgreSQL | `INSERT ... ON CONFLICT DO UPDATE` |
| 达梦 | `MERGE INTO`；若版本能力不足，使用 `UPDATE` 后 `INSERT` 冲突重试 |

UPSERT 由 PHP DAO 实现。Go Runtime 不包含任何上述 SQL，也不负责数据库重连、事务和凭据管理。

### 7.3 删除和过期清理

请求路径不执行同步过期删除。维护任务通过 PHP DAO：

1. 按 `expiresAt` 索引读取少量过期主键。
2. 按主键分批删除。
3. 每批提交并让出数据库连接。
4. 达到时间预算或批量上限后退出，下一轮继续。

不能使用一个没有上限的大型 `DELETE WHERE expiresAt <= ...`。不同数据库对 `DELETE ... LIMIT` 的支持也不同，清理器应先取键再执行精确删除。

清理任务可以由禅道已有计划任务执行，不需要 Go 直接连接数据库。若计划任务停止，过期记录仍会在 `get` 时视为未命中，空间问题由健康检查告警。

## 8. 一致性与失效

数据库缓存不保证强一致。推荐的业务流程是：

```text
业务读取
  -> cache.get
  -> 命中：返回结果
  -> 未命中：执行原始计算
  -> cache.set

业务更新
  -> 提交业务数据
  -> 显式 cache.delete 相关键
  -> 删除失败由 TTL 兜底
```

以下数据禁止进入该缓存：

- 用户权限和角色判断结果。
- 许可证和 ionCube 授权状态。
- 并发锁和租约。
- 任务、需求、Bug 等对象的关键状态流转。
- 影响权限或计费的关键配置。

业务代码需要缓存严格一致数据时，应在读取时直接访问事实表，不能通过调整 TTL 掩盖一致性问题。

### 8.1 缓存击穿

首期不实现跨节点分布式锁，也不提供自动 `remember()`。高成本调用方应自行控制重复计算，例如：

- 使用已有业务锁或唯一约束。
- 对同一请求内重复读取做局部变量复用。
- 对外部接口调用使用调用方已有的限流。
- 通过随机 TTL 和预热任务分散过期时间。

如果压测证明击穿是主要问题，再单独增加数据库兼容的短租约锁；该锁不能复用缓存表的普通 `get/set/delete` 语义。

## 9. 性能预期与容量边界

### 9.1 与 Redis 的相对性能

在连接复用、缓存值已位于数据库缓冲区、数据库没有明显竞争时，经验范围如下：

| 操作 | 数据库显式缓存相对 Redis |
|---|---|
| 热数据 `get` | 通常慢约 2～10 倍 |
| `set` | 通常慢约 3～20 倍 |
| `delete` | 通常慢约 2～10 倍 |
| 数据库出现竞争时的 p95/p99 | 可能慢一个数量级以上 |

常见单次端到端延迟范围是：

```text
Redis：约 0.1～0.5 ms
数据库主键点查：约 0.5～3 ms
```

实际值取决于数据库版本、网络、PHP PDO 连接复用、缓存值大小、Buffer Pool 命中率和业务负载。数据库缓存的价值是避免 20～500 ms 的复杂计算，而不是替代 Redis 的高频低延迟访问。

### 9.2 保护数据库

- Cache Client 使用独立短超时，连接失败立即回源。
- 只使用主键点查，不在请求中扫描过期数据。
- 不更新访问时间，不记录逐次命中日志。
- 对缓存值和命名空间设置硬上限。
- 控制缓存表总行数和总大小，超过配额时优先删除最早过期项。
- 清理任务使用小批次和时间预算。
- 监控数据库 CPU、连接池、锁等待、WAL/redo、autovacuum 和表膨胀。
- 只有当原始计算明显贵于一次数据库点查时才允许接入缓存。

### 9.3 首版性能门槛

必须在 MySQL、PostgreSQL 和 DM8 上分别完成相同数据模型的压测：

- `get` p95 小于 5 ms。
- `set` 和 `delete` p95 小于 10 ms。
- 缓存操作不使业务数据库 CPU 长期超过 70%～80%。
- 过期清理不能产生长事务或明显锁等待。
- 数据库缓存停止或超时后，业务仍能成功回源。

若数据库缓存达不到门槛，优先在 Cache Client 内增加可选的本地 L1，而不是修改业务调用接口。

## 10. 可观测性

Cache Client 记录聚合指标，不记录完整缓存值：

- `cache_get_total{namespace,result=hit|miss|error}`。
- `cache_get_latency_seconds`。
- `cache_set_total{result=success|error}`。
- `cache_delete_total{result=success|error}`。
- `cache_decode_error_total`。
- `cache_expired_total`。
- `cache_gc_rows_total` 和 `cache_gc_duration_seconds`。
- `cache_value_bytes` 直方图。
- 缓存表行数和估算大小。

日志只包含命名空间、键摘要、错误类别和耗时，不写入原始缓存键对应的敏感内容。

## 11. 安全设计

- Cache Client 不接受任意 SQL、表名或列名。
- 所有值使用参数绑定，禁止字符串拼接。
- 原始键只用于内部校验和诊断，日志默认只记录摘要。
- 禁止缓存密码、Token、完整会话凭据和许可证密钥。
- JSON 解码失败和哈希冲突必须告警，但不能让请求失败。
- 缓存表使用禅道数据库账号的最小权限；不需要 DDL 权限的运行账号与安装升级账号分离。

## 12. 测试计划

### 12.1 数据库契约测试

每个 DAO Driver 都必须通过同一组测试：

1. 空值和非空值命中区分。
2. 字符串、JSON 数组和 JSON 对象的编码解码。
3. 相同键重复 `set` 的覆盖语义。
4. 不同命名空间相同键互不影响。
5. 过期读取返回未命中但不阻塞请求。
6. `delete` 幂等。
7. 哈希冲突校验。
8. 并发 `set` 不产生重复主键或损坏值。
9. 大小上限和非法 TTL。
10. 数据库连接失败时正确降级。

### 12.2 数据库专项测试

- MySQL InnoDB 的行锁和重复键并发 UPSERT。
- PostgreSQL UNLOGGED 崩溃恢复和备用节点提升后的空表行为。
- PostgreSQL autovacuum、表膨胀和长时间清理。
- 达梦聚集主键点查、`MERGE` 并发及 BLOB 读写。
- 达梦堆表候选方案与普通表的读写比例对照压测。
- MySQL、PostgreSQL、达梦的 Unicode 键、二进制值和最大值大小。

### 12.3 性能测试矩阵

至少覆盖：

```text
读写比例：90/10、70/30、50/50
缓存命中率：50%、80%、95%
值大小：1 KB、16 KB、256 KB、1 MB
并发：单连接、连接池、双应用节点
表规模：1 万、100 万、1000 万条
```

每组记录吞吐、p50/p95/p99、数据库 CPU、连接数、锁等待、磁盘 I/O、WAL/redo 和清理耗时。

## 13. 迁移和上线顺序

1. 在安装和升级流程中创建 `zt_cache` 及数据库专用索引。
2. 上线 Cache Client，但默认关闭业务模块接入。
3. 选择一个高计算成本、低一致性风险的模块进行灰度。
4. 观察命中率、缓存表增长、数据库负载和回源延迟。
5. 通过配置逐模块启用，不允许一次性替换所有已有缓存逻辑。
6. 发现数据库压力异常时，关闭模块缓存即可恢复原行为。
7. 保留一键清空缓存表的维护命令；清空不影响业务数据。

## 14. 后续演进

如果数据库显式缓存达到容量或延迟边界，优先沿用当前接口增加本地 L1：

```text
业务 Cache Client
  -> 可选本地 L1
  -> 数据库显式 L2
  -> 原始业务计算
```

本地 L1 可以使用 Ristretto，但不改变数据库缓存表的契约，也不把 Go Runtime 变成数据库客户端。bbolt 暂不纳入第一版；它只能帮助单节点重启后的本地恢复，不能解决多节点共享和故障转移后的冷缓存问题。

