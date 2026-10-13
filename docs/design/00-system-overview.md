# zentao Runtime 系统总设计

## 1. 设计目标

`zentao-runtime` 为禅道提供 Windows、Linux 和 Docker 上一致的安装、运行、升级、诊断和构建能力。首要交付是可独立运行的单机环境，同时保留企业部署所需的双节点、高可用和外部负载均衡组合能力。

目标按优先级分为三层：

| 层级 | 目标 |
|---|---|
| P0 运行 | 安装、登录、PHP 请求、上传下载、升级回滚和故障恢复可重复运行 |
| P0 边界 | Classic mode 请求隔离、业务数据库不被 Go 访问、管理面不暴露公网 |
| P1 运维 | 队列并发与恢复、缓存降级、健康检查、诊断包、结构化日志和指标 |
| P1 部署 | Windows/Linux/Docker 单机，以及 Linux 双节点或外部负载均衡 |
| P2 演进 | 更丰富的缓存后端、可选观测子进程和更大规模集群，不改变首版契约 |

## 2. 非目标

- 不实现禅道业务逻辑、业务 DAO 或数据库方言。
- 不让 Go Runtime 连接禅道业务数据库、保存其凭据或打包其 Driver。
- 不把 Runtime 变成通用 Broker、数据库集群或配置中心。
- 不使用 FrankenPHP Worker mode，不依赖跨请求的禅道 PHP 应用状态。
- 不把双应用节点描述成数据库、NFS 或 Redis 的高可用；这些属于部署方基础设施。

## 3. 总体架构

```text
用户/负载均衡
        |
  Caddy Gateway (zentao-runtime)
        |
  FrankenPHP + PHP 8.4 ZTS Classic mode
        |
  禅道 PHP 应用
    |             |                 |
  PHP DAO       Queue Bridge       APCu / Redis 缓存
    |             |                 |
业务数据库   Runtime Queue Worker   lib/cache 键值缓存
        |                 |          |
        +---------+-------+----------+
                  |
       Runtime Host / Control Plane
       lifecycle / supervisor / upgrade / health
                  |
       DuckDB Library -> Parquet / JSONL / spool
```

Runtime 可以管理自身内嵌库（Caddy、FrankenPHP、DuckDB）、自身服务（Web、Scheduler、Worker）和受控的外部独立二进制服务（例如 Full 包 MySQL）。管理关系由 Supervisor 抽象统一，不把外部服务的业务协议内嵌到 Host。

## 4. 组件职责与事实来源

| 组件 | 负责 | 不负责 | 事实来源 |
|---|---|---|---|
| Runtime Host | 配置、生命周期、服务管理、控制面、升级和诊断 | 禅道业务规则和 DAO | Runtime 状态 |
| 应用装载 | 按 `[[apps]]` 声明装载多个可拔插应用、子目录路由、注入每应用数据/Session/环境 | 应用代码的合并、发布和合并结果校验 | `config/runtime.toml` |
| Caddy | TLS、HTTP、路由、代理、静态文件和连接排空 | VIP/VRRP、业务重试 | 连接状态 |
| FrankenPHP | PHP SAPI、Classic 请求执行和扩展加载 | 禅道请求之间的状态共享 | PHP 请求 |
| PHP DAO/Queue Service | 业务数据库、队列领取、ACK、重试和租约持久化 | Worker 并发和进程生命周期 | 业务数据库 |
| Queue Worker | Claim 编排、超时、心跳、排空和重试调用 | 数据库 SQL 和业务副作用 | Bridge 响应 |
| PHP 缓存（APCu/Redis） | 显式缓存读写、TTL、容量和降级 | 强一致业务状态、业务数据库查询 | APCu 共享内存 / Redis |
| DuckDB/Parquet | 日志、指标批处理和受控查询 | 事务、权限和业务状态 | 业务数据库仍为业务事实来源 |
| Supervisor | Runtime 自身服务和受控外部进程启停 | 外部基础设施 HA | 子进程状态 |

## 5. 关键一致性语义

- PHP 业务数据和队列状态使用数据库事务和 DAO 保证正确性。
- 队列是 At-least-once；Handler 必须使用幂等键、唯一约束或状态检查控制重复副作用。
- PHP 缓存（APCu/Redis）是可丢弃数据；读取失败按 miss，写入失败不能回滚业务结果。
- Parquet 是分析副本；发布失败只造成可观测性降级，不影响业务事务。
- 非幂等 HTTP 请求和升级步骤不自动重试。

## 6. 请求与任务数据流

### 6.1 Web 请求

