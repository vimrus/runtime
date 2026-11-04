# BI 业务数据 Parquet 同步与 DuckDB 查询方案

## 1. 文档信息

| 项目 | 内容 |
| --- | --- |
| 状态 | 同步、Parquet 发布、DuckDB 查询和源码级验收已实现，OLAP 功能默认开启 |
| 日期 | 2026-10-26 |
| 责任范围 | Runtime、禅道 BI、数据库兼容层 |
| 目标数据源 | MySQL、PostgreSQL、DM、Gauss 等禅道业务数据库 |
| 查询引擎 | MySQL、PostgreSQL、DuckDB |
| 永久事实来源 | 禅道业务数据库 |
| 分析副本 | Parquet |

本文定义新的 BI 业务数据同步与 DuckDB 查询方向，并作为 BI 域设计基线。
Runtime 侧的 Manifest、Parquet Publisher、DuckDB 查询桥、应用隔离 Token
以及 PHP 侧的导出、状态租约、初始全量和增量运行器已经实现。三引擎结果
比较、完整 IPD 合并制品、大屏引用闭包和 PHP 到 DuckDB 的联合测试均已通过。
`[olap]` 默认开启，Runtime 提供 OLAP Parquet 发布和 DuckDB 查询能力。

22.7 移除的 [MySQL 到 Parquet 增量同步设计](./08-mysql-parquet-sync.md)
仅作为历史参考；[统一 DuckDB 方案](./09-unified-duckdb-design.md)中的日志域
职责仍有效，BI 域按本文新增。

## 2. 背景与目标

BI 图表、透视表、大屏和自定义 SQL 当前直接读取业务数据库。数据量增长后，复杂 JOIN、聚合和跨多表扫描会占用业务数据库的 CPU、IO 和连接资源。

目标：

- BI 历史查询从不可变 Parquet 读取，由 Runtime 内嵌 DuckDB 执行。
- 显著降低 BI 查询对业务数据库的资源占用。
- 保留业务数据库作为唯一事实来源。
- 数据发布对业务事务无侵入，同步失败不回滚业务。
- 单表数据发布具有原子可见点。
- 查询可定位到明确的数据时间点，不被发布中的文件干扰。
- 所有内置 BI SQL 同时兼容 MySQL、PostgreSQL 和 DuckDB。
- 保持 MySQL、PostgreSQL、DM/Gauss 等来源数据库兼容边界。

非目标：

- 不让 Go Runtime 直接连接业务数据库。
- 不把 Parquet 用作事务、权限、审批或业务状态的判断依据。
- 不承诺跨表、跨业务事务的强一致快照。
- 不要求 MySQL、PostgreSQL、DuckDB 使用完全相同的 SQL 字符串。
- 首版不要求所有数据库都具备 CDC/准实时能力。

## 3. 已确认的核心结论

1. 保留 MySQL 风格 SQL 作为内置 BI 查询的逻辑源。
2. 通过 SQL 编译器生成目标方言 SQL，不维护三份内置 SQL。
3. 现有 MySQL→PostgreSQL 格式化器和兼容函数继续复用。
4. 为 DuckDB 增加独立的只读 SQL 格式化器、兼容 Macro 和查询桥。
5. 所有内置 BI SQL 必须通过 MySQL、PostgreSQL、DuckDB 三引擎执行测试。
6. 大屏不单独维护 SQL；测试必须覆盖大屏引用到的图表和透视表 SQL。
7. 同步链路采用“PHP DAO 分块导出 → Runtime Parquet Publisher → Manifest → DuckDB 查询”。
8. 首版采用 `snapshot`、`append`、`rewrite` 三种模式。
9. 硬删除首版依靠全量对账收敛；CDC 作为后续增强。
10. BI 数据集独立于 Observability 数据集，不复用日志 Event Schema。

“SQL 兼容”的准确含义：

```text
同一份 SQL 逻辑源
  -> 目标方言格式化/兼容函数转换
  -> 在所有声明支持的引擎上成功执行
  -> 返回列、类型和业务语义一致
```

要求 SQL 原文完全一致不可行，至少存在以下不可统一项：

- 反引号与双引号标识符。
- `LIMIT offset,count` 与 `LIMIT count OFFSET offset`。
- MySQL `DATE_SUB(..., INTERVAL n DAY)` 语法。
- 不同数据库的字符串 Collation 和 NULL 排序。

## 4. 总体架构

```text
MySQL / PostgreSQL / DM / Gauss
              |
              | PHP DAO + 数据库方言适配
              v
       PHP OLAP Sync Service
              |
              | 按表、按变更分区、分批导出
              v
     本地 NDJSON staging
              |
              | Runtime OLAP Bridge
              v
      Runtime OLAP Publisher
              |
              | DuckDB: staging -> immutable Parquet
              | validate -> atomic rename -> Manifest
              v
       OLAP Parquet Dataset
              |
              | Manifest logical views
              v
      Runtime OLAP Query Service
              |
              | 受控 DuckDB SQL
              v
         禅道 BI UI
```

职责分离：

| 组件 | 负责 | 不负责 |
| --- | --- | --- |
| PHP OLAP Sync | 读取业务库、数据库方言、增量范围、水位、权限前置检查 | DuckDB/Parquet 文件格式和查询资源控制 |
| Runtime OLAP Publisher | Schema 校验、Parquet 写入、原子发布、Manifest、回收 | 连接业务库、理解业务表关系 |
| Runtime OLAP Query | SQL 只读校验、逻辑视图、DuckDB 查询、资源和超时限制 | 用户权限、业务事务、任意文件访问 |
| OLAP SQL Compiler | MySQL 逻辑 SQL 到目标方言的转换 | 数据同步和查询执行 |
| 业务数据库 | 唯一业务事实来源 | 不依赖 Parquet 才能保持正确性 |

