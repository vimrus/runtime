# zentao 集成环境开发计划

## 1. 文档信息

| 项目 | 内容 |
|---|---|
| 状态 | Runtime 侧已实施并通过三平台原生矩阵验证；正式发布与禅道联合测试待执行 |
| 日期 | 2026-08-18 |
| 责任仓库 | `vimrus/runtime` |
| 产品名称 | `zentao` |
| Runtime 程序 | `zentao-runtime` |
| 应用侧计划 | [禅道代码适配开发计划](./zentao-application-adaptation-plan.md) |

## 2. 计划目标

本计划只负责集成环境自身的开发、构建、打包、运行和发布，不负责实现禅道业务逻辑或复制禅道的数据库适配体系。

最终交付物包括：

- Windows x86_64 Runtime 包。
- Windows x86_64 Full 包，包含 MySQL 8.4 LTS。
- Linux amd64、arm64 Runtime 包。
- Linux amd64、arm64 Full 包，包含 MySQL 8.4 LTS。
- Linux amd64、arm64 Docker Web 镜像和 Compose 模板；MySQL 使用独立容器。
- 构建清单、校验和、SBOM、签名和制品大小报告。

## 3. 已确认的边界

1. Caddy 和 FrankenPHP 均作为 Go Library 嵌入 `zentao-runtime`。
2. FrankenPHP 使用 Classic mode；PHP 应用状态不跨请求保留。
3. PHP 基线为 PHP 8.4 ZTS，付费版必须通过 ionCube Loader 验证。
4. Linux PHP 保持 `--disable-zend-signals`，通过最小 ABI Shim 导出 ionCube 所需符号；Windows 不应用该 Shim。
5. Go Runtime 不访问禅道业务数据库、不保存数据库凭据、不打包业务数据库 Driver。
6. 队列数据操作由 PHP Queue Service 完成；Go 负责 Worker Pool、并发、超时、心跳和生命周期。
7. Session 由 PHP `php.ini` 配置；Runtime 只校验并诊断，不实现 Session 存储。
8. 缓存由禅道 PHP 代码显式访问数据库缓存表，Runtime 不实现缓存服务。
9. DuckDB 嵌入 Runtime，用于受控生成和查询日志、指标 Parquet；多节点不共享可写 `.duckdb` 文件。
10. Linux 双节点可使用 Keepalived VIP 和内嵌 Caddy Gateway；数据库、NFS 和 Redis 的高可用属于外部基础设施。

## 4. 不属于本计划的工作

- 禅道 PHP Queue Service、DAO、任务 Handler 和队列管理页面。
- 缓存表、Cache Client 及业务模块缓存接入。
- 禅道 Session Handler、附件逻辑和业务健康检查实现。
- 禅道数据库迁移脚本及各数据库 SQL 方言。
- 禅道应用各版本的业务兼容修改。
- MySQL、NFS、Redis 或外部负载均衡集群本身的高可用建设。

以上工作归入[禅道代码适配开发计划](./zentao-application-adaptation-plan.md)或客户基础设施范围。

## 5. 任务编号与优先级

Runtime 任务使用 `R-<领域>-<序号>` 编号。

| 优先级 | 含义 |
|---|---|
| P0 | 阻塞首个可运行版本或影响不可逆架构边界 |
| P1 | 阻塞正式发布，但不阻塞最小 PoC |
| P2 | 可在首版稳定后补充，不影响核心运行 |

任务只有同时满足代码、测试、文档和制品要求才算完成。仅在单一开发机运行成功不视为完成。

## 6. 阶段 0：契约冻结与仓库骨架

目标是先冻结跨语言、跨平台和跨仓契约，避免 Runtime 与禅道代码分别实现后无法对接。

