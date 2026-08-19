# zentao DuckDB 与共享 Parquet 可观测性详细设计

## 1. 文档信息

| 项目 | 内容 |
|---|---|
| 状态 | 已实施（Runtime 侧落地；NFS 真实故障注入待集成环境） |
| 日期 | 2026-08-18 |
| 查询与生成引擎 | DuckDB，作为 Go Library 嵌入 `zentao-runtime` |
| 持久化格式 | Parquet |
| 单机存储 | 本地 Parquet 数据集 |
| 多节点存储 | NFSv4.1 共享 Parquet 数据集 |
| 写入模型 | 每节点独立分区、批量生成、不可变 part 文件 |
| 共享 `.duckdb` | 禁止 |
| 关联设计 | [Runtime Host 详细设计](./runtime-host-library-design.md)、[多节点部署设计](./deployment-and-ha-design.md)、[GitHub Actions 构建设计](./github-actions-build-design.md) |

## 2. 核心结论

zentao 使用 DuckDB 保存和查询 Runtime 度量项与结构化日志，但共享存储的事实格式是 Parquet 数据集，不是 DuckDB 数据库文件。

正式决策如下：

1. DuckDB 通过官方 Go Driver/C API 作为 Library 链接到 `zentao-runtime`，不启动独立 DuckDB Server。
2. 不在 NFS 上创建或共享可写 `.duckdb` 文件；DuckDB 使用内存 Catalog 或节点本地临时 Catalog 查询和生成 Parquet。
3. 多节点共享一个 Parquet 数据集目录，但每个节点只写自己的 `node=<nodeID>` 分区。
4. 每次写入创建新的不可变 `part-<batchID>.parquet`，不追加、修改或覆盖已经发布的 Parquet 文件。
5. 文件先以不匹配查询通配符的临时名称写入，关闭并校验后在同一 NFS 文件系统内原子 `rename` 发布。
6. 节点之间不需要写入选主、分布式锁或共享写入器；低频写入通过节点分区和不可变文件彻底消除写竞争。
7. 指标在内存中按时间窗口聚合，日志批量写入；禁止每个样本或每条日志生成一个 Parquet 文件。
8. NFS 不可用时写入节点本地有界 spool，恢复后使用原 `batchID` 补发；可观测性故障不能阻止禅道业务请求。
9. 查询必须限制时间范围、返回行数、线程和内存，公共 Web 接口不接受任意 DuckDB SQL。
10. Runtime 操作指标和日志可以以 Parquet 为主存储；禅道业务度量结果若参与事务、权限或业务状态，业务数据库仍是事实来源，Parquet 只保存分析副本。

## 3. 目标与非目标

### 3.1 设计目标

- 不引入 Prometheus、Loki、Elasticsearch、ClickHouse 等强制外部服务，也能查询本机和双节点历史运行状态。
- 统一保存 Runtime、Caddy、FrankenPHP、PHP、Scheduler、队列和缓存产生的结构化度量与日志。
- 两个应用节点可以同时向共享 NFS 发布数据，不产生同文件竞争、覆盖或部分文件可见。
- 节点故障后，已经发布到 NFS 的历史数据仍可由另一节点查询。
- 通过日期、小时和节点分区裁剪，避免每次诊断扫描全部历史文件。
- 对写入、查询、保留和本地 spool 设置明确资源边界，不让可观测性拖慢业务。
- 保留未来导出 OTLP、Prometheus 或其他外部平台的接口，但不作为第一版依赖。

### 3.2 非目标

- 不把 DuckDB 或 Parquet 用作 Session、缓存、消息队列、锁或业务数据库。
- 不提供多个进程共同写一个 `.duckdb` 文件。
- 不允许多个节点修改同一个 Parquet 文件。
- 不提供实时流处理、跨数据中心日志复制或亚秒级集中查询承诺。
- 不保存未经筛选的请求体、响应体、上传文件、Cookie、Session 或完整 SQL 参数。
- 不用 DuckDB 查询代替数据库慢查询治理。
- 第一版不实现跨节点全局文件合并和统一压缩任务。

## 4. 总体架构

