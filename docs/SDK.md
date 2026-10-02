# 多版本 OpenHarmony SDK 视图

SDK 视图是 `OHECO_ROOT` 内的持久交付物，不是 SDK 副本，也不是临时目录。
它只包含真实目录、管理清单和五个组件的顶层**相对软链接**。不会下载组件、
修改原始 `oh-uni-package.json`、修改 `es2abc`、重新签名或修改用户现有 SDK。

## 创建与选择

先通过普通包安装流程安装以下五个**同一精确包版本**：

- `ohos-sdk-native`
- `ohos-sdk-ets`
- `ohos-sdk-js`
- `ohos-sdk-toolchains`
- `ohos-sdk-previewer`

例如 CLI 接入后：

```sh
oo sdk create 26.0.0.35-Beta
oo sdk list
oo sdk path 26.0.0.35.Beta
oo sdk remove 26.0.0.35.Beta
```

`create` 只读取 `LoadState` 中确实安装的指定版本及其原始组件元数据；
不访问在线索引，不查询 `latest`，不使用 `active`，不扫描目录拼凑版本。
缺包错误一次列出所有缺少的精确组件版本；目录存在但没有安装收据也算缺包。
`previewer` 必须来自真实已安装包，不能由管理器合成。上游某些版本的真实
previewer 包只有元数据和 NOTICE：这仍是合法组件，但不代表提供预览器程序。

以上 Beta 示例布局：

```text
OHECO_ROOT/
  packages/
    ohos-sdk-native/26.0.0.35-Beta/oh-uni-package.json
    ohos-sdk-ets/26.0.0.35-Beta/oh-uni-package.json
    ohos-sdk-js/26.0.0.35-Beta/oh-uni-package.json
    ohos-sdk-toolchains/26.0.0.35-Beta/oh-uni-package.json
    ohos-sdk-previewer/26.0.0.35-Beta/oh-uni-package.json
  sdk/
    26.0.0.35.Beta/
      view.json
      root/
        26.0.0/
          native -> ../../../../packages/ohos-sdk-native/26.0.0.35-Beta
          ets -> ../../../../packages/ohos-sdk-ets/26.0.0.35-Beta
          js -> ../../../../packages/ohos-sdk-js/26.0.0.35-Beta
          toolchains -> ../../../../packages/ohos-sdk-toolchains/26.0.0.35-Beta
          previewer -> ../../../../packages/ohos-sdk-previewer/26.0.0.35-Beta
```

`SDKPath` / `oo sdk path` 返回的是 `sdk/<view-id>/root`，供 SDKmanager/Hvigor
配置使用，而不是内层 `root/26.0.0`。切换任意包的 active 版本不会改变视图。
含空格的 `OHECO_ROOT` 也使用相对链接，整体移动根目录不需要重写链接。

## 元数据身份与目录规则

原始 JSON 文件只读。允许上游扩展字段，拒绝重复 JSON 键及多余 JSON 内容。
五个组件的 `path` 必须分别精确等于 `native`、`ets`、`js`、`toolchains`、
`previewer`；不接受路径、绝对路径或大小写变体。`apiVersion` 接受正整数或规范的十进制数字字符串（官方 SDK26 使用 `"26"`），内部规范化为整数但不改原始 JSON，
`version`、非空 `platformVersion`、非空 `fullApiVersion` 只接受数字点分版本，
防止它们成为路径穿越入口。

五个组件的 `apiVersion`、`platformVersion`、`fullApiVersion`、`releaseType`、
`version` 必须一致，不允许混合组件。内层目录遵循 SDKmanager 选择规则：

- API **>= 26**：必须有非空 `platformVersion`，内层目录取它；不会回退。
- API **< 26**：优先取非空 `fullApiVersion`（支持字符串或正整数），否则取
  十进制 `apiVersion`。已有 `platformVersion` 不用于该范围的内层目录。

外层视图 ID 来自元数据，不重命名实际包版本：

- `releaseType == "Beta"`：`<metadata.version>.Beta`。
- `releaseType == "Stable"` 或 `"Release"`：`<metadata.version>`，不加后缀。
- 其他 releaseType / 非规范大小写拒绝，避免含糊的目录命名。

`Stable` 与 `Release` 的目录规则相同，但清单保留真实 releaseType。二者、
不同适配包版本或不同内容如果映射到同一 ID，清单不一致就拒绝创建；
必须显式移除原视图，不能静默覆盖或混合。指定的包版本独立记录在清单内，
因此也可以选择保持元数据 version 不变的合法适配修订包。

