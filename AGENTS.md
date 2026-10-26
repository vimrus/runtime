# zentao 集成环境开发指南

## 项目名称

本项目的集成环境产品名称是 **zentao**。

仓库位于 `/home/z/dev/runtime`，GitHub 仓库为 `vimrus/runtime`。自研 Go Runtime Host 的可执行程序按现有设计命名为 `zentao-runtime`；除非需求明确要求，不要自行修改产品名、二进制名或交付物前缀。

## 项目目标

zentao 是禅道面向 Windows、Linux 和 Docker 的新一代集成运行环境。项目将 Caddy 和 FrankenPHP 作为 Go Library 嵌入自研 Runtime Host，为禅道 PHP 应用提供统一的安装、运行、升级、诊断、构建和打包能力。

交付形态包括：

- Windows Runtime。
- Windows Full，包含 MySQL。
- Linux Runtime。
- Linux Full，包含 MySQL。
- Docker 镜像及 Compose 部署。

## 当前阶段

项目已实现 runtime-development-plan 阶段 0-5 的主要 Runtime 侧任务，并在
GitHub-hosted Runner 上通过 Linux amd64、Linux arm64、Windows x86_64 原生
构建矩阵（含 ionCube 与 DuckDB）。已交付：Control Plane、分层健康、
Queue Engine、DuckDB/Parquet 可观测性、升级事务、Linux/Windows 服务集成、
Runtime/Full 打包、签名供应链和 CI workflow。

禅道侧尚未修改：适配计划已冻结联合契约（第 17 节）并定位具体改动点
（第 18 节），待确认后按 `docs/development/01-zentao-application-adaptation-plan.md`
实施。正式安装器、代码签名、Docker 多架构镜像推送和再分发法务确认仍需
发布环境与权限。

开始开发前必须先阅读与任务相关的设计文档，不能绕过已经确认的架构边界：

- `docs/design/00-system-overview.md`：总体目标、组件边界、数据流和部署模型。
- `docs/design/01-runtime-host.md`：Caddy、FrankenPHP 和 Runtime Host 设计。
- `docs/design/02-frankenphp-integration.md`：PHP、Classic mode 和 ionCube 集成设计。
- `docs/design/03-message-queue.md`：数据库消息队列和 PHP Queue Bridge 设计。
- `docs/design/05-observability.md`：DuckDB、度量项、日志和共享 Parquet 数据集设计。
- `docs/design/06-deployment-and-ha.md`：PHP Session、NFS、外部负载均衡和双节点高可用设计。
- `docs/design/07-build-and-release.md`：Windows、Linux、Docker 构建与打包设计。

所有新增设计文档必须放在 `docs/`，不要在仓库根目录或其他目录散落设计说明。

## 已确认的架构

### Runtime

- Runtime Host 使用 Go 开发，程序名为 `zentao-runtime`。
- Caddy 作为 Go Library 嵌入 Runtime Host。
- FrankenPHP 作为 Go Library/Caddy Module 嵌入 Runtime Host。
- FrankenPHP 使用 Classic mode，不使用 Worker mode。
- Classic mode 不跨请求保留禅道 PHP 应用状态，但 Runtime、Caddy、PHP ZTS 线程池和扩展模块仍然常驻。
- PHP 基线为 PHP 8.4 ZTS。
- 付费版本必须支持 ionCube Loader。
- Linux PHP 使用最小 `zend_signal` ABI Shim 兼容 ionCube，同时保持 Zend Signals 关闭。
- Windows 当前不应用 Linux 的 `zend_signal` ABI Shim。

### 平台和构建

- Windows 首期支持 x86_64。
- Linux 首期支持 amd64 和 arm64。
- Docker 使用 Linux amd64 和 arm64 镜像。
- GitHub Actions 优先使用目标平台原生 Runner，不默认采用跨平台交叉编译。
- 设计基线 Runner 为 `ubuntu-24.04`、`ubuntu-24.04-arm` 和 `windows-2025`。
- 正式依赖必须固定版本、提交或镜像 digest，并生成 manifest 和 SBOM。
- Linux 和 Docker 使用 glibc/Debian 兼容基线，不把 Alpine/musl 作为正式交付基线。

### 数据库边界

