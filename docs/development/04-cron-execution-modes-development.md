# 定时任务多运行模式开发文档

> **22.7 变更**：RoadRunner 支持已整体移除（`framework/zand/`、`roadrunner/`、
> `bin/roadrunner/`、`www/worker.php`、`www/cron.php`、`www/.rr.yaml`），
> `cron::rrSchedule()/rrConsume()` 入口随之删除，定时任务只保留 Runtime 与
> Web fallback 两种执行模式，优先级为 `Runtime > Web fallback`。本文中与
> RoadRunner 相关的入口、验收证据和待办仅作历史记录。

## 1. 文档范围

本文是 Runtime、禅道 PHP 应用和 RoadRunner 定时任务功能的开发与验收说明，面向代码开发、联调和测试人员。它不描述安装命令和生产运维步骤；部署参数请参阅[配置与安全](../configuration-and-security.md)、[编译与打包](../build-and-package.md)和[多节点部署指南](../deployment/multi-node.md)。

三种执行模式共用最新的 `zt_queue`、`zt_queueexec`、`zt_servernode` 结构和同一套任务状态机，优先级固定为：

```text
Runtime > RoadRunner > Web fallback
```

Runtime 是正式运行路径，RoadRunner 是已有常驻部署的兼容路径，普通 Web 请求是无前两者时的最低依赖保底路径。

## 2. 入口与调用链

| 模式 | 入口 | 调度 | 消费 | 启用条件 |
|---|---|---|---|---|
| Runtime | FrankenPHP 内部 Queue Bridge | Runtime 调用 `schedule`，PHP 生成到期任务 | Runtime Worker 调用 `claim`、`execute` | 取得有效 `runtime` 模式租约 |
| RoadRunner | `zentaopms/roadrunner/scheduler.php`、`consumer.php` | `cron::rrSchedule()` | `cron::rrConsume()`，PHP 消费循环 | 取得有效 `roadrunner` 租约，且没有有效 Runtime 租约 |
| Web fallback | `cron::ajaxExec()` | AJAX 进程中的 scheduler 角色 | 同一进程中的 PHP 消费循环 | 没有有效 Runtime 和 RoadRunner 租约 |

启动决策如下：

```text
FrankenPHP SAPI -> 申请 runtime 租约 -> 成功则 Runtime 运行；失败则停止，不降级
RoadRunner CLI  -> 申请 roadrunner 租约 -> 冲突则立即退出
普通 Web AJAX   -> 检查 runtime/roadrunner -> 均无效才申请 web 租约
```

判断“有 Runtime/RoadRunner”只看数据库租约是否有效，不看二进制、配置文件或进程文件是否存在。

## 3. 模式租约与 fencing

`zt_servernode` 中 `nodeID = '__cron_mode__'` 是全局模式记录。关键字段如下：

| 字段 | 作用 |
|---|---|
| `role` | `runtime`、`roadrunner` 或 `web` |
| `state` | 当前为 `active`；停止时通过租约过期失效 |
| `leaderEnd` | 租约截止时间 |
| `heartbeatTime` | 最近一次续租时间 |
| `version` | 模式代次，用于 fencing |
| `tokenHash` | 当前持有者标识的哈希 |

`nodeID = '__runtime_scheduler__'` 是另一个唯一的保留行，仅保存 Runtime scheduler
leader lease。它不代表实际 Runtime 节点，`tokenHash` 保存当前 leader `nodeID` 的哈希。
Runtime 通过该行的条件更新（或首次插入）竞争 scheduler；不能将 leader 写在各自节点行上，
否则两个节点可能各自观察到“无 leader”并同时成功更新不同的行。

模式申请、续租和接管都必须在数据库事务内完成，并用版本、角色和状态做 CAS：

1. 无有效租约时可以申请模式。
2. 同一模式续租不增加 `version`。
3. 有效的高优先级模式不能被低优先级模式覆盖。
4. 高优先级模式可接管已过期或更低优先级的租约，并增加 `version`。
5. 旧进程退出时不删除租约；新进程在租约过期后通过 CAS 接管。

领取、心跳、回收和完成提交前都要再次校验模式租约。校验失败统一返回 `execution_mode_conflict`，迟到请求不能领取任务或覆盖新模式的结果。

## 4. 队列状态机与执行语义

```text
queued/retrying -> running -> succeeded
                             -> retrying
                             -> failed
                             -> canceled
```

