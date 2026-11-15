# Runtime 验证与发布门槛

本文把各设计文档中的测试要求汇总为可执行的发布门槛。单个开发机成功不能替代矩阵验证；未执行的平台或数据库必须在发布说明中列明。

## 1. 门槛分层

| 层级 | 内容 | 触发时机 |
|---|---|---|
| L0 | 文档链接、Schema、版本清单、静态安全检查 | 每个 PR |
| L1 | Go 单元测试、Fake Bridge、配置/生命周期/缓存测试 | 每个 PR |
| L2 | Linux amd64 Classic/worker mode、健康、升级和容器冒烟 | 每个 PR 或合并前 |
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

- PHP 8.4 ZTS 默认 worker mode 与 Classic mode 连续请求隔离。
- Worker PDO 连接复用、健康检查、dirty 连接丢弃和事务回滚。
- `tests/e2e/zentao-worker-smoke.sh <app-package|app-dir>` 验证 worker 启动、
  ionCube、连续请求和 `worker-restart`；open/biz/max/ipd 包均纳入发布矩阵。
  合成包通过 `ZENTAO_WORKER_ENTRIES=index.php,api.php,bridge.php` 验证多入口
  worker 及重启后的总线程数。
- 短周期 soak 可设置 `ZENTAO_WORKER_REQUEST_ROUNDS`、`ZENTAO_WORKER_SOAK_ROUNDS`
  和 `ZENTAO_WORKER_RSS_LIMIT_MB`；脚本先预热，再按轮次采样容器 RSS，
  超过上限时失败。24 小时 soak 仍是发布门槛。
- Release Candidate 可用 `ZENTAO_WORKER_SOAK_SECONDS=86400` 按持续时间执行，
  `ZENTAO_WORKER_SOAK_SAMPLE_SECONDS` 控制 RSS 采样间隔；已设置
  `ZENTAO_WORKER_SOAK_ROUNDS` 时仍会按轮数提前结束。
  `ZENTAO_WORKER_SOAK_INTERVAL_SECONDS` 控制每轮请求之间的等待时间，
  便于在共享环境执行低频长周期 soak。
- 本地源码联调可设置 `ZENTAO_WORKER_DIRECT_APP=1`，把应用目录只读挂载；
  需要访问宿主数据库时再设置 `ZENTAO_WORKER_HOST_NETWORK=1` 和独立
  `ZENTAO_WORKER_PORT`。数据、临时目录、Session 和日志仍使用独立临时根目录。
- `ZENTAO_WORKER_EXTRA_PATHS` 用 `|` 分隔额外真实路由，例如登录页和未登录
  重定向页面；这些路径会纳入连续请求、soak 及 restart 后检查。
- 未认证 API/Bridge 可设置 `ZENTAO_WORKER_ALLOWED_STATUSES` 接受预期的
  400/401/403/404/405，同时仍会拒绝 500 和 worker 启动失败。
- `ZENTAO_WORKER_REQUIRE_CLEAN_LOG=1` 会检查容器日志中的 PHP fatal/parse、
  未捕获异常和入口错误；CI 合成包默认开启。
- `ZENTAO_WORKER_VERBOSE=1` 输出每个 worker entry 和额外路由的实际 HTTP 状态，
  用于发布证据归档。
- `ZENTAO_WORKER_CODE_RELOAD=1` 会在重启前修改合成应用加载的 PHP 版本文件：
  restart 前必须保持旧代码，restart 后必须加载新代码，以验证 worker 和
  OPcache 上下文确实重建。
- `ZENTAO_WORKER_HEADER_ISOLATION=1` 先让 Bridge 返回 405 + `Allow: POST`，
  再请求非法 endpoint；后续 404 不得继承 `Allow` header。
- `ZENTAO_WORKER_BENCHMARK=1` 通过 Runtime app env 启用 worker benchmark 日志，
  汇总 create/config/lang/handler 的 p50/p95 和内存，用于 WorkerRuntime Catalog
  改造前后对比。
- `tests/e2e/zentao-worker-page-matrix.sh <base-url> <cookie-jar>` 验证已登录
  worker 的 12 页面 GET 矩阵；设置 `ZENTAO_PAGE_MATRIX_BENCH_LOG` 后按页面和总体
  汇总 benchmark p50/p95。
- 设置 `ZENTAO_WORKER_CONCURRENCY` 后，脚本并发发送带唯一 probe 的请求并
  校验响应逐一对号；CI 合成包固定执行 16 路交错请求。
- 设置 `ZENTAO_WORKER_SESSION_ISOLATION=1` 后，合成包用两个独立 Cookie jar
  验证 Session 不串数据，并在 `worker-restart` 后验证 Session 持久化。
- 设置 `ZENTAO_WORKER_EXCEPTION_RECOVERY=1` 后，合成包主动触发一次未捕获
  异常，验证 Runtime 恢复 ready 后 worker 请求重新返回成功。
- `ZENTAO_MULTI_APP_WORKER=1 tests/e2e/multi-app-smoke.sh` 为根应用和子路径
  应用各启动一个 worker，验证应用 ID、文档根、数据根、子路径路由及 Session
  目录隔离；交叉读取另一个应用的 Session 必须为空。
- 设置 `ZT_TEST_PGSQL_HOST/PORT/DB/USER/PASSWORD/SCHEMA` 时执行 PostgreSQL
  连接池矩阵；未设置时该契约明确跳过。
- 设置 `ZT_TEST_MYSQL_HOST/PORT/DB/USER/PASSWORD` 时执行 MySQL 连接复用、
  字符集恢复、session 状态丢弃和事务回滚契约；未设置时明确跳过。
- 设置 `ZT_TEST_DM_*` 或 `ZT_TEST_KINGBASE_*` 时执行对应连接池矩阵。
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
| Worker mode 冒烟 | 必测 | 必测 | 按发布承诺 | 必测 |
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
