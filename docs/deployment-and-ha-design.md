# zentao 多节点部署与高可用详细设计

## 1. 文档信息

| 项目 | 内容 |
|---|---|
| 状态 | 详细设计讨论稿 |
| 日期 | 2026-08-18 |
| 产品名称 | `zentao` |
| Runtime Host | `zentao-runtime` |
| 标准多节点范围 | 两个 Linux 应用节点 |
| Session | 遵循 PHP `php.ini`；多节点使用共享 NFS 文件或外部 Redis |
| 附件 | NFSv4.1 共享文件系统 |
| 指标/日志 | DuckDB + NFS 共享分区式 Parquet 数据集 |
| 自建入口 | Keepalived 浮动 VIP + 内嵌 Caddy Gateway |
| 优先入口 | 客户已有云、硬件或托管负载均衡 |
| 关联设计 | [Runtime Host 详细设计](./runtime-host-library-design.md)、[数据库显式缓存设计](./database-cache-design.md)、[DuckDB/Parquet 可观测性设计](./duckdb-parquet-observability-design.md) |

## 2. 核心结论

zentao 提供单机和多节点两种部署模式。标准多节点参考拓扑为两个 Linux 应用节点，共享同一个数据库和附件 NFS；Session 由部署方通过 PHP `php.ini` 选择共享 NFS 文件或外部 Redis。Runtime 不实现 Session 服务，也不建设集中式配置中心。

正式决策如下：

1. Runtime 不接管 Session 数据；PHP 的 `session.save_handler` 和 `session.save_path` 是 Session 后端的配置来源。
2. 单机默认使用本地文件 Session。多节点必须由部署方选择共享 NFS 文件 Session 或外部 Redis Session；本地文件 Session 不属于标准多节点高可用方案。
3. Runtime 只负责配置交叉检查和 PHP 侧读写诊断，不保存 Session，不持有 Redis 凭据，也不管理 NFS/Redis 高可用。
4. 多节点附件目录使用客户提供的 NFSv4.1 共享文件系统，所有节点挂载到相同逻辑路径。
5. Runtime 配置由各节点分别管理，不建设配置中心；集群关键配置必须一致，并通过配置指纹检测漂移。
6. 客户已有云、硬件或托管负载均衡时优先直接接入。
7. 只有两个 Linux 节点且没有外部负载均衡时，使用 Keepalived 管理浮动 VIP，使用 `zentao-runtime` 已嵌入的 Caddy 承担 L7 Gateway，不再引入 HAProxy。
8. 使用共享 NFS 或 Redis Session 时，Caddy Gateway 使用 `least_conn` 在两个应用节点间分配请求，不需要 Session Sticky。
9. 非幂等请求不自动重试，避免重复创建、更新或上传。
10. 数据库、NFS 和外部 Redis 的高可用由客户基础设施负责。两个应用节点不等于数据层和共享存储也具备高可用。
11. 第一版自建双节点高可用面向 Linux；Windows 多节点需要客户现有负载均衡和共享文件服务，不纳入 Keepalived + NFS 标准拓扑。
12. DuckDB 不共享 `.duckdb` 文件；节点在 NFS 上共享 Parquet 数据集，各自发布不可变 part 文件，不需要写入选主或全局锁。

## 3. 目标与非目标

### 3.1 设计目标

- 任一应用节点故障后，用户可以通过同一访问地址继续使用禅道。
- Session 在应用节点之间共享，不依赖负载均衡会话保持。
- 两个节点读取和写入同一份附件。
- 节点配置独立维护，同时能够发现影响集群行为的配置漂移。
- 复用 Caddy 的反向代理、主动健康检查、WebSocket 和流式传输能力。
- 允许客户使用已有负载均衡替代自建 VIP。
- 应用节点可以逐台排空、升级、验证和重新加入。

### 3.2 非目标