## 5. BI SQL 三引擎兼容

### 5.1 当前 SQL 规模

当前开源版内置：

| 类型 | 数量 |
| --- | ---: |
| 图表 SQL | 182 |
| 透视表 SQL | 30 |
| 数据视图 SQL | 15 |
| 大屏 JSON | 9 |

大屏 JSON 主要引用图表和透视表 ID，不大量内嵌独立 SQL。因此测试清单应按引用关系展开，覆盖大屏实际可达的查询。

企业版、旗舰版和 IPD 版本必须在合并后的应用制品上重新生成清单，不能只测试开源版源码。

### 5.2 已有兼容基础

项目已经有一套 MySQL 风格 SQL 的兼容基础：

- `lib/dbh/dbh.class.php::formatSQL()`：按数据库驱动选择格式化逻辑。
- `formatPgSQL()`：处理标识符、函数、`LIMIT`、PostgreSQL 函数等。
- `formatField()`：反引号转双引号及字段引用处理。
- `db/pgsql_function.sql`：提供 `IF`、`IFNULL`、`DATEDIFF`、`DATE_FORMAT`、`FIND_IN_SET`、`GROUP_CONCAT`、`TO_DAYS` 等兼容函数。
- BI 内置 SQL 已修复过一批驼峰字段引用、日期函数、整数除法和 PostgreSQL 兼容问题。
- 已知问题集中在 `docs/module/bug.md`。

该路线继续保留，并扩展到 DuckDB。

### 5.3 方言分层

#### 第一层：通用格式转换

这些差异可稳定、低风险转换：

| MySQL 写法 | 目标处理 |
| --- | --- |
| 反引号标识符 | PostgreSQL/DuckDB 转为双引号或安全标识符 |
| `LIMIT offset,count` | 转为 `LIMIT count OFFSET offset` |
| `IF / IFNULL` | 可逐步改写为 `CASE WHEN / COALESCE` |
| `DATE_SUB(x, INTERVAL n DAY)` | 按目标引擎生成合法日期表达式 |
| 驼峰字段 | 按目标方言引用 |

格式转换不得使用不受控的字符串全局替换。应基于 SQL Parser 或受限 Tokenizer，至少能区分字符串常量、函数参数和标识符。

#### 第二层：兼容函数

以下函数由 PostgreSQL 兼容函数和 DuckDB Macro 承担：

| 函数 | MySQL | PostgreSQL | DuckDB |
| --- | --- | --- | --- |
| `IF` | 原生 | 兼容函数 | 原生 |
| `IFNULL` | 原生 | 兼容函数 | 原生 |
| `DATE_FORMAT` | 原生 | 已有兼容函数 | DuckDB Macro → `strftime` |
| 两参数 `DATEDIFF` | 原生 | 已有兼容函数 | DuckDB Macro → `date_diff('day', ...)` |
| `DATE_SUB` | 原生，语法不同 | 兼容函数 + 格式化 | DuckDB Macro + 格式化 |
| `FIND_IN_SET` | 原生，返回位置 | 兼容函数保持布尔语义 | DuckDB Macro 返回位置/0 |
| `TO_DAYS` | 原生 | 已有兼容函数 | 编译期展开为 `date_diff` 表达式 |
| `LEFT` | 原生 | 原生 | 编译期展开为 `substr` |
| `GROUP_CONCAT` | 原生 | 已有聚合函数 | 原生，但需检查顺序 |
| `TRUNCATE` | 原生 | 已有兼容函数 | DuckDB Macro |

DuckDB Macro 必须是 Runtime 内置、版本化、只读加载的基础设施，不允许用户 SQL 自行创建或安装扩展。

#### 第三层：语义归一

下列差异即使“都能执行”也可能产生不同结果，必须人工审计或修改 SQL：

| 问题 | 影响 | 处理原则 |
| --- | --- | --- |
| `CONCAT` 遇 NULL | MySQL 返回 NULL；PostgreSQL/DuckDB 忽略 NULL | 对可能为 NULL 的参数显式 `COALESCE`，或生成方言表达式 |
| 字符串 Collation | MySQL 常不区分大小写，PostgreSQL/DuckDB 默认不同 | 禁止依赖隐式 Collation；必要时显式 `LOWER` |
| 整型除法 | MySQL 与其他引擎结果不同 | 比例计算显式 `* 1.0` 或 Decimal |
| NULL 排序 | 默认位置可能不同 | 显式排序表达式，不依赖默认 NULL 位置 |
| `GROUP_CONCAT` 顺序 | 未指定顺序时不稳定 | 不依赖隐式顺序；需要稳定输出时显式排序 |
| 零日期 | MySQL 可存 `0000-00-00`，其他引擎/Parquet 不接受 | 同步时转 NULL 或约定哨兵值 |
| `FIND_IN_SET` 返回值 | MySQL/DuckDB 返回位置，PostgreSQL 兼容函数返回布尔 | 内置 SQL 仅把结果用于 `WHERE/ON` 条件，不读取具体位置 |

### 5.4 实测结果

调查使用 MySQL 8.0.46、PostgreSQL 16.14、DuckDB CLI 1.5.3 进行最小表达式验证。正式 Runtime 使用 DuckDB 1.5.5，最终测试必须锁定正式版本重新执行。

