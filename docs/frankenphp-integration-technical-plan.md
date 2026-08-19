# 禅道 FrankenPHP 集成环境技术方案

## 1. 文档信息

| 项目 | 内容 |
|---|---|
| 状态 | 已实施（Linux amd64/arm64 与 Windows x64 构建通过） |
| 日期 | 2026-08-18 |
| PHP 基线 | PHP 8.4 ZTS |
| Web Runtime | 自研 Go Host + Caddy Library + FrankenPHP Library（Classic mode） |
| 数据库 | MySQL，兼容外部 PostgreSQL |
| 目标平台 | Windows、Linux、Docker |

## 2. 背景与目标

禅道当前使用 PHP 开发，需要建设一套自研 Go Runtime Host。Host 将 Caddy 和 FrankenPHP 作为 Library 链接到同一进程，替代或演进现有的一键安装环境。

本方案需要满足以下目标：

1. 提供 Windows、Linux 和 Docker 三类运行环境。
2. Windows 和 Linux 分别提供 Runtime 版与内置 MySQL 的 Full 版。
3. 支持禅道开源版、企业版、旗舰版和 IPD 版。
4. 支持 ionCube 加密的付费版 PHP 代码。
5. Caddy 和 FrankenPHP 均作为 Go Library 使用；FrankenPHP 使用 Classic mode，不启用 worker mode，PHP 请求状态不跨请求保留。
6. PHP 版本固定为 8.4，并支持 MySQL 和外部 PostgreSQL。
7. 运行时具备可重复构建、升级、备份、诊断和供应链追踪能力。
8. DuckDB 作为 Go Library 生成和查询指标/日志 Parquet，双节点通过 NFS 共享分区式数据集。

## 3. 核心结论

方案可行，但 Linux Runtime 不能直接使用未修改的 PHP 8.4 ZTS 解释器。

FrankenPHP 要求 PHP 使用 ZTS，并推荐以如下参数编译：

```text
--enable-embed
--enable-zts
--disable-zend-signals
--enable-zend-max-execution-timers
```

`--disable-zend-signals` 可以避免 PHP 的 Zend Signals 与 Go runtime 的信号处理发生冲突，但 ionCube Linux x86_64 PHP 8.4 TS Loader 会引用被移除的三个符号：

```text
zend_signal_globals_id
zend_signal_globals_offset
zend_signal_handler_unblock
```

解决方式是在保持 `ZEND_SIGNALS` 关闭的前提下，为 PHP 增加一个最小 ABI 兼容层：

- `zend_signal_globals_id` 始终为 `0`。
- `zend_signal_globals_offset` 始终为 `0`。
- `zend_signal_handler_unblock()` 为空函数。
- 不注册、不接管、不转发任何操作系统信号。
- 不恢复 Zend Signals 的结构、队列和请求生命周期逻辑。

ionCube Loader 在访问 TSRM offset 前会判断 `zend_signal_globals_id != 0`。保持该值为 `0` 后，Loader 中的信号中断保护逻辑会整体短路，不会读取无效的 TSRM 内存。

## 4. 交付物矩阵

计划提供五类交付物：

| 交付物 | Runtime | 数据库 | 初始架构 |
|---|---|---|---|
| Windows Runtime | `zentao-runtime` + Caddy + FrankenPHP + DuckDB + PHP 8.4 TS | 外部 MySQL/PostgreSQL | x86_64 |
| Windows Full | 同上 | 内置 MySQL 8.4 LTS | x86_64 |
| Linux Runtime | `zentao-runtime` + Caddy + FrankenPHP + DuckDB + patched PHP 8.4 ZTS | 外部 MySQL/PostgreSQL | amd64、arm64 |
| Linux Full | 同上 | 内置 MySQL 8.4 LTS | amd64、arm64 |
| Docker | Debian + `zentao-runtime` + DuckDB + patched PHP 8.4 ZTS | 独立 MySQL 容器或外部数据库 | amd64、arm64 |

Docker 中 FrankenPHP 与 MySQL 必须使用不同容器。默认 Compose 可以启动 MySQL，也应允许禁用 MySQL 服务并连接外部 MySQL 或 PostgreSQL。

## 5. 总体架构

