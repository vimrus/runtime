# ZenTao Runtime

ZenTao Runtime 是禅道新一代集成运行环境。项目计划将 Caddy 和 FrankenPHP 作为 Go Library 嵌入自研 `zentao-runtime`，使用 PHP 8.4 ZTS Classic mode 运行禅道，并面向 Windows、Linux 和 Docker 提供一致的安装、运行和构建能力。

当前仓库处于详细设计阶段，尚未开始 Runtime、Workflow、构建脚本和 PHP 补丁的实现。

## 已确认的技术边界

- Caddy 和 FrankenPHP 均作为 Go Library 集成。
- FrankenPHP 使用 Classic mode，不使用 Worker mode。
- PHP 基线为 PHP 8.4 ZTS，付费版本支持 ionCube Loader。
- Linux PHP 使用最小 `zend_signal` ABI Shim 兼容 ionCube。
- Go Runtime 不直接连接禅道数据库，数据库访问由 PHP DAO 负责。
- 消息队列使用数据库持久化、PHP Queue Service 和 Go Worker Pool。
- 缓存使用数据库显式 `get/set/delete`，不引入默认外部缓存服务。
- GitHub Actions 使用目标平台原生 Runner 构建 Windows、Linux 和 Docker 交付物。

## 设计文档

- [FrankenPHP 集成环境技术方案](./docs/frankenphp-integration-technical-plan.md)
- [Runtime Host 详细设计](./docs/runtime-host-library-design.md)
- [GitHub Actions 构建与打包详细设计](./docs/github-actions-build-design.md)
- [消息队列详细设计](./docs/message-queue-design.md)
- [数据库显式缓存详细设计](./docs/database-cache-design.md)