| 表达式 | MySQL | PostgreSQL | DuckDB | 结论 |
| --- | --- | --- | --- | --- |
| `IF` | 通过 | 通过兼容函数 | 原生通过 | 可兼容 |
| `IFNULL` | 通过 | 通过兼容函数 | 原生通过 | 可兼容 |
| `DATE_FORMAT` | 通过 | 通过兼容函数 | 需 Macro | 可兼容 |
| `DATEDIFF(a,b)` | 通过 | 通过兼容函数 | 需二参数 Macro | 可兼容 |
| `DATE_SUB(..., INTERVAL)` | 通过 | 当前语法失败 | 原生函数不匹配 | 格式化器必须改写 |
| `FIND_IN_SET` | 返回位置 | 兼容函数返回布尔 | Macro 返回位置 | 条件语境可兼容 |
| `TO_DAYS` | 通过 | 通过兼容函数 | 编译期展开 | 可兼容 |
| `GROUP_CONCAT` | 通过 | 通过兼容函数 | 通过 | 需审计顺序 |
| `CONCAT('a', NULL)` | 返回 NULL | 返回 `'a'` | 返回 `'a'` | 源码需规范 |

调查没有发现必须把 227 条报表 SQL 和 54 条透视下钻 SQL 全部改写成另一种
SQL 的结构性障碍。主要工作量集中在函数兼容、语义审计和完整回归测试。
透视下钻不是独立模板，而是“对象表 + 内置 whereSql + 运行时条件”生成；
测试按实际生成结构编译，不把 whereSql 片段当成完整 SQL。

真实 Schema 校验又发现并修复了以下跨引擎问题：

- `zt_release.project/build` 和 Bug 的 `resolvedBuild` 是逗号分隔字符串，
  统一使用 `FIND_IN_SET(...) > 0`，避免单值 CAST 丢失多值语义。
- PostgreSQL/DuckDB 严格执行 `GROUP BY`，选择列必须显式分组或聚合。
- `YEAR(CONCAT(...))` 显式先转 `DATE`，不依赖 MySQL 的字符串隐式转换。
- DuckDB 没有可用于 `LEFT()` 的同名 Macro（`LEFT` 是语法关键字），编译器将
  受限调用改写为 `substr(CAST(value AS VARCHAR), 1, n)`。
- `YEAR/MONTH` 别名统一使用小写引用，避免 PostgreSQL 未引用别名被折叠。
- 字符串表达式与数值兜底值混用 `IFNULL` 时显式使用字符串兜底。

### 5.5 SQL 编译器

建议新增与数据库连接解耦的 BI SQL Compiler：

```text
compile(string $sql, string $target):
  target = mysql | pgsql | duckdb
```

职责：

- 解析或受限切分 SQL。
- 拒绝非只读语句。
- 校验引用表在允许集合中。
- 处理标识符、分页和日期语法。
- 应用目标方言函数。
- 生成稳定的目标 SQL。
- 返回使用的兼容函数版本。

可以抽取现有 `formatPgSQL()` 中的通用能力，但不建议直接让 `dbh` 承担 DuckDB 查询。DuckDB 不是业务数据库连接，也不应伪装成 `loadDriver()` 的数据库驱动。

存量 SQL 的处理规则：

- 内置配置 SQL 以源码为事实来源，由安装/升级流程更新 `builtin=1` 的记录。
- 用户自建 SQL 不做批量重写；进入 DuckDB 前由编译器做兼容转换，无法转换时给出明确错误。
- 已安装实例的升级测试必须包含旧版 `zt_chart`、`zt_pivot`、`zt_dataview` 数据。
- 编译器版本和兼容函数版本必须进入 Runtime 状态信息，便于定位跨节点差异。

### 5.6 DuckDB 兼容 Macro

Runtime 当前内置兼容版本 2。以下 Macro 在查询前由 Runtime 自动加载，
不读取用户配置，也不安装 Extension：

```sql
CREATE MACRO date_format(d, fmt)
AS strftime(d, fmt);

CREATE MACRO datediff(a, b)
AS date_diff('day', CAST(b AS DATE), CAST(a AS DATE));

CREATE MACRO date_sub(d, i)
AS d - i;

CREATE MACRO find_in_set(target, list)
AS CASE
     WHEN list IS NULL OR list = '' THEN 0
     ELSE COALESCE(
       list_position(string_split(list, ','), CAST(target AS VARCHAR)),
       0
     )
   END;
```

不能把 `TO_DAYS` 注册为 DuckDB Macro。DuckDB 内部用 `to_days(integer)` 表示
`INTERVAL`，同名 Macro 会破坏 `INTERVAL '1' DAY` 等日期运算。因此 SQL 编译器
把 MySQL 的 `TO_DAYS(date)` 展开为：

```sql
(date_diff('day', DATE '0001-01-01', CAST(date AS DATE)) + 366)
```

该约束由 Macro 单测和编译测试覆盖。

### 5.7 SQL 编写规则

新增或修改内置 SQL 必须：

- 使用 MySQL 风格作为逻辑源。
- 不使用未登记的 MySQL 专有函数。
- 不依赖字符串大小写不敏感。
- 不依赖 NULL 默认排序。
- 不依赖 `HAVING` 中的 SELECT 别名。
- 比例计算显式使用浮点或 Decimal。
- 不依赖 `GROUP_CONCAT` 的隐式顺序。
- 日期格式只使用已实现并测试的格式符。
- 不使用零日期参与比较。
- 透视表下钻的 `object` 必须是已同步逻辑表或其允许的 `ztv_*` 视图；不得
  直接依赖业务库物理前缀。