```text
Runtime/Caddy/FrankenPHP/PHP/Scheduler
  -> Structured Telemetry Event
  -> Node-local bounded buffers
       +-- metric window aggregation
       +-- log batch buffer
       +-- local spool on shared-storage failure
  -> DuckDB COPY ... TO Parquet
  -> .<batchID>.parquet.tmp
  -> validate and atomic rename
  -> shared Parquet dataset

Admin CLI / protected diagnostics
  -> DuckDB read_parquet(..., hive_partitioning=true)
  -> time/row/resource limits
  -> formatted logs, metrics or diagnostic bundle
```

DuckDB 负责列式编码、压缩、Parquet 读取和分析查询。Runtime 负责事件规范化、批次生命周期、目录所有权、资源配额、权限和故障降级。

### 4.1 进程边界

第一版在 `zentao-runtime` 内嵌 DuckDB Library，并为每个运行实例创建一个 Observability Manager：

- 一个逻辑写入器串行发布本节点批次。
- 指标聚合和日志入队可以并发，但不能直接并发调用文件发布。
- 管理查询使用独立 DuckDB Connection，并设置更低线程数和内存上限。
- 高成本离线诊断优先由新的 CLI 进程执行，避免占用 Web 主进程资源。

若压力测试证明原生库或分析查询的故障隔离不足，后续可以使用同一个 `zentao-runtime` 二进制增加 `observer` 子进程；该演进不改变 Parquet 数据集契约。

## 5. 数据目录与所有权

### 5.1 目录布局

```text
observability/
  metrics/
    schema=v1/
      date=2026-08-18/
        hour=10/
          node=node-a/
            part-<batchID>.parquet
          node=node-b/
            part-<batchID>.parquet
  logs/
    schema=v1/
      date=2026-08-18/
        hour=10/
          node=node-a/
            part-<batchID>.parquet
          node=node-b/
            part-<batchID>.parquet
```

目录使用 Hive partitioning 命名，使 DuckDB 可以从路径读取 `schema`、`date`、`hour` 和 `node`，并根据查询条件进行文件裁剪。

指标与日志使用不同根目录和 Schema，不能混在同一个 Parquet 文件中。`nodeID` 必须是安装时生成并在 Cluster 内唯一的稳定 ID，不能使用可重复的主机短名作为唯一依据。

每个节点目录包含不匹配 Parquet 查询通配符的不可变 owner marker，记录 Cluster ID、nodeID 和节点安装身份摘要。节点首次创建目录时原子声明所有权；发现同一 nodeID 对应不同安装身份时拒绝发布并告警。节点重装或接管必须通过显式管理流程更新 marker，不能自动覆盖。

### 5.2 单机与多节点

| 模式 | 数据集根目录 | 行为 |
|---|---|---|
| 单机 | 本地持久目录 | 使用同一分区和 part 文件协议 |
| Linux/Docker 多节点 | NFSv4.1 共享目录 | 各节点只写自己的节点分区，读取全部分区 |
| Windows 单机 | 本地持久目录 | 正式支持 |
| Windows 多节点 | 客户共享存储 | 仅在文件语义和 DuckDB/Parquet 兼容测试通过后支持 |

共享 Parquet 可以使用附件 NFS 的独立目录，也可以使用独立 NFS Export。无论哪种方式，都必须设置独立容量配额和健康状态，避免日志增长耗尽附件空间。

### 5.3 所有权规则

- 节点只能发布、重试、清理自己的 `node=<nodeID>` 目录。
- 发布前必须验证 owner marker；重复 nodeID 或 Cluster ID 不匹配时停止本节点 Parquet 写入。
- 任一节点可以只读查询所有节点已经发布的 `.parquet` 文件。
- 节点不得删除、改名或压缩其他节点的分区。
- 节点永久下线后的目录由显式管理命令接管，不能由普通保留任务自动认领。
- Cluster ID 不直接作为 Parquet 用户数据列时，也必须位于配置的根目录边界中，禁止不同集群误用同一数据集。

## 6. 事件与 Schema

### 6.1 通用事件信封

所有事件至少包含：

