# 应用装载

这篇文档说明如何把一个或多个应用交给 Runtime，包括目录、路由、环境变量、PHP 运行模式和深探针。

## 声明应用

`[[apps]]` 是唯一支持的应用声明方式，`[web]` 只配置公共监听和 PHP 线程。

允许不配置任何 `[[apps]]`。此时 Runtime 可以启动并提供健康检查和本地管理能力，但所有业务路径返回 503；Runtime 不会再自动生成默认应用。

```toml
[[apps]]
id = "zentao-ipd"
path = "/ipd"
dir = "apps/zentao-ipd-22.6"
webRoot = "www"
dataDir = "data/apps/zentao-ipd"
tmpDir = "tmp-local/zentao-ipd"
enabled = true
env = { ZT_DB_NAME = "zentao_ipd" }
```

| 字段 | 说明 |
| --- | --- |
| `id` | 应用唯一标识，用于日志、Bridge Token、队列命名空间和数据目录 |
| `path` | 公共 URL 前缀，最多一个应用使用 `/` |
| `dir` | 应用代码根目录 |
| `webRoot` | 相对 `dir` 的文档根，默认 `www` |
| `dataDir` | 持久数据根，默认 `data/apps/<id>` |
| `tmpDir` | 节点本地临时目录，默认 `tmp-local/<id>` |
| `env` | 应用显式环境变量 |
| `queue` | 应用 Queue Bridge 配置，详见 [Queue Bridge 接入](queue-bridge.md) |

### 路径约束

- `path` 必须以 `/` 开头。
- 路径不能包含 `?`、`#`、反斜杠、重复斜杠或 `.`、`..` 路径段。
- `/_runtime` 是 Runtime 保留前缀。
- 应用路径不能重复，也不能互相嵌套。
- `webRoot` 必须是应用目录内的相对路径。

一个 Runtime 可以声明多个子路径应用：

```toml
[[apps]]
id = "zentao"
path = "/"
dir = "apps/zentao-22.6"
webRoot = "www"
enabled = true
dataDir = "data/apps/zentao"
tmpDir = "tmp-local/zentao"

[[apps]]
id = "zentao-ipd"
path = "/ipd"
dir = "apps/zentao-ipd-22.6"
webRoot = "www"
enabled = true
dataDir = "data/apps/zentao-ipd"
tmpDir = "tmp-local/zentao-ipd"
```

所有应用共享同一个公共 Listener 和同一个 PHP 线程池。隔离边界是文档根、环境变量、数据目录和 Bridge Token，不是独立 PHP 进程。

## 目录布局

每个应用需要把代码、持久数据和节点临时数据分开：

```text
apps/<app-id>-<version>/     # 应用代码，可整体替换
data/apps/<app-id>/
  upload/                    # 附件
  session/                   # 文件 Session
  apisession/                # API Session
  backup/                    # 备份
  observability/             # 应用观测数据
tmp-local/<app-id>/          # 节点本地临时文件
```

附件、Session 和备份不得写入应用代码目录。应用升级只替换代码目录，持久数据仍保留在 `dataDir`。

Runtime 启动时会创建所需的数据和临时目录。目录无法创建时，Runtime 启动失败。

## Runtime 注入的环境

Runtime 为每个应用提供公共前缀、数据目录和日志目录：

| 环境变量 | 含义 |
| --- | --- |
| `ZT_WEB_ROOT` | 应用公共前缀，根应用为空字符串 |
| `ZT_APP_ID` | 应用 ID |
| `ZT_DATA_ROOT` | 持久数据根 |
| `ZT_TMP_ROOT` | 临时目录 |
| `ZT_SESSION_PATH` | `<dataDir>/session` |
| `ZT_APISESSION_PATH` | `<dataDir>/apisession` |
| `ZT_BACKUP_PATH` | `<dataDir>/backup` |
| `ZENTAO_LOG_DIR` | `<dataDir>/observability/raw-logs` |

Runtime 进程还提供节点身份：

| 环境变量 | 含义 |
| --- | --- |
| `ZENTAO_NODE_ID` | 节点 ID |
| `ZENTAO_BOOT_ID` | 当前 Runtime 启动实例 ID |

应用显式配置的环境变量优先于 Runtime 生成值。数据库凭据、数据库地址等业务环境必须由应用或部署配置显式提供，Runtime 不生成或保存这些信息。

