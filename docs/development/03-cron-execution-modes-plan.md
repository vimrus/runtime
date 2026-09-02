# 定时任务执行模式兼容开发计划

## 1. 文档信息

| 项目 | 内容 |
|---|---|
| 状态 | 核心实现和本地三模式联合验收完成；目标部署环境矩阵仍按交付边界补测 |
| 适用范围 | Runtime、`zentaopms` Cron、RoadRunner |
| 数据库结构 | 使用最新 `zt_queue`、`zt_queueexec`、`zt_servernode`，以现有 MySQL DAO/DDL 抽象为统一实现 |
| 主运行模式 | FrankenPHP + Runtime |
| 兼容模式 | RoadRunner、普通 PHP Web |

## 2. 目标

Runtime 是正式的定时任务执行方式。RoadRunner 是已有常驻部署的兼容方式，普通 PHP Web 是不依赖 Runtime 和 RoadRunner 时的最低依赖保底方式。

三种方式不允许同时执行定时任务，优先级固定为：

```text
Runtime > RoadRunner > Web fallback
```

三种方式都使用最新队列表结构，不恢复旧的 `wait/doing/done` 队列协议。

具体实现、测试证据和当前剩余工作见[《定时任务多运行模式开发文档》](./04-cron-execution-modes-development.md)。

## 3. 运行模式

### 3.1 Runtime 模式

当应用运行在 FrankenPHP SAPI 下，由 Runtime 负责调度和消费：

```text
Runtime Scheduler
  -> cron/schedule
  -> PHP 写入 queued 任务

Runtime Worker
  -> cron/claim
  -> cron/execute
  -> PHP 执行 Handler 并提交结果
```

Runtime 是唯一执行者。浏览器请求 `cron/ajaxExec` 必须立即返回，不能启动 PHP 常驻 scheduler 或 consumer。

### 3.2 RoadRunner 模式

RoadRunner 兼容现有两个入口：

- `zentaopms/roadrunner/scheduler.php`，调用 `cron::rrSchedule()`；
- `zentaopms/roadrunner/consumer.php`，调用 `cron::rrConsume()`。

两个入口运行在 CLI SAPI，由 RoadRunner 的 `.rr.yaml` 管理为常驻进程。它们不调用 Runtime Queue Bridge，而是在 PHP 进程内直接访问数据库队列。

RoadRunner 只在没有有效 Runtime 模式租约时允许启动。

### 3.3 Web fallback 模式

普通 PHP-FPM、Apache 或 CGI 环境通过页面访问触发 `cron/ajaxExec()`：

- AJAX 请求启动兼容 scheduler/consumer；
- scheduler 负责生成到期任务；
- consumer 负责本地领取和执行任务；
- 进程继续使用 `ignore_user_abort(true)` 和 `set_time_limit(0)`。

Web fallback 只在没有有效 Runtime 或 RoadRunner 租约时启用。

## 4. 模式互斥与租约

### 4.1 为什么不能只判断 SAPI

`PHP_SAPI` 可以识别 FrankenPHP 和 CLI，但普通 Web 请求无法通过 SAPI 判断 RoadRunner 是否正在运行。因此必须使用共享的数据库状态协调入口。

“有 Runtime”或“有 RoadRunner”定义为对应进程已经成功注册并持续刷新租约，不是指磁盘上存在二进制文件。

### 4.2 模式注册

使用最新 `zt_servernode` 的保留节点 `nodeID = '__cron_mode__'` 保存全局 Cron 模式租约。至少包含：

- `mode`：`runtime`、`roadrunner` 或 `web`；
- `nodeID`/`instanceID`；
- `heartbeatTime`；
- `leaseEnd`；
- 单调递增的 fencing version；
- `state`：`active`、`draining` 或 `stopped`。

租约更新和模式切换必须使用数据库 CAS，不能只依赖本地 PID 文件。

### 4.3 优先级规则

| 当前有效模式 | 新入口行为 |
|---|---|
| `runtime` | RoadRunner 和 Web 立即退出，不领取新任务 |
| `roadrunner` | Web 立即退出；Runtime 可以按优先级接管 |
| `web` | 没有更高模式时继续工作 |
| 无有效模式 | Web 或 RoadRunner 可竞争接管，Runtime 优先 |

Runtime 获得租约后，低优先级模式停止派发新任务并进入 drain。正在执行的任务等待当前租约结束，不强制杀死。

