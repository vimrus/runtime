# ZenTao Runtime

ZenTao Runtime（可执行文件：`zentao`）是禅道的统一运行环境。它把 Web 服务、PHP 8.4、进程生命周期、健康检查、日志、诊断、升级和可选的队列/观测能力组合成一个可交付产品，支持单机部署，也支持 Linux 双节点接入现有负载均衡。

## 产品形态

| 形态 | 内容 | 适用场景 |
| --- | --- | --- |
| Runtime | Runtime、PHP、FrankenPHP/Caddy 和应用目录 | 已有 MySQL/PostgreSQL 等数据库的单机或集群 |
| Full | Runtime + MySQL 8.4 | 快速交付的单机环境；生产环境仍应规划数据库备份 |
| Docker | Web 容器，可选 MySQL Compose profile | Docker 或已有数据库的容器部署 |

Runtime 不直接访问禅道业务数据库。数据库连接由 PHP 应用和禅道 DAO 负责；启用队列或观测功能时，相关能力通过 Runtime 提供的本机服务使用。

## 快速开始

### 已有 Runtime 安装目录

准备应用发布目录和配置后启动：

```bash
zentao serve --config /opt/zentao/config/runtime.json
```

常见运行目录：

```text
/opt/zentao/
  bin/zentao               Runtime 程序
  app/current/www          当前禅道应用
  app/releases/            应用版本
  config/runtime.json      Runtime 配置
  data/                    附件、节点标识等持久化数据
  logs/                    Runtime、访问和 PHP 日志
  observability/           Parquet 数据集
  spool/observability/     观测故障时的本地 spool
```

Linux 可以使用 systemd 安装：

```bash
sudo packaging/linux/install.sh --prefix /opt/zentao
sudo systemctl status zentao-runtime
```

安装脚本默认创建 `zentao` 系统用户。配置样例见 [`config/runtime.example.json`](./config/runtime.example.json)。

### Docker Compose

仅启动 Web（连接外部数据库）：

```bash
docker compose -f packaging/docker/compose.yaml up -d web
```

启动 Web 和 Compose 内的 MySQL：

```bash
docker compose -f packaging/docker/compose.yaml --profile full up -d
```

使用外部数据库时，通过 `ZT_DB_DRIVER`、`ZT_DB_HOST`、`ZT_DB_PORT`、`ZT_DB_NAME`、`ZT_DB_USER`、`ZT_DB_PASSWORD` 和 `ZT_DB_PREFIX` 配置 PHP 应用。不要把生产密码提交到 Compose 文件或仓库。

### 集成环境

有应用包和 Runtime 制品时，可以生成一个自包含目录：

```bash
scripts/package/build-integrated-env.sh open /tmp/zentao-open
/tmp/zentao-open/run.sh
```

版本参数支持 `open`、`biz`、`max` 和 `ipd`。应用包默认从 `app-packages/` 查找，也可以通过 `ZENTAO_APP_PACKAGES_DIR` 指定目录。

## 子命令

运行 `zentao` 不带参数会显示命令概览。控制类命令默认使用配置中的控制 socket；可以用 `--control-socket` 覆盖。

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
  --config            Runtime 配置文件
  --root              覆盖 PHP 文档根目录
  --listen            覆盖 Web 监听地址
  --pid-file          覆盖 PID 文件
  --control-socket    覆盖控制 socket/Named Pipe
  --threads           覆盖 PHP 线程数
  --install-root      应用、数据和升级目录根路径

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

升级使用应用版本目录和 `app/current` 指针，不覆盖 `config/` 和 `data/`：

```bash
zentao upgrade --action status
zentao upgrade --action prepare --release /opt/zentao/app/releases/2026.08.24
zentao upgrade --action apply
zentao upgrade --action rollback
```

