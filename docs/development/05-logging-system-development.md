# Runtime 日志系统开发文档

## 1. 文档信息

| 项目 | 内容 |
|---|---|
| 文档定位 | Runtime 日志系统的实现、扩展、联调和验收指南 |
| 责任仓库 | `vimrus/runtime` |
| Runtime 程序 | `zentao-runtime`（用户命令通常为 `zentao`） |
| 当前状态 | Runtime/Caddy 日志链路与 PHP 事件接入已实现；真实多节点故障验证待完成 |
| Schema | 可观测性事件 Schema v1 |
| 相关设计 | [可观测性设计](../design/05-observability.md) |
| 相关契约 | [Runtime Control Plane v1](../design/contracts/runtime-control-plane-v1.md) |

本文件描述如何在现有 Runtime 日志系统上开发和验证功能。设计原则、部署拓扑、Parquet 目录契约和跨节点约束以相关设计文档为准；本文不替代这些规范。

## 2. 目标和边界

### 2.1 目标

日志系统需要同时满足以下目标：

1. Runtime、HTTP 入口、PHP 错误和管理操作职责分离，单类日志故障不应阻断业务请求。
2. 每个节点只能追加自己的原始日志文件，避免多节点在共享文件系统上并发追加同一文件。
3. 日志先以可直接排查的 JSONL 保存，再异步归档到 Parquet，支持按时间和节点查询。
4. 敏感信息在进入缓冲区或持久化文件前脱敏，而不是依赖查询时过滤。
5. NFS、DuckDB、Parquet 发布失败时保留本地证据，并在恢复后幂等补发。
6. 所有管理查询和日志收集均通过受保护的本地 Control Plane 完成。

### 2.2 不属于 Runtime 日志系统的职责

- Runtime 不直接访问禅道业务数据库，也不解析业务数据库中的操作历史。
- Runtime 不替代 PHP 应用的业务审计、Action History 或合规审计记录。
- Runtime 不把 PHP 请求正文、上传内容、Cookie 或完整 Session 写入日志。
- Runtime 不允许通过公共 HTTP 接口执行任意 SQL、任意 DuckDB 文件查询或任意路径读取。
- Parquet 是分析副本，不是业务状态或合规原始审计文件的唯一事实来源。

## 3. 总体架构

### 3.1 四类日志

| 类型 | 主要来源 | 默认文件 | 是否转 Parquet | 主要用途 |
|---|---|---|---|---|
| Runtime 结构化日志 | Go Host、队列、Scheduler、观测管线 | `runtime-<nodeID>.jsonl` | 是 | 启停、组件状态、运行异常 |
| HTTP 访问/错误日志 | Caddy HTTP Server | `access-<nodeID>.jsonl` | 是 | 请求 URI、状态、耗时、响应大小 |
| Control Plane 审计 | Unix Socket/Named Pipe 管理接口 | `audit-<nodeID>.jsonl` | 否 | 管理操作留痕和权限审计 |
| PHP 错误日志 | PHP `log_errors` | `php-error.log` | 否 | PHP Warning、Fatal、扩展错误 |

四类日志不能混写。尤其是审计日志和 PHP 错误日志保留各自的原始文件语义；JSONL 转换器只处理已封闭的 Runtime/Caddy 段文件。

`php-error.log` 由内嵌 PHP 的 `error_log` 写入，进程无法被通知重开文件，因此 Runtime 用 `ExternalRotator` 做 copytruncate：达到 `runtime.phpErrorLogMaxBytes`（默认 16 MiB）时复制为 `php-error.log-<UTC 时间戳>.log` 并原地清空，每分钟检查一次；轮转段保留由 `observability.jsonlKeepDays`（>0 时优先）或 `runtime.phpErrorLogMaxBackups` 控制，与其他日志一致。

### 3.2 数据流

```text
Go Logger (slog) ───────────────┐
                                ├─> per-node JSONL ─> rotation ─> jsonl Converter
Caddy access/error logger ──────┘                                  │
                                                                   v
                                                        Event Normalize/Redact
                                                                   │
                                                        bounded Publisher buffer
                                                                   │
                                                   local spool -> DuckDB Parquet
                                                                   │
                                              atomic rename -> controlled query

Control Plane request -> authorization -> handler -> audit JSONL -> response
PHP errors -----------------------------------------------------> php-error.log
```

