# 配置与安全

## 配置文件

Runtime 固定读取安装根目录下的 `config/runtime.toml`，**该文件是必需的**：
缺失时 `serve` 会直接报错并提示创建方式，不会静默使用内置默认值。
`config/runtime.example.toml` 是带注释的模板。应用、日志和观测目录等相对
路径会按实际 Runtime 根目录自动解析。

首次使用在 Runtime 根目录执行：

```bash
sudo install -m 0640 config/runtime.example.toml config/runtime.toml
```

Windows 也在 Runtime 根目录下创建 `config\runtime.toml`；`C:\Program Files\ZenTao` 只是默认安装示例。

配置格式为严格 TOML；未知字段或不支持的 `schemaVersion` 会阻止启动。标准目录按约定解析；需要使用系统级目录时，也可以填写绝对路径。

配置文件内的可选字段（监听地址、线程数、超时、队列与观测参数等）在省略时
使用按安装根解析的内置默认值；必填的只有配置文件本身，以及 `[[apps]]` 中
每个应用的 `id`、`path`、`dir`。

## Docker Compose

Compose 模板要求先准备宿主机的 `packaging/docker/config/runtime.toml`
（该目录以只读方式挂载到容器 `/opt/zentao/config`）；缺失时容器启动会报
配置文件不存在的错误：

```bash
mkdir -p packaging/docker/config
cp config/runtime.example.toml packaging/docker/config/runtime.toml
docker compose -f packaging/docker/compose.yaml up -d web
```

数据库连接通过 `ZT_DB_*` 环境变量配置，应用目录放在 `apps/` 卷下。

以下数据使用命名卷持久化，删除容器不会丢失：

- `zentao-app`：禅道应用文件
- `zentao-data`：Runtime 数据
- `zentao-logs`：Runtime、访问和 PHP 日志
- `zentao-observability`：Parquet 观测数据
- `zentao-spool`：观测故障时的本地 spool
- `mysql-data`：Full 模式的 MySQL 数据

配置文件是宿主机绑定挂载，需与命名卷一起备份。

## 安全要求

- 控制 socket/Named Pipe 是管理入口，不应暴露到公网；Linux 默认使用 `0600` 和同用户鉴权。
- Queue Bridge 仅允许本机回环地址，并使用每次 Runtime 启动生成的随机 token。
- Process Bridge 仅允许本机回环地址；Web 只获得应用级 call Token，常驻 PHP
  CLI 只获得本实例的 worker Token。Process RPC 不持久化，也不自动重试。
- Runtime 日志和审计日志会对密码、token、Cookie、Authorization 等敏感字段脱敏。
- 多节点部署需要部署方提供共享 Session/附件存储、数据库高可用和负载均衡；Runtime 不替代这些基础设施。
- 不要将实际 `runtime.toml`、生产密码或 Bridge token 提交到仓库。