- 不在 `zentao-runtime` 中实现 VRRP；VIP 由成熟的 Keepalived 管理。
- 不自研数据库、NFS 或 Redis 集群，不实现跨数据中心共识协议。
- 不承诺两个 Full 包内置 MySQL 自动组成 MySQL 高可用集群。
- 不把 NFS 文件复制到节点本地作为故障兜底。
- 不通过 DNS Round Robin 代替健康检查和故障切换。
- 不在首期提供 Windows Keepalived，也不强制 Windows 使用 NFS。
- 不允许负载均衡自动重试非幂等 PHP 请求。
- 不提供自定义 Session 持久化表或 Runtime Session 网络服务。

## 4. 部署模式

### 4.1 单机

```text
用户
  -> zentao-runtime
       -> Caddy
       -> FrankenPHP Classic mode
       -> PHP files Session -> 本地持久目录
       -> 附件目录 -> 本地持久目录或外部 NFS
```

单机默认使用 PHP 文件 Session。Session 和附件都可以使用本地持久目录；加入多节点前，Session 必须迁移到共享 NFS 或外部 Redis，附件必须迁移到共享 NFS。切换 Session 后端时允许现有用户重新登录，不迁移旧 Session 数据。

### 4.2 已有外部负载均衡

这是首选模式：

```text
用户
  -> 云/硬件/托管负载均衡
       -> 节点 A zentao-runtime App listener
       -> 节点 B zentao-runtime App listener

节点 A/B
  -> 共享数据库
  -> 共享附件 NFS
  -> 共享 Session NFS 或外部 Redis
  -> 共享指标/日志 Parquet NFS 数据集
```

外部负载均衡应提供：

- 稳定的 VIP 或域名。
- TLS 终止或 TLS 透传。
- HTTP 健康检查。
- 原始 Host 和标准代理头转发。
- WebSocket、流式下载和大附件上传支持。
- 后端排空和维护状态。

### 4.3 两节点自建入口

当客户没有外部负载均衡，且两个 Linux 节点位于支持浮动 IP 的网络中时：

```text
                         VIP: 192.168.1.100
                                  |
                       Keepalived Active/Standby
                                  |
             +--------------------+--------------------+
             |                                         |
       zentao 节点 A                              zentao 节点 B
       192.168.1.101                              192.168.1.102
       +-- Caddy Gateway :80/:443                 +-- Caddy Gateway :80/:443
       +-- PHP App listener :8080                 +-- PHP App listener :8080
       +-- Keepalived                             +-- Keepalived
       +-- NFS mount                              +-- NFS mount
             |                                         |
             +-- 共享数据库/NFS/Session/Parquet 数据集 --+
```

两个节点都运行相同 Gateway 配置。任一时刻由 Keepalived 决定哪个节点持有 VIP；持有 VIP 的 Caddy Gateway 可以把请求发送给本机 App listener 或对端 App listener。

## 5. 组件职责

| 组件 | 职责 |
|---|---|
| Keepalived | VRRP/单播 VRRP、VIP 持有、Gateway 健康跟踪和故障漂移 |
| Caddy Gateway | TLS、HTTP 路由、后端健康检查、负载均衡、代理头和连接排空 |
| App listener | Caddy + FrankenPHP Classic mode 执行禅道 PHP 请求 |
| PHP Session Handler | 按 `php.ini` 使用本地文件、共享 NFS 文件或外部 Redis |
| 共享数据库 | 业务数据、消息队列和显式缓存，不保存 Runtime Session |
| NFS | 多节点附件、可选文件 Session，以及指标/日志共享 Parquet 数据集 |
| 外部 Redis | 部署方选择 Redis Session 时保存 Session；不用于第一版业务缓存 |
| DuckDB Library | 每节点批量生成本节点不可变 Parquet part，并受控查询共享数据集 |
| Runtime control plane | 节点配置、状态、诊断、排空和升级控制，只在管理边界内开放 |

Caddy 负责 L7，不负责 VIP；Keepalived 负责 VIP，不解析 HTTP。两者边界不能混淆。

## 6. 网络与端口

推荐网络边界如下：

