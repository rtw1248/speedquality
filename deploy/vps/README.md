# VPS 部署（v1.1）

适用于已有 Linux 服务器、希望减少 Cloudflare 用量的运营者。客户端用法、报告链接、社区节点协议不变。该方案需要 Docker Compose v2+、Nginx，以及 Cloudflare 托管的域名。程序使用 Node.js 24.14，包含在 Docker 镜像中，不需要给服务器安装 Node、Python、Redis 或 MySQL。

下面的双服务清单供持有私有 Core 的平台维护者使用，私有 Core 不包含在公开仓库中。
独立开发者可按照 [公开 API 文档](../../docs/api.md) 实现自己的节点服务；公开 Web 入口还支持 `STATIC_NODES` 配置，
不配置 `CORE_URL` 时可单独运行 `server.mjs`，但不提供官方社区节点调度与注册能力。

```text
用户 ──HTTPS──▶ Cloudflare ──HTTPS──▶ Nginx
                                      │
                               127.0.0.1:52800
                                      ▼
                               SQ Web / API
                               ├─ reports.sqlite
                               ├─ NQ 压缩快照
                               └─ 私有 Core :52801
                                  ├─ core.sqlite
                                  └─ 内存节点缓存

用户 ───────────测速流量────────────▶ 测速节点
```

Cloudflare 只承担 DNS、HTTPS 和缓存，不再为每次 API 请求执行 Worker。每个社区节点目前仍会每秒轮询控制接口，迁移后这些请求由 VPS 处理。后续可以优化控制通道。测速流量不会经过 VPS 或 Cloudflare。

## 1. 文件放在哪里

| 内容 | 位置 | 是否公开 |
| --- | --- | --- |
| Bash、Go 客户端、社区节点、Web、VPS 运行时 | `speedquality` GitHub 仓库 | 公开 |
| 上游目录、选点、调度、私有 Core 服务入口 | 独立 `speedquality-node-core` 仓库 | 私有 |
| 服务镜像 | VPS 本地 Docker | 镜像仅包含公开代码 |
| Core 代码 | VPS 的独立目录，只读挂载给 Core 容器 | 私有 |
| 配置、密钥、SQLite、NQ 快照 | VPS 的 `state/` | 禁止提交 Git |
| 域名、证书、DNS 代理 | Cloudflare + Nginx | 密钥只留服务器 |

推荐目录：

```text
/opt/speedquality/
  releases/v1.1.0/
    public/                 公开代码
    core/                   私有代码
  state/
    public.json             Web 私有配置
    core.json               Core 私有配置
    compose.env             路径、容器 UID/GID
    data/public/
      reports.sqlite        报告、会话、访问限额
      snapshots/            NodeQuality 的 .json.gz
      assets/               可删除并重新下载的发行包缓存
    data/core/
      core.sqlite           社区节点、凭据哈希、额度、租约
  backups/                  本机恢复副本
```

`state/` 与版本目录分开。默认每个容器最多使用 1 CPU、1 GiB 内存，整个 SQ 的容器上限是 2 CPU、2 GiB；这是配置上限，不是平时必然占用。SQLite 使用 WAL、事务、5 秒锁等待。先使用每种服务一个进程，禁止把 SQLite 放在 NFS/共享网络盘上。

## 2. 先检查服务器

通过 `ssh 你的服务器别名` 登录，可以使用密码。不要把密码写入脚本或发送到聊天中。

```bash
docker version
docker compose version
nginx -t
free -h
df -h /
ss -lnt
```

80/443 通常已经由 Nginx 监听。为 SQ 增加独立虚拟主机即可。API 端口默认只绑定 `127.0.0.1:52800`，Core 不发布任何宿主机端口，不需要在公网开放 52800/52801。

## 3. 上传发布代码并构建

在开发电脑执行（两个仓库需已提交相应版本）：

```bash
export SQ_SSH=你的服务器别名
export SQ_RELEASE=v1.1.0
export SQ_ROOT=/home/demo/project/tool/speedquality
export SQ_CORE=/home/demo/project/tool/speedquality-node-core
ssh "$SQ_SSH" "install -d /opt/speedquality/releases/$SQ_RELEASE/public /opt/speedquality/releases/$SQ_RELEASE/core"
git -C "$SQ_ROOT" archive "$SQ_RELEASE" | ssh "$SQ_SSH" "tar -xf - -C /opt/speedquality/releases/$SQ_RELEASE/public"
git -C "$SQ_CORE" archive HEAD | ssh "$SQ_SSH" "tar -xf - -C /opt/speedquality/releases/$SQ_RELEASE/core"
```