| ID | 优先级 | 工作项 | 交付物 | 依赖 | 验收标准 |
|---|---|---|---|---|---|
| R-ARCH-01 | P0 | 建立版本锁定清单 | PHP、FrankenPHP、Caddy、Go、DuckDB、ionCube、MySQL 版本和来源清单 | 无 | 所有正式依赖固定版本、提交或摘要，记录许可证和校验和 |
| R-ARCH-02 | P0 | 定义 Runtime 配置 Schema | 版本化配置 Schema、默认值、平台覆盖规则、敏感字段规则 | 无 | 无效配置在启动前失败；未知字段按明确策略处理 |
| R-ARCH-03 | P0 | 定义本地 Control Plane 契约 | CLI、Unix Socket/Named Pipe 操作和响应 Schema | R-ARCH-02 | 管理接口不可从公共网络访问，所有操作可鉴权和审计 |
| R-ARCH-04 | P0 | 冻结 PHP Queue Bridge v1 | Claim、Heartbeat、ACK、Retry、Dead、Cancel 的请求响应和错误码 | 无，与 Z-QUEUE-02 联合设计 | Fake Bridge 与 PHP 契约测试使用同一组夹具 |
| R-ARCH-05 | P0 | 定义可观测性事件契约 | Runtime 日志、指标 Schema，PHP 事件入口和脱敏规则 | 无，与 Z-OBS-01 联合设计 | Schema 可版本化，未知或超限事件被拒绝且不影响业务 |
| R-REPO-01 | P0 | 建立最小仓库结构 | `cmd/`、`internal/`、`patches/`、`scripts/`、`packaging/`、`tests/` | R-ARCH-01 | 不创建空占位目录；每个目录随首个真实实现提交 |

阶段门槛：跨仓契约有版本号、示例、错误模型和契约测试入口；尚未确认的协议项不得进入正式实现。

## 7. 阶段 1：PHP、FrankenPHP 与 ionCube PoC

该阶段只验证风险最高的运行链路，先以 Linux amd64 为基线。

| ID | 优先级 | 工作项 | 交付物 | 依赖 | 验收标准 |
|---|---|---|---|---|---|
| R-PHP-01 | P0 | 固化 PHP 8.4 ZTS 构建 | 可重复构建脚本、扩展清单、构建元数据 | R-ARCH-01 | CLI 与嵌入式 PHP 报告相同 ABI、扩展和配置目录 |
| R-PHP-02 | P0 | 实现 Linux `zend_signal` ABI Shim | 最小源代码补丁、导出符号检查 | R-PHP-01 | Zend Signals 保持关闭；只导出已验证的最小符号集合 |
| R-PHP-03 | P0 | 验证 ionCube | Loader 装配、明文/加密探针和失败诊断 | R-PHP-02 | 付费版最小请求成功；Loader 不兼容时启动检查给出明确原因 |
| R-HOST-01 | P0 | 建立最小 Go Host | 自研 `main` 链接 Caddy 和 FrankenPHP Module | R-PHP-01 | 不依赖上游 FrankenPHP CLI，能启动和优雅停止 |
| R-HOST-02 | P0 | 跑通 Classic 请求 | 最小 Caddy JSON、静态文件和 PHP 路由 | R-HOST-01 | 连续请求不共享禅道请求状态；上传、PATH_INFO 和异常响应正确 |
| R-TEST-01 | P0 | 建立 ABI 与 Classic 冒烟测试 | CI 可执行测试夹具 | R-PHP-03, R-HOST-02 | 明文和 ionCube 用例均通过，并能发现符号缺失与 ABI 漂移 |

阶段门槛：Linux amd64 上完成一次可重复的端到端构建，能运行禅道开源版和一个最小付费版探针。

## 8. 阶段 2：通用 Runtime Host

| ID | 优先级 | 工作项 | 交付物 | 依赖 | 验收标准 |
|---|---|---|---|---|---|
| R-HOST-03 | P0 | Host 生命周期状态机 | 初始化、运行、排空、停止和失败状态 | R-HOST-02 | 重复启动被阻止；停止时不接受新任务并完成有界排空 |
| R-CONFIG-01 | P0 | 配置加载与渲染 | 默认配置、用户配置、环境覆盖和 Caddy JSON 生成 | R-ARCH-02 | 热更新失败时旧配置继续服务；需重启项明确提示 |
| R-CTRL-01 | P1 | 本地管理命令 | `start/status/reload/stop/diagnose/version` | R-ARCH-03, R-HOST-03 | Windows/Linux 返回一致的结构化结果和退出码 |
| R-HEALTH-01 | P0 | 分层健康检查 | Liveness、Readiness、Deep Health | R-HOST-03 | 使用 Fake 应用探针时可区分 Runtime、PHP、应用及共享依赖故障 |
| R-SEC-01 | P0 | Runtime 安全基线 | 最小权限、路径校验、敏感信息脱敏、管理面权限 | R-CONFIG-01 | 非特权用户可运行；诊断包和日志不泄漏密钥 |
| R-LOG-01 | P1 | 统一日志管道 | Caddy、Runtime、PHP 子系统结构化日志 | R-HOST-03 | 日志包含节点、请求和组件标识，支持大小和保留限制 |
| R-UPGRADE-01 | P0 | 目录与升级事务 | Runtime/Application/Config/Data 分离，备份与回滚状态机 | R-CONFIG-01 | 升级失败能恢复旧版本；用户配置不被静默覆盖 |