| 端口/协议 | 监听范围 | 用途 |
|---|---|---|
| `80/tcp` | VIP/公网入口 | HTTP 跳转或受控明文入口 |
| `443/tcp` | VIP/公网入口 | HTTPS Gateway |
| `8080/tcp` | 集群内网 | 节点 App listener |
| Runtime control IPC | 本机 | 高权限管理操作 |
| Runtime health | 集群管理网 | 负载均衡健康检查，只返回非敏感状态 |
| VRRP protocol 112 | 两节点网络 | Keepalived 通告；云环境通常不支持 |
| 数据库端口 | 数据库网络 | PHP DAO 访问数据库 |
| `2049/tcp` | 存储网络 | NFSv4.1 |
| Redis 端口 | Session 网络 | PHP Redis Session Handler；按客户部署配置 |

安全要求：

- App listener 不直接暴露到公网。
- 健康端点不返回凭据、绝对路径、Session ID 或详细异常栈。
- 数据库、NFS 和外部 Redis 只能由应用节点所在网络访问。
- 公网请求不能访问 Caddy Admin API 或 Runtime control plane。
- 公有云不能假设支持 VRRP 或 Gratuitous ARP；应使用云负载均衡。

## 7. Caddy Gateway 设计

### 7.1 双 listener

每个节点生成两个逻辑 Caddy Server：

```text
Gateway server :80/:443
  -> security headers
  -> access log
  -> reverse_proxy
       192.168.1.101:8080
       192.168.1.102:8080

App server :8080
  -> internal readiness route
  -> static files
  -> FrankenPHP php_server
```

正式实现使用 Caddy JSON 结构生成器，不通过字符串拼接生成配置。Gateway 和 App server 可以运行在同一个 `zentao-runtime`/Caddy 实例中，但必须使用不同 listener，避免反向代理形成环路。

### 7.2 负载策略

- 默认策略为 `least_conn`。
- 使用共享 NFS 或 Redis Session 时，不启用 Cookie、源 IP 或其他 Sticky 策略。
- 多节点仍使用本地文件 Session 时必须显式标记为降级拓扑并配置 Sticky；节点故障会使该节点上的用户重新登录，因此不能通过标准多节点验收。
- 后端恢复后先通过连续健康检查，再重新加入流量。
- Gateway 配置中的后端列表顺序不承担主备语义。
- 节点进入维护状态后停止接收新请求，并等待在途请求结束。

### 7.3 重试边界

自动重试仅允许：

- 尚未向后端成功发送请求体的连接建立失败。
- GET、HEAD、OPTIONS 等明确幂等方法。
- 调用方显式声明幂等且具备业务幂等键的内部请求。

禁止自动重试：

- POST、PATCH、DELETE 等业务变更请求。
- 文件上传。
- 已经向后端发送部分请求体的请求。
- 后端已经开始返回响应的请求。

负载均衡不能通过重试掩盖重复副作用风险。

### 7.4 代理头

Gateway 需要保留或设置：

```text
Host
X-Forwarded-For
X-Forwarded-Proto
X-Forwarded-Host
X-Request-ID
```

App listener 只信任明确配置的 Gateway、云负载均衡或硬件负载均衡地址。来自其他来源的 `X-Forwarded-*` 必须覆盖或移除，避免伪造客户端地址和协议。

### 7.5 大附件和长连接

- WebSocket 由 Caddy 反向代理原生处理。
- 大附件上传不做完整内存缓冲。
- 文件下载保持流式传输和背压。
- Gateway、App listener、PHP 和禅道上传限制必须一致。
- 上传超时与普通页面超时分开配置。

## 8. Keepalived 设计

### 8.1 VIP 所有权

两个节点使用不同优先级。推荐启用非抢占或延迟抢占策略：高优先级节点恢复后不立即夺回 VIP，避免连接和证书状态反复切换。

Keepalived 跟踪的是本机 Gateway listener 和 Runtime Host 健康，而不仅是本机 PHP App listener。即使本机 PHP 暂时异常，只要 Gateway 仍能把流量代理到对端，VIP 可以继续保留。

