# 常驻 PHP 进程设计

## 1. 文档信息

| 项目 | 内容 |
|---|---|
| 状态 | v1 核心实现完成；三平台制品回归待执行 |
| 日期 | 2026-11-04 |
| 适用范围 | Windows x86_64、Linux amd64/arm64、Docker |
| Runtime | `zentao-runtime` 监管 PHP CLI 子进程 |
| Web PHP | 继续使用 FrankenPHP Classic mode |
| 关联设计 | [Runtime Host](./01-runtime-host.md)、[FrankenPHP 集成](./02-frankenphp-integration.md)、[消息队列](./03-message-queue.md)、[Service 层](./10-service-layer.md) |

## 2. 核心结论

增加一种由应用声明、由 Runtime 监管的 **常驻 PHP 进程**：

```text
应用 Web 请求
  -> Runtime 私有 Process Bridge（127.0.0.1 随机端口）
  -> 有界内存请求队列
  -> PHP CLI 常驻子进程
  -> 处理完成后回传结果
```

设计边界如下：

1. 常驻脚本使用 Runtime 自带的 PHP CLI 可执行文件，加载与 Web 相同的
   `php.ini`、PHP 8.4 ZTS 扩展和 ionCube Loader。
2. FrankenPHP 仍只负责 Classic mode Web 请求。常驻进程不占用 FrankenPHP
   线程池，不启用 FrankenPHP worker mode，也不把 PHP 应用对象跨 Web 请求复用。
3. Runtime Host 负责启动、就绪握手、崩溃重启、超时、日志、健康检查和优雅停止。
4. 应用 Web 代码通过 Runtime 提供的 SDK 调用常驻进程；Runtime 负责本机传输、
   鉴权、排队和超时，不解释业务方法或访问业务数据库。
5. 常驻进程只提供**短生命周期同步 RPC**。需要持久化、重试、死信、跨节点执行
   或延迟执行的工作继续使用现有 Queue Bridge，不复制第二套队列。
6. 一个配置项对应一个 PHP CLI 进程，首版不自动启动多副本。需要并行隔离时
   声明多个命名进程，由应用自行划分职责。

## 3. 与现有能力的关系

### 3.1 必须使用常驻进程的场景

- 需要在多次调用间保留 PHP 进程内状态。
- 需要维护一个长连接，并在应用请求到来时复用该连接。
- 需要预加载较大的只读结构，避免每次 Web 请求重复初始化。
- 需要运行一个应用级事件循环，并由 Web 代码按需发命令。

### 3.2 应继续使用现有 Queue 的场景

- 后台任务、批处理和定时任务。
- 必须持久化、自动重试、退避或进入死信的任务。
- 多节点中只允许一个执行者，或必须按租约防止重复执行的任务。
- 任务执行时间可能超过 Web 请求预算。
- 不能在进程崩溃或节点切换时丢失的工作。

常驻进程不是 At-least-once 队列，不持久化请求，也不在进程重启后重放请求。

### 3.3 不采用的方案

| 方案 | 不采用原因 |
|---|---|
| FrankenPHP worker mode | 违反已确认的 Classic-only 边界，并将 Web 请求状态和常驻脚本生命周期混在一起 |
| 在 Host 的 PHP Embed SAPI 中启动长期请求 | 会占用 Web ZTS 线程，且无法复用 CLI 生命周期语义 |
| 每次调用启动一个 PHP CLI | 不能保留进程内状态，属于队列或普通 `php-cli` 的职责 |
| 由脚本自行监听固定 TCP 端口 | 端口冲突、鉴权和 Windows 生命周期分散在应用代码中 |
| 仅用 systemd/Windows Service 管理脚本 | 无法按应用隔离、注入应用环境，也不能统一跨平台控制面 |
| Runtime 直接执行业务 SQL | 违反 Go 不连接禅道业务数据库的边界 |

## 4. 总体架构