### 4.4 租约失效

- Runtime 停止后，等待其租约过期，不能立即降级；
- RoadRunner 停止后，等待其租约过期，Web 才能接管；
- 过期租约必须由新入口通过 CAS 清理；
- 低优先级入口不能覆盖仍有效的高优先级租约。

## 5. 统一队列状态

所有执行模式使用相同的任务状态：

```text
queued/retrying -> running -> succeeded
                              -> retrying
                              -> failed
                              -> canceled
```

本地 Web/RoadRunner Consumer 也必须使用 `version + status` CAS 抢占，并设置：

- `leaseOwner`；
- `leaseToken`；
- `leaseEnd`；
- `attempts`；
- `startedDate` 和 `finishedDate`。

每次尝试都写入 `zt_queueexec`。执行语义为 At-least-once；如果进程在业务逻辑完成但状态提交前崩溃，任务可能再次执行，Handler 必须支持幂等。

## 6. PHP 应用改造项

### 6.1 公共本地执行器

在 `zentaopms/module/cron` 中复用公共逻辑：

- `runSchedule()`：生成到期任务；
- `consumeLocalTasks()`：执行本地 Consumer 循环；
- `claimLocal()`：本地 CAS 领取；
- `finishExecute()`：提交状态和执行记录；
- `reapLocal()`：回收过期租约；
- `acquireSchedulerLease()`、`acquireConsumerLease()`：协调角色数量。

Web AJAX 和 RoadRunner 只负责调用这些公共方法，不各自维护一套队列逻辑。

### 6.2 `ajaxExec()` 分流

```text
PHP_SAPI === frankenphp
  -> 立即返回，Runtime 接管

其他 SAPI
  -> 检查 runtime/roadrunner 租约
  -> 无高优先级模式时进入 Web fallback
```

### 6.3 RoadRunner 入口恢复

恢复并适配：

- `cron::rrSchedule()`；
- `cron::rrConsume()`；
- `roadrunner/scheduler.php`；
- `roadrunner/consumer.php`；
- `roadrunner/.rr.yaml` 中 scheduler 和 consumer 服务。

`rrSchedule()` 和 `rrConsume()` 必须验证当前 RoadRunner 模式租约，不能在 Runtime 模式下继续运行。

### 6.4 Handler 安全边界

Runtime 和兼容模式使用不同的执行入口：

- Runtime 只允许注册白名单 Handler，禁止 system command；
- Web/RoadRunner 保留旧版合法 ZenTao Cron 行为；
- Web/RoadRunner 是否允许 system Cron 由 `cronSystemCall` 控制；
- 兼容逻辑不能放宽 Runtime Handler 白名单。

## 7. 实施阶段

| 阶段 | 工作内容 | 完成标志 |
|---|---|---|
| A | 冻结模式租约字段、状态和优先级 | Schema、错误码和切换规则评审通过 |
| B | 实现统一本地 Consumer | 最新队列表下可完成 claim、执行、重试和回收 |
| C | 接入 Web fallback | 无 Runtime/RoadRunner 时 AJAX 可执行任务 |
| D | 恢复 RoadRunner 入口 | `rrSchedule/rrConsume` 可由原配置启动 |
| E | 接入模式互斥 | 已完成：租约、fencing 和 Runtime 冲突停止已实现 |
| F | 完成联合测试和文档 | 本地核心联合验证、PHP-FPM 探针、Runtime scheduler 跨进程单例、RoadRunner 2.12.3 首启并发/崩溃恢复、模式过期接管和开发文档已完成；目标环境仍需按交付边界覆盖 Apache 前置代理、长时间运行和平台矩阵。信创数据库沿用 MySQL DAO/DDL 抽象，不新增专用队列 SQL |

## 8. 验收条件

### Runtime

- FrankenPHP 下 `cron/ajaxExec` 不启动 Web scheduler/consumer；
- Runtime Scheduler 可以派发到期任务；
- Runtime Worker 可以领取、执行、重试和回收任务；
- Runtime 运行时 RoadRunner 和 Web 入口均不能执行任务；
- Runtime 的 Queue Bridge 鉴权和安全限制不变。

### RoadRunner

- 原有 `scheduler.php` 和 `consumer.php` 启动方式继续有效；
- 一个 scheduler 和配置数量的 consumer 可以常驻运行；
- RoadRunner 使用最新队列表结构完成任务执行；
- Runtime 有效时 RoadRunner 启动即退出或拒绝执行；
- RoadRunner 进程异常退出后任务可被恢复。