### 3.3 代码目录

| 目录/文件 | 职责 |
|---|---|
| `cmd/zentao-runtime/main.go` | 计算节点日志路径、创建 Logger/Auditor/Converter、启动后台循环 |
| `internal/logging/logging.go` | `slog` JSON Logger、UTC 时间、字段/值脱敏 |
| `internal/logging/rotate.go` | Runtime 文件追加、按大小/时间轮转、历史清理 |
| `internal/web/config.go` | Caddy 访问/错误日志输出、小时轮转和保留配置 |
| `internal/control/audit.go` | Control Plane 审计 JSONL 文件写入和同步 |
| `internal/control/socket_unix.go` | Linux 控制请求解析、鉴权、审计和响应顺序 |
| `internal/control/socket_windows.go` | Windows Named Pipe 的同等控制流程 |
| `internal/observability/events.go` | 事件信封、Schema v1、字段边界和最终脱敏 |
| `internal/observability/publisher.go` | 有界批处理、spool、Parquet 原子发布和恢复 |
| `internal/observability/jsonl/jsonl.go` | 已封闭 JSONL 扫描、解析、幂等转换 |
| `internal/observability/retention/retention.go` | Parquet 节点自清理 |
| `internal/observability/query/query.go` | 受控日志/指标查询模板 |
| `internal/diagnostics/collect.go` | 生成大小受限的诊断压缩包 |
| `packaging/runtime/php.ini` | PHP 错误日志路径和 PHP 错误显示策略 |

## 4. 启动和路径解析

### 4.1 配置入口

配置文件默认为 Runtime 根目录下的 `config/runtime.toml`。相对路径在配置边界解析为绝对路径；未知 TOML 字段或不支持的 `schemaVersion` 会阻止启动。主要字段如下：

```toml
[runtime]
logPath = "logs/runtime.log"
logMaxBytes = 16777216
logMaxBackups = 5
nodeID = "node-a"
clusterID = "cluster-prod-1"
auditLog = ""

[observability]
enabled = true
datasetRoot = "data/observability"
spoolPath = "spool/observability"
maxBatchRows = 10000
maxBatchBytes = 16777216
minBatchRows = 1000
minBatchBytes = 1048576
flushInterval = "60s"
maxFlushInterval = "10m"
logDays = 30
jsonlConvertInterval = "1h"
jsonlConvertSources = ["access", "runtime"]
jsonlKeepDays = 7
jsonlDeleteAfterConvert = true
```

字段定义在 [`internal/config/config.go`](../../internal/config/config.go)。

### 4.2 有效路径

`runtime.logPath` 当前主要用于取得日志目录；Runtime 启动后按节点重新生成文件名：

```text
logsDir = dirname(runtime.logPath)
Runtime   = logsDir/runtime-<nodeID>.jsonl
Audit     = runtime.auditLog，未配置时 logsDir/audit-<nodeID>.jsonl
Access    = logsDir/access-<nodeID>.jsonl
PHP       = <install-root>/logs/php-error.log
应用日志  = <dataDir>/observability/raw-logs/<topic>-<nodeID>-<date>.jsonl（file 模式，ZENTAO_LOG_DIR）
```

因此不要假设 `runtime.logPath` 的 basename 会成为最终 Runtime 日志文件名。`nodeID` 为空时由 Runtime 从 `run/node-id` 持久化生成；多节点部署必须显式使用稳定且唯一的节点标识。

### 4.3 文件权限

- 日志目录由 Runtime 以 `0700` 创建。
- Runtime、审计和 spool 文件以 `0600` 创建。
- Parquet 临时文件和最终文件不应位于 Web document root 下。
- 诊断包以 `0600` 创建，不能包含符号链接或目录遍历目标。

## 5. Runtime Logger 实现

### 5.1 初始化

`logging.New(logging.Options{...})` 创建 `log/slog` Logger：

