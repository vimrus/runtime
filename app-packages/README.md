# ZenTao 应用包约定目录（gitignored）

本目录用于放置由外部发布流程生成的各版本禅道应用包（平台无关，`www/`
为 Web 根目录）。runtime 不负责合并或打包。

目录位于 runtime 版本库内，但已加入 `.gitignore`（`/app-packages/`），
付费包不会被推送到 GitHub。

## 目录与命名

```text
runtime/app-packages/
  open/zentaopms.zip
  biz/zentaopms.zip
  max/zentaopms.zip
  ipd/zentaopms.zip
```

- 每个版本目录只保留一个 zip；也可以使用 `zentaopms-<edition>.zip`。
- 版本号由包内 `VERSION` 文件或 manifest 识别，不依赖文件名。
- 新版本直接覆盖对应目录中的 zip；存在多个 zip 时查找脚本会报错。
- 包内必须包含 `www/index.php`、`www/install.php`、`www/api.php`；
  不得包含 `.git`、测试缓存和开发依赖。

## 使用

```bash
# 查找某个版本的包路径
runtime/scripts/ci/find-app-package.sh ipd

# 解包到 Runtime 应用布局
runtime/scripts/ci/stage-app-package.sh "$(runtime/scripts/ci/find-app-package.sh ipd)" /opt/zentao

# 联合冒烟
runtime/tests/e2e/zentao-app-smoke.sh "$(runtime/scripts/ci/find-app-package.sh ipd)"
```

可通过 `ZENTAO_APP_PACKAGES_DIR` 覆盖本目录位置（例如 self-hosted Runner
上的其他路径）。
