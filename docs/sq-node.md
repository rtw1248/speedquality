# sq-node 部署与管理

`sq-node` 是 SpeedQuality 社区节点服务。它在节点服务器上直接提供上传和下载字节流，实际
测速流量由用户服务器直连该节点，不经过 Cloudflare Worker 或私有 Core。

当前版本已实现短期 JWT、来源 IP 绑定、并发和总流量硬限制、低权限 systemd 服务、SSH
安全预检、一次性 nftables 租约、Route Key 轮换、签名更新和紧急隔离。真实发行版、云安全组、
IPv4/IPv6 nftables 和公网并发尚需在私有 Alpha 验收。完成这些部署验收前，只应使用自有节点。
完整安全边界见
[`community-node-security.md`](community-node-security.md)。

## 工作流程

```text
用户服务器      公开 Worker      私有 Core       节点网关
    │               │               │               │
    │ 申请租约      │               │               │
    ├──────────────▶├──────────────▶│               │
    │◀─ 202 准备中 ─┤◀──────────────┤               │
    │               │◀──────────────────────────────┤ 认证轮询
    │               ├──────────────▶│               │
    │               │◀──────────────┤               │
    │               ├──────────────────────────────▶│ 下发命令
    │               │               │               │ 写 nftables
    │               │               │               │ 启动随机端口
    │               │◀──────────────────────────────┤ ACK
    │               ├──────────────▶│               │
    │ 再次申请租约  │               │               │
    ├──────────────▶├──────────────▶│               │
    │◀─ JWT/随机端口┤◀──────────────┤               │
    │ HTTP 测速（仅租约来源可达）                      │
    ├────────────────────────────────────────────────▶│
```

注册时，平台要求注册请求的来源 IP 与登记地址一致，并从公网访问固定端口上的一次性所有权
挑战。地区还会经过平台独立校验。正常运行时固定端口只监听 `127.0.0.1`；带
`CAP_NET_ADMIN` 的独立网关单元只校验来源、维护临时规则和转发原始 TCP 字节，不解析用户的
HTTP。低权限主服务验证本机 HMAC 前导后恢复真实来源并处理 HTTP。每次测速还使用绑定节点、
用户来源 IP、速度档位、字节上限和到期时间的短期 ES256 JWT。

## 安装

推荐从已经部署的 SpeedQuality 域名执行一条命令：

```bash
bash <(curl -fsSL https://sq.yolo2.cc/install-node)
```

安装器会自动识别 Linux `amd64/arm64`、下载并校验 Release 二进制、安装到
`/usr/local/bin/sq-node`，随后进入首次设置向导。向导自动检测公网地址、地区和运营商，
完成所有权注册并安装 systemd 后台服务与更新定时器，不需要编辑 JSON 或浏览器管理页面。

向导自动生成以下摘要，正常情况下只需按一次 Enter：

| 配置 | 是否必须 | 说明 |
| --- | --- | --- |
| 省份 | 自动 | 根据平台看到的公网来源和独立地理数据识别 |
| 运营商 | 自动 | 根据来源 ASN/组织识别；无法可靠判断时才询问 |
| 公网 IP | 自动 | 分别经 IPv4 和 IPv6 请求平台，以实际连通结果为准 |
| 内部 TCP 端口 | 自动 | 从 `50000-59999` 选择空闲端口；日常只监听回环地址 |
| 最高档位 | 默认值 | 初始 `100 Mbps`；更高公共档位需要平台认证 |
| 最大/保留并发 | 默认值 | 默认总并发 `1`、保留并发 `0` |
| 每日公共流量 | 默认值 | 默认 `10GB` |
| 每日总流量 | 默认值 | 默认 `20GB`，同时覆盖公共任务和 Route Key 任务 |
| 访问模式 | 有默认值 | 默认 `public`，可随时改成 `private` 或 `paused` |
| 可用时间 | 默认值 | 默认 `always`，可以设置每日时间段和时区 |
| 自动更新 | 默认值 | 默认 `automatic/stable`，只安装签名更新 |