1. 输出目标有配置路径时为 `RotatingFile`，否则为 stderr。
2. 使用 `slog.NewJSONHandler`，默认级别为 Info。
3. 通过 `With` 写入 `node_id`、`instance_id`、`component`。
4. 通过 `redactingHandler` 在 JSON 编码前过滤敏感字段。
5. 所有记录的时间统一转换为 UTC。

Runtime 日志级别由 `runtime.logLevel` 配置（`debug`/`info`/`warn`/`error`，默认 `info`）；`Debug` 调用在默认级别下不会输出。新增日志应使用稳定、可检索的英文 message，并把可筛选信息放入结构化属性，不要拼接成不可解析的大字符串。

### 5.2 脱敏规则

字段名（大小写不敏感）包含以下标记时，值替换为 `[REDACTED]`：

```text
password, passwd, secret, token, cookie, session_id, authorization,
private_key, credential, apikey, api_key, access_key, bearer
```

字符串值以 `Bearer ` 或 `Basic ` 开头时同样替换。Group 属性会递归处理。进入 Parquet 前，`observability.Normalize` 还会执行一次最终脱敏，并限制：

- 单条 message 最大 4096 字节（保证 UTF-8 边界）。
- Fields 最多 32 个。
- Labels 最多 16 个。
- 日志事件必须携带 cluster、node、boot、source 和 kind。

新增敏感字段时必须同时更新 Logger 和 `observability.sensitiveKey`，并增加正向（保留安全值）和负向（确认秘密不出现）测试。

### 5.3 Runtime 轮转

`RotatingFile` 对单个文件串行加锁，写入前检查：

- `RollInterval` 到期：按时间轮转。
- `MaxBytes > 0` 且当前写入后超过阈值：按大小轮转。

轮转步骤是关闭当前文件、rename 为带 UTC 时间戳的 `.log` 文件、创建新的 active 文件，再执行清理。Runtime 文件的典型名称为：

```text
runtime-node-a.jsonl
runtime-node-a.jsonl-20260820T010000.000000000Z.log
```

当 `KeepDays > 0` 时，按时间戳执行天数清理，`MaxBackups` 不再作为主要上限。开发修改轮转策略时应明确两者优先级，并更新配置注释和测试，避免把“数量保留”误认为始终生效。

`php-error.log` 使用独立的 copytruncate 轮转（见 3.1 节）：写方在内嵌 PHP 进程内，文件句柄无法通过信号重开，因此不能使用 rename 轮转；Runtime 每分钟检查大小，超过 `phpErrorLogMaxBytes` 时复制后原地截断。

## 6. Caddy 访问和错误日志

`internal/web/config.go` 给业务 HTTP Server 设置默认 access logger，并配置一个 Caddy `FileWriter`：

- JSON 编码器。
- 每小时整点轮转。
- 64MB 大小轮转阈值。
- `jsonlKeepDays` 天保留。
- `http.log.access` 和 `http.log.error` 一起写入。

访问日志通常包括：

```text
request.method
request.uri
request.host
request.remote_ip
duration
status
size
logger
```

Caddy 默认不写 Cookie 和 Authorization header，但 `request.uri` 包含 query string。不要把访问日志当作敏感参数过滤器；应用和网关都不应在 URL query 中传递 Token、密码或 Session 信息。若必须支持此类旧接口，应在 Caddy 或应用层增加明确的 URI 脱敏策略，并为此增加端到端测试。

Runtime 在访问日志编码层对 `request.uri` 使用 Caddy `QueryFilter`，把
`token/password/passwd/secret/session/session_id/api_key/apikey/access_key/authorization/signature/sign`
等常见敏感 query 参数的值替换为 `[REDACTED]` 后再落盘；该行为在
`tests/e2e/access-log-smoke.sh` 中有端到端断言。

## 7. Control Plane 审计

### 7.1 被审计的操作

以下是本地管理接口的典型操作：

```text
status, version, health, diagnose, reload, stop,
logs, metrics, observability-flush, observability-clean,
convert-jsonl, collect-logs
```