Queue 和可观测性启用后，Runtime 还会注入各自的 URL 和 Token，分别见 [Queue Bridge 接入](queue-bridge.md)和[可观测性接入](observability.md)。

### 缓存环境

Runtime 的 PHP 发行包加载 APCu 和 Redis 扩展，但没有缓存 Service、Cache Bridge 或 `[cache]` 配置段。

应用可以使用 APCu 或连接自己配置的 Redis，并自行负责：

- 选择缓存后端。
- 构造和隔离 Key namespace。
- 设置 TTL。
- 处理主动失效和版本失效。
- 在读取失败时回源，在写入失败时保持业务事务不受影响。

同一 Runtime 中的多个应用共享 APCu 空间。每个应用必须把应用 ID、集群或业务 namespace 纳入 Key，不能假设 APCu 天然提供应用隔离。

## 请求路由

公共 Listener 按以下顺序处理请求：

1. `/_runtime/*` 健康探针。
2. 子路径应用匹配。
3. 静态文件。
4. PHP 文件及 PATH_INFO 请求。

子路径请求会剥掉公共前缀后再查找文件和 PHP 入口。PHP 仍能取得带前缀的原始 `REQUEST_URI`，应用通过 `ZT_WEB_ROOT` 构造公开 URL。

例如：

```text
浏览器请求：/ipd/index.php?m=user
文件查找：  <app>/www/index.php
ZT_WEB_ROOT：ipd
```

公共 Listener 不能访问应用目录中 `webRoot` 之外的文件。

## 应用缺失时

应用文档根不存在时，Runtime 不会因此停止：

- Host 继续存活。
- `/_runtime/liveness` 返回成功。
- `/_runtime/readiness` 返回 503。
- 该应用前缀返回 503 占位响应。
- `health --deep` 报告 `app-root:<id>` 失败。

应用重新放回原目录后，仍需重启 Runtime。当前版本不实现应用热加载。

## PHP 运行模式

Runtime 固定使用 FrankenPHP Classic mode：

```text
php_request_startup
  -> 执行 PHP 入口
  -> php_request_shutdown
```

PHP 主线程、ZTS 线程池、OPcache 和扩展会在 Runtime 进程内常驻，但业务对象和全局变量不能跨请求依赖。Session 数据应通过配置的 Session 后端持久化，而不是依赖 PHP 进程内留存。

应用必须兼容普通 PHP 请求模型。需要跨请求共享的数据应写入数据库、缓存或显式的共享存储。

CLI 任务使用：

```bash
./bin/zentao php-cli -- /path/to/script.php
```

该命令使用与 Web 相同的 PHP、扩展和 ionCube 配置。

## 应用深探针

`health --deep` 会请求：

```text
GET <app-prefix>/health.php
```

应用返回：

```json
{"status":"ok"}
```

支持 `ok`、`degraded` 和 `failed`。Runtime 最多读取 64 KiB 响应，并在 5 秒后取消探针。

`health.php` 应只检查应用能否提供基本服务，不应加载完整业务上下文、创建真实用户 Session 或返回配置和凭据。数据库、共享存储或依赖服务不可用时，可以返回 `degraded` 或 `failed`，并使用稳定错误码辅助诊断。

深度探针不得执行昂贵请求，也不得写入业务数据。

## 更新应用

替换应用代码时：

1. 备份应用目录和业务数据目录。
2. 将新应用放置到新的版本目录。
3. 修改对应 `[[apps]].dir`。
4. 执行 `./bin/zentao reload`。

如果返回 `restartRequired: true`，说明 `[[apps]]` 变化不能热加载，需要重启 Runtime。

如果直接在原目录替换 PHP 文件，即使 `runtime.toml` 没有变化，也必须完整重启
Runtime。内置 PHP 使用 `opcache.validate_timestamps=0`，`reload` 只重载 Caddy
配置，不会清除已加载的 PHP 字节码。

应用替换失败时，将 `dir` 指回原版本并重启。数据库迁移和业务回滚不属于 Runtime 能力，由应用发布流程负责。

## 相关文档

- [配置与安全](../configuration-and-security.md)
- [运维与健康检查](operations.md)
- [Runtime Host 设计](../design/01-runtime-host.md)