### 8.2 网络要求

- 两节点必须能够交换 VRRP 通告。
- 网络设备必须允许 VIP 漂移和 Gratuitous ARP/邻居通告。
- 不支持组播时使用明确对端地址的单播 VRRP。
- 云环境使用云负载均衡，不尝试绕过云网络限制。

### 8.3 Split Brain

网络分区可能让两个节点短暂认为自己持有 VIP。该场景不会产生两个业务数据库主节点，但可能使流量随机到达任一 Gateway。控制措施包括：

- 单播 VRRP 明确对端。
- 两节点使用相同 TLS 证书和 Gateway 配置。
- Session、业务数据和附件均使用各自的共享后端。
- 监控重复 VIP、VRRP 状态和邻居表变化。
- 数据中心支持时使用交换机、BFD 或基础设施提供的防脑裂能力。

## 9. 健康检查与流量摘除

### 9.1 探针分层

| 探针 | 使用者 | 检查内容 |
|---|---|---|
| Gateway liveness | Keepalived | Caddy Gateway listener 和 Runtime event loop |
| Backend readiness | Caddy/外部 LB | Caddy App listener、FrankenPHP、PHP 最小请求、版本与配置代数 |
| Deep health | 管理和诊断 | 数据库、按实际 Handler 执行的 PHP Session 读写、NFS、Scheduler 和磁盘 |

推荐基线：每 5 秒检查一次，连续 3 次失败后摘除，连续 2 次成功后恢复。实际值必须可配置并经过故障测试。

公共高频 readiness 不创建真实用户 Session。共享后端读写属于有频率限制的启动检查或 Deep health，并使用专用探针键。

### 9.2 共享后端故障

数据库故障会影响所有应用节点。Gateway 应返回明确的 `503` 或维护响应，不能通过反复切换节点制造请求风暴。

NFS 故障时：

- 禁止把附件或文件 Session 写入未挂载的本地空目录。
- 附件上传、下载和预览返回明确错误。
- 使用 NFS Session 的节点进入 Not Ready，不能静默切换本地 Session。
- 与附件和 Session 无关的功能可以保持服务，但健康状态标记为 `degraded`。

Redis Session 故障时，使用该 Handler 的节点进入 Not Ready。Runtime 不自动切换到文件后端；恢复后旧 Session 是否存在取决于客户 Redis 的高可用与持久化配置。

## 10. PHP Session 配置设计

### 10.1 配置所有权与支持模式

Runtime 不提供 Session 数据服务。PHP `php.ini` 是 Handler 和连接参数的配置来源，Runtime 配置仅声明部署模式和期望的 Session 类型，用于交叉检查，不复制 Secret。

| 部署模式 | `session.save_handler` | 存储 | 标准支持级别 |
|---|---|---|---|
| 单机 | `files` | 本地持久目录 | 默认 |
| 多节点 | `files` | 两节点挂载的同一 NFS 目录 | 支持 |
| 多节点 | `redis` 或经验证的 `rediscluster` | 客户外部 Redis | 支持 |
| 多节点 | `files` | 各节点本地目录 | 仅 Sticky 降级，不属于 HA |

所有模式统一启用 `session.use_strict_mode=1` 和 `session.lazy_write=1`。切换后端不迁移已有 Session，用户重新登录是可接受的升级行为。

### 10.2 禅道现有代码适配

当前 `framework/base/router.class.php::startSession()` 存在两个与配置所有权冲突的行为：

- `session.save_handler=files` 时会用 `ZT_SESSION_PATH` 或 `tmp/session` 覆盖 `php.ini` 中的 `session.save_path`，并注册 `ztSessionHandler`。
- 部分 API 请求由 `$useToken` 分支强制使用本地 `tmp/apisession`，即使 `php.ini` 已选择 Redis。

实现阶段必须完成以下适配：