Linux 通过 Unix Socket，Windows 通过 Named Pipe。它们不是普通业务 HTTP 请求，而是 Runtime 管理命令的进程间接口。CLI 命令最终发送版本化 JSON 请求，例如：

```json
{"version":1,"operation":"reload"}
```

### 7.2 审计顺序和字段

控制请求经过严格 JSON 解析、版本检查和本地调用者鉴权后交给 Handler。响应写出前先写审计记录，保证调用者不会看到成功响应而没有对应审计记录。审计字段为：

```json
{
  "time": "2026-09-02T08:00:00Z",
  "operation": "reload",
  "peer": {"uid": 1000, "gid": 1000, "pid": 1234},
  "ok": true,
  "durationMs": 4
}
```

审计不能写入请求 Payload、Token、配置明文或业务数据。`FileAuditor` 每次写入后调用 `Sync`，并按
`runtime.auditLogMaxBytes`、`runtime.auditLogMaxBackups` 和
`runtime.auditLogKeepDays` 内置轮转和保留；`auditLogKeepDays > 0` 时按天保留，
`auditLogMaxBackups` 不再作为主要上限，语义与 Runtime JSONL 保留规则一致。

## 8. JSONL 转 Parquet

### 8.1 只处理已封闭段

转换器只扫描当前 `nodeID` 的已轮转文件：

```text
access-<nodeID>-<timestamp>-time.jsonl
access-<nodeID>-<timestamp>-size.jsonl
runtime-<nodeID>.jsonl-<timestamp>.log
```

active 文件不会被读取，避免读取仍在追加的数据。`jsonlConvertSources` 只允许 `access` 和 `runtime`，并且每个节点只处理自己的文件前缀。

### 8.2 解析和字段映射

转换器将 Caddy 和 Runtime 原始行转换为统一 `observability.Event`：

| 原始字段 | 事件字段 |
|---|---|
| `ts`/`time` | `eventTime` |
| `level` | `level` |
| `msg` | `message` |
| Caddy `status` | `statusCode` |
| Caddy `duration`（秒） | `durationMs` |
| Caddy request 对象 | `fields.method/uri/host/remote_ip` |
| Caddy `size` | `fields.size` |
| 其他 Runtime 属性 | `fields` |

非法 JSON 行计入 `malformed` 并跳过；不属于 `http.log.access` 或 `http.log.error` 的 Caddy logger 被忽略。

### 8.3 幂等和状态

状态保存于：

```text
logs/.jsonl-state-<nodeID>.json
```

每个段保存 `(size, modTime)`。再次执行转换时，若两者都没有变化则跳过。状态文件采用临时文件写入、`Sync`、关闭、rename 的方式更新；损坏时先改名为 `.corrupt`，再重新建立状态。

默认无合规保留要求：转换成功并保存状态后，已封闭段文件会被删除
（`jsonlDeleteAfterConvert = true`）。需要保留原始 JSONL 时显式关闭该配置。

这是一种段级幂等机制，不是内容级去重。不要在转换后修改已封闭段文件；如果确需修复，应删除对应状态条目并明确处理已经发布的 Parquet part，避免产生业务上重复的事件。

## 9. Publisher、spool 和 Parquet 发布

### 9.1 有界缓冲

`Publisher.Add` 先执行事件 Normalize，再按 `maxBatchRows` 或 `maxBatchBytes` 封闭批次。`flushInterval` 到期时后台循环依次恢复 spool，并检查是否需要刷新。批次按 `Kind`（logs/metrics）分开，不允许日志挤占指标的类型边界。

为避免低流量下产生大量小 Parquet 文件，后台循环只有在缓冲达到
`minBatchRows`/`minBatchBytes`，或距离上次刷新超过 `maxFlushInterval`
时才真正写 Parquet；`maxBatchRows`/`maxBatchBytes` 仍是硬性封闭上限。
JSONL 转换器也会在一个转换周期内先批量 Add，再统一 Flush，避免每个
segment 单独生成一个小批次。

### 9.2 原子发布状态

发布一个批次的顺序为：