- 通过三引擎 Lint、Explain 和结果测试。

允许使用已审核的兼容函数，但不能通过自由增加数据库函数解决单个报表问题。

## 6. BI 数据同步方案

### 6.1 同步表清单

同步范围以 BI 实际可查询表为基础，而不是同步整个业务库。

当前从 227 条内置 SQL 提取出 26 张来源表，均已进入同步白名单：

| 分类 | 表 |
| --- | --- |
| 维度 | `zt_user`、`zt_dept`、`zt_product`、`zt_productplan`、`zt_project`、`zt_module`、`zt_team`、`zt_config`、`zt_compile` |
| 研发事实 | `zt_story`、`zt_task`、`zt_bug`、`zt_build`、`zt_release`、`zt_doc`、`zt_projectproduct`、`zt_projectstory` |
| 测试数据 | `zt_case`、`zt_casestep`、`zt_testtask`、`zt_testrun`、`zt_testresult`、`zt_testsuite` |
| 高频分析 | `zt_todo`、`zt_effort`、`zt_action` |

该清单由内置图表、透视表、数据视图和大屏引用关系生成，测试会在 SQL 新增
未登记表时失败。

`zt_compile` 由部分版本或 CI 插件创建，属于可选来源表。来源表不存在时，
同步器使用版本内固定 Schema 发布空 Parquet，使相关图表稳定返回空结果，
而不是让整个 BI 数据集永久无法 ready。必选表缺失仍返回失败。

禁止默认同步：

- `zt_config` 等系统配置表。
- 密码、Token、Session、许可证和私钥字段。
- 附件正文、完整请求/响应和大文本字段。
- 未纳入 BI 白名单的表。

### 6.2 同步规格

每张表使用版本化同步规格：

```text
sourceTable
primaryKey
columns
excludedColumns
syncMode
watermarkColumn
idChunkSize
targetPartBytes
fullReconcileInterval
deletionPolicy
```

规格属于应用代码，不允许管理员在页面任意编辑 SQL 或表名。

### 6.3 同步模式

#### snapshot

适用于小维度表：

- 读取整表。
- 发布新的完整快照 Manifest。
- 自动处理更新和删除。
- 新快照发布成功后切换可见版本。

#### append

只用于经过验证的追加型表：

- 按主键或时间水位读取新增记录。
- 旧 part 保持不变。
- 不支持更新和删除。
- 必须提供数据不可变的测试或约束。
- 首次同步和到期全量对账使用 `snapshot` 完整替换，避免初始缺失并限制
  append part 数量；`zt_action` 当前每日执行一次全量替换。

#### rewrite

当前适用于 Bug、任务、需求、项目和用例等可变表：

- `rewrite` 表示按稳定 bucket 重写受影响分区。
- 使用稳定的数值主键区间或 hash bucket 分区。
- 根据 ID 和 `lastEditedDate` 水位找出变更 bucket。
- 重新读取整个 bucket 并生成新 part。
- Manifest 原子切换到新 part。
- bucket 内更新、软删除和硬删除自然消失。

优先使用数值 ID 区间，避免按日期分区导致记录修改后跨分区移动。

迁移为 `zt_bug`、`zt_task`、`zt_story`、`zt_project`、`zt_case` 增加
`idx_lastEditedDate` 索引；同步配置测试会校验每个 rewrite 表的时间水位
都有对应索引，防止 15 分钟增量退化为全表扫描。

### 6.4 水位和幂等

建议新增业务库同步状态表，暂定名 `zt_bisync`：

| 字段 | 含义 |
| --- | --- |
| `sourceTable` | 来源表 |
| `syncMode` | 同步模式 |
| `schemaVersion` | BI Schema 版本 |
| `watermarkID` | 已成功发布的 ID 水位 |
| `watermarkTime` | 已成功发布的时间水位 |
| `activeGeneration` | 当前查询可见的 Manifest generation |
| `status` | idle/running/ready/failed |
| `leaseOwner` | 同步持有者 |
| `leaseEnd` | 租约过期时间 |
| `lastErrorCode` | 稳定错误码 |
| `lastError` | 脱敏错误信息 |
| `lastSuccessAt` | 最近成功时间 |
| `lastFullAt` | 最近一次全量 bucket 对账完成时间 |

约束：

- `sourceTable` 唯一；Schema 升级时替换该表的同步状态和数据集版本。
- 同步任务必须有租约，避免多节点并发写同一张表。
- 每次批次使用稳定 `batchID`，重试幂等。
- 水位必须保留安全重叠窗口，避免事务提交晚于查询读取。
- 发布成功但水位更新失败时，用同一 batch 重试，不生成重复数据。
- 普通增量成功只推进 `lastSuccessAt`；周期全量完成才推进 `lastFullAt`，
  避免高频增量不断重置全量对账周期。
- append 表执行周期全量时，以读取前 `MAX(id)` 为上界生成快照，避免读取
  期间新增的记录被快照提前包含后又被下一轮 append 重复追加。

### 6.5 初始全量

- 分表、分 bucket 执行，避免单个长事务。
- 按主键范围或稳定 bucket 分批读取。
- 单个 staging 分块建议控制在 64–256 MB 的目标 Parquet 尺寸附近。
- 支持断点续传和按表重跑。
- 初始全量未全部完成前，BI 使用业务库。

当前 `rewrite` 表的首次同步从 bucket 0 发布到当前最大 ID 所在 bucket。
没有记录的空 bucket 也发布带完整 Schema 的空 Parquet，避免后续 bucket
重写产生“未初始化”歧义。所有 bucket 完成后才写入 `lastSuccessAt` 和
`lastFullAt`；在此之前，即使已经出现部分 Manifest，PHP 也不会把查询
切到 DuckDB。

