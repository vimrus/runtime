# MySQL 到 Parquet 增量同步设计（支持修改与一致性）

## 1. 目标与范围

- 把禅道业务库（MySQL，兼容 PostgreSQL/达梦）中的表同步为 Parquet 分析副本。
- 支持原始数据的插入、修改和删除，而不是只支持追加。
- 在“成本可控”和“尽量一致”之间选择可落地的机制，不承诺强一致。
- Parquet 是分析副本，不是业务事实来源；业务库始终是唯一事实来源。

## 2. 核心原则

1. Parquet 文件不可变：每次同步生成新的 `part-<batchID>.parquet`，不追加、不覆盖旧文件。
2. 可见性通过 Manifest 控制：查询只读 Manifest 里列出的 part，不直接 glob 全部文件。
3. 发布顺序：先写临时文件 → 校验 → rename → 更新 Manifest → 更新同步元数据 → 清理旧文件。
4. 幂等：`batchID` 由 `table + snapshotID` 稳定生成，重复执行不产生重复数据。
5. 失败只影响本次同步，不影响已发布的旧数据。
6. 多节点共享数据集时，每个节点只写自己的 `node=<nodeID>` 分区；Manifest 按表维护，不允许并发写同一张表的 Manifest。

## 3. 数据集布局

```text
parquet/
  manifest/
    <table>.json              # 每张表一个 Manifest，列出当前活跃 part
  <table>/
    schema=v1/
      date=YYYY-MM-DD/
        part-<batchID>.parquet
        .part-<batchID>.parquet.tmp
```

- `date` 是同步分区字段，粒度按表配置（day / week / month / year），
  示例使用 day；10 年历史的小表不应按天分区，避免产生数千个小文件。
- 对单分区过大的表，可在 `date` 下增加 `bucket=<id % N>` 二级目录；
  id 分桶稳定，行的修改/删除不会换桶，分区重写范围更小。
- `part-*.parquet.tmp` 不参与查询。
- Manifest 文件示例：

```json
{
  "table": "zt_bug",
  "schemaVersion": 1,
  "updatedAt": "2026-09-02T10:00:00Z",
  "parts": [
    {"path": "zt_bug/schema=v1/date=2026-09-01/part-b1.parquet", "date": "2026-09-01", "rows": 1234},
    {"path": "zt_bug/schema=v1/date=2026-09-02/part-b2.parquet", "date": "2026-09-02", "rows": 56}
  ]
}
```

## 4. 同步元数据表

同步元数据建议放在业务库新增的 `zt_duckdbsync` 表中；也可以放在每张表的
Manifest JSON 中，但 Manifest 更新必须原子且多节点同步同一张表时要加锁。
不建议从数据 Parquet 自身的 `max(id)` / `max(editedDate)` 反推水位，因为
DELETE 不产生新 id、UPDATE 可能不更新 editedDate、历史回填不会推进
`max(editedDate)`，都会导致漏同步。

`zt_duckdbqueue` 不再承担“脏表标记/触发同步”的职责；同步触发改为按时间窗口
扫描业务库（partition_rewrite / full_snapshot）或使用独立水位（append）。

```sql
CREATE TABLE zt_duckdbsync (
  id            bigint unsigned AUTO_INCREMENT PRIMARY KEY,
  object        varchar(255) NOT NULL,
  syncMode      varchar(20)  NOT NULL,          -- append / partition_rewrite / full_snapshot
  partitionField varchar(64) NOT NULL DEFAULT 'editedDate',
  partitionExpr varchar(255) NOT NULL DEFAULT 'COALESCE(editedDate, createdDate)',
  idChunkSize  bigint       NOT NULL DEFAULT 1000000,
  lastMaxID     bigint       NOT NULL DEFAULT 0,
  lastMaxEditedDate datetime DEFAULT NULL,
  lastSyncTime  datetime     DEFAULT NULL,
  lastSnapshotID varchar(64) DEFAULT NULL,
  status        varchar(20)  NOT NULL DEFAULT 'idle',  -- idle / running / failed
  UNIQUE KEY uk_object (object)
);
```

- `append`：追加型表，按 `id > lastMaxID OR editedDate > lastMaxEditedDate` 增量。
- `partition_rewrite`：可变表，重写受影响日期分区。
- `full_snapshot`：小表/无法可靠分区的大表，整体快照重写。
- `partitionExpr`：实际用于分区的日期表达式。新建行 `editedDate` 可能为
  NULL，必须用 `COALESCE(editedDate, createdDate/openedDate)` 之类表达式，
  避免新数据无法按时间分区；没有可用时间列的表直接使用 `full_snapshot`。

### 5.4 双水位语义（插入 / 修改 / 删除）

