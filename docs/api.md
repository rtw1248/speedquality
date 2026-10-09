# SpeedQuality API

## 平台参考时间

```http
GET /api/time
Cache-Control: no-cache
```

返回 `{"version":1,"epoch":1791532800}`，其中 `epoch` 为平台当前 Unix 秒，响应不缓存。
客户端必须通过 HTTPS 获取。Bash 将该时间与 Linux `/proc/uptime` 配对，后续时间按经过的
秒数推进；每次启动探测器时通过内部参数 `--reference-time` 传入当前参考时间。探测器再用
Go 单调时钟推进时间，继续检查租约签发时间和有效期，并记录测速起止时间。

测速时长、延迟和限速计时仍采用单调时钟，不受本机系统时间偏差影响。整份报告及 NodeQuality
时间差校验采用同一参考时间。不会修改系统时钟或关闭 TLS 证书校验；这个参考时间受网络
请求延迟和秒级取整影响，用于租约与报告，不用于高精度授时。平台时间不可用时提示并回退
本机时间，真实过期或无效的租约仍会被拒绝。

## 创建会话

```http
POST /api/session
Content-Type: application/x-www-form-urlencoded

regions=hb,bj&mode=s&ip_mode=v4&duration_seconds=5&target_mbps=200
```

公开脚本固定发送 `mode=s` 和 `duration_seconds=5`。`ip_mode` 是平台内部的兼容字段：
实际任务仅包含 IPv4 时使用 `v4`，包含 IPv6 或仅包含 IPv6 时使用 `v6`。具体执行
了哪些 IP 类型由租约和 `speed_data` 中的 `family` 记录确定。`target_mbps` 只能为
`100`、`200` 或 `400`。

公共测速暂不接受 `hk`、`mo`、`tw`，会在创建会话前返回 HTTP 400；通过 `node_route`
精确选择已经注册的自有节点时，仍按节点实际登记地区校验。

成功时返回纯文本短期 Token。会话与来源 IP 绑定，单次允许 1 至 5 个省份，并限制 IP 类型、
目标速率、有效期和每个省份的租约重试次数。多省或同时测试 IPv4/IPv6 的任务会按预计执行时间延长，
最长 6 小时，具体到期时间见 `X-Session-Expires-At`。

同时测试 IPv4/IPv6 时，Bash 客户端分别经 IPv4 和 IPv6 创建来源绑定会话，并用
对应会话申请该地址族的租约。这是因为社区节点会把平台看到的来源 IP 写入短期 JWT，
并在用户直连节点时再次核对。最终报告由本次任务的第一个可用 IP 类型会话提交；
仅 IPv6 的任务会经 IPv6 提交。

精确使用社区节点时，先通过 `/api/nodes/resolve` 取得登记信息，再创建只含该节点省份的会话：

```http
POST /api/session
Content-Type: application/x-www-form-urlencoded

regions=hb&mode=s&ip_mode=v4&duration_seconds=5&target_mbps=200&node_route=sqn_...
```

`node_route` 使用注册时签发的 Route Key。该会话只允许一个省份，地区、IP 类型和档位必须与
登记节点一致；节点不可用时创建失败，后续租约也不会回退到其它社区或兼容节点。

## 社区节点管理

### 自动检测

```http
GET /api/nodes/detect
```

节点分别通过 IPv4 和 IPv6 请求该接口。响应包含平台实际看到的公网地址、地址族、国家、
省份候选、ASN、网络组织和可可靠识别时的运营商。安装器只把它作为首次配置候选，注册时
Core 仍会重新核对请求来源、地区和公网所有权。无法识别运营商时返回空字符串，由终端向导
只询问这一项。

### 注册

```http
POST /api/nodes/register
Content-Type: application/json

{
  "version": 1,
  "challenge": "sq-owner-...",
  "region": "hb",
  "carrier": "ct",
  "label": "provider-node-1",
  "ipv4": "YOUR_PUBLIC_IPV4",
  "ipv6": "",
  "port": 51234,
  "max_mbps": 100,
  "max_concurrency": 1,
  "reserve_concurrency": 0,
  "daily_public_bytes": 10000000000,
  "daily_total_bytes": 20000000000,
  "access_mode": "public",
  "availability": "always",
  "timezone": "Local",
  "available": true,
  "agent_version": "v1.0.0",
  "protocol_version": 1,
  "capabilities": ["speed.http.v1"],
  "firewall_ready": false,
  "authorized": true
}
```

上面的地址仅表示字段格式，必须换成节点自己的真实公网地址。公开 Worker 把实际请求来源 IP
传给私有 Core；来源必须与登记的 IPv4 或 IPv6 一致。Core 随后访问
`/.well-known/speedquality/ownership` 核对一次性 challenge，并独立校验地区。

