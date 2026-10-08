# SpeedQuality 测速节点协议

本文面向希望自己搭建测速节点的用户。个人自用时，只需在自己有权使用的服务器上运行一个
HTTP 服务，实现激活、下载、上传和释放四个操作，再把节点信息填入 SpeedQuality 的
`STATIC_NODES`。加入官方社区网络时可以直接运行本仓库的 `sq-node`，由平台完成注册、
所有权验证、JWT、认证心跳、临时端口和调度。

这套接口是 **SpeedQuality 自建节点协议**，只用于用户自己实现和控制的节点。它不是 Taier
或全球网测的协议，也不能把 Taier 节点 IP 填进来直接使用。第三方节点使用各自的原始协议和
授权方式，不属于本文范围。

## 测试流程

```text
用户服务器（sqprobe）                 测速节点
        │                                │
        │  POST /activate                │
        ├───────────────────────────────▶│
        │◀───────────────────────────────┤  返回短期 key
        │                                │
        │  GET /download?key=...         │
        ├───────────────────────────────▶│
        │◀═══════════════════════════════│  持续下载字节流
        │                                │
        │  POST /upload?key=...          │
        │═══════════════════════════════▶│  持续上传字节流
        │                                │
        │  POST /release?key=...         │
        ├───────────────────────────────▶│
        │◀───────────────────────────────┤  释放完成
```

两种模式都要求四个操作使用同一个 IP、端口和下面列出的固定路径。

## 接口协议

### `POST /activate`

为本次测试创建一个短期 key。

请求没有正文。静态自用实现可以自行限制来源；社区节点要求
`Authorization: Bearer <短期 JWT>`。成功时返回 `2xx` 和纯文本 key：

```text
fGXjJld7P31uMhH92Fep0A
```

要求：

- key 去除首尾空白后为 1 至 256 字节，不能包含换行。
- 推荐使用只含字母、数字、`-`、`_` 的随机值，方便安全地放入 URL。
- key 应绑定来源 IP，最多保留 15 分钟，并在释放后立即失效。
- 没有剩余容量时返回 `429` 或 `503`，不能继续签发 key。
- 对个人自用节点，建议限制允许调用 `/activate` 的来源 IP，并设置每个来源的请求频率。

### `GET /download?key={key}&nonce={nonce}`

校验 key 后持续返回不可压缩字节流，直到客户端断开连接。

推荐响应头：

```http
Content-Type: application/octet-stream
Cache-Control: no-store, no-transform
Content-Encoding: identity
```

服务端应边生成边发送并及时刷新缓冲区。不能返回重复的零字节、固定小文件或经过 gzip 压缩
的内容，否则统计值不能代表真实网络下载流量。`nonce` 只用于避免缓存，节点不需要保存。

### `POST /upload?key={key}`

校验 key 后持续读取并丢弃请求体。不要把整个请求体写入内存或磁盘。

请求会携带较大的 `Content-Length`，但探针在测试时间结束后会主动取消连接。因此服务端必须
支持流式读取，并把客户端提前断开当作正常测试结束。成功接收时返回 `2xx`，响应正文应尽量
小。

### `POST /release?key={key}`

立即让 key 失效并释放并发名额。该接口应具有幂等性：同一个 key 重复释放也返回 `2xx`。

下载、上传和释放遇到无效或过期 key 时返回 `401` 或 `403`。所有接口都不应跳转到其他主机。

## 社区节点扩展

社区节点另外提供：

```http
GET /healthz
GET /.well-known/speedquality/ownership
```

匿名 `/healthz` 只返回存活状态和协议版本。访问模式、容量、流量、可用时间和临时网关状态
通过 API Token 认证的心跳上报，不通过公网健康接口暴露。所有权路径只在注册过程中返回当前
一次性 challenge，注册完成后应返回 `404`。

平台给 `/activate` 的 ES256 JWT 包含：

| Claim | 约束 |
| --- | --- |
| `iss`、`aud`、`sub` | 必须匹配登记的平台、受众和 Node ID |
| `jti` | 单次使用；节点必须持久化到过期，防止重放 |
| `scope` | `public` 或 Route Key 任务使用的 `owner` |
| `client_ip` | 必须与直接连接节点的来源 IP 一致 |
| `target_mbps` | `100/200/400`，且不能超过节点能力 |
| `max_bytes` | 每个上传或下载方向允许的最大字节数 |
| `duration_seconds` | 当前固定为 `5` |
| `iat`、`nbf`、`exp` | 签发、生效和过期时间，最长 15 分钟 |

节点必须先验证 JWT 签名和所有约束，再生成普通短期 key。`public` 任务受公共并发和每日公共
流量限制；`owner` 任务可使用提供者保留并发，但仍受节点总并发、每日总流量、JWT 速度和字节
上限约束。
参考实现和部署方法见 [`sq-node.md`](sq-node.md)。

社区模式不会把固定内部端口直接交给测速用户。Core 先下发来源 IP、随机外部端口和 TTL，节点
写入 nftables 并 ACK 后才返回租约；日常主服务只监听回环地址。静态自用实现不需要采用该控制
面，但公开给他人使用时应提供等价的来源和暴露时间限制。

## 节点要求

