# ZenTao Runtime

ZenTao Runtime（可执行文件：`zentao`）是禅道的统一运行环境。它把 Web 服务、PHP 8.4、进程生命周期、健康检查、日志、诊断和可选的队列/观测能力组合成一个可交付产品，支持单机部署，也支持 Linux 双节点接入现有负载均衡。

本文中的 `/opt/zentao` 是 Linux Runtime 根目录示例。Runtime 会根据可执行文件位置自动识别根目录，配置中的相对路径也以该目录为基准。

文中的 `zentao` 和 `./bin/zentao` 指向同一个程序：前者要求程序已加入 `PATH`，后者是在 Runtime 根目录下直接调用二进制文件。未配置 `PATH` 时请使用 `./bin/zentao`；Windows 对应为 `runtime\\zentao.exe`。

## 产品形态

| 形态 | 内容 | 适用场景 |
| --- | --- | --- |
| Runtime | Runtime、PHP、FrankenPHP/Caddy，**不含应用代码** | 已有 MySQL/PostgreSQL 等数据库的单机或集群 |
| 应用包 | 平台无关的禅道应用制品，按 `[[apps]]` 配置装载，可独立升级 | 单应用或多应用（同 host 子目录）部署 |
| Full | Runtime + MySQL 8.4 | 快速交付的单机环境；生产环境仍应规划数据库备份 |
| Docker | Web 容器，可选 MySQL Compose profile | Docker 或已有数据库的容器部署 |

Runtime 不直接访问禅道业务数据库。数据库连接由 PHP 应用和禅道 DAO 负责；启用队列或观测功能时，相关能力通过 Runtime 提供的本机服务使用。

## 快速开始

### Linux

请在 Runtime 根目录执行。Runtime 固定读取 `<安装根>/config/runtime.toml`，
**该文件是必需的**：缺失时启动会直接报错并提示创建方式。首次使用先从模板创建：

```bash
sudo install -m 0640 config/runtime.example.toml config/runtime.toml
```

按需编辑监听地址、`[[apps]]` 应用清单等配置，准备好应用发布目录后启动：

```bash
./bin/zentao serve
```

也可以使用统一的服务注册命令。请在 Runtime 根目录执行，Runtime 会自动识别根目录：

```bash
sudo ./bin/zentao service install
sudo systemctl status zentao
```

安装脚本默认创建 `zentao` 系统用户。配置样例见 [`config/runtime.example.toml`](./config/runtime.example.toml)。

### Docker Compose

Compose 模板会把 `packaging/docker/config/` 挂载为容器的 `config/` 目录，
因此需要先把配置放进去（配置文件是必需的，缺失时容器启动即报错提示）：

```bash
mkdir -p packaging/docker/config
cp config/runtime.example.toml packaging/docker/config/runtime.toml
```

配置中的相对路径按容器内的 Runtime 根目录 `/opt/zentao` 解析，应用放在
`apps/`（`zentao-apps` 卷）下；Web 默认监听 `0.0.0.0:8080`。

仅启动 Web（连接外部数据库）：

```bash
docker compose -f packaging/docker/compose.yaml up -d web
```

启动 Web 和 Compose 内的 MySQL：

```bash
docker compose -f packaging/docker/compose.yaml --profile full up -d
```

使用外部数据库时，通过 `ZT_DB_DRIVER`、`ZT_DB_HOST`、`ZT_DB_PORT`、`ZT_DB_NAME`、`ZT_DB_USER`、`ZT_DB_PASSWORD` 和 `ZT_DB_PREFIX` 配置 PHP 应用。不要把生产密码提交到 Compose 文件或仓库。

## 运行目录

```text
/opt/zentao/
  bin/zentao               Runtime 程序
  apps/zentao-ipd-22.6/    可拔插应用目录（应用代码，由配置装载）
  data/apps/zentao-ipd/    应用数据：附件（upload/）、Session、备份、观测数据（多节点共享）
  tmp-local/zentao-ipd/    应用临时区：缓存、编译产物、SQLite（节点本地）
  config/runtime.toml      Runtime 固定配置
  data/observability/      Runtime 自身遥测数据集：访问/运行日志与自监控指标
  logs/                    Runtime、访问和 PHP 日志
  run/                     PID、控制 socket（节点本地）
  spool/observability/     观测发布失败时的本地 spool（节点本地）
```

Runtime 默认开启观测（flush 模式）：应用日志与 Runtime 遥测默认进入
Parquet 数据集；如需保留本地 JSONL 文件日志，可在 `config/runtime.toml`
中设置 `observability.enabled = false`。

