# 禅道代码适配开发计划

## 1. 文档信息

| 项目 | 内容 |
|---|---|
| 状态 | 契约已冻结、改动点已定位；禅道代码实施待确认后执行 |
| 日期 | 2026-08-18 |
| 责任代码库 | `zentaopms`、`zentaoext`、`zentaomax`、`zentaoipd`、`zentaopatch` |
| Runtime 计划 | [zentao 集成环境开发计划](./runtime-development-plan.md) |
| PHP 基线 | PHP 8.4 |
| 数据库基线 | MySQL，并保持 PostgreSQL及信创数据库兼容 |

## 2. 计划目标

本计划负责禅道 PHP 应用为新集成环境进行的代码适配。所有业务数据库访问、数据库方言、迁移和业务 Handler 均留在 PHP 侧；Go Runtime 只通过明确、版本化的契约提供运行与调度能力。

适配完成后，禅道各版本应能够：

- 在 FrankenPHP Classic mode 下正确安装、运行和升级。
- 使用新的可靠数据库任务队列，并由 Runtime 统一调度 Worker。
- 通过显式 `get/set/delete` 使用数据库缓存。
- 遵循 `php.ini` 的 Session 配置，支持单机文件及多节点 NFS/Redis Session。
- 在多节点下使用共享 NFS 附件目录。
- 向 Runtime 输出受控、脱敏的日志和度量事件。
- 提供安装、健康检查、诊断和灰度迁移所需的应用接口。

## 3. 版本与代码归属

禅道版本存在包含关系，公共能力必须放在最低可复用层，避免在付费版本重复实现：

```text
开源版 zentaopms
  -> 企业版 zentaoext/zentaobiz + zentaoext/zentaopro/bizext
    -> 旗舰版 zentaomax
      -> IPD 版 zentaoipd
```

信创数据库方言和兼容补丁放在 `zentaopatch/innovation`。所有非开源版本共有的插件仍遵循现有 `zentaoext/zentaopro` 目录和合并机制。

代码放置原则：

- 通用框架能力优先放入 `zentaopms/framework` 或 `zentaopms/lib`。
- 通用业务入口放入 `zentaopms/module`。
- 版本专属行为放在各版本的 `extension` 或既有扩展目录。
- 数据库厂商差异复用现有 DAO 和 `zentaopatch/innovation`，不在业务模块散布 Driver 判断。
- Web 入口保持在现有 `www/index.php`、`install.php` 和 `api.php` 体系中。

## 4. 不属于本计划的工作

- Caddy、FrankenPHP、PHP、ionCube 或 DuckDB 的编译和链接。
- Windows/Linux 服务管理、Docker 镜像和安装器实现。
- Go Worker Pool、Runtime 生命周期和 MySQL 子进程管理。
- NFS、Redis、数据库集群及外部负载均衡的部署与高可用建设。
- 将业务数据库驱动或业务 SQL 移植到 Go。

这些工作归入[zentao 集成环境开发计划](./runtime-development-plan.md)或客户基础设施范围。

## 5. 任务编号与开发约束

禅道任务使用 `Z-<领域>-<序号>` 编号。

| 优先级 | 含义 |
|---|---|
| P0 | 阻塞集成环境运行、数据正确性或升级安全 |
| P1 | 阻塞正式发布或客户可运维性 |
| P2 | 可在首版稳定后按业务价值逐步接入 |

所有 PHP 修改必须遵循禅道代码规范，并同时考虑 MySQL 与 PostgreSQL。新增数据库能力必须提供 DAO 契约测试；无法完成某个信创数据库验证时，必须记录为未验证项。

## 6. 阶段 0：运行契约与兼容性基线