```text
                       zentao-runtime Go Host
                              |
             +----------------+----------------+
             |                                 |
       Caddy Library                  lifecycle/control plane
             |
   FrankenPHP Caddy Module/Library
             |
      PHP 8.4 ZTS Classic mode
             |
         禅道应用包
                              |
           +------------------+------------------+
           |                                     |
      MySQL 8.4 LTS                         外部数据库
   Full 版或 Compose                 MySQL / PostgreSQL

  DuckDB Library
    -> 指标/日志批处理
    -> 本地或 NFS 共享 Parquet 数据集

独立后台进程：
  - 禅道计划任务服务
  - MySQL 服务（仅 Full 版或 Compose）
```

Classic mode 表示每次 HTTP 请求都重新初始化和释放禅道 PHP 请求上下文。`zentao-runtime`、Caddy、FrankenPHP PHP main thread 和 ZTS thread pool 仍然常驻，但不会像 worker mode 一样复用禅道应用对象和请求级全局状态。

Runtime Host 的详细架构参见 [Caddy + FrankenPHP Library Runtime Host 详细设计](./runtime-host-library-design.md)。

## 6. PHP ionCube ABI 补丁

### 6.1 最小补丁

建议仅修改 PHP 8.4 的 `Zend/zend_signal.c`，将文件末尾原有的
`#endif /* ZEND_SIGNALS */` 替换为以下内容：

```c
#else /* !ZEND_SIGNALS */

#if defined(ZTS) && !defined(PHP_WIN32)

ZEND_API int zend_signal_globals_id = 0;
ZEND_API size_t zend_signal_globals_offset = 0;

ZEND_API void zend_signal_handler_unblock(void);

ZEND_API void zend_signal_handler_unblock(void)
{
}

#endif
#endif /* ZEND_SIGNALS */
```

### 6.2 不采用完整空实现的原因

不建议在 `zend_signal.h` 中恢复完整的结构、宏和函数声明，也不建议提供下列空实现：

```text
zend_signal_startup
zend_signal_activate
zend_signal_deactivate
zend_signal_handler
zend_signal_globals_ctor
zend_signal_globals_dtor
zend_signal_handler_defer
zend_sigaction
zend_signal
zend_signal_init
```

原因如下：

1. 当前 ionCube PHP 8.4 TS Loader 没有引用这些符号。
2. PHP 8.4 中部分函数的签名和可见性已经变化，例如 `zend_signal_startup()` 返回 `void`。
3. 完整复制 `siginfo_t`、`struct sigaction`、队列和 globals 结构会增加 Windows、Linux libc 和 PHP 小版本之间的兼容风险。
4. 最小 ABI shim 不会创建假的 TSRM 槽，也不会让其他代码误以为 Zend Signals 已启用。

### 6.3 平台差异

截至 2026-08-17，对当前 ionCube PHP 8.4 Loader 的检查结果如下：

| 平台 Loader | `zend_signal_*` 未定义符号 |
|---|---|
| Linux x86_64 TS | 上述三个符号 |
| Linux ARM64 TS | 当前未发现 |
| Windows x86_64 TS | 当前未发现，Loader 依赖 `php8ts.dll` |

为了保持 Linux Runtime 一致，Linux amd64 和 arm64 可以使用同一份 PHP 补丁。Windows 不应用该补丁。

每次升级 PHP 或 ionCube Loader 时，必须重新检查 Loader 的未定义符号。如果出现新的 `zend_signal_*` 依赖，构建流水线应直接失败，不允许自动增加未经评审的空实现。

## 7. Runtime 构建方案

### 7.1 Linux

使用 FrankenPHP 官方推荐的动态 `libphp.so` 编译方式，便于自研 Go Host 通过 CGO 链接 PHP，并加载 ionCube 等动态扩展：

```text
下载固定版本 PHP 8.4 源码
  -> 校验源码 SHA256
  -> 应用 ionCube ABI patch
  -> 运行 configure
  -> 编译并安装 libphp.so 和 PHP 扩展
  -> 以 Go modules 链接固定版本 Caddy 和 FrankenPHP
  -> 编译 zentao-runtime Host
  -> 安装 ionCube TS Loader
  -> 生成 Runtime manifest 和 SBOM
  -> 执行兼容性测试
  -> 签名并发布
```

不使用 Alpine/musl 作为正式运行环境。Linux Runtime 和 Docker 统一以 glibc/Debian 为构建与兼容基线。

### 7.2 Windows

Windows 使用 PHP 8.4 TS、Caddy/FrankenPHP Go modules、自研 Runtime Host 和 ionCube Windows x86_64 Loader。

