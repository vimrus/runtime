# DuckDB 统一数据方案设计（日志 / 度量库 / BI）

## 1. 背景与目标

DuckDB 在 zenat 体系中有三类用途：

1. 日志：Runtime/Caddy/PHP 日志先落 JSONL，再周期转为 Parquet。
2. 度量库：`zt_metriclib` 按实时、年、月、日维度保存业务度量。
3. 业务数据：报表和 BI 查询使用的业务表。

目标：

- 三类数据都能被 DuckDB 统一、受控地查询。
- MySQL、PostgreSQL、达梦（DM）、高斯（GaussDB）都能使用同一套方案。
- 不要求 DuckDB 直接连接或扫描业务数据库。
- Parquet 是分析副本；业务库和度量库始终是事实来源。

## 2. 关键决策：DuckDB 只读 Parquet，不直连业务库

达梦和高斯没有与 MySQL Scanner 等价的官方 DuckDB 扩展；即使 MySQL/PostgreSQL
有 Scanner，不同方言的“数据库 + Parquet 混合查询”也无法保证行为一致。

因此统一原则是：

```text
业务库（MySQL/PG/DM/Gauss）
  -> PHP DAO（方言适配）读取/计算
  -> 导出为 Parquet（统一格式）
  -> DuckDB 只读 Parquet
```

好处：

- DuckDB 不需要任何数据库驱动或 Scanner，三平台一致。
- 方言差异全部收敛在 PHP DAO 和同步任务里。
- 不把业务库暴露给分析引擎，避免长查询、锁和凭据扩散。
- 同步失败不影响业务，Parquet 可重建。

## 3. 三个数据域

| 数据域 | 来源 | 事实来源 | 分析副本 |
|---|---|---|---|
| 日志域 | Runtime/Caddy/PHP JSONL | 原始 JSONL（短期） | Parquet |
| 度量域 | `zt_metriclib`（PHP 计算后写库） | `zt_metriclib` 业务库 | Parquet |
| BI 域 | 业务表（bug/story/task/action 等） | 业务库 | Parquet |

每个域有独立的 Parquet 根目录和 Manifest，互不混写。

## 4. 总体数据流

```text
Runtime/Caddy/PHP 日志
  -> JSONL（按节点）
  -> Runtime Publisher 攒批
  -> parquet/logs/...

PHP 度量计算（按 scope/dateType）
  -> zt_metriclib（业务库，实时/年/月/日都先落库）
  -> 度量同步任务（PHP DAO 读库）
  -> parquet/metrics/...

业务表变更（MySQL/PG/DM/Gauss）
  -> 业务库
  -> BI 同步任务（PHP DAO 读库，idrange + 水位）
  -> parquet/bi/...

查询入口（CLI/BI 页面）
  -> 受控查询层（固定模板 + 白名单）
  -> DuckDB read_parquet(...)
```

## 5. 日志域

维持现有方案：

- 每个节点写 `runtime-<nodeID>.jsonl`、`access-<nodeID>.jsonl`。
- JSONL 段封闭后由转换器解析，批量进入 Publisher。
- Publisher 攒批（`minBatchRows/minBatchBytes/maxFlushInterval`）后生成
  `parquet/logs/schema=v1/date=YYYY-MM-DD/hour=HH/node=<nodeID>/part-*.parquet`。
- 无合规审计要求，原始 JSONL 在“转换成功并校验通过”后删除，不长期保留。
- 查询时 JSONL 只覆盖“尚未转换的 active/新段”，已转换历史只查 Parquet，
  因此不会出现 JSONL 与 Parquet 重复。
- 日志查询由查询层合并两部分结果：DuckDB 查 Parquet，运行时读 active/未转换
  JSONL（复用 JSONL 解析器，忽略最后不完整行），按 eventTime 合并排序。

### 5.1 JSONL 生命周期

```text
写入 active JSONL
  -> 轮转封闭为段文件
  -> 转换器解析并攒批发布 Parquet
  -> 校验 Parquet（行数/schema/可读性）
  -> 删除已封闭段文件
```

删除前必须确认 Parquet 已成功发布；转换失败时保留段文件并重试或隔离。

## 6. 度量域

### 6.1 先落业务库，再同步 Parquet

实时、年、月、日维度的度量都必须先写入 `zt_metriclib`，原因：

- 业务库是事务事实来源，支持校验、补算、审计。
- MySQL/PG/DM/Gauss 都可以用 PHP DAO 写入，不依赖 DuckDB 方言。
- DuckDB 不直接连接业务库，因此“高斯/达梦无法用 DuckDB 统一读库”的问题
  被规避：DuckDB 只读同步后的 Parquet。

### 6.2 不同维度使用不同同步频率

| 维度 | 数据特征 | 同步频率 | 存储 |
|---|---|---|---|
| 实时/今日 | 变化快 | 1～5 分钟 | 今日分区 + 业务库兜底 |
| 日 | 按天新增 | 小时～天 | date/day 分区 |
| 月 | 月末固化 | 天 | month 分区 |
| 年 | 年末固化 | 天 | year 分区 |

`zt_metriclib` 只保留热窗口：年度保留今年、月度保留本月、周度保留本周、
日度保留今天，无日期（最新）行保留。热窗口之外的数据在成功 flush 到
Parquet history 并校验后从业务库删除。

### 6.3 度量 Parquet 布局

```text
parquet/metrics/
  schema=v1/
    metriclib/
      scope=current/
        date=2026-09-02/
          part-<batchID>.parquet
      scope=history/
        date=2025-12-31/
          part-<batchID>.parquet
```

current 与 history 使用独立 Manifest；当前周期高频整分区重写不会影响历史
分区，周期关闭时再把 current 数据发布/合并到 history。

Manifest 记录 `minDate/maxDate`，支持按时间裁剪。