`prepare` 的 `--release` 必须是 `AppsDir` 下包含 `www/` 的绝对路径。升级前请保留备份目录，并在应用层完成登录、静态资源和关键业务验证。

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
zentao.exe run-service --name ZenTaoRuntime --config 'C:\Program Files\ZenTao\config\runtime.json'
```

## 配置和安全

- 使用 [`config/runtime.example.json`](./config/runtime.example.json) 生成 `runtime.json`，并按实际安装路径修改绝对路径。
- 控制 socket/Named Pipe 是管理入口，不应暴露到公网；Linux 默认使用 `0600` 和同用户鉴权。
- 队列 Bridge 仅允许本机回环地址，并使用每次 Runtime 启动生成的随机 token。
- Runtime 日志和审计日志会对密码、token、Cookie、Authorization 等敏感字段脱敏。
- 多节点部署需要部署方提供共享 Session/附件存储、数据库高可用和负载均衡；Runtime 不替代这些基础设施。

## 从源码编译

正式构建会固定 PHP、FrankenPHP、Caddy、DuckDB、MySQL 和 ionCube 版本，并生成 manifest、SBOM、校验和和大小报告。构建脚本需要 Docker/BuildKit、Git、`jq`；Linux 构建还需要能够运行 Docker 的环境。

### Linux amd64 / arm64

推荐在目标架构的 Linux Runner 上执行：

```bash
scripts/native/build-linux.sh amd64
scripts/native/build-linux.sh arm64
```

产物位于 `dist/runtime-linux-amd64/` 或 `dist/runtime-linux-arm64/`。构建脚本会使用
`packaging/runtime/Dockerfile` 生成 Runtime 制品并执行制品校验。

如需覆盖 Go 代理或 Debian 镜像源：

```bash
GOPROXY=https://goproxy.cn,direct \
DEBIAN_MIRROR=http://deb.debian.org/debian \
scripts/native/build-linux.sh amd64
```

### Windows x86_64

在 Windows 原生环境执行 PowerShell，要求 Go、Visual Studio LLVM/Clang、Windows PHP 8.4 开发包、vcpkg 和网络下载依赖可用：

```powershell
Set-Location C:\path\to\runtime
.\scripts\windows\build.ps1
```

默认输出 `dist/runtime-windows-x64/`。脚本会校验锁定归档，编译 MSVC/COFF DuckDB，构建 `runtime\zentao.exe`，复制 PHP/扩展/ionCube，并执行 PHP 和 Parquet 冒烟验证。仅在已有 DuckDB 缓存时才使用：

```powershell
.\scripts\windows\build.ps1 -SkipDuckDB
```

### Docker 镜像和 Compose

直接校验 Compose 配置：

```bash
docker compose -f packaging/docker/compose.yaml config
docker compose -f packaging/docker/compose.yaml --profile full config
```

构建本地 Web 镜像（脚本会从 `versions.lock.json` 读取全部构建参数）：

```bash
RUNTIME_ARCH=amd64 bash -c 'source scripts/build/common.sh; build_runtime_image'
# 生成 zentao:runtime-linux-amd64
```

Docker 交付以 Linux amd64/arm64 原生 Runner 为基线；正式发布还应执行包校验、SBOM、签名和 Compose 冒烟测试。

macOS 不是 Runtime 的正式交付目标；在 macOS 上请使用 Docker/BuildKit 构建 Linux 制品，或在 Linux 原生 Runner 上构建和验证。

### Runtime / Full 安装包

Runtime staging 目录准备完成后：

```bash
scripts/package/assemble-runtime.sh <staging-dir> zentao-runtime-linux-amd64 dist/packages
scripts/package/assemble-full.sh <staging-dir> linux-amd64 zentao-full-linux-amd64 dist/packages
```

支持的 Full 平台参数为 `linux-amd64`、`linux-arm64` 和 `windows-x64`。脚本会从 `versions.lock.json` 下载并校验 MySQL 归档。

## 文档入口

- 产品/部署设计：[docs/design/](./docs/design/)
- 开发与验证：[docs/development/](./docs/development/)
- 文档索引：[docs/README.md](./docs/README.md)

README 只描述产品使用和交付方式；设计决策、协议细节、开发任务和发布门槛请查看上述文档。