Windows 构建必须固定以下内容：

- PHP 8.4 patch 版本和 PHP API 编号。
- MSVC/Clang 工具链版本。
- `php8ts.dll`、PHP 扩展和 ionCube Loader 的架构。
- Visual C++ Runtime 版本。
- Caddy、FrankenPHP 和 Runtime Host 的 module/commit。

Windows 首版只支持 x86_64，不提供 32 位运行环境。

### 7.3 Docker

Docker Runtime 使用 Debian 基础镜像和多阶段构建：

1. Builder 阶段编译 patched PHP、扩展和 `zentao-runtime` Host。
2. Runtime 阶段只复制运行所需共享库、扩展、配置和禅道应用。
3. Web 容器只运行 `zentao-runtime`，由其在进程内启动 Caddy 和 FrankenPHP。
4. Scheduler 使用相同应用镜像、不同启动命令。
5. MySQL 使用独立的官方固定版本镜像。

镜像禁止使用浮动 `latest` tag 作为发布依赖，基础镜像应固定 digest。

## 8. PHP 扩展与配置

基础扩展建议包括：

```text
ionCube Loader
opcache
pdo_mysql
pdo_pgsql
mysqli
redis
curl
gd
mbstring
openssl
zip
intl
iconv
zlib
filter
fileinfo
ldap
bcmath
sockets
```

最终扩展矩阵需要覆盖开源版、企业版、旗舰版、IPD 版和公共付费插件。

ionCube 必须先于其他 Zend 扩展加载，例如：

```ini
zend_extension=/opt/zentao/runtime/extensions/ioncube_loader_lin_8.4_ts.so
zend_extension=opcache
```

Web 和 CLI 必须使用同一套主 `php.ini` 与扩展扫描目录，避免 Web 可以解密而计划任务无法执行付费代码。

建议的基础 PHP 参数：

```ini
memory_limit=512M
upload_max_filesize=100M
post_max_size=110M
max_execution_time=120
max_input_vars=10000
expose_php=Off
display_errors=Off
log_errors=On
session.use_strict_mode=1
session.lazy_write=1
opcache.enable=1
```

具体数值应在性能和大附件测试后确定，并允许管理员通过独立覆盖文件修改。

## 9. Caddy + FrankenPHP Library 配置

Runtime Host 使用结构化配置生成并加载 Caddy JSON。配置语义示例：

```yaml
web:
  listen: ":80"
  document_root: /opt/zentao/app/current/www
  auto_https: false
  compression: [zstd, gzip]
php:
  mode: classic
  worker: false
```

Host 将上述配置转换为包含 FrankenPHP Caddy `php_server` route 的 Caddy JSON。正式配置还需要处理：

- 禅道 PATH_INFO 路由。
- 静态资源和附件下载。
- 上传体积限制。
- 代理客户端 IP 与可信代理。
- HTTP 安全响应头。
- 访问日志轮转。
- HTTP/HTTPS 切换。
- 禁止访问隐藏文件、备份文件和内部目录。
- Caddy module allowlist。
- 本地 control plane 与公共 Web listener 隔离。

不得配置 FrankenPHP worker，也不得将禅道路由入口注册为常驻 worker 脚本。Caddy module 负责 FrankenPHP Init/Shutdown 时，Host 不得重复直接调用相同 API。

## 10. 目录与持久化设计

建议将 Runtime、应用代码和持久数据分离：

```text
runtime/                 zentao-runtime、PHP、扩展和共享库
app/releases/<version>/  各版本禅道应用
app/current              当前版本链接或版本指针
config/                  Runtime 配置、Caddy JSON 基线、php.ini
data/                    附件及其他持久数据
observability/           本地或 NFS 指标/日志 Parquet 数据集
spool/observability/     NFS 故障期间的有界本地批次暂存，不能放在共享 NFS
mysql/                   Full 版 MySQL 程序与数据
logs/                    Web、PHP、Scheduler、MySQL 日志
backups/                 数据库、附件和配置备份
licenses/                第三方许可证
manifest.json            构建和组件版本信息
```

Windows 可以使用目录指针文件代替符号链接，避免普通用户权限不足。

应用升级不得覆盖持久数据目录、用户配置和数据库目录。

## 11. 服务管理

### 11.1 Linux

使用 systemd 管理：

```text
zentao-web.service
zentao-scheduler.service
zentao-mysql.service       # 仅 Full 版
```