```text
                       zentao-runtime
  +-----------------------------------------------------------+
  | Caddy + FrankenPHP Classic                                |
  |   public listener -> Web PHP request                      |
  |                              |                            |
  |                              | Process SDK                |
  |                              v                            |
  | Process Bridge (net/http, 127.0.0.1:<ephemeral>)          |
  |   app endpoint: call -> pending queue                     |
  |   worker endpoint: ready / next / reply / shutdown        |
  |                              |                            |
  | Process Supervisor            |                            |
  |   start / ready / restart / drain / kill                  |
  +------------------------------|----------------------------+
                                 |
                                 v
                    bundled PHP CLI process
                      - same PHPRC/extensions/ionCube
                      - application environment
                      - one configured script
                      - SDK bridges next/reply

  持久化工作：PHP application -> existing Queue Bridge -> business DB
```

Process Bridge 使用 Runtime 启动时分配的 `127.0.0.1` 临时端口，不配置固定端口，
不通过公共 Caddy listener 暴露。端口、URL 和 Token 只通过环境变量提供给对应应用。

## 5. 进程模型

### 5.1 可执行文件

Host 只允许使用 Runtime 制品中的 PHP CLI：

```text
Linux:   <install-root>/php/bin/php
Windows: <install-root>\runtime\php.exe
```

不接受配置任意解释器、Shell 字符串或 PATH 中发现的 `php`。Host 为子进程设置
与 `zentao-runtime php-cli` 相同的 `PHPRC`、`PHP_INI_SCAN_DIR`、
`ZENTAO_RUNTIME_PHP_ROOT` 和达梦配置环境，并用受控的
`-d max_execution_time=0` 启动长期循环；业务单次处理仍受 Process RPC
`requestTimeout` 约束。

CLI 中的 ionCube 加载、扩展集合和 PHP patch 版本必须纳入发布验证。CLI 进程拥有
独立 OPcache 和地址空间；不能依赖它与 Web 进程共享 PHP 对象或 APCu 状态。

### 5.2 生命周期状态机

```text
stopped
  -> starting
  -> handshaking
  -> ready <-> busy
  -> draining
  -> stopped

starting/handshaking/ready/busy
  -> backoff
  -> starting

starting/handshaking
  -> failed
```

- `starting`：已创建子进程，尚未完成协议握手。
- `handshaking`：脚本已启动，正在等待 Ready 登记。
- `ready`：可以接收新的 RPC。
- `busy`：已向脚本交付请求，等待 Reply；进程仍存活。
- `backoff`：异常退出，等待指数退避后重启。
- `failed`：超过重启窗口或禁止重启，需要人工处理。
- Host 正常停止导致的退出不触发重启。

### 5.3 启动顺序

```text
加载并校验配置
  -> 生成应用/进程 Token 和临时 Bridge
  -> 启动 Caddy/FrankenPHP
  -> 启动应用常驻进程
  -> 在 ReadyTimeout 内等待 required 进程
  -> required 进程全部 Ready 后报告 Runtime Ready
```

非 `required` 进程异步启动和重试，不延迟 Runtime Ready；未 Ready 时进程组件
报告 degraded，调用返回 `process_unavailable`。

进程脚本缺失、启动失败或 Ready 超时：

- 不让 Runtime 进程崩溃。
- 记录稳定的错误码和退出原因。
- 非 `required` 进程使整体状态为 `degraded`。
- `required=true` 进程使 readiness 为 not ready。

### 5.4 停止顺序

```text
标记 not ready
  -> Caddy 停止接收新请求并完成既有 Web 请求
  -> Process Bridge 停止接收新 call
  -> 向进程发送逻辑 shutdown
  -> 等待 worker 完成当前请求并退出
  -> 超时后终止进程组/Job Object
  -> 停止 Queue 和可观测性组件
```

Linux 使用独立 process group；Windows 使用独立 Job Object。无论 PHP 父进程还是
其派生进程，Host 退出时都不得遗留孤儿进程。

### 5.5 重启策略

默认策略：