1. Caddy 接收连接并执行 TLS、路由、大小和超时限制。
2. FrankenPHP 为每个请求建立新的 PHP request context。
3. PHP 通过现有 DAO 访问业务数据库；需要缓存时经 lib/cache 读写 APCu/Redis。
4. 请求日志、错误和受控度量进入 Runtime 本地缓冲区。
5. 请求完成后释放 PHP request context，不把禅道对象或用户状态留给下一请求。

### 6.2 队列任务

1. PHP 生产者在业务事务中写入任务。
2. Runtime Worker 通过本机 Queue Bridge 批量 Claim。
3. PHP DAO 返回带租约和 fencing token 的任务引用。
4. Worker 调用受注册表约束的 Handler，按周期发送 Heartbeat。
5. 成功、重试、失败、取消和过期结果通过 token 条件更新回数据库。
6. Worker 崩溃后由 Reaper 回收租约，旧 token 不能覆盖新执行结果。

### 6.3 缓存

1. PHP 或受控 Runtime 组件生成稳定的 namespace/key。
2. lib/cache 直接读写 APCu（PHP 进程间共享内存，runtime 内置扩展）或外部 Redis；命中返回带类型的值。
3. 未命中由调用方回源计算，再显式写入 TTL。
4. 后端故障、容量耗尽或超时立即降级，不访问禅道数据库代替缓存。

## 7. 部署形态

### 7.1 单机首要入口

单机由一个 `zentao-runtime` 管理 Web、可选 Scheduler、Worker、Full 包 MySQL 和本地观测目录。Session 默认使用 PHP 文件 Handler，缓存使用 runtime 内置的 APCu 共享内存，附件使用本地持久目录。

### 7.2 Docker

Web Runtime 和 Scheduler 可分为容器，MySQL 使用独立容器。应用、配置、数据、附件、缓存和观测目录分别挂载，禁止把临时 spool 当作业务数据卷。

### 7.3 企业双节点

两个 Linux 应用节点共享业务数据库和附件 NFS，Session 由 NFS 文件或外部 Redis 提供。每个节点使用自己的本地 APCu 缓存（跨节点共享改配 Redis），缓存不作为跨节点一致性机制；节点通过外部负载均衡或 Keepalived + Caddy Gateway 接入。缓存丢失只会造成回源，不影响业务正确性。

## 8. 管理与安全边界

- Control Plane 只使用 Unix Socket 或 Windows Named Pipe，并进行本机身份鉴权和审计。
- Queue Bridge 只绑定 loopback，使用随机启动凭据和版本化 JSON。
- Cache Service 只接受结构化命令，不接受任意 SQL、文件路径或 Go 插件。
- DuckDB 查询使用固定模板、资源限制和允许的数据集，不暴露任意 SQL 或 Extension 加载。
- 公共 Web 入口不能路由到 Control Plane、Queue Bridge 或 Cache 管理端点。
- 日志和诊断包在进入缓冲区前脱敏，禁止凭据、Cookie、Session、Token 和 SQL 参数。

## 9. 运行状态与故障归属

`liveness` 只表示 Host 进程存活；`readiness` 表示 Caddy、FrankenPHP 和必需组件可接收流量；`deep health` 通过本地控制面返回 Runtime、PHP、应用、队列、缓存、共享存储和可观测性组件的细分状态。单个非关键组件故障优先标记 `degraded`，不得把缓存或观测故障误报为业务数据损坏。

## 10. 质量目标

首版发布必须验证：单机安装/登录/升级/回滚；Classic mode 请求隔离；Queue Bridge 领取、租约、重试和崩溃恢复；缓存命中、过期、容量和故障降级；DuckDB/Parquet 原子发布和 spool 补发；Windows、Linux amd64/arm64、Docker 构建；Linux 双节点滚动升级和共享 Session/附件联合行为。

具体阈值和平台矩阵见[验证与发布门槛](../development/02-validation-and-release-gates.md)。

## 11. 设计决策记录

| 决策 | 结论 | 原因 |
|---|---|---|
| PHP 执行模式 | Classic mode | 降低禅道请求状态跨请求污染风险 |
| 数据库访问 | 仅 PHP DAO | 复用禅道数据库方言和信创适配 |
| 队列持久化 | 业务数据库 | 与业务事务和多数据库矩阵一致 |
| 缓存归属 | PHP lib/cache（APCu/Redis） | Runtime 内置 apcu 扩展，进程间共享内存即缓存，不复制数据库访问体系 |
| 观测存储 | DuckDB + Parquet | 无强制外部服务，支持本机和共享数据集查询 |
| 首发入口 | 单机 | 降低安装和运维门槛，同时保留 HA 组合能力 |
| 外部服务管理 | Runtime Supervisor | 统一启停、健康和升级边界 |
