# zentao Runtime 文档

本目录是 `zentao-runtime` 的设计、接口契约和开发文档。阅读顺序是先看系统总设计，再按组件阅读设计，最后按开发计划和验收门槛实施。

## 文档分层

### 设计

- [系统总设计](./design/00-system-overview.md)：产品目标、边界、组件、数据流和部署模型。
- [Runtime Host](./design/01-runtime-host.md)：Go Host、Caddy、FrankenPHP、生命周期和控制面。
- [FrankenPHP 集成](./design/02-frankenphp-integration.md)：PHP 8.4 ZTS、Classic mode、ionCube 和平台构建基础。
- [消息队列](./design/03-message-queue.md)：PHP DAO 持久化、Queue Bridge、Worker 和 At-least-once 语义。
- [Runtime 缓存](./design/04-runtime-cache.md)：由 Runtime 提供的缓存服务和客户端协议。
- [可观测性](./design/05-observability.md)：DuckDB、Parquet、日志、指标和降级策略。
- [部署与高可用](./design/06-deployment-and-ha.md)：单机、Docker、双节点和外部负载均衡。
- [构建与发布](./design/07-build-and-release.md)：原生构建、制品组装、供应链和发布。

### 契约

- [Runtime Control Plane v1](./design/contracts/runtime-control-plane-v1.md)
- [PHP Queue Bridge v1](./design/contracts/queue-bridge-v1.md)
- [Runtime Cache Bridge v1](./design/contracts/runtime-cache-v1.md)

契约文档是跨进程、跨语言和跨仓实现的规范来源。实现不能通过读取对方内部文件或数据库表绕过契约。

### 开发

- [Runtime 开发计划](./development/00-runtime-development-plan.md)
- [禅道应用适配计划](./development/01-zentao-application-adaptation-plan.md)
- [验证与发布门槛](./development/02-validation-and-release-gates.md)

## 当前状态

Runtime 侧 Host、控制面、健康检查、队列 Worker、DuckDB/Parquet、升级事务、平台服务、制品组装和 CI 已有实现。禅道 PHP 队列服务、Runtime 缓存客户端接入、Session/附件适配和跨版本联合测试仍按开发计划推进。正式安装器、代码签名和镜像发布需要发布环境与权限。

## 设计原则

1. 单机是首要交付入口，企业级双节点和外部负载均衡是可组合的部署扩展。
2. Runtime 管理自己的库、服务和受控的外部独立二进制服务；不把禅道业务逻辑搬入 Go。
3. Go 不直连禅道业务数据库；队列由 PHP DAO 执行；Go 可以使用内嵌 DuckDB 处理观测数据，并提供 Runtime 缓存。
4. 业务数据库是业务状态和队列状态的事实来源，缓存和 Parquet 都不能成为业务正确性的前提。
5. 所有跨边界接口版本化、可鉴权、可测试，并明确失败降级行为。