- 插入：新行一定产生更大的 `id`，所以 `id > lastMaxID` 一定能捕获新增。
- 修改/软删除：修改一定写入 `editedDate`，所以
  `editedDate > lastMaxEditedDate` 一定能捕获更新。
- 同一行在同一次同步周期内既插入又修改，会被两个条件重复捕获；写入前按
  `id` 去重，保留 `editedDate` 最大的一行。
- 硬删除：两种水位都捕获不到，只能靠“最近 N 天分区重写 / 每日 full_snapshot
  / binlog CDC”处理。

### 5.5 idChunkSize 选择

`idChunkSize` 决定一个 idrange 包含多少行，按表配置，不要全局写死。
优先按“生命周期总行数”倒推，而不是按行宽估算：

```text
idChunkSize ≈ 10年总行数 / 期望分区数
期望分区数：20～100 个
```

示例（10 年 200 万行的中表）：

| 期望分区数 | idChunkSize |
|---|---|
| 20 | 100000 |
| 40 | 50000 |
| 100 | 20000 |

初始建议：

| 表 | 特点 | idChunkSize |
|---|---|---|
| zt_user / zt_dept | 小表 | full_snapshot，不用 idrange |
| zt_product / zt_project | 10 年几十万行 | full_snapshot 或 20000～50000 |
| zt_bug / zt_task / zt_story | 10 年约 200 万行 | 50000～100000 |
| zt_action / zt_metriclib | 10 年约 5000 万行 | 500000～2500000，追加为主默认 2000000 |

更新频繁的表选小区间，追加为主的表选大区间。上线后按单个 idrange 重写
耗时和总分区数调整。

## 5. 三种同步模式

### 5.1 append（追加型历史表）

适合 `action`、`metriclib`、`effort`、日志类表：

```sql
SELECT * FROM zt_action
WHERE id > :lastMaxID
   OR editedDate > :lastMaxEditedDate;
```

- 新数据写为新 part，加入 Manifest。
- 旧 part 不动。
- 不支持修改/删除，表定义上必须保证只追加。

### 5.2 partition_rewrite（可变当前态表）

适合 `bug`、`story`、`task`、`project` 等高频修改表：

先用双水位找出“有变化的分区”，再对每个分区整分区重写：

```sql
-- 1) 找出需要重写的分区
SELECT DISTINCT COALESCE(editedDate, openedDate) AS _sync_date
FROM zt_bug
WHERE id > :lastMaxID
   OR editedDate > :lastMaxEditedDate;

-- 2) 对每个分区整分区重写
SELECT * FROM zt_bug
WHERE COALESCE(editedDate, openedDate) >= :partitionStart
  AND COALESCE(editedDate, openedDate) <  :partitionEnd;
```

- 每个同步周期只重写“有变化的分区”，而不是全表。
- 新 part 写入独立临时目录，校验后原子发布。
- 发布后 Manifest 中该 `date` 只指向新 part，旧 part 删除。
- UPDATE/DELETE 通过重写自然生效。
- 分区目录的 `date` 取自 `_sync_date`（即 `partitionExpr` 的计算结果）。
  新建行用创建时间分区，编辑后可能移动到 editedDate 分区，属于跨分区移动，
  按 7.2 的“最近 N 天重写 + 每日全量兜底”处理。
- 分区粒度按数据量配置：日增量大的表用 day；历史长且日增量小的表用
  month/year，再配合文件内的 `date` 列和 Parquet row-group 统计做裁剪。

### 5.3 full_snapshot（小表或兜底）

适合 `user`、`dept`、`group` 等小表：

- 每次全量 `SELECT * FROM table`。
- 整表写入 `date=snapshotDate/part-<snapshotID>.parquet`。
- Manifest 切换后删除旧版本。
- 一致性最简单，适合无法安全增量的大表兜底。

## 6. 写入与发布流程

以 `partition_rewrite` 为例：

```text
1. 从 zt_duckdbsync 读取上次同步状态。
2. 计算需要同步的分区范围。
3. 在业务库开启一致快照（MySQL: START TRANSACTION WITH CONSISTENT SNAPSHOT）。
4. 查询分区数据，写入 parquet/<table>/schema=v1/date=<date>/.part-<batchID>.parquet.tmp。
5. 校验：行数、schema、时间范围、无重复主键。
6. rename 为 part-<batchID>.parquet。
7. 生成新 Manifest（临时文件 -> rename 覆盖 <table>.json）。
8. 更新 zt_duckdbsync（lastSnapshotID/lastSyncTime/水位）。
9. 删除不再被 Manifest 引用的旧 part。
```

- 步骤 6 之前，查询仍读旧 Manifest。
- 步骤 7 是“原子可见点”：读者要么看到旧 Manifest，要么看到新 Manifest，不会混读。
- 步骤 8 失败：下次同步用相同 `snapshotID` 重试，幂等。

## 7. 修改与删除的一致性

