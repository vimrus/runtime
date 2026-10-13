# DuckDB 数据方案设计（22.7 起仅日志域）

> **22.7 变更**：PHP 侧 DuckDB 已整体下线。付费扩展 `zentaomisc/duckdb`（本地 DuckDB CLI 与
> BI 驱动）、`zentaopms/lib/duckdb`、`zentaopms/lib/parquet`、`zt_duckdbqueue`、
> `zt_duckdbsync`，以及度量/BI 的 Parquet 同步（`biModel::syncParquetIncremental` 系列、
> queue handler `bi.syncIncremental`）全部移除；BI 与度量的数据源回到业务库
> （`bi/config.php` 的 `drivers = array('mysql', 'dm')`）。DuckDB 只保留 Runtime 内嵌
> Library，服务日志域的 Parquet 生成与受控查询。

## 1. 背景

DuckDB 在 22.6 及以前有三类用途：

1. 日志：Runtime/Caddy/PHP 日志先落 JSONL，再周期转为 Parquet。
2. 度量库：`zt_metriclib` 按实时、年、月、日维度保存业务度量。
3. 业务数据：报表和 BI 查询使用的业务表。

22.7 起收敛为第 1 类，原因：

- 本地 CLI 形态需要额外下载、授权和版本维护，且只在安装付费扩展后可用；
- 业务库始终是事实来源，Parquet 只是可重建的分析副本，同步复杂度（双水位、分区重写、
  热窗口、硬删除收敛）与收益不成正比；
- Runtime 内嵌 DuckDB 已能覆盖日志域的批量写入与受控查询。

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

- DuckDB 不需要任何数据库驱动或 Scanner，各平台行为一致。
- 方言差异全部收敛在 PHP DAO 一侧（22.7 后仅剩业务查询，不再有导出任务）。
- 不把业务库暴露给分析引擎，避免长查询、锁和凭据扩散。
- Parquet 可重建，发布失败不影响业务。

## 3. 数据域

| 数据域 | 来源 | 事实来源 | 分析副本 | 状态 |
|---|---|---|---|---|
| 日志域 | Runtime/Caddy/PHP JSONL | 原始 JSONL（短期） | Parquet | 保留 |
| 度量域 | `zt_metriclib`（PHP 计算后写库） | 业务库 | 无 | 22.7 移除 |
| BI 域 | 业务表（bug/story/task 等） | 业务库 | 无 | 22.7 移除 |

度量与 BI 的业务查询直接走业务库：`driver = 'mysql'` 时 `loadDriver()` 返回主库连接，
`driver = 'dm'` 时同样落在主库连接上；历史数据里残留的 `driver = 'duckdb'` 行在 22.7
升级时被回写为 `mysql`。

## 4. 总体数据流

```text
Runtime/Caddy/PHP 日志
  -> JSONL（按节点）
  -> Runtime Publisher 攒批
  -> parquet/logs/...

查询入口（zentao logs / Runtime 查询桥）
  -> 受控查询层（固定模板 + 白名单）
  -> DuckDB read_parquet(...) 与 active JSONL 合并
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

## 6. 数据集布局

```text
parquet/
  logs/       # Runtime/Caddy/PHP 日志
    schema=v1/date=YYYY-MM-DD/hour=HH/node=<nodeID>/part-*.parquet
  manifest/   # 每域/表一个 Manifest
```

只保留日志域一个数据集；`metrics/`、`bi/` 目录不再产生新数据。保留策略见
[可观测性设计](./05-observability.md) 第 10 章。

## 7. 查询统一层

所有查询经过同一个受控入口：

```text
zentao logs
  -> query router
  -> 校验 domain/时间范围/节点/行数白名单
  -> DuckDB read_parquet(...)
