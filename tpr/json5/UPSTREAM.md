# JSON5 源码来源与离线接入

本目录完整保留 `github.com/titanous/json5` 的固定提交源码，包括原始测试、测试数据、CI 配置、模块文件和许可证。除新增本说明外，134 个归档文件均未修改。

- 上游仓库：<https://github.com/titanous/json5>
- 固定提交：[`5f74668fe3ed2dc3cc990224d823e5dfa0a678ac`](https://github.com/titanous/json5/commit/5f74668fe3ed2dc3cc990224d823e5dfa0a678ac)
- 提交时间：2023-01-20T03:45:58Z
- 下载归档：<https://codeload.github.com/titanous/json5/tar.gz/5f74668fe3ed2dc3cc990224d823e5dfa0a678ac>
- 归档 SHA-256：`872e7af9d5e94f304e078da3fc4aee75ed6c8f2663e58c22e2de7341d4aff7b9`
- 归档大小：41,799 字节；解压文件总大小：132,057 字节。
- 许可证：[原始 LICENSE](LICENSE)。基于 Go `encoding/json` 的解码器采用 BSD-3-Clause；来自 JSON5 上游的测试数据采用 MIT，完整原始声明均已保留。
- 下载时设置 `HTTP_PROXY=HTTPS_PROXY=socks5://127.0.0.1:10808`，curl 同时指定 `--proxy socks5h://127.0.0.1:10808`；临时归档位于 `$TMPDIR`，下载验证后已清理。

## 父仓推荐接入

直接导入本项目的 `internal/json5`：

```go
import "github.com/oheco/oheco/internal/json5"

var cfg struct {
    App struct {
        BundleName string `json:"bundleName"`
        Label      string `json:"label"`
    } `json:"app"`
}
if err := json5.Unmarshal(data, &cfg); err != nil {
    return fmt.Errorf("decode app configuration: %w", err)
}

var generic map[string]interface{}
if err := json5.Unmarshal(data, &generic); err != nil {
    return err
}
```

`data` 是调用者用 `os.ReadFile` 读入的完整文件字节。`Unmarshal` 校验整个输入，适合单个配置文件；它会拒绝结尾附加的第二个对象。结构体沿用标准 `json` 标签，默认忽略未知字段。

[internal/json5](../../internal/json5/) 原样复制本目录的六个生产 Go 文件及完整许可证：`decode.go`、`fold.go`、`scanner.go`、`stream.go`、`tags.go`、`types.go`，共 71,404 字节生产源码。没有本地语义补丁、外部运行依赖或 CGO；另附本项目集成测试。父仓不需要修改 `go.mod`，也不需要 `replace`。更新依赖时必须同步替换这六个文件与许可证，并重新核实归档摘要。

本目录保留上游模块边界，因此父仓的 `go test ./...` 不会递归运行这里带第三方依赖的上游测试。不要在父仓直接导入 `github.com/oheco/oheco/tpr/json5`；它是独立模块。若坚持导入原路径 `github.com/titanous/json5`，才需要父仓额外 `require` 和 `replace github.com/titanous/json5 => ./tpr/json5`，本方案没有采用。

## 支持范围与行为

支持解码到结构体和 map：行注释、块注释、单引号字符串、ASCII 未加引号键、对象和数组尾逗号、中文字符串及带引号的中文键；字符串中的 `//`、`/* */`、URL、路径和转义不会被误删。以解析器状态机和解码器处理输入，没有正则预处理。

解析到 Go 值保留字符串内容；注释和原始排版不属于输出数据，不提供保留格式后写回的能力。上游的未加引号键只支持 ASCII 标识符：中文键请加引号。自定义 `UnmarshalJSON` 收到的是原始 JSON5 片段；若该实现内部调用标准 `encoding/json`，应改用普通结构体/map 或自行支持 JSON5。

map 中数值默认是 `float64`。结构体里的整数字段不经过 `float64`；需要保留动态 map 中大整数精度时，可用 `NewDecoder(reader)` 和 `UseNumber()`，所得数值为 `json5.Number`。`Decoder.Decode` 是流式接口，若用于单个完整文件，应继续检查后续为 `io.EOF`；一般配置文件优先用 `Unmarshal`。

## 独立离线验证

在父仓根目录运行，测试仅编译该依赖：

```sh
GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local CGO_ENABLED=0 \
    go test -count=1 ./internal/json5
```

本机已在 `GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local CGO_ENABLED=0` 下通过上述命令：集成测试覆盖 DevEco `app`、`module`、`modules`、`signingConfigs` 的结构体和 map 解码、中文与转义、注释标记出现在字符串内、大整数精度、语法和类型错误，并复用本目录完整上游语法测试数据执行 106 个接受/拒绝检查。另将上游全部 Go 文件（排除唯一依赖外部库的 `json5_test.go`）作为显式文件列表运行 `go test -count=1`，其余上游单元测试全部通过。六个生产源文件和许可证的副本已逐字节核对一致。没有运行父仓全量测试，也没有下载上游测试的 JavaScript VM 等第三方测试依赖。