| ID | 优先级 | 工作项 | 主要代码范围 | 交付物 | 依赖 | 验收标准 |
|---|---|---|---|---|---|---|
| Z-ENV-01 | P0 | 定义 Runtime 环境信息读取 | framework/lib | 只读环境对象、版本和能力检测 | R-ARCH-02 | 普通 PHP-FPM/Apache 环境仍可运行，不强依赖 Runtime |
| Z-ENV-02 | P0 | Classic mode 兼容审计 | framework、入口、全局状态使用点 | 不兼容清单和修复 | R-HOST-02 | 连续请求之间没有错误依赖进程内业务状态 |
| Z-HEALTH-01 | P0 | 定义应用健康检查 | 独立最小入口或现有健康模块 | Liveness/Readiness/Deep Health 应用结果 | R-HEALTH-01 | 健康检查不建立昂贵全量业务上下文，不泄漏配置 |
| Z-OBS-01 | P1 | 定义 PHP 事件契约 | framework/lib | 日志、度量事件 Schema 和发送 Client | 无，与 R-ARCH-05 联合设计 | 发送失败快速降级，不阻断用户请求 |
| Z-REL-01 | P0 | 定义各版本应用制品 | 发布脚本和版本合并规则 | 平台无关的开源/企业/旗舰/IPD 应用包 | 无 | 不包含 `.git`、测试缓存、开发依赖和非目标版本代码 |

阶段门槛：禅道应用在没有新队列、缓存和可观测性功能时，也能在 Runtime PoC 中完成安装、登录和基础页面访问。

## 7. 阶段 1：可靠任务队列基础

### 7.1 数据与协议

| ID | 优先级 | 工作项 | 主要代码范围 | 交付物 | 依赖 | 验收标准 |
|---|---|---|---|---|---|---|
| Z-QUEUE-01 | P0 | 新队列 Schema | 安装/升级 SQL、innovation 方言 | Job、Attempt、Lease 相关表和索引 | 无 | 不复用 `zt_queue` 协议，不占用已有 `zt_job`；迁移可回滚 |
| Z-QUEUE-02 | P0 | 冻结 Queue Service 契约 | lib/module | PHP 接口、状态机、错误分类、Payload Schema | 无，与 R-ARCH-04 联合设计 | 语义为 At-least-once，明确不承诺 Exactly-once |
| Z-QUEUE-03 | P0 | 实现私有 Queue Bridge | 私有 PHP 入口、鉴权层 | 批量 Claim/Heartbeat/ACK/Retry/Cancel API | R-ARCH-04, Z-QUEUE-02 | 可通过 Fake Runtime 独立测试；仅本机 Runtime 可调用 |
| Z-QUEUE-04 | P0 | 实现 Portable CAS 领取 | Queue DAO、数据库适配 | 候选查询、条件更新、租约和 fencing | Z-QUEUE-01 | 正确性不依赖 `SKIP LOCKED`；领取结束后立即释放事务 |
| Z-QUEUE-05 | P0 | 实现完成与恢复 | Queue Service | ACK、Retry、Dead、Reaper、持久化退避 | Z-QUEUE-04 | Worker 崩溃后任务可恢复；旧 token 无法提交迟到结果 |
| Z-QUEUE-06 | P1 | 数据库快速路径 | Queue DAO、innovation | 经能力验证的 `SKIP LOCKED` 或等效实现 | Z-QUEUE-04 | 快速路径与 Portable CAS 通过同一契约测试 |

### 7.2 生产者与业务 Handler

| ID | 优先级 | 工作项 | 主要代码范围 | 交付物 | 依赖 | 验收标准 |
|---|---|---|---|---|---|---|
| Z-QUEUE-07 | P0 | PHP Queue Client | framework/lib | enqueue、delay、cancel 和查询接口 | Z-QUEUE-01 | 业务事务可与任务创建原子提交，不隐式开启嵌套事务 |
| Z-QUEUE-08 | P0 | Handler 注册协议 | module/lib | Handler 名称、Payload 版本、超时和幂等约束 | Z-QUEUE-02 | 禁止传入任意 PHP 路由、类名、Shell 或文件路径 |
| Z-QUEUE-09 | P0 | 幂等基础设施 | Queue Service/业务模块 | `idempotencyKey`、去重和副作用记录模式 | Z-QUEUE-07 | 重复投递或租约恢复不会产生不可控重复副作用 |
| Z-QUEUE-10 | P1 | 首批 Handler 迁移 | cron、邮件或 Webhook 等首批模块 | 低风险任务的新 Handler | Z-QUEUE-08/09 | 新旧消费者不能同时处理同一任务类型，可独立回滚 |
| Z-SCHED-01 | P1 | Scheduler 注册表适配 | cron/module | 固定 Job 注册和触发接口 | R-SCHED-01, Z-QUEUE-07 | 仅执行已注册任务；原始系统命令必须经过白名单映射 |

