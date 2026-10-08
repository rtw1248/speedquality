# Cloudflare Worker 部署

公开 Worker 提供以下入口：

| 路径 | 作用 |
| --- | --- |
| `GET /run` | 返回注入当前服务域名的一键 Bash 脚本 |
| `GET /install-node` | 返回注入当前服务域名和版本的节点安装器 |
| `GET /bin/<version>/<asset>` | 代理指定版本的二进制、校验清单和签名发布清单 |
| `GET /api/nodes/detect` | 返回节点安装请求的公网地址、地区和运营商候选 |
| `GET /api/nodes/update` | 返回当前稳定版本及签名清单位置 |
| `GET /api/features` | 返回客户端可选能力的运营开关 |
| `POST /api/session` | 创建与来源 IP 绑定的短期会话 |
| `POST /api/node-lease` | 从静态配置或私有 Provider 获取单地区租约 |
| `POST /api/results` | 保存结构化测速结果和可选 NodeQuality 快照 |
| `POST /api/nodes/register` | 注册社区节点并转发真实来源 IP |
| `POST /api/nodes/heartbeat` | 使用节点 API Token 同步容量和状态 |
| `POST /api/nodes/unregister` | 撤销社区节点凭据 |
| `POST /api/nodes/route-key` | 单独轮换或撤销 Route Key |
| `POST /api/nodes/control` | 节点认证轮询临时端口命令 |
| `POST /api/nodes/control/ack` | 节点确认临时 nftables 和网关状态 |
| `POST /api/nodes/resolve` | 用 Route Key 解析可用节点约束 |
| `GET /r/<id>` | 展示联合报告 |
| `GET /health` | 检查 D1、R2 和节点来源配置 |

## 1. 配置节点来源

### 自用静态节点

按照 [`../../docs/node-protocol.md`](../../docs/node-protocol.md) 实现自己的测速节点，然后把
节点写入 `STATIC_NODES`。此模式不需要 Provider 或 Service Binding，适合个人自用和协议
开发。

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

支持主 README 中的全部省份代码，单次会话最多选择 5 个省份。运营商只支持 `ct`（电信）、
`cu`（联通）和 `cm`（移动），每组最多取 4 个候选节点。`max_mbps` 必须为 `100`、`200` 或
`400`。可以增加不超过 32 个字符的 `label`，以及
`0` 至 `10000` 的 `ready_delay_ms`。示例 IP 不能直接使用。

不要在 `wrangler.toml` 或 GitHub 中提交真实节点清单。部署前执行
`npx wrangler secret put STATIC_NODES`，按提示粘贴单行 JSON。静态模式不做跨用户原子容量
预留，公开给大量用户前应切换到动态模式。

### 官方动态节点

需要动态发现、健康评分和容量预留时，部署兼容 [`../../docs/api.md`](../../docs/api.md) 的
Provider Worker：

- 不配置公网 Route，并设置 `workers_dev = false`。
- 设置 `INTERNAL_SECRET`。
- 只通过公开 Worker 的 Service Binding 接收请求。
- 回显并校验 `target_mbps`，只接受固定 5 秒、单线程租约。
- 按物理 IP 限制活跃租约，并为多省任务和 400 Mbps 档位保留容量。
- 使用原子预留，且让未反馈的预留在短时间内自动过期。

开发阶段可以使用 [`../../examples/fixture-provider`](../../examples/fixture-provider/) 验证
动态 Provider 合同。官方动态适配器负责把上游原始协议转换为通用租约；第三方节点不能放入
`STATIC_NODES` 冒充 SQ 自建节点。

## 2. 配置公开 Worker

安装 Wrangler 并登录：

```bash
cd deploy/cloudflare-worker
npm install
npx wrangler login
```

修改 `wrangler.toml`：

```toml
name = "speedquality"
routes = [{ pattern = "sq.yolo2.cc", custom_domain = true }]

[vars]
GITHUB_OWNER = "YOUR_GITHUB_USER"
GITHUB_REPO = "speedquality"
GITHUB_REF = "v1.0.0"
PROBE_VERSION = "v1.0.0"
RESULT_TTL_DAYS = "90"
DAILY_RESULT_LIMIT = "100"
DAILY_SESSION_LIMIT = "20"
NQ_BINDING_ENABLED = "false"
PROMOTION_TEXT = "SpeedQuality 社区节点计划"
LOG_LEVEL = "info"
# PROMOTION_URL = "https://example.com/promotion"

```

只有动态模式需要另外加入：

```toml
[[services]]
binding = "NODE_CORE"
service = "speedquality-node-core"
```

## 3. 创建 D1 和 R2

```bash
npx wrangler d1 create speedquality-results
npx wrangler r2 bucket create speedquality-nodequality
```

把返回的 D1 ID 填入 `wrangler.toml`，启用绑定：

```toml
[[d1_databases]]
binding = "DB"
database_name = "speedquality-results"
database_id = "这里填写 D1 ID"
migrations_dir = "migrations"

[[r2_buckets]]
binding = "SNAPSHOTS"
bucket_name = "speedquality-nodequality"
```

项目尚未部署时，下面的命令只负责按编号依次创建初始空表：

```bash
npx wrangler d1 migrations apply speedquality-results --remote
```

当前会依次执行 `0001_results.sql`、`0002_community_node_sessions.sql` 和
`0003_lease_preparations.sql`。这里的“迁移”是 Cloudflare 对有序建表或改表文件的统一名称，
不表示已经存在旧数据库；全新数据库也要执行全部迁移。后续升级仍使用这条命令，Wrangler
只执行尚未应用的文件。

## 4. 设置 Secret

```bash
npx wrangler secret put RATE_LIMIT_SALT
```