成功响应只在本次注册返回 API Token：

```json
{
  "node_id": "0123456789abcdef0123456789abcdef",
  "route_key": "sqn_...",
  "api_token": "sqa_...",
  "jwt_public_jwk": {
    "kty": "EC",
    "crv": "P-256",
    "x": "...",
    "y": "...",
    "alg": "ES256",
    "kid": "sq-node-1"
  },
  "jwt_issuer": "https://sq.yolo2.cc",
  "jwt_audience": "sq-node",
  "heartbeat_seconds": 60,
  "admission_status": "observing",
  "certified_mbps": 100
}
```

Route Key 只负责精确路由；API Token 只负责节点管理。`sq-node` 将两者保存到权限为 `0600`
的本机配置，平台只保存加盐哈希。

### 心跳与注销

```http
POST /api/nodes/heartbeat
Authorization: Bearer sqa_...
Content-Type: application/json

{
  "access_mode": "public",
  "max_mbps": 100,
  "max_concurrency": 1,
  "reserve_concurrency": 0,
  "daily_public_bytes": 10000000000,
  "daily_total_bytes": 20000000000,
  "public_bytes_today": 123456789,
  "public_reserved_bytes": 0,
  "total_bytes_today": 123456789,
  "total_reserved_bytes": 0,
  "emergency_stopped": false,
  "active": 0,
  "public_active": 0,
  "availability": "08:00-23:00",
  "timezone": "Asia/Shanghai",
  "available": true,
  "agent_version": "v1.0.0",
  "protocol_version": 1,
  "capabilities": ["speed.http.v1"],
  "firewall_ready": true
}
```

心跳会同步访问模式、容量、可用时间、程序能力、公共与总流量、紧急隔离和临时网关状态，
并取得当前 JWT 公钥、平台准入状态、认证档位及下一次心跳间隔。`firewall_ready=false` 或
`emergency_stopped=true` 时 Core 不签发社区节点租约。平台超过 180 秒未收到心跳时停止调度。
注销使用同一 Bearer API Token：

```http
POST /api/nodes/unregister
Authorization: Bearer sqa_...
Content-Type: application/json

{}
```

注销会撤销平台记录和活跃预留，本地程序、配置和状态由 `sq-node` 保留。

### Route Key 管理

```http
POST /api/nodes/route-key
Authorization: Bearer sqa_...
Content-Type: application/json

{"action":"rotate"}
```

`rotate` 返回新的 `route_key`，旧 Key 立即失效；`revoke` 撤销当前 Key，但保留节点注册和 API
Token。节点以后仍可再次执行 `rotate` 生成新 Key。

### 临时网关控制

`sq-node gate` 使用 API Token 轮询 `POST /api/nodes/control`，取得经过 Core 校验的待确认、就绪
或撤销命令。命令只允许当前节点的内部固定端口、`50000-59999` 外部端口、合法来源地址和不
超过 15 分钟的到期时间。节点写入 nftables 并开始监听后，通过
`POST /api/nodes/control/ack` 提交 `ready`；失败则提交 `failed`。这两个接口供节点代理使用，
普通测速用户不应直接调用。

### 解析 Route Key

```http
POST /api/nodes/resolve
Content-Type: application/json

{"route_key":"sqn_..."}
```

响应只包含创建会话所需的 Node ID、地区、运营商、可用 IP 类型、最高档位和访问模式，不返回
节点地址或 API Token。离线、暂停、当前不在可用时间或被平台暂停的节点不可解析。

### 节点更新

```http
GET /api/nodes/update?channel=stable
```

响应提供当前稳定版本、签名清单地址和签名地址。该响应本身不构成软件信任依据；`sq-node`
必须使用内置 Ed25519 发布公钥验证清单，再核对目标二进制的大小和 SHA-256。当前公开协议
只定义 `stable` 通道，不在文档中预告尚未上线的测量能力。

## 获取节点租约

```http
POST /api/node-lease
Authorization: Bearer SESSION_TOKEN
Content-Type: application/x-www-form-urlencoded

region=hb&family=v4
```

租约格式由 `probe/lease.go` 定义。每份租约只包含一个省级地区、一个 IP 类型和少量候选
节点，有效期不超过 15 分钟，并回显以下会话约束：

```json
{
  "duration_seconds": 5,
  "target_mbps": 200,
  "modes": ["s"]
}
```

公开 Worker 会拒绝 Provider 返回的档位、时长、模式、地区或 IP 类型不一致的租约。探针
在本地测量候选节点 TCP 连接时间并激活合适节点。不同运营商的候选节点可以并行准备，
正式上传和下载按固定顺序执行，避免争抢用户服务器带宽。运营商代码只允许 `ct`（电信）、
`cu`（联通）和 `cm`（移动），暂不接受教育网节点。

