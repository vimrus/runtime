# ZenTao Runtime

ZenTao Runtime 是禅道新一代集成运行环境。项目计划将 Caddy 和 FrankenPHP 作为 Go Library 嵌入自研 `zentao-runtime`，使用 PHP 8.4 ZTS Classic mode 运行禅道，并面向 Windows、Linux 和 Docker 提供一致的安装、运行和构建能力。

当前仓库已实现 runtime-development-plan 的阶段 0-5 全部 Runtime 侧任务，并已在 GitHub-hosted Runner 上通过 Linux amd64、Linux arm64、Windows x86_64 原生构建矩阵（含 ionCube 与 DuckDB）。禅道业务适配按 [禅道代码适配开发计划](./docs/zentao-application-adaptation-plan.md) 实施，当前只读定位了改动点，尚未修改禅道代码。

## 已确认的技术边界

- Caddy 和 FrankenPHP 均作为 Go Library 集成。
- FrankenPHP 使用 Classic mode，不使用 Worker mode。
- PHP 基线为 PHP 8.4 ZTS，付费版本支持 ionCube Loader。
- Linux PHP 使用最小 `zend_signal` ABI Shim 兼容 ionCube。
- Go Runtime 不直接连接禅道数据库，数据库访问由 PHP DAO 负责。
- 消息队列使用数据库持久化、PHP Queue Service 和 Go Worker Pool。
- 缓存使用请求内 Array L0 和独立数据库连接显式 `get/set/delete`，不引入默认外部缓存服务。
- Session 遵循 PHP `php.ini`：单机默认使用本地文件，多节点由部署方配置共享 NFS 文件或外部 Redis；附件在多节点下使用 NFS。
- DuckDB 作为 Go Library 生成和查询指标/日志 Parquet；多节点共享 NFS Parquet 数据集，每个节点只发布自身不可变 part 文件。
- 双 Linux 节点可使用 Keepalived 浮动 VIP 和内嵌 Caddy Gateway；已有外部负载均衡时优先接入。
- GitHub Actions 使用目标平台原生 Runner 构建 Windows、Linux 和 Docker 交付物。

## 设计文档

- [zentao 集成环境开发计划](./docs/runtime-development-plan.md)
- [禅道代码适配开发计划](./docs/zentao-application-adaptation-plan.md)
- [FrankenPHP 集成环境技术方案](./docs/frankenphp-integration-technical-plan.md)
- [Runtime Host 详细设计](./docs/runtime-host-library-design.md)
- [GitHub Actions 构建与打包详细设计](./docs/github-actions-build-design.md)
- [消息队列详细设计](./docs/message-queue-design.md)
- [数据库显式缓存详细设计](./docs/database-cache-design.md)
- [多节点部署与高可用详细设计](./docs/deployment-and-ha-design.md)
- [DuckDB 与共享 Parquet 可观测性详细设计](./docs/duckdb-parquet-observability-design.md)
- [Runtime Alpha 配置与 Control Plane 契约](./docs/runtime-alpha-control-contract.md)

## 本地构建与验证

本地构建需要 Docker、BuildKit 和 `jq`：

```bash
scripts/poc/build-linux-amd64.sh
scripts/poc/test-linux-amd64.sh
```

中国大陆网络环境下构建脚本默认使用 `https://goproxy.cn,direct`。可通过环境变量覆盖，例如：

```bash
GOPROXY=https://proxy.golang.org,direct scripts/poc/build-linux-amd64.sh
```

本地构建默认使用 USTC Debian 镜像源；可以通过 `DEBIAN_MIRROR` 和 `DEBIAN_SECURITY_MIRROR` 覆盖。GitHub Actions 明确使用 Debian 官方源。

如需额外验证本机 PHP 8.4 加密版 IPD 代码，显式传入源码目录：

```bash
ZENTAO_POC_APP_DIR=/home/z/zentaoipd scripts/poc/test-linux-amd64.sh
```

IPD 付费代码不会复制到镜像、测试夹具、构建制品或公开仓库。GitHub-hosted Runner 只验证公开 PHP 夹具和 ionCube Loader 加载；真实加密代码测试应在有权访问该源码的本地或私有 Runner 上执行。

PoC 运行镜像使用锁定 digest 的 Debian bookworm-slim，并在编译后删除 `phpdbg`、PHP 头文件及扩展构建工具。启用 DuckDB 后，Linux amd64 解压制品约 `233 MB`（`zentao-runtime` 约 `109 MB`）；构建和测试会断言这些开发文件未进入交付物。

Runtime Alpha 已增加版本化 `runtime.json`、Linux Unix Socket Control Plane、生命周期状态、分层健康检查、结构化日志/审计、升级事务、Queue Engine、DuckDB/Parquet 可观测性和诊断 CLI。配置样例在 [config/runtime.example.json](./config/runtime.example.json)，详细协议见 [Runtime Alpha 契约](./docs/runtime-alpha-control-contract.md)。

## 已实现能力

