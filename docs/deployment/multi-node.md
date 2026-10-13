# 多节点部署指南

本文面向部署人员，说明如何部署两个 Linux Runtime 节点。多节点部署需要让两个独立 Runtime 共享业务后端，同时保留各自的节点身份和本地运行状态。

本文不覆盖数据库、NFS、Redis 或负载均衡的安装方法；这些组件由部署环境提供。

## 推荐拓扑

```text
客户端
  -> 云/硬件负载均衡，或 Keepalived + Caddy Gateway
       -> 节点 A Runtime
       -> 节点 B Runtime

节点 A/B 共同使用：数据库、附件 NFS、Session NFS 或 Redis、Parquet NFS
节点 A/B 分别保存：Runtime 配置、PID、日志、缓存、观测 spool、node-id
```

数据库、NFS、Redis 和负载均衡的高可用由部署环境负责。Runtime 不提供配置中心，也不直接连接业务数据库或 Redis。

## 配置两个节点

在两个节点分别放置一份 `config/runtime.toml`。两份文件大部分内容相同，只有节点身份和明确的本地路径可以不同。

### 两台机器保持一致

- `runtime.clusterID`
- 应用、PHP、Runtime 和扩展版本
- `[[apps]]` 应用清单（id、path、webRoot）和各应用的目录布局、监听端口和线程策略
- 数据库、Session、附件、Queue Bridge 和安全相关配置
- `observability.datasetRoot`、Parquet Schema、保留周期和脱敏策略

### 每台机器分别设置

- `runtime.nodeID`：同一集群内唯一，例如 `node-a`、`node-b`
- `runtime.controlSocket` 和 `runtime.pidFile`：默认使用各自 Runtime 根目录下的 `run/`，只有需要特殊目录时才覆盖
- `runtime.logPath`、`runtime.auditLog`：建议写入各自本地日志目录
- `observability.spoolPath`：每台机器使用自己的本地 spool

示例（节点 A）：

```toml
[runtime]
nodeID = "node-a"
clusterID = "cluster-prod-1"
controlSocket = "run/runtime.sock"
pidFile = "run/runtime.pid"
logPath = "logs/runtime.log"

[web]
listen = "0.0.0.0:8080"

[[apps]]
id = "zentao-ipd"
path = "/"
dir = "apps/zentao-ipd-22.6"
webRoot = "www"

[observability]
enabled = true
datasetRoot = "/mnt/zentao-observability"
spoolPath = "spool/observability"
```

节点 B 复制相同配置后，将 `nodeID` 改为 `node-b`。两台机器都把共享 Parquet NFS 挂载为 `/mnt/zentao-observability`。`datasetRoot` 使用共享路径，`spoolPath` 保持相对路径，使 spool 留在各自 Runtime 根目录。

配置中的 `app/...`、`logs/...`、`spool/...` 等相对路径会按每台机器自己的 Runtime 根目录解析，不要把两个节点的本地 Runtime 根目录写成同一个共享路径。

## Session 和附件

Session 和附件不由 Runtime 配置管理：

- Session 必须在 PHP `php.ini` 中配置为共享 NFS 文件目录，或外部 Redis。
- 两台机器必须使用同一个规范化 Session 路径、Cookie 参数和锁策略。
- 附件目录必须挂载同一份 NFSv4.1 共享存储，并在禅道应用配置中使用相同逻辑路径。
- 不要在多节点模式下让两台机器各自写本地 Session 或附件目录。
- NFS/Redis 不可用时不能静默回退到本地目录；使用对应后端的节点应进入 Not Ready。

切换 Session 后端不迁移旧 Session，已有用户重新登录是预期行为。

## 数据库和 Queue

两台节点使用相同的数据库地址、数据库名、Schema 和表前缀。数据库连接由 PHP 应用通过 `ZT_DB_*` 环境变量或 PHP 配置提供，Runtime 不保存数据库凭据。

如果启用 Queue：

- 两台节点使用相同的 `queue.bridgeBaseURL` 规则和 Worker 配置。
- Queue Bridge 通常绑定本机回环地址，由每台节点自己的 PHP 应用提供。
- `bridgeToken` 不提交到配置文件；Runtime 启动时生成本次运行的 token。
- 队列状态在业务数据库中持久化，Worker 通过租约和 fencing token 避免重复领取。

## 接入负载均衡

优先使用已有云、硬件或托管负载均衡；没有外部负载均衡时，两个 Linux 节点使用 Keepalived 浮动 VIP 和 Runtime 内嵌的 Caddy Gateway。

- 后端健康检查使用每个节点的 readiness 端点。
- 使用共享 Session 时采用 `least_conn`，不启用 Sticky Session。
- 节点维护或升级前先摘除流量，等待在途请求结束，再重启 Runtime。
- 不自动重试已经发送请求体的 POST、PATCH、DELETE 和文件上传。
- App listener 只允许负载均衡或 Gateway 所在内网访问，不直接暴露公网。

## 观测数据

每个节点拥有唯一 `nodeID`，向共享 Parquet 根目录写入自己的 `node=<nodeID>` 分区。节点不共享 `.duckdb` 文件，也不覆盖其他节点的 part 文件。共享数据集不可用时，事件先写入本地 `spool/observability`，达到上限后标记 degraded；这不能阻断业务请求。

## 上线步骤

1. 检查两个节点的 `clusterID`、应用版本、数据库和共享存储配置一致。
2. 确认两个 `nodeID` 唯一，且 Session、附件和 Parquet 挂载路径可写并具有正确权限。
3. 分别执行 `zentao health --deep`，确认 PHP Session、附件、数据库和观测检查通过。
4. 先只将节点 A 加入负载均衡，验证登录、上传、下载和 Queue，再加入节点 B。
5. 逐台执行摘流、重启、恢复流量，确认用户 Session 不依赖 Sticky 且附件无本地分叉写入。

更完整的故障边界、Keepalived、Session 锁和验收项见[多节点部署与高可用设计](../design/06-deployment-and-ha.md)。