## 7. BI 域

按 [MySQL 到 Parquet 增量同步设计](./08-mysql-parquet-sync.md) 实施：

- idrange 分区（`FLOOR(id / idChunkSize)`），新增只影响当前分区。
- 双水位：`lastMaxID` 捕获插入，`lastMaxEditedDate` 捕获修改/软删除。
- Manifest 控制原子可见性。
- 硬删除通过“定期轮转重写所有 idrange / 每日全量 / CDC”收敛。
- `idChunkSize` 按表配置，参考 5.5 节。

## 8. 统一数据集布局

建议一个统一数据集根目录，按域隔离：

```text
parquet/
  logs/       # Runtime/Caddy/PHP 日志
  metrics/    # zt_metriclib 分析副本
  bi/         # 业务报表表
  manifest/   # 每域/表一个 Manifest（可选）
```

域之间不允许混写；每个域可有自己的保留策略：

- logs：按节点日期保留。
- metrics：按 metricCode/date 保留。
- bi：按表和 idrange 保留。

## 9. 查询统一层

所有查询经过同一个受控入口：

```text
zentao logs / zentao metrics / BI 报表
  -> query router
  -> 校验 domain/table/时间范围/白名单
  -> DuckDB read_parquet(...)
```

规则：

- 不允许用户提交任意 SQL、任意文件路径或任意 DuckDB 扩展。
- 查询模板由系统固定生成，参数只影响时间、域、表、指标名、节点、行数。
- 业务库查询（实时兜底）走 PHP DAO，不允许 DuckDB 直连。
- 日志域例外：查询层在 DuckDB 返回 Parquet 结果后，合并 active/未转换
  JSONL 行；度量域和 BI 域只查 Parquet。

## 10. 一致性与实时性

### 10.1 一致性

- 业务库是事实来源，Parquet 是可重建的分析副本。
- Manifest 原子切换，避免读到半提交数据。
- 同一份 Parquet 数据集可被多节点只读共享。

### 10.2 实时性分级

| 查询类型 | 数据来源 | 延迟 |
|---|---|---|
| 实时运维/日志 | JSONL + 内存状态 | 秒级 |
| 今日度量 | 业务库兜底 + 高频同步 Parquet | 分钟级 |
| 历史日志/度量/BI | Parquet | 周期同步延迟 |

对“必须实时”的业务判断，不承诺从 Parquet 读取；仍以业务库为准。

## 11. 方言兼容验证

每个同步任务只使用 PHP DAO 的方言层，因此：

- MySQL：验证增量、水位、分区重写。
- PostgreSQL：验证 SQL 方言和类型映射。
- 达梦/高斯：验证 DAO 查询与导出结果一致。

DuckDB 侧不引入任何数据库 Scanner/Extension，三平台行为一致。

## 12. 实施阶段

1. 日志域：维持现有 Runtime Publisher，完成 JSONL→Parquet。
2. 度量域：`zt_metriclib` 先落库；增加度量同步任务（今日高频、历史低频）。
3. BI 域：按 08 文档实现 idrange + 双水位 + Manifest。
4. 统一查询层：domain 路由 + 固定模板 + 白名单。
5. 方言矩阵：MySQL/PG/DM/Gauss 各跑一轮同步与查询契约测试。

## 13. 边界与风险

- DuckDB 不承诺跨域事务一致性，只提供分析副本。
- 实时查询依赖业务库，Parquet 只做趋势和历史分析。
- idrange 大小和同步频率需要按表调优。
- 不共享可写 `.duckdb` 文件；如需要本地加速，使用节点本地缓存。

## 14. 参考

- [可观测性设计](./05-observability.md)
- [MySQL 到 Parquet 增量同步设计](./08-mysql-parquet-sync.md)
- [Runtime 日志系统开发文档](../development/05-logging-system-development.md)

## 15. 实施状态

- 日志域：JSONL 转换成功后删除（`jsonlDeleteAfterConvert`）已实现；
  `zentao logs` 已合并 active/未转换 JSONL 与 Parquet 结果。
- PHP 侧基础库：`zentaopms/lib/parquet/parquet.class.php` 已提供 Manifest
  原子读写、路径布局和 idrange 计算，供 metrics/bi 两类场景共用。
- `zt_duckdbsync`：22.6 升级 SQL 已建表，BI Tao 已提供状态读写与表级锁
  （fetch/save/lock/unlock）。
- PHP 发布层：`publishRowsToParquet`（idrange，BI）与
  `publishMetricRowsToParquet`（date，度量）共用 CSV→DuckDB→Parquet 转换；
  `syncMetricLibDate` 已提供按日整分区重写入口。
- 度量生命周期：`scope=current/history` 独立分区与 Manifest 已实现；
  `cleanMetricLibHotWindow` 提供热窗口清理（今年/本月/本周/今天，无日期保留）。
- BI 同步：`syncBITableByWatermark` 已按白名单 + 双水位取数 + idrange 整分区
  重写实现，并更新 `zt_duckdbsync` 水位。
- 查询路由：`buildParquetQuery(domain, table, dateRange, where)` 已按
  metrics/bi 分域从 Manifest 生成只读 `read_parquet` SQL；logs 域由 Runtime
  `zentao logs` 负责。
- 真实 DuckDB v1.5.5 CLI 已验证：CSV→Parquet 批量转换、Manifest→
  read_parquet 查询闭环通过。
- MySQL 真实端到端已验证：新建测试库 `zentao_parquet_test`，PDO 读取
  `zt_metriclib` → CSV → Parquet（scope=current）→ DuckDB 查询；热窗口
  清理 SQL 验证只删除历史行、保留当前行。
- 待验证：PostgreSQL/DM/Gauss 端到端同步矩阵（需对应数据库环境）。