| 字段 | 类型 | 说明 |
|---|---|---|
| `schemaVersion` | integer | 文件内 Schema 版本 |
| `eventTime` | timestamp(us), UTC | 事件发生时间 |
| `ingestTime` | timestamp(us), UTC | Runtime 接收时间 |
| `clusterID` | string | 集群标识，不包含 Secret |
| `nodeID` | string | 节点标识 |
| `bootID` | string | 本次 Runtime 启动标识 |
| `eventID` | string | 节点内唯一事件 ID |
| `source` | string | `runtime`、`caddy`、`php` 等受控来源 |

事件时间统一写 UTC。查询和 UI 可以转换为管理员时区，但不得把本地时间作为跨节点排序依据。

### 6.2 度量项 Schema

建议逻辑字段：

| 字段 | 类型 | 说明 |
|---|---|---|
| `bucketStart` | timestamp(us), UTC | 聚合窗口开始时间 |
| `bucketSeconds` | integer | 聚合窗口长度 |
| `metricName` | string | 受注册表约束的稳定名称 |
| `metricKind` | string | `counter`、`gauge` 或 `histogram` |
| `labels` | JSON/string | 低基数标签 |
| `labelHash` | fixed binary | 规范化标签摘要 |
| `value` | double/null | Gauge 或单值 |
| `count` | unsigned bigint/null | Counter 或 Histogram 样本数 |
| `sum` | double/null | 窗口总和 |
| `min` | double/null | 窗口最小值 |
| `max` | double/null | 窗口最大值 |
| `histogram` | JSON/null | 固定边界和 bucket count |

指标名称和标签必须来自注册表。用户 ID、Session ID、请求 ID、完整 URL 参数和原始错误消息不能作为指标标签，避免高基数和敏感信息泄露。

第一版默认保存聚合窗口，不保存每个 HTTP 请求的原始指标样本。分位数不能通过平均各节点 p95 得到；需要跨节点聚合时保存固定 Histogram buckets，再在查询时合并 bucket count。

### 6.3 日志 Schema

建议逻辑字段：

| 字段 | 类型 | 说明 |
|---|---|---|
| `level` | string | `debug`、`info`、`warn`、`error` |
| `message` | string | 经过大小限制和脱敏的消息 |
| `requestID` | string/null | 请求关联 ID |
| `traceID` | string/null | 可选 Trace 关联 ID |
| `module` | string/null | 禅道或 Runtime 模块 |
| `operation` | string/null | 稳定操作名 |
| `durationMs` | double/null | 操作耗时 |
| `statusCode` | integer/null | HTTP 或内部状态码 |
| `fields` | JSON/string | 受大小和字段数限制的扩展字段 |

日志不得保存数据库密码、Redis 凭据、Cookie、Session ID、Authorization、ionCube 授权内容、完整 SQL 参数、请求体、响应体或上传文件内容。

### 6.4 业务度量边界

Runtime 运行指标可以直接以 Parquet 为事实存储。禅道业务度量项分为两类：

- 只用于趋势、报表和诊断的分析副本，可以发布到 Parquet。
- 参与事务、权限、审批、目标状态或业务计算的度量结果，必须先写禅道业务数据库；Parquet 只能接收提交成功后的副本。

DuckDB/Parquet 写入失败不能回滚已经提交的业务事务，也不能让业务数据库依赖 Parquet 才能恢复正确状态。

## 7. 批处理与发布协议

### 7.1 批次触发

建议初始配置：

| 参数 | 建议基线 |
|---|---|
| `flushInterval` | 60 秒 |
| `maxRowsPerBatch` | 指标 10,000；日志 50,000 |
| `maxUncompressedBytes` | 16 MB |
| Parquet compression | ZSTD |
| shutdown flush timeout | 10 秒 |

任一时间、行数或大小阈值达到即封闭批次。错误日志可以触发提前刷新，但不能每条错误生成一个文件。具体默认值必须根据实际流量和 NFS 性能压测调整。

封闭批次必须先按数据类型、Schema、UTC 日期、UTC 小时和 nodeID 分组；一个 Parquet 文件不能跨越目录声明的日期或小时分区。迟到事件仍写入其 `eventTime` 对应的历史分区。