- 版本锁定清单 `versions.lock.json`：PHP 8.4.24、FrankenPHP v1.12.7、Caddy v2.11.4、DuckDB 1.5.5/Go Binding、phpredis 6.3.0、ionCube（Linux amd64/arm64、Windows x64）、MySQL 8.4.11 三平台归档（含校验和）与许可证。
- 本地 Control Plane：`status/start/stop/reload/health/diagnose/version`，Linux Unix Socket `0600` + `SO_PEERCRED` 鉴权，审计日志 JSON Lines；Windows Named Pipe ACL 与 `run-service` 入口。
- CLI 扩展：`upgrade prepare/apply/rollback/status`、`logs/metrics`、`flush-observability/clean-observability`、`collect-logs` 诊断包、`php-cli`（与 Web 共用 PHP/ionCube）。
- 分层健康检查：`/_runtime/liveness|readiness` 动态反映 Host 状态，`health --deep` 返回 runtime/php/app 组件结果。
- 结构化日志：`slog` JSON、敏感字段脱敏、大小轮转；Caddy access log 可写入独立文件。
- 升级事务：Runtime/Application/Config/Data 目录分离，`app/current` 指针原子切换、备份与回滚状态机。
- Queue Engine：PHP Queue Bridge 客户端（loopback-only + 随机凭据）、有界 Worker Pool（队列级并发、超时、取消、排空）、租约心跳与 fencing、Wakeup + 自适应轮询、Scheduler 注册表。
- 可观测性：DuckDB Go Library 已编入默认 Linux 构建；事件信封与脱敏、节点分区 Batch/Spool/原子发布 Parquet、运行中自动补发、崩溃恢复、受控查询模板与节点自清理；CLI 提供 `logs`、`metrics`、`flush-observability`、`clean-observability`、`collect-logs`。
- 平台与打包：systemd unit 与安装/卸载脚本、logrotate、Keepalived + Caddy Gateway 模板、Docker Compose（web/scheduler/mysql）、Runtime/Full 包组装、manifest/SBOM/校验和/大小报告；MySQL Supervisor 已用真实 MySQL 8.4.11 二进制完成初始化/启动/优雅停止验证。
- 安全边界：构建产物校验不嵌入任何数据库 Driver（`verify-no-db-driver.sh`）；Windows 子进程挂入 Job Object（KILL_ON_JOB_CLOSE），Host 异常退出不会遗留孤儿进程。
- 供应链：`sign-artifacts.sh` / `verify-signature.sh` / `verify-supply-chain.sh` 实现 GPG 签名与端到端校验（checksum + manifest + SBOM + 大小报告 + 签名），release workflow 已接入；受保护 release 另含 provenance attestation 与 release draft。
- CI：PR lint/单测/文档检查、Linux amd64/arm64 原生构建矩阵、Windows x64 构建脚本、受保护 release workflow。
- 验收测试：进程崩溃恢复、双节点滚动升级（A 保持 v1 服务期间升级 B，再升级 A）、Docker Compose 冒烟（healthcheck + Classic PHP）、安装 dry-run、升级事务 prepare/apply/rollback、应用版本合并测试均已通过。
- 禅道应用制品接入：由外部发布流程提供平台无关应用包（`www/` 根目录），runtime 通过 `scripts/ci/stage-app-package.sh` 解包到 `app/releases/<version>` 并切换 `app/current`，`tests/e2e/zentao-app-smoke.sh` 负责联合冒烟。
- 应用包约定目录：`/home/z/dev/app-packages/{opensource,biz,max,ipd}/zentao-app.zip`（可被 `ZENTAO_APP_PACKAGES_DIR` 覆盖），查找用 `scripts/ci/find-app-package.sh <edition>`。

## 原生构建矩阵（已验证）

`native-builds.yml` 在 GitHub-hosted Runner 上完整通过：

| 平台 | Runner | 结果 |
|---|---|---|
| Linux amd64 | `ubuntu-24.04` | 构建 + 冒烟 + Runtime/Full 包 ✓ |
| Linux arm64 | `ubuntu-24.04-arm` | 构建 + 冒烟 + Runtime/Full 包 ✓ |
| Windows x86_64 | `windows-2025` | 构建 + ionCube 校验 + Runtime/Full 包 ✓ |

本轮产物（GitHub Actions artifacts）：`zentao-runtime-linux-amd64`（约 67.5 MB）、`zentao-runtime-linux-arm64`（约 62.9 MB）、`zentao-runtime-windows-x64`（约 33.1 MB），以及对应的三个 Full 包（约 914.8 / 902.2 / 187.4 MB）。每个包均含 manifest、SBOM、SHA256 与大小报告。

## 尚未完成

- 禅道业务适配（队列 PHP Service、缓存 Client、Session 共享、可观测性事件发送）按 [禅道代码适配开发计划](./docs/zentao-application-adaptation-plan.md) 实施；改动点已定位到第 18 节。应用制品由外部流程提供，runtime 侧接入工具已就绪，待提供真实包后执行安装/登录/队列联合测试。
- 正式安装器（Windows Inno Setup/WiX、Linux deb/rpm）、正式代码签名、Docker 多架构镜像推送（GHCR）与 MySQL/ionCube 再分发法务确认需要发布环境和权限。