### Web fallback

- 没有 Runtime 和 RoadRunner 时，页面 AJAX 可以启动并执行 Cron；
- 多个 Web 请求不会重复成功领取同一任务；
- scheduler 只有一个，consumer 数量不超过配置；
- Web Consumer 异常退出后任务可恢复；
- 不需要 Queue Bridge Token 或 Runtime 进程。

### 模式切换

- Web 运行时启动 RoadRunner，Web 停止派发新任务；
- Web 或 RoadRunner 运行时启动 Runtime，低优先级入口停止工作；
- 高优先级模式停止后，租约过期，低优先级模式可以重新接管；
- 切换过程中不会出现两个执行器同时成功提交同一租约；
- 任务不会永久停留在 `running`。

## 9. 测试计划

### 单元测试

- SAPI 分流和入口权限；
- 模式租约申请、续租、过期、抢占和 fencing version；
- 本地 Consumer CAS 并发领取；
- 成功、失败、重试、取消和最大尝试次数；
- 过期租约回收；
- Web/RoadRunner Handler 兼容性；
- Runtime Handler 白名单和 system command 拒绝。

### Web 集成测试

- PHP-FPM/Apache + MySQL，无 Runtime；
- AJAX 触发 scheduler 和 consumer；
- 多浏览器并发请求；
- 强制终止 Consumer 后恢复；
- 长任务续租和失败重试；
- `zt_queue`、`zt_queueexec` 最终状态正确。

### RoadRunner 集成测试

- 使用原有 `.rr.yaml` 启动 scheduler；
- 启动 4 个 consumer 并行执行；
- scheduler/consumer 重启；
- consumer 崩溃后的租约回收；
- RoadRunner 与 Web 同时尝试启动时的互斥；
- Runtime 启动后 RoadRunner 停止派发。

### Runtime 回归测试

- FrankenPHP Classic mode 请求；
- Runtime Scheduler/Worker 完整队列链路；
- Bridge 鉴权失败和 Schema 不兼容；
- Worker 心跳、超时、重试、取消和优雅停止；
- Runtime 与兼容入口切换期间的 fencing 验证。

### 故障测试

- 执行中强制终止 Web Consumer、RoadRunner Consumer 和 Runtime Worker；
- 数据库短暂不可用；
- Handler 抛出异常；
- 租约续期失败；
- Runtime、RoadRunner 先后启动和停止；
- 多节点 Runtime 与单节点兼容模式竞争。

## 10. 完成定义

本计划只有在以下条件全部满足后完成：

1. 三种入口均使用最新数据库结构。
2. Runtime 是默认且唯一的正式执行路径。
3. RoadRunner 的 `rrSchedule/rrConsume` 兼容方式可运行。
4. Web 是无 Runtime、无 RoadRunner 时的保底方式。
5. 任意时刻只有一个有效执行模式。
6. 任务具备 CAS、防重复领取、租约、重试和异常恢复能力。
7. Runtime、RoadRunner、Web 三套测试矩阵全部通过。
8. 未验证的平台或部署组合在交付记录中明确列出；信创数据库专用队列适配不作为新增实现项。

## 11. 当前交付边界

本计划对应的代码改造已完成，当前工作区用于验证，尚未提交或 push。已确认的实现包括：

- Runtime、RoadRunner、Web 使用同一套新队列表和本地 CAS 语义；
- 执行模式通过 `__cron_mode__` 租约和 fencing 版本互斥，优先级为 Runtime > RoadRunner > Web；
- RoadRunner 继续支持 `rrSchedule()`、`rrConsume()`，并由仓库现有的 `.rr.yaml` 管理 scheduler 和 consumer；
- Runtime 检测到模式冲突后停止调度、领取和回收，不降级到其他入口；
- PostgreSQL、达梦、金仓等数据库沿用现有 MySQL DAO/DDL 抽象，不新增队列专用方言 SQL；这是已确认的数据库适配边界。

已通过的自动化检查和 MariaDB、Web、Runtime scheduler 跨进程、RoadRunner、Runtime 联调用例记录在[定时任务多运行模式开发文档](./04-cron-execution-modes-development.md)中。未覆盖的平台或部署组合仍需在发布记录中单独列出。