记录私有仓库的 `git rev-parse HEAD`，之后更新时使用确定的提交。`git archive` 不包含本机密钥、Git 认证和依赖目录。

然后在 VPS 上执行：

```bash
cd /opt/speedquality/releases/v1.1.0/public
docker build -f deploy/vps/Dockerfile -t speedquality-platform:1.1.0 .
docker run --rm --user 0:0 \
  -v /opt/speedquality:/opt/speedquality \
  speedquality-platform:1.1.0 node /app/deploy/vps/admin.mjs init \
  --dir /opt/speedquality/state \
  --origin https://sq.example.com \
  --core-dir /opt/speedquality/releases/v1.1.0/core \
  --github-owner 你的GitHub账号 --release v1.1.0
```

`init` 自动生成相互独立的凭据，配置权限为 0600；root 执行时默认分配给容器 UID/GID 10001。重复运行会拒绝覆盖。配置可编辑，修改后重启服务生效。`compose.env` 的 UID/GID 必须与数据和配置所有者一致；Core 代码需对该 UID 可读。

新平台可继续下一节。**已有平台迁移必须先完成第 7 节，不能用新生成的盐和 JWT 密钥替代旧凭据。**

## 4. 启动与检查

```bash
cd /opt/speedquality/releases/v1.1.0/public
docker compose --env-file /opt/speedquality/state/compose.env -f deploy/vps/compose.yaml up -d
docker compose --env-file /opt/speedquality/state/compose.env -f deploy/vps/compose.yaml ps
curl -fsS http://127.0.0.1:52800/health
```

应看到两个容器 `healthy`，健康接口返回 `ok`。健康接口用于本机检查；其他 API 必须经过持有代理密钥的 Nginx。直接 curl 本地业务接口返回 403 是预期行为。

常用配置：

| 配置 | 默认/用途 |
| --- | --- |
| `ORIGIN` | 对外 HTTPS 域名；生成的报告链接使用它 |
| `PROBE_VERSION`、`GITHUB_REF` | 已发布且签名完成的版本，不能填尚未发布的标签 |
| `NQ_BINDING_ENABLED` | `true`，开启 NQ 关联 |
| `RESULT_TTL_DAYS` | 90 天，过期报告和快照自动清理 |
| `DAILY_SESSION_LIMIT` | 同一来源每天 20 次新会话 |
| `GEOIP_URL` | 默认 `https://ipwho.is/{ip}`，识别客户端地区/ASN；结果缓存 6 小时 |
| `MAINTENANCE_MODE` | 空字符串正常服务；`drain` 停止新会话/注册；`readonly` 拒绝写入 |

第三方 GeoIP 有自己的使用限制，失败时显示未知，不能凭空填省份。上游目录可达性也需要从 VPS 实际验证。节点缓存上限 32 MiB / 5000 条，IP 信息缓存上限 8 MiB / 10000 条，丢失缓存可以重建。报告和社区节点登记不能丢失，必须备份 SQLite。

## 5. HTTPS 和 Nginx

在 Cloudflare 的域名设置里进入 **SSL/TLS → Origin Server → Create Certificate**，创建仅用于 `sq.example.com` 的 Origin CA 证书；私钥保存在 VPS，例如 `/etc/speedquality/tls/origin.key`，权限 0600。证书放在同目录 `origin.pem`。也可使用覆盖 SQ 域名的有效 Let's Encrypt 证书。

Origin CA 证书供 Cloudflare 验证源站使用，直接用浏览器访问源站通常不受信任。Cloudflare 应使用 **Full (strict)**；不要使用 Flexible。若同一个域名区域中其他站点还没有有效源站证书，先为 SQ 单独配置严格模式，避免影响其他站点。

生成配置的工具会读取最新 Cloudflare 代理网段，仅信任这些来源的 `CF-Connecting-IP`。它为转发请求附加私有凭据，后端不会信任用户自行提交的来源头。

