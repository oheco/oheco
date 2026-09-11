# 验证记录 · 2026-09-11

## 2026-09-12 · 0.2.0 开发验证

- 宿主 `/usr/bin/zip` 3.0、`/usr/bin/unzip` 6.0 可用。使用 unzip 解出原始
  toolchains ZIP 的 binary-sign-tool，字节哈希与 SDK 目录中的文件一致，帮助命令退出 0。
- Linux：`go test -race ./...`、`go vet ./...` 和 gofmt 通过；鸿蒙：`go test ./...` 通过。
- ZIP 测试覆盖 CRC 损坏、截断、大小上限、加密标志、路径越界、目录剥离、重复条目、
  特殊文件、执行权限，以及内部软链接、越界/循环/悬空软链接和保留启动器目录。
- ZIP/tar.gz 大小写冲突测试覆盖两种先后顺序、三个变体的全部排列、小写内容及权限保留、
  无冲突名称保留、无小写候选/目录/软链接冲突拒绝，以及被舍弃 ZIP 条目的 CRC 校验。
- 启动器测试覆盖原名执行、空参数/空格/引号/命令替换文本原样传递、目录迁移、
  多版本切换、幂等安装、卸载、中断恢复和修改检测。无命令包离线生命周期通过。
- JSON Schema 2020-12 验证包描述及 v1/v2 索引通过；v1 兼容索引仅保留旧客户端可读包。
- 鸿蒙 loopback 自举、PATH 配置保留和重复安装通过。先切换至 0.1.0 并移除 0.2.0，
  再由旧客户端读取 v1 索引、下载并安装 0.2.0；新版随后读取 v2 索引，离线切换与卸载通过。
- PATH 验证同时覆盖显式加载 `.zshrc` 及直接启动新交互式 `zsh -i`，两者均能找到
  安装目录下的 `oo` 并运行 0.2.0；安装器输出当前终端加载配置或直接设置 PATH 的命令。
- 原生 `oo 0.2.0` 已重新构建并签名；在主目录下的 `.oheco-sdk-test.XXXXXXXX/root with spaces`
  中安装了 5 个原始 SDK ZIP，与默认 `~/.oheco` 使用相同文件系统。安装使用本地原始 ZIP
  缓存并由 oo 校验 SHA-256、大小及 ZIP CRC。
- native 的 8 个冲突头文件仅保留全小写名称；逐个枚举实际目录项、检查只有一个变体，
  并核对 SHA-256 与原始 ZIP 中的小写条目一致。测试包保留原始 Release 的下载地址和哈希。
- 实测 `ld.lld@26.0.0.35-Beta`、`lld-link@26.0.0.35-Beta` 启动正常；Clang、CMake、
  Ninja、HDC 通过版本命令检查。通过安装后的 clang 编译 C 程序、安装后的 binary-sign-tool
  自签名并执行成功；CMake 通过启动器找到随包 Modules；删除索引后的组件切换和全部卸载通过。
- native 的 sysroot 含 8 对仅大小写不同的文件，早期严格解压在主目录因文件重名失败。
  现按用户确定的规则保留原始小写版本。ETS 的原始 es2abc 在共享目录执行被宿主拒绝，ETS/JS 暂按资源包
  收录，不创建编译器链接。LLDB 及其相关命令不注册。

本轮输出：`sdk-unit-host.log`、`sdk-e2e-home-host.log`（主目录）、
`sdk-e2e-host.log`（早期私有目录）、`sdk-bootstrap-host.log`、`sdk-bootstrap-path-host.log`、
`build-sdk-host.log`（均被 Git 忽略）。
SDK 和安装器集成测试的隔离目录已清理。

以下为 0.1.0 的历史验证记录；Go 后续因适配未完成，已从当前软件索引移除。

第一版 `oo 0.1.0`，索引仅包含 `oheco 0.1.0` 和 `go 1.27.1`。

## 平台

- 开发与通用测试：Linux arm64，Go 1.27.1。
- 原生构建与集成：HarmonyOS / aarch64，已移植的 Go 1.27.1 ohos/arm64。
- 原生构建由 Go 工具链调用宿主 PATH 中的 `binary-sign-tool` 完成签名。

## 已通过