### 6.6 硬删除

- 软删除通过 `editedDate` 更新捕获。
- 小表通过周期全量快照收敛。
- 大表通过周期性 bucket 对账收敛。
- 需要精确捕获硬删除时，再评估 MySQL Binlog、PostgreSQL WAL 和对应 CDC。
- 首版不把 CDC 作为跨数据库统一前提。

## 7. Staging 与 Parquet 发布

### 7.1 Staging 格式

推荐 PHP 输出 NDJSON：

```text
<app-data>/olap-staging/<syncID>/
  table=zt_bug/
    schema.json
    chunk-000001.ndjson
    chunk-000002.ndjson
    complete.json
```

选择 NDJSON 的原因：

- PHP 无新增 Arrow/Parquet 二进制依赖。
- 比 CSV 更能保留 NULL、字符串和复杂字段。
- Runtime 可以使用显式 Schema 读取，避免自动推断。
- 可以按块校验和清理。

`schema.json` 至少包含：

- Schema 版本。
- 列名、类型、是否可空。
- 主键和分区键。
- 来源表与同步模式。
- 行数、block 列表和校验和。

同步完成后删除 staging。staging 目录不得位于 Web 根目录，权限至少 `0700`。

### 7.2 Runtime Publisher

Runtime 新增独立 `internal/olap` 域：

```text
validate schema
  -> parse NDJSON
  -> DuckDB write .parquet.tmp
  -> validate row count/schema
  -> atomic rename part
  -> atomically replace table manifest
  -> notify PHP commit
```

不能直接复用 Observability Publisher：

- Observability Event Schema 固定且主要追加。
- BI 表字段可变、需要主键和 `rewrite`。
- BI Manifest 必须描述逻辑表当前可见 part。

每个逻辑表除进程内互斥外，还使用 Manifest 目录下的跨进程文件锁和
generation CAS。PHP 状态租约是第一层单写保护；文件锁用于覆盖租约过期、
错误抢占或多 Runtime 实例共享同一应用目录时的临界区。

可以复用：

- DuckDB Library 初始化。
- Parquet 原子写入。
- 文件校验。
- 同一文件系统 rename。
- owner marker 和资源限制。

### 7.3 数据集布局

```text
data/apps/<app>/olap/
  tables/
    zt_bug/
      schema=v1/
        bucket=000/part-<batchID>.parquet
        bucket=001/part-<batchID>.parquet
  manifests/
    zt_bug.json
  staging/
```

Manifest 示例：

```json
{
  "schemaVersion": 1,
  "table": "zt_bug",
  "generation": 42,
  "publishedAt": "2026-10-26T10:00:00Z",
  "rowCount": 123456,
  "parts": [
    {
      "path": "tables/zt_bug/schema=v1/bucket=000/part-b42-000.parquet",
      "rows": 50000,
      "sha256": "..."
    }
  ]
}
```

查询只读取 Manifest 列出的 part，禁止直接 glob 数据目录。

### 7.4 可见性和清理

- 新 part 原子 rename 后才允许进入 Manifest。
- Manifest 原子替换是唯一可见点。
- 查询开始时固定 Manifest generation。
- Manifest 切换后保留旧 part 至少一个查询宽限期，例如 1 小时。
- 宽限期结束后再清理不被任何活跃 generation 引用的 part。

Runtime 每小时执行一次清理：默认删除一小时前已不被当前 Manifest 引用的
Parquet 和过期 `.parquet.tmp`，以及三小时前仍无活动的 staging 目录。
staging 保留时间必须大于 PHP 同步 cron 的两小时上限；manifest 锁文件不
参与数据回收。

## 8. DuckDB 查询服务

### 8.1 逻辑表视图

Runtime 根据 Manifest 创建逻辑视图：

```sql
CREATE VIEW zt_bug AS
SELECT *
FROM read_parquet([
  '/dataset/tables/zt_bug/.../part-a.parquet',
  '/dataset/tables/zt_bug/.../part-b.parquet'
], union_by_name = true);
```

查询开始时加载所需表的 Manifest。同一查询中的多个表固定使用各自的当前可见 generation。

Runtime 还会从已同步基础表生成与 BI 兼容的语义视图：

```text
ztv_projectnotpl
ztv_tasknotpl
ztv_executionsummary
ztv_projectsummary
ztv_projectteams
ztv_projectstories
ztv_projectbugs
ztv_productstories
ztv_productbugs
ztv_dayactions
ztv_dayuserlogin
ztv_dayeffort
ztv_daystoryopen
ztv_daystoryclose
ztv_daytaskopen
ztv_daytaskfinish
ztv_daybugopen
ztv_daybugresolve
```

数据视图使用的 `ztv_build`、`ztv_product`、`ztv_project`、`ztv_task` 等名称
也会建立只读别名。所有视图只引用 Manifest 中已经登记的基础表。

### 8.2 查询协议

私有本机 Bridge 草案：

```text
POST /internal/runtime/olap/v1/query
```

请求必须包含：

- Schema 版本。
- 目标 SQL。
- 参数绑定。
- `maxRows` 和 `offset`。
- `skipCount`，列元数据和预览请求不会执行全量 `COUNT(*)`。

响应：

```json
{
  "schemaVersion": 1,
  "compatibilityVersion": 2,
  "columns": [],
  "columnTypes": {},
  "rows": [],
  "total": 0,
  "generations": {},
  "truncated": false
}
```

