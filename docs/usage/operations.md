# 运维与健康检查

这篇文档说明如何观察 Runtime、使用本地控制面、重载配置和收集诊断信息。

业务应用的健康规则见[应用深探针](application-hosting.md#应用深探针)。本文只描述 Runtime 自己提供的运维入口。

## 健康检查分层

Runtime 提供三个层次的健康信息：

| 层次 | 入口 | 用途 |
| --- | --- | --- |
| liveness | `/_runtime/liveness`、`/_runtime/healthz` | 判断 Host 是否还活着 |
| readiness | `/_runtime/readiness` | 判断是否应该接收业务流量 |
| deep health | `./bin/zentao health --deep` | 定位 PHP、应用、Queue、观测和共享依赖问题 |

### Liveness

```bash
curl --fail http://127.0.0.1:8080/_runtime/liveness
```

成功：

```json
{"status":"ok"}
```

liveness 只表示 Runtime event loop 没有被判定为 stopped 或 failed。它不代表数据库、应用或队列可用。

### Readiness

```bash
curl --fail http://127.0.0.1:8080/_runtime/readiness
```

成功：

```json
{"status":"ready"}
```

readiness 要求：

- Runtime 生命周期已经进入 ready。
- Runtime Host 探针通过。
- PHP/FrankenPHP 初始化成功。
- 每个启用应用的文档根存在。

失败时返回 `503`：

```json
{
  "status": "unavailable",
  "live": true,
  "ready": false,
  "degraded": true
}
```

Queue 或可观测性不是公共 readiness 的必需项。它们的问题应通过 deep health 和诊断日志定位。

### Deep health

```bash
./bin/zentao health --deep
```

输出包含：

```text
live
ready
lifecycle
components
deep
```

组件名通常使用：

- `runtime`
- `php`
- `app-root:<id>`
- `app:<id>`
- `queue:<id>`
- `observability:<id>`

组件状态：

| 状态 | 含义 |
| --- | --- |
| `ok` | 检查通过 |
| `degraded` | 部分功能不可用，但可以继续运行 |
| `failed` | 组件检查失败 |
| `unknown` | 尚未运行或没有有效结果 |

深度检查只通过本地 Control Plane 执行，不提供公共 Web 入口。

## Control Plane

### 传输

Linux 使用 Unix Domain Socket，Windows 使用 Named Pipe。控制协议 v1：

- 请求和响应都是单行 JSON。
- 根对象包含 `version: 1`。
- 单次请求最大 64 KiB。
- 未知字段会被拒绝。
- 控制连接有 5 秒服务端超时。

请求：

```json
{"version":1,"operation":"health","deep":true}
```

响应结构：

```json
{
  "version": 1,
  "ok": true,
  "result": {}
}
```

或：

```json
{
  "version": 1,
  "ok": false,
  "error": {
    "code": "invalid_request",
    "message": "invalid control request"
  }
}
```

### 安全

- Linux Socket 权限为 `0600`。
- Linux 调用者必须与 Runtime 同 effective user，或为 root。
- Windows Named Pipe 只允许 SYSTEM 和本地 Administrators。
- 每次控制操作都先写入审计日志，再向调用者返回结果。
- Control Plane 不监听 TCP。

集成工具可以直接连接 Socket/Named Pipe。普通使用应优先调用 CLI，以复用参数解析、超时和 `status`/`stop` 的 PID 回退。

## CLI

### 生命周期

```bash
./bin/zentao serve
./bin/zentao status
./bin/zentao health --deep
./bin/zentao diagnose
./bin/zentao reload
./bin/zentao stop
./bin/zentao version
```

`status`、`stop`、`reload`、`health` 和 `diagnose` 默认连接配置的控制 Socket。

如果 `status` 或 `stop` 无法连接 Control Plane，CLI 会尝试读取 PID 文件：

- `status` 只确认 PID 对应进程仍在运行。
- `stop` 向进程发送 SIGTERM。

PID 回退只用于兼容诊断，正常管理应修复 Control Plane。

### 可观测性

```bash
./bin/zentao logs --since 30m --level error --node node-a --limit 200
./bin/zentao metrics --since 1h --metric-name http.request.duration --limit 100
./bin/zentao flush-observability
./bin/zentao clean-observability
./bin/zentao convert-jsonl
./bin/zentao collect-logs
```

这些查询针对 Runtime 自身数据集。应用数据集的查询方式见[可观测性接入](observability.md#查询日志)。

常用参数：

| 参数 | 说明 |
| --- | --- |
| `--since` | Go duration，默认 `30m` |
| `--until` | RFC 3339 时间 |
| `--node` | 节点 ID |
| `--level` | 日志级别 |
| `--metric-name` | 指标名 |
| `--limit` | 最大行数 |
| `--control-socket` | 覆盖控制 Socket |

### 管理命令

| 命令 | 作用 |
| --- | --- |
| `flush-observability` | 立即刷新 Runtime 和应用观测缓冲区 |
| `clean-observability` | 按保留期清理本节点自己的分区 |
| `convert-jsonl` | 将已封闭的 Runtime JSONL 转为 Parquet |
| `collect-logs` | 生成受大小限制的诊断压缩包 |

`collect-logs` 只收集本地日志、审计记录、版本和脱敏配置摘要，不收集业务数据库内容、请求正文或凭据。

### 系统服务

```bash
sudo ./bin/zentao service install
sudo ./bin/zentao service uninstall
```

Linux 注册 systemd 服务，Windows 注册系统服务。`run-service` 只作为 Windows Service Control Manager 的内部入口。

## 配置重载

```bash
./bin/zentao reload
```

Runtime 会先重新读取并严格校验配置文件，再尝试加载 Caddy 配置。

热加载成功：

```json
{"reloaded":true,"restartRequired":false}
```

配置合法但需要重启：

```json
{"reloaded":false,"restartRequired":true}
```

需要重启的变化包括：

- 应用清单、路径、目录和环境变量。
- Web 监听地址、线程数和根目录。
- Control Socket、PID 文件和节点身份。
- Queue Worker、租约和 Bridge 参数。
- Observability 监听、数据集和发布参数。
- 日志、审计和保留期设置。
- 应用 PHP 代码或扩展代码变更；`reload` 不会清除 Runtime 内的 OPcache。

候选配置不合法或 Caddy 拒绝新配置时，Runtime 保留旧配置并进入 degraded。修复配置后可以再次执行 `reload`。

## 状态和诊断

### Status

```bash
./bin/zentao status
```

主要返回：

- 生命周期状态和变更原因。
- Web 监听地址和线程数。
- 应用 ID、路径和目录。
- 节点与集群身份。
- 可观测性缓冲区统计。

不会返回应用环境变量和 Bridge Token。

### Diagnose

```bash
./bin/zentao diagnose
```

`diagnose` 默认带上 deep health，并汇总：

- 版本信息。
- 生命周期和组件状态。
- 非敏感配置摘要。
- 可观测性缓冲区、spool 和丢弃计数。

### 日志包

```bash
./bin/zentao collect-logs
```

返回压缩包路径。适合收集：

- Runtime 日志。
- Caddy 访问和错误日志。
- PHP `error_log`。
- Control Plane 审计。
- 版本和非敏感配置摘要。

## 停止和排空

```bash
./bin/zentao stop
```

停止顺序：

1. 进入 draining，readiness 不再成功。
2. 停止 Queue Worker 领取。
3. 等待在途 Queue 任务。
4. 刷新可观测性缓冲区。
5. 优雅停止 Caddy 和 PHP 请求。
6. 释放 PID、Socket 和实例状态。

排空超时后，未完成的 Queue 任务依靠租约恢复，不会伪造任务成功。

## 常见故障

| 现象 | 优先检查 |
| --- | --- |
| 启动提示配置文件不存在 | 创建 `config/runtime.toml` |
| 配置启动失败 | 检查未知字段、Schema、路径和超时格式 |
| readiness 503 | 检查 PHP 初始化、生命周期和应用文档根 |
| 某个应用返回 503 | 检查 `[[apps]].dir` 和 `webRoot` |
| Queue 不消费 | 检查 `capabilities`、Queue Token 和 deep health |
| 观测事件写入 401 | 从当前应用环境重新读取 Token |
| 观测事件写入 413 | 拆分批次并检查单请求大小 |
| 应用日志查询为空 | 确认事件已经发布到 Parquet，并检查查询时间范围 |
| `reload` 返回 restartRequired | 按提示重启 Runtime |

## 相关文档

- [Runtime Control Plane v1 契约](../design/contracts/runtime-control-plane-v1.md)
- [配置与安全](../configuration-and-security.md)
- [Runtime 日志系统开发文档](../development/05-logging-system-development.md)
- [多节点部署](../deployment/multi-node.md)
