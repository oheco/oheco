# 验证记录 · 2026-09-11

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