```bash
docker run --rm --user 0:0 \
  -v /opt/speedquality/state:/config:ro \
  -v /etc/nginx/sites-available:/output \
  speedquality-platform:1.1.0 node /app/deploy/vps/nginx.mjs \
  --config /config/public.json --output /output/speedquality \
  --cert /etc/speedquality/tls/origin.pem --key /etc/speedquality/tls/origin.key
ln -s /etc/nginx/sites-available/speedquality /etc/nginx/sites-enabled/speedquality
nginx -t
systemctl reload nginx
```

配置文件含代理密钥，不能把 `nginx -T` 的完整输出发到论坛。访问日志记录请求 ID、路径、状态码和耗时，不记录完整来源 IP、查询参数、请求体或 Token。升级时重新生成到新文件、比较后替换，并刷新 Cloudflare 网段。

## 6. Cloudflare DNS

新部署：**DNS → Records → Add record**，类型 `A`，名称 `sq`，内容为 VPS IPv4，代理状态 **Proxied（橙云）**。没有配置 VPS 的 IPv6 入口时，不添加 AAAA。

如果 SQ 已绑定 Worker，先执行迁移步骤，最后才在 **Workers & Pages → speedquality → Settings → Domains & Routes** 移除 `sq.example.com` 自定义域名，再新增上述 A 记录。保留 Worker、D1、R2、KV，先不要删除。

缓存只应用于 `/bin/` 静态发行包。不要给 `/api/*`、`/r/*`、`/run` 配置 Cache Everything；沿用应用的 Cache-Control。普通 DNS/CDN 代理请求不消耗 Workers 调用配额，但仍须遵守 Cloudflare 免费服务条款，VPS 流量也受服务商套餐限制。

## 7. 从现有 Cloudflare 迁移

### 凭据与演练

先保存两个 Worker 当前配置、版本 ID、定时任务及域名绑定。**Worker secret 无法从 Dashboard 明文取回**，请使用部署时保存的原始凭据：

| 旧配置 | VPS 配置 |
| --- | --- |
| 公共 Worker `RATE_LIMIT_SALT` | `public.json` 同名项，保留会话和限额身份 |
| 公共 `NODE_CORE_SECRET` / 私有 `INTERNAL_SECRET` | 两个 JSON 对应项保持原值且相等 |
| Core `NODE_ID_SALT` | 保留节点身份和健康历史 |
| Core `NODE_CREDENTIAL_SALT` | 保留 API Token / Route Key 的校验能力 |
| Core `NODE_JWT_PRIVATE_JWK` | 连同 `kid`、issuer、audience 保持原值 |
| Nginx `PROXY_SECRET` | 新生成，只有 Nginx 和 Web 需要它 |

JWT 是签名令牌，依然需要密钥；它不能取代密钥本身。私有 JWK 只给 Core，发布签名私钥仍留在 GitHub Secrets/开发电脑，不需要放 VPS。

在开发电脑的公开仓库运行：

```bash
node deploy/vps/export-cloudflare.mjs --output deploy/vps/local/rehearsal
```

工具使用本机 Wrangler 登录，只读取两个 D1 与 R2；输出 SQL、已验证的 SQLite，以及未过期 NQ 快照。会比较列、约束和索引，检查数据库完整性。只有带 `export-complete.json` 的目录是完整导出。失败时保留目录以供排查，重试使用新目录。示例数据库/桶名称与本项目默认名称一致，其他运营者需在脚本中修改。

先把演练数据放在隔离目录启动 VPS，配置 `MAINTENANCE_MODE: "readonly"`。检查已有报告链接和 NQ 分页，确认 `/run`、发行包、Core 健康，不在生产数据库写入模拟结果。

### 最终切换

