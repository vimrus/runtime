# 禅道 Service 层设计（定时/缓存/队列/日志/度量）

## 1. 目标与边界

把 Runtime 集成能力从业务 Module 和 framework 中抽离，形成与 `module/` 并列的
`service/` 层。

目标：

- 业务模块只调用 Service，不直接读 `ZENTAO_*` 环境变量、不直接拼接 Bridge 请求。
- 每个能力有统一的版本、超时、降级、注册表和状态语义。
- 非 Runtime 环境（Apache/PHP-FPM）仍可运行，Service 自动降级。
- Service 不替代 DAO；数据库操作仍归 DAO。

## 2. 目录结构

```text
zentaopms/
  module/                 # Web 业务 MVC
  service/                # 应用 Service 层，与 module 并列
    runtime/
      runtime.service.php # 能力检测入口
    queue/
      queue.service.php
      queue.service.interface.php
    cache/
      cache.service.php
      cache.service.interface.php
    scheduler/
      scheduler.service.php
    log/
      log.service.php
    metric/
      metric.service.php
  framework/
  lib/
```

## 3. Service 加载

在 framework 增加 `loadService()`，语义类似 `loadModel()`：

```php
$app->loadService('queue');   // service/queue/queue.service.php
$app->loadService('metric');
```

Service 内部可持有 DAO、其他 Service 或 Bridge Client，但不对业务暴露这些细节。

## 4. 分层关系

```text
业务 Controller/Model
   ↓ 只调用 Service
Service（用例编排）
   ├── DAO            # 数据库操作
   ├── CacheService   # APCu / Redis 键值缓存
   ├── QueueService   # Runtime Queue
   ├── SchedulerService
   ├── LogService
   └── MetricService
        ↓
Runtime Bridge / 本地实现
```

规则：

- DAO：只管数据库读写。
- Service：负责编排、超时、降级、状态和协议版本。
- Bridge：只做本机传输，不碰业务表。

## 5. 服务清单与失败语义

| Service | 触发方式 | 失败语义 |
|---|---|---|
| LogService | 业务/运行时内联 | 失败降级到本地日志，不阻塞 |
| CacheService | 业务内联 | 失败按 Miss 回源 |
| QueueService | 业务动作 | 失败必须让调用方知道并处理 |
| SchedulerService | 调度者 | 记录调度结果，支持重试 |
| MetricService | 定时任务触发 | 失败进入 failed 状态，支持重试/补算 |

### 5.1 Log：尽力而为

```php
try
{
    observability::log($message, $options);
}
catch(Throwable $e)
{
    error_log('runtime log failed: ' . $e->getMessage());
}
```

### 5.2 Cache：Miss 回源

```php
$value = $cache->get($key);
if($value === null)
{
    $value = $this->computeExpensiveValue();
    $cache->set($key, $value, 300);
}
```

### 5.3 Queue：持久化失败必须可见

```php
$result = $queue->push('mail.send', $payload, $options);
if(!$result->ok)
{
    // 调用方决定重试/告警/回滚
}
```

### 5.4 Metric：定时任务语义，不是内联上报

度量不掺和业务请求，由 SchedulerService 定时触发：

```text
SchedulerService
  -> MetricService::run(metricCode, period)
       -> 批量取数
       -> 计算
       -> 写 zt_metriclib
       -> 成功/失败状态
```

MetricService 结果模型：

```php
class metricRunResult
{
    public string $metricCode;
    public string $period;
    public string $status;      // success / failed
    public int    $rows;
    public string $lastError;
    public string $nextRetryTime;
}
```

失败不静默丢弃：标记 `failed`，支持重试和补算。

## 6. Interface 还是具体类

建议：

- Queue / Cache：Interface + Runtime 实现 + 本地/测试实现。
- Scheduler / Metric / Log：先具体类，需要降级或 Fake 时再拆。

原因：

- Queue/Cache 存在“Runtime 可用/不可用/测试”多种行为，适合面向接口；
- Log/Metric 语义单一（尽力而为或后台批量），具体类足够；
- 避免过度抽象，接口数量随真实需求增加。

## 7. 降级矩阵

| Service | Runtime 不可用 | 超时 | 业务影响 |
|---|---|---|---|
| Queue | 返回失败 | 不阻塞 | 调用方处理 |
| Cache | Miss 回源 | 快速返回 null | 无 |
| Scheduler | 退化为旧 cron | 不阻塞请求 | 由迁移策略控制 |
| Log | 写本地 PHP 日志 | 丢弃 | 无 |
| Metric | 任务 failed，可重试 | 任务 failed | 无业务影响 |

## 8. 与现有代码的迁移关系