向导显示摘要后，必须在云平台安全组允许 `50000-59999/TCP`。主机上的 nftables 会让随机
外部端口只对当前租约来源开放，并在最长 3 分钟后删除；不要再添加绕过该规则的主机级宽泛
放行。注册时平台会短暂访问摘要中的固定端口完成所有权验证。NAT 环境需要转发整个端口范围，
因此不建议用于公共节点。这些云侧规则无法由安装器代替用户完成。

只安装程序、不立即配置：

```bash
bash <(curl -fsSL https://sq.yolo2.cc/install-node) --install-only
sudo sq-node setup --platform https://sq.yolo2.cc
```

如需手工从 GitHub Release 安装，把示例中的仓库地址和版本换成实际值：

```bash
SQ_VERSION=v1.0.0
SQ_ARCH=amd64
[ "$(uname -m)" = "aarch64" ] && SQ_ARCH=arm64

curl -fLO "https://github.com/OWNER/REPO/releases/download/${SQ_VERSION}/sq-node-linux-${SQ_ARCH}"
curl -fLO "https://github.com/OWNER/REPO/releases/download/${SQ_VERSION}/checksums.txt"
grep " sq-node-linux-${SQ_ARCH}$" checksums.txt | sha256sum -c -
sudo install -m 0755 "sq-node-linux-${SQ_ARCH}" /usr/local/bin/sq-node
```

客户端和节点端都提供帮助：

```bash
sq-node --help
sq-node setup --help
```

## 第一次自测

先用自己控制的节点短时测试，设为 `private`；验证成功后再决定是否开放公共调度。
需要 Linux `amd64/arm64`、root 或 sudo、systemd、可用的 `nft`，以及平台能直接连接的公网地址。
节点地区和运营商必须与真实信息一致；例如美西服务器可以作为测速请求方，不能登记为湖北节点。
安装器不自动安装系统依赖或更改 SSH 设置；缺少 `nft` 时应先由提供者按所用发行版安装 nftables。

在**提供测速服务的节点服务器**执行：

```bash
bash <(curl -fsSL https://sq.yolo2.cc/install-node) --install-only
sudo sq-node setup --platform https://sq.yolo2.cc \
  --access private --speed 100 --daily 1GB --total 2GB
```

向导显示自动检测结果后，核对地区、运营商和公网地址，在云安全组允许 `50000-59999/TCP`，
再按 Enter 完成注册和启动。主机现有防火墙也不能阻断所有权验证端口或合法租约连接；
不要清空现有规则，也不要把整个范围直接向公网永久放行。正常测速由网关按来源临时开放端口。

检查服务：

```bash
sudo sq-node status
sudo sq-node diagnose
```

应看到“注册：是”“临时租约网关：就绪”“访问模式：private”，并取得 `Route Key: sqn_...`。
网关尚未就绪时先看 `sudo sq-node logs 100`，不要只凭注册成功判断测速可用。

在**另一台待测服务器**执行，将示例 Key 换成自己的：

```bash
bash <(curl -fsSL https://sq.yolo2.cc/run) --node sqn_YOUR_ROUTE_KEY -s 100 -v4
```

首次显式选择 `100 Mbps`，与节点默认最高档位一致；不传 `-p`，自动使用该节点登记省份。
这次只测试指定节点对应的运营商。成功标准是出现有效的上传/下载结果和报告链接；节点端
`status` 中今日总流量增加、测试结束后当前任务归零。双方均具备 IPv6 时，可再用 `-v6` 单独测试。

测试后不继续运行时，在节点服务器执行：

```bash
sudo sq-node emergency-stop
```

该命令暂停平台调度并停止节点服务和临时网关。若不再保留注册，按下文“注销与移除服务”处理。
当前网关仍每秒轮询控制接口；平台尚使用 Cloudflare Workers 时，不应把短时验收直接当作
免费额度下可长期运行的结论，持续运行前需完成控制通道优化或 VPS 迁移。

## 配置文件

以 root 运行时，配置和状态分别保存在：

```text
/etc/speedquality/node.json
/etc/speedquality/node-state.json
```

文件权限为 `0600`。非 root 运行时使用当前用户的配置目录，也可以通过
`SQ_NODE_CONFIG=/path/node.json` 指定位置。

## 日常管理