错误码至少包括：

- `unauthenticated`
- `unsupported_schema`
- `invalid_query`
- `unsupported_table`
- `unsupported_function`
- `query_timeout`
- `resource_exhausted`
- `dataset_unavailable`
- `internal`

### 8.3 安全限制

- 只允许单条 `SELECT` 或 `WITH ... SELECT`。
- 禁止 DDL、DML、`COPY`、`ATTACH`、`INSTALL`、`LOAD`、`PRAGMA`。
- 禁止任意文件路径、HTTP/S3、环境变量和 Extension。
- 只允许 Manifest 中登记的逻辑表和只读视图。
- SQL 模板和兼容函数来自 Runtime 内置清单。
- 查询资源独立于 Web 请求线程池生命周期，但客户端断开必须取消查询。
- 每个 DuckDB 查询连接只把当前应用数据集目录列入
  `allowed_directories`，随后关闭 `enable_external_access` 并锁定配置。
  即使未来出现未识别的文件函数，也无法读取数据集之外的本地文件。

### 8.4 资源与并发

建议默认：

```text
queryThreads = 4
queryMemoryLimit = 1GB
queryTimeout = 30s
maxRows = 10000
maxConcurrentQueries = 4
```

具体值按客户数据量和硬件压测后确定。

### 8.5 数据源路由

查询接口继续接受应用已有的 `driver` 值，但 Runtime 数据集完成首次同步后，
BI 读查询统一路由到 `duckdb`：

```text
driver = mysql | dm | pgsql | duckdb
```

其中 MySQL/PostgreSQL/DM 查询业务库，DuckDB 查询 Parquet。

当前路由规则：

1. `[olap] enabled = false` 或应用未收到 OLAP Bridge 环境变量时走业务库。
2. `zt_bisync` 中所有白名单表至少完成过一次同步后，读查询自动走 DuckDB。
3. 显式 `driver = duckdb` 不会回退业务库。
4. 已经路由到 DuckDB 后出现查询失败时返回错误，不静默回退，避免掩盖
   Schema、权限或数据同步故障。
5. 回滚时关闭 Runtime OLAP；应用会重新使用原业务库查询路径。

## 9. 配置建议

```toml
[olap]
enabled = true
bridgePort = 8083
queryThreads = 4
queryMemoryLimit = "1GB"
queryTimeout = "30s"
maxRows = 10000
maxConcurrentQueries = 4
```

Runtime 只负责接收发布和执行查询，不主动调度应用同步。表级
`syncInterval`、`fullReconcileInterval`、水位字段和 bucket 大小位于
禅道 `service/olap/config.php`，由 PHP cron 运行器执行。

以下路径按应用自动隔离：

```text
<dataDir>/olap/
<dataDir>/olap-staging/
```

首版不支持在 `[olap]` 中配置全局 dataset/staging root；路径按应用固定隔离，
避免多个应用误写同一数据集。

`bridgePort` 必须与 Web、Queue、Observability 端口不同，并固定绑定 `127.0.0.1`。

## 10. 权限与安全

- BI 用户权限仍在 PHP 应用中判断。
- Runtime 只执行已授权请求，不替代业务权限模型。
- 每个应用独立 OLAP Token、数据集、Manifest 和查询桥命名空间。
- 同步字段必须使用白名单。
- staging、Parquet 和 Manifest 不得位于应用 Web 根目录。
- Runtime 诊断包不得包含 BI 数据内容。
- 查询日志不记录完整 SQL 参数和结果数据。

## 11. 一致性边界

- 单表 Manifest 切换原子。
- 单次查询固定使用开始时的 generation。
- 不承诺跨表强一致。
- 初始全量和增量期间允许继续使用业务库。
- Parquet 查询失败不能影响业务数据库读写。
- 业务数据库仍是权限、审批、事务和最终状态的事实来源。

建议默认新鲜度：

| 数据 | 目标延迟 |
| --- | --- |
| 小维度表 | 1–5 分钟 |
| 当前态事实表 | 10–15 分钟 |
| 全量对账 | 每日或每周 |

最终延迟目标需要结合数据库负载确认。

## 12. 故障处理

| 故障 | 行为 |
| --- | --- |
| 初始全量失败 | 保留 staging 和断点，BI 继续使用业务库 |
| 单批导出失败 | 任务 failed，按退避重试 |
| Parquet 写入失败 | 删除未发布临时文件，旧 Manifest 继续可见 |
| Manifest 更新前崩溃 | part 成为孤儿，由清理任务回收 |
| Manifest 更新后水位更新失败 | 相同 batchID 重试，Runtime 按 SyncID + generation 幂等 |
| 查询超时 | 返回稳定错误码，不占用长期连接 |
| DuckDB 查询失败 | 返回错误并保留旧 Manifest；由管理员关闭 BI 完成整体回滚 |
| NFS/共享存储不可用 | 停止发布，查询继续使用已加载 Manifest 或明确报错 |

## 13. 实施计划

### Phase 0：SQL 清单与兼容门禁

| ID | 工作 |
| --- | --- |
| BI-SQL-01 | 从最终合并应用包提取图表、透视表、数据视图和大屏引用 SQL |
| BI-SQL-02 | 建立 SQL ID、配置 ID、大屏引用关系和稳定快照清单 |
| BI-SQL-03 | 搭建 MySQL/PostgreSQL/DuckDB Lint + Explain 测试 |
| BI-SQL-04 | 实现三引擎 Fixture 和结果集规范化比较 |

完成条件：

