# 日志与并发排障

SpeedQuality 使用同一组关联字段串起一次测速，但不会把 Session Token、Route Key、API Token、
JWT、激活 key、完整来源 IP 或测速节点地址写入日志。

| 组件 | 日志位置 | 主要关联字段 |
| --- | --- | --- |
| 公开 Worker | Cloudflare Workers Logs | `request_id`、`session_id`、`lease_id`、`report_id` |
| 私有 Node Core | Cloudflare Workers Logs | `request_id`、`lease_id`、Node ID 前 8 位 |
| `sq-node` | systemd journal / stderr | `request_id`、不可逆 `task_id`、Node ID 前 8 位 |
| `sqprobe` | 用户显式指定的本地 JSONL | `lease_id`、Node ID、阶段和耗时 |

公开 Worker 会把自己的 `request_id` 传给私有 Core。Core 签发的 `lease_id` 会出现在探针结果、
健康反馈和报告日志中。社区节点把 JWT 的一次性 `jti` 做 SHA-256 后只取 12 个十六进制字符
作为 `task_id`，可关联该节点上的激活、上传、下载和释放事件，无法还原原凭据。

## 节点日志

查看最近日志：

```bash
sudo sq-node logs
sudo sq-node logs 500
```

也可以直接查询 journald：

```bash
sudo journalctl -u speedquality-node.service -o cat --since "30 min ago"
sudo journalctl -u speedquality-node-gate.service -o cat --since "30 min ago"
sudo journalctl -u speedquality-node.service -o cat | jq -c \
  'select(.event == "activation.rejected" or .event == "state.lock_stalled")'
```

主要事件：

| 事件 | 用途 |
| --- | --- |
| `activation.accepted/rejected/released` | 判断鉴权、并发、公共额度、可用时间和状态落盘问题 |
| `transfer.started/completed/rejected` | 判断某方向是否开始、耗时多久、实际传输了多少字节 |
| `heartbeat.failed/recovered` | 判断节点和平台之间的暂时或持续通信故障 |
| `state.persist_failed/slow` | 判断磁盘权限、空间、文件系统延迟 |
| `state.lock_wait_slow` | 某个 goroutine 等待状态锁超过 250ms |
| `state.lock_held_slow` | 某次临界区持锁超过 500ms |
| `state.lock_stalled` | 状态锁连续持有超过 2 秒，疑似阻塞或死锁 |
| `runtime.goroutine_dump` | 自动或手工生成的 Go goroutine 栈，按 `dump_id` 和 `chunk` 拼接 |
| `gate.started/stopped` | 判断临时端口网关是否正常启动或被隔离 |
| `gate.control_failed/recovered` | 判断节点能否取得 Core 的租约准备命令 |
| `gate.lease_opened/closed/open_failed` | 判断脱敏租约是否完成主机防火墙和随机端口动作 |
| `gate.proxy_dial_failed` | 判断低权限回环测速服务是否不可达 |

检测到 `state.lock_stalled` 时，程序会自动抓取一次全部 goroutine 栈。也可以在进程仍有响应时
手工抓栈，`SIGUSR1` 不会停止服务：

```bash
sudo systemctl kill -s SIGUSR1 speedquality-node.service
sudo journalctl -u speedquality-node.service -o cat -n 200
```

匿名 `/healthz` 只返回 `status` 和 `protocol_version`。当前任务、公共与总流量、预留、紧急
隔离和临时网关状态只通过带 API Token 的认证心跳上报；提供者可运行 `sudo sq-node telemetry`
查看下一次心跳的完整 JSON，或运行 `sudo sq-node status` 查看本机汇总。

## Worker 与 Core

两个 Worker 的 `wrangler.toml` 默认设置 `LOG_LEVEL = "info"`。实时查看：

在公开仓库根目录执行：

```bash
(cd deploy/cloudflare-worker && npx wrangler tail --format json)
(cd ../speedquality-node-core && npx wrangler tail --format json)
```

排查并发容量时先在公开 Worker 搜索 `lease.delivered`，再用相同 `request_id` 查看 Core 的
`lease.created` 和 `community.reservation_decided`。后者记录原子预留是否成功、任务范围、速度
档位、总并发和公共槽位。失败请求还会记录稳定的 `reason`，例如
`node_capacity_exhausted`、`public_quota_exhausted` 或 `specified_node_unavailable`。

生产环境可把 `LOG_LEVEL` 改为 `warn` 降低日志量；调查问题时改回 `info`。`off` 会关闭应用
日志，不影响 Cloudflare 自身的请求指标。

## 客户端诊断

普通测速不在用户服务器留下日志文件。需要复现客户端选点或激活问题时，可显式指定一个由
用户控制的位置：

```bash
SPEEDQUALITY_DIAGNOSTIC_LOG=/tmp/sq-diagnostic.jsonl \
  bash <(curl -fsSL https://sq.yolo2.cc/run) -p hb -s 200
```

该文件权限会尽量设为 `0600`。它只记录候选数量、阶段耗时、匿名 Node ID、字节数、速率和
归类后的网络错误，不记录候选地址、请求 URL 或激活凭据。脚本不会删除用户指定的日志。

## 能力边界

日志可以定位容量竞争、重复 JWT、释放遗漏、慢磁盘、慢锁和大多数锁卡住位置。日志本身不能
证明所有并发代码都正确，因此发布检查仍需执行 Go race detector；进程被操作系统直接杀死、
主机断电或日志保留期已过时，最后一段事件可能不存在。Cloudflare D1 的容量控制使用单条
条件写入，排障关注的是该原子决策及其输入，不存在常驻进程内的互斥锁死锁。

## VPS 部署补充

`deploy/vps/` 沿用公开层和 Core 的结构化日志及请求 ID。Nginx 配置生成器将请求 ID 传入 Web；
Web 再传入 Core。Docker 默认每个容器保留 3 个 10 MiB 日志文件。
用 `docker compose logs --tail=100 web core` 排查 5xx、上游超时和数据库锁等待；不公开配置文件。
`vps.request_failed`、`vps.scheduled_failed` 和 `vps.shutdown_failed` 提供运行时错误类型。
`/health` 表示进程可响应及依赖已配置，不证明所有第三方节点可用；应同时检查旧报告可读取、
磁盘可写、备份成功和 Core 状态。生产验收不生成假报告，也不通过重复测速做压力测试。