无参数运行会打开编号菜单。节点服务本身在 systemd 后台运行，菜单退出不会停止测速服务：

```bash
sudo sq-node
```

一键向导内部依次执行初始化、注册和后台服务安装。需要自动化部署时，也可以分别使用子命令：

```bash
sudo sq-node init \
  --platform https://sq.yolo2.cc \
  --region hb \
  --carrier ct \
  --ipv4 YOUR_PUBLIC_IPV4 \
  --speed 100 \
  --concurrency 1 \
  --reserve 0 \
  --daily 10GB \
  --total 20GB \
  --availability always \
  --timezone Local \
  --access public

sudo sq-node status
sudo sq-node register
sudo sq-node service install
sudo sq-node diagnose
sudo sq-node logs 200
sudo sq-node preflight
sudo sq-node telemetry
```

注册成功会显示：

```text
Node ID: 0123456789abcdef0123456789abcdef
Route Key: sqn_...
```

`Route Key` 用于测速路由，可以传给受信任的使用者。节点 API Token 仅用于认证心跳、临时
端口控制、Route Key 管理和注销，只保存在本机 `0600` 配置文件中，不应复制到 Bash 测速
命令、聊天记录或网页。
平台数据库只保存两种凭据各自加盐后的 SHA-256 哈希；API Token 只在注册响应中返回一次。

## 使用指定节点

节点注册后，可在任意待测 Linux 服务器上使用 Route Key：

```bash
bash <(curl -fsSL https://sq.yolo2.cc/run) \
  --node sqn_YOUR_ROUTE_KEY -s 100
```

未传 `-p` 时自动采用节点登记省份。显式 `-p` 必须恰好是该省份；`bsg` 和多省组合会被
拒绝。测速档位不能超过节点登记能力，显式使用 `-v4` 或 `-v6` 时也要求节点登记了对应的
公网地址。默认模式只会测试用户服务器与该节点共同支持的 IP 类型。指定节点离线、
暂停或满载时直接失败，不会悄悄切换到其它节点。

## 访问与额度

| 配置 | 含义 |
| --- | --- |
| `public` | 参加公共调度，也允许 Route Key 精确访问 |
| `private` | 不参加公共调度，只允许 Route Key 精确访问 |
| `paused` | 拒绝所有新的测速任务 |
| `speed` | 节点允许的最高档位：`100/200/400` Mbps |
| `concurrency` | 节点所有任务合计的最大并发 |
| `reserve` | 从总并发中为 Route Key 任务保留的数量 |
| `daily` | 每个 UTC 自然日允许公共任务使用的流量；`0` 表示关闭公共额度 |
| `total` | 每个 UTC 自然日所有任务合计的硬上限，必须不小于公共额度 |

默认单并发时不长期预留空闲位置；Route Key 不计入每日公共额度，但仍受每日总额度约束。
若精确访问时位置正被公共任务占用，平台会为该节点建立短暂的提供者优先窗口：当前位置释放后，
新的公共任务不能立即抢占，提供者再次请求可先取得位置。提高到总并发 3、保留并发 1 后，普通
公共调度最多占用 2 个位置，Route Key 任务仍受总并发 3 的限制。激活时先预留最坏情况流量，
释放时再按实际上传和下载字节结算；异常重启会保守结算未完成预留，避免重启绕过额度。

新节点注册后处于 `observing`。临时网关就绪后 Route Key 可使用；平台连续收到默认三次认证
心跳后切换为 `active`，同时满足新鲜心跳、`firewall_ready=true` 和其它容量条件时才加入公共
调度。公共调度只使用平台认证档位；节点本地把上限调到 200 或 400 Mbps 不会绕过认证。

常用管理命令：

```bash
sudo sq-node access public
sudo sq-node access private
sudo sq-node access paused
sudo sq-node limit speed 200
sudo sq-node limit concurrency 3
sudo sq-node limit reserve 1
sudo sq-node limit daily 50GB
sudo sq-node limit total 100GB
sudo sq-node schedule 08:00-23:00 Asia/Shanghai
sudo sq-node heartbeat
sudo sq-node status
sudo sq-node telemetry
sudo sq-node route-key rotate
sudo sq-node route-key revoke
sudo sq-node preflight
sudo sq-node emergency-stop
sudo sq-node emergency-stop --recover
```