## 管理清单与卸载保护

`view.json` schema 1 包含 `view_id`、精确 `package_version`、内层 `sdk_version`，
以及每个组件的包名、精确包版本、相对源路径、元数据字段和两个 SHA-256：

- `receipt_sha256`：对收据中不可变的内容身份（`platform`、`artifact`、`upstream_version`）的 Go JSON 序列化结果计算 SHA-256；安装时间、automatic/显式归属和 active 选择不属于内容身份，正常 `switch` 不会因此使视图失效；
- `metadata_sha256`：对原始 `oh-uni-package.json` 字节计算 SHA-256。

不依赖 chmod 判定所有权。相同清单且原有目录/链接均符合白名单的重复创建
是幂等操作；不同包内容身份、原始 metadata 字节、源路径、额外文件或改动链接
均拒绝。`SDKPath` 还重新检查已安装收据和原始元数据，拒绝返回已改变的来源。
`SDKList` 校验视图结构；不会为了列出清单递归遍历源包或访问网络。

原生卸载的最终计划必须调用 `SDKRemovalGuard(before, after)`：
在已有 manager lock 内、commit 之前对完整 before/after 状态检查，不能只检查
用户请求的包，否则 autoremove/cascade 可能绕过保护。任何视图引用的精确
组件版本默认拒绝卸载，错误指导先 `oo sdk remove <view-id>`。其他未引用版本
仍可卸载。合法的部分视图也继续保护其清单里的全部版本；未知/损坏 SDK
内容导致 guard fail-closed，而不是猜测其引用。该 helper 自身**不再次加锁**。

## 并发、安全与失败恢复

创建、列出、查询路径和移除使用现有 manager lock，与原生包操作互斥。
受检查的根、state、packages、sdk、组件版本、视图和内层目录必须是实际
目录；元数据/清单必须是普通文件，读取使用 `O_NOFOLLOW`。视图内仅允许固定
白名单目录和预期相对链接，大小写冲突明确拒绝；不依赖 HOME 的权限语义。
原有 Root 的祖先目录属于调用方信任边界；这不是防御一个能任意并发修改
同一根目录的恶意进程的文件系统隔离机制。

不使用根目录中的临时 staging。新视图直接独占创建在最终持久交付路径，
先写入并同步完整清单，再依次创建真实目录和相对链接。正常失败按逆序删除
本次独占创建且 inode 身份仍匹配的节点，**不使用 RemoveAll**；遇到替换目录、
额外用户文件或清理错误时保留内容并报错，不递归清理未知内容。

进程中断后的恢复规则：

1. 完整合法清单已落盘，但部分目录/链接尚未创建：同版本 `sdk create` 补全；
   `sdk remove` 也可只清理已有白名单节点。`list`/`path` 不把部分视图视为成功。
2. 移除先逐个 unlink 链接，再删除空目录，最后删除清单和外层空目录。中断后
   清单还在时可重复 `sdk remove`。源包内容是否已消失不会阻止安全清理视图。
3. 在独占目录创建后、完整清单落盘前，或最终删除清单后、删除外层空目录前
   中断，会留下没有合法清单的路径。无法证明所有权时故意 fail-closed，需要
   人工检查该持久残留；不会把它当成可递归删除的临时目录。不存在的视图
   重复 remove 则成功。

仅 unlink 视图自身链接，不进入源包：就算源版本后来被外部替换为指向其他
位置的软链接，移除视图也不会触碰那个目标。视图目录本身被替换则拒绝移除。

## 核心 Go 接口

```go
SDKCreate(ctx context.Context, packageVersion string) (SDKView, error)
SDKList() ([]SDKView, error)
SDKPath(viewID string) (string, error)
SDKRemove(ctx context.Context, viewID string) error
SDKRemovalGuard(before, after State) error // 调用方已持有原生 manager lock
```

`SDKView` 包含 `ID`、绝对 `Root` 和 `Manifest`；管理器不直接打印 CLI 表格。
本模块不更改安装/卸载事务实现，CLI 与最终原生 removal hook 由调用方接入。

测试只构造独立已安装状态和组件 fixtures，不使用用户 SDK、不下载、不执行
SDK 工具。以 `$TMPDIR` 作为 Go 测试临时目录，构建缓存可放 `$XDG_CACHE_HOME`：

```sh
GOCACHE="$XDG_CACHE_HOME/oheco-go-cache" GOPROXY=off GOSUMDB=off \
  go test ./internal/manager -run '^TestSDK' -count=1
```