- Go Runtime 不直接连接禅道数据库。
- Go Runtime 不打包 MySQL、PostgreSQL、达梦或其他厂商数据库 Driver 和客户端。
- Go Runtime 不持有禅道数据库凭据。
- 所有数据库操作由 PHP Service 通过禅道现有 DAO 和信创数据库适配层执行。
- 新增数据库能力时，应扩展 PHP DAO、迁移脚本和契约测试，而不是在 Go 中复制数据库适配体系。

### 消息队列

- 消息队列使用禅道业务数据库持久化。
- PHP Queue Service 负责数据库操作，Go Runtime 负责 Worker Pool、并发、超时、心跳和生命周期。
- Go 通过本机私有、版本化的 PHP Queue Bridge 调用队列能力。
- 消费语义为 At-least-once，必须使用租约、fencing token、持久化重试和死信状态。
- 不复用现有 `zt_queue` 协议，不使用已被其他模块占用的 `zt_job`。
- `watermill-sql` 不进入 Runtime 依赖；Watermill Core 只能作为 POC 对照项。

### 缓存

- 缓存归属 PHP 侧 `lib/cache`，后端顺序为 Redis（多节点共享）→ APCu（单节点，Runtime 内置扩展）→ Null；Go Runtime 不提供 Cache Service 或 Cache Bridge。
- 缓存是可丢弃数据：读取失败按 miss 回源数据库，写入失败不回滚业务结果；Runtime 不连接禅道业务数据库。
- 多节点默认各节点各自本地 APCu（缓存不作为跨节点一致性机制）；需要跨节点共享命中率时改配 Redis。
- 同一 Runtime 进程内多个应用共享同一块 APCu 空间，必须按应用隔离 namespace。
- 权限、锁、许可证、工作流状态和其他强一致数据不能缓存。
- DuckDB 只保存缓存聚合指标，不保存完整缓存值。

### 应用装载与可拔插

- Runtime 制品不包含任何应用代码；应用是独立的平台无关制品，由配置装载。
- 一个 Runtime 实例可以托管多个应用：单一监听、单一 host，应用按 URL 子目录前缀区分，最多一个应用使用 `/`。
- 应用在 `config/runtime.toml` 的 `[[apps]]` 中声明（`id`、`path`、`dir`、`webRoot`，可选 `dataDir`、`tmpDir`、`env`）；不做目录自动扫描。
- 每个应用使用独立数据库和独立的附件/Session 数据根，可变数据不得写回应用代码目录。
- Runtime 为每个应用注入 `ZT_WEB_ROOT`、`ZT_APP_ID`、`ZT_DATA_ROOT`（应用数据目录，附件位于其 `upload/`）、`ZT_TMP_ROOT`、`ZT_SESSION_PATH`、`ZT_APISESSION_PATH`、`ZT_BACKUP_PATH`、`ZENTAO_LOG_DIR`（`<dataDir>/observability/raw-logs`）；启用每应用 Queue Bridge 时还会注入该应用专属的 `ZENTAO_QUEUE_TOKEN`，启用观测时注入该应用专属的 `ZENTAO_OBSERVABILITY_*`（含 flush 模式的查询桥变量），均每次启动轮换。应用侧显式配置的环境变量优先。
- 新增、替换或移除应用通过重启 Runtime 生效；首版不实现热加载。
- `/_runtime/` 为 Runtime 保留前缀；配置校验拒绝重复前缀和嵌套前缀冲突。

### 部署与高可用

- Runtime 不实现 Session 服务，Session Handler 和存储参数遵循 PHP `php.ini`。
- 单机默认使用本地文件 Session；多节点由部署方配置共享 NFS 文件 Session 或外部 Redis Session。
- 多节点本地文件 Session 只允许作为显式 Sticky 降级模式，不属于标准高可用拓扑。
- Runtime 只做 Session 配置交叉检查和 PHP 侧读写诊断；NFS/Redis 高可用由部署方负责。
- 现有文件 Handler 和 API `apisession` 分支必须适配共享后端，不能覆盖显式 `php.ini` 配置或回退节点本地目录。
- Linux/Docker 多节点附件使用客户提供的 NFSv4.1 共享文件系统。
- 节点配置分别维护，但 Cluster ID、应用版本、Schema、Session Handler/共享存储配置和附件路径等集群配置必须一致。
- 客户已有云、硬件或托管负载均衡时优先接入。
- 两个 Linux 节点没有外部负载均衡时，使用 Keepalived 浮动 VIP 和内嵌 Caddy Gateway。
- Caddy Gateway 默认使用 `least_conn`，不需要 Session Sticky，不自动重试非幂等请求。
- 数据库和 NFS 的高可用属于客户基础设施边界，不能把两个应用节点描述为完整的数据层高可用。
- 第一版不承诺 Windows Keepalived + NFS 多节点拓扑。