修改访问方式或额度后会立即保存，并在节点已注册时尝试同步平台；临时同步失败会由后台心跳
重试。平台连续收不到心跳、临时网关未就绪或测速反馈连续失败时，会停止调度该节点。已经注册后不能
直接修改平台地址、公网 IP、端口、地区、运营商或标签；这些字段变化需要先注销，再重新
注册并完成一次新的所有权和地区验证。

节点服务把结构化 JSON 日志写入 systemd journal，包含测速任务生命周期、容量拒绝、心跳、
状态落盘和慢锁事件，不包含任何长期或短期凭据。并发卡住超过 2 秒时会自动保存 goroutine
栈。完整事件表和 Worker/Core 联合排查方法见 [`observability.md`](observability.md)。

## 服务与安全状态

`service install` 创建两个常驻单元：

| 单元 | 权限与职责 |
| --- | --- |
| `speedquality-node.service` | `speedquality` 低权限账户；仅监听 `127.0.0.1`，验证 JWT、执行限速并处理字节流 |
| `speedquality-node-gate.service` | 同一低权限账户加 `CAP_NET_ADMIN`；认证轮询、维护专用 nftables 表、校验来源并转发原始 TCP |

网关向本机连接写入与 API Token 派生的 HMAC 前导，主服务只在回环连接上接受并验证它。外部
HTTP 头不能伪造来源。网关不解析公网用户的 HTTP，也不能更改 SpeedQuality 专用表之外的
防火墙规则。固定内部端口不会在正常运行时绑定公网地址。

`sq-node preflight` 只读检查 SSH 有效配置、公网监听、高风险端口和 nftables，不会修改 SSH、
重启其它服务或安装软件。密码或键盘交互登录、缺少 nftables 等严重项会阻止切换到 `public`。
`sq-node telemetry` 打印下一次认证心跳的完整 JSON，便于提供者确认平台实际收到哪些运行字段；
其中不含 API Token、Route Key、用户来源、节点公网地址、用户文件或其它端口连接。

遇到滥用时，`emergency-stop` 会持久化 `paused`、停止随机端口网关、清理专用 nftables 表、
结算活动预留并通知平台撤销待确认租约。机器重启不会自动解除。`--recover` 会先重新执行安全
预检，并只恢复到 `private`，需要提供者再明确执行 `sq-node access public`。

## 签名更新

安装 systemd 服务时会同时创建一个带随机延迟的六小时更新定时器。平台只返回版本和下载
位置；节点使用编译进二进制的 Ed25519 公钥验证发布清单，再按清单校验二进制 SHA-256。
替换采用同目录原子重命名。更新前先停止特权网关，新主服务在 20 秒内通过本地健康检查后才
恢复网关；主服务或网关启动失败时，会同时恢复上一版二进制、主服务和网关。

```bash
sudo sq-node update --check
sudo sq-node update
sudo sq-node update --mode automatic
sudo sq-node update --mode manual
```

自动更新只替换 `sq-node` 自身，不安装系统包、不升级系统库，也不修改测速用户环境。

## 注销与移除服务

```bash
sudo sq-node unregister
sudo sq-node service remove
```

`unregister` 会先停止并禁用两个节点服务，再让平台撤销 Node ID、Route Key、API Token 和
活动租约；节点程序、配置、状态和 systemd 单元会保留。`service remove` 会停止并删除两个
服务及更新定时器，并清理 SpeedQuality 专用 nftables 表，同样不会卸载用户软件或删除二进制、
配置和状态。需要重新加入平台时再次执行 `register`，平台会签发一组新凭据并恢复两个服务。

## 与静态自用协议的关系

社区模式的 `/activate` 必须携带平台签发的短期 JWT，因此 `sq-node` 不能直接作为
`STATIC_NODES` 中的无鉴权节点使用。个人完全自建时，仍可按照
[`node-protocol.md`](node-protocol.md) 实现无平台 JWT 的四接口服务，并由自己的 Worker
生成静态租约；该模式不需要部署私有 Core。