### 7.1 分区内修改/删除

`partition_rewrite` 直接重写整个分区，天然支持 UPDATE 和 DELETE。

### 7.2 跨分区移动

如果行的 `editedDate` 变化导致它从一个 `date` 分区移动到另一个分区，只重写新分区会导致旧分区残留旧行。

处理方式：

1. 同步时同时重写“新分区”和“最近可能发生移动的分区”（例如最近 N 天）；或
2. 对该表定期执行一次 `full_snapshot` 兜底；或
3. 使用 MySQL binlog/CDC 捕获变更前后的分区归属。

推荐：首版选择“新分区重写 + 每日全量兜底”，把跨分区移动的窗口收敛到一天以内；复杂表后续再上 CDC。

### 7.3 删除

- `partition_rewrite` / `full_snapshot`：删除行不参与重写，重写后自然消失。
- `append` 模式不支持删除，只用于只追加表。

### 7.4 删除的发现机制

禅道大部分删除是软删除（`deleted=1`），本质是 UPDATE，分区重写即可处理。
真正的硬删除（DELETE）无法靠 `max(id)` 或 `max(editedDate)` 水位发现，只能
通过以下方式：

1. 周期重写“最近 N 天”分区：删除行的 `editedDate` 落在窗口内时，重写后
   自然消失；未落在窗口内的删除会延迟到该分区下次被重写。
2. 每日 `full_snapshot` 兜底：整表重写后所有硬删除都会消失。
3. binlog/CDC：捕获 DELETE 事件，拿到被删行的 `id` 和原 `editedDate`，
   精确重写对应分区，实时性最好。

示例：用户硬删除 `zt_bug` 中 `id=100`（原 editedDate=2026-09-02）。

- 只靠水位：看不到 id=100，旧 Parquet 会一直保留该行。
- 分区重写：扫描 `editedDate >= 2026-09-02` 的 MySQL 数据时 id=100 已不存在，
  新分区 part 不包含它；Manifest 切换后旧行消失。
- 如果删除发生在更早分区，则依赖“最近 N 天重写”或每日全量兜底最终收敛。

## 8. 查询层

BI 查询不再直接 glob `part-*.parquet`，而是：

1. 读取 `manifest/<table>.json`。
2. 按 `date` 过滤。
3. 生成 `read_parquet([...], union_by_name=true)` 语句。

好处：

- 不会读到临时文件、旧版本或未提交 part。
- 可以按 Manifest 里的 `rows` 做统计和校验。
- 未来可以平滑升级为 Delta/Iceberg 风格元数据。

## 9. 故障恢复与幂等

| 场景 | 行为 |
|---|---|
| 写临时文件失败 | 删除临时文件，Manifest 不变，任务标记 failed，下次重试 |
| rename 后 Manifest 更新前崩溃 | 新 part 存在但不在 Manifest，视为孤儿；下次用相同 snapshotID 重试，孤儿由清理任务删除 |
| Manifest 更新后元数据更新前崩溃 | 数据已可见，但水位未推进；下次重试会重新生成相同 batchID，最终文件已存在时跳过 |
| 查询读到旧 Manifest | 允许，最终一致 |
| 多节点并发同步同一张表 | 不允许，Manifest 更新前先取表级锁或使用单节点同步任务 |

## 10. 一致性边界（明确不承诺）

- 不提供跨表事务一致性：不同表在不同时间点可见。
- 不提供与业务库严格一致的实时性：同步是周期性的。
- 跨分区移动在首版最多保证“一天内收敛”。
- Parquet 是分析副本，参与事务/权限/审批的业务判断仍以业务库为准。

## 11. 落地步骤

1. 新增 `zt_duckdbsync` 元数据表和 Manifest 读写组件。
2. 先接入一张追加型表（`action`）验证增量 + Manifest + 查询。
3. 再接入一张可变表（`bug` 或 `task`）验证分区重写 + 修改/删除。
4. 增加一致性校验：对比业务库行数和 Parquet Manifest 行数。
5. 增加故障注入：写临时文件失败、rename 后崩溃、Manifest 更新后崩溃。
6. 与 Runtime 观测的 Parquet 数据集契约对齐，复用原子发布和清理逻辑。

### 11.1 文件数量与压缩

- 目标 part 大小建议 64MB～256MB，避免大量小文件。
- 低流量表优先使用 month/year 分区，而不是 day。
- 定期合并压缩：把小 part 合并成大 part，更新 Manifest 后删除旧 part。
- Manifest 只维护活跃 part，历史小文件被压缩后从 Manifest 移除。

## 12. 参考

- [可观测性设计](./05-observability.md)
- [Runtime 日志系统开发文档](../development/05-logging-system-development.md)
- 现有 BI 同步实现：`zentaopms/module/bi/model.php`、`zentaopms/module/bi/tao.php`