```text
restart = "on-failure"
minBackoff = "500ms"
maxBackoff = "30s"
restartWindow = "10m"
maxRestarts = 8
```

同一窗口内超过重启次数后进入 `failed`，不再自动重启。配置变更或本地管理员执行
`process restart` 可以重置失败状态。所有重启带随机抖动，避免多个进程同时恢复。

## 6. 配置模型

常驻进程归属于应用，在对应 `[[apps]]` 下声明：

```toml
[[apps]]
id = "zentao"
path = "/"
dir = "apps/zentao"
webRoot = "www"
enabled = true

[[apps.processes]]
name = "event-worker"
script = "service/event-worker.php"
args = ["--mode=resident"]
workDir = "."
required = false
readyTimeout = "30s"
requestTimeout = "15s"
stopTimeout = "20s"
maxPending = 32
restart = "on-failure"
maxRestarts = 8
restartWindow = "10m"
minBackoff = "500ms"
maxBackoff = "30s"
```

约束：

- `name` 在应用内唯一，只允许稳定标识字符。
- `script` 必填，是应用目录内相对路径；解析符号链接后仍必须位于应用目录。
- `workDir` 默认应用根目录，解析后必须位于应用目录。
- `args` 是字符串数组，不经过 Shell，不做变量展开和重定向。
- `required` 默认 `false`，避免非核心进程故障导致整个 Web not ready。
- `requestTimeout` 是单次 RPC 的硬上限，应用 SDK 只能选择更短的调用超时。
- `maxPending` 是每个常驻进程的待执行请求上限，满时立即返回 busy。
- 单个常驻进程配置首版只对应一个 OS 进程，Handler 串行执行。
- 进程配置变化必须完整重启 Runtime；脚本文件更新至少需要重启对应进程以清除
  CLI OPcache，可通过本地控制面的单项重启完成。

## 7. 应用通信

### 7.1 Runtime 提供的 SDK

Runtime 制品增加一个无 Composer 依赖的 PHP SDK，并通过环境变量提供路径：

```text
ZENTAO_PROCESS_SDK=<install-root>/lib/php/runtime-process.php
```

该文件由 Runtime 构建和打包时安装到统一的相对目录，不依赖 Linux 的
`php/lib` 或 Windows 的 `runtime` 布局。Linux、Windows 和 Docker 都只通过
Runtime 注入的绝对路径加载，不要求应用硬编码安装目录。

### 7.2 应用侧加载方式

业务 Module 不直接 `require` Runtime SDK。应用 Service 层增加一个 Runtime
适配器，例如 `service/runtime/runtime.service.php`，由它延迟加载并缓存当前
请求内的客户端实例：

```php
final class runtimeProcessService
{
    private $client = null;

    public function client()
    {
        if($this->client !== null) return $this->client;

        if(!class_exists('RuntimeProcessClient', false))
        {
            $sdk = getenv('ZENTAO_PROCESS_SDK');
            if(!is_string($sdk) || $sdk === '' || !is_file($sdk))
            {
                throw new RuntimeException('runtime_process_unavailable');
            }
            require_once $sdk;
        }

        return $this->client = RuntimeProcessClient::forCurrentApp();
    }
}
```

业务代码通过既有 Service 层使用：

```php
$processService = $app->loadService('runtimeProcess');
$result = $processService->client()->call('event-worker', 'message.send', $payload, 5000);
```

加载规则：

- Web PHP 由 Runtime 在请求环境中注入 `ZENTAO_PROCESS_SDK`。
- 常驻脚本由 Runtime 在子进程环境中注入同一路径，脚本在启动时执行一次
  `require_once`。
- SDK 文件通过 `require_once` 显式加载，不要求应用修改 Composer 配置，也不把
  类文件复制到应用目录。
- PHP Classic mode 每个请求仍有独立的 PHP request context；应用适配器只在当前
  请求内缓存客户端对象，不能依赖全局对象跨请求共享。
- 在 Apache/PHP-FPM 等非 Runtime 环境中，`ZENTAO_PROCESS_SDK` 不存在。
  适配器返回明确的 `runtime_process_unavailable`，不把本机文件或旧端口
  作为兜底。

