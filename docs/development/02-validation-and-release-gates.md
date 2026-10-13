# Runtime 验证与发布门槛

本文把各设计文档中的测试要求汇总为可执行的发布门槛。单个开发机成功不能替代矩阵验证；未执行的平台或数据库必须在发布说明中列明。

## 1. 门槛分层

| 层级 | 内容 | 触发时机 |
|---|---|---|
| L0 | 文档链接、Schema、版本清单、静态安全检查 | 每个 PR |
| L1 | Go 单元测试、Fake Bridge、配置/生命周期/缓存测试 | 每个 PR |
| L2 | Linux amd64 Classic mode、健康、升级和容器冒烟 | 每个 PR 或合并前 |
| L3 | Linux arm64、Windows x86_64 原生构建和服务测试 | 合并与候选版本 |
| L4 | MySQL、PostgreSQL、信创数据库 PHP DAO 契约测试 | 队列/应用适配变更 |
| L5 | 真实禅道各版本、ionCube、Session、附件和队列联合测试 | Release Candidate |
| L6 | 双节点故障、NFS/Redis 故障、滚动升级和恢复 | Release Candidate |

## 2. 必测行为

### Runtime 基础

- 配置 schema、未知字段、路径权限和端口冲突。
- 启动、reload、排空、停止、重复启动和进程崩溃恢复。
- Unix Socket/Named Pipe 鉴权、审计和公共网络不可达。
- liveness/readiness/deep health 的状态归属。
- Runtime、应用、配置和数据目录分离，升级失败回滚不覆盖用户数据。

### PHP 和 Web

- PHP 8.4 ZTS Classic mode 连续请求隔离。
- 静态文件、PATH_INFO、上传、下载、WebSocket 和大请求限制。
- ionCube 明文/加密探针和 OPcache 组合。
- PHP 错误、Caddy 错误和访问日志脱敏。

### 队列

- 多 Worker/多节点 Claim 竞争和 Portable CAS。
- Heartbeat、租约过期、fencing token、迟到 ACK 和 Reaper。
- Retry、退避、死信、取消、超时和优雅排空。
- Handler 幂等指导和业务事务内入队。

### 缓存（APCu/Redis）

- `get/set/delete`、空值命中、TTL、过期和 namespace 隔离。
- 进程内 L0 与 APCu 后端的边界。
- 容量上限、淘汰、慢请求和进程重启后的缓存丢失。
- 后端不可用时业务回源，且不连接禅道数据库代替缓存。
- 多节点缓存独立失效不会造成业务错误。

### DuckDB/Parquet

- 批次幂等、临时文件校验、同文件系统原子 rename 和崩溃恢复。
- NFS 不可用时有界 spool，恢复后补发。
- 查询模板、资源限制、保留清理和任意 SQL 拒绝。

## 3. 平台和部署矩阵

| 场景 | Windows x64 | Linux amd64 | Linux arm64 | Docker amd64/arm64 |
|---|---:|---:|---:|---:|
| Runtime 包 | 必测 | 必测 | 必测 | - |
| Full 包/MySQL | 必测 | 必测 | 必测 | 独立 MySQL 容器 |
| Classic mode 冒烟 | 必测 | 必测 | 必测 | 必测 |
| 双节点 HA | 首版不承诺 | 必测 | 按基础设施验证 | 外部编排 |

## 4. 发布阻断条件

- 业务数据库、队列或 PHP Bridge 契约测试失败。
- Go Runtime 试图连接禅道业务数据库或制品包含数据库 Driver。
- 缓存或观测故障能够阻断业务请求，或缓存被错误当作强一致数据源。
- 升级失败后无法恢复旧应用、配置或数据目录。
- 真实付费版 ionCube、应用包或双节点共享后端未验证却被宣传为已支持。
- 制品缺少版本清单、SBOM、校验和、签名或来源信息。

## 5. 发布证据

每个候选版本必须保存：构建 manifest、依赖锁定版本、SBOM、校验和、签名、测试矩阵、未验证项、已知限制、升级/回滚记录和故障注入结果。
