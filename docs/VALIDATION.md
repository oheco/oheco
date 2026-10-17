# 验证记录

本文件按日期和版本保留已执行的验证结果；历史记录描述当时的发行包和验证范围。
当前安装方式与支持范围见 [README](../README.md)，可用软件版本见
[软件下载站](https://oheco.org/)。

## 2026-10-17 · 0.13.0 工程导出、签名和安装

当前状态：原生验收全部通过，[v0.13.0](https://github.com/oheco/oheco/releases/tag/v0.13.0)
已正式发布并设为 Latest；目录、Pages、正式索引安装与工程导出复核全部完成。

- 实现提交：`da0e8641f1a7de46222f645ca8357cc3dae32548`。HarmonyOS arm64、原生 Go 1.27.1、
  `CGO_ENABLED=0`，由 PATH 中的 `binary-sign-tool` 完成最终可执行文件签名。
- 从新的 `$XDG_CACHE_HOME/oheco-0.13.0-sdk-deps-20261017` 构建；`go version -m` 确认上述
  `vcs.revision`、`vcs.modified=false`，版本入口为 `oo 0.13.0 (ohos/arm64)`。
- [工程安装验收脚本](../scripts/test-export-install-native.py) 的微型工程链路通过：不同 origin 的
  索引与项目制品、dry-run 无写入、含空格导出目录、官方开发签名、OHPM/Hvigor 构建、模块签名及
  Profile 校验、HDC 覆盖安装、设备实际 bundle/versionCode 确认、同命令恢复及用户修改保留。
- 使用正式 v6 索引下载的 `godot-editor@4.7.2-ohos.6` 工程，以上完整链路通过；下载归档大小
  `76859548` 字节，SHA-256 `0019ce5f3176b75517584976d2b6ecce84a59ba20ee84dd8ebaad640837c2562`。
  在自有测试副本中只重设 fixture bundle/版本并移除签名配置，保留项目依赖和实际源码。
- 使用正式索引的 `blender@5.2.2-ohos.17` 同样通过完整导出、签名、安装与同命令恢复链路；
  原发布归档大小 `143782689` 字节，SHA-256
  `536c42361fbffae4dcacc3ce0fccf1de0536c216d49f0151196612541d7edb05`。
  保留真实源码和依赖，测试副本改用 fixture bundle/version。缺省 `applyToProducts` 按 SDK 映射至
  default 产品；仅在 OHPM 安装期间排除 CLT 自带的两个工具开发依赖，其余依赖保留。
  真实根 `oh-package.json5` 在两次安装后均恢复原字节，摘要
  `1c910ba416c76f4db3bbaff65d8d46c012ad2ebb61be1af1a605f92107d875fd`。
- 工具环境：CLT `26.0.0.201`、内置 Node `24.18.1`、官方 DevEco CLI `1.3.5`、设备 API 26。
  复核范围止于导出工程、构建签名、安装和版本确认；不启动或测试 Godot/Blender 应用功能。
  测试使用自有 fixture bundle，不替换已有应用；结束只卸载该自有 bundle。
- 采用 SDK 公开工程构建任务产生的签名 HAP/HSP，并按 product/target/artifactName 精确选择新产物。
  oo 不提供现成 HAP 的签名入口。当前原生 SDK 的可选 all-signed APP 扩展依赖 Java 解包工具，
  该扩展未启用；无需 Java。保留的定向进程适配仅修正已知 SDK rootDir 工程边界缺陷。
- 客户端定向测试、真实 SDK 工程边界/大小写/EACCES 回归、全量 `go vet ./...` 与补丁检查通过。
  [Go 1.23 CI](https://github.com/oheco/oheco/actions/runs/39013580246) 的 race、vet 和格式检查通过。
  SDK 缺省 `applyToProducts` 的真实 Blender 配置已固化为默认产品映射回归。
  JSON5 的六份实现源码及许可证与 `tpr/json5` 内固定的完整上游快照逐字节一致。
- 原生 `CGO_ENABLED=0 GOMAXPROCS=2 go test -p 1 ./...` 全部通过；在 SDK 空闲时串行运行，
  当前提交 manager 用时 258.858 秒，registry 用已有通过缓存（此前完整运行 133.226 秒）。
  新增 SDK 工具依赖剔除及原字节恢复、真实子进程 SIGKILL 后恢复、外部修改保留测试均通过。
- 发行包大小 `3485372` 字节，SHA-256
  `6ae4aa5b0baad2bbe84bfab9a5fa121baa6ac6acd32e5dc9ed68940572edc220`。
  签名可执行字节保留、原生执行权限、含空格路径解包/迁移、版本/help 和文档许可证布局均通过。

本机链路记录位于 `$XDG_CACHE_HOME` 下的 `oo-export-install-20261017-native-success`、
`oheco-0.13.0-godot-native-20261017`、`oheco-0.13.0-blender-sdk-deps-20261017` 和
`oheco-0.13.0-release-sdk-deps-20261017`；正式站点复核位于 `oheco-0.13.0-published-20261017`。
这些结果针对本机 HarmonyOS/API 26 环境，不推广为所有设备与系统版本的保证。

目录提交 `e4406adcd3f698e8fcab8a18cc9d0f37fbca12bf` 仅更新 oheco 版本和 Blender 使用说明；
隔离工作树重接最新远端后构建通过（25 包、自举 0.13.0），网站 DOM/复制/工程入口回归通过。
[Pages 构建与部署](https://github.com/oheco/oheco-packages/actions/runs/39014647639) 成功。
正式 [v6 索引](https://oheco.org/index/v6/index.json) 的 `generated_at` 为
`2026-10-17T07:43:07Z`，客户端 latest=0.13.0，下载 URL、大小和 SHA-256 均与正式 Release 一致；
官网和 `install.sh` 同样通过复核，GitHub Latest 的远端 asset digest 与本地一致。

[正式索引验收脚本](../scripts/test-export-install-published.py) 使用私有安装根、HOME、XDG 和 TMPDIR，
从官方索引安装 0.12.0，再由旧客户端刷新索引并下载正式 GitHub 0.13.0；默认和版本入口及实际软链接
均正确。正式 `godot-editor@4.7.2-ohos.7`、`blender@5.2.2-ohos.17` 的 app 工程导出至含空格路径通过，
对已导出目录和新目录的 `--install --dry-run` 均未创建 receipt、锁或新目录。
阻断网络代理后切换 0.12/0.13 及卸载全部版本通过，临时安装、配置和源码副本已清理。
本轮正式复核不再次构建、签名或启动应用；设备安装证据来自发布前的原生验收。

## 2026-10-15 · 0.12.0 用户服务发行验收

- 实现与发行标签目标：`f1a8cb91b42794b95333476408e958e0653da218`，基线为
  `b29bdfda80e6087d4873984377338d70c601770e`（0.11.0）。原有安装状态 schema 和索引 schema 不变。
- HarmonyOS arm64、原生 Go 1.27.1、`CGO_ENABLED=0`、PATH 中的 `binary-sign-tool`。
  使用新的 `$XDG_CACHE_HOME/oheco-0.12.0` 构建缓存验证完整签名流程；最终包从干净实现提交重新构建，
  `go version -m` 确认为该提交且 `vcs.modified=false`。未引入第三方依赖或临时联网构建输入。
- 原生全量 `go test -p 2 -count=1 ./...`、`go vet -p 2 ./...` 和 gofmt 检查通过。
  [Linux Go 1.23 CI（含 race）](https://github.com/oheco/oheco/actions/runs/38708071039) 通过。
- [原生验收脚本](../scripts/test-daemon-native.py) 的 13 项全部通过：并发单实例；enable 与当前状态分离；
  追加的两路日志和字面 argv/cwd/env/PATH；坏定义隔离；重启限流及取消重试；daemon SIGKILL 后立即重建；
  主进程自然退出的孙进程清理；同会话内不同 PGID 清理；runner SIGKILL；报告管道中断；
  zsh autoload/PTY 退出；原映像删除及入口切换；前台模式。
- autoload 验收故意把 oo 执行延迟 3 秒，要求 zsh 在 2 秒内退出并关闭 PTY，随后 daemon 和服务仍成功启动。
  标记幂等、无末尾换行原样恢复、特殊路径、ZDOTDIR、软链接、权限保留、并发写入、损坏标记及写入日志
  被替换成软链接时保留原配置，均通过单元测试和原生流程。未改写用户真实 zsh 配置。
- 明确复现并固化：报告管道关闭时旧实现被 SIGPIPE 结束（退出 -13）；修复后 runner 完成清理并退出 0。
  鸿蒙在进程退出期间读取 `/proc/<pid>/stat` 会偶发 EINVAL；实现和验收脚本有限重试，不据此认定 PID 已消失。
- [发行包安装脚本](../scripts/test-daemon-install-native.py) 在真实官方 v6 索引下载 0.11.0，
  由旧客户端安装最终候选归档并验证版本入口、daemon/日志/autoload、0.11/0.12 切换、全卸载和配置保留。
  删除全部已安装 oo 映像后，原 daemon 仍可启动 runner 并正常关闭。所有配置和进程均在私有 TMPDIR 隔离。
- [v0.12.0 Release](https://github.com/oheco/oheco/releases/tag/v0.12.0) 已正式发布为 Latest，
  重新下载核对资产名、大小、归档 SHA-256 和校验文件一致。归档大小 **3,322,569 字节**，SHA-256：
  `cfa050f9be9a2e5a8f5e3a508fa8d37e1845ec855427a3048624f4d905fd1e24`。
- 目录提交 `6017b76d4392552184a9d0e4c1adf67e283d65d8`，`sh scripts/build.sh` 和
  `node scripts/test-site.cjs` 通过（25 个包，自举版本 0.12.0；网站测试使用模拟 DOM，不声称浏览器渲染验收）。
- [Pages 构建与部署](https://github.com/oheco/oheco-packages/actions/runs/38708813508) 成功。
  通过必需代理复核 `https://oheco.org/`、默认 v6 索引和安装脚本：latest=0.12.0，URL、大小和 SHA-256 全部一致。
- 正式默认索引的隔离安装复核通过：真实下载 0.11.0，由旧客户端刷新正式索引、下载 GitHub 上的 0.12.0，
  检查默认/版本入口、服务启停与日志、autoload、离线切换和全卸载；卸载后仍由固定映像启动服务并正常关闭。
  元数据与制品分别来自 oheco.org 和 GitHub，未用本地 fixture 替代正式安装。测试结束后隔离配置和进程已清理。

复现主要命令（从仓库根执行，按环境设置真实的 TMPDIR 和必需代理）：

```zsh
CGO_ENABLED=0 go test -p 2 -count=1 ./...
CGO_ENABLED=0 go vet -p 2 ./...
GOCACHE="$XDG_CACHE_HOME/oheco-0.12.0" OHECO_GO="$(command -v go)" sh scripts/build-ohos.sh
python3 scripts/test-daemon-native.py build/oo
python3 scripts/test-daemon-install-native.py --archive dist/oheco-0.12.0-ohos-arm64.tar.gz
python3 scripts/test-daemon-install-native.py --published
```

本机完整日志位于被 Git 忽略的 `build/daemon-{build,native,unit-final,vet-final,install-candidate,install-published,pages,online}.log`。
普通终端退出和 PTY 挂断已验证；系统强制结束整个应用不承诺存活；主动 daemonize/setsid 不在服务契约内。
这些结果针对本机 HarmonyOS 环境，未推广为所有设备/系统版本的保证。

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
- 安装脚本配置分支：PATH 与 XDG/TMPDIR 分两次询问，无终端时不修改配置，历史
  `export PATH=` 行原地升级为 `case` 判断；`scripts/test-installer-config-native.py`
  覆盖同意、拒绝、非交互和迁移四条路径。
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

安装脚本的配置分支自带 loopback fixture，不依赖发行包，可单独复现：

```sh
python3 scripts/test-installer-config-native.py
```

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