同时提供统一的 `zentaoctl`：

```text
install
start
stop
restart
status
logs
backup
restore
diagnose
uninstall
```

### 11.2 Windows

使用 WinSW 或等价的 Windows Service Wrapper 管理：

```text
ZenTaoWeb
ZenTaoScheduler
ZenTaoMySQL               # 仅 Full 版
```

提供 PowerShell 管理入口，并正确处理带空格和非 ASCII 字符的安装路径。

### 11.3 Scheduler

Classic mode 只约束 HTTP 请求模型。禅道计划任务程序仍需要独立运行：

- Windows/Linux 中注册为独立服务。
- Docker 中使用相同应用镜像启动独立 Scheduler 容器。
- Scheduler 异常不得导致 Web 服务退出。
- Web 和 Scheduler 使用相同的 PHP Runtime、ionCube Loader 和应用版本。

## 12. 数据库方案

### 12.1 Full 版 MySQL

建议使用 MySQL 8.4 LTS：

- 默认只监听 `127.0.0.1` 或本地 socket。
- 首次启动生成随机 root 密码和禅道专用数据库账号。
- 禅道账号仅拥有目标数据库所需权限。
- MySQL 数据目录与程序目录分离。
- 禁止发布包内置固定默认密码。
- 升级前自动备份数据库、附件和配置。

### 12.2 PostgreSQL

不提供内置 PostgreSQL 包，但所有 Runtime 必须包含 `pdo_pgsql`，并支持连接外部 PostgreSQL。

CI 必须分别执行 MySQL 和 PostgreSQL 的以下测试：

- 全新安装。
- 版本升级。
- 主要业务模块回归。
- 数据库函数和 SQL 兼容性测试。
- 备份与恢复。

### 12.3 多节点部署

标准多节点参考拓扑为两个 Linux 应用节点，共享数据库和附件 NFS：

- Session 遵循 PHP `php.ini`；单机默认使用本地文件，多节点由部署方配置共享 NFS 文件或外部 Redis。
- Runtime 不保存 Session，只负责配置一致性检查和通过 PHP 探针诊断实际 Handler；NFS/Redis 的高可用由部署方负责。
- 文件 Session 必须尊重显式 `session.save_path`，API `apisession` 也不能在多节点下回退本地目录。
- 附件由所有应用节点挂载同一个 NFSv4.1 文件系统。
- DuckDB 不共享 `.duckdb` 文件；两个节点在 NFS 上共享指标/日志 Parquet 数据集，并只向各自的节点分区发布不可变 part 文件。
- 节点 Runtime 配置分别维护，通过 Cluster ID、Release ID、Schema 代数和配置指纹发现漂移。
- 客户已有云、硬件或托管负载均衡时优先接入。
- 只有两个 Linux 节点且没有外部负载均衡时，使用 Keepalived 浮动 VIP 和内嵌 Caddy Gateway。
- 使用共享 NFS 或 Redis Session 时，Caddy Gateway 使用 `least_conn`，不依赖 Session Sticky，也不自动重试非幂等请求。
- 数据库、NFS 和外部 Redis 高可用由客户基础设施负责。

详细设计参见 [zentao 多节点部署与高可用详细设计](./deployment-and-ha-design.md)。

### 12.4 DuckDB 与 Parquet 可观测性

- DuckDB 作为 Library 链接到 `zentao-runtime`，不启动独立数据库服务。
- 单机使用本地 Parquet 数据集；Linux/Docker 多节点使用 NFSv4.1 共享 Parquet 数据集。
- 每个节点只写 `node=<nodeID>` 分区，通过临时文件、校验和同文件系统原子 rename 发布。
- NFS 不可用时写有界本地 spool，可观测性故障不阻断业务请求。
- 查询必须按时间和节点裁剪，并限制线程、内存、临时空间、返回行数和超时。

详细设计参见 [DuckDB 与共享 Parquet 可观测性详细设计](./duckdb-parquet-observability-design.md)。

## 13. 安全要求

