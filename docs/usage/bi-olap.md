# BI / OLAP 接入

这篇文档面向应用对接人员，说明如何把业务数据库中的分析数据导出为 Runtime
OLAP 数据集，并通过受控 DuckDB 查询供 BI 使用。

业务数据库始终是业务事实来源。Parquet 和 DuckDB 只是分析副本，不能参与业务
事务、权限判断或最终状态写入。

## Runtime 的作用

Runtime 不连接业务数据库，也不负责调度应用同步。应用侧的定时任务或队列任务
负责读取业务库并把数据写入 staging，Runtime 负责：

1. 将应用提交的 NDJSON staging 转换为不可变 Parquet。
2. 原子更新 Manifest，使新数据在一次切换后可见。
3. 只读打开当前 Manifest 登记的 Parquet，提供受限 DuckDB SQL 查询。

```text
业务数据库
  -> 应用 DAO 分块导出
  -> staging NDJSON
  -> Runtime OLAP Publish
  -> Parquet + Manifest
  -> Runtime OLAP Query
  -> BI
```

## 应用侧交付物

应用需要提供：

- 数据导出和同步调度。
- 同步状态、水位、generation 和单写租约。
- `schemaVersion`、逻辑表名和字段类型定义。
- NDJSON staging 写入。
- 调用 Runtime Publish Bridge。
- 调用 Runtime Query Bridge，并在不可用时回退业务数据库。
- BI 查询权限校验和查询结果展示。

Runtime 只校验协议、Schema、Manifest 和 SQL 安全边界，不判断当前用户是否有权
查看某个报表。

## Runtime 侧准备

Runtime 部署需要启用 OLAP：

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

OLAP 关闭时，应用不会获得 `ZENTAO_OLAP_*` 环境变量，应继续使用业务数据库。
应用不需要了解 Runtime 内部 Listener 地址。

## Runtime 注入的环境

OLAP 启用后，每个应用获得独立的环境变量和数据集：

```text
ZENTAO_OLAP_PUBLISH_URL
ZENTAO_OLAP_QUERY_URL
ZENTAO_OLAP_TOKEN
ZENTAO_OLAP_STAGING_DIR
ZENTAO_OLAP_SCHEMA
ZENTAO_OLAP_MAX_ROWS
```

应用应使用这些变量，不硬编码 Runtime 内部端口或路径。每个应用使用独立 Token，
不能查询另一个应用的数据集。

发布和查询都使用 `POST application/json`，请求头为：

```text
X-Zentao-OLAP-Token: <ZENTAO_OLAP_TOKEN>
X-Zentao-OLAP-Schema: <ZENTAO_OLAP_SCHEMA>
```

请求上限为 256 KiB，JSON 必须只有一个根对象，并拒绝未知字段。查询响应大小由
`maxRows`、查询列数和 Runtime 资源限制共同约束。
错误响应：

```json
{
  "error": {
    "code": "query_failed",
    "message": "OLAP query failed"
  }
}
```

常见结果码包括 `unauthenticated`、`unsupported_schema`、`invalid_request`、
`publish_failed`、`query_failed` 和 `unavailable`。查询超时返回 HTTP 504；
数据集不可用返回 HTTP 503。

数据集位置由 Runtime 按应用隔离：

```text
<dataDir>/olap/
<dataDir>/olap-staging/
```

staging、Parquet 和 Manifest 不得放在 Web 根目录中。

## 数据发布

### 1. 准备 staging

应用先为本次同步创建独立目录，并在目录中写入 `.jsonl` 或 `.ndjson` 文件：

```text
<ZENTAO_OLAP_STAGING_DIR>/<syncID>/
  data-0001.jsonl
  data-0002.jsonl
```

每行是一个 JSON 对象，字段必须与提交的 Schema 一致：

```json
{"id":1,"title":"Bug A","status":"active"}
{"id":2,"title":"Bug B","status":"closed"}
```

`syncID` 在重试期间必须保持不变。Runtime 按 `syncID` 查找 staging，并对相同
generation 的重复发布做幂等处理。

### 2. 提交 Schema 和发布请求

请求体：

```json
{
  "schemaVersion": 1,
  "table": "zt_bug",
  "mode": "snapshot",
  "syncID": "bug-snapshot-20261104-100000",
  "expectedGeneration": 0,
  "schema": {
    "version": 1,
    "table": "zt_bug",
    "primaryKey": "id",
    "columns": [
      {"name": "id", "type": "BIGINT"},
      {"name": "title", "type": "VARCHAR"},
      {"name": "status", "type": "VARCHAR"}
    ]
  }
}
```

`expectedGeneration` 是乐观并发控制。首次发布传 `0`；后续请求必须传当前可见
Manifest 的 generation。

增量或 bucket 同步还可以提交 `watermarkID` 和 `watermarkTime`，用于应用侧
记录本次发布覆盖的数据范围。`syncID` 只能包含字母、数字、点、下划线或连字符，
长度为 1 到 128 个字符。

支持的类型：

```text
BOOLEAN
TINYINT
SMALLINT
INTEGER
BIGINT
UTINYINT
USMALLINT
UINTEGER
UBIGINT
DOUBLE
VARCHAR
JSON
DATE
TIMESTAMP
DECIMAL(p,s)
```

### 3. 选择发布模式

| Mode | 用途 | 可见结果 |
| --- | --- | --- |
| `snapshot` | 全量快照 | 用新 part 替换该表全部可见数据 |
| `append` | 仅追加新记录 | 在现有可见 part 后追加新 part |
| `rewrite` | 重写主键 bucket | 只替换指定 bucket 的可见 part |

