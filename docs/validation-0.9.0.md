# oheco 0.9.0 验证记录

0.9.0 把 `oo` 对 npm / pip 的处理从「oheco 自己代理并校验包」改成「oheco 只提供元数据」：

- `oo` 只托管目录（catalog）里列出的、经过 HarmonyOS 适配的包；
- 目录之外的包名不再代理、不再下载缓存，而是由 `oo` 直接 302 到用户自己配置的源；
- `oo npm` / `oo pip` 两个遗留子命令被删除，`oo install` / `oo remove` / `oo info` / `oo list` / `oo search` 统一处理所有生态；
- 注入参数只保留三类：源的指向、正确性、以及脚本/轮子安全相关的开关。作用域、噪音、重试一律交给用户自己通过 `--` 传递。

## 验证环境

- HarmonyOS 主机（非虚拟机、非容器），aarch64。
- Go 1.27.1（`ohos/arm64`），`CGO_ENABLED=0`。
- 签名工具 `binary-sign-tool` 来自 `ohos-sdk-toolchains` 包，在 `go build` 期间由构建脚本调用。

## 发布前门禁（必须全部通过才允许打 tag）

### 静态检查与测试

| 项目 | 命令 | 结果 |
| --- | --- | --- |
| 格式 | `gofmt -l cmd internal` | 无输出 |
| 静态分析 | `go vet ./...` | 通过 |
| 单元/集成测试 | `go test -count=1 ./...` | 全部包 ok（`cmd/oo`、`cmd/oo-index`、`catalog`、`manager`、`registry`） |
| 原生构建 | `scripts/build-ohos.sh` | `oo 0.9.0 (ohos/arm64)` |

### HarmonyOS 原生端到端

| 脚本 | 结果 |
| --- | --- |
| `scripts/test-language-native.py` | 通过 |
| `scripts/test-language-forwarding-native.py` | 5/5 通过 |
| `scripts/test-dependencies-native.py` | 通过 |
| `scripts/test-domain-native.py` | 通过 |

`test-language-forwarding-native.py` 覆盖了 0.9.0 的核心新行为：目录内的包由 `oo` 直接给出元数据（tarball 指向 GitHub Releases 且带 `integrity`），目录外的包名 302 到用户的源；转发用的 tarball 故意放在**另一个 origin**（不同端口），以证明 npm 的 `replace-registry-host` 重写被 `--replace-registry-host=never` 关掉后不会再出现 0.8.0 里那个 404。

### 真实源冒烟（发布前最后一步）

用真实的公共源跑一遍，确认 `oo` 转发出去之后 npm / pip 自己仍然能用：

| 生态 | 场景 | 结果 |
| --- | --- | --- |
| npm | 目录外包名经 `oo` 302 转发到真实 registry，global 布局安装 | 通过，`require()` 可用 |
| pip | 目录外包名经 `oo` 聚合转发到真实索引 | 通过（`Successfully installed idna-3.10`，`import idna` 且 IDNA 编码正常） |

## 已知限制

- **认证源不支持**。npm 会把 `_authToken` 从 `config ls --json` 里隐藏、并拒绝 `config get` 读取；pip 配置里的 URL userinfo 虽然可见，但 `oo` 也刻意不读取凭据值。因此 `oo` 转发时一律以**匿名**身份访问上游，并在检测到凭据配置时打印警告（只读键名，从不读取、从不打印、从不转发凭据值）。跨主机 302 本身也会丢凭据，这是 npm / pip 的行为，`oo` 无法绕过。
- **上游必须是 https**（回环地址允许 http，用于测试）。
- **聚合顺序为「第一个索引优先」**，不做版本择优。
- **pip 的 `find-links` 源直接报错**：`oo` 只支持索引型（simple index）源。`--only-binary=:all:` 是强制注入的，源码分发包不会被安装。
- **代理**：`oo` 会把用户自己的代理设置原样传给 npm / pip。注意 pip 无法使用 `socks5://` 代理（除非装了 PySocks），此时需要改用 HTTP 代理——这是 pip 的限制，不是 `oo` 的缺陷。
- **目录内包名的 pip 制品 URL 必须以轮子文件名结尾**，由目录校验强制。
- 本版本删除了安装 npm 包前的「项目级预检」（工作区拒绝、lockfile 里 `resolved` 的 https 校验、项目 `.npmrc` 的 `node-options` / `script-shell` / `onload-script` 检查）。argv 级别的拦截（`--script-shell`、`--node-options`、`--workspace`、`--ignore-scripts=false` 等）仍然有效；预检被移除是因为 `oo` 不再替用户决定作用域，项目/工作区上下文重新回到用户自己的选择里。
- 只在单台 HarmonyOS 设备上验证过。

## 发布后确认

发布后（v0.9.0 已成为 Latest、`oheco-packages` Pages 已部署）用官方索引和官方产物再跑了一遍
`dist/verify-official-0.9.0.py`，全部通过：

- 未设置 `OHECO_INDEX_URL`，走编译期默认索引 `https://oheco.org/index/v5/index.json`，
  线上 v5 索引中 `oheco` 的 `latest` 为 `0.9.0`，URL / 大小 / SHA-256 与 Release 附件一致。
- 在隔离的 `OHECO_ROOT` / `HOME` 下 `oo install oheco -y` 升级到正式发布的 0.9.0，
  `bin/oo --version` 输出 `oo 0.9.0 (ohos/arm64)`。
- 目录内 npm 包 `deepseek-harness` 用显式 `-- --global` 安装成功：`oo` 没有注入作用域，
  `~/.npmrc` 的 `prefix` 被 npm 正常使用，`dsh --version` 可运行，`oo search` 显示
  `<由 npm 管理>`。
- 目录外的 `npm:is-number@7.0.0` 经 `oo` 转发到真实 registry 安装成功，`require()` 返回
  预期结果。
- 两个包都能用相同的 `-- --global` 参数卸载。

此项属于复核，不构成发布门禁；本次复核未发现功能缺陷。