1. Runtime Host、Web 和数据库服务使用低权限账号运行。
2. 数据库默认不向外部网卡开放。
3. 安装时生成随机凭据，不提供公开默认密码。
4. 禁止通过 Web 访问 `config`、`tmp`、`logs` 和备份目录。
5. 默认关闭 `phpinfo`、调试模式和 PHP 版本响应头。
6. 发布包、镜像和升级包提供 SHA256 与数字签名。
7. 生成 SBOM，记录全部 Runtime 组件及许可证。
8. 对 Runtime Host、Caddy、FrankenPHP、DuckDB/Go Binding、PHP、ionCube、MySQL 和基础镜像建立漏洞跟踪机制。
9. Full 版 MySQL、ionCube Loader、DuckDB/Go Binding 和相关 C/C++ Runtime 的许可证及再分发条款必须完成确认。
10. DuckDB 禁止运行时自动安装或加载未审核 Extension，不向公共 Web 暴露任意 SQL 和文件路径。

## 14. 升级与回滚

升级流程建议如下：

```text
预检查
  -> 停止 Scheduler
  -> 进入维护模式
  -> 备份数据库、附件和配置
  -> 安装新 Runtime 和应用到新版本目录
  -> 执行禅道升级程序
  -> 运行健康检查
  -> 切换 current 版本
  -> 启动 Scheduler
  -> 退出维护模式
```

应用文件可以通过切换版本目录回滚。数据库发生迁移后，不能只回滚应用文件；必须恢复升级前数据库备份。

首版需要明确支持的迁移来源：

- 现有 Windows XAMPP 一键包。
- 现有 Linux ZBox/一键包。
- 现有官方 Docker 环境。
- 源码安装环境。

## 15. 构建与发布流水线

建议构建矩阵：

```text
Windows: x86_64
Linux:   amd64, arm64
Docker:  amd64, arm64
DB:      MySQL, PostgreSQL
Edition: 开源版, 企业版, 旗舰版, IPD版
```

每个 Runtime manifest 至少记录：

```text
PHP 源码版本、PHP API 和 SHA256
Caddy/FrankenPHP Go module 版本、commit 和校验
Runtime Host version/commit
ionCube Loader 版本和 SHA256
PHP ABI patch SHA256
configure 参数
编译器和链接器版本
PHP 扩展版本
MySQL 版本（Full 版）
基础镜像 digest（Docker）
禅道版本和版本类型
```

## 16. ionCube 专项发布门槛

### 16.1 静态检查

Linux 构建后执行：

```bash
nm -D libphp.so | grep zend_signal
nm -D --undefined-only ioncube_loader_lin_8.4_ts.so
readelf -Ws libphp.so | grep zend_signal
```

要求：

- `libphp.so` 导出三个兼容符号。
- ionCube Loader 不存在其他未满足的 `zend_signal_*` 依赖。
- Runtime 仍报告 Zend Signal Handling 为 disabled。
- `zend_signal_globals_id` 在进程生命周期内保持为 `0`。

### 16.2 功能与稳定性检查

1. `extension_loaded('ionCube Loader')` 返回成功。
2. Web 和 CLI 均可执行加密代码。
3. 企业版、旗舰版、IPD 版完成核心流程冒烟测试。
4. OPcache 开启后加密文件正常执行。
5. Classic mode 并发请求无崩溃、串数据和持续内存增长。
6. `max_execution_time` 能通过 FrankenPHP execution timer 生效。
7. `SIGTERM` 和 `SIGINT` 不被 PHP shim 接管，Runtime Host 可以优雅停止 Caddy、FrankenPHP 和子服务。
8. 进行至少 24 小时稳定性测试。

## 17. 通用验收范围

除 ionCube 专项测试外，还需要覆盖：

- 禅道全新安装与登录。
- PATH_INFO 和 APIv2 路由。
- 文件上传、附件预览和大文件下载。
- 数据导入导出。
- 邮件、LDAP、Git/SVN 等集成功能。
- 计划任务和后台队列。
- MySQL/PostgreSQL 双数据库。
- HTTP、反向代理和 HTTPS。
- Windows 服务安装、卸载和异常恢复。
- Linux systemd 启停、重启和开机启动。
- Docker 健康检查、卷持久化和滚动升级。
- 从现有一键包和 Docker 版本升级。

## 18. 风险与对策