应用常驻脚本示例：

```php
<?php
require getenv('ZENTAO_PROCESS_SDK');

$worker = new RuntimeProcessWorker();

$worker->run(function (array $request): array {
    switch ($request['method']) {
        case 'connection.status':
            return ['connected' => true];
        case 'message.send':
            // 在 PHP 中执行应用允许的业务操作。
            return ['accepted' => true];
        default:
            throw new RuntimeProcessMethodNotFound();
    }
});
```

应用 Web 代码示例：

```php
require getenv('ZENTAO_PROCESS_SDK');

$client = RuntimeProcessClient::forCurrentApp();
$result = $client->call(
    'event-worker',
    'message.send',
    ['topic' => 'example', 'body' => 'hello'],
    5000
);
```

SDK 负责构造请求、校验版本、设置 Token、处理长轮询和把 Runtime 错误映射为异常。
应用代码不直接拼接 Bridge 地址或读取进程 PID。

### 7.3 RPC 语义

首版只支持应用调用常驻进程并等待响应：

```text
call(requestId, method, payload, deadline)
  -> Runtime 校验应用和进程
  -> 请求进入有界队列
  -> worker next 取得一个请求
  -> 脚本处理并 reply
  -> Runtime 将结果返回调用方
```

规则：

- 单次请求和响应有固定字节上限，建议各 1 MiB；超限拒绝，不截断。
- 不记录 payload 和 result 正文。
- Runtime 不维护业务方法白名单；脚本内部必须注册允许的方法。
- 不支持流式响应、文件上传或 WebSocket 透传。
- 同步 call 会占用一个 Web PHP 线程；默认超时必须明显小于 Web 请求剩余预算。
- 不自动重试任何 call。请求已交付后超时，必须返回 `outcome_unknown`，不能宣称
  业务操作失败。
- 调用方需要重试时，应提供业务幂等键并由进程实现去重。
- 常驻进程退出后，未交付请求返回 `process_restarted`；已交付但无结果的请求返回
  `outcome_unknown`。
- 常驻进程从 `ready` 转为 `busy` 后，新的并发请求可以在 `maxPending` 内排队；
  超过上限返回 `process_busy`。

需要队列语义时使用现有 Queue Bridge，而不是给 Process Bridge 增加持久化和重试。

### 7.4 协议版本

Bridge 路径使用固定前缀：

```text
/internal/runtime/processes/v1/apps/<appID>/<processName>/call
/internal/runtime/processes/v1/apps/<appID>/<processName>/ready
/internal/runtime/processes/v1/apps/<appID>/<processName>/next
/internal/runtime/processes/v1/apps/<appID>/<processName>/reply
```

请求和响应使用严格 JSON、固定 schema 版本、未知字段拒绝和大小限制。正式实现前
DTO、错误码、时间格式和 HTTP 状态映射冻结在
[Runtime PHP Process RPC v1](./contracts/process-rpc-v1.md)；该契约与 Queue
Bridge 独立。

### 7.5 鉴权与运行环境

每次 Runtime 启动生成随机凭据：

- Web 应用获得应用级 call Token，只能调用本应用声明的常驻进程。
- 每个常驻进程获得实例级 worker Token，只能登记、领取和回复自己的请求。
- Token 每次启动轮换，不写入 `runtime.toml`，不进入日志和审计。

Runtime 注入的保留变量由 Host 最终覆盖，应用 `env` 不能改写：

```text
ZENTAO_PROCESS_BRIDGE_URL
ZENTAO_PROCESS_SDK
ZENTAO_PROCESS_APP_ID
ZENTAO_PROCESS_CALL_TOKEN        # Web PHP
ZENTAO_PROCESS_NAME              # 常驻脚本
ZENTAO_PROCESS_INSTANCE_ID       # 常驻脚本
ZENTAO_PROCESS_WORKER_TOKEN      # 常驻脚本
ZENTAO_PROCESS_SCHEMA=1
```