Web/RoadRunner 消费循环和 Runtime Bridge 必须遵守相同的 CAS 语义。领取时检查原 `version`、状态和 `availableDate`，成功后写入 `leaseOwner`、`leaseToken`、`leaseEnd`、`heartbeatTime`、`attempts`、`startedDate` 和新 `version`，并向 `zt_queueexec` 写入一次执行记录。

任务语义为 At-least-once：进程可能在 Handler 已执行但结果尚未提交时退出，因此业务 Handler 必须幂等。Reaper 将过期的 `running` 任务改回 `retrying`；达到 `maxAttempts` 后改为 `failed`，不能永久停留在 `running`。

调度写入 `cron` 字段（来源规则 ID），`handler = 'cron'` 为执行方式标记（不是 handler 名），payload 保存任务类型和 command，幂等键为 `cron:<cronID>:<scheduledAt>`，避免 scheduler 重复入队。

## 5. PHP 应用改造边界

实现按域拆分为 `zentaopms/module/cron`（定时任务系统：调度扫描、调度入队、scheduler 单例租约）与 `zentaopms/module/queue`（队列实现：状态机原语、模式租约、Bridge 协议层、消费循环），三种入口只负责选择模式和驱动公共逻辑。模块依赖 cron→queue 单向；反向依赖只有两处：消费面是 Bridge `schedule` 端点回调 `cronModel::scheduleRuntime()`，展示面是管理界面 browse 的只读规则名查询 `loadModel('cron')->getById()`：

| 方法 | 所属模块 | 职责 |
|---|---|---|
| `acquireExecutionMode()` | queue | 申请、续租和按优先级接管模式 |
| `executionModeActive()` | queue | 检查模式租约 |
| `fenceExecutionMode()` | queue | 在队列事务中锁定并校验模式（cron 调度入队侧调用） |
| `acquireConsumerLease()` | queue | 限制 consumer 数量 |
| `acquireSchedulerLease()` | cron | 保证 Web/RoadRunner scheduler 单例 |
| `acquireRuntimeSchedulerLease()` | cron | 保证 Runtime scheduler 跨进程单例（`__runtime_scheduler__` 保留行） |
| `runSchedule()` | cron | 调度扫描，到期规则经 `enqueueCron()` 入队 |
| `enqueueCron()` | cron | 调度入队事务（幂等键与 `lastTime` 推进），任务行写入复用 queue 的 `insertTask()` |
| `scheduleRuntime()` | cron | Bridge schedule 端点的回调：竞争 leader 并执行调度扫描 |
| `insertTask()` | queue | 通用任务行写入原语 |
| `consume()` | queue | Web/RoadRunner 共用消费循环（cron 兼容入口经 fetch 调用） |
| `claimNext()` | queue | 消费循环的单条任务 CAS 领取 |
| `reap()` | queue | 回收过期任务 |
| `finishExecute()` | queue | 写入成功、重试、失败及执行记录 |

### 5.1 代码落点

| 文件 | 修改边界 |
|---|---|
| `zentaopms/module/cron/control.php` | 定时任务管理页与 `ajaxExec`/`rrSchedule`/`rrConsume` 兼容入口；不持有队列消费逻辑 |
| `zentaopms/module/cron/model.php` | 调度扫描（`runSchedule`）、调度入队事务（`enqueueCron`）、scheduler 单例租约、`scheduleRuntime` 回调 |
| `zentaopms/module/cron/config.php` | 运行上限（`maxRunDays/maxRunTime`）配置 |
| `zentaopms/module/queue/control.php` | 队列管理界面（browse/cancel/retry）、清理与指标运维入口、Queue Bridge 八端点、`consume` 消费循环 |
| `zentaopms/module/queue/model.php` | CAS 领取、心跳、执行结果、回收、通道控制、`insertTask` 与模式租约等队列状态机原语 |
| `zentaopms/module/queue/config.php` | 队列批量、租约、重试、consumer 上限（`maxConsumer`）和 Handler 白名单配置 |
| `zentaopms/roadrunner/scheduler.php` | RoadRunner scheduler 的最小启动入口 |
| `zentaopms/roadrunner/consumer.php` | RoadRunner consumer 的最小启动入口 |
| `zentaopms/roadrunner/.rr.yaml` | scheduler 单例、consumer 数量、重启和执行超时策略 |
| `zentaopms/db/zentao.sql`、`zentaopms/db/update22.6.sql` | 新安装和升级所需的队列表结构 |

`runtime` 侧只实现 Queue Bridge 客户端、调度器和 Worker；业务 Handler、数据库 DAO 和
权限判断仍归 `zentaopms`。`runtime/dist/zentaopms` 仅用于本地 Runtime 制品验证，不作为
应用源码改动入口。

