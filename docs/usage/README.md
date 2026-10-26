# Runtime 使用开发指南

Runtime 是禅道应用的统一运行环境。它负责启动 Caddy 和 FrankenPHP、装载应用、执行 Queue Worker、收集观测事件，并提供本机管理能力。

这组文档只描述 Runtime 自己的边界和接口。Runtime 不认识禅道业务 Service、Model 或 DAO；业务数据库、任务持久化和 Handler 执行都留在应用侧。

## 从哪里开始

| 你要完成的事情 | 阅读顺序 |
| --- | --- |
| 第一次启动 Runtime 并访问应用 | [快速开始](getting-started.md) → [应用装载](application-hosting.md) |
| 把现有 Web 应用接入 Runtime | [应用装载](application-hosting.md) → [运维与健康检查](operations.md) |
| 发送日志、度量或查询应用日志 | [可观测性接入](observability.md) |
| 让 Runtime 执行应用任务 | [Queue Bridge 接入](queue-bridge.md) |
| 部署后检查、诊断或热加载 | [运维与健康检查](operations.md) |
| 查询字段、限制、错误码 | [速查参考](reference.md) |

## Runtime 的边界

Runtime 提供四类能力：

| 能力 | Runtime 负责 | 应用负责 |
| --- | --- | --- |
| Web 和 PHP | 监听、路由、PHP 运行环境、Classic mode 请求隔离 | 应用文件、业务代码、数据库连接 |
| 队列执行 | Worker 并发、领取节奏、租约续期、回收触发 | 队列持久化、Handler 执行、业务幂等 |
| 可观测性 | 事件接收、校验、缓冲、Parquet 发布和受限查询 | 产生事件、发送前脱敏、选择业务指标 |
| 生命周期 | 健康探针、控制面、诊断、PHP CLI | 提供应用深探针和可诊断的运行状态 |

Runtime 不提供通用业务 RPC，不访问业务数据库，也不替代应用的业务事务。

### 缓存边界

Runtime 发行包加载 APCu 和 Redis PHP 扩展，使应用可以在 Runtime 进程内使用 APCu，或连接部署方提供的外部 Redis。

这不等于 Runtime 提供缓存能力。Runtime 没有 Cache Service、Cache Bridge、缓存配置段或缓存管理接口。缓存后端选择、Key、TTL、失效策略和故障降级都由托管应用负责。

多个应用共享同一个 PHP 进程和 APCu 空间。应用必须使用稳定的应用级 namespace，避免不同应用发生 Key 冲突。

## 三种访问边界

Runtime 的接口按访问来源分成三组：

```text
公共 Web Listener
  ├─ /_runtime/healthz
  ├─ /_runtime/liveness
  ├─ /_runtime/readiness
  └─ 已配置应用的业务请求

本机 Private Listener
  ├─ Queue Bridge
  └─ Observability Bridge

本机 Control Plane
  ├─ Unix Socket
  └─ Windows Named Pipe
```

公共 Listener 可以被负载均衡访问。Queue Bridge、Observability Bridge 和 Control Plane 不得暴露到公网。

应用与 Runtime 的主要调用方向如下：

```text
浏览器/客户端 -> Runtime -> 应用 PHP

应用 -> Observability Bridge -> Runtime -> Parquet

Runtime Queue Worker -> Queue Bridge -> 应用

运维工具 -> Control Plane -> Runtime Host
```

其中 Queue 是反向调用：Runtime 主动领取工作并调用应用提供的 Bridge，而应用不需要向 Runtime 提供业务投递 API。

## 文档地图

| 文档 | 内容 |
| --- | --- |
| [快速开始](getting-started.md) | 配置文件、最小应用、启动和第一轮验证 |
| [应用装载](application-hosting.md) | `[[apps]]`、路由、目录、环境变量、Classic mode、应用深探针 |
| [可观测性接入](observability.md) | 事件写入、日志查询、指标、保留和降级 |
| [Queue Bridge 接入](queue-bridge.md) | 队列配置、Bridge 端点、租约和一致性 |
| [运维与健康检查](operations.md) | 探针、CLI、Control Plane、重载和诊断 |
| [速查参考](reference.md) | 端点、环境变量、限制、错误码和指标注册表 |

## 版本基线

| 项目 | 版本或约束 |
| --- | --- |
| Runtime 配置 | `schemaVersion = 1` |
| Queue Bridge | v1 |
| Observability Schema | v1 |
| Control Plane | v1 |
| PHP | PHP 8.4 ZTS，FrankenPHP Classic mode |

跨进程接口遵循版本化契约。未知字段通常会被拒绝；新增必填字段或改变既有语义必须升级协议版本。

## 相关文档

- [Runtime 配置与安全](../configuration-and-security.md)
- [Queue Bridge v1 契约](../design/contracts/queue-bridge-v1.md)
- [Runtime Control Plane v1 契约](../design/contracts/runtime-control-plane-v1.md)
- [Runtime 可观测性设计](../design/05-observability.md)
- [Runtime 多节点部署](../deployment/multi-node.md)
