# oheco 0.8.0 语言包下载模型验证

日期：2026-09-13。本版把语言包通道从“oo 代理下载并校验”改为“oo 只提供元数据 + 转发/聚合”，
这是一次能力变化：**第三方包不再由 oo 校验**。原生包、索引 schema v5、本地状态 schema2 均未改动。

## 结论摘要

| 项目 | 结果 |
| --- | --- |
| Go 全量测试 | `go test -count=1 ./...`、`go test -count=1 -shuffle=on ./...` 全部通过 |
| 静态检查 | `go vet ./...` 无输出，`gofmt -l cmd internal` 无输出 |
| 原生构建 | HarmonyOS arm64、GO 1.27.1、`binary-sign-tool` 自动签名，产出 `oo 0.8.0 (ohos/arm64)` |
| 端到端验收 | `scripts/test-language-forwarding-native.py` 5/5 通过（全部 loopback fixture，无外网） |

## 端到端验收覆盖

隔离 HOME 与 `OHECO_ROOT`，本地索引与假源全部在 loopback，且把 `HTTP(S)_PROXY` 指向一个死端口，
以此同时验证 loopback 的 `NO_PROXY` 豁免：

1. **npm 目录包**：npm 直接从描述里的 URL 下载 tarball，且安装进 `~/.npmrc` 配置的 `prefix`
   （证明用户配置被尊重——这正是本版要修的用户问题）。
2. **npm 转发**：非目录包名经 oo 302 到用户 npmrc 配置的源，安装成功；配置了 token 时打印
   “匿名访问”告警，且目标源**没有收到** `Authorization` 头。
3. **pip 目录包**：oo 的 simple 页给出适配 wheel 的原始 URL 与 `#sha256=`，pip 自己去取并校验。
4. **pip 聚合**：非目录包名跨两个源聚合——一个返回 PEP 691 JSON，另一个返回 PEP 503 HTML 且
   使用**相对链接**；合并结果可用，依赖被正确解析。
5. **pip `find-links`**：明确报错，不静默忽略。

## 协议行为实测（用于确认方案可行性）

- npm 会跟随跨主机 302，但**不转发凭据**（`always-auth=true` 也无效），也不会泄漏 oo 源自己的
  凭据 → 认证源只能匿名访问，因此本版选择“匿名 + 提示”。
- npm 会校验**非注册中心地址**上的 tarball：声明 integrity 错误时直接 `EINTEGRITY` 失败。
- npm 命令行 `--@scope:registry=` 生效并压过 npmrc 的同名 scope 配置 → 目录里的 scoped 适配包
  可用它保证来自 oo。
- pip 同样跟随 simple 索引的 302 且不转发凭据；会强制校验 `#sha256=`（错误即失败）。
- 鸿蒙上 pip 计算的兼容标签只有 `ohos_aarch64` 与 `none-any`，没有 manylinux/musllinux，
  因此上游 Linux 二进制 wheel 本就不可能被选中——这也是继续强制 `--only-binary=:all:` 的依据。
- 主流源的格式是混的：PyPI、清华返回 PEP 691 JSON，阿里云、华为云只返回 HTML，
  PyTorch 的 wheel 目录是 HTML 清单页 → 聚合必须同时支持 JSON 与 HTML 并解析相对链接。

## 本次发现并修复的问题

- **凭据检测**：npm 把 `_authToken` 从 `config ls --json` 中隐藏，`config get` 也拒绝读取。
  改为只扫描 `~/.npmrc`、`NPM_CONFIG_USERCONFIG`、项目 `.npmrc` 链和 `NPM_CONFIG_*` 的
  **键名**（从不读取或打印值）。
- **多索引 404 语义**：某索引 404 表示“该源没有这个包”，在 `extra-index-url` 场景下是常态，
  不能判定为整次失败；现在 404 跳过、其他错误（如 5xx）仍整体报错。
- **pip 制品 URL 规则**：pip 在 HTML 索引下用 **URL basename** 判定 wheel 文件名，因此
  `catalog` 现在要求 pip 制品的 URL 必须以 `filename` 结尾，否则在索引生成阶段就报错，
  避免线上静默 “invalid wheel filename / no matching distribution”。

## 限制

- oo 不经手凭据：配置了认证的源会以匿名身份访问并可能 401/403，oo 只提示不代填。
- 上游源仍是 https（或 loopback http）；明文 http 的企业源与旧版一样不被接受。
- 聚合策略是“按索引顺序、同名文件先到先得”，不做版本择优——第三方包的候选集完全交给用户
  配置的源，oo 只保证目录包不与上游候选混合。
- 单元与端到端验收都在单机 loopback 上完成，不代表所有企业源、镜像实现或全部 HarmonyOS 设备。