### DuckDB 与可观测性

- DuckDB 作为 Go Library 嵌入 `zentao-runtime`，用于指标/日志 Parquet 的生成和受控查询。
- 不共享 `.duckdb` 文件；多节点在 NFS 上共享分区式 Parquet 数据集。
- 每个节点只写自己的 `node=<nodeID>` 分区，每个批次发布新的不可变 `part-<batchID>.parquet`。
- 临时文件写完并校验后，只能在同一 NFS 文件系统内通过原子 rename 发布。
- 不追加或覆盖已经发布的 Parquet，不为写入建立选主或分布式锁。
- NFS 故障时使用有界本地 spool；可观测性故障不能阻断业务。
- 指标名称和标签必须受注册表约束，日志在进入缓冲区前脱敏。
- 不向公共 Web 暴露任意 DuckDB SQL、任意文件路径或 Extension 加载能力。
- 每个节点只清理自己的过期分区；第一版不实现跨节点文件合并。
- 参与事务、权限和业务状态的禅道度量仍以业务数据库为事实来源，Parquet 只是分析副本。

## 目录约定

当前和计划中的主要目录如下：

```text
runtime/
  AGENTS.md
  README.md
  docs/                    设计文档
  cmd/zentao-runtime/      Go 程序入口
  internal/                Runtime Host 内部包
  patches/                 PHP/依赖补丁及说明
  scripts/                 可重复构建和打包脚本
  packaging/               Windows、Linux、Docker 打包资源
  tests/                   集成、兼容和端到端测试
  .github/workflows/       GitHub Actions Workflow
```

只有在实际实现相应模块时才创建目录，不要提交没有用途的占位目录。

## 开发要求

- 优先使用成熟开源库和仓库已有模式，不重复实现 Caddy、FrankenPHP、数据库 Driver 或通用协议能力。
- 新依赖必须检查许可证、维护状态、跨平台能力、CGO 影响和供应链风险。
- Go 代码保持 Runtime 基础设施职责，不承载禅道业务逻辑。
- PHP Bridge 必须是私有、版本化、可鉴权的本机接口，不能暴露为公共业务 API。
- 平台差异放入明确的 Windows/Linux 实现边界，不在通用代码中散布条件判断。
- 配置、manifest、Workflow 输入和内部协议使用结构化格式并进行 schema 校验。
- 不在代码、Workflow、日志、测试夹具或文档中提交 Token、证书、数据库密码和签名私钥。
- 修改 PHP 或 FrankenPHP 源码时使用最小补丁，并为上游版本和 ABI 增加验证测试。
- 不扩大 ionCube `zend_signal` Shim 的符号范围，除非新 Loader 的符号检查和兼容测试证明必须调整。

## 测试要求

测试范围应与修改风险匹配，至少考虑：

- Windows、Linux amd64、Linux arm64 和 Docker 构建。
- PHP 8.4 ZTS Classic mode 请求隔离。
- ionCube Loader 加载和付费版最小请求。
- Caddy 静态文件、PHP 路由、PATH_INFO、上传和 HTTPS。
- Runtime 启动、重载、优雅停止和子进程管理。
- MySQL、PostgreSQL 和信创数据库的 PHP DAO 契约。
- 消息队列的并发领取、租约、重试、死信和 Worker 崩溃恢复。
- PHP APCu/Redis 缓存后端的过期、容量、故障降级和多应用 namespace 隔离。
- DuckDB 三平台加载、共享 Parquet 原子发布、NFS 故障补发、查询资源限制和日志脱敏。

没有执行某个平台或数据库测试时，必须在交付说明中明确列出未验证项和剩余风险。

## 变更纪律

- 保持修改范围与需求一致，不顺手重构无关模块。
- 不覆盖或回退用户已有修改。
- 不把 `runtime/` 内容提交到上级禅道代码仓库；本目录是独立 Git 仓库。
- 未经明确要求，不创建 Release、不发布镜像、不上传制品，也不修改 GitHub 仓库权限。
- 架构决策发生变化时，先更新对应 `docs/` 设计，再实现代码和 Workflow。
