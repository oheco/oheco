# oheco · oo

鸿蒙原生软件包管理器，使用 Go 编写，二进制名为 `oo`。支持 HarmonyOS arm64。

> **v0.8.0** 改变了语言包的下载模型：oo 只提供元数据——目录里的适配包直连其发布地址
> （npm 用 `integrity`、pip 用 `#sha256=` 校验），其余包名转发到用户自己配置的源。oo
> **不再代理下载、不再校验第三方字节**，也不再屏蔽用户的 npmrc / pip.conf。详见下文
> “Python 与 Node.js 包”。
> npm/pip 继续管理各自的依赖与安装状态；oheco 查询中的管理器标记不表示已经安装。
> 原生测试及隔离 CLI 验收见 `scripts/test-dependencies-native.py`。安装脚本取得正式源已发布版本。
> 历史版本说明见 [0.7.1 验证说明](docs/validation-0.7.1.md)。

## 安装

在鸿蒙原生 zsh 终端执行：

```zsh
curl -fsSL https://oheco.org/install.sh | zsh
```

安装器依赖 `curl`、`tar`、`sha256sum`（或 `shasum`）及基础文件命令，不依赖 Go、Git
或 jq。安装成功后，脚本自动把 `~/.oheco/bin` 添加到 `${ZDOTDIR:-$HOME}/.zshrc` 的 PATH
配置；自定义 `OHECO_ROOT` 时写入对应的 `bin` 目录。保留原有配置，重复安装不重复写入。
新开的 zsh 终端自动生效；当前终端执行 `source "${ZDOTDIR:-$HOME}/.zshrc"` 即可使用 `oo`，
也可以按脚本输出直接设置 PATH。`curl | zsh` 的子进程无法直接修改当前终端的环境。
`OHECO_NO_MODIFY_PATH=1` 可关闭配置文件修改。已有工具不会因索引同步失败被删除。

使用自定义安装目录时，先在 `${ZDOTDIR:-$HOME}/.zshrc` 中添加以下配置，将示例目录
替换为实际安装目录的绝对路径：

```zsh
export OHECO_ROOT="$HOME/tools/oheco"
```

执行 `source "${ZDOTDIR:-$HOME}/.zshrc"` 加载配置后，再运行上面的安装命令。
安装器只自动配置 PATH，后续终端必须继续导出相同的 `OHECO_ROOT`；未设置时，
`oo` 使用默认的 `~/.oheco`，不会根据可执行文件所在位置推断安装目录。

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
- 每次执行命令都会尝试启动独立的后台索引更新进程，包括 `help`、`--version`。
  同一 `OHECO_ROOT` 最多一个更新进程，前台不等待，也不显示后台输出；发现更新时自动
  下载、校验并原子替换本地索引，后续命令使用更新后的索引，不升级已安装的软件。
  后台检查间隔至少 30 分钟；网络失败也会等待这个间隔再重试，单次后台任务最多运行 30 秒。
  显式执行 `oo update` 会等待已有检查结束并立即检查，不受 30 分钟间隔限制。
  请求优先使用 `ETag`，没有 `ETag` 时使用 `Last-Modified`；服务器返回 `304` 时不下载
  索引正文。首次检查、源地址变化、本地索引损坏或服务器没有缓存标识时会下载完整索引。
  设置 `OHECO_NO_AUTO_UPDATE=1` 可关闭自动检查，`oo update` 仍可手动执行。
- `search` 查询所有目录管理器（原生、npm、pip），以对齐表格显示名称、管理器、当前平台的最新版本、
  已安装版本、包大小、维护者和说明。原生包已安装列只显示版本号最新的已装版本；多个版本
  标注总数，例如 `0.4.0 (3)`，未安装显示 `-`。外部包显示 `<由npm管理>` 或 `<由pip管理>`，
  **仅表示管理归属，不断言已经安装**。大小取对应下载包；维护者与 `list` 一致，优先显示
  姓名，没有姓名则显示 `@GitHub账号`。查询只读取本地索引，不等待后台检查，也不询问是否更新。
  后台收到过期的同源索引时，会保留较新的本地索引。