1. 普通 Session 使用 `php.ini` 选定的 Handler；文件模式尊重显式 `session.save_path`，仅在未设置时回退到 `ZT_SESSION_PATH` 或 `tmp/session`。若继续保留环境变量覆盖，优先级必须固定、可诊断且在所有节点一致。
2. `ztSessionHandler` 当前只对单次写操作使用 `LOCK_EX`，没有覆盖完整读写周期。NFS 文件模式必须改用 PHP 原生文件 Handler，或为自定义 Handler 实现并验证等价的跨节点文件锁；不能按现状直接宣称支持并发 Session。
3. 浏览器 Session、`ss_` Session Token 和确实需要 Session 的 API 流程必须遵循多节点共享后端。API 若需要独立存储空间，NFS 使用独立共享子目录，Redis 使用独立 key prefix，不能回退到节点本地 `apisession`。
4. 安装检查从“本地目录可写”扩展为按实际 Handler 检查；帮助文案不能只提示 `files`。

### 10.3 NFS 文件 Session

- 两节点使用同一个规范化 `session.save_path`，挂载 NFSv4.1 或更高版本。
- Session 目录与附件目录分开，使用相同的服务 UID/GID 和最小权限；禁止 Web 直接访问。
- 文件 Handler 必须在整个 Session 读、改、写周期持有互斥锁，并完成双节点并发请求测试。NFS 服务端和挂载参数必须支持可靠文件锁。
- GC 只能删除超过 `session.gc_maxlifetime` 的 Session 文件，并验证两个节点不会并发执行无界扫描；需要时由单个计划任务分批清理。
- Runtime 在多节点 readiness 中检查路径可写、挂载身份和共享存储声明。挂载丢失时不得写入同名本地空目录，也不得自动回退本地 Session。

### 10.4 Redis Session

- 交付包必须包含与 PHP 8.4 ZTS 和目标平台匹配、经过兼容测试的 PHP Redis 扩展。
- Redis 地址、认证、TLS、数据库或 key prefix 通过受保护的 PHP 配置提供，不进入 Caddy JSON、健康响应或日志。
- 必须启用并压测 phpredis Session Locking，明确锁等待、重试和锁过期参数，避免同一用户的并发请求互相覆盖。
- 外部 Redis 的主从、Sentinel、Cluster、托管服务、备份和故障切换由部署方负责。只有 PHP Redis Handler 已验证支持的连接形式才能列入兼容矩阵。
- Runtime 不直接连接 Redis；readiness/deep health 通过受限的内部 PHP 探针验证 Handler 加载、连接和临时 Session 读写，探针数据立即清理。
- Redis 不可用时不得静默创建本地 Session。需要 Session 的请求安全失败或要求重新登录；恢复后是否保留旧 Session 取决于客户 Redis 高可用和持久化配置。

### 10.5 Readiness 判定

| 配置 | Readiness |
|---|---|
| 单机 `files`，本地路径可写 | Ready |
| 多节点 `files`，共享 NFS 身份、路径、权限和锁检查通过 | Ready |
| 多节点 Redis，PHP 扩展、连接和读写检查通过 | Ready |
| 多节点 `files`，节点本地目录 | 标准 HA 模式拒绝 Ready；仅显式 Sticky 降级模式允许启动 |
| 配置的 Handler 不可加载或共享后端不可访问 | Not Ready，不自动切换后端 |

## 11. NFS 共享存储设计

### 11.1 基线

- 推荐 NFSv4.1 或更高版本。
- 所有节点挂载到相同逻辑路径，例如 `/var/lib/zentao/data`。
- Runtime 服务账号在所有节点使用一致 UID/GID。
- NFS 导出启用合理的 `root_squash` 和网络访问控制。
- 使用 `hard` 挂载，避免网络抖动时应用误认为短写成功；具体 `timeo` 和 `retrans` 由 NFS 厂商建议确定。
- 通过 `_netdev` 或等价机制保证网络和挂载完成后再启动 zentao。

### 11.2 写入语义