1. 根据 node、boot 和单调序列生成稳定 Batch ID。
2. 把批次清单和事件写入本地 spool JSON。
3. 在目标 Hive 分区创建 `.parquet.tmp`。
4. DuckDB 写入临时 Parquet。
5. 在同一文件系统内 rename 为 `part-<batchID>.parquet`。
6. 只有 rename 成功后才删除 spool 清单。

目录结构为：

```text
<datasetRoot>/logs/schema=v1/date=YYYY-MM-DD/hour=HH/node=<nodeID>/part-<batchID>.parquet
```

禁止跨文件系统移动临时文件，禁止追加或覆盖已经发布的 part，禁止多个节点写入同一 node 分区。

### 9.3 故障降级

- Dataset/NFS 不可用：批次保留在本地 spool，Web 和队列继续工作。
- DuckDB 写入失败：保留 spool，按后续周期重试。
- spool 超过配额：按配置拒绝新批次并增加 dropped 计数，不递归写入同一拥塞队列。
- Runtime 重启：启动时扫描 spool，最终文件已存在时按已提交处理，否则按原 Batch ID 重试。
- 查询错误：返回受控错误，不回显 SQL、内部绝对路径或凭据。

## 10. 查询和诊断

### 10.1 受控查询

CLI 使用本地 Control Plane 查询：

```bash
zentao logs --since 30m --level error --node node-a --limit 200
zentao metrics --since 1h --metric-name http.request.duration
```

查询参数只能影响时间范围、节点、日志级别、指标名和行数上限。SQL 模板、Parquet 根目录、表函数和允许字段由 Runtime 固定生成；不允许用户提交任意 SQL 或任意文件路径。

近期未转换日志仍保留在 JSONL 中，因此“查询 Parquet”不等于“实时读取 active 日志”。需要本节点即时排障时，应直接读取受保护的 active JSONL 或使用诊断命令。

### 10.2 诊断包

`zentao collect-logs` 生成 `logs/collect-<timestamp>.tar.gz`，包含：

- Runtime、Access、Audit 和 PHP 日志（单文件最大 16MB）。
- 版本信息和脱敏配置摘要。
- 不存在的日志会跳过。

诊断包不会跟随符号链接，不包含数据库内容、请求正文或配置 Payload。由于诊断包读取原始日志，新增任何可能携带秘密的字段时必须同时评估诊断包泄漏风险。

### 10.3 日志查看查询桥（D7）

禅道后台“日志”查看器在 flush 模式下经 loopback 查询桥读取日志：

```text
POST http://127.0.0.1:<observability.bridgePort>/internal/runtime/observability/v1/query
X-Zentao-Observability-Token: <token>
X-Zentao-Observability-Schema: 1
{"schemaVersion":1,"filters":{"topic":"error","level":"error","module":"common",
 "account":"admin","requestID":"...","message":"...","fromDate":"20260901",
 "toDate":"20260904","fields":{"objectType":"webhook","objectID":"5"},
 "offset":0,"limit":200}}
```

- 桥只监听 loopback；token 与 ingest 共用；拒绝任意 SQL/路径；
- 过滤字段为白名单（topic/module/method/account/requestID/eventID/message/level/时间，
  以及 `fields` map 的任意 `fields.<key>` 等值过滤，如 objectType/objectID/action），
  对应 Parquet `fields` JSON 列过滤与日志查看器统一；
- 结果按 `eventTime` 倒序分页，返回 `list/total/truncated`，行结构与 JSONL 事件同构
  （含 `fields`），PHP 侧 file/flush 两种模式共用同一渲染；
- Runtime 在 DuckDB query engine 可用时设置 `ZENTAO_OBSERVABILITY_QUERY_URL` 与
  `ZENTAO_LOG_MODE=flush`；禅道据此关闭本地 JSONL（不双写）。

查询桥当前读取 Parquet（历史）并合并本节点近期 access/runtime JSONL；
跨节点与近期未转换的 PHP bridge 事件一致性验证属于 Phase 5 验收。

## 11. 开发扩展指南

### 11.1 新增 Runtime 日志

1. 在拥有 `*logging.Logger` 或 `*slog.Logger` 的组件中添加日志。
2. 使用固定 message 和结构化属性：

   ```go
   logger.Warn("queue bridge call failed",
       slog.String("operation", operation),
       slog.String("code", code),
       slog.Any("error", err))
   ```