| 风险 | 影响 | 对策 |
|---|---|---|
| ionCube 新版本增加信号符号依赖 | Loader 无法加载或发生运行时错误 | CI 检查未定义符号，Loader 升级必须重新评审 |
| PHP 8.4 小版本修改 Zend ABI | 补丁失败或扩展不兼容 | 固定版本，patch apply check，完整回归后升级 |
| 用户单独替换 PHP/Loader | 破坏已验证的 Runtime 组合 | Runtime 整体升级，启动时校验组件版本和哈希 |
| Zend Signals shim 被误认为已启用 | 第三方扩展访问无效 TSRM 数据 | `id` 固定为 0，不恢复头文件宏和 globals 结构 |
| Linux 发行版共享库差异 | Runtime 无法启动 | 统一 glibc 基线，检查依赖，提供支持矩阵 |
| Windows Defender 误报 | 安装包被拦截 | 代码签名、稳定下载地址、发布前多引擎扫描 |
| 内置 MySQL 数据损坏或升级失败 | 用户数据不可用 | 独立数据目录、升级前备份、恢复演练 |
| 许可证或再分发限制 | 无法合法发布 | 发布前完成 ionCube、MySQL 等组件法务审查 |
| DuckDB CGO/C++ 链接冲突 | Runtime 无法启动或发生原生崩溃 | 三平台原生 Runner 构建，检查 PHP/FrankenPHP/ionCube/CRT 组合 |
| Parquet 小文件或日志增长 | NFS 查询变慢或容量耗尽 | 批量发布、节点分区、保留策略、spool 和独立容量配额 |

## 19. 建议实施阶段

### 阶段一：Linux amd64 PoC

- 建立 PHP 8.4 最小 ABI patch。
- 构建动态 `libphp.so` 和最小 Caddy + FrankenPHP Library Runtime Host。
- 加载 ionCube PHP 8.4 TS Loader。
- 验证付费版、Classic mode、并发和信号行为。

### 阶段二：统一 Runtime

- 完成 PHP 扩展矩阵。
- 完成 Runtime 配置、Caddy JSON、php.ini 和目录规范。
- 完成 Linux amd64/arm64 与 Windows x86_64 构建。
- 建立 Runtime manifest、SBOM 和签名流程。
- 完成 DuckDB 三平台链接、Parquet 读写和禁用外部 Extension 的 PoC。

### 阶段三：集成环境

- 完成 Windows/Linux Runtime 包。
- 完成 Windows/Linux Full + MySQL 包。
- 完成 Docker Web、Scheduler、MySQL Compose。
- 实现服务管理、诊断、备份和恢复。
- 实现指标/日志批处理、共享 Parquet 发布、本地 spool 和受控查询。

### 阶段四：升级与发布

- 验证旧环境迁移。
- 完成全版本、双数据库测试。
- 完成安全与许可证审查。
- 灰度发布并建立 Runtime 升级策略。

## 20. 待讨论决策

1. Linux 首版是否同时发布 arm64，还是先完成 amd64。
2. Windows/Linux 是否需要图形控制面板，还是首版只提供 CLI/PowerShell 管理工具。
3. Full 包是否确定使用 MySQL 8.4 LTS，以及对应的再分发方式。
4. 首版必须支持哪些旧一键包版本的原地升级。
5. Runtime 与禅道应用是否独立发布，还是始终组合为单一安装包。
6. Runtime Host、Caddy、PHP、FrankenPHP 和 ionCube 的安全升级服务周期及支持期限。

## 21. 参考资料

- FrankenPHP 编译文档：<https://frankenphp.dev/docs/compile/>
- FrankenPHP Classic mode：<https://frankenphp.dev/docs/classic/>
- FrankenPHP Docker：<https://frankenphp.dev/docs/docker/>
- FrankenPHP ionCube 讨论：<https://github.com/php/frankenphp/issues/901>
- FrankenPHP ionCube 支持讨论：<https://github.com/php/frankenphp/issues/1775>
- ionCube Loaders：<https://www.ioncube.com/loaders.php>
- DuckDB Go API：<https://github.com/duckdb/duckdb-go>
- DuckDB Parquet 文档：<https://duckdb.org/docs/stable/data/parquet/overview>

## 22. 实施状态（2026-08-19）

- Linux amd64：PHP 8.4.24 ZTS + ionCube Loader + DuckDB 构建与 PoC 全链路
  验证通过。
- Linux arm64：在 `ubuntu-24.04-arm` 原生 Runner 上完成构建、冒烟与包
  组装，ionCube arm64 Loader 校验通过。
- Windows x86_64：在 `windows-2025` 原生 Runner 上完成构建、ionCube
  Loader 加载验证与 Full 包组装。
- ionCube 符号检查与 `zend_signal` ABI Shim 保持最小范围；Loader 升级时
  仍需重新执行未定义符号 diff。
