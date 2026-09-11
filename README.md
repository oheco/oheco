# oheco · oo

鸿蒙原生软件包管理器，使用 Go 编写，二进制名为 `oo`。首版支持 HarmonyOS arm64。

## 安装

在鸿蒙原生 zsh 终端执行：

```zsh
curl -fsSL https://oheco.github.io/oheco-packages/install.sh | zsh
```

安装器依赖 `curl`、`tar`、`sha256sum`（或 `shasum`）及基础文件命令，不依赖 Go、Git
或 jq。安装成功后，脚本自动把 `~/.oheco/bin` 添加到 `${ZDOTDIR:-$HOME}/.zshrc` 的 PATH
配置；自定义 `OHECO_ROOT` 时写入对应的 `bin` 目录。保留原有配置，重复安装不重复写入。
新开的 zsh 终端自动生效；当前终端执行 `source "${ZDOTDIR:-$HOME}/.zshrc"` 即可使用 `oo`，
也可以按脚本输出直接设置 PATH。`curl | zsh` 的子进程无法直接修改当前终端的环境。
`OHECO_NO_MODIFY_PATH=1` 可关闭配置文件修改。已有工具不会因索引同步失败被删除。

## 使用

```zsh
oo update
oo search ohos-sdk
oo info ohos-sdk-toolchains
oo install ohos-sdk-toolchains
binary-sign-tool@26.0.0.35-Beta -h
oo list
oo switch ohos-sdk-toolchains 26.0.0.35-Beta
oo remove ohos-sdk-toolchains@26.0.0.35-Beta
oo install oheco
```

- `update` 只更新索引，失败保留旧索引；`search`、`info` 查询本地索引。
- `search` 以对齐表格显示名称、当前平台的最新版本、已安装版本、包大小、维护者和说明。
  已安装列只显示版本号最新的已装版本；装有多个版本时标注总数，例如 `0.3.1 (3)`，
  未安装显示 `-`。大小取对应下载包；维护者与 `list` 一致，优先显示姓名，没有姓名则显示 `@GitHub账号`。
  本地查询开始时并行检查云端索引；本地结果输出完后最多再等 500 毫秒，超时或网络失败
  不影响查询。发现索引内容更新时，在结果后询问是否更新，默认不更新；确认后保存已校验的
  索引并提示重新执行 `oo search`。重定向或管道输出只提示执行 `oo update`，不会等待输入。
  仅生成时间变化不会触发提示，过期的云端索引不会覆盖本地更新的索引。
- `install name` 安装该平台的 `latest`；`install name@version` 安装指定版本。
  默认启用本次安装的版本；`--no-switch` 只安装，保留当前启用状态。
  下载时显示进度、已下载/总大小和平均下载速度；终端中每 200 毫秒刷新同一行，
  重定向输出时每 5 秒记录一行。下载结束显示最终状态，命中有效缓存时跳过下载。
- `switch name version` 只切换已安装版本，一起处理包内全部命令。
- `remove name` 删除当前启用版本；`remove name@version` 删除指定版本；
  `remove name --all` 删除全部版本。删除启用版本后不自动选择其他版本。
- `list` 以名称、全部已装版本、维护者三列显示，每个包一行，用 `*` 标记启用版本。
  维护者优先显示本地索引中的姓名，没有姓名则显示 `@GitHub账号`；索引或对应包信息缺失时
  显示 `-`。切换、卸载和列出安装状态不需要网络或远端索引。
- `recover` 恢复中断事务。其他修改命令启动时也会自动恢复。
- `oheco` 自身作为普通包更新和切换；删除 `oheco` 的启用版本也会删除默认 `oo`
  链接，可通过保留的 `oo@版本` 或安装脚本恢复。

## 目录和配置

默认安装到 `~/.oheco`；`OHECO_ROOT` 可指定其他根目录。
`OHECO_INDEX_URL` 可覆盖索引地址。正式源必须使用 HTTPS；仅 loopback 允许 HTTP，
用于本地开发与集成测试。客户端按原生 `GOOS-GOARCH` 选择产物，不将 Linux 当成鸿蒙。

索引和软件包下载遵循 Go 标准库的代理环境变量：HTTPS 使用 `HTTPS_PROXY` 或
`https_proxy`，HTTP 使用 `HTTP_PROXY` 或 `http_proxy`，大写非空值优先；
`NO_PROXY` / `no_proxy` 指定直连目标，localhost 和 loopback 地址始终直连。
变量需要导出到 `oo` 进程，例如 `export https_proxy=http://127.0.0.1:7890`。
`ALL_PROXY` / `all_proxy` 不会自动读取。