### 7.3 运维能力

| ID | 优先级 | 工作项 | 主要代码范围 | 交付物 | 依赖 | 验收标准 |
|---|---|---|---|---|---|---|
| Z-QUEUE-11 | P1 | 队列管理界面 | 新模块或既有后台模块 | 积压、失败、Attempt、重试、取消、死信处理 | Z-QUEUE-05 | 权限与审计完整，Payload 和错误信息默认脱敏 |
| Z-QUEUE-12 | P1 | 队列清理与保留 | Queue Service/Scheduler | 完成任务和 Attempt 的分批归档清理 | Z-QUEUE-05 | 清理使用有界批次，不长时间锁表或造成复制突增 |
| Z-QUEUE-13 | P1 | 队列指标 | Queue Service、OBS Client | 积压、最老任务、延迟、重试和死信指标 | Z-OBS-01, Z-QUEUE-05 | 指标失败不改变任务状态，标签基数受控 |

## 8. 阶段 2：数据库显式缓存

| ID | 优先级 | 工作项 | 主要代码范围 | 交付物 | 依赖 | 验收标准 |
|---|---|---|---|---|---|---|
| Z-CACHE-01 | P0 | 缓存表迁移 | 安装/升级 SQL、innovation | 四字段缓存表及数据库物理实现 | 无 | MySQL InnoDB、PostgreSQL UNLOGGED、达梦行存储实现符合设计 |
| Z-CACHE-02 | P0 | Cache Client | framework/lib | `get/set/delete`、Array L0、编码和键摘要 | Z-CACHE-01 | 只接受显式操作，不拦截 DAO，不提供自动 `remember()` |
| Z-CACHE-03 | P0 | 独立连接与降级 | DAO/数据库配置 | 懒加载独立 PDO、短超时、配额和错误隔离 | Z-CACHE-02 | 连接或查询失败按 Miss 处理，不加入业务事务 |
| Z-CACHE-04 | P1 | 过期清理 | Scheduler/Cache Service | 分批清理和容量指标 | Z-CACHE-02 | 清理不扫描业务表，不在读取路径写命中时间 |
| Z-CACHE-05 | P1 | 首批热点接入 | 经性能分析选定的模块 | 显式缓存键、TTL 和失效点 | Z-CACHE-03 | 仅缓存生成成本明显高于主键点查的数据，业务正确性不依赖缓存 |
| Z-CACHE-06 | P1 | 性能与故障验证 | tests | MySQL/PostgreSQL/达梦基准和故障注入 | Z-CACHE-03 | 达到详细设计门槛；慢缓存连接不拖住普通 DAO 连接 |

缓存接入必须逐个场景评审。大 ID 集合和非索引 `IN` 查询不应通过缓存掩盖，应另行采用 `JOIN/EXISTS`、临时表或数据库方言化的 Large ID Set 方案。

## 9. 阶段 3：Session、附件与部署适配