### 7.2 Batch ID

批次 ID 由以下稳定信息生成：

```text
nodeID + bootID + monotonicSequence
```

本地 spool 段创建时确定 `batchID`，所有重试沿用相同 ID。最终文件名是幂等提交键；发现相同最终文件已经存在时，Runtime 校验其元数据后将该批视为已发布，而不是再生成一个新文件。

### 7.3 发布状态机

```text
Collecting
  -> Sealed
  -> Spooling
  -> WritingTemp
  -> Validating
  -> Publishing
  -> Published
  -> SpoolDeleted
```

发布步骤：

1. 将封闭批次写入节点本地 spool，并持久化 `batchID`、目标分区和记录数。
2. 在目标 NFS 分区创建 `.<batchID>.parquet.tmp`。
3. 使用 DuckDB `COPY ... TO` 生成 Parquet，完成 flush、关闭和平台支持的持久化同步。
4. 重新读取 Parquet metadata，校验 Schema、记录数、时间范围和 nodeID。
5. 在同一文件系统内原子 `rename` 为 `part-<batchID>.parquet`。
6. 发布成功后删除对应本地 spool 段。

临时文件扩展名不能匹配查询使用的 `*.parquet`。不能先在本地文件系统写临时 Parquet，再跨文件系统移动到 NFS，因为该移动不具备原子性。

### 7.4 崩溃恢复

启动时扫描本节点 spool 和目标节点分区：

- spool 存在且最终文件不存在：使用原 `batchID` 重试。
- spool 和最终文件都存在：校验最终文件后删除 spool。
- 仅存在超过时限的临时文件：校验无活跃发布者后删除并从 spool 重建。
- 最终文件损坏：移动到本节点 quarantine 目录、告警并从 spool 重建；没有 spool 时保留证据，不静默删除。

Runtime 不扫描或修复其他节点的临时文件，除非管理员明确执行节点接管命令。

## 8. 本地缓冲与降级

### 8.1 有界队列

指标和日志使用不同队列与配额：

- 指标队列拥塞时优先合并相同窗口、名称和标签的聚合值。
- Debug/Info 访问日志可以按配置采样或丢弃最旧记录。
- Warn/Error 优先保留并触发本地 spool。
- Audit、安装、升级和安全日志继续写追加式轮转文件；DuckDB/Parquet 是查询副本，不是唯一合规副本。

所有丢弃行为必须产生累计计数和速率受限告警，不能为记录“日志丢失”再次递归写入同一拥塞队列。

### 8.2 NFS 故障

NFS 不可用、只读或容量耗尽时：

- 停止尝试创建新的 NFS 临时文件，按退避策略探测恢复。
- 已封闭批次保存在本地 spool。
- Web、Scheduler 和队列继续运行，可观测性健康状态为 `degraded`。
- 本地 spool 到达配额后按日志等级和指标类型执行配置的淘汰策略。
- NFS 恢复后按原时间分区和原 `batchID` 顺序补发。

禁止在 NFS 挂载丢失后把共享根目录当成本地普通目录继续发布，否则会形成节点不可见的分叉数据集。

### 8.3 DuckDB 错误

DuckDB 返回编码、内存或查询错误时：

- 当前批次保留在 spool，并进行有上限重试。
- 写入错误不能影响业务请求返回。
- 查询错误返回受控诊断信息，不回显任意内部路径或 SQL。
- 连续失败达到阈值后打开本节点可观测性熔断，保留文件日志并告警。

## 9. 查询设计

### 9.1 逻辑视图

Runtime 为受支持的查询构建结构化 SQL，例如：

```sql
SELECT *
  FROM read_parquet(
       '/shared/observability/logs/schema=v1/date=2026-08-18/hour=10/node=*/part-*.parquet',
       hive_partitioning = true,
       union_by_name = true
  )
 WHERE date BETWEEN :startDate AND :endDate
   AND eventTime >= :startTime
   AND eventTime < :endTime
 ORDER BY eventTime DESC
 LIMIT :limit;
```