- Linux：`go test -race ./...`、`go vet ./...`、gofmt。
- 鸿蒙：`go test ./...`，包括下载、索引更新、安装、切换、移除、文件锁和中断恢复。
- 鸿蒙主目录：已签名程序复制、chmod、两级软链接以及默认链接重命名后执行。
- 多版本与多命令：版本默认启用、`--no-switch`、新旧命令集合变化、保留版本链接。
- 失败保留：下载 SHA-256 错误、无效索引、用户文件和修改过的链接、跨包同名命令。
- 解压：越界路径、包外软链接、通过软链接写文件、重复条目、硬链接及非可执行命令。
- 事务：中断安装回滚、部分链接切换恢复、已提交卸载清理以及独占进程锁。
- 目录保护：拒绝沿用户替换的包目录软链接卸载其他位置的内容。
- 完整自举：通过宿主 loopback HTTP 服务执行 `curl | zsh`，在含空格的隔离根目录安装。
- 原生 Go：下载现有 66,411,593 字节发行包，校验 SHA-256、解压、建立 go/gofmt 链接。
- Go 重定位：清除 GOROOT 后，通过默认链接得到版本目录内的 GOROOT，GOOS 为 ohos。
- 已安装 Go：`go version`、`gofmt@1.27.1 -h`、编译/签名/运行一个 Go 程序。
- 重复执行安装脚本：保留已有 zsh 配置，不重复写入同一路径；新 zsh 能找到 oo。
- 删除本地索引后：切换 Go、卸载 Go、清理 go/gofmt 两种链接、保留并切换 oheco。

原始输出保留在忽略目录 `build/test-host.log`、`build/e2e-host.log` 和
`build/build-host.log`。测试的安装根目录和 zsh 配置均是隔离的临时目录，测试结束清理。

## 复现原生端到端测试

在 Linux 侧完成正式包描述和发行文件后，从 oheco 仓库运行：

```sh
python3 scripts/prepare-native-test.py
go run ./cmd/oo-index \
  --packages ../oheco-packages/tmp/e2e-packages \
  --output ../oheco-packages/tmp/e2e-public
```

然后在鸿蒙宿主的 oheco 仓库中运行（先配置原生 Go 和私有可写 TMPDIR）：

```zsh
go build -o build/testserver ./scripts/testserver
build/testserver > build/testserver.log 2>&1 &
oo_fixture_pid=$!
zsh scripts/test-native.zsh > build/e2e-host.log 2>&1
kill "$oo_fixture_pid"
wait "$oo_fixture_pid" || true
```

fixture 只改写忽略目录内的索引 URL，不修改正式描述；使用真实的已签名软件包。
它监听宿主 `127.0.0.1:18808`，可在独立终端前台启动，确认 ready 后再执行测试。

## 索引和下载站

- 两个包描述及合并索引通过 JSON Schema 2020-12 验证，包括跨文件 schema 引用。
- 索引生成器的语义校验通过；实际发行文件的大小、SHA-256 与索引一致。
- 已确认 oheco 压缩包内二进制与最终签名构建逐字节一致，权限为 0755。
- 安装脚本通过 zsh 语法检查；页面脚本通过 JavaScript 语法检查。
- 下载站通过 DOM 交互测试：两个组件展示、按命令搜索、空结果、版本与下载链接、
  安装命令复制和带可访问标签的版本选择器；未出现脚本错误。
- 浏览器下载遇到 TLS 传输错误，未完成真实浏览器的视觉和布局验证。

## 发布边界

首轮验证使用本地源码和真实发行包，公开下载入口在下述首次发布中启用。
2026-09-12，用户确认两个组件的移植维护者均为 Guo Wei (@kdada)。

通用 CI 使用官方 Go 1.23.x 运行平台无关逻辑；OHOS 产物必须使用移植工具链构建。
首轮本机验证使用 Go 1.27.1；随后执行的远端检查记录在下方。

## 2026-09-12 · GitHub 首次发布

- [oheco v0.1.0](https://github.com/oheco/oheco/releases/tag/v0.1.0) 已发布，
  标签指向 `c053e754ad87b69d31a792790a1a14948ed8b57f`。
- [Go v1.27.1](https://github.com/oheco/go/releases/tag/v1.27.1) 已发布，
  标签指向源码快照 `e4baa69d468dce9939528693585fa0ce71a4cfcd`。
  快照以发行包中的鸿蒙源码为准，已对照压缩包逐文件校验 15,669 个已暂存源码文件；
  仓库 README 补充安装、移植说明和上游来源。
- 两个 Release 的压缩包大小及 GitHub 返回的 SHA-256 与软件索引一致。
- oheco 的 GitHub Actions 测试已通过，包括官方 Go 1.23.x 环境。
- oheco-packages 已开启 GitHub Actions 类型的 Pages，HTTPS 已启用。
- [Pages 工作流 34622788674](https://github.com/oheco/oheco-packages/actions/runs/34622788674)
  构建和部署成功，其中包括两个正式软件包的下载与校验。
- 已通过 HTTPS 验证[下载站](https://oheco.github.io/oheco-packages/)、
  [安装脚本](https://oheco.github.io/oheco-packages/install.sh)和
  [索引](https://oheco.github.io/oheco-packages/index/v1/index.json)可访问且内容正确。
- 鸿蒙宿主已直接使用公开地址完成 `curl | zsh` 自举、默认源索引同步、Go 下载和安装、
  Go 重定位、编译/签名/运行程序、版本切换及卸载。原始输出为 `build/public-host.log`。
  测试在隔离的临时安装目录执行，结束后清理。