子进程同时获得与 Web 相同的 `ZT_*`、`ZENTAO_LOG_DIR`、Queue/观测变量和显式
`apps[].env`，因此仍通过 PHP 应用自己的 DAO、Session 和日志能力工作。Runtime
不向子进程注入 Host 管理 Token。

Process Bridge 只监听 `127.0.0.1`，固定校验 HTTP 方法、Token、schema 和请求体
大小。它与应用公共 Listener、Control Plane、Queue Bridge 和 OLAP Bridge 完全分离。

## 8. 健康、日志和控制面

每个常驻进程注册独立健康项：

```text
process:<appID>:<processName>
```

状态至少包含：

- `state`：starting、ready、busy、backoff、failed、stopped。
- `pid` 和当前实例 ID。
- 最近启动、Ready、退出和重启时间。
- 最近退出码和脱敏原因。
- 重启次数、待处理请求数、delivered 请求是否超过 deadline 和最近 RPC 错误码。

`required=true` 的进程参与 readiness；所有常驻进程都出现在本地 `health --deep` 和
`diagnose` 中。单个可选进程故障只能标记 degraded。

日志边界：

- Runtime 记录 start、ready、exit、restart、timeout、forced kill。
- 子进程 stdout/stderr 写入
  `<dataDir>/observability/raw-logs/processes/<appID>-<processName>-<nodeID>.log`，
  由 Runtime 做有界轮转。多节点共享应用数据目录时，每个节点只写自己的日志文件。
- 应用可通过 `ZENTAO_LOG_DIR` 继续写结构化日志。
- Token、请求 payload、响应正文和业务凭据不得进入 Runtime 日志。

本地控制面增加只读 `processes` 状态，以及一个受审计的单项重启操作。控制面不提供
任意脚本、任意命令、任意环境变量或任意进程启动功能。

## 9. 多应用与多节点

- 多应用之间按 app ID、进程名、Token、子进程和环境隔离。
- 一个应用只能调用自己的常驻进程，不能借用另一个应用的 Token。
- 每个 Runtime 节点独立启动本节点声明的常驻进程，不提供跨节点 RPC 路由。
- 首版不提供分布式单例、主节点选举或进程请求跨节点故障转移。
- 需要全集群只执行一次的任务必须走 Queue 的租约/fencing，或由 PHP 应用实现数据库租约。
- 常驻进程内状态是节点本地状态；节点故障时不会自动恢复。业务关键状态必须落到
  数据库或其他由部署方提供的共享存储。
- 常驻进程声明属于应用配置指纹；多节点应保持一致，差异通过现有集群配置诊断报告。

Docker 中常驻进程与 Web Runtime 位于同一容器和 PID namespace，不新增常驻进程专用
容器。容器 PID 1 仍为 `zentao-runtime`。

## 10. 安全与故障边界

1. 常驻脚本是受信任的应用代码，与 Web 代码共享同一 OS 用户和业务凭据边界；
   Runtime 不为它提供安全沙箱。
2. 只接受配置中显式声明的脚本，不扫描目录，不接受 URL、Shell 命令或动态脚本参数。
3. 应用目录、脚本和工作目录必须通过路径 containment 检查，防止配置穿越和符号链接逃逸。
4. Process Bridge 只绑定 loopback，所有端点使用启动期随机 Token 和严格大小限制。
5. 未知请求字段、未知 schema、超大响应、跨应用 Token 和过期实例 ID 一律拒绝。
6. RPC 不持久化。进程崩溃、节点故障和调用超时可能产生未知结果，调用方必须按业务
   幂等性处理。
7. Process RPC 请求/响应正文不进入 Runtime 日志；脚本 stdout/stderr 属于应用
   输出，应用必须避免打印 Token、密码和完整业务 Payload。
8. 常驻进程故障不能让小流量存活探针失败，也不能在没有配置 `required=true` 时阻断
   整个 Web readiness。