3. 检查属性名是否可能包含密码、Token、Cookie、Session 或授权信息。
4. 若该日志需要进入 Parquet，确认字段能被 `jsonl.parseLine` 转成有限的字符串字段。
5. 增加脱敏、结构和轮转测试；不要在业务错误路径中再次调用可能失败的外部日志服务。

### 11.2 新增 PHP/业务事件

PHP 不应直接写共享 Parquet 或 DuckDB。应实现版本化、本机、短超时、有限大小的 Runtime Event Client：

1. 携带 Schema v1 的 event envelope。
2. 在 PHP 侧先过滤 Password/Token/Cookie/Session/SQL 参数。
3. Runtime 不可用时快速丢弃或本地降级，不能阻塞业务事务。
4. 禁止用户输入直接成为指标名或无界标签。
5. 在开源版、企业版、旗舰版和 IPD 的包含关系中选择最低公共实现层。

Runtime 已提供私有 Observability Bridge：`POST
http://127.0.0.1:8082/internal/runtime/observability/v1/events`，使用
`X-Zentao-Observability-Token`（每次启动随机生成）和
`X-Zentao-Observability-Schema: 1` 鉴权，请求体上限 256 KiB、单批上限 100
个事件，任何事件校验失败整批拒绝。PHP 侧最低公共实现为
`zentaopms/lib/observability/observability.class.php`，自动补齐 Schema v1
信封、先做字段/文本脱敏、限制标签基数，并在 Runtime 不可用时快速返回失败。
更完整的入口和验收标准见 [禅道应用适配计划](./01-zentao-application-adaptation-plan.md:138)。

Runtime 与 PHP 侧共用固定度量注册表：
`internal/observability/registry.go` 和
`zentaopms/lib/observability/observability.class.php::METRIC_REGISTRY`。
未注册的 `metricName` 或非 `counter`/`gauge` 的 `metricKind` 在事件
Normalize 阶段整批拒绝；扩展指标必须同时更新两侧并增加契约测试，保证
集群内所有节点对同一指标集达成一致。

### 11.2.1 Z-OBS-05 候选首批业务度量（待评审）

以下为建议的低频、低基数首批业务度量，正式接入前需完成禅道侧评审：

| 指标名 | 类型 | 标签 | 触发点 |
|---|---|---|---|
| `zentao.queue.schedule.count` | counter | `node` | Runtime Scheduler 每次 schedule 调用成功后 +1 |
| `zentao.queue.heartbeat.count` | counter | `node`, `worker` | Queue Worker 每次心跳成功后 +1 |
| `zentao.php.fatal.count` | counter | `node` | PHP Fatal 错误发生时 +1，禁止用户输入直接控制 |

评审通过后同时更新 `internal/observability/registry.go` 和 PHP
`METRIC_REGISTRY`，并补充端到端契约测试；指标失败不得改变任务或请求状态。

### 11.3 修改 Schema

不得直接改变 Schema v1 字段类型或语义。需要不兼容变化时：

1. 新增 Schema 版本和明确迁移规则。
2. 更新事件 Normalize、Parquet writer、query 模板和状态恢复。
3. 保留旧版本文件的受控读取能力。
4. 增加跨版本 fixture、升级和回滚测试。
5. 同步更新设计文档、跨仓契约和发布说明。

## 12. 测试和验收

### 12.1 单元测试

在 `runtime` 目录执行：

```bash
go test ./internal/logging ./internal/observability/... ./internal/control ./internal/diagnostics
```

至少覆盖：

- 敏感字段、分组属性和 Bearer/Basic 文本脱敏。
- UTC 时间和结构化字段输出。
- 大小轮转、时间轮转、过期清理和关闭后的写入行为。
- 审计 JSONL 格式、并发写入、Sync 和关闭。
- Caddy access/error 字段映射及非访问 logger 过滤。
- Runtime active 文件不被转换。
- malformed 行计数和跳过。
- 自节点文件筛选，其他节点文件不被处理。
- JSONL 转换的重复执行幂等性。
- Batch ID、spool 恢复、原子发布和损坏状态隔离。
- Parquet 查询的时间、节点、级别和行数限制。
- 指标注册表：未注册 `metricName` 和非 `counter`/`gauge` 类型整批拒绝。
- Publisher 自指标：以注册表内 metric 事件周期发布到 Parquet，且计数按
  delta 计算；内存 `status`/`diagnose` 计数保持独立可用。