### Web fallback

`ajaxExec()` 在 `PHP_SAPI === 'frankenphp'` 时立即返回，由 Runtime 接管。其他 SAPI 先检查 Runtime 和 RoadRunner 租约，再经 `queueModel::acquireExecutionMode()` 申请 `web` 租约。取得租约后，scheduler 由 `acquireSchedulerLease()` 保证单例，consumer 由 `queueModel::acquireConsumerLease()` 限制并发数；每轮由 `runSchedule()` 调度、`queue::consume()` 消费、`queueModel::reap()` 回收过期任务并续租，失去模式租约后退出。`ajaxExec` 与 `rrSchedule`/`rrConsume` 是历史遗留的兼容入口：常规路径是 cron 调度生成队列消息后，由队列模块自行消费。

### RoadRunner

`rrSchedule()` 和 `rrConsume()` 只接受 CLI SAPI，分别由 `scheduler.php` 和 `consumer.php` 加载。两者申请并续租 `roadrunner` 模式；Runtime 接管、租约失效或申请失败时退出。`.rr.yaml` 使用单 scheduler、默认 4 个 consumer 和无限执行超时。

### Handler 权限

Runtime 对 zentao 型 cron command 不做白名单校验，与 Web/RoadRunner 的 `runLegacyHandler()` 行为一致；direct handler 仍必须命中 `directHandlers`。Runtime 一律拒绝 system command；Web/RoadRunner 是否允许 system Cron 仍由 `cronSystemCall` 控制。

## 6. Runtime Queue Bridge 链路

Runtime 启动 Queue Engine 后按以下顺序调用 PHP Bridge：

1. `capabilities`：协商 schema/能力并申请 Runtime 模式租约。
2. `schedule`：竞争短时 scheduler leader，调用 PHP DAO 入队。
3. `claim`：Worker 批量领取任务。
4. `execute`：PHP 校验 lease token 并执行白名单 Handler。
5. `heartbeat`：Worker 延长运行中任务的租约。
6. `reap`：回收过期任务。

`schedule`、`heartbeat` 或 `reap` 返回 `execution_mode_conflict` 时，Runtime 发送 fatal signal，停止调度、领取和回收，不回退到 RoadRunner 或 Web。Bridge 字段、错误码、鉴权和大小限制以[Queue Bridge v1 契约](../design/contracts/queue-bridge-v1.md)为准。

`schedule` 端点只有在 PHP 实际完成调度扫描后才返回 `scheduled=true`；如果在租约校验或物化队列阶段被 fencing，则返回失败状态，Runtime 会在下一轮重试。

## 7. 数据库与配置约定

队列相关配置位于 `zentaopms/module/queue/config.php`，包括批量大小、租约/心跳周期、最大尝试次数、重试退避、consumer 上限（`maxConsumer`）和 Handler 白名单；cron 运行上限（`maxRunDays/maxRunTime`）位于 `zentaopms/module/cron/config.php`。Runtime 侧 Bridge 地址和 Worker 参数位于 `runtime.toml`。

新安装和升级必须提供：

- `zt_queue`：任务状态、版本和租约；
- `zt_queueexec`：每次尝试的执行记录；
- `zt_servernode`：节点、`__cron_mode__` 模式租约和 `__runtime_scheduler__` 单例 scheduler 租约。

队列 DAO/DDL 以现有 MySQL 抽象为唯一实现基线。PostgreSQL、达梦、金仓等数据库沿用公共 DAO、迁移和数据库函数抽象，不新增队列专用 SQL 分支；通过公共数据库契约测试检查方言回归。

## 8. 开发顺序

1. 确认三张表、保留模式节点、字段精度和索引。
2. 实现并测试模式租约、优先级、CAS 和 fencing。
3. 实现消费循环的领取、执行、重试、心跳和回收。
4. 接入 Web fallback，验证 AJAX 并发与异常恢复。
5. 接入 RoadRunner 入口和 `.rr.yaml`，验证常驻进程行为。
6. 接入 Runtime Bridge，验证冲突停止和不降级规则。
7. 执行跨进程、跨模式、故障和数据库契约测试，更新本文件的状态。

新增入口必须先接入模式租约和 fencing，再接入任务执行；不得复制一套独立队列协议。

## 9. 测试与验收

### 自动化检查