```text
接收上传
  -> 在目标 NFS 的临时目录创建文件
  -> 写入并校验大小/摘要
  -> 原子 rename 到最终位置
  -> 提交或更新数据库附件元数据
```

临时文件和最终文件必须位于同一 NFS 文件系统，才能依赖原子 rename。NFS 未挂载时，Runtime 必须识别挂载点身份，不能在同名本地目录继续写入。

### 11.3 备份和恢复

- 数据库附件元数据和 NFS 文件需要同一恢复点。
- 优先使用 NFS/NAS 快照，再记录对应数据库备份点。
- 恢复演练需要检查缺失文件、孤立文件、大小和摘要。
- NFS 高可用、快照、容量和备份是客户基础设施责任，但 zentao 需要提供检查和诊断工具。

### 11.4 平台边界

Linux/Docker 多节点正式支持 NFS。Windows 的 NFS Client、服务账号映射和文件锁差异较大，第一版 Windows 只承诺单节点本地附件；Windows 多节点需要客户已有负载均衡和经过验证的 SMB/企业共享存储方案。

### 11.5 DuckDB/Parquet 数据集

- 不在 NFS 上创建可写 `.duckdb` 文件。
- 指标和日志使用不同根目录，按 `schema/date/hour/node` 分区。
- 每个节点只写、重试和清理自己的 `node=<nodeID>` 目录，任一节点可以读取所有已发布分区。
- 批次先写 `.<batchID>.parquet.tmp`，完成 DuckDB 关闭和校验后在同一 NFS 文件系统内原子 rename 为 `part-<batchID>.parquet`。
- 查询只匹配最终 `.parquet` 文件，不读取临时文件。
- 附件与 Parquet 可以使用同一 NFS 的独立目录，也可以使用独立 Export，但必须设置独立容量配额和健康状态。
- NFS 故障时批次进入节点本地有界 spool；可观测性降级不阻止 Web Ready，也不能回退到同名本地伪共享目录。

详细协议参见 [DuckDB 与共享 Parquet 可观测性详细设计](./duckdb-parquet-observability-design.md)。

## 12. 分节点配置与一致性

### 12.1 允许不同的配置

- 节点 ID 和节点地址。
- 监听地址和管理端口。
- PHP 线程数、队列并发和本机资源限制。
- 日志、临时目录和本地诊断目录。
- 可观测性本地 spool 路径、容量和查询资源上限。
- Keepalived 优先级和本机网卡。

### 12.2 必须一致的配置

- Cluster ID。
- 禅道应用、插件、Runtime、PHP 和 ionCube 版本。
- 数据库地址、数据库名、Schema 和表前缀。
- Session Handler 类型、Cookie 名称、Domain、Secure 和 SameSite。
- 文件模式的规范化共享路径和挂载标识，或 Redis 模式的非敏感端点/配置版本摘要。
- 对外访问地址和可信代理。
- NFS 附件根目录和附件存储代数。
- Parquet 数据集根目录、挂载身份、Schema、分区规则、脱敏和保留策略。
- 队列和缓存 Schema 版本。
- 影响身份认证、权限和加密的安全配置。

业务配置优先保存在禅道数据库中，由所有节点共享。节点本地文件只保存 Runtime 和平台配置。

节点 ID 虽然允许不同，但在同一 Cluster 内必须唯一。Parquet 节点目录通过不可变 owner marker 绑定节点安装身份；发现两个在线节点使用相同 nodeID 时，冲突节点停止 Parquet 发布并进入可观测性 degraded，不能覆盖 marker 或继续清理该目录。

### 12.3 配置指纹

Runtime 对必须一致的配置进行规范化并计算 `clusterConfigDigest`：

- 不把 Secret 明文放入摘要输入或健康响应；使用 Secret ID/版本或单向摘要。
- Readiness 和诊断输出 Cluster ID、Release ID、Schema 代数和配置指纹。
- 节点加入负载均衡前比较两个节点的指纹。
- 指纹不一致时允许管理员查看差异字段类别，但不能泄露值。
- 安全配置、应用版本或 Schema 不一致时节点不能进入 Ready。

