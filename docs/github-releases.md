# GitHub Release 自动发布

工作流：`.github/workflows/release.yml`。

## 触发方式

- 每隔 3 天（每月 1、4、7…日）北京时间 **09:00**（UTC 01:00）从默认分支发布预览版，日期间隔按自然月重置。GitHub 定时任务可能延迟；无代码变化也会生成新版本。
- 在 GitHub 的 **Actions → Release → Run workflow** 手动触发，可以选择分支。默认发布预览版；取消 `prerelease` 后发布正式 Release。
- 首次 Release 名称为 **bqAgent v0.0.1**，Git 标签为 `v0.0.1`。后续取已有 `v主版本.次版本.补丁版本` 标签中的最大版本，递增补丁号，例如 `v0.0.2`、`v0.0.3`。预览版和正式版共用递增序列；旧的 `build-*` 标签不参与计算。已有草稿 Release 的版本也会计入，避免上传失败后重用版本号。

将这些文件提交并推送到 GitHub 默认分支后，定时任务才会生效。仓库需要启用 GitHub Actions，并允许工作流使用 `contents: write` 权限。使用内置 `GITHUB_TOKEN`，无需另建个人访问令牌。

## 发布内容

先使用 Node.js 24 构建 WebUI，再使用 `go.mod` 指定的 Go 版本编译并嵌入前端。提供 Linux、macOS（文件名为 darwin）、Windows 的 amd64 和 arm64 共六个压缩包；Windows 为 zip，其余为 tar.gz。每个包包含可执行文件及中英文 README，Release 另附 `SHA256SUMS`。

构建全部成功后创建草稿 Release，上传全部附件，再公开发布。上传失败时草稿保留供检查，工作流失败；可删除该草稿，或确认附件后手动处理。重跑会生成新的标签。

定时版本标为 prerelease；手动取消预览版选项可发布正式版。版本号自动递增补丁号，不自动递增主版本或次版本，也不自动清理历史版本。

## 修改时间与本地验证

编辑工作流中的 `cron: '0 1 */3 * *'` 调整时间（`*/3` 表示每月按 3 天间隔的日期，月份切换时重新从 1 日开始）。GitHub cron 使用 UTC，北京时间减 8 小时即为 UTC。公开仓库长时间没有活动时，GitHub 可能禁用定时工作流，需要重新启用。

仅验证打包逻辑（不联网、不编译项目）：

```sh
python3 -m unittest discover -s scripts -p 'test_release.py' -v
```

测试替换 Go 编译器，实际执行六个平台的归档与校验和生成，并检查编译失败、非法标签和已有输出目录的处理。这不代表真实跨平台构建已验证；完整构建和 GitHub 发布需在 Actions 中验证。

本地完整打包需要安装 Go、Node.js、npm、zip、tar 和 shasum，并允许下载依赖：

```sh
make webui-build webui-verify
RELEASE_TAG=build-local sh scripts/build-release.sh
```

输出目录为 `release/`；如果目录已存在，脚本会拒绝执行，避免混入旧附件。