| 现有位置 | 迁移目标 |
|---|---|
| queue/control、queue/model 的 Queue Bridge | 保留为协议实现；调度编排迁到 SchedulerService |
| 散落的 `ZENTAO_*` 判断 | runtimeService 能力检测 |
| 各模块直接调用 Runtime HTTP | QueueService/CacheService/MetricService |
| framework 日志/saveError | LogService |
| metriclib 计算/同步 | MetricService + SchedulerService |

## 9. 实施顺序

1. framework 增加 `loadService()`。
2. 建立 `service/` 骨架和 runtimeService 能力检测。
3. Queue/Cache Interface + 默认实现。
4. SchedulerService + MetricService（定时触发、状态、重试）。
5. LogService 接入现有错误处理。
6. 业务模块逐步迁移到 Service。

## 10. 参考

- [Runtime Control Plane v1](./contracts/runtime-control-plane-v1.md)
- [MySQL 到 Parquet 增量同步设计](./08-mysql-parquet-sync.md)（22.7 已废弃）
- [DuckDB 数据方案设计](./09-unified-duckdb-design.md)
- [BI Parquet 同步与 DuckDB 查询方案](./11-bi-parquet-query-plan.md)

## 11. 实施状态

- `service/` 目录骨架已建立，与 `module/` 并列。
- `runtimeService`：Runtime/Queue/Observability 能力检测。
- `logService` / `metricService`：已实现，Runtime 不可用时降级/丢弃。
- LogService 已接入 framework 错误处理：`router::saveError()` 写本地日志后
  同步发送结构化事件（error/warning/notice），Observability Bridge 不可用
  或失败时 logService 内部降级到本地 error_log，不改变原 debug 日志行为。
- MetricService：度量计算不再有 Parquet flush 环节（22.7 PHP 侧 DuckDB 已下线），
  metric cron handler（updateMetricLib / updateDashboardMetricLib）计算成功后直接返回
  `success`。
- `service/bi`：实现业务数据导出、`zt_bisync` 租约和 `snapshot|append|rewrite`
  运行器；BI 分析副本与实时度量链路相互独立。
- MetricService::run 已实现：`run(metricCode|period, options)` 按 code
  列表或 dateType（nodate/year/month/week/day）投递队列任务，
  `status(taskID)`/`retry(taskID)` 提供状态与重试；`metricRunResult` 作为
  对外返回模型，不新建运行状态表（状态由 zt_queue 承载）。
- metric 模块新增按需计算入口 `updateMetricLibByCodes`/
  `updateMetricLibByPeriod`：`metricZen::runCalculation()` 统一“取
  instance → 分类 → 计算 → 去重”，按 code/period 的增量任务只去重相关
  code 且跳过全表主键重建，避免并发长事务；二者已加入 cron handlers 与
  directHandlers 白名单。真实 MySQL 契约测试
  `module/metric/test/model/metricrunservice.php` 通过。
- `cacheService`：Interface + `cacheBackendInterface` 已落地；后端顺序为
  Redis（多节点共享）→ APCu（单节点，runtime 内置扩展）→ Null，最终 miss
  由业务层回源 DB，不使用文件作为业务缓存兜底。
- `queueServiceInterface` / `schedulerServiceInterface`：契约已定义，
  `queueService` 已实现（enqueue 业务入队 + DAO 状态/取消/重试，
  不依赖 cron 模块模型）；`schedulerService` 已实现（zt_cron 注册 +
  queueService 触发）；`loadService()` 已加入 baseModel。
- 缓存后端收敛：phpredis 6.3.0 与 apcu 5.1.28 均已内置到 runtime 镜像；
  原通过 HTTP 桥访问的 Go 侧 Cache Service 已删除，单机部署由 APCu
  进程间共享内存直接承载，真实 Redis e2e 通过。
- 业务入队下沉 queueService：`queueService::enqueue()`/`retry()` 直接基于
  DAO 实现白名单与幂等键校验，不依赖模块模型（调度入队 `enqueueCron`
  保留在 cron 模型并复用 queue 模块的 `insertTask()` 写入任务行；模式租约
  在 queue 模型）；真实 DB 契约测试
  （`module/queue/test/model/queueservice.php`）。
- 业务缓存服务：`cachedFacade` 基类 + namespace 版本失效；缓存内容为
  实体基础行 map（id => 行），权限 view 过滤在业务层完成。user/project/
  product 三个模块的 `getPairs()` 已按该模式接入（默认关闭），核心写路径
  后统一版本失效；真实 MySQL/Redis 跨进程命中与回源验证通过。
- MetricService 当前提供 counter/gauge 发送；文档 5.4 的定时批量取数已由
  metric cron handler 与 `MetricService::run()` 承载（无 Parquet 环节）。
- 待接入：其他业务型入队调用方；`MetricService::run` 的失败状态/重试
  完善（rows 统计与错误分类）与按业务评审的默认调度策略。
