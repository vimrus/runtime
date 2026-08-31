# 配置与安全

## 配置文件

`config/runtime.example.toml` 是带注释的模板，Runtime 不会自动读取。复制后命名为 `runtime.toml`；应用、日志和观测目录等相对路径会按安装根目录解析，无需把安装前缀写入每一项。系统级 socket 和 PID 路径仍按平台约定使用绝对路径。

Linux 在 Runtime 根目录下创建 `config/runtime.toml`：

```bash
sudo install -m 0640 config/runtime.example.toml config/runtime.toml
sudo editor config/runtime.toml
```

Windows 也在 Runtime 根目录下创建 `config\runtime.toml`；`C:\Program Files\ZenTao` 只是默认安装示例。

配置格式为严格 TOML；未知字段或不支持的 `schemaVersion` 会阻止启动。标准目录按约定解析；需要使用系统级目录时，也可以填写绝对路径。

## Docker Compose

在仓库根目录创建配置：

```bash
cp config/runtime.example.toml config/runtime.toml
$EDITOR config/runtime.toml
docker compose -f packaging/docker/compose.yaml up -d web
```

Compose 将宿主机配置只读挂载到容器 `/opt/zentao/config/runtime.toml`。数据库连接通过 `ZT_DB_*` 环境变量配置。

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
- Runtime 日志和审计日志会对密码、token、Cookie、Authorization 等敏感字段脱敏。
- 多节点部署需要部署方提供共享 Session/附件存储、数据库高可用和负载均衡；Runtime 不替代这些基础设施。
- 不要将实际 `runtime.toml`、生产密码或 Bridge token 提交到仓库。
