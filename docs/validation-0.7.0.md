# oheco 0.7.0 原生验证记录

验证日期：2026-09-13。发行源码以 `v0.7.0` 标签为准；发行归档的独立 `.sha256` 文件记录最终摘要。

## 环境与隔离

- HarmonyOS PC ARM64，HongMeng Kernel 1.13.0；不是 Linux 构建或交叉编译验收。
- OHOS Go 1.27.1；`CGO_ENABLED=0`；本机 `binary-sign-tool`，Go 链接阶段自动签名。
- 实际 npm/Node.js 和 Python 3.14 / pip 26.2.1 参与验收。
- 自动化测试使用应用私有临时目录，避免共享 `/storage` 文件系统的权限位转换。
- CLI 验收使用含空格的独立 `OHECO_ROOT`、npm global prefix、显式 Python 虚拟环境及 loopback 源；没有改动用户已有全局安装和配置。
- 原生及生态 fixture 全部本地生成并校验；实际网络发布操作单独使用用户确认的 `127.0.0.1:10808` SOCKS5 代理。

## 已通过

```sh
GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local CGO_ENABLED=0 \
  go test -count=1 -shuffle=on ./...
GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local CGO_ENABLED=0 go vet ./...
OHECO_VERSION=0.7.0 OHECO_GO="$(command -v go)" sh scripts/build-ohos.sh
python3 scripts/test-dependencies-native.py build/oo
```

以上命令的 `TMPDIR` 应设置为当前应用的私有可写目录。CLI 日志写入本地 `build/dependency-native.log`（构建目录不提交）。

覆盖：

- schema v5、旧索引完整包过滤、原生版本大小/等于/不等于、AND/OR、SDK 多段、tmux 字母修订、OHOS 修订、opaque 范围拒绝。
- 多根统一求解、冲突无副作用、选中版本循环与回溯、手动/自动安装归属、共享依赖和整批卸载清理。
- 带自身依赖的新版本 `--no-switch` 与旧 active 并存；禁止传递依赖暗中切换同名 active；离线切换与删除保护。
- 复用依赖的实际目录和命令缺失时拒绝安装/切换，不仅检查 JSON 记录。
- 全部原生包先暂存、验证再提交；后续归档损坏不留下已安装的前置依赖。
- 事务记录 stage 目录设备号/inode；未搬入或被替换的目录不被回滚删除；legacy 日志保守保留未知归属目录；已提交日志正常清理；重复恢复可用。
- 独立只读复验用真实 CLI 在下载/解压期间创建目标目录和用户 sentinel，确认操作拒绝且用户数据保留；另验证第一 stage 已搬入、第二 stage 未搬入时的恢复。
- PTY 确认提示处 SIGTERM 后无需再输入即退出（独立复验约 0.1 秒）；安装目录和状态不变；EOF 和已取消 context 均不代表同意。
- state v1 备份/迁移；激活的 `oo` 必须支持 state2，避免升级状态后留下旧默认管理器或错误降级。
- 查询明确列出管理器；外部 `<由 npm 管理>` / `<由 pip 管理>` 只是管理归属，不建立外部 receipt 或假报已安装。
- 真实 npm tarball 在独立 prefix 中按 global 布局安装，命令可执行，安装脚本被禁用；源关闭后可卸载。
- 真实 pip 双 wheel 的依赖由 pip 解析，模块可导入；源关闭后可卸载根包；依赖按约定保留，随后可显式卸载。
- `--` 参数顺序与含空格参数保留、`--prefix` 不隐含 local、显式 local 覆盖、混合管理器透传拒绝、危险源覆盖拒绝。
- 缺工具链、显式解释器、已激活 venv/base 选择、dry-run、原有生态入口兼容、用户配置保持。

软件目录另执行离线 `scripts/build.sh`、`scripts/test-v5.py`、`scripts/test-site.cjs`，检查 v1–v5 输出、历史 v4 schema、管理器/逐版本依赖展示和网站事件行为。

## 明确限制

- npm/pip 自己拥有依赖解析和安装状态；不维护外部镜像，不承诺外部跨根反向依赖保护；pip 不自动清理孤立依赖。
- 跨原生/npm/pip 没有全局原子事务；后端失败保留已完成步骤并报告，不假装回滚全部环境。
- 只有一个共享 active 环境，不承诺同时启用互不兼容的原生依赖版本。
- v1 备份用于核对，不能直接覆盖 `installed.json` 来降级；旧事务缺少身份信息时保留目录需要人工核对。
- 网站使用离线 DOM 合同测试，不冒充真实浏览器视觉验收；结构合同测试不冒充完整 JSON Schema 标准实现，实际目录语义由 Go 校验器验证。
- 本机验收不代表所有 HarmonyOS 版本、设备或架构均支持。正式 Release、Pages 和线上索引安装结果应另核对对应远端发布记录。