Runtime 根据允许的时间范围在服务端生成明确的日期/小时文件模式；跨小时查询向 `read_parquet` 传入受控文件列表。文件根目录、表函数和字段名来自服务端 allowlist，不能由请求参数拼接。查询参数只能影响绑定值和受控筛选条件。

### 9.2 管理入口

建议提供：

```text
zentao-runtime logs --since 30m --level error --node all
zentao-runtime metrics --since 1h --name http.request.duration
zentao-runtime diagnose --include-observability
```

Control plane 只提供预定义查询和分页结果。任意 SQL 仅允许离线支持工具在本机管理员权限下使用，并默认以只读方式打开数据集。

### 9.3 资源限制

每次查询至少限制：

- 必填或默认补齐的时间范围。
- 最大扫描天数。
- 最大返回行数和返回字节数。
- DuckDB threads 和 memory limit。
- 临时目录及最大临时空间。
- 查询超时和客户端取消。
- 同时运行的诊断查询数量。

默认交互查询只读取最近时间范围。跨月查询、完整导出和支持包生成使用离线 CLI，不占用公共 Web 请求线程。

### 9.4 可见性

共享查询只能看到已经原子发布的 part 文件。内存缓冲和本地 spool 中的近期事件在其他节点暂不可见，因此跨节点可见性延迟上限约为 `flushInterval + publishDuration`。

本节点实时诊断可以合并当前内存窗口；跨节点第一版不建设实时 fan-out 查询。

## 10. 保留、清理与小文件

### 10.1 保留策略

建议初始值，可由部署方调整：

| 数据 | 建议保留 |
|---|---|
| 聚合指标 | 30 天 |
| Debug/Info 日志 | 7 天 |
| Warn/Error 查询副本 | 30 天 |
| Audit 查询副本 | 90 天；原始审计文件按合规策略保留 |
| 本地 spool | 按容量和最长补发时间双重限制 |

### 10.2 节点自清理

每个节点只删除自己的过期日期分区：

- 使用规范化根路径和固定 `nodeID`，拒绝路径穿越、空路径、根目录或符号链接越界。
- 只删除已经关闭且超过保留期的分区。
- 删除使用时间和文件数预算，避免长时间占用 NFS。
- 记录删除文件数、字节数和最老/最新时间，不记录日志内容。

永久下线节点的数据通过 `zentao-runtime observability prune-node <nodeID>` 显式清理，并要求管理员确认目标 Cluster、节点和时间范围。

### 10.3 小文件策略

第一版依靠批量阈值控制小文件，不实现跨节点合并。监控以下指标：

- 每小时每节点 part 文件数。
- 平均和 p95 Parquet 文件大小。
- 目录列举、查询规划和首行返回耗时。

只有压测证明小文件成为主要瓶颈时，才增加对“本节点已经关闭的历史分区”的 copy-on-write 合并。合并不能原地覆盖，也不能由两个节点处理同一分区。

## 11. 安全设计

- Parquet 根目录不位于 Web document root 下。
- Runtime 在应用层强制节点目录所有权；存储支持按客户端或身份设置 ACL 时，只授予本节点目录写权限和共享查询所需读权限。
- NFS Export 仅允许集群节点网络访问，并设置容量配额。
- 所有字段在进入缓冲区前脱敏，不能依赖查询时再脱敏。
- 消息、字段和单条事件设置大小上限，超限内容截断并记录摘要。
- 禁止通过日志字段注入文件路径、SQL、DuckDB 配置或 Extension 名称。
- DuckDB 禁止自动安装、自动加载和从网络下载 Extension；只允许构建清单中审核过的内置能力。
- 禁止 DuckDB 查询任意本机文件、环境变量、HTTP/S3 URL 或 NFS 根目录之外的路径。
- CLI 和 Control plane 查询需要本机管理员权限或受保护管理身份，并记录审计日志。

## 12. 配置设计

配置语义示例：