- 攒批策略：低于 `minBatchRows`/`minBatchBytes` 不刷新，超过
  `maxFlushInterval` 强制刷新；JSONL 转换一个周期只生成一批 Parquet。

### 12.2 端到端测试

```bash
runtime/tests/e2e/access-log-smoke.sh <app-package|app-dir>
runtime/tests/e2e/jsonl-parquet-smoke.sh [app-package|app-dir]
runtime/tests/e2e/observability-bridge-smoke.sh [app-package|app-dir]
runtime/tests/e2e/observability-dual-node-smoke.sh [app-package|app-dir]
runtime/tests/e2e/observability-readonly-smoke.sh [app-package|app-dir]
runtime/tests/e2e/observability-fault-recovery-smoke.sh [app-package|app-dir]
```

访问日志冒烟测试应验证请求 URI、状态、耗时、500 错误、敏感 query 参数脱敏和 PHP 错误文件；JSONL/Parquet 冒烟测试应验证节点文件、手动转换、Hive 分区、状态文件和第二次转换零新增事件；Observability Bridge 冒烟测试应验证 PHP 事件鉴权、接收并发布到节点 Parquet 分区；双节点冒烟测试应验证两个节点共享同一数据集根目录时只写入各自的 `node=<nodeID>` 分区且互不覆盖；只读冒烟测试应验证共享数据集不可写时业务继续、事件留在本地 spool 且不发布 Parquet；故障恢复冒烟测试应验证数据集只读期间 spool 保留、恢复读写后无需重启即可自动补发。

### 12.3 必须补充的生产验收

以下场景不能只用单元测试代替：

| 场景 | 验收要求 |
|---|---|
| Linux 双节点 NFS | 两节点同时发布不同 part，无互相覆盖；断开/恢复后 spool 补发 |
| NFS 只读或挂载丢失 | 已通过只读 bind mount 与故障恢复冒烟验证（业务继续、spool 保留、恢复后自动补发）；真实 NFS 协议仍需生产验收 |
| Runtime 崩溃 | 重新启动后 spool 可恢复，最终文件不重复 |
| 轮转边界 | 整点轮转、64MB 边界、并发写入和进程停止不丢完整行 |
| 敏感 query 参数 | 明确禁止或验证脱敏；诊断包和 Parquet 均不能泄漏 |
| Windows | Named Pipe 审计、路径含空格、轮转和文件权限行为符合平台语义 |
| PHP 集成 | PHP Event Client 超时、丢弃、字段限制和各版本包含关系通过联合测试 |

## 13. 当前状态和待办

### 13.1 已实现

- Runtime `slog` JSONL、节点/实例/组件标识和字段脱敏。
- Caddy 访问/错误日志、按小时/大小轮转和 JSON 编码。
- Control Plane 本地鉴权、操作审计和同步写入。
- 日志事件 Normalize、批量缓冲、spool、Parquet 原子发布和崩溃恢复。
- JSONL → Parquet 手动/定时转换、节点隔离和段级幂等。
- Parquet 受控查询、节点自清理和诊断包。
- Runtime logging、observability、control、diagnostics 单元测试，以及访问日志和 JSONL/Parquet 冒烟脚本。
- 私有 PHP Observability Bridge 事件接入端点和 `zentaopms/lib/observability`
  Event Client；访问日志敏感 query 参数脱敏；审计日志内置轮转/保留；
  `runtime.logLevel` 配置；Publisher 自身计数（received/published/recovered/dropped）
  通过 status/diagnose 暴露，并周期以注册表内 metric 事件发布到 Parquet，
  不依赖 Parquet 管线自证健康。