## 9. 阶段 3：队列、Scheduler 与可观测性

| ID | 优先级 | 工作项 | 交付物 | 依赖 | 验收标准 |
|---|---|---|---|---|---|
| R-QUEUE-01 | P0 | PHP Queue Bridge 客户端 | 私有本机传输、鉴权、批量请求、版本协商 | R-ARCH-04 | 使用 Fake Bridge 可独立测试；Runtime 不持有数据库凭据 |
| R-QUEUE-02 | P0 | 有界 Worker Pool | 队列级并发、超时、取消、排空和公平调度 | R-QUEUE-01 | 慢队列不阻塞其他队列；进程退出不产生无界新领取 |
| R-QUEUE-03 | P0 | 租约维护 | 批量 Heartbeat、fencing token 透传和过期处理 | R-QUEUE-02, Z-QUEUE-03 | Worker 崩溃后任务可恢复；迟到 ACK 不能覆盖新租约 |
| R-QUEUE-04 | P1 | Wakeup 与兜底轮询 | 自适应轮询、抖动和退避 | R-QUEUE-02 | 空队列不高频打数据库；唤醒丢失后仍能最终消费 |
| R-SCHED-01 | P1 | Scheduler 编排 | 受控触发、并发限制、超时和状态报告 | R-HOST-03, R-ARCH-04 | 使用 Fake 注册表可测试；不执行未注册的任意系统命令 |
| R-OBS-01 | P1 | DuckDB Library 集成 | 三平台链接、扩展锁定和资源限制 | R-ARCH-01 | 运行时不下载扩展，不开放任意 SQL 或任意路径 |
| R-OBS-02 | P1 | Parquet 发布器 | 本地 spool、批处理、校验、原子 rename | R-OBS-01 | NFS 故障不阻断业务，恢复后可按稳定 Batch ID 补发 |
| R-OBS-03 | P1 | 受控查询与保留 | 固定查询模板、分区裁剪、节点自清理 | R-OBS-02 | 查询有内存/CPU/超时限制，只清理本节点拥有的数据 |

阶段门槛：使用 Fake PHP Bridge 和真实禅道 Bridge 分别完成单节点、双节点、崩溃恢复和排空测试。

## 10. 阶段 4：平台服务与打包

| ID | 优先级 | 工作项 | 交付物 | 依赖 | 验收标准 |
|---|---|---|---|---|---|
| R-LINUX-01 | P0 | Linux 服务集成 | systemd unit、目录权限、日志轮转和卸载脚本 | R-HOST-03 | amd64、arm64 原生 Runner 安装、启动、升级、卸载通过 |
| R-WIN-01 | P0 | Windows 服务集成 | Windows Service、Named Pipe、Job Object 和事件日志 | R-HOST-03 | 子进程随服务停止，路径含空格和非 ASCII 用户目录可用 |
| R-DOCKER-01 | P0 | Docker Web 镜像 | Debian/glibc 多架构镜像、健康检查和 Compose | R-LINUX-01 | Web 镜像不包含 MySQL，amd64/arm64 manifest 正确 |
| R-MYSQL-01 | P1 | Full 包 MySQL 管理 | 固定 MySQL 8.4 发行物、初始化和生命周期管理 | R-LINUX-01, R-WIN-01 | 端口冲突、初始化失败、已有数据和停止超时均有明确处理 |
| R-PACK-01 | P0 | Runtime 包组装 | Windows/Linux Runtime 包 | R-PHP-03, R-UPGRADE-01 | 包内无 `.git`、编译缓存、测试密钥和不必要开发文件 |
| R-PACK-02 | P1 | Full 包组装 | Windows/Linux Full 包 | R-PACK-01, R-MYSQL-01 | Runtime 与 MySQL 可分别升级，业务数据目录不随卸载删除 |
| R-GW-01 | P1 | 双节点入口模板 | Caddy Gateway + Keepalived 配置生成和检查 | R-CONFIG-01 | 非幂等请求不自动重试；VIP 故障切换有清晰诊断 |