```yaml
observability:
  enabled: true
  node_id: node-a
  dataset_root: /var/lib/zentao/observability
  shared: true
  flush_interval: 60s
  max_rows:
    metrics: 10000
    logs: 50000
  max_uncompressed_bytes: 16777216
  compression: zstd
  spool:
    path: /var/lib/zentao/spool/observability
    max_bytes: 1073741824
  retention:
    metrics_days: 30
    info_logs_days: 7
    error_logs_days: 30
  query:
    max_days: 7
    max_rows: 10000
    threads: 2
    memory_limit: 256MB
```

多节点必须一致：

- `dataset_root` 的逻辑路径和 NFS 存储身份。
- Schema 版本、压缩格式和时间分区规则。
- 指标注册表和标签规范化规则。
- 脱敏策略和保留策略。

允许每节点不同：

- `node_id`、本地 spool 路径和本地容量。
- Flush 阈值和查询资源上限，但差异需要在诊断中显示。

配置校验不能主动创建共享根目录来掩盖 NFS 未挂载；必须先验证挂载身份，再创建本节点分区。

## 13. 可观测性自身指标

至少记录：

- `observability_events_received_total{kind,source}`。
- `observability_events_dropped_total{kind,reason}`。
- `observability_buffer_items{kind}`。
- `observability_spool_bytes` 和 `observability_spool_batches`。
- `observability_batch_publish_total{result}`。
- `observability_batch_rows`、`observability_batch_bytes` 和发布耗时。
- `observability_last_published_timestamp`。
- `observability_parquet_files` 和平均文件大小。
- `observability_query_total{result}` 和查询耗时。
- `observability_nfs_available`。

这些内部指标在写入管线自身故障时仍需通过内存状态、健康端点或速率受限文件日志暴露，不能只依赖同一 Parquet 管线。

## 14. 构建与供应链

- 优先使用 DuckDB 官方维护的 Go Driver/C API，不使用非维护 Fork。
- DuckDB 和 Go Binding 必须固定精确版本、Commit 和校验信息。
- GitHub Actions 使用 Linux amd64、Linux arm64 和 Windows x64 原生 Runner 构建，不采用 QEMU 生成正式 Runtime。
- Manifest 和 SBOM 记录 DuckDB、Binding、C/C++ Runtime、编译器、Parquet 能力和许可证。
- 明确静态或动态链接策略，检查与 PHP Embed、FrankenPHP、ionCube 和 Windows CRT 的符号/运行库兼容性。
- 正式构建禁用未审核 Extension 的网络安装和运行时自动加载。
- Parquet Extension/能力必须在构建期静态包含或作为锁版本、校验和的正式制品随包交付，不能在客户环境联网下载。
- DuckDB 升级必须执行 Parquet 前后兼容、查询结果、崩溃恢复、内存和文件格式回归测试。

DuckDB 会增加 Runtime 二进制体积和 CGO/C++ 链接复杂度；增加量必须在各平台 manifest 中可见，不能通过运行时下载隐藏依赖。

## 15. 测试计划

### 15.1 单元与契约测试

- 指标窗口聚合、Counter/Gauge/Histogram 语义。
- 标签规范化和 `labelHash` 稳定性。
- 日志字段脱敏、大小限制和非法 UTF-8。
- Batch ID 幂等性和 spool 状态机。
- Schema v1 的必填字段、类型和向后兼容读取。
- 路径生成、节点所有权和路径穿越拒绝。
- owner marker 原子声明、重复 nodeID 拒绝和显式节点接管。

### 15.2 文件发布测试

- 临时文件不会被查询通配符读取。
- Parquet 关闭和校验完成前不发布最终文件。
- 同一文件系统原子 rename。
- 发布前、发布中和发布后进程崩溃恢复。
- 相同 `batchID` 重试不产生重复最终文件。
- 两节点同时向同一小时、不同节点分区发布。
- 一个节点不能覆盖或删除另一节点文件。

### 15.3 NFS 故障测试

- 短暂中断、超时、只读、容量耗尽和 stale handle。
- 挂载丢失后不会写入同名本地空目录。
- 故障期间本地 spool 有界增长和优先级淘汰。
- 恢复后按原 Batch ID 补发且查询无重复。
- 附件与可观测性共用 NFS 时的容量和 I/O 干扰测试。