- `install name` 安装原生包该平台的 `latest`；`install name@version` 安装指定版本。
  原生包默认启用本次安装的版本；`--no-switch` 只安装，保留当前启用状态。
  v0.7.0 的统一 `install` / `remove` 入口支持多个目标，并按目录包的管理器分派；
  外部包默认全局范围，作用域和参数规则见下方“Python 与 Node.js 包”。
  原生包下载时显示进度、已下载/总大小和平均下载速度；终端中每 200 毫秒刷新同一行，
  重定向输出时每 5 秒记录一行。下载结束显示最终状态，命中有效缓存时跳过下载。
- `switch name version` 只切换已安装的原生版本，一起处理包内全部命令。
- 原生 `remove name` 删除当前启用版本；`remove name@version` 删除指定版本；
  `remove name --all` 删除全部版本。删除启用版本后不自动选择其他版本。
  v0.7.0 原生依赖管理提供 `--autoremove` 清理不再需要的自动依赖、`--cascade` 连同
  反向依赖一起卸载；两者仅限原生包。`-y` / `--yes` 接受确认，**不隐含任何依赖清理**。
  安装/卸载可用 `--dry-run` 查看计划，不执行修改。
- `list` 仅列原生安装记录，以名称、管理器、全部已装版本、维护者四列显示，每个包一行，用 `*`
  标记启用版本；**不盘点 npm/pip 安装**。维护者优先显示本地索引中的姓名，没有姓名则显示
  `@GitHub账号`；索引或对应包信息缺失时显示 `-`。原生切换、卸载和列出安装状态不需要网络或远端索引。
- `recover` 恢复中断事务。安装、切换、卸载命令启动时也会自动恢复。
- `oheco` 自身作为普通包更新和切换；删除 `oheco` 的启用版本也会删除默认 `oo`
  链接，可通过保留的 `oo@版本` 或安装脚本恢复。

`oo` 安装预编译原生产物，不执行原生包内安装脚本；不提供源码构建、自动升级或服务管理。
v0.7.0 的原生依赖使用下述 schema v5；npm/pip 依赖仍由相应后端实时解析。
升级软件时先执行 `oo update`，再执行 `oo install <包名>`；
升级包管理器使用 `oo update && oo install oheco`。