社区节点租约的 `/activate` 请求会携带私有 Core 签发的 ES256 JWT。JWT 绑定 Node ID、用户
来源 IP、`100/200/400` 档位、单方向字节上限、5 秒时长、到期时间和一次性 `jti`。节点成功
激活后返回普通短期 key，后续下载、上传和释放格式与公开节点协议一致。

社区节点需要先准备来源限定的随机端口。首次请求可能返回：

```http
HTTP/1.1 202 Accepted
Retry-After: 1
Content-Type: application/json

{"status":"preparing","preparation":"lease_...","expires_at":1790727000}
```

客户端使用同一会话、地区和地址族再次请求，并附加 `preparation=lease_...`。在节点 ACK 前继续
返回 `202`；ACK 后才返回 `200` 和包含随机外部端口的正式租约。准备标识不能跨会话、地区或
地址族使用，也不额外消耗租约重试次数。准备失败、撤销或超时后返回错误，客户端不会获得固定
内部端口。

开源自用部署没有配置 `NODE_CORE` 时，Worker 会从 `STATIC_NODES` 读取用户自建节点并生成
相同格式的租约。静态节点必须实现 [`node-protocol.md`](node-protocol.md)，不能填写 Taier
或其它第三方协议节点。

## 上传结果

```http
POST /api/results
Authorization: Bearer SESSION_TOKEN
Content-Type: application/x-www-form-urlencoded
```

主要字段：

| 字段 | 内容 |
| --- | --- |
| `tested_at` | 测试完成 Unix 时间 |
| `regions` | 展示用省份名称 |
| `mode` | 固定为 `s` |
| `ip_mode` | 内部兼容字段：仅 IPv4 为 `v4`，任务包含 IPv6 时为 `v6` |
| `duration_seconds` | 固定为 `5` |
| `target_mbps` | `100`、`200` 或 `400` |
| `speed_data` | `sqprobe` 生成的 NDJSON |
| `speed_text` | 截断后的终端输出 |
| `traffic_rx_bytes` | 网卡接收字节差值 |
| `traffic_tx_bytes` | 网卡发送字节差值 |
| `nq_url` | 可选 NodeQuality 报告 |
| `nq_identity_reason` | NQ 身份校验依据：`full_ip` 或 `masked_ip_and_asn` |

Worker 会重新校验结构化测速字段，并拒绝超过目标速率 5% 容差、包含多线程结果或与会话
约束不一致的数据。动态模式下，报告保存后会把节点成功率、延迟、目标档位和实际吞吐反馈给
Provider。结果接口必须使用创建会话时取得的同源 Token；报告创建失败不影响已经显示在
终端的结果。Worker 会根据同源请求地址生成报告中的脱敏 IP 段：IPv4 只保留前两段，IPv6
只保留 `/48`；完整地址不会新增到报告表。

```http
GET /api/features
```

返回当前客户端能力开关。`nodequality_binding=false` 时，客户端不得读取 NodeQuality 报告；
Worker 会把旧客户端提交的关联字段降级为空并仅保存独立 SQ 报告。

## 动态 Provider Binding

公开 Worker 通过名为 `NODE_CORE` 的 Service Binding 调用私有 Provider：

```http
POST https://node-core.internal/lease
X-Node-Core-Secret: ...
Content-Type: application/json
```

Provider 输入包含：

```json
{
  "region": "hb",
  "family": "v4",
  "client_ip": "203.0.113.9",
  "client_asn": 64500,
  "duration_seconds": 5,
  "target_mbps": 200,
  "modes": ["s"]
}
```

Provider 应按物理节点而非逻辑标签控制并发，并根据 `target_mbps` 选择容量足够的节点。
生产 Provider 不配置公网 Route，只允许公开 Worker 通过 Service Binding 访问。开发环境可
使用 `examples/fixture-provider` 验证合同；该示例不包含任何生产节点或节点发现规则。
本节只面向需要动态节点发现和容量调度的平台维护者。使用 `STATIC_NODES` 的自用部署不需要
部署 Provider。

私有 Core 会先为每个运营商选择健康的 SQ 原生社区节点，只对缺失的运营商查询兼容目录。
指定 `community_node_id` 时只尝试该节点，失败不回退。设置
`LEGACY_FALLBACK_ENABLED=false` 后，普通调度也完全停止兼容目录回退。

报告保存后，公开 Worker 向 Provider 的 `/feedback` 发送每个已使用节点的
`lease_id`、`node_id`、`target_mbps`、状态、延迟及上下行速率。Provider 可据此按档位更新
健康数据并提前释放物理节点预留；没有成功提交报告的租约必须依靠自身过期时间释放。