## 11. 阶段 5：CI、发布与质量门槛

| ID | 优先级 | 工作项 | 交付物 | 依赖 | 验收标准 |
|---|---|---|---|---|---|
| R-CI-01 | P0 | CI 骨架 | lint、单元测试、依赖和文档检查 | R-REPO-01 | PR 不拥有发布权限，Actions 固定到提交摘要 |
| R-CI-02 | P0 | 原生平台构建矩阵 | Ubuntu amd64/arm64、Windows x86_64 Jobs | R-TEST-01 | 正式 Runtime 不通过 QEMU 或非目标平台交叉编译产生 |
| R-CI-03 | P0 | 应用组合测试 | 各禅道版本平台无关应用制品输入 | Z-REL-01 | 开源、企业、旗舰、IPD 的规定矩阵完成安装和请求测试 |
| R-CI-04 | P1 | 安装与升级测试 | 干净安装、覆盖升级、失败回滚和保留数据测试 | R-PACK-02, Z-UPGRADE-01 | Runtime、应用、配置和数据四类内容边界符合契约 |
| R-REL-01 | P0 | 发布供应链 | manifest、SBOM、校验和、签名和 provenance | R-CI-02 | 每个制品可追溯到源码、依赖版本和构建 Job |
| R-SIZE-01 | P1 | 制品大小报告 | 压缩/解压大小、目录统计、最大文件、OCI 层报告 | R-PACK-01 | 基线可比较；超过既定增长阈值时发布需要人工确认 |
| R-E2E-01 | P0 | 故障与稳定性验收 | 进程崩溃、NFS 中断、Bridge 故障、滚动升级测试 | R-CI-04 | 业务故障边界与详细设计一致，没有静默数据损坏 |

## 12. 跨仓依赖

| Runtime 任务 | 禅道任务 | 联合产物 |
|---|---|---|
| R-ARCH-04 | Z-QUEUE-02 | Queue Bridge v1 Schema、夹具和错误码 |
| R-QUEUE-01 | Z-QUEUE-03 | 私有 Bridge 的真实端到端通信 |
| R-QUEUE-02/03 | Z-QUEUE-04/05 | 领取、心跳、ACK、重试、死信和崩溃恢复 |
| R-HEALTH-01 | Z-HEALTH-01 | Runtime 与应用分层健康状态 |
| R-ARCH-05 | Z-OBS-01 | PHP 到 Runtime 的日志/指标事件契约 |
| R-SCHED-01 | Z-SCHED-01 | 已注册 Scheduler Job 的触发与执行协议 |
| R-CI-03 | Z-REL-01 | 各版本平台无关应用制品和安装测试 |
| R-CI-04 | Z-UPGRADE-01 | 数据库迁移、应用升级和 Runtime 回滚联合测试 |

任何一方修改联合契约，必须先更新 Schema 和契约测试，再修改实现。禁止通过读取对方内部文件或数据库表绕过正式接口。

## 13. 建议的里程碑

| 里程碑 | 范围 | 可演示结果 |
|---|---|---|
| M0 契约基线 | 阶段 0 | Schema、版本清单和测试夹具可评审 |
| M1 Linux PoC | 阶段 1 | Linux amd64 运行开源版及 ionCube 探针 |
| M2 Runtime Alpha | 阶段 2 | Host、配置、健康检查、控制面和升级骨架可用 |
| M3 服务集成 | 阶段 3 | 队列、Scheduler、DuckDB/Parquet 端到端可用 |
| M4 多平台 Beta | 阶段 4 | Windows、Linux、Docker 及 Full 包可安装 |
| M5 Release Candidate | 阶段 5 | 完成版本矩阵、故障、升级和供应链验收 |

里程碑不预设日历日期。完成前一个门槛并确认未决策项后，才承诺下一个里程碑时间。

## 14. 首个 PoC 的最小范围

首个 PoC 只包含：

1. Linux amd64。
2. Caddy + FrankenPHP Library 最小 Host。
3. PHP 8.4 ZTS Classic mode。
4. Linux ionCube ABI Shim 和加密探针。
5. 禅道开源版最小安装/请求。
6. Runtime 启动、状态和优雅停止。
7. 可重复构建脚本和依赖清单。