该机制用于发现漂移，不是配置中心，也不自动覆盖节点配置。

## 13. TLS 设计

优先级如下：

1. 客户已有负载均衡时，在外部负载均衡终止 TLS，后端使用隔离网络；网络不可信时使用后端 TLS/mTLS。
2. 自建 VIP 时，客户证书和私钥以安全方式分别部署到两个 Gateway 节点，并验证证书指纹一致。
3. Caddy 自动 ACME 是可选能力，启用前必须提供可靠的共享 Caddy Storage 或受控证书同步。

第一版不允许两个节点无协调地为同一域名独立申请和续期证书，避免 ACME 频率限制、挑战冲突和故障切换后的证书差异。

## 14. 故障矩阵

| 故障 | 预期行为 | 高可用边界 |
|---|---|---|
| 一个 PHP App listener 异常 | Gateway 摘除该后端，使用另一节点 | 应用层可用 |
| 当前 VIP 节点宕机 | Keepalived 将 VIP 漂移到另一节点 | 依赖网络支持 VIP |
| 一个 Caddy Gateway 异常 | Keepalived 停止该节点持有 VIP | 另一 Gateway 接管 |
| 一个应用节点维护升级 | 排空并摘除，另一节点继续服务 | 容量暂时减半 |
| NFS Session 目录不可用 | 使用文件 Session 的节点 Not Ready，不回退本地目录 | NFS HA 由部署方负责 |
| Redis Session 不可用 | 使用 Redis Session 的节点 Not Ready，用户可能需要重新登录 | Redis HA 由部署方负责 |
| 数据库整体故障 | 所有动态业务不可用，Gateway 返回 503 | 数据库 HA 不由应用节点解决 |
| 附件 NFS 整体故障 | 附件功能不可用，禁止本地分叉写入 | NFS HA 不由应用节点解决 |
| NFS 容量耗尽 | 拒绝新附件并告警 | 普通非附件业务可继续 |
| Parquet NFS 不可用 | 指标/日志写本地有界 spool，状态 degraded | 已发布历史保留，业务继续 |
| 一个节点发布中宕机 | 临时文件不被查询，恢复后按 Batch ID 补发 | 不影响另一节点发布 |
| 两节点配置相同 nodeID | 冲突节点停止 Parquet 发布并告警 | 不覆盖目录 owner marker |
| 节点配置不一致 | 不一致节点不进入 Ready | 管理员修复配置 |
| VRRP 网络分区 | 可能短暂双 VIP，持续告警 | 共享后端降低业务分叉风险 |
| 外部 LB 故障 | 由客户 LB 集群能力处理 | 外部基础设施责任 |

## 15. 滚动升级

```text
验证备份和共享后端
  -> 将节点 B 标记 draining
  -> Gateway 摘除节点 B
  -> 等待在途请求和队列任务排空
  -> 升级并验证节点 B
  -> 比较 Release/Schema/Config 指纹
  -> 节点 B 重新加入
  -> 对节点 A 重复流程
```

只有新旧应用版本和数据库 Schema 明确兼容时才允许滚动升级。存在不兼容数据库迁移时必须进入维护窗口，停止两个节点后执行升级，不能让不同 Schema 期望的节点同时服务。

Keepalived 建议使用非抢占策略，升级完成后不强制让 VIP 回到原高优先级节点。

## 16. 可观测性

至少记录：

- 当前 VIP owner、Keepalived 状态变化和漂移次数。
- Gateway 后端健康、摘除原因和恢复时间。
- 每个后端连接数、请求量、延迟和状态码。
- 节点 Release ID、Cluster ID、配置指纹和 Schema 代数。
- 按 Handler 分类的 Session 读写失败、锁等待、过期和 GC；不记录 Session ID。
- NFS 挂载身份、延迟、错误、剩余容量和只读状态。
- 数据库、NFS 或 Redis 故障导致的降级状态。
- Parquet 批次发布、spool 容量、文件数量、NFS 可用性和查询资源限制。

