# 验证记录

本文件按日期和版本保留已执行的验证结果；历史记录描述当时的发行包和验证范围。
当前安装方式与支持范围见 [README](../README.md)，可用软件版本见
[软件下载站](https://oheco.org/)。

## 2026-09-12 · 0.2.0 开发验证

- 系统 `/usr/bin/zip` 3.0、`/usr/bin/unzip` 6.0 可用。使用 unzip 解出原始
  toolchains ZIP 的 binary-sign-tool（来自 `ohos-sdk-toolchains` 包），字节哈希与 SDK 目录中的文件一致，帮助命令退出 0。
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
  0.2.0 按大小写冲突处理规则保留原始小写版本。ETS 的原始 es2abc 在共享目录执行被系统拒绝，ETS/JS 按资源包
  收录，不创建编译器链接。LLDB 及其相关命令不注册。

本轮输出：`sdk-unit-host.log`、`sdk-e2e-home-host.log`（主目录）、
`sdk-e2e-host.log`（早期私有目录）、`sdk-bootstrap-host.log`、`sdk-bootstrap-path-host.log`、
`build-sdk-host.log`（均被 Git 忽略）。
SDK 和安装器集成测试的隔离目录已清理。

以下为 0.1.0 的历史验证记录。旧 Go `1.27.1` 包曾因适配未完成从索引撤下；后续已以
独立版本 [`1.27.1-ohos.1`](https://github.com/oheco/go/releases/tag/go1.27.1-ohos.1) 重新收录。
下述旧包验证结果保留为历史记录，新适配版的支持范围见其发行说明。

第一版 `oo 0.1.0`，索引仅包含 `oheco 0.1.0` 和 `go 1.27.1`。

## 平台

- 开发与通用测试：Linux arm64，Go 1.27.1。
- 原生构建与集成：HarmonyOS / aarch64，已移植的 Go 1.27.1 ohos/arm64。
- 原生构建由 Go 工具链调用 PATH 中的 `binary-sign-tool`（由 `ohos-sdk-toolchains` 包提供）完成签名。

## 已通过

- Linux：`go test -race ./...`、`go vet ./...`、gofmt。
- 鸿蒙：`go test ./...`，包括下载、索引更新、安装、切换、移除、文件锁和中断恢复。
- 鸿蒙主目录：已签名程序复制、chmod、两级软链接以及默认链接重命名后执行。
- 多版本与多命令：版本默认启用、`--no-switch`、新旧命令集合变化、保留版本链接。
- 失败保留：下载 SHA-256 错误、无效索引、用户文件和修改过的链接、跨包同名命令。
- 解压：越界路径、包外软链接、通过软链接写文件、重复条目、硬链接及非可执行命令。
- 事务：中断安装回滚、部分链接切换恢复、已提交卸载清理以及独占进程锁。
- 目录保护：拒绝沿用户替换的包目录软链接卸载其他位置的内容。
- 完整自举：通过本机 loopback HTTP 服务执行 `curl | zsh`，在含空格的隔离根目录安装。
- 原生 Go：下载现有 66,411,593 字节发行包，校验 SHA-256、解压、建立 go/gofmt 链接。
- Go 重定位：清除 GOROOT 后，通过默认链接得到版本目录内的 GOROOT，GOOS 为 ohos。
- 已安装 Go：`go version`、`gofmt@1.27.1 -h`、编译/签名/运行一个 Go 程序。
- 重复执行安装脚本：保留已有 zsh 配置，不重复写入同一路径；新 zsh 能找到 oo。
- 删除本地索引后：切换 Go、卸载 Go、清理 go/gofmt 两种链接、保留并切换 oheco。

原始输出保留在忽略目录 `build/test-host.log`、`build/e2e-host.log` 和
`build/build-host.log`。测试的安装根目录和 zsh 配置均是隔离的临时目录，测试结束清理。

## 复现原生端到端测试

以下命令使用当前检出的测试脚本，具体测试版本随源码更新。先准备好包描述及
脚本所需发行文件，再从 oheco 仓库运行：

```sh
python3 scripts/prepare-native-test.py
go run ./cmd/oo-index \
  --packages ../oheco-packages/tmp/e2e-packages \
  --output ../oheco-packages/tmp/e2e-public
```

然后运行原生端到端测试（先配置原生 Go 和私有可写 TMPDIR）：

```zsh
go build -o build/testserver ./scripts/testserver
build/testserver > build/testserver.log 2>&1 &
oo_fixture_pid=$!
zsh scripts/test-native.zsh > build/e2e-host.log 2>&1
kill "$oo_fixture_pid"
wait "$oo_fixture_pid" || true
```

fixture 只改写忽略目录内的索引 URL，不修改正式描述；使用真实的已签名软件包。
它监听本机 `127.0.0.1:18808`，可在独立终端前台启动，确认 ready 后再执行测试。

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
本轮发布的两个组件由 Guo Wei (@kdada) 负责移植维护。

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
- 鸿蒙上已直接使用公开地址完成 `curl | zsh` 自举、默认源索引同步、Go 下载和安装、
  Go 重定位、编译/签名/运行程序、版本切换及卸载。原始输出为 `build/public-host.log`。
  测试在隔离的临时安装目录执行，结束后清理。

## 2026-09-12 · 0.5.0 语言包验收

- Linux/ARM64 Go 1.27.1：`go test ./...`、`go vet ./...` 通过；registry 和 manager 的 `-race` 检查通过。
- HarmonyOS/ARM64 原生 Go 1.27.1、Node 24.21.0/npm 11.19.0、Python 3.14.7：完整 Go 测试通过。临时目录使用应用私有路径，避免共享文件系统改写权限位。
- 真实 npm：目录适配包覆盖、传递依赖、脚本禁用、换端口后的 `npm ci`、缓存复用、导入和卸载通过。真实 pip：多个 wheel 的兼容选择、传递依赖、导入和卸载通过。
- 原生 oo 经进程级代理访问官方 npm/PyPI，完成 `is-number@7.0.0` 与 `idna==3.10` 的安装、运行和卸载；含空格路径及 npm 锁文件复用通过。
- 交互式终端验收发现仅创建子进程组会使 npm 在安装后挂起；改为独立进程会话后，同一验收完整通过。正常输入输出仍继承，取消会终止整个包管理器进程组。
- 发布生成器已生成 v1/v2/v3 索引；语言描述的元数据从归档提取，实际下载校验同时核对元数据。正式索引发布安装结果在发布后补录。

## 2026-09-12 · 0.6.0 项目导出

- Linux：全量 `go test ./...`、`go test -race ./...`、`go vet ./...` 通过。
- 鸿蒙：使用已安装的 Go 1.27.1 原生工具链，在新的应用私有 Go 缓存中执行全量
  `go test -p 2 -count=1 ./...`、`go vet -p 2 ./...` 并签名构建，输出
  `oo 0.6.0 (ohos/arm64)`。编译并发限制为 2，没有替换用户的工具链或修改全局配置。
- Go 测试覆盖 projects-only/混合版本、单项目默认、多项目选择、版本及跨平台 latest
  歧义、ZIP/tar.gz、下载和缓存损坏、路径越界、重复条目、严格大小写冲突、内部软链接、
  含空格路径、已有文件/目录保护、复制取消清理和无安装记录/命令链接。
- 当前包描述及生成的 v1/v2/v3/v4 索引通过 JSON Schema 2020-12 验证；无效项目
  描述被拒绝。旧索引不包含 v4 描述，持续保留 oheco 自举入口。
- 网站实际 DOM 验证：项目包、同时提供安装产物与多个项目的版本、普通包，版本切换、
  项目名称/说明搜索、下载链接和复制导出命令通过，无脚本错误。
- 鸿蒙候选索引使用临时 loopback 服务；项目文件来自真实 GitHub Release，通过
  Linux 代理下载。`godot-editor@4.7.2-ohos.1` 的 682,872,170 字节归档经过 oo 的
  大小/SHA-256 校验后，导出到新的私有中文及含空格路径。全部 59 个导出文件与原归档
  逐一核对 SHA-256，包括已签名引擎和完整 runtime.zip。
- 默认当前目录导出、指定项目/版本/目标目录、修改项目后重复导出保留用户修改、
  禁用网络后的缓存导出通过；导出前后安装记录完全相同。项目包 install 错误提示
  正确指向 `oo export`。临时测试根目录及服务已清理。

实际项目摘要：`0723d5096ba02959bd098d823a39d430ca2f2ba9b9910935ef9606a2ff560bca`。
导出验证不执行工程代码，不验证 Godot 的签名 HAP、窗口呈现或应用沙箱 .NET 行为。
强制结束进程或断电后的外部输出目录不纳入安装事务恢复；可能留下部分导出文件。

原生验收脚本可在正式站点发布后验证默认索引和旧客户端升级：

```sh
python3 scripts/test-project-native.py --oo /path/to/signed/oo \
  --log /path/to/project-export.log --upgrade
```

脚本使用隔离 OHECO_ROOT，验证 0.5.0 读取 v3 索引升级至 0.6.0、新客户端读取 v4、
实际项目导出与离线复用，以及测试安装的 oo 卸载。默认使用当前工作区的代理地址，
其他环境通过 `--proxy` 和 `--tmp-parent` 指定代理及私有可写目录。