首个 PoC 不包含 Windows、Full 包、Keepalived、完整队列、DuckDB 管理查询和正式安装器。PoC 的目标是消除 ABI 与 Library 集成风险，不是制作可发布产品。

## 15. 全局完成标准

正式版本必须同时满足：

- Windows x86_64、Linux amd64/arm64、Docker amd64/arm64 的规定矩阵全部通过。
- PHP 8.4 ZTS、ionCube 和所有内置扩展的 ABI 已验证。
- Go Runtime 没有禅道数据库 Driver、数据库凭据和业务 SQL。
- Runtime、应用、配置、数据库、附件和 Parquet 数据的升级边界清晰。
- 单机和双节点部署的健康、停止、恢复和滚动升级行为可重复验证。
- 所有发布制品带版本清单、SBOM、校验和、签名和大小报告。
- 未执行的平台或数据库测试在发布说明中明确列为限制，不以“理论兼容”替代验证。

## 16. 实施前仍需确认

1. Caddy 自动 HTTPS 的默认策略。
2. 是否允许用户加载额外 Caddy Module。
3. Windows/Linux Full 包中的 MySQL 采用 Host 子进程还是独立系统服务。
4. Scheduler 的最终进程边界。
5. Queue Worker Pool 使用标准 Go 实现还是在 POC 后采用 Watermill Core。
6. Control Plane 首版是否只提供 CLI。
7. Prometheus 指标是否对外开放及默认监听地址。
8. 正式包的体积上限和增长审查阈值。

## 17. 实施状态记录（2026-08-19）

本计划阶段 0-5 的 Runtime 侧任务已全部实现，并在 GitHub-hosted Runner 上
完成原生矩阵验证：

- Linux amd64（`ubuntu-24.04`）、Linux arm64（`ubuntu-24.04-arm`）、
  Windows x86_64（`windows-2025`）构建、冒烟测试和 Runtime/Full 包组装
  全部通过。
- DuckDB 已编入默认构建（`-tags duckdb`），事件 → Parquet → 受控查询
  全链路在本机 PoC 与真实 Runner 构建中验证。
- 日志自动记录与 JSONL → Parquet 管线（2026-08-20）：Caddy 访问日志和
  Runtime 结构化日志按节点命名（`access-<nodeID>.jsonl`、
  `runtime-<nodeID>.jsonl`），每小时整点轮转并按 `jsonlConvertInterval`
  同步为 Parquet；`convert-jsonl` 可手动触发且幂等，原始 JSONL 按
  `jsonlKeepDays`（默认 7 天）保留后清理（R-LOG-01/R-OBS-02 扩展）。
- MySQL 8.4.11 三平台归档已锁定并校验；MySQL Supervisor 用真实二进制完成
  初始化/启动/停止验证；Full 包在 Linux amd64 上完成端到端组装（约 917 MB
  压缩，含 `mysqld` 与 manifest mysql 组件）。
- 供应链：manifest、SBOM、SHA256SUMS、大小报告与 GPG 签名脚本已实现并
  本地验证；release workflow 已接入签名、校验和 provenance attestation。
- 故障验收：进程崩溃恢复、双节点滚动升级、Compose 冒烟、安装 dry-run、
  Parquet spool 运行中补发均已通过。

仍未完成且属于本计划最终交付范围的门槛：

- 正式安装器（Windows 安装程序、Linux deb/rpm）与正式代码签名、容器镜像
  多架构发布（GHCR）需要发布环境和权限。
- Windows DuckDB/Parquet：DuckDB 官方 Windows 预编译库是 MinGW GNU 归档
  （`.a`）并依赖 libstdc++，与当前 Windows 工具链（clang/lld + MSVC
  PHP）不兼容；Windows 暂只提供按节点 JSONL 日志与轮转，不启用
  observability/Parquet（R-OBS-01 Windows 链接待 MinGW 工具链迁移或
  DuckDB 提供 COFF `.lib` 后验证）。
- 禅道版本联合测试（R-CI-03/R-CI-04）依赖 `Z-REL-01` 应用制品与禅道侧
  适配实施，见 [禅道代码适配开发计划](./zentao-application-adaptation-plan.md)。
- 未执行的平台或数据库测试（例如真实付费版 ionCube 代码、双节点 NFS
  故障注入）在发布说明中明确列为限制，不以“理论兼容”替代验证。
