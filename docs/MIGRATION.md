# 官方 command-line-tools 与旧安装迁移

`oo 0.11.0` 使用 v6 索引，支持只作用于启动器子进程的包内环境。
`command-line-tools@26.0.0.201` 原样保留官方归档及 SDK 目录，提供 65 个旧 SDK 命令以及
`hvigor`、`hvigorw`、`ohpm`。`js_rawheap_translator` 已退出。工具链的 Node、npm、npx、corepack
不创建全局命令链接；Hvigor/OHPM 使用包内 Node，全局命令仍由 `nodejs` 提供。

## 升级与新安装

```sh
oo update
oo install oheco
oo update
oo install command-line-tools
hvigor --version --no-daemon
ohpm --version
```

若显式设置了 `OHECO_INDEX_URL`，请将官方 v5 地址改成
`https://oheco.org/index/v6/index.json`；oo 不改写用户显式指定的源。

## 保留旧 SDK 文件，释放命令

旧 SDK 与新包会提供同名命令。先用 `oo list` 核对安装情况，仅对已安装的旧包执行：

```sh
oo unlink ohos-sdk-native ohos-sdk-toolchains
```

`unlink` 只移除这些包的默认和带版本命令链接；保留每个版本的原始收据、文件、active、
automatic 和依赖信息。不会复制、改写或删除旧 `sdk/` 视图。ETS、JS、previewer 原本没有
命令，不需要 unlink。该操作独立于安装新包，也不卸载旧包。

之后安装 `command-line-tools`。要恢复旧命令，先 `oo unlink command-line-tools`，再
`oo switch <旧包> <已装版本>`。仍有其他包占用命令或链接被人工修改时会报错，不覆盖。
`oo list` 会标明命令已 unlink；版本文件仍可通过明确的实际路径使用。

## 旧 npm Hvigor

旧 `@oheco/hvigor` 由 npm 管理，不在原生收据中。先核对其安装作用域和 npm prefix；
若此前通过 oo 默认全局安装，使用：

```sh
oo remove npm:@oheco/hvigor -y
```

若安装在自定义 prefix、项目或其他 Node 版本中，用同一作用域的 npm 移除，或在 oo 的
`--` 后显式指定对应 prefix。oo 不改动用户 npm 配置，不覆盖 unmanaged `hvigor` 文件。
移除会卸载旧适配器；需保留它的工程应先保存依赖锁定记录与对应安装目录，或使用独立根。

## 旧 SDK 视图与 Godot

`oo sdk` 及其创建、查询、移除、卸载保护钩子已经删除。升级不会删除旧组件、视图或用户
项目配置；旧视图只是指向精确 SDK 版本的软链接，保留组件文件时可继续由明确路径使用。

旧视图不再参与卸载保护。显式 `oo remove`、`--all`、`--cascade`、`--autoremove` 或已提交
卸载事务的恢复可能删除它们引用的组件。继续使用旧工程时保留这些版本，不执行其卸载；
不能只保留 `sdk/` 目录而删除源包，也不能仅搬走视图而忽略其相对链接。

Godot Editor 本次不迁移；旧工程的 SDK/适配器配置需单独评估，不自动改向新工具。

## 状态兼容

原有 schema 1/2 状态仍可读取，普通升级不改写旧 SDK 收据。安装带 `launcher_options` 的包
或执行 `unlink` 后，状态升为 schema 3；先升级当前 active oo 到 0.11.0。
旧版本不能管理这个根，新客户端会阻止切换到不支持当前状态的 active oo。
状态备份仅供核对，不可直接覆盖新状态来降级，包文件与链接可能已经变化。