| ID | 优先级 | 工作项 | 主要代码范围 | 交付物 | 依赖 | 验收标准 |
|---|---|---|---|---|---|---|
| Z-SESSION-01 | P0 | 遵循 `php.ini` Session 配置 | framework、Session 初始化 | Handler 选择和参数读取修复 | R-CONFIG-01 | 应用不覆盖部署方配置，不静默回退本地目录 |
| Z-SESSION-02 | P0 | 兼容现有文件 Handler/API Session | framework、`apisession` 分支 | 共享文件/Redis 模式适配 | Z-SESSION-01 | 双节点无 Sticky 时登录和 API Session 可跨节点读取 |
| Z-SESSION-03 | P1 | Session 诊断 | 健康/诊断模块 | 写入、读取、锁和过期测试 | Z-HEALTH-01 | 诊断使用隔离测试键并清理，不暴露真实 Session 内容 |
| Z-FILE-01 | P0 | 附件共享路径适配 | 配置、附件模块 | 可配置共享根目录和路径校验 | R-CONFIG-01 | 节点不把附件写回本地默认目录，路径逃逸被拒绝 |
| Z-FILE-02 | P1 | 附件故障行为 | 附件模块、健康检查 | NFS 只读/不可用时的错误和诊断 | Z-FILE-01 | 写入失败不产生数据库成功但文件缺失的静默状态 |

Session 和附件的 NFS/Redis 高可用由部署方负责，禅道代码只负责正确使用配置和暴露可诊断状态。

## 10. 阶段 4：日志、度量与 Parquet 接入

| ID | 优先级 | 工作项 | 主要代码范围 | 交付物 | 依赖 | 验收标准 |
|---|---|---|---|---|---|---|
| Z-OBS-02 | P1 | Runtime 事件 Client | framework/lib | 有界、短超时、本机事件发送接口 | Z-OBS-01, R-OBS-02 | Runtime 不可用时快速丢弃或降级，不阻塞业务事务 |
| Z-OBS-03 | P1 | 日志脱敏与字段规范 | 日志框架 | 用户、请求、Trace、错误字段和脱敏策略 | Z-OBS-02 | 密码、Token、Cookie、Session、SQL 参数不写入事件 |
| Z-OBS-04 | P1 | 业务度量注册表 | 各模块 + OBS Client | 允许的名称、类型、单位和有限标签 | Z-OBS-02 | 不允许用户输入直接成为指标名或高基数标签 |
| Z-OBS-05 | P2 | 首批业务度量 | 经评审的模块 | 低频度量事件 | Z-OBS-04 | 参与事务和权限判断的数据仍以业务数据库为事实来源 |

PHP 不直接写共享 Parquet，也不操作 DuckDB 文件。PHP 只发送受控事件，由 Runtime 负责本地 spool、Parquet 发布、查询和保留。

## 11. 阶段 5：安装、升级和版本矩阵

| ID | 优先级 | 工作项 | 主要代码范围 | 交付物 | 依赖 | 验收标准 |
|---|---|---|---|---|---|---|
| Z-INSTALL-01 | P0 | 新安装适配 | `www/install.php`、安装 SQL | Runtime 环境检查和新表初始化 | Z-QUEUE-01, Z-CACHE-01 | Runtime/Full/外部数据库三种安装路径均可完成 |
| Z-UPGRADE-01 | P0 | 增量升级和回滚边界 | upgrade、数据库迁移 | 幂等迁移、失败提示和恢复说明 | Z-INSTALL-01 | 数据库迁移失败时应用不进入半启用新协议状态 |
| Z-MIGRATE-01 | P1 | 旧队列灰度迁移 | cron、Queue Service | 按 Handler 切换、回退和观察窗口 | Z-QUEUE-10, Z-UPGRADE-01 | 禁止无控制双消费；旧任务有明确排空或保留策略 |
| Z-EDITION-01 | P0 | 版本包含关系验证 | 各版本/插件 | 开源、企业、旗舰、IPD 合并测试 | Z-REL-01 | 公共能力不被扩展覆盖破坏，各工作界面可启动 |
| Z-DB-01 | P0 | 数据库契约矩阵 | DAO、innovation、tests | Queue/Cache/Upgrade SQL 契约测试 | 各数据任务 | MySQL、PostgreSQL为强制门槛；信创数据库结果逐项记录 |
| Z-E2E-01 | P0 | 联合端到端测试 | tests | 安装、登录、队列、缓存、Session、附件、升级测试 | Runtime RC | Windows/Linux/Docker 规定矩阵和付费版 ionCube 用例通过 |

## 12. 推荐的首批代码入口

