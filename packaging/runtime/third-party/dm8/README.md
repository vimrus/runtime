# DM8 PHP PDO 驱动与客户端运行库（vendored）

本目录存放构建 Linux Runtime 所需的达梦 DM8 PHP 8.4 ZTS PDO 驱动及其客户端
运行库。二进制从厂商安装介质提取后入库，构建时不再联网下载，并在构建阶段
用 `SHA256SUMS` 做完整性校验。

## 来源

- 厂商安装介质：`dm8_20260710_x86_CentOS7_64.zip`（DM8 build 20260710，
  x86_64 / CentOS 7 基线，glibc 2.17 起可用）
- 厂商安装介质：`dm8_20260708_FTarm2000_kylin10_sp1_64.zip`（DM8 build
  20260708，aarch64 / 麒麟 Kylin 10 SP1（FT-2000）基线，glibc 2.28 起可用）
- 介质内来源路径与目录结构的对应关系：

| 入库目录 | 介质内来源 | 内容 |
| --- | --- | --- |
| `php-8.4-zts/linux-amd64/` | `source/drivers/php_pdo/php84ts_pdo_dm.so` | PHP 8.4 ZTS PDO 驱动 `pdo_dm`（amd64） |
| `dpi/linux-amd64/libdmdpi.so` | `source/drivers/dpi/libdmdpi.so`（与 `source/bin/libdmdpi.so` 内容一致） | DM 客户端接口库，pdo_dm 运行时按名 dlopen（amd64） |
| `dpi/linux-amd64/components/` | `source/bin/`（达梦官方 PHP 组件清单的 18 个组件，另含闭包依赖） | libdmclientlex/libdmutl/libdmstrt/libdmshm/libdmos/libdmmsg/libdmmout/libdmmem/libdmelog/libdmdta/libdmdcr/libdmcyt/libdmcvt/libdmcpr/libdmcomm/libdmcfg/libdmcalc，以及 `libdmtlog.so`（`libdmcomm` 的 NEEDED 闭包依赖，官方清单之外）（amd64） |
| `dpi/linux-amd64/dependencies/` | `source/drivers/dpi/dependencies/` | DM 客户端自带的 `libssl.so` / `libcrypto.so`（OpenSSL 3，SONAME 为 `libssl.so.3`/`libcrypto.so.3`）（amd64） |
| `php-8.4-zts/linux-arm64/` | `source/drivers/php_pdo/php84ts_pdo_dm.so` | PHP 8.4 ZTS PDO 驱动 `pdo_dm`（arm64） |
| `dpi/linux-arm64/libdmdpi.so` | `source/drivers/dpi/libdmdpi.so`（与 `source/bin/libdmdpi.so` 内容一致） | DM 客户端接口库（arm64） |
| `dpi/linux-arm64/components/` | `source/bin/` | 与 amd64 相同的 18 个组件闭包（arm64） |
| `dpi/linux-arm64/dependencies/` | `source/drivers/dpi/dependencies/` | 与 amd64 相同的 OpenSSL 3 依赖（arm64，介质自带版本 3.5.6） |

> 说明：达梦官方 FAQ（《PHP 语言》-“申请 php 依赖的 dmdbms/bin 目录下的
> dll 组件清单”）明确 PHP 驱动（PDO 与传统 dm 扩展共用）需要 18 个动态库：
> dmdpi、dmclientlex、dmutl、dmstrt、dmshm、dmos、dmmsg、dmmout、dmmem、
> dmelog、dmdta、dmdcr、dmcyt、dmcvt、dmcpr、dmcomm、dmcfg、dmcalc；
> `libdmdpi.so` 会按功能路径 dlopen 其余组件。实测只带 `libdmdpi.so` 时建连
> 报 `[-70089] Encryption module failed to load`，补齐
> `dependencies/libcrypto.so + libssl.so`（`$ORIGIN/dependencies`，与
> `libdmdpi.so` 的 DT_RPATH 对应）后可正常进入建连流程。

## PHP ABI