自用静态模式还需设置节点 JSON：

```bash
npx wrangler secret put STATIC_NODES
```

动态模式改为设置 `NODE_CORE_SECRET`，其值必须和 Provider 的 `INTERNAL_SECRET` 相同：

```bash
npx wrangler secret put NODE_CORE_SECRET
```

所有 Secret 都使用独立随机值或私有配置，不要提交到 Git。同时配置 `NODE_CORE` 和
`STATIC_NODES` 时，动态 Provider 优先。

社区注册、`--node` 和 SQ 原生节点调度都依赖 `NODE_CORE`。公开 Worker 不保存节点 API
Token 或 Route Key，只转发 API Token，并在 D1 会话中保存私有 Core 返回的不透明 Node ID。

`PROMOTION_TEXT` 和 `PROMOTION_URL` 控制报告正文下方的推广位，未设置链接时仍保留推广
文案。报告会根据 `GITHUB_OWNER` 和 `GITHUB_REPO` 另行展示 GitHub 项目地址，避免把开源
仓库标成推广内容。

公开 Worker 使用 JSON 结构化日志，并把 `request_id` 传给私有 Core。生产排障命令、关联字段
和脱敏范围见 [`../../docs/observability.md`](../../docs/observability.md)。

## 5. 发布二进制

先在可信机器生成一次 Ed25519 发布密钥：

```bash
go run tools/release-manifest/main.go -generate-key
```

把输出中的 `private_key` 保存为 GitHub Actions Secret `SQ_RELEASE_PRIVATE_KEY`，把
`public_key` 保存为 Actions Variable `SQ_RELEASE_PUBLIC_KEY`。私钥不能提交到仓库、Worker
变量或节点配置；公钥会在构建时写入 `sq-node`，用于验证后续版本。

仓库的 `release.yml` 会在推送 `v1.0.0` 形式的标签后构建并签名：

```text
sqprobe-linux-amd64
sqprobe-linux-arm64
sq-node-linux-amd64
sq-node-linux-arm64
checksums.txt
release-manifest.json
release-manifest.json.sig
```

`PROBE_VERSION` 必须指向已存在的 Release。首次发布示例：

```bash
git tag v1.0.0
git push origin v1.0.0
```

`release-manifest.json` 记录每个二进制的大小和 SHA-256，`.sig` 是对清单原始字节的 Ed25519
签名。`/run` 会把这个版本写入脚本，探测器下载 URL 也包含版本号，因此更新 Release 后不会
与 Cloudflare 中旧版本的文件缓存混用。节点更新器只接受与平台同源的下载地址和签名有效的
清单。

## 6. 部署和检查

```bash
npx wrangler deploy

curl -i https://sq.yolo2.cc/health
bash <(curl -fsSL https://sq.yolo2.cc/run) --version
bash <(curl -fsSL https://sq.yolo2.cc/run) --help
bash <(curl -fsSL https://sq.yolo2.cc/install-node) --help
```

健康响应应包含：

```text
x-report-store: configured
x-snapshot-store: configured
x-node-service: configured
```

## 自动部署

GitHub Actions 需要：

- `CLOUDFLARE_API_TOKEN`
- `CLOUDFLARE_ACCOUNT_ID`

API Token 需要 Workers Scripts、D1、R2 和对应域名权限。Worker Secret 由 Wrangler 单独
维护，后续 Actions 部署不会覆盖。

在 Actions 页面手工运行 `deploy-speedquality-worker` 时，`release_ref` 必须填写已经由
`release.yml` 创建的签名 Release tag，例如 `v1.0.0`。工作流会拒绝非法或不存在的 tag，
并把 `/run` 脚本来源和探针版本同时固定到该版本。

## 配额与保留

| 配置 | 默认值 | 说明 |
| --- | ---: | --- |
| `RESULT_TTL_DAYS` | `90` | 报告保留天数 |
| `DAILY_RESULT_LIMIT` | `100` | 每个来源每天最多创建的报告数 |
| `DAILY_SESSION_LIMIT` | `20` | 每个来源每天最多创建的会话数 |
| `NQ_BINDING_ENABLED` | `false` | 是否允许读取、绑定并保存新的 NodeQuality 报告快照；基础测速验收后再开启 |

将 `NQ_BINDING_ENABLED` 设为 `false` 后，`/api/features` 会立即通知新版客户端停止读取
NodeQuality；Worker 也会把旧客户端提交的关联结果强制降级为独立 SQ 报告，且不保存快照。
已有联合报告仍按原到期时间展示。

报告请求体最多 512 KiB，结构化测速数据最多 192 KiB，终端文本最多 16 KiB，NodeQuality
快照 JSON 最多 256 KiB，其中 ANSI 文本合计最多 192 KiB。容量估算见
[`../../docs/capacity.md`](../../docs/capacity.md)。

## 本地验证

```bash
npm test
npm run check
npm run preview:venv
```

SpeedQuality 分页预览地址：

```text
联合报告中的 SQ 页：http://127.0.0.1:4173/report-speed.html
独立 SQ 报告：      http://127.0.0.1:4173/report-standalone.html
```

使用 `sqprobe nq-verify` 生成的真实安全快照预览五个 NodeQuality 分页：

```bash
SPEEDQUALITY_PREVIEW_SNAPSHOT=/path/to/nodequality-snapshot.json npm run preview:venv
```

也可以将快照保存为仓库根目录的 `.cache/nodequality-snapshot.json`，预览脚本会自动读取。
`.cache/` 已被 Git 忽略，不会把真实报告数据提交到公开仓库。未提供快照时才使用精简示例。

默认入口 `http://127.0.0.1:4173/report.html` 对应“全部”页，
`report-basic.html` 对应“基本信息”页。
