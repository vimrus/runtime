# Runtime 多节点配置自动诊断

## 1. 文档信息

| 项目 | 内容 |
| --- | --- |
| 状态 | 已实现自动注册、单/多节点比较和错误展示 |
| 日期 | 2026-10-27 |
| 核心数据表 | `zt_servernode` |
| 检查范围 | Runtime、应用、数据库、共享存储、Session、Cache、Queue、日志、OLAP、安全 |
| 失败策略 | 显示具体错误，不阻断业务、管理、Queue 或 Cron |

## 2. 设计目标

- 每个 Runtime 节点自动上报节点身份和配置摘要。
- 只有一个节点时直接显示当前节点，不进行多节点比较。
- 多个节点时自动比较 Profile，发现差异后显示具体配置域。
- 节点配置修正并重新上报后，错误自动消失。
- 不设置启停开关，不设置 bootstrap，不维护集群基线。
- 不因配置差异关闭业务路由或阻止 Queue/Cron 运行。
- Runtime 仍不直接连接禅道业务数据库。

## 3. 非目标

- 不把配置检查作为流量准入控制。
- 不自动修改节点配置。
- 不按 `runtime.toml` 文本或绝对路径逐字比较。
- 不在 `profileData` 中保存密码、Token、私钥或完整连接字符串。
- 不在两个完全独立、互不共享注册数据的数据库之间自动发现节点。

## 4. 运行模型

```text
Runtime Node
  ├─ Runtime-owned profile
  ├─ App/DB/Session/Cache/Log/OLAP profile
  └─ private loopback Profile Bridge
                |
                v
        PHP Runtime Profile Service
                |
                v
          zt_servernode
                |
                v
         Runtime 节点诊断页面
```

节点每次启动生成新的 `instanceID` 和 `bootID`，使用稳定 `nodeID` 写入
`zt_servernode`。PHP 读取当前业务数据库、当前表前缀下 `nodeType=runtime`
的行并比较。

节点归属不需要额外配置。`zt_servernode` 位于业务数据库中，当前数据库、
Schema 和表前缀天然界定了同一次部署；不同数据库中的节点属于独立部署，
彼此不参与配置比较。

单节点部署可以不配置 `nodeID`，此时 Runtime 与页面都保持空字符串；多节点
部署必须为每个节点显式配置唯一值。

## 5. 节点数据

每节点一行，多个应用放在 `profileData.apps.<appID>`：

| 字段 | 说明 |
| --- | --- |
| `nodeID` | 节点稳定标识 |
| `instanceID` | 本次启动实例，用于 fencing |
| `bootID` | 启动周期标识 |
| `nodeType` | `runtime`、`consumer`、`control` |
| `state` | 进程状态，如 `active`、`stopped` |
| `heartbeatTime` | 最近上报时间 |
| `profileVersion` | Profile Schema 版本 |
| `profileDigest` | 脱敏 Profile 摘要 |
| `profileData` | 最新 Profile 与比较结果 |

配置不一致不会把进程状态改成 `blocked`；差异记录在
`profileData.comparison` 中。

## 6. Profile 范围

Runtime 提供：

- Runtime 版本和 Release ID。
- Web 线程、Queue、Observability、OLAP 启停和容量摘要。
- 共享数据 Storage ID。
- 扩展与 Runtime 能力。

PHP 补充：

- 应用版本、PHP 版本和关键扩展。
- Web 服务器与 PHP SAPI 组合名称，如 `Caddy / FrankenPHP`、`Apache / PHP-FPM`。
- 数据库驱动、主机、端口、库名/Schema、表前缀、字符集和凭据指纹。
- Session Handler 和共享路径身份。
- Cache 开关、后端和 Redis 配置。
- 日志模式。
- OLAP 开关和 Schema。
- Web Root、应用 ID 等安全行为配置。

Secret 使用集群密钥 HMAC；未配置集群密钥时只标记为 `configured`，不保存原值。

## 7. 比较规则

1. 只有一个 Runtime 节点时状态为 `ready`，不做比较。
2. 多个节点先按应用集合和 `critical`、`behavioral` 摘要比较。
3. `capacity` 差异只展示，不算配置错误。
4. 任意数据库、共享存储、Session、Cache、日志、OLAP、版本或能力差异返回
   `mismatch`，并列出配置域。
5. `mismatch` 只影响诊断展示，不关闭业务、Queue 或 Cron。
6. 节点修正配置并重新启动后，下一次 Profile 上报会自动重新比较。

## 8. 状态

| 状态 | 含义 | 业务影响 |
| --- | --- | --- |
| `ready` | 单节点或所有节点配置一致 | 无 |
| `mismatch` | 多节点存在配置差异 | 无，仅显示错误 |
| `registering` | 同一 nodeID 的旧实例尚未释放 | 无 |
| `blocked` | Profile 无法保存或注册服务内部错误 | 无，仅诊断失败 |
| `stale` | 心跳超时 | 无，页面标记过期 |

## 9. 后台页面

- 无节点：显示“暂无运行节点”。
- 单节点：显示当前节点和“无需进行多节点配置比较”。
- 多节点一致：显示“所有运行节点配置一致”。
- 多节点不一致：显示“运行节点配置存在差异”和具体配置域。
- 页面同时展示节点状态、版本、心跳、能力、差异和容量信息。

页面不提供基线审批、bootstrap 或强制放行操作。

## 10. 健康与就绪

`/_runtime/readiness` 不因配置差异失败。Cluster Profile 检查作为非强制健康项：

- 配置差异在状态和诊断输出中显示为失败项。
- 应用路由保持可用。
- Queue/Cron 保持运行。
- 部署方根据后台错误决定是否将异常节点接入负载均衡。

## 11. Profile 安全

- Bridge 只接受来自 `127.0.0.1` 或 `::1` 的 POST。
- 必须校验每应用 Token 和 Profile Schema Header。
- `profileData` 只保存摘要，不保存密码、Token、Cookie 或完整连接字符串。
- 公共网络不能直接访问 Profile Bridge。
- 后台页面不显示 Secret 明文。

## 12. 已知边界

- 如果两个节点使用不同业务数据库，它们不会共享 `zt_servernode`，当前实现无法
  仅通过业务数据库互相发现。需要共享 Runtime 注册中心或独立监控注册点才能
  自动发现这种情况。
- 当前实现只对写入同一业务数据库、同一表前缀的节点做自动比较。
- 滚动升级兼容窗口仍未实现；Release ID 不同会作为配置差异展示。

## 13. 验收

- 单节点自动 `ready`，页面显示当前节点。
- 第二节点配置一致时自动 `ready`。
- 数据库、Session、Cache、日志、OLAP 或 Storage ID 不一致时返回 `mismatch`。
- 配置差异下业务、管理、Queue 和 Cron 仍可访问。
- 修正节点配置并重启后，比较结果自动恢复。
- 容量差异只进入 `capacityDiff`。
- Profile 和日志不包含 Secret 明文。