访问日志需要包含请求 ID 和后端节点 ID，但不能记录 Session ID、Cookie 和授权信息。

## 17. 安全要求

- VIP/Gateway、App、管理、数据库和存储网络尽量分区。
- 负载均衡只信任明确后端，App 只信任明确代理。
- Keepalived 脚本使用固定路径和参数，不执行可配置任意 Shell。
- TLS 私钥、Redis Session 凭据和数据库凭据使用最小文件权限。
- NFS 导出限制客户端地址，禁止匿名广泛写入。
- Session Payload、Cookie 和 Session ID 不写日志。
- DuckDB 不接受公共任意 SQL、任意文件路径或运行时 Extension 安装。
- Runtime 健康端点仅暴露非敏感状态，Deep health 需要管理权限。

## 18. 测试计划

### 18.1 流量与故障测试

- 两节点正常 `least_conn` 分流。
- 当前 VIP 节点断电、进程崩溃和网卡故障。
- 单个 App listener 卡死、超时和恢复。
- 非幂等 POST 在后端断连时不重复执行。
- WebSocket、大附件上传和流式下载。
- 节点 drain 后无新请求进入。
- VRRP 单播、VIP 漂移和 Gratuitous ARP。

### 18.2 Session 测试

- 用户在两个后端之间切换后保持登录。
- NFS 文件 Session 的双节点并发请求不会覆盖更新，进程退出后不会遗留永久锁。
- Redis Session Locking 在并发、超时和请求崩溃场景下行为符合配置。
- 普通浏览器、`ss_` Token 和需要 Session 的 API 请求都使用共享后端。
- 多节点本地文件 Session 在标准 HA 模式下不能 Ready。
- NFS 挂载丢失或 Redis 不可用时不回退到本地目录。
- Handler、共享路径/Redis 配置版本或 Cookie 配置不一致的节点不能 Ready。
- 从文件切换到 Redis、从本地文件切换到 NFS 时，允许旧用户重新登录且不迁移旧 Session。

### 18.3 NFS 测试

- 两节点上传、读取、删除和重命名同一附件。
- NFS 短暂中断、只读、容量耗尽和 stale handle。
- 未挂载时不会写入本地空目录。
- 数据库与 NFS 快照配对恢复。
- 服务账号 UID/GID 和权限一致。
- 两节点同时向不同 node 分区发布 Parquet，不发生覆盖或锁竞争。
- 重复 nodeID、错误 Cluster ID 和目录 owner marker 不匹配时拒绝发布。
- 临时 Parquet 不可见、原子 rename、Batch ID 幂等和崩溃补发。
- Parquet NFS 中断期间本地 spool 有界增长，恢复后无重复补发。

### 18.4 配置与升级测试

- Cluster ID、版本、Schema 和安全配置漂移检测。
- 兼容版本滚动升级和节点排空。
- 不兼容 Schema 升级自动拒绝滚动模式。
- Keepalived 非抢占和升级后的稳定性。

## 19. 验收标准

- 两个 Linux 节点能够通过一个 VIP 或外部 LB 地址提供服务。
- 使用健康的共享 NFS 或 Redis Session 时，任一应用节点故障后现有用户继续访问且不依赖 Sticky。
- 两节点能够安全共享附件，不出现本地分叉写入。
- 两节点无需选主或全局锁即可共享查询 Parquet，并只能写自己的不可变 part 文件。
- POST 和附件上传在故障时不会被负载均衡自动重复提交。
- 配置、应用版本和 Schema 漂移能够在节点接流量前发现。
- 节点可以排空、升级、验证和重新加入。
- 数据库、NFS 或 Redis 整体故障时能够明确降级，不把责任错误归因于应用负载均衡。
- 云负载均衡和自建 Keepalived + Caddy Gateway 使用同一 App readiness 与代理头契约。