| 项目 | 要求 |
| --- | --- |
| 地址 | 公网 IPv4 或 IPv6 字面量，不能只提供域名 |
| 端口 | 四个操作使用相同端口，并允许用户服务器直接连接 |
| 地区 | 节点实际入口和出口应位于登记的省级地区 |
| 运营商 | 应准确登记电信、联通、移动或其它实际网络 |
| 连接 | 应快速接受 TCP 连接；SpeedQuality 会用连接耗时计算延迟 |
| 代理 | 不要经过会压缩、缓存、限流或限制请求体大小的 CDN 和反向代理 |
| HTTP | 可以使用 HTTP；如使用 HTTPS，证书必须对 IP 字面量有效 |
| 日志 | 不记录上传内容和完整 key，避免日志泄露短期凭据 |

节点只能使用自己拥有或已获得明确授权的服务器和带宽。服务器名称、销售标签和 IP 数据库
只能作为线索，接入前应独立核验实际省份、运营商和出口。

## 流量与容量

每个方向先进行约 2 秒预热，再进行固定 5 秒正式测量；单个节点的上传和下载依次执行。达到
所选档位时，一次完整测试的最大参考流量为：

| 档位 | 下载 | 上传 | 合计 |
| ---: | ---: | ---: | ---: |
| 100 Mbps | 约 87.5 MB | 约 87.5 MB | 约 175 MB |
| 200 Mbps | 约 175 MB | 约 175 MB | 约 350 MB |
| 400 Mbps | 约 350 MB | 约 350 MB | 约 700 MB |

实际线路较慢时流量会更少。节点应设置最大活跃 key 数，并按物理 IP 和端口计算总容量。例如，
一个可稳定提供 1 Gbps 的物理节点不应同时接受三个 400 Mbps 测试。达到上限时由
`/activate` 明确拒绝。

## 静态自用接入

节点部署完成后，需要准备以下信息：

| 字段 | 示例 |
| --- | --- |
| 省份代码 | `hb` |
| 运营商 | `ct`（电信）、`cu`（联通）、`cm`（移动） |
| IP 类型 | `v4` 或 `v6` |
| IP 地址 | `203.0.113.10` |
| 端口 | `8080` |
| 最高档位 | `200` |

当前公共测速只接收 `ct`（电信）、`cu`（联通）和 `cm`（移动）三类节点，暂不接收教育网或
其他运营商代码。

将节点加入 SpeedQuality 的 `STATIC_NODES` 配置：

```json
{
  "hb": [
    {
      "carrier": "ct",
      "address": "203.0.113.10",
      "port": 8080,
      "scheme": "http",
      "max_mbps": 200
    }
  ]
}
```

省份代码是最外层 key；同一省份可以配置多个运营商或同一运营商的多个候选节点。`scheme`
可以省略，默认 `http`；`max_mbps` 只能是 `100`、`200` 或 `400`。当用户选择的测速档位高于
节点的 `max_mbps` 时，开源项目不会分配该节点。

最大并发由节点服务自己的 `/activate` 实现控制，不写入 `STATIC_NODES`。静态模式不在平台侧
重复预留容量，适合个人自用；公开给多人使用时必须保证节点的激活接口能原子限制并发。

示例 IP 是文档保留地址，不能直接用于部署。`STATIC_NODES` 的具体保存位置和公开项目部署
步骤见 [`../deploy/cloudflare-worker/README.md`](../deploy/cloudflare-worker/README.md)。

## 本地验收

先用实际地址替换 `SQ_NODE_ORIGIN`：

```bash
SQ_NODE_ORIGIN='http://203.0.113.10:8080'
SQ_NODE_KEY=$(curl -fsS -X POST "$SQ_NODE_ORIGIN/activate")
test -n "$SQ_NODE_KEY"

curl -sS --max-time 2 -o /dev/null \
  -w 'downloaded=%{size_download} bytes\n' \
  "$SQ_NODE_ORIGIN/download?key=$SQ_NODE_KEY&nonce=1" || true

SQ_UPLOAD_FILE="/tmp/speedquality-node-upload.$$"
dd if=/dev/urandom of="$SQ_UPLOAD_FILE" bs=1M count=1 status=none
curl -fsS -X POST --data-binary "@$SQ_UPLOAD_FILE" \
  "$SQ_NODE_ORIGIN/upload?key=$SQ_NODE_KEY"
rm -f "$SQ_UPLOAD_FILE"

curl -fsS -X POST "$SQ_NODE_ORIGIN/release?key=$SQ_NODE_KEY"
```

下载命令在两秒后由 `curl` 主动停止，出现超时退出属于预期，但 `downloaded` 必须大于零。
完成后还应确认：

- 错误 key 无法下载或上传。
- 释放后的 key 立即失效。
- 多次释放不会报服务器内部错误。
- 达到最大并发后，新的激活请求返回 `429` 或 `503`。
- 连续运行时内存、临时文件和连接数不会持续增长。

以上项目通过后，把配置加入 SpeedQuality 即可进行完整 Bash 测速。要加入平台调度，不应把
社区 `sq-node` 手工写入 `STATIC_NODES`；请改用 `sq-node register`，让平台签发节点凭据并
通过认证心跳和测速反馈管理状态。
