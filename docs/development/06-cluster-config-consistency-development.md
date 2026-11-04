# Runtime 多节点配置自动诊断开发文档

## 1. 实现范围

- Runtime 启动后自动上报 Profile，不需要 `cluster.enabled`。
- 删除 `cluster.bootstrap`、基线控制行和基线审批。
- 删除 Caddy Cluster Gate 对业务请求的阻断。
- Queue/Cron 不依赖配置诊断状态。
- 单节点自动视为正常；多节点自动比较并展示差异。
- 配置差异状态为 `mismatch`，不是流量准入状态。

## 2. 配置

基础配置不包含 `[cluster]` 启停开关：

```toml
schemaVersion = 1

[web]
listen = "0.0.0.0:8080"

[[apps]]
id = "zentao"
path = "/"
dir = "apps/zentao"
webRoot = "www"
dataDir = "data/apps/zentao"
```

多节点诊断自动运行。若需要调整内部比较周期，可通过既有环境变量覆盖：

- `ZENTAO_CLUSTER_RELEASE_ID`
- `ZENTAO_CLUSTER_SECRET_KEY`
- `ZENTAO_CLUSTER_REGISTRATION_TIMEOUT`
- `ZENTAO_CLUSTER_PROFILE_INTERVAL`
- `ZENTAO_CLUSTER_STALE_TIMEOUT`
- `ZENTAO_CLUSTER_REQUIRE_SHARED_DATA`

这些参数不属于正常部署必填项。`cluster.enabled` 和 `cluster.bootstrap`
会被严格配置解析器作为未知字段拒绝。

## 3. Runtime 侧

1. 为每个应用生成私有 Profile Token 和 Bridge Schema。
2. 注入 `ZENTAO_RUNTIME_PROFILE_TOKEN/SCHEMA` 及诊断参数。
3. 解析共享 Storage Identity；失败时标记为 `unavailable`，不阻止 Runtime 启动。
4. 生成 Runtime 和应用 Profile。
5. 定时调用本机 Profile Bridge。
6. 将注册状态写入 `status` 和健康检查的非强制组件。

配置差异不会：

- 关闭 Caddy 业务路由；
- 停止 Queue Engine；
- 停止 Cron；
- 阻止后台管理页面访问。

## 4. PHP 侧

1. `runtime.profile` 校验回环地址、Token 和 Schema。
2. `runtimeModel::registerProfile()` 补充 PHP 有效配置。
3. `runtimeClusterRegistry::register()` 保存节点并比较当前业务数据库中的全部运行节点。
4. 单节点返回 `ready`。
5. 多节点一致返回 `ready`。
6. 多节点不一致返回 `mismatch`，并记录 `mismatchDomains`。
7. 页面读取 `profileData.comparison` 展示差异。

使用 `instanceID + heartbeatTime` CAS 拒绝旧实例写入。

## 5. 状态语义

| 状态 | 说明 |
| --- | --- |
| `ready` | 单节点或所有节点配置一致 |
| `mismatch` | 配置存在差异，仅展示错误 |
| `registering` | 同 nodeID 旧实例尚未释放 |
| `blocked` | Profile 保存或注册内部错误 |
| `stale` | 心跳超时 |

`mismatch` 不修改节点进程状态，节点仍为 `active`。

## 6. 比较规则

- 比较 `releaseID`。
- 比较 Runtime 的 `critical`、`behavioral` 摘要。
- 按 appID 比较应用版本、数据库、Session、Cache、日志、OLAP、安全和 Storage ID。
- `capacity` 只进入 `capacityDiff`。
- Secret 只比较 HMAC 指纹。
- 节点本地 PID、Socket、日志路径和临时目录不参与比较。

## 7. 页面验收

| 场景 | 页面 |
| --- | --- |
| 无节点 | 暂无运行节点 |
| 单节点 | 当前节点，无需多节点比较 |
| 多节点一致 | 所有运行节点配置一致 |
| 多节点不一致 | 显示配置错误与差异域 |
| 配置修正 | 下一次上报后错误自动消失 |

## 8. 测试

```bash
php test/runtime/cluster-profile.php
php test/runtime/cluster-registry.php
go test ./internal/config ./internal/cluster
```

测试覆盖：

- 单节点不依赖基线，直接 ready。
- 多节点一致 ready。
- 数据库不一致返回 `mismatch`。
- 配置差异不把节点状态改成 blocked。
- 修正配置后自动恢复。
- 容量差异不产生 mismatch。
- 能识别 Caddy/FrankenPHP、Apache/PHP-FPM 和 Nginx/PHP-FPM。

## 9. 已知限制

- 不同业务数据库不会共享 `zt_servernode`，当前实现无法自动互相发现。
- 真正的跨数据库节点发现需要共享 Runtime 注册中心或独立监控注册点。
- Release 兼容窗口尚未实现，版本不同会作为配置差异显示。

