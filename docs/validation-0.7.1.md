# oheco 0.7.1 域名统一验证

日期：2026-09-13。此补丁将客户端默认索引及当前官网入口统一为 `https://oheco.org`。
索引 schema 仍为 v5，本地安装状态仍为 schema2，不改变软件包布局、Go 模块路径和 GitHub Release 下载位置。

## 已完成的源码与候选验证

- HarmonyOS PC ARM64，OHOS Go 1.27.1，`CGO_ENABLED=0`；本机 `binary-sign-tool` 自动签名。
- 应用私有 `TMPDIR` 中执行 `go test -count=1 -shuffle=on ./...`、`go vet ./...`，全部通过。
- 新增 `internal/manager/domain_test.go`，无需外网即可验证：
  - 默认索引直接为 `https://oheco.org/index/v5/index.json`。
  - 显式自定义源不被覆盖；远程 HTTP 源仍被拒绝。
  - 旧官网 HTTPS 到新域名 HTTPS 的跳转可用；HTTP 降级在发出下一请求前被拒绝。
  - 从旧域名迁移缓存时，不复用旧 ETag/Last-Modified，不受旧源检查间隔阻挡，也不修改安装记录或命令链接。
- 鸿蒙签名候选程序显示 `oo 0.7.1 (ohos/arm64)`；不设置 `OHECO_INDEX_URL`，在独立 root 中成功 `oo update`，缓存记录确认新域名是请求源。
- 通过用户指定代理进行真实 GET，新域名 v5 索引返回 HTTP 200，重定向次数为 0。
- 组织和目录站点另有离线测试，检查新安装命令、复制按钮、canonical、CNAME、当前 schema `$id`、历史 v1–v4 标识及相对资源路径。

## 可重复执行

```sh
# TMPDIR 指向当前应用的私有可写目录；代理按当前环境配置。
export HTTPS_PROXY=socks5://127.0.0.1:10808
export HTTP_PROXY="$HTTPS_PROXY"
GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local CGO_ENABLED=0 go test -count=1 ./...
OHECO_GO="$(command -v go)" sh scripts/build-ohos.sh
python3 scripts/test-domain-native.py

# 新 Release 和 Pages 都部署完成后再执行：
python3 scripts/test-domain-native.py --published
```

发布后模式验证新首页/安装脚本/CSS/JS、v1–v5 索引、当前及历史 schema 的 HTTPS 直接访问和旧地址重定向兼容；
随后在独立目录执行正式 install.sh、原生 Git/Git LFS 安装卸载，以及从原版 oo 0.7.0 到新版的升级。
发布前模式不声称新版已经上线；正式发布后的结果应查看对应 Release、Pages 工作流及交付日志。

## 保留与边界

- 旧 Release 内容、标签和摘要不覆盖；历史验证记录不改写为曾使用新域名。
- 历史 v1–v4 schema 的旧 `$id` 继续保留为协议标识；新域名提供这些文件，不要求重写其标识。
- GitHub Pages 后台的自定义域名与强制 HTTPS 仍是部署配置；`site/CNAME` 将意图固化进源码并随站点复制，不替代后台/DNS设置。
- 显式 `OHECO_INDEX_URL` 设置仍优先。旧客户端使用与其兼容的索引版本升级，不能把仅支持 v4 的客户端直接指向 v5。
- 没有删除用户已有安装、修改个人 shell 配置或放宽 TLS/重定向校验；单机和 DOM 合同测试不等于所有设备/浏览器的兼容性保证。