9. 观测和日志写入失败不能阻断进程 RPC，但必须记录 degraded 状态。

## 11. 实施建议

### 阶段一：契约与最小原型

- 冻结 Process RPC v1 配置、DTO、错误码和 Token 边界。
- 实现 Go 侧进程监管、Ready/Next/Reply 最小链路和 PHP SDK。
- 验证 Web PHP -> SDK -> Runtime -> PHP CLI -> SDK -> Web PHP 往返。
- 验证命令行和常驻脚本都加载同一 ionCube Loader。

### 阶段二：跨平台和运维能力

- Linux process group 与 Windows Job Object。
- 指数退避、重启窗口、优雅 shutdown、强制清理。
- 健康项、控制面状态/单项重启、stdout/stderr 轮转。
- Docker PID 1 信号与进程树测试。

### 阶段三：验收与加固

- 多应用 Token 隔离、跨应用调用拒绝、超大请求、错误 schema。
- 常驻进程崩溃、启动卡死、Handler 卡死、Host 停止和客户端断连。
- 24 小时稳定性、内存增长和重启恢复。
- Windows x86_64、Linux amd64/arm64、Docker 原生构建矩阵。

## 12. 验收标准

1. 常驻脚本使用与 Web 相同的 PHP 8.4、扩展和 ionCube Loader。
2. 一个应用可声明多个常驻进程；每个进程独立环境、Token、日志和健康状态。
3. Web PHP 代码可通过 SDK 同步调用指定常驻进程并取得结构化结果。
4. 常驻进程崩溃按策略重启；超过阈值后保持 failed，不拖垮 Web。
5. Host 正常停止时常驻进程先完成逻辑 shutdown 和有界排空，再终止进程树。
6. Windows 异常退出不遗留 PHP 子进程；Linux 不遗留 process group。
7. 跨应用调用、未知 schema、超大 body 和公共网络访问全部被拒绝。
8. 常驻进程请求超时明确区分 `not_delivered` 和 `outcome_unknown`，Runtime 不自动重试。
9. 需要持久化、重试和跨节点单例的工作继续由现有 Queue 承担。

## 13. 首版决策

1. 首版只做应用 -> 常驻进程的同步 RPC；进程主动回调应用继续使用 Queue/数据库。
2. `required` 默认 `false`，应用按需将关键进程纳入 readiness。
3. 一个进程配置对应一个 OS 进程，首版不做多副本和自动扩缩容。
4. 正式 SDK 路径为 `<install-root>/lib/php/runtime-process.php`，类名为
   `RuntimeProcessClient` 和 `RuntimeProcessWorker`。
5. 首版沿用统一 `php.ini`，不提供进程级 `memoryLimit` 或额外 INI 白名单。
6. 控制面提供异步单项 `process-restart`，用于应用代码更新后清除 CLI OPcache。

## 14. 当前实现状态

已实现：

- `[[apps.processes]]` 严格配置、默认值、路径校验和重启分类。
- Runtime 分配的 loopback Process Bridge、随机 call/worker Token 和严格 JSON 契约。
- PHP CLI 启动、Ready/Next/Reply、进程组/Windows Job Object、崩溃重启和日志轮转。
- `RuntimeProcessClient`、`RuntimeProcessWorker` 和 PHP 异常映射。
- `process:<appID>:<processName>` 健康项、`processes` 状态和异步
  `process-restart` 控制操作。
- Linux 真实 PHP CLI 端到端测试、broker race 测试、配置/集群 profile 测试和
  Windows Go 交叉编译。
- Linux amd64 正式 artifact 已通过 Web PHP -> SDK -> CLI -> Reply ->
  process-restart -> 再次调用的完整 smoke。

仍需在正式制品环境验证：

- Windows x86_64 上真实 PHP CLI、Job Object、停止和异常退出。
- Linux arm64 Runtime/Full 制品中的真实 Web PHP -> SDK -> CLI 链路。
- Docker PID 1、信号、只读配置和日志轮转。
- 24 小时内存、重启和连接稳定性。
