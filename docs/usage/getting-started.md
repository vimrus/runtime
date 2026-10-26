# 快速开始

这篇文档用最小配置启动一个应用。Queue 和可观测性先关闭，避免第一次运行就依赖应用实现 Bridge。

## 1. 准备目录

在 Runtime 安装根目录执行。目录名称可以按实际制品调整：

```text
server_root/
  bin/zentao
  config/
  apps/demo-1.0.0/
    www/index.php
```

应用代码目录与 Runtime 制品分离。应用入口应位于 `apps/demo-1.0.0/www`。

## 2. 创建配置

Runtime 固定读取 `config/runtime.toml`，文件缺失时不会启动：

```bash
cp config/runtime.example.toml config/runtime.toml
```

将配置改为：

```toml
schemaVersion = 1

[runtime]
nodeID = "node-a"
clusterID = "cluster-demo"
drainTimeout = "30s"

[web]
listen = "127.0.0.1:8080"
threads = 4
readHeaderTimeout = "10s"
idleTimeout = "30s"
maxHeaderBytes = 16384

[[apps]]
id = "demo"
path = "/"
dir = "apps/demo-1.0.0"
webRoot = "www"
enabled = true

[queue]
enabled = false

[observability]
enabled = false
```

相对路径以 Runtime 安装根目录为基准。应用未填写 `dataDir` 和 `tmpDir` 时，Runtime 使用：

```text
data/apps/demo
tmp-local/demo
```

## 3. 启动

```bash
./bin/zentao serve
```

Runtime 会加载配置、初始化 PHP、装载应用并开始监听 `127.0.0.1:8080`。

## 4. 检查运行状态

应用进程是否存活：

```bash
curl --fail http://127.0.0.1:8080/_runtime/liveness
```

是否可以接收业务流量：

```bash
curl --fail http://127.0.0.1:8080/_runtime/readiness
```

两个接口成功时分别返回：

```json
{"status":"ok"}
```

```json
{"status":"ready"}
```

然后访问业务入口：

```bash
curl --fail http://127.0.0.1:8080/
```

## 5. 检查 CLI 环境

`php-cli` 与 Web 使用同一个 PHP 二进制、扩展和配置：

```bash
./bin/zentao php-cli -- -r 'echo PHP_VERSION, PHP_EOL;'
```

如果 Web 和 CLI 需要一致的 ionCube、数据库扩展或时区配置，应始终通过该命令执行应用脚本。

## 6. 停止

```bash
./bin/zentao stop
```

`stop` 会先摘除 readiness，再等待在途请求完成。如果 Caddy 排空超过 `runtime.drainTimeout`，Runtime 会记录排空失败并结束 Host；未完成的队列任务仍通过租约恢复。

## 下一步

应用已经可以接收请求后，按需要继续接入：

1. [多应用、路由和环境变量](application-hosting.md)
2. [日志、度量与日志查询](observability.md)
3. [Queue Worker 和 Queue Bridge](queue-bridge.md)
4. [健康检查、Control Plane 与诊断](operations.md)

应用要实现 `health --deep` 的业务探针时，可先阅读[应用深探针](application-hosting.md#应用深探针)。