实施前应先按禅道技术文档和实际调用链再次确认，以下仅作为初始调查范围：

- 当前队列：`zentaopms/module/cron/control.php`、`model.php` 和相关安装 SQL。
- 通用运行能力：`zentaopms/framework`、`zentaopms/lib`。
- Web 与安装入口：`zentaopms/www/index.php`、`install.php`、`api.php`。
- 数据库公共 DAO：`zentaopms/lib/base/dao`。
- PostgreSQL、达梦及其他信创差异：`zentaopatch/innovation/lib/dao` 和对应 SQL 补丁。
- 版本扩展：`zentaoext`、`zentaomax`、`zentaoipd` 的既有 `extension` 机制。

不得仅根据文件名直接修改。每个工作项开始前必须先定位真实入口、继承关系和版本覆盖顺序。

## 13. 跨仓依赖

| 禅道任务 | Runtime 任务 | 联合验收 |
|---|---|---|
| Z-ENV-01/02 | R-HOST-02, R-CONFIG-01 | Classic mode 安装、登录和连续请求隔离 |
| Z-HEALTH-01 | R-HEALTH-01 | 分层健康状态和故障归属 |
| Z-QUEUE-02/03 | R-ARCH-04, R-QUEUE-01 | Bridge Schema、鉴权和兼容协商 |
| Z-QUEUE-04/05 | R-QUEUE-02/03 | 双节点领取、心跳、崩溃恢复和迟到结果拒绝 |
| Z-SCHED-01 | R-SCHED-01 | 注册任务的可靠触发与取消 |
| Z-OBS-01/02 | R-ARCH-05, R-OBS-02 | 有界事件发送和 Parquet 发布 |
| Z-REL-01 | R-CI-03 | 各版本应用制品输入和安装测试 |
| Z-UPGRADE-01 | R-UPGRADE-01, R-CI-04 | 应用、数据库和 Runtime 联合升级回滚 |

## 14. 建议的迁移顺序

1. 先完成 Classic mode、安装和健康检查兼容，不改变现有业务能力。
2. 新建队列表和 Queue Service，与旧 `zt_queue` 并存但不双消费。
3. 选择邮件或 Webhook 等边界清晰、可幂等的任务完成首批迁移。
4. 完成队列管理界面和故障恢复后，再迁移更多 Handler 与 Scheduler。
5. 缓存先完成 Client 和数据库契约，再按实际性能数据接入少量热点。
6. Session 和附件在多节点发布前完成 NFS/Redis 联合验证。
7. 日志和度量先接 Runtime 自身，再逐步增加低频业务事件。
8. 最后完成各付费版本、IPD 工作界面和信创数据库回归。

## 15. 全局完成标准

禅道适配正式完成必须满足：

- 普通 Apache/PHP-FPM 部署仍可运行，新代码不无条件依赖 `zentao-runtime`。
- FrankenPHP Classic mode 下安装、登录、API、附件、队列和升级流程正确。
- Go Runtime 不访问业务数据库；PHP Bridge 不向公共网络开放。
- 队列具备租约、fencing、持久化重试、死信、幂等指导和可操作管理界面。
- 缓存只通过显式 `get/set/delete` 使用，失败按 Miss 降级，不影响业务正确性。
- 多节点 Session 尊重 NFS/Redis 配置，附件统一使用共享路径。
- PHP 日志和度量事件有版本、限流、大小限制和脱敏规则。
- MySQL、PostgreSQL通过强制契约测试；达梦及其他信创数据库结果有明确记录。
- 开源、企业、旗舰、IPD 和规定插件通过版本合并与升级测试。

## 16. 实施前仍需确认

1. 新队列表最终命名，以及首批迁移的 Handler 类型。
2. Queue Bridge 采用独立 loopback Listener 还是受控进程内入口。
3. 默认租约、Heartbeat、Retry、死信和历史保留期限。
4. 队列管理界面的模块归属和权限点。
5. 缓存首批接入场景及每个场景的 TTL、容量和失效责任人。
6. 应用健康检查使用独立入口还是现有模块扩展。
7. PHP 到 Runtime 的日志/度量本机传输协议。
8. 各版本和信创数据库的首发强制支持矩阵。