PHP 8.4 ZTS，API 号 `20240924`（`php84ts_pdo_dm.so` 内嵌
`API20240924,TS`）。本目录只放 ZTS 版本；NTS 运行时（`php84_pdo_dm.so`）
不在范围内。

## 文件与校验

全部 44 个二进制（amd64/arm64 各 22 个）的 SHA256 见同目录
`SHA256SUMS`，构建时执行 `sha256sum -c SHA256SUMS` 校验。各架构关键文件的
摘要：

| 文件 | SHA256 |
| --- | --- |
| `php-8.4-zts/linux-amd64/php84ts_pdo_dm.so` | `b2b953de1e1591f08b17f3744781e12c0c88eee40712338cea10699431921930` |
| `dpi/linux-amd64/libdmdpi.so` | `17cbf0b9ca84da8e82598b792598daffa9201ac1f465b683975afd92f6265403` |
| `php-8.4-zts/linux-arm64/php84ts_pdo_dm.so` | `76849588953657ee330f0ed977634e9572a95221f77c947b809c67e91d1f9624` |
| `dpi/linux-arm64/libdmdpi.so` | `9069ef497d25f56d23e5de9c5038c2acaa09c112eb38260c6d92e2394ae82a05` |

## 部署位置（Docker 镜像 / artifact）

- `php84ts_pdo_dm.so` → `/opt/zentao/php/lib/php/extensions/no-debug-zts-20240924/pdo_dm.so`
  （按扩展名规范改名为 `pdo_dm.so`）
- `libdmdpi.so` + `components/*.so` → `/opt/zentao/php/lib/`
  （php CLI RPATH `$ORIGIN/../lib`、libdmdpi 的 `$ORIGIN` 均指向该目录，
  组件之间按 DT_RPATH `$ORIGIN` 互相解析）
- `dependencies/*.so` → `/opt/zentao/php/lib/dependencies/`
  （libdmdpi 的 DT_RPATH 含 `$ORIGIN/dependencies`）
- `packaging/runtime/php.ini` 启用
  `extension=${ZENTAO_RUNTIME_PHP_ROOT}/php/lib/php/extensions/no-debug-zts-20240924/pdo_dm.so`

## dm_svc.conf

`packaging/runtime/dm_svc.conf` 为运行时默认交付的达梦客户端配置：

```text
TIME_ZONE=(480)
LANGUAGE=(cn)
CHAR_CODE=(PG_UTF8)
```

- Docker 镜像：构建时写入 `/etc/dm_svc.conf`，同时放入
  `/opt/zentao/config/dm_svc.conf`；
- 非 Docker（原生/信创）部署：Runtime 进程启动时通过环境变量
  `DM_SVC_PATH=<installRoot>/config` 指向同文件，不需要 root 权限写 `/etc`；
- Runtime 启动会自检 `<installRoot>/config/dm_svc.conf`：随包已有或用户
  自定义时保留原样，缺失时自动写入上面的默认内容（`zentao serve` 与
  `zentao php` 都会执行），因此用户无需手动创建任何文件即可让 DM PDO
  读到时区/语言/字符集配置。

`CHAR_CODE=(PG_UTF8)` 缺省会导致 UTF-8 数据库中文乱码，`TIME_ZONE=(480)`
缺失会导致时间类函数偏移，这两项是达梦 PHP 连接的常见坑。

## 未包含的内容

- `libphp84_dm.so` / `libphp84ts_dm.so`：传统 `dm` 扩展（`dm_connect` 等
  函数式 API）。禅道全代码库只使用 PDO（`new PDO('dm:...')`），未发现
  `dm_*` 调用点，故不启用也不入库；如未来有外围脚本需要，可按本目录同样
  方式从介质补充 ZTS 版本并校验后启用。
- NTS 驱动、Windows 驱动：介质/授权范围不同，需要另行获取。

## 许可证与再分发

达梦驱动与客户端库为厂商专有二进制，入库与随 Runtime 再分发前需获得厂商
授权（`proprietary-redistribution-approval-required`）。当前支持 Linux
amd64（CentOS 7 基线介质）与 arm64（FT-2000 / Kylin 10 SP1 介质）。