Runtime 制品本身不包含应用代码。应用在 `config/runtime.toml` 的 `[[apps]]`
中声明，一个 Runtime 可以同时托管多个应用：单一监听、单一 host，应用按
URL 子目录前缀区分，最多一个应用使用 `/`。每个应用使用独立数据库和独立的
附件/Session 数据根；新增或替换应用后重启 `zentao` 生效。

配置文件位于 Runtime 根目录的 `config/runtime.toml`，且为必需文件：缺失时
启动会报错并提示创建方式。标准安装示例分别为 Linux
`/opt/zentao/config/runtime.toml` 和 Windows
`C:\Program Files\ZenTao\config\runtime.toml`。

## 子命令

运行 `zentao` 不带参数会显示命令概览。Runtime 会从自身根目录的 `config/runtime.toml` 读取配置，不能通过命令行更改配置文件位置。控制类命令默认使用配置中的控制 socket；可以用 `--control-socket` 覆盖。

### 服务与生命周期

```text
serve [flags]       启动 Runtime（主服务入口）
start [flags]       serve 的别名
status [flags]      查看生命周期、Web 和组件状态
stop [flags]        请求优雅停止
reload [flags]      重新加载可热更新配置
health [flags]      查看健康状态
diagnose [flags]    查看深度健康信息、版本和配置摘要
version             输出版本和构建能力
```

常用选项：

```text
serve:
  --listen            覆盖 Web 监听地址
  --pid-file          覆盖 PID 文件
  --control-socket    覆盖控制 socket/Named Pipe
  --threads           覆盖 PHP 线程数
  --install-root      应用、数据和日志目录根路径

status/stop/reload/health/diagnose:
  --control-socket    控制 socket（Windows 为 Named Pipe）
  --pid-file          控制端不可用时的 status/stop PID 文件回退路径
health/diagnose:
  --deep              执行共享存储、观测等深度检查
```

健康 HTTP 端点：

```text
/_runtime/healthz       综合健康状态
/_runtime/liveness      进程存活状态
/_runtime/readiness     是否可以接收业务流量
```

### 升级

应用升级由部署人员自行备份并替换对应应用目录（如
`/opt/zentao/apps/zentao-ipd-22.6`）；Runtime 不维护应用版本目录、指针或
自动回滚。替换完成后重启 `zentao`，再检查 `zentao health --deep` 和
readiness 端点。

### 日志、指标和诊断

```bash
zentao logs --since 30m
zentao logs --since 2h --level error --limit 200
zentao metrics --since 1h --metric-name http.requests
zentao flush-observability
zentao clean-observability
zentao convert-jsonl
zentao collect-logs
```

查询选项：`--since` 使用 Go duration，`--until` 使用 RFC3339 时间，另有 `--node`、`--level`、`--metric-name`、`--limit` 和 `--control-socket`。`collect-logs` 返回诊断压缩包路径；诊断包只包含本地日志和摘要，不包含业务数据库内容、请求正文或凭据。

### PHP CLI 和 Windows 服务

```bash
zentao php-cli -- -r 'echo PHP_VERSION, PHP_EOL;'
```

`php-cli` 使用与 Web 相同的 PHP、扩展和 ionCube 配置，也可以指定 `--php /path/to/php`。

Windows 服务入口：

```powershell
zentao.exe service install
```

`service install` 和 `service uninstall` 分别注册和注销 Linux systemd 服务或 Windows 系统服务；`run-service` 仅作为 Windows 服务的内部启动入口。

## 配置和安全

详细的配置初始化、Docker 持久化和安全要求见
[《配置与安全》](./docs/configuration-and-security.md)。

## 编译与打包

源码编译、Docker 镜像、Runtime/Full 安装包以及集成环境的完整说明见
[`docs/build-and-package.md`](./docs/build-and-package.md)。

## 文档入口

- 使用与开发：[Runtime 使用开发指南](./docs/usage/README.md)
- 产品/部署设计：[docs/design/](./docs/design/)
- 开发与验证：[docs/development/](./docs/development/)
- 配置与安全：[docs/configuration-and-security.md](./docs/configuration-and-security.md)
- 多节点部署：[docs/deployment/multi-node.md](./docs/deployment/multi-node.md)
- 编译与打包：[docs/build-and-package.md](./docs/build-and-package.md)
- 文档索引：[docs/README.md](./docs/README.md)

README 只描述产品使用和交付方式；设计决策、协议细节、开发任务和发布门槛请查看上述文档。
