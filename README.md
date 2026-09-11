# oheco · oo

鸿蒙原生软件包管理器，使用 Go 编写，二进制名为 `oo`。首版支持 HarmonyOS arm64。

## 安装

在鸿蒙原生 zsh 终端执行：

```zsh
curl -fsSL https://oheco.github.io/oheco-packages/install.sh | zsh
```

安装器依赖 `curl`、`tar`、`sha256sum`（或 `shasum`）及基础文件命令，不依赖 Go、Git
或 jq。脚本会添加 zsh PATH 配置；重新打开终端，或按脚本输出设置当前终端的 PATH。
`OHECO_NO_MODIFY_PATH=1` 可关闭配置文件修改。已有工具不会因索引同步失败被删除。

## 使用

```zsh
oo update
oo search
oo info go
oo install go
oo list
oo install go@1.27.1 --no-switch
oo switch go 1.27.1
go@1.27.1 version
oo remove go@1.27.1
oo install oheco
```

- `update` 只更新索引，失败保留旧索引；`search`、`info` 查询本地索引。
- `install name` 安装该平台的 `latest`；`install name@version` 安装指定版本。
  默认启用本次安装的版本；`--no-switch` 只安装，保留当前启用状态。
- `switch name version` 只切换已安装版本，一起处理包内全部命令。
- `remove name` 删除当前启用版本；`remove name@version` 删除指定版本；
  `remove name --all` 删除全部版本。删除启用版本后不自动选择其他版本。
- `list` 用 `*` 标记启用版本。切换、卸载和列出安装状态不需要网络或远端索引。
- `recover` 恢复中断事务。其他修改命令启动时也会自动恢复。
- `oheco` 自身作为普通包更新和切换；删除 `oheco` 的启用版本也会删除默认 `oo`
  链接，可通过保留的 `oo@版本` 或安装脚本恢复。

## 目录和配置

默认安装到 `~/.oheco`；`OHECO_ROOT` 可指定其他根目录。
`OHECO_INDEX_URL` 可覆盖索引地址。正式源必须使用 HTTPS；仅 loopback 允许 HTTP，
用于本地开发与集成测试。客户端按原生 `GOOS-GOARCH` 选择产物，不将 Linux 当成鸿蒙。

```text
~/.oheco/
  packages/oheco/0.1.0/bin/oo
  packages/go/1.27.1/bin/{go,gofmt}
  bin/oo@0.1.0 -> ../packages/oheco/0.1.0/bin/oo
  bin/oo -> oo@0.1.0
  bin/go@1.27.1 -> ../packages/go/1.27.1/bin/go
  bin/go -> go@1.27.1
  index/index.json
  state/installed.json
  state/lock
  state/transaction.json       # 仅事务进行中存在
  cache/downloads/<sha256>.tar.gz
  tmp/
```

本地安装记录保存每个版本的完整产物描述和命令映射。不同包提供相同命令、用户已有
同名文件、用户修改过的链接都会导致操作停止；不会覆盖这些文件。
下载必须通过大小和 SHA-256 校验；解压拒绝路径越界、硬链接和特殊文件。
允许包内有效的相对软链接，禁止越界、循环及悬空软链接。
单包解压上限 8 GiB / 250000 条目，索引上限 32 MiB。

修改操作使用进程锁和持久化事务记录，单条链接通过重命名替换；进程中断后回滚未提交
操作或完成已提交清理。多个命令的链接逐条切换，不承诺对外同时变化，也不承诺底层
文件系统不支持的断电持久化。请在鸿蒙宿主执行安装，以保留宿主所需执行权限。
下载缓存不会随卸载删除，可手动删除 `cache/downloads/` 中的缓存文件。

## 原生构建

使用已经移植的 OHOS Go 工具链；官方 Go 发行版尚不支持 `GOOS=ohos`。
Linux 工作区与鸿蒙共享源码。通过 `zshc` 连接宿主后：

```zsh
export TMPDIR=/data/storage/el2/base/haps/entry/files/go-tmp
mkdir -p "$TMPDIR"
cd /storage/Users/currentUser/dev/ohos/oheco
OHECO_GO=/storage/Users/currentUser/dev/ohos/go/bin/go sh scripts/build-ohos.sh
build/oo --version
```

`TMPDIR` 需按当前宿主应用的可写目录调整。构建脚本默认使用相邻 `go/bin/go`；
`OHECO_VERSION` 默认 `0.1.0`。OHOS Go 工具链自动调用 PATH 中的 `binary-sign-tool`
签名，工具缺失时检查 LLVM 工具目录的 PATH。普通用户运行已签名的 `oo` 无需编译工具。

Linux 侧打包，不改变已签名二进制内容：

```sh
python3 scripts/package.py --version 0.1.0
```

输出 `dist/oheco-0.1.0-ohos-arm64.tar.gz` 和 `.sha256`。同名文件不会被覆盖。
包内包含 `bin/oo`、README 和 MIT 许可证。

## 测试与索引生成

常规 Go 环境可运行核心逻辑测试：

```sh
go test ./...
go vet ./...
```

宿主使用 OHOS Go 重复执行同一测试套件，临时目录使用宿主私有可写目录。
测试包括多版本和多命令切换、离线卸载、损坏下载、索引失败保留、命令冲突、
安全解压、进程锁和中断恢复。

相邻检出 `oheco-packages` 后生成网站（`go` 为开发环境可运行的 Go）：

```sh
go run ./cmd/oo-index
go run ./cmd/oo-index --verify-artifacts
```

后一个命令会下载验证全部远端软件包，适用于发布。安装脚本从本仓库模板和
`oheco-packages` 中的 `oheco.json` 生成，不需要独立维护版本或哈希。

第一版只安装预编译包，不执行包内安装脚本；跨包依赖求解、源码构建、自动升级和
服务管理留待后续版本。Go 编译新程序时仍需要签名工具，cgo 还需要 OHOS LLVM/SDK；
详见 Go 包自身的移植文档。
