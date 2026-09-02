# 配置与安全

## 配置文件

Runtime 内置系统默认配置。`config/runtime.example.toml` 是带注释的可选覆盖模板；只有需要修改监听地址、线程数、队列或观测选项时，才复制为同目录的 `config/runtime.toml`。未提供 `runtime.toml` 时直接使用内置默认值。应用、日志和观测目录等相对路径会按实际 Runtime 根目录自动解析。

需要覆盖默认值时，在 Runtime 根目录执行：

```bash
sudo install -m 0640 config/runtime.example.toml config/runtime.toml
```

Windows 也在 Runtime 根目录下创建 `config\runtime.toml`；`C:\Program Files\ZenTao` 只是默认安装示例。

配置格式为严格 TOML；未知字段或不支持的 `schemaVersion` 会阻止启动。标准目录按约定解析；需要使用系统级目录时，也可以填写绝对路径。

## Docker Compose

未提供配置文件时直接使用镜像内置默认值：

```bash
docker compose -f packaging/docker/compose.yaml up -d web
```

默认配置即可启动，Web 默认监听 `0.0.0.0:8080`（所有 IPv4 网卡）。只有需要自定义监听地址、线程数、队列或观测选项时，才编辑 `runtime.toml`。

需要覆盖默认值时，将宿主机的 `runtime.toml` 只读挂载到容器 `/opt/zentao/config/runtime.toml`。数据库连接通过 `ZT_DB_*` 环境变量配置。

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