```text
# 在 runtime 仓库根目录执行 Go 检查
go test -race ./internal/queue/...
go vet ./internal/queue/... ./internal/mysql
./scripts/ci/test-runtime-host.sh   # 使用 nowatcher 构建标签的 Host 检查

# 在 zentaopms 仓库根目录执行 PHP 检查
php -l zentaopms/module/cron/control.php
php -l zentaopms/module/cron/model.php
php -l zentaopms/module/queue/control.php
php -l zentaopms/module/queue/model.php
php zentaopms/module/queue/test/model/executionmode.php
php zentaopms/module/queue/test/control/consume.php
php zentaopms/module/cron/test/model/runtimescheduler.php
php zentaopms/module/queue/test/model/queueservice.php
php zentaopms/module/queue/test/model/queueadmin.php

# 分别检查两个工作区的补丁格式
git -C runtime diff --check
git -C zentaopms diff --check
```

### 必测场景

| 类别 | 验收条件 |
|---|---|
| Runtime | FrankenPHP 请求不启动 Web 逻辑；Runtime 可调度、领取、执行、心跳、重试和回收 |
| RoadRunner | 原有两个 PHP 入口可由 `.rr.yaml` 启动；scheduler 单例、consumer 数量受限；进程退出后任务可恢复 |
| Web | 无 Runtime/RoadRunner 时 AJAX 可执行；多请求不会重复成功领取；异常退出后任务可恢复 |
| 互斥 | Runtime 有效时 RoadRunner/Web 不领取；RoadRunner 有效时 Web 不领取 |
| 接管 | 高优先级模式停止并租约过期后，低优先级模式可 CAS 接管 |
| fencing | 迟到 ACK、心跳和取消不能覆盖新 attempt；任务不会永久停留在 `running` |
| 故障 | 数据库短暂不可用、Handler 异常、心跳失败、强制终止和优雅停止都有明确结果 |

### 当前状态

已具备代码和证据：模式租约与优先级、消费循环 CAS、scheduler/consumer 租约、Runtime Bridge、Web `cli-server` 探针、PHP-FPM FastCGI 探针、MariaDB 队列状态流转、Go queue race、心跳传输失败处理、`nowatcher` Host 检查和 PHP lint。PHP 模型测试还覆盖了无幂等键任务的并行入队、任务超时租约、过期 ACK/Heartbeat 拒绝、消费循环重试/回收、RoadRunner 租约过期后 Web 接管，以及 Runtime scheduler 跨进程单例；调度端点的第二阶段物化结果也由 `runSchedule()` 的布尔返回值约束。旧 `wait/doing/done` 队列在独立迁移库中已验证为 `queued/retrying/succeeded/canceled`，`doing` 任务记录 `legacy_lease_lost`，删除标记映射为 `canceled`，旧表迁移后删除，并确认保留两个 `zt_servernode` 节点。两个并发 FPM 请求最终只有一个 scheduler 租约，consumer 租约受上限限制。RoadRunner 2.12.3 supervisor 已用仓库 `.rr.yaml` 验证：1 个 scheduler 和 4 个 consumer 首启并发申请 `__cron_mode__` 时无唯一键异常；有效 Runtime 租约存在时，RoadRunner 不创建 scheduler/consumer 角色记录；空闲 consumer 被强制终止后自动补回；执行中的 consumer 被终止后，任务先记为 `lost`，再由其他 consumer 重试并成功。该竞态修复通过回归测试覆盖：并发首次插入失败会回滚并重新竞争，而不是让 PHP 请求致命退出。另验证了全新安装首次启动时删除 `guest` 用户视图的场景，4 个 RoadRunner consumer 并发初始化均保持运行；`computeUserView()` 对首次唯一行插入使用公司行锁和事务内二次读取。

本机执行完整 `go build ./cmd/zentao-runtime` 仍受环境缺少 `wtr/watcher-c.h` 阻断；该依赖问题不属于 Cron PHP/队列逻辑，不能用关闭 CGO 代替正式 Runtime 构建验收。

本地开发验收已完成。发布前仍必须在目标环境补齐：

- Apache 前置代理下的 PHP-FPM 多请求 Web fallback；当前已完成独立 PHP-FPM FastCGI 验证；
- 使用固定 RoadRunner 版本和仓库 `.rr.yaml` 的 supervisor 长时间运行、滚动重启及多节点恢复；本地已完成首启并发、单节点补进程和任务重试验证；
- Runtime、RoadRunner、Web 的完整启动/停止/租约过期接管矩阵；
- 数据库短暂不可用、Handler 异常、心跳失败和优雅停止故障注入；
- 发布矩阵中未覆盖的平台和数据库契约回归。

只有上述真实集成和故障测试全部通过，才可将开发项标记为发布完成。当前工作区保持未提交、未 push。