软件目录已收录 Go 原生工具链，可通过 `oo install go` 安装。当前适配包版本为
`1.27.1-ohos.1`；签名工具（`ohos-sdk-toolchains` 包）、cgo 依赖及系统配置见
[Go 适配说明](https://github.com/oheco/go/blob/go1.27.1-ohos.1/misc/harmony/README.md)。

## v0.7.0：逐版本原生依赖

schema v5 在 `versions[].dependencies` 声明依赖，对应
`Version.Dependencies []Dependency`，字段为 `name`、`constraint`、可选 `version_basis`
和 `platforms`。例如某个原生版本的字段：

```json
{
  "dependencies": [
    {
      "name": "go",
      "constraint": ">=1.27, <1.28",
      "version_basis": "upstream",
      "platforms": ["ohos-arm64"]
    }
  ]
}
```

- 依赖声明属于具体版本，不是整个包；`platforms` 可限定生效平台。不指定 `version_basis`
  时按 `package`（包版本）比较；`upstream` 要求目标版本显式声明 `upstream_version`，
  按该字段比较，**不回退到包版本**。
- 依赖来源和目标都必须是可安装的原生包版本；仅提供项目的版本不能声明或充当原生依赖。
  npm/pip 包不使用此原生依赖图，其依赖交给后端，不向原生安装状态镜像外部安装记录。
- 约束支持 `=` / `==` / `!=` / `>` / `>=` / `<` / `<=` / `*`，裸版本表示精确匹配。
  空白、逗号、`AND`、`&&` 表示 AND；`OR`、`||` 表示 OR，AND 优先。
  不支持 `^`、`~`、括号、`1.*` 等局部通配符，也不把所有版本强制视为 SemVer。
- 数字多段版本比较忽略数字补零和末尾零段（如 `01.2.0 = 1.2`）；支持 tmux 风格
  `3.5 < 3.5a < 3.5b`，预发布排在正式版之前。末尾 `-ohos.N` 在基础版本之后按数字
  修订号比较，未声明修订号按 `0` 处理。无法识别结构的 opaque 版本只允许精确匹配、
  `!=` 和 `*`；使用范围比较会报错，不按字符串猜测顺序。

原生解析器以所选平台和已安装/启用状态检查依赖兼容性，再生成安装或卸载计划。
`--no-switch` 保留同名包的当前启用版本，可以存储带自身依赖的新版本；其依赖仍需与共享运行环境兼容。
外部 pip/npm 的依赖解析是运行时行为，不承诺为保留的 pip 依赖提供跨 `OHECO_ROOT`
的反向依赖保护；删除 Python 顶层包不自动删除其依赖。

## 目录和配置

默认安装到 `~/.oheco`；`OHECO_ROOT` 可指定其他根目录。
`OHECO_INDEX_URL` 可覆盖索引地址。正式源必须使用 HTTPS；仅 loopback 允许 HTTP，
用于本地开发与集成测试。客户端按原生 `GOOS-GOARCH` 选择产物，不将 Linux 当成鸿蒙。

索引和软件包下载遵循 Go 标准库的代理环境变量：HTTPS 使用 `HTTPS_PROXY` 或
`https_proxy`，HTTP 使用 `HTTP_PROXY` 或 `http_proxy`，大写非空值优先；
`NO_PROXY` / `no_proxy` 指定直连目标，localhost 和 loopback 地址始终直连。
变量需要导出到 `oo` 进程，例如 `export https_proxy=http://127.0.0.1:7890`。
`ALL_PROXY` / `all_proxy` 不会自动读取。
后台进程继承当前命令的代理环境。检查记录保存在 `index/http.json`，包含缓存标识、
上次检查时间和最近错误；后台失败不影响前台命令，手动 `oo update` 会直接报告错误。

```text
~/.oheco/
  packages/oheco/0.4.0/bin/oo
  packages/ohos-sdk-toolchains/26.0.0.35-Beta/lib/binary-sign-tool
  packages/ohos-sdk-toolchains/26.0.0.35-Beta/.oo-launchers/binary-sign-tool
  bin/oo@0.4.0 -> ../packages/oheco/0.4.0/bin/oo
  bin/oo -> oo@0.4.0
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

修改操作使用进程锁和持久化事务记录。原生批次的所有新包先完成暂存与校验，再共同提交；
事务记录暂存目录的设备号与 inode，回滚只删除本事务实际移入的目录，不删除尚未接管或被替换的目标目录。
缺少目录身份的旧版未提交日志会保留新增目录并提示人工核对，不冒险删除；已提交操作继续完成清理。
单条链接通过重命名替换；多个命令的链接逐条切换，不承诺对外同时变化，也不承诺底层
文件系统不支持的断电持久化。确认提示支持 SIGINT/SIGTERM 取消，取消后不提交卸载。
请在鸿蒙上执行安装，以保留所需的执行权限。
下载缓存不会随卸载删除，可手动删除 `cache/downloads/` 中的缓存文件。

## 原生构建

使用已经移植的 OHOS Go 工具链；官方 Go 发行版尚不支持 `GOOS=ohos`。
可通过 `oo install go` 和 `oo install ohos-sdk-toolchains` 安装 Go 和签名工具。
在 HarmonyOS 原生 zsh 终端中进入本仓库，将以下示例路径替换为实际源码目录和
当前应用的私有可写临时目录，并确认 PATH 中的 `go` 为 OHOS 原生工具链：

```zsh
cd /path/to/oheco
export TMPDIR="/path/to/app-private/go-tmp"
mkdir -p "$TMPDIR"
go env GOHOSTOS GOHOSTARCH
command -v binary-sign-tool
OHECO_GO="$(command -v go)" sh scripts/build-ohos.sh
build/oo --version
```

`go env GOHOSTOS GOHOSTARCH` 应输出 `ohos` 和 `arm64`。也可以将 `OHECO_GO` 设置为
OHOS Go 可执行文件的绝对路径；未设置时，构建脚本默认使用相邻 `go/bin/go`。
`OHECO_VERSION` 默认 `0.8.0`。OHOS Go 工具链自动调用 PATH 中的 `binary-sign-tool`（由
`ohos-sdk-toolchains` 包提供）签名，工具缺失时检查 LLVM 工具目录的 PATH。普通用户运行已签名的 `oo` 无需编译工具。

打包不改变已签名二进制内容：

```sh
python3 scripts/package.py --version 0.8.0
```

`--version` 省略时同样默认 `0.8.0`。输出 `dist/oheco-0.8.0-ohos-arm64.tar.gz` 和 `.sha256`。
同名文件不会被覆盖；本地打包不代表该版本已发布。包内包含 `bin/oo`、README 和 MIT 许可证。

## 测试与索引生成

常规 Go 环境可运行核心逻辑测试：

```sh
go test ./...
go vet ./...
```

在鸿蒙上使用 OHOS Go 重复执行同一测试套件，临时目录使用应用私有可写目录。
测试包括多版本和多命令切换、离线卸载、损坏下载、索引失败保留、命令冲突、
安全解压、进程锁和中断恢复。

相邻检出 `oheco-packages` 后生成网站（`go` 为开发环境可运行的 Go）：

```sh
go run ./cmd/oo-index
go run ./cmd/oo-index --verify-artifacts
```

后一个命令会下载验证全部远端软件包，适用于发布。安装脚本从本仓库模板和
`oheco-packages` 中的 `oheco.json` 生成，不需要独立维护版本或哈希。

## ZIP、启动器与 SDK

ZIP 安装使用 Go 标准库，无需外部解压程序。已在鸿蒙验证系统 `zip` 3.0 / `unzip` 6.0，
以及解压后的签名程序执行。启动器依赖 `/bin/sh` 和支持 `-f` 的 `readlink`，均已在鸿蒙上验证。

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
`binaries: {}`。ETS/JS 作为开发资源包提供，不创建编译器命令链接；完整应用构建
需另行配置相应运行环境，内置编译工具的执行验证情况见 [验证记录](docs/VALIDATION.md)。
当前不支持 LLDB，也不创建 lldb-server 等相关链接。

## 索引兼容与升级

升级已发布版本仍使用：

```zsh
oo update
oo install oheco
oo update
```

自 v0.7.1 起默认索引为 `https://oheco.org/index/v5/index.json`，直接访问新域名，
不再依赖旧 GitHub Pages 地址跳转。显式设置的 `OHECO_INDEX_URL` 仍优先，升级不会擅自
覆盖用户自定义源；若曾在 shell 配置中设置旧官网地址，请按需改成新 HTTPS 地址。
索引源改变时，客户端自动重新验证索引缓存，不需要删除安装记录或重新安装已有软件。

域名迁移不改变索引 schema v5 和安装状态 schema 2。仍保留 v1–v4 索引；生成的
`index/v1/index.json` 至 `index/v4/index.json` 采用 fail-closed 策略：**移除整个 schema v5 包**，
不能只删掉依赖字段而让旧客户端不安全地安装。`oheco` 自举包继续使用 schema v1，
旧客户端可通过旧地址的 HTTPS 重定向取得新版；无法使用旧跳转时，0.6.x 可临时指定
`https://oheco.org/index/v4/index.json`，0.7.x 可指定新域名的 v5 索引，再升级客户端。
不能将只支持旧 schema 的客户端直接指向 v5。已发布归档、GitHub Release 下载地址及
历史验证记录保持不变；官网迁移不意味着软件产物也移出 GitHub。
安装了 ZIP 或启动器包后应继续使用 0.2.0 或更新的客户端管理它们。

v0.7.0 的原生状态迁移写入 **schema 2**，与目录索引的 schema v5 不同；迁移前的记录保存在
`state/installed.v1.backup.json`。迁移后，**旧客户端不能再管理同一个 `OHECO_ROOT`**。
备份仅供核对和手工恢复参考，不是安全的直接降级办法：后续安装、切换或卸载会改变包目录和
链接，不能把旧备份直接覆盖回 `state/installed.json`；测试旧客户端应使用独立根目录。

SDK 原生验证脚本为 `scripts/test-sdk.zsh`，使用已校验的原始 ZIP 缓存，在隔离目录中测试
编译、签名、版本链接及离线卸载。隔离目录默认位于主目录，可通过 `OHECO_SDK_TEST_PARENT`
调整位置；同时核对 8 个小写头文件的名称和原包哈希。`scripts/test-native.zsh` 验证安装脚本、
PATH 和旧客户端升级。

SDK 的 native 原始 ZIP 含 8 对仅大小写不同且内容不同的 Linux netfilter 头文件。
安装时按上述规则保留 8 个小写版本，舍弃对应的大写变体，以兼容鸿蒙主目录文件系统。
因此需要大写变体中定义的代码仍需另行适配；Release ZIP 及其校验值保持不变。
在含空格的安装路径下，可直接使用 `clang --target=aarch64-linux-ohos --sysroot=<native版本目录>/sysroot`；
原包提供的 target shell 包装脚本没有完整引用路径，不适用于含空格的目录。

## Python 与 Node.js 包

0.5.0 增加 schema v3。包级 `package_manager` 选择 `oheco`、`pip` 或 `npm`，
未设置时仍按原生包处理。`name` 是目录名称，语言包另设 `package_name`，例如
`deepseek-harness` 对应 `@deepseek-ai/dsh`。原生版本保留 `artifacts`；Python 版本只使用
`pip_artifacts` 数组；Node.js 版本只使用 `npm_artifacts` 对象。这三种安装产物不能混用；v4 的可选 `projects` 另见下方项目导出说明。

```zsh
oo update
oo install <目录包名>
oo install npm:<npm包名>             # 统一入口默认全局
oo install pip:<Python包名>          # 使用选定的基础 Python 环境
oo install npm:<npm包名> -- --global=false --prefix ./project  # 显式本地范围
oo install npm:<npm包名> -- --prefix "$HOME/.local"            # 独立目录，仍为全局
oo remove npm:<npm包名>
oo remove pip:<Python包名>
# 保留旧入口兼容：
oo npm install <npm包名>             # 旧 oo npm 仍默认当前项目
oo npm install <npm包名> --global
oo pip install <Python包名>
```

统一 `oo install` / `oo remove` 按目录记录选择管理器；`npm:<name>` / `pip:<name>`
可显式选择生态包。统一入口**默认全局范围**：npm 使用全局目录，pip 使用选定的基础 Python
环境。作用域参数必须放在 `--` 分隔符之后，不能直接当作 oo 顶层选项。
`--prefix DIR` **只选择目录，不隐含 local**；统一安装/卸载即便带 prefix 仍默认 global。
只有显式 `--global=false`、`--no-global`、`--local` 或 `--location=project` 才选择本地范围；
`-g` / `--global` 显式选择全局。旧 `oo npm` 保留 npm-local 兼容默认值，不随统一入口
改为全局。安装和卸载时应选择相同的作用域和目录。

`--` 后的参数仅允许用于**单一外部管理器**，不用于原生包或混合管理器操作，作为参数数组传递，
不经 shell 求值。`--registry`、额外索引、`find-links`、启用安装脚本等**源与脚本**类参数仍被
拒绝：oo 必须自己决定源，否则同名上游包会顶替目录里的适配包。`--autoremove` / `--cascade`
仅用于原生依赖管理；`-y` 只接受确认，不要求 pip/npm 清理依赖。

语言包安装由所选 Python 的 pip 或 PATH 中的 npm 执行。Python 默认使用 PATH 中的
`python3`，可通过 `OHECO_PYTHON` 选择解释器；npm 的 `--prefix` 可选择独立目录。安装库时，
应选择实际使用它的 Python 环境或 Node.js 项目。全局 npm 库不会自动成为其他项目的依赖。

`oo` 在安装期间启动一个带随机路径的 loopback HTTP 源，但**只提供元数据**，不再代理下载：

- **npm**：目录里收录的适配包由 oo 返回自己的元数据，`dist.tarball` 直接指向描述里的原始
  地址（通常是 GitHub Releases），并带 `integrity` 由 npm 自己校验；**未收录的包名一律 302
  转发**到用户 npmrc 里配置的源，包括 `@scope:registry` 形式的 scope 级源。
- **pip**：oo 是**唯一索引**。目录包只返回适配 wheel 的链接和 `#sha256=`；**未收录的包名**
  由 oo 聚合用户配置的索引型源（`index-url` + `extra-index-url`）后转发，PEP 691 JSON 与
  PEP 503 HTML 两种协议都支持，相对链接会解析成绝对地址，哈希和 `requires-python` 原样透传。

因此 **oo 不再下载、缓存或校验第三方字节**：第三方包的安全性由 npm/pip 自身的完整性校验和
用户选择的源决定。目录包的 `integrity` / `#sha256=` 仍来自目录描述，所以适配产物本身仍有
完整性保护。`find-links` 不是索引型源，无法在“一个包名一个源”的前提下安全聚合，oo 会明确
报错而不是静默忽略。

用户的包管理器配置会被正常加载：`~/.npmrc`、项目 `.npmrc`、`NPM_CONFIG_*`、`pip.conf`、
`PIP_*` 环境变量以及代理设置都会生效（oo 只把 loopback 加进 `NO_PROXY`，只剥离能在工具内部
执行代码的 `NODE_OPTIONS`）。oo 只强制两件与校验无关的事：npm 禁用安装脚本
（`--ignore-scripts`），pip 只安装 wheel（`--only-binary=:all:`）。目录里的 scoped 适配包由 oo
在命令行上强制指向临时源，保证装到适配版本而不是同名的上游版本。

oo 只转发元数据、**不经手凭据**：若用户的源配置了 token 或 URL 里的用户名密码，这些请求会
以匿名身份发出，oo 会提前提示；需要认证的源可能返回 401/403。

Python 只安装 wheel；需要原生代码的包必须预先提供兼容 wheel。npm 禁用生命周期脚本，适配包
必须包含可直接使用的预编译产物。直接 URL、Git、本地路径依赖及 npm workspace 安装仍不支持。
项目 `.npmrc` 里能在 npm 内执行代码的设置（`node-options`、`script-shell`、`onload-script`）
会被拒绝；其他源与代理设置会被尊重。

npm 仍使用 `--omit-lockfile-registry-resolved`，锁文件保留版本和完整性信息、省略下载地址。
锁文件里已有的 `resolved` 现在接受任意 HTTPS 源，但 http 或 loopback 地址会被拒绝，需要重新
生成锁文件。用户的 pip/npm 源配置不会被修改。中断时 oo 终止自己的包管理器进程组并关闭服务；
安装目录遵循 pip/npm 的中断恢复行为，需要时重跑安装。

语言环境及其依赖由 pip/npm 实时解析和管理，不镜像为 oo 的原生安装记录。使用 `oo pip list`、
`oo npm list` 查询相应环境，使用统一 `oo remove` 或旧 `oo pip uninstall <包名>`、
`oo npm uninstall <包名>` 卸载，并注意全局/本地作用域。pip 会保留被卸载包的依赖；
外部后端不保证跨根目录的反向依赖保护。`oo list` 不盘点这些外部安装，`oo switch`、
`--no-switch` 和 `命令@版本` 链接只适用于原生包；语言包通过安装所需版本切换。

维护者可从最终 wheel/tarball 生成对应字段，避免手写依赖信息：

```sh
go run ./cmd/oo-index --inspect package.whl --package-manager pip --url https://example.com/package.whl
go run ./cmd/oo-index --inspect package.tgz --package-manager npm --url https://example.com/package.tgz
```

输出分别放入 `pip_artifacts` 数组或 `npm_artifacts` 对象。npm 保留归档内完整
`package.json`；wheel 保留文件名、`Requires-Python` 和 `Requires-Dist`。发布检查
`--verify-artifacts` 会重新下载并核对这些信息。协议测试在安装了 pip/npm 时执行真实的依赖
安装、wheel 平台标签选择、目录包元数据（`integrity` / `#sha256=`）校验、非目录包转发与聚合，
以及卸载。

鸿蒙交互式终端中的代理验收可运行（代理按当前环境填写，本机已用 `127.0.0.1:10808`）：

```zsh
python3 scripts/test-language-native.py --proxy socks5://127.0.0.1:10808 \
  --tmp-parent /path/to/app-private/tests
```

脚本在独立目录中访问官方源，验证 `is-number@7.0.0` 和 `idna==3.10`，结束后清理。
检出目录若触发 Git 所有权检查，构建时可仅给该进程设置
`GIT_CONFIG_COUNT=1`、`GIT_CONFIG_KEY_0=safe.directory` 和
`GIT_CONFIG_VALUE_0=<源码绝对路径>`，无需修改全局 Git 配置。

## DevEco 项目导出

0.6.0 增加 schema v4 和 `oo export`。每个版本可以在 `artifacts` 之外提供
`projects`，也可以只提供项目。项目是可编辑的 DevEco 工程归档，可包含 ArkTS/C++
外壳、编译好的 `.so`、运行资源和离线依赖；用户导出后用 DevEco 自行构建、签名和安装。

```zsh
oo update
oo info godot-editor
oo export godot-editor --output ./GodotEditor
# 指定版本和项目名：
oo export godot-editor@4.7.2-ohos.1 editor -o './Godot Editor'
# 导出到当前目录：
mkdir my-editor
cd my-editor
oo export godot-editor
```

`oo export <包名[@版本]> [项目名] [-o|--output 目录]`：只有一个项目时可省略项目名；
多个项目时列出名称并要求选择。目标目录默认当前目录，缺失的目录会创建。
版本默认取当前平台的 `latest`；在其他平台导出时，如果所有 `latest` 条目指向
同一版本，也可直接使用默认版本；若它们不同，需要显式指定 `@版本`。

导出沿用代理、进度、大小/SHA-256 校验和下载缓存。完整解压验证后才写入目标目录；
现有顶层文件或目录重名时拒绝导出，不合并已有目录，其他文件保持原样。文件使用排他创建，
不覆盖已有内容。Ctrl+C 取消时清理本次新建的输出；强制终止或断电可能留下部分导出内容，
可保留所需文件并选择新目录重试。ZIP 和 tar.gz 都支持，保留执行权限及有效的包内相对软链接。
项目中的大小写冲突直接报错，不使用 SDK 安装时保留小写变体的规则，以免丢失源码。

导出不执行项目代码，不调用 DevEco 或签名工具，也不创建安装记录或命令链接。
导出的目录由用户维护，`oo remove` 不管理它；下载缓存仍保留在 `OHECO_ROOT` 内。
只提供项目的版本使用 `oo install` 时会提示改用 `oo export`。

`projects` 是项目名到归档描述的映射，字段包括 `url`、`sha256`、`size`、`format`、
`strip_components` 和可选 `description`，不声明 `binaries` 或 `launchers`。
`strip_components: 1` 表示移除归档的一层根目录，直接把工程内容写入指定目录。
v4 的 pip/npm 版本也可额外提供项目，其语言产物仍按原有规则安装。
`oo search`、`oo info` 和软件目录网站会显示项目及导出入口。

首个项目包是实验性的 `godot-editor`；应用验收范围与未验证项见其 `oo info`、
项目内 `VALIDATION.md` 和 Release。包管理器成功导出不代表 HAP 已完成运行验收。