### 15.4 查询测试

- Hive partitioning 和时间分区裁剪。
- 多节点、多小时和 Schema 演进的 `union_by_name` 查询。
- Histogram 跨节点聚合正确性。
- 时间、行数、内存、线程、临时空间和超时限制。
- 恶意路径、任意 SQL、外部 URL 和 Extension 加载被拒绝。
- 大规模目录下的规划时间、p95 首行延迟和取消行为。

### 15.5 平台测试

- Linux amd64、Linux arm64、Windows x64 的 DuckDB 加载和 Parquet 读写。
- Docker 本地卷和 NFS Volume。
- Windows 非 ASCII 和带空格路径。
- DuckDB 与 PHP Embed/FrankenPHP/ionCube 同进程启动、请求、查询和优雅退出。
- 24 小时混合 Web 负载、批量发布和管理查询稳定性。

## 16. 故障矩阵

| 故障 | 预期行为 |
|---|---|
| DuckDB 批次生成失败 | 保留 spool、有限重试、业务继续 |
| NFS 不可用 | 转本地 spool、状态 degraded、不写本地伪共享目录 |
| 本地 spool 容量耗尽 | 按优先级淘汰并告警，不耗尽系统盘 |
| Runtime 在临时文件阶段崩溃 | 最终文件不可见，启动后从 spool 重建 |
| Runtime 在 rename 后崩溃 | 发现最终文件后确认提交并删除 spool |
| 一个节点宕机 | 已发布数据仍可查询，另一节点继续独立发布 |
| Parquet 文件损坏 | 隔离文件并告警；有 spool 时重建 |
| 查询超时或内存超限 | 取消该查询，不影响 Web 和写入管线 |
| Schema 不兼容 | 拒绝错误写入；旧文件继续按旧版本读取 |

## 17. 上线顺序

1. 锁定 DuckDB/Go Binding 版本，完成三平台最小 Parquet PoC。
2. 定义 Schema v1、指标注册表、日志脱敏和目录契约。
3. 实现本地数据集、单写入器、批次发布和 CLI 查询。
4. 增加本地 spool、崩溃恢复和资源限制。
5. 在两个 Linux 节点的 NFSv4.1 上执行并发发布和故障测试。
6. 接入 Runtime/Caddy 指标与日志，再逐步接入 PHP、Scheduler、队列和缓存。
7. 灰度启用共享查询，观察 NFS I/O、文件大小、目录数量和业务 p95。
8. 达到验收门槛后再确定正式保留期；第一版不启用自动合并。

## 18. 验收标准

- Windows x64、Linux amd64、Linux arm64 和 Docker 能够使用同一 Schema 生成并读取 Parquet。
- 两个节点无需选主或全局锁，可以同时向共享数据集发布不同 part 文件。
- 查询永远看不到未完成的临时文件。
- 节点崩溃和重试不产生同 Batch ID 的重复最终文件。
- 任一节点宕机后，另一节点仍可查询已经发布的两节点历史数据并继续写入自身分区。
- NFS 故障不阻断禅道业务，恢复后可以从本地 spool 补发。
- 指标和日志中不包含已禁止的凭据、Session 和请求内容。
- 默认查询可以按时间和节点裁剪文件，并受到行数、内存、线程和超时限制。
- 可观测性启用后，业务请求 p95 增幅不超过 5%，且不会耗尽附件 NFS 或系统盘。

## 19. 实施状态（2026-08-19）

- DuckDB 1.5.5 / duckdb-go v2.10505.0 已编入默认 Linux 构建，并在
  amd64/arm64/Windows 三平台原生构建中通过链接验证。
- 事件信封、脱敏、Batch/Spool/原子发布、运行中自动补发、受控查询与
  节点自清理已实现并接入 Host；CLI 提供 `logs`、`metrics`、
  `flush-observability`、`clean-observability`、`collect-logs`。
- 真实 NFS 中断/双节点共享数据集故障注入仍需在有 NFS 的集成环境执行；
  当前以 spool 状态机与恢复单测覆盖，发布说明中列为未验证项。