1. Cloudflare 公共 Worker 设置 `MAINTENANCE_MODE=drain` 并部署：暂停新会话和节点注册，让已经开始的测速完成。
2. 查询 D1：`SELECT COUNT(*) FROM sessions WHERE completed_at IS NULL AND expires_at > unixepoch();`。等到 0，最长需等现有会话过期，不能导出后继续接受旧库写入。
3. 公共 Worker 切到 `readonly`；暂时禁用两个 Worker 的定时触发器，等待在途请求完成。已有社区节点会暂时无法心跳/取租约，应尽量缩短这一阶段。
4. 再导出到一个全新目录，保留演练结果用于对比。传到 VPS；停止 SQ 两个容器，替换为新目录的数据，通过修改 `compose.env` 指向新目录。不要覆盖打开的 SQLite，不要只复制运行中的 `.sqlite` 主文件。
5. 校验报告数量、抽查已有链接，核对所有密钥/盐。VPS 仍保持 `readonly`，启动两个容器。
6. 移除 Worker 自定义域名、添加橙云 A 记录。确认公开 `/health`、旧报告、`/run` 已由 VPS 正常提供。确认 HTTPS 严格模式。
7. 清空 VPS 两个配置的 `MAINTENANCE_MODE`，重启 SQ 容器，恢复新会话；社区节点重新心跳即可恢复调度。

当前会话/租约也在 SQLite 中，但切换仍按“停止新任务→排空→冻结→导出”进行，以减少跨环境状态不一致。切换域名会有短暂维护窗口。

### 回滚

开放写入前可以停止 VPS，恢复原 Worker 域名绑定、变量和定时任务。**开放写入后，旧 D1 已经落后，直接切回会丢失新报告和节点状态。** 这时优先回退 VPS 的代码版本，继续使用当前兼容的 SQLite；若必须退回 Cloudflare，需要再次排空、冻结，再迁回新增数据和快照并验证。禁止直接用旧数据库覆盖新数据库。

## 8. 备份与恢复

每天备份一次；升级/迁移前再做一次。在 VPS 上：

```bash
export SQ_BACKUP=/opt/speedquality/backups/$(date -u +%Y%m%dT%H%M%SZ)
docker run --rm --user 0:0 \
  -v /opt/speedquality:/opt/speedquality \
  speedquality-platform:1.1.0 node /app/deploy/vps/admin.mjs backup \
  --public-dir /opt/speedquality/state/data/public \
  --core-dir /opt/speedquality/state/data/core --output "$SQ_BACKUP"
```

这是 SQLite 在线备份，包含报告、节点状态、未过期关联报告的快照和 SHA256 清单。缓存不备份。两个数据库分别取得一致快照；需要严格保持跨服务同一时刻状态时，先停止 SQ 两个容器再备份。缺少应有的 NQ 快照会退出 2，不能当作完整备份；可能是历史报告从未存入快照，需要先核实并修复。

将备份以及 `public.json`、`core.json`、`compose.env`、源站 TLS 私钥**加密后复制到另一台机器**。本机备份无法应对 VPS 磁盘丢失。可保留最近 7 份日备份和 4 份周备份；备份目录有敏感节点信息，不应公开上传 GitHub。

恢复时先停止容器，再运行 `admin.mjs restore --input BACKUP_DIR --public-dir NEW_EMPTY_PUBLIC --core-dir NEW_EMPTY_CORE`。恢复工具验证所有清单文件和 SQLite 完整性，拒绝额外快照、缺失快照和非空目标。将恢复目录所有者设为 Compose 中的 UID/GID，修改 `compose.env` 数据路径后启动。至少做一次恢复到隔离目录的演练。

## 9. 日常维护

```bash
docker compose --env-file /opt/speedquality/state/compose.env -f deploy/vps/compose.yaml ps
docker compose --env-file /opt/speedquality/state/compose.env -f deploy/vps/compose.yaml logs --tail=100 web core
docker stats --no-stream
df -h /opt/speedquality
```

Docker 日志每个容器最多 3 × 10 MiB；Nginx 日志沿用系统 logrotate。优先关注健康状态、5xx/429、响应耗时、磁盘余量、备份成功与证书到期。每小时清理过期报告，每 5 分钟维护 Core 健康状态；定时任务不重叠。数据库事务容量限制由 SQLite 执行，不能用内存缓存代替额度计数。

升级：准备新版本目录→运行测试→备份→构建新镜像→更新 `compose.env` 的 Core 代码路径→使用新版本 compose 启动→抽查健康和旧报告。自动迁移仅应用未执行的 SQL，已执行迁移被修改会拒绝启动。涉及不兼容数据库迁移时，须另列升级/回滚步骤。

先观察真实负载再估算容量，不能仅用 CPU/内存换算可承载用户数。该 VPS 是单机部署，停机时 API 和报告都会受影响；测速节点本身的带宽和可用性依然独立限制用户测速体验。