## 17. 实现阶段新增的联合契约确认（2026-08-18）

Runtime 侧首版实现完成后，以下契约需要在禅道代码适配时按原样实现，禁止在
PHP 侧自行改名或绕过。

### 17.1 分层健康探针

- `/_runtime/liveness` 和 `/_runtime/healthz` 只表示 Runtime Host 存活，
  返回 200 `{"status":"ok"}`；`/_runtime/readiness` 在 Caddy/FrankenPHP
  和必需组件就绪时返回 200 `{"status":"ready"}`，否则 503。
- 禅道应用健康检查（`Z-HEALTH-01`）作为独立的内部 PHP 入口提供给
  Runtime 深探针，返回 `ok|degraded|failed` 和稳定错误码；不得在公共
  readiness 路径上执行昂贵业务上下文初始化，也不得返回配置、路径或
  Session 内容。
- 深探针结果中的组件名建议使用 `runtime|php|app|dependency|shared`，
  与应用相关探针放入 `app` 或 `shared`，便于 Runtime 故障归属。

### 17.2 Queue Bridge 传输细节

`R-ARCH-04` 已冻结的 Go DTO（`internal/queue/bridge`）是唯一字段来源。PHP
实现（`Z-QUEUE-03`）必须提供：

- 仅绑定 loopback 的私有 HTTP Listener，路径前缀
  `/internal/runtime/queue/v1/`，固定端点
  `capabilities/claim/execute/heartbeat/reap/stats/control`。
- 每次 Runtime 启动生成随机凭据，通过
  `X-Zentao-Queue-Token` 请求头传递；Go 不写日志、不持久化该凭据。
- `X-Zentao-Queue-Schema: 1` 请求头与根对象 `schema: 1` 双重校验。
- 请求/响应 64 KiB 上限、批量 128 上限、未知字段拒绝、错误对象
  `{code,message,retryable}`，错误码集合见
  `docs/queue-bridge-v1-contract.md`。

### 17.3 可观测性事件信封

PHP 发送给 Runtime 的日志/度量事件（`Z-OBS-01`）必须携带：

```text
schemaVersion=1, eventTime, ingestTime, clusterID, nodeID, bootID,
eventID, source, kind(metrics|logs)
```

日志补充 `level/message/traceID/durationMs/statusCode/fields`；度量补充
`metricName/metricKind/value/count/labels`。所有字段在进入 Runtime 缓冲前
由 PHP 侧脱敏：不得包含密码、Token、Cookie、Session ID、Authorization、
完整 SQL 参数、请求体或上传内容。指标名和标签必须来自受控注册表。

### 17.4 配置指纹与节点身份

- Runtime 配置包含 `clusterID` 和 `nodeID`；`nodeID` 在 Cluster 内必须唯一，
  PHP 健康检查、队列 Claim/Heartbeat 和可观测性事件必须透传同一值。
- 多节点必须一致的配置（Cluster ID、应用/Runtime/PHP 版本、数据库 Schema、
  Session Handler、NFS 路径、Parquet 数据集根目录、队列/缓存 Schema）由
  节点管理员维护；Runtime 计算 `clusterConfigDigest`，PHP 侧不得静默修改
  这些配置或回退本地目录。

### 17.5 可信代理与转发头

- 使用内嵌 Caddy Gateway 或外部负载均衡时，App listener 只信任明确配置的
  代理地址；来自其他来源的 `X-Forwarded-For/Proto/Host` 必须覆盖或移除。
- 非幂等请求（POST/PATCH/DELETE、上传、已发送请求体的请求）不得由
  Gateway 自动重试；PHP 侧不依赖重试掩盖重复副作用，幂等键必须落库。

### 17.6 Runtime 环境信息

`Z-ENV-01` 的只读环境对象至少读取以下变量，并允许普通 PHP-FPM/Apache
环境缺失时继续运行：