- 所有 SQL 都被清单覆盖。
- 大屏引用闭包完整。
- 任一未测试 SQL 都会阻止发布。

### Phase 1：SQL 编译器与 DuckDB 兼容函数

| ID | 工作 |
| --- | --- |
| BI-SQL-11 | 抽取只读 BI SQL Compiler，支持 mysql/pgsql/duckdb |
| BI-SQL-12 | 处理标识符、分页、日期语法和只读校验 |
| BI-SQL-13 | 增加版本化 DuckDB MySQL 兼容 Macro |
| BI-SQL-14 | 修复 CONCAT、GROUP_CONCAT、Collation、整数除法和零日期问题 |
| BI-SQL-15 | 验证正式 DuckDB 版本 1.5.5 |

完成条件：

- 227 条报表 SQL 和 54 条透视下钻 SQL 在 MySQL、PostgreSQL、DuckDB 上
  100% 通过编译；报表 SQL 完成实际执行和结果比较。
- 结果 Fixture 无未解释差异。
- 企业、旗舰、IPD 合并版新增 SQL 同样通过。

### Phase 2：同步基础

| ID | 工作 |
| --- | --- |
| BI-SYNC-01 | 定义 `zt_bisync` 状态模型和跨数据库 DDL |
| BI-SYNC-02 | 定义表同步规格 Schema |
| BI-SYNC-03 | 实现 staging NDJSON 协议 |
| BI-SYNC-04 | 实现 Runtime OLAP Manifest 和 Publisher |
| BI-SYNC-05 | 实现幂等、租约和失败恢复 |

完成条件：

- 一张小维度表完成 `snapshot`。
- 一张追加表完成 `append`。
- 重复发布不产生重复数据。
- 进程崩溃后可从 staging 重试。

### Phase 3：可变表同步

| ID | 工作 |
| --- | --- |
| BI-SYNC-11 | 实现数值 ID 范围或 bucket 选择 |
| BI-SYNC-12 | 实现 `rewrite` |
| BI-SYNC-13 | 实现软删除、硬删除对账 |
| BI-SYNC-14 | 增加行数和 checksum 校验 |

完成条件：

- Bug/任务/需求表的更新和删除可收敛。
- 同一 bucket 并发同步被租约阻止。
- 旧 generation 查询不受新发布影响。

### Phase 4：DuckDB 查询接入

| ID | 工作 |
| --- | --- |
| BI-QUERY-01 | 定义 Runtime OLAP Query Bridge v1 |
| BI-QUERY-02 | 根据 Manifest 创建逻辑表和 `ztv_*` 视图 |
| BI-QUERY-03 | 接入 BI 分页、列类型和错误模型 |
| BI-QUERY-04 | 增加查询超时、内存、并发和取消 |
| BI-QUERY-05 | 接入降级和状态指标 |

完成条件：

- 内置图表、透视表、数据视图和大屏可切换 DuckDB。
- 查询结果与业务库一致。
- 权限和 SQL 安全测试通过。

### Phase 5：灰度与多节点

| ID | 工作 |
| --- | --- |
| BI-ROLLOUT-01 | 按报表灰度切换数据源 |
| BI-ROLLOUT-02 | 多节点共享 Parquet，单写多读 |
| BI-ROLLOUT-03 | 监控同步延迟、失败率和查询性能 |
| BI-ROLLOUT-04 | 建立回滚开关 |
| BI-ROLLOUT-05 | 评估 CDC 增强 |

### 当前实现进度（2026-10-26）

已完成：

- PHP 同步配置覆盖 227 条内置 SQL 引用的 26 张来源表，支持字段包含/排除
  白名单和固定行过滤；`zt_config` 只同步 `installedDate`、`version` 两行。
- PHP DAO 流式导出 NDJSON staging，`snapshot`、`append`、`rewrite` 三种发布
  模式均已接通 Runtime。
- `rewrite` 首次同步覆盖空和有效 bucket，周期全量。增量同时查询
  `id > watermarkID` 与 `lastEditedDate` 重叠时间窗口，再按 ID 归桶重写。
- `zt_bisync` 已实现状态初始化、CAS 租约、generation 进度、错误记录、
  `lastSuccessAt` 与独立 `lastFullAt`。
- 新增 `bi.syncData` cron，15 分钟调度一次；单任务上限 2 小时，所有同步
  读取强制走业务库主库。
- Runtime 实现 Manifest、带 checksum 的原子 Parquet 发布、空 bucket
  Parquet、应用级 Token、发布/查询 Bridge 和 deep health。
- Runtime 从 Manifest 装配基础表及 `ztv_*` 语义视图，查询限制单条只读 SQL、
  文件函数、写操作、超时、内存、并发、行数和 offset；DuckDB 文件访问进一步
  限制在当前应用数据集目录。
- BI 查询桥已接入 `biModel` 的分页、总行数、列名和列类型；全表同步完成后
  自动切换 DuckDB，显式 `duckdb` 不静默回退业务库。
- PHP SQL 编译器支持 `mysql`、`pgsql`、`duckdb`；227 条报表 SQL 和 54 条
  透视下钻 SQL 已全部通过三目标编译，兼容版本为 2。
- 编译器会把自定义业务表前缀映射回 Runtime 逻辑 `zt_*` 表；PostgreSQL
  `BOOLEAN`、无精度 `NUMERIC/DECIMAL/NUMBER` 和 MySQL 无法精确定义类型的
  数值列也有确定映射。MySQL 导出期间关闭 PDO buffered query，避免大表
  虽然逐行 fetch 但仍整体驻留 PHP 内存。