- 固定度量注册表（Runtime `registry.go` + PHP `METRIC_REGISTRY`），
  未注册指标名和非 counter/gauge 类型在 Normalize 阶段整批拒绝。

### 13.2 未完成或需要确认

1. 真实 NFS 协议中断仍需生产环境故障注入；双节点共享数据集、只读/挂载丢失和恢复补发已通过 Docker 端到端验证（含 mount 层故障恢复）。当前开发环境无法加载 NFS 内核模块（`modprobe nfs` 被拒绝），因此真实协议级测试需在具备 NFS 的生产或 CI 环境执行。
2. 业务度量注册表已落地（Runtime + PHP 双侧），首批业务度量（Z-OBS-05）
   仍按禅道侧评审节奏实施。

已落地：PHP Event Client 与 Runtime 事件接入端点、审计日志轮转和容量上限、
`logMaxBackups`/`jsonlKeepDays` 优先级语义（按天保留优先）、`runtime.logLevel`
配置、访问日志 query 脱敏，以及 Publisher 自身计数（通过 `status`/`diagnose`
暴露，不依赖同一 Parquet 管线报告自身故障）。

## 14. 发布前检查清单

- [x] `schemaVersion`、事件字段和跨仓契约版本一致。
- [x] Runtime、Caddy、Audit、PHP 四类日志目录和权限已检查。
- [x] `nodeID` 在每个节点稳定且唯一，`clusterID` 一致。
- [x] 日志脱敏测试覆盖新增字段和 query 参数策略。
- [x] active/rotated JSONL 文件名符合转换器正则。
- [x] `jsonlConvertSources` 只使用受支持的 `access`/`runtime`。
- [x] Parquet 发布使用同文件系统原子 rename，临时文件不匹配查询 glob。
- [x] spool 配额、恢复、清理和 dropped 计数有验证证据。
- [x] Control Plane 每个管理操作可鉴权、可审计，审计不含 Payload/Token。
- [x] 诊断包大小、权限、路径和秘密泄漏检查通过。
- [ ] Linux amd64、Linux arm64、Windows x86_64 和 Docker 的日志冒烟测试结果已记录。
- [x] 真实 NFS、双节点、付费版 ionCube 和 PHP 应用事件接入的未验证项已写入发布说明。

### 14.1 发布说明：未验证项

- 真实 NFS 协议中断：当前开发环境无法加载 NFS 内核模块
  （`modprobe nfs` 被拒绝），需在具备 NFS 的生产或 CI 环境执行；
  mount 层只读/恢复补发已通过 `observability-fault-recovery-smoke.sh` 验证。
- Linux arm64、Windows x86_64 的日志冒烟结果需由 CI 原生矩阵补充记录；
  本次开发环境仅完成 Linux amd64 Docker 验证。
- 付费版 ionCube 与 PHP 事件接入的联合测试需禅道侧适配实施后执行。
- Z-OBS-05 首批业务度量待评审（候选指标见 11.2.1）。

### 14.2 已验证结果记录

| 场景 | 结果 |
|---|---|
| Linux amd64 Docker：access-log / jsonl-parquet / observability-bridge / dual-node / read-only / fault-recovery | 全部通过 |
| Linux amd64 单元测试（logging/config/control/observability/jsonl/query/retention/diagnostics） | 全部通过 |
| Linux arm64 / Windows x86_64 日志冒烟 | 待 CI 原生矩阵记录 |
| 真实 NFS 协议中断 | 环境受限，待生产/CI 执行 |

## 15. 参考入口

- [可观测性设计](../design/05-observability.md)
- [Runtime Control Plane v1](../design/contracts/runtime-control-plane-v1.md)
- [Runtime 配置样例](../../config/runtime.example.toml)
- [Runtime 日志实现](../../internal/logging/logging.go)
- [Runtime 轮转实现](../../internal/logging/rotate.go)
- [JSONL 转换实现](../../internal/observability/jsonl/jsonl.go)
- [Parquet Publisher 实现](../../internal/observability/publisher.go)
- [访问日志配置](../../internal/web/config.go)
- [禅道应用适配计划](./01-zentao-application-adaptation-plan.md)