```text
ZENTAO_RUNTIME_ROOT
ZENTAO_RUNTIME_LISTEN
ZENTAO_RUNTIME_NODE_ID
ZENTAO_RUNTIME_CLUSTER_ID
ZENTAO_RUNTIME_DRAIN_TIMEOUT
ZENTAO_RUNTIME_ACCESS_LOG
```

新功能（队列、缓存、Session 诊断、可观测性）必须通过能力检测判断
Runtime 是否可用，不得在无 Runtime 环境时报错。

## 18. 已定位的禅道代码改动点（2026-08-18 只读核查）

以下位置来自当前仓库实际代码，实施时以提交时的最新代码为准，先确认继承
和版本覆盖顺序再修改。

### 18.1 Session 配置所有权（Z-SESSION-01/02）

- `zentaopms/framework/base/router.class.php`
  - `startSession()`：约 1226-1268 行。当前行为：
    1. `session.save_handler == files` 时强制
       `session_save_path(ZT_SESSION_PATH 或 tmp/session)` 并注册
       `ztSessionHandler`。
    2. API `useToken` 分支强制 `ZT_APISESSION_PATH` 或
       `tmp/apisession`，即使 `php.ini` 已配置 Redis/NFS 也会被覆盖。
  - `ztSessionHandler`：约 3841 行起。当前只对单次写使用 `LOCK_EX`，
    未覆盖完整读-改-写周期的互斥；NFS 多节点场景需要改用 PHP 原生文件
    Handler 或实现等价跨节点锁。
- 改动点：普通会话尊重 `php.ini` 的 `session.save_handler/save_path`；
  `ss_` Token 和需要 Session 的 API 流程使用同一共享后端（NFS 独立共享
  子目录或 Redis 独立 key prefix）；多节点本地文件只允许显式 Sticky
  降级；安装检查按实际 Handler 验证。

### 18.2 队列现状与改造入口（Z-QUEUE-01..13）

- 当前队列表：`zentaopms/db/zentao.sql:1665-1678` 的 `zt_queue`
  （字段 `cron/type/command/status/execId/createdDate/deleted`）。
- 当前消费者：`zentaopms/module/cron/control.php`
  - `consumeTasks()`：约 369 行起，直接 `SELECT ... WHERE status='wait'
    ORDER BY createdDate`。
  - `consumeTask()`：约 386 行起，条件更新 `doing + execId` →
    `usleep(500000)` 复核 → 执行 → 无条件更新 `done`（约 454 行）。
  - `type=system` 直接 `exec($task->command)`（约 442 行），存在任意系统
    命令执行面，Scheduler 改造必须改为白名单映射。
- `zt_job` 已被 CI 模块占用（`zentaomax/db/standard/*.sql` 中建表），
  新队列表不能使用该名称；建议 `zt_asyncjob`、`zt_asyncjobattempt`、
  `zt_runtimelease`，最终命名进入数据库评审后确认。
- 改动点：新增 Queue Service/DAO、Portable CAS、租约与 fencing、持久化
  重试/死信、私有 Bridge 入口（loopback + 随机凭据）、按 Handler 切换的
  灰度迁移；新表 SQL 进入 `zentao.sql` + `db/update*.sql` +
  `zentaopatch/innovation` 方言。

### 18.3 安装、升级与入口（Z-INSTALL-01/Z-UPGRADE-01/Z-ENV-01/Z-HEALTH-01）

- Web 入口：`zentaopms/www/index.php`（约 36-42 行调用
  `checkInstalled()`/`checkNeedUpgrade()`）、`www/api.php`、
  `www/install.php`。
- 安装/升级检测：`zentaopms/framework/base/router.class.php` 的
  `checkInstalled()`（约 3543 行）和 `checkNeedUpgrade()`（约 3561 行）。
- 版本合并：`zentaoipd/build/mergesourceipd.php` 等现有合并脚本可作为
  `Z-REL-01` 平台无关应用制品的参考，但不能直接复用为发布供应链。
