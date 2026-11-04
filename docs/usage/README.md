# Runtime 使用开发指南

Runtime 是禅道应用的统一运行环境，负责 Web/PHP 执行、队列 Worker、常驻 PHP
进程监管、BI/OLAP、可观测性和生命周期管理。Runtime 制品不包含应用代码，也不
连接禅道业务数据库；业务数据、任务持久化和 Handler 始终由应用负责。

## 快速开始

从最小配置开始，创建应用目录、生成 `config/runtime.toml`、启动 Runtime，并
验证页面和健康检查。

详见[快速开始](getting-started.md)。

## 应用与 Web

介绍 `[[apps]]` 应用声明、URL 前缀、目录和环境变量，以及 FrankenPHP Classic
mode、PHP CLI、应用深探针和缓存扩展边界。

详见[应用装载](application-hosting.md)。

## 消息队列

应用通过 PHP DAO 在业务数据库中持久化任务，Runtime 通过本机 Queue Bridge
领取任务、管理并发和租约、续租、重试及回收过期任务。

详见[Queue Bridge 接入](queue-bridge.md)。

## 常驻 PHP 进程

应用通过 `[[apps.processes]]` 声明常驻 PHP 脚本。Runtime 负责 PHP CLI
生命周期、重启和日志，应用通过 Process SDK 和私有本机 RPC 与进程通信。

详见[常驻 PHP 进程](resident-php-processes.md)。

## 可观测性

应用向 Runtime 发送结构化日志和度量，Runtime 负责校验、脱敏、缓冲、Parquet
发布以及受限的日志查询。

详见[可观测性接入](observability.md)。

## BI / OLAP

应用从业务数据库导出分析数据到 staging，Runtime 负责生成 Parquet 和 Manifest，
并通过受控 DuckDB 查询为 BI 提供只读数据集。

详见[BI / OLAP 接入](bi-olap.md)。

## 运维与健康检查

介绍 liveness、readiness、deep health、Control Plane、CLI、配置重载、进程
管理和诊断收集。

详见[运维与健康检查](operations.md)。

## 速查参考

集中查询端点、环境变量、限制、超时和错误码。

详见[速查参考](reference.md)。