```text
~/.oheco/
  packages/oheco/0.3.1/bin/oo
  packages/ohos-sdk-toolchains/26.0.0.35-Beta/lib/binary-sign-tool
  packages/ohos-sdk-toolchains/26.0.0.35-Beta/.oo-launchers/binary-sign-tool
  bin/oo@0.3.1 -> ../packages/oheco/0.3.1/bin/oo
  bin/oo -> oo@0.3.1
  bin/binary-sign-tool@26.0.0.35-Beta -> ../packages/ohos-sdk-toolchains/26.0.0.35-Beta/.oo-launchers/binary-sign-tool
  bin/binary-sign-tool -> binary-sign-tool@26.0.0.35-Beta
  index/index.json
  state/installed.json
  state/lock
  state/transaction.json       # 仅事务进行中存在
  cache/downloads/<sha256>.{tar.gz,zip}
  tmp/
```

本地安装记录保存每个版本的完整产物描述和命令映射。不同包提供相同命令、用户已有
同名文件、用户修改过的链接都会导致操作停止；不会覆盖这些文件。
下载必须通过大小和 SHA-256 校验；支持 `tar.gz` 和 ZIP，ZIP 额外验证条目 CRC。
解压保留执行权限，拒绝路径越界、硬链接、特殊文件、加密 ZIP 和重复条目。
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
`OHECO_VERSION` 默认 `0.3.1`。OHOS Go 工具链自动调用 PATH 中的 `binary-sign-tool`
签名，工具缺失时检查 LLVM 工具目录的 PATH。普通用户运行已签名的 `oo` 无需编译工具。

Linux 侧打包，不改变已签名二进制内容：

```sh
python3 scripts/package.py --version 0.3.1
```

输出 `dist/oheco-0.3.1-ohos-arm64.tar.gz` 和 `.sha256`。同名文件不会被覆盖。
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
服务管理留待后续版本。Go 适配尚未完成，当前未收录到软件索引。

## ZIP、启动器与 SDK

ZIP 安装使用 Go 标准库，无需外部解压程序。已在鸿蒙验证系统 `zip` 3.0 / `unzip` 6.0，
以及解压后的签名程序执行。启动器依赖 `/bin/sh` 和支持 `-f` 的 `readlink`，宿主均已验证。

ZIP 和 tar.gz 解压默认处理普通文件的大小写冲突：同一目录中仅文件名大小写不同的条目，
保留全小写文件名对应的原始内容与执行权限，不受压缩包条目顺序或目标文件系统影响。
没有冲突的文件保持原名；冲突中没有全小写候选、涉及目录或软链接时仍拒绝安装。
完全同名的重复条目仍报错，被舍弃的文件也必须通过大小及 ZIP CRC 校验。

包描述可添加 `launchers: ["ld.lld", "lld-link"]`。列表中的名称必须出现在 `binaries` 中。
`oo` 在版本目录的 `.oo-launchers/` 中生成固定脚本，版本链接指向该脚本；脚本解析自身
真实位置，调用原名二进制并原样传递参数，因此 `ld.lld@版本` 保持 LLD 的 Unix 链接器模式。
启动器随安装事务提交、随版本卸载，支持离线切换和根目录迁移；安装或切换拒绝被修改的启动器。
`.oo-launchers/` 是保留目录，软件包不能自行携带或引用它。包内安装脚本不会自动执行。

SDK 按组件收录为 `ohos-sdk-native`、`ohos-sdk-toolchains`、`ohos-sdk-ets`、`ohos-sdk-js`、
`ohos-sdk-previewer`，版本统一为 `26.0.0.35-Beta`。Previewer 当前只有元数据，允许
`binaries: {}`。ETS/JS 暂按资源包安装，内置工具尚未验证在主目录中可执行，暂不创建命令链接。
当前不支持 LLDB，也不创建 lldb-server 等相关链接。

## 从 0.1.0 升级

```zsh
oo update
oo install oheco
oo update
```

新版默认读取 `index/v2/index.json`，并兼容已有 v1 本地索引和安装记录。发布站同时保留
`index/v1/index.json`，只包含 v1 兼容包，包括最新的 oheco 自举包，让旧客户端完成升级。
安装了 ZIP 或启动器包后应继续使用 0.2.0 或更新的客户端管理它们。

SDK 原生验证脚本为 `scripts/test-sdk.zsh`，使用已校验的原始 ZIP 缓存，在隔离目录中测试
编译、签名、版本链接及离线卸载。隔离目录默认位于主目录，可通过 `OHECO_SDK_TEST_PARENT`
调整位置；同时核对 8 个小写头文件的名称和原包哈希。`scripts/test-native.zsh` 验证安装脚本、
PATH 和旧客户端升级。

SDK 的 native 原始 ZIP 含 8 对仅大小写不同且内容不同的 Linux netfilter 头文件。
安装时按上述规则保留 8 个小写版本，舍弃对应的大写变体，以兼容鸿蒙主目录文件系统。
因此需要大写变体中定义的代码仍需另行适配；Release ZIP 及其校验值保持不变。
在含空格的安装路径下，可直接使用 `clang --target=aarch64-linux-ohos --sysroot=<native版本目录>/sysroot`；
原包提供的 target shell 包装脚本没有完整引用路径，不适用于含空格的目录。