- 改动点：
  - 新增只读 Runtime 环境对象（读取 `ZENTAO_RUNTIME_*`），无 Runtime
    时返回未启用，不阻断普通 PHP-FPM/Apache。
  - 独立健康入口（建议 `www/health.php` 或框架静态方法），输出
    `ok|degraded|failed` 与稳定错误码，不建立昂贵业务上下文。
  - 安装 SQL 增加 Queue/Cache 新表；升级 SQL 幂等并保留回滚说明。

### 18.4 数据库方言（Z-DB-01）

- 公共 DAO：`zentaopms/lib/base/dao/dao.class.php`（默认 `mysql`
  driver；达梦特殊处理见约 2339 行与 3117 行）。
- 信创方言：`zentaopatch/innovation/lib/dao/` 下的
  `pgsql/kingbase/highgo/gauss/dm.class.php`。
- 改动点：Queue/Cache 的 DDL、UPSERT、影响行数与时间精度差异放入对应
  DAO 或 innovation 扩展，不在业务模块散布 driver 判断；MySQL 与
  PostgreSQL 为强制契约测试门槛。

### 18.5 应用制品交付契约（Z-REL-01，外部流程提供）

禅道各版本应用包由外部发布流程生成并校验，runtime 仓库不负责合并或打包。
提供给联合测试的制品必须满足：

- 格式：`tar`/`tar.gz`/`tar.xz`/`tar.zst`/`zip`，或已解包的应用目录；
  压缩包内允许存在一层版本目录，也允许文件直接在包根。
- 根布局：`www/` 下包含 `index.php`、`install.php`、`api.php` 等入口；
  框架、模块、扩展随应用包合并结果一起提供（对应禅道现有
  `zentaoext`/`zentaomax`/`zentaoipd` 合并产物）。
- 元数据：建议包含 `VERSION` 或等价版本标识；不包含 `.git`、测试缓存、
  开发依赖和非目标版本代码。
- 消费方式：`scripts/ci/stage-app-package.sh <package> <install-root>`
  解包到 `<install-root>/app/releases/<version>` 并切换 `app/current`；
  `tests/e2e/zentao-app-smoke.sh` 在 Runtime 容器中执行
  readiness 与 PHP 入口冒烟。
- CI：`application-matrix.yml` 在 private/self-hosted Runner 上通过
  `app_package_path` 输入消费外部包，付费制品不上传公开 artifact。
- 本地约定目录为 runtime 仓库内的 `app-packages/`（已加入
  `.gitignore`，付费包不会入库；`ZENTAO_APP_PACKAGES_DIR` 可覆盖）：
  `open/zentaopms.zip`、`biz/zentaopms.zip`、
  `max/zentaopms.zip`、`ipd/zentaopms.zip`；每个目录只保留一个 zip，
  版本由包内 `VERSION` 识别。查找使用
  `scripts/ci/find-app-package.sh <edition>`。

### 18.6 FrankenPHP Classic mode 兼容（Z-ENV-02，当前阻塞联合测试）

真实应用包（ipd5.5 已核实）的 `zentaopms/www/index.php` 末尾使用：

```php
if(php_sapi_name() == 'frankenphp')
{
    \frankenphp_handle_request($handler);
}
else
{
    $handler();
}
```

`frankenphp_handle_request()` 仅在 worker mode 下可用；Classic mode 调用会抛出
`frankenphp_handle_request() called while not in worker mode`，导致 500。
正确判断方式是 FrankenPHP 官方 profile 使用的 `FRANKENPHP_WORKER` 环境变量。

禅道侧正式修复建议（待实施）：

```php
if(php_sapi_name() == 'frankenphp' && isset($_SERVER['FRANKENPHP_WORKER']))
{
    while(\frankenphp_handle_request($handler)) {}
}
else
{
    $handler();
}
```

Runtime 集成环境生成时会对暂存副本应用最小兼容补丁
（`scripts/ci/patch-classic-mode.sh`，只改 `app/releases/<version>/www/index.php`，
不改原始 zip），作为正式修复前的临时 shim；正式修复后应移除该补丁逻辑。