```

规则：

- 不允许用户提交任意 SQL、任意文件路径或任意 DuckDB 扩展。
- 查询模板由系统固定生成，参数只影响时间、节点、topic 和行数。
- 业务库查询（BI、度量、报表）走 PHP DAO，不允许 DuckDB 直连。
- 日志域例外：查询层在 DuckDB 返回 Parquet 结果后，合并 active/未转换
  JSONL 行。

## 8. 一致性与实时性

### 8.1 一致性

- 业务库是事实来源，Parquet 是可重建的分析副本。
- Manifest 原子切换，避免读到半提交数据。
- 同一份 Parquet 数据集可被多节点只读共享。

### 8.2 实时性分级

| 查询类型 | 数据来源 | 延迟 |
|---|---|---|
| 实时运维/日志 | JSONL + 内存状态 | 秒级 |
| 历史日志 | Parquet | 周期同步延迟 |
| BI / 度量 / 报表 | 业务库（PHP DAO） | 业务库实时 |

对“必须实时”的业务判断，不从 Parquet 读取。

## 9. 22.7 移除清单

| 层 | 移除内容 |
|---|---|
| 插件 | `zentaoext/zentaomisc/duckdb`（本地 CLI、BI 驱动覆盖、`enableDuckdb=true`、`bi.syncParquetFile` cron） |
| 引擎 | `zentaopms/lib/duckdb`、`zentaopms/lib/parquet`、`framework/router.class.php` 的 `loadDriver('duckdb')` 分支 |
| 写钩子 | `lib/base/dao/dao.class.php` 与信创 `pgsql/dm` DAO 中的 `zt_duckdbqueue` 脏表标记 |
| 表 | `zt_duckdbqueue`（22.7 升级脚本 DROP）、`zt_duckdbsync`（未发布，直接移除建表语句） |
| 模块 | `biModel` 的 `getDuckDBPath/initParquet/prepareCopySQL/generateParquetFile`、`publish*ToParquet`、`syncMetricLibDate`、`cleanMetricLibHotWindow`、`syncParquetIncremental`、`syncBITableByWatermark`、`syncBITableSnapshot`、`getParquetRoot`、`buildParquetQuery` 等；`biTao` 整文件；`bi` 控制层的 `syncParquetFile/syncIncremental/initParquet/ajaxInstallDuckdb/ajaxCheckDuckdb` |
| 配置 | `bi/config.php` 的 `cdnUrl/duckdbBin/duckdbExt/duckdbExtUrl/duckSQLTemp/duckdbExcludeTables/parquet*`、`columnTypes->duckdb`、`duckdb->ztvtables`；`config/config.php` 的 `enableDuckdb`；`TABLE_DUCKDBQUEUE`、`TABLE_DUCKDB_SYNC` |
| 队列 | `module/queue/config.php` 的 `bi.syncIncremental` handler |
| 数据 | 升级脚本把 `zt_chart/zt_pivot/zt_dataview` 的 `driver='duckdb'` 回写为 `mysql`，并清理 `bi.defaultDriver` 配置与 `bi.syncParquetFile` cron |

## 10. 参考

- [可观测性设计](./05-observability.md)
- [MySQL 到 Parquet 增量同步设计](./08-mysql-parquet-sync.md)（22.7 已废弃，仅作历史记录）
- [Runtime 日志系统开发文档](../development/05-logging-system-development.md)

## 11. 实施状态

- 日志域：JSONL 转换成功后删除（`jsonlDeleteAfterConvert`）已实现；
  `zentao logs` 已合并 active/未转换 JSONL 与 Parquet 结果。
- 日志域的 DuckDB/Parquet 实现全部在 `runtime/internal/observability/`，PHP 侧只通过 HTTP 桥交互。
- PHP 侧度量/BI 分析副本：22.7 已按第 9 章移除，业务查询回到业务库；
  升级脚本负责清理遗留表、配置与 `driver` 数据。
- 待验证：PostgreSQL/DM/Gauss 环境下 BI/度量查询走业务库的端到端矩阵。