`rewrite` 请求还需要：

```json
{
  "bucket": 3,
  "expectedGeneration": 41
}
```

表使用 `snapshot`、`append` 还是 `rewrite`，由应用自己的同步配置决定。

### 4. 处理发布响应

成功响应包含新的 Manifest 和本次写入行数：

```json
{
  "manifest": {
    "schemaVersion": 1,
    "table": "zt_bug",
    "generation": 1,
    "publishedAt": "2026-11-04T02:00:00Z",
    "rowCount": 2,
    "buckets": {
      "all": [
        {
          "path": "tables/zt_bug/schema=v1/bucket=all/part-g1-....parquet",
          "rows": 2,
          "sha256": "..."
        }
      ]
    }
  },
  "rows": 2
}
```

应用应在自己的同步状态中保存：

- `activeGeneration`
- 数据水位
- 最近成功时间
- 最近错误
- 同步租约状态

Manifest 更新成功但应用状态保存失败时，可以使用相同的 `syncID` 和
`expectedGeneration` 重试。Runtime 会返回已经发布的相同 generation。

发布成功并保存应用侧状态后，应用应删除本次 staging 目录。未完成或失败的
staging 由应用重试，Runtime 也会清理长期没有活动的 staging。

## 数据查询

应用通过 `ZENTAO_OLAP_QUERY_URL` 调用查询接口：

```json
{
  "schemaVersion": 1,
  "sql": "SELECT \"id\", \"title\", \"status\" FROM \"zt_bug\" WHERE \"status\" = ?",
  "params": ["active"],
  "maxRows": 100,
  "offset": 0,
  "skipCount": false
}
```

请求头与发布接口相同。

成功响应：

```json
{
  "schemaVersion": 1,
  "compatibilityVersion": 2,
  "columns": ["id", "title", "status"],
  "columnTypes": {
    "id": "BIGINT",
    "title": "VARCHAR",
    "status": "VARCHAR"
  },
  "rows": [
    {"id": 1, "title": "Bug A", "status": "active"}
  ],
  "total": 1,
  "generations": {"zt_bug": 1},
  "truncated": false
}
```

`skipCount=true` 时 Runtime 不执行全量 `COUNT(*)`，适合列预览；此时
`total = offset + 当前页返回行数`，不代表完整结果数。`maxRows` 不能超过
Runtime 配置或 `ZENTAO_OLAP_MAX_ROWS` 的上限，`offset` 最大为 1,000,000。

## 查询约束

查询只允许单条只读 `SELECT` 或 `WITH ... SELECT`：

- 不允许 DDL、DML、`COPY`、`ATTACH`、`INSTALL`、`LOAD` 或 `PRAGMA`。
- 不允许任意文件函数、URL、Extension 或数据集外路径。
- 只能查询当前应用中已发布 Manifest 的逻辑表和 Runtime 生成的只读视图。
- 逻辑表名使用应用提交时的名称，例如 `zt_bug`。
- Runtime 还会生成 `ztv_*` BI 兼容视图。
- 查询超时、资源耗尽和数据集不可用会返回错误，不会静默返回错误数据。

应用仍需在调用前完成当前用户的 BI 权限校验。不能因为 Runtime 接受 SQL 就跳过
业务权限。

## 一致性与降级

- 单表 Manifest 切换是原子的。
- 一次查询使用开始时固定的 Manifest generation。
- 不同表之间不承诺强一致。
- Parquet 尚未首次发布完成、数据集不可用或查询失败时，BI 可以回退到业务数据库。
- 发布失败或查询失败不能影响业务数据库读写。
- 业务数据库始终是权限、事务和最终状态的唯一事实来源。

## 多节点

多个 Runtime 节点访问同一业务数据库和共享 OLAP 数据集。应用必须通过数据库
租约保证同一逻辑表同一时间只有一个同步写入者，并继续遵守 Manifest generation
CAS 和 Runtime 文件锁。

不要把进程内锁当作跨节点互斥。节点切换后，旧的发布请求不能覆盖更新后的
generation。

## 故障处理

| 故障 | 应用处理 |
| --- | --- |
| 业务库导出失败 | 保留 staging/断点，任务重试，BI 继续使用旧数据或业务库 |
| Parquet 发布失败 | 使用相同 `syncID` 重试，旧 Manifest 继续可见 |
| generation 冲突 | 重新读取当前 generation，重新规划同步 |
| Manifest 成功但状态保存失败 | 用相同 `syncID + expectedGeneration` 重试 |
| 查询超时或资源不足 | 缩小查询范围或提升 Runtime 配额；回退业务库 |
| 数据集不可用 | 返回业务库查询，不能返回空数据冒充成功 |

## 对接检查

- [ ] staging 文件是合法 NDJSON，字段与 Schema 一致。
- [ ] Schema 使用受支持的类型和合法主键。
- [ ] 首次发布使用 `expectedGeneration=0`，后续使用当前 generation。
- [ ] 相同 `syncID` 的重试不会重复推进 generation。
- [ ] 应用持久化 active generation、水位和同步状态。
- [ ] 每次发布只允许一个应用写入者。
- [ ] BI 用户权限在应用侧完成校验。
- [ ] 查询失败时能明确回退业务数据库。
- [ ] Parquet、Manifest 和 staging 不在 Web 根目录。
- [ ] 多节点共享数据集通过 NFS 或等效共享存储访问。

完整数据模型和 SQL 兼容说明见
[BI Parquet 同步与 DuckDB 查询方案](../design/11-bi-parquet-query-plan.md)。