- DuckDB Macro 和空 bucket、分页、列类型、JSON 序列化、SQL 安全检查已有
  `duckdb` build tag 集成测试。
- 在现有 MariaDB 禅道测试库上对 227 条 SQL 执行了 `EXPLAIN`，模板变量以
  `NULL` 代入，`zt_compile` 用会话级临时表补齐；无语法、缺表或缺列错误。
- 使用真实 MySQL Schema 生成空 PostgreSQL/DuckDB Fixture，执行了全部 227 条
  查询；PostgreSQL 兼容函数、DuckDB Macro 和 `ztv_*` 视图均通过。
- 将现有 IPD 5.7 测试库的非空业务数据导入 PostgreSQL 和 DuckDB，对 200 条
  无条件 SQL、27 条动态筛选 SQL 做三引擎实际执行和结果规范化比较：
  行数、列顺序、数值、布尔、日期和 NULL 语义均为 227/227 一致。
- 非空比较发现并修复了用例视图无意义关联造成重复行、安装天数 Decimal
  拼接、空解决状态 trailing-space collation、百分比 Decimal 精度等差异。
- 从当前源码生成的完整 IPD 合并制品已重新执行 SQL、同步和大屏测试：
  227 条报表、54 条透视下钻、26 张来源表、9 个内置大屏和 166 个实际引用
  查询全部通过。旧安装库中的内置记录没有作为最终制品结论。
- 联合测试已覆盖 PHP Fake DAO → PHP NDJSON staging → 真实 HTTP Publisher
  → Parquet/Manifest → DuckDB Query，并校验 2 行数据、generation 和兼容
  版本。
- append 周期全量增加 ID 上界，避免快照读取期间新增记录被后续 append
  重复追加；`zt_action` 的首次和每日全量均使用该边界。
- 内置大屏引用闭包、自定义表前缀映射、DuckDB 数据集外文件拒绝、空 bucket、
  generation 冲突、跨进程发布锁和 Race Detector 已纳入自动测试。

尚未完成：

- 同步延迟/失败率指标、按报表灰度和 Top N 查询 P50/P95 生产基线。
- 多节点共享存储故障、租约过期竞争和跨节点 generation 一致性压测。
- 客户规模数据量下的初始全量耗时、索引创建窗口和业务库 IO 压测。

当前 `[olap]` 默认开启。该能力在运行节点页面显示为 OLAP；未完成项仍按报表逐项验收。

## 14. 验收标准

### 功能

- 所有内置 SQL 在三引擎上成功执行。
- MySQL 与 PostgreSQL 查询结果一致。
- MySQL 与 DuckDB 查询经规范化后结果一致。
- 大屏、图表、透视表和数据视图全部覆盖。
- 用户自定义 SQL 不支持时返回明确错误，不造成静默错误结果。

### 数据

- 单表 Manifest 发布原子。
- 跨进程 Publisher 使用文件锁串行化同表发布。
- 全量、增量、更新、软删除和硬删除收敛符合设计。
- 行数、主键和 checksum 可对账。
- 旧 generation 在查询宽限期内可继续读取。
- 未被 Manifest 引用的过期 Part 和 staging 目录由 Runtime 清理任务回收。

### 性能

- 提供切换前后 Top N BI 查询的 P50/P95 对比。
- 业务数据库 BI 查询读负载明显下降。
- 同步任务对业务库的锁等待和复制延迟在约定阈值内。
- DuckDB 查询内存、CPU、超时和并发满足配置上限。

### 安全

- BI 数据目录不可通过 Web 访问。
- SQL 不接受任意文件路径、Extension 或写操作。
- 敏感字段不进入 Parquet。
- 每应用数据隔离和 Token 鉴权通过。

### 兼容回归

- MySQL 和 PostgreSQL 的现有 BI 查询不得回归。
- DuckDB 正式版本必须与三引擎测试锁定的版本一致。
- DM/Gauss 不是本次三引擎查询目标，但作为同步来源的 DAO 读取和类型转换必须保留现有兼容门槛。

## 15. 风险与待确认事项

| 风险 | 说明 |
| --- | --- |
| SQL 兼容工作量 | 227+ 条 SQL 的结果语义测试比语法测试成本更高 |
| Collation 差异 | 可能造成不报错但结果不同 |
| 大文本字段 | 会显著增加存储和查询开销 |
| 全量同步压力 | 大表初始同步影响业务库 IO |
| 硬删除 | 无 CDC 时只能周期收敛 |
| 多节点 Manifest | 必须保证单写多读和原子切换 |
| 用户自定义 SQL | 不在 100% 内置兼容承诺内，需要能力提示 |

待确认：

1. 正式发布采用 15 分钟增量、每日全量，还是按客户规模分档。
2. 大文本字段在首版是否保持当前范围，或按报表进一步收窄。
3. 多节点共享存储是否统一要求 POSIX 原子 rename 和可靠文件锁语义。
4. 硬删除是否在首版继续依赖每日全量，不引入 CDC。

## 16. 参考

- [MySQL 到 Parquet 历史设计](./08-mysql-parquet-sync.md)
- [当前 DuckDB 数据方案](./09-unified-duckdb-design.md)
- [Runtime 可观测性设计](./05-observability.md)
- [Runtime 使用开发指南](../usage/README.md)
- 禅道数据库兼容问题：`docs/module/bug.md`
- PostgreSQL 兼容函数：`zentaopms/db/pgsql_function.sql`
- SQL 格式化入口：`zentaopms/lib/dbh/dbh.class.php`
