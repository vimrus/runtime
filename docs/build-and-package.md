# 编译与打包

本文档说明 Runtime 的源码编译、Docker 镜像、安装包和集成环境构建流程。正式构建会固定 PHP、FrankenPHP、Caddy、DuckDB、MySQL 和 ionCube 版本，并生成 manifest、SBOM、校验和及大小报告。

## 构建环境

通用依赖包括 Docker/BuildKit、Git 和 `jq`。Linux 构建需要能够运行 Docker 的环境；Runtime 使用 CGO 和 PHP Embed SAPI，正式构建应在目标平台的原生 Runner 上执行。

## Linux Runtime

在 Linux 主机上执行，脚本会根据主机架构自动选择 amd64 或 arm64：

```bash
scripts/native/build-linux.sh
```

产物位于 `dist/runtime-linux-amd64/` 或 `dist/runtime-linux-arm64/`。Linux Runtime 不做正式交叉编译；构建 arm64 时请在 arm64 Linux 主机或原生 arm64 Runner 上执行。构建脚本会使用 `packaging/runtime/Dockerfile` 生成 Runtime 制品并执行校验。

如需覆盖 Go 代理或 Debian 镜像源：

```bash
GOPROXY=https://goproxy.cn,direct \
DEBIAN_MIRROR=http://deb.debian.org/debian \
scripts/native/build-linux.sh
```

## Windows Runtime

在 Windows 原生环境执行 PowerShell，要求 Go、Visual Studio LLVM/Clang、Windows PHP 8.4 开发包、vcpkg 和网络下载依赖可用：

```powershell
Set-Location C:\path\to\runtime
.\scripts\windows\build.ps1
```

默认输出 `dist/runtime-windows-x64/`。脚本会校验锁定归档，编译 MSVC/COFF DuckDB，构建 `runtime\zentao.exe`，复制 PHP、扩展和 ionCube，并执行 PHP 与 Parquet 冒烟验证。仅在已有 DuckDB 缓存时才使用：

```powershell
.\scripts\windows\build.ps1 -SkipDuckDB
```

## Docker 镜像与 Compose

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

Docker 交付以 Linux amd64/arm64 原生 Runner 为基线；正式发布还应执行包校验、SBOM、签名和 Compose 冒烟测试。macOS 不是 Runtime 的正式交付目标；在 macOS 上请使用 Docker/BuildKit 构建 Linux 制品，或在 Linux 原生 Runner 上构建和验证。

开发者验证 Go Runtime Host 时使用：

```bash
scripts/ci/test-runtime-host.sh
```

该脚本会自动读取 PHP 头文件路径，并使用 `nowatcher` 构建标签关闭 FrankenPHP 的可选 Watcher C 扩展；生产构建也使用相同标签。

## Runtime / Full 安装包

Runtime staging 目录准备完成后：

```bash
scripts/package/assemble-runtime.sh <staging-dir> zentao-runtime-linux-amd64 dist/packages
scripts/package/assemble-full.sh <staging-dir> linux-amd64 zentao-full-linux-amd64 dist/packages
```

支持的 Full 平台参数为 `linux-amd64`、`linux-arm64` 和 `windows-x64`。脚本会从 `versions.lock.json` 下载并校验 MySQL 归档。

## 集成环境

准备应用包和 Runtime 制品后，可以生成一个自包含目录：

```bash
scripts/package/build-integrated-env.sh open /tmp/zentao-open
/tmp/zentao-open/run.sh
```

版本参数支持 `open`、`biz`、`max` 和 `ipd`。应用包默认从 `app-packages/` 查找，也可以通过 `ZENTAO_APP_PACKAGES_DIR` 指定目录。
