# SpeedQuality

SpeedQuality 是面向 Linux 服务器的分省单线程测速工具。它可以按省份和运营商测试
`100/200/400 Mbps` 档位，直接在终端显示彩色结果，并生成可分享的网页报告；也可以把经过
服务器身份和检测时间校验的 NodeQuality 报告追加为联合报告。

> 当前为公网 Alpha。一键命令已可使用；兼容节点受第三方服务的可用性、鉴权和限流
> 影响，结果页和社区节点功能仍在持续验收。

## 快速运行

Linux `amd64` 和 `arm64`：

```bash
# bash / zsh
bash <(curl -fsSL https://sq.yolo2.cc/run)
```

```bash
# fish
curl -fsSL https://sq.yolo2.cc/run | env TERM=xterm bash
```

不传参数时，脚本会识别当前 SSH 来源省份，并允许在交互界面中修改。测速档位默认是
`200 Mbps`；IPv4 和 IPv6 默认都检测，服务器只支持其中一种时只测可用的协议。

如果未指定 `-p` 且无法识别来源省份，脚本会直接退出并给出可复制的命令：先用 `-l`
（`--list-provinces`）查看地区代码，再用 `-p hb` 等参数重新运行。

测速过程和结果会直接显示在终端，完成后再返回分享链接：

```text
报告链接：https://sq.yolo2.cc/r/<REPORT_ID>
```

测速时，进度条下方每 8 秒轮换一条使用提示，随机起始，相邻两次不重复。`--nq` 关联提示
优先级最高，其次是省份选择，穿插介绍报告复制、档位和测速结果含义；完成后自动清除，
提示不写入测速结果或分享报告。NodeQuality 关联提示仅在部署方开放该功能时显示。
切换 IPv4、IPv6 或省份时继续之前的提示及其剩余展示时间；提示状态随本次临时目录清理。

表格中的 `-` 表示本次没有可连接的候选节点；`失败` 表示该方向未取得有效速率，另一方向
已测出的数值仍会保留。终端、网页和复制内容都保持每个运营商一行，不额外插入失败说明。

普通测速不会安装系统软件、修改 Python、创建后台服务或删除用户文件。脚本只下载经过校验的
`sqprobe`，并缓存到 `~/.cache/speedquality/` 供后续使用。

脚本自动获取 HTTPS 平台时间，并用运行计时持续推进，供租约校验和报告时间使用；本机时钟
偏快或偏慢时也能测速，不修改系统时间。平台时间无法获取时会提示并回退到本机时间。

## 常用示例

```bash
# 湖北，200 Mbps，自动测试可用的 IPv4/IPv6
bash <(curl -fsSL https://sq.yolo2.cc/run) -p hb

# 湖北和北京，100 Mbps；支持中文名称及中英文逗号
bash <(curl -fsSL https://sq.yolo2.cc/run) -p '湖北，北京' -s 100

# 只测 IPv4 或只测 IPv6
bash <(curl -fsSL https://sq.yolo2.cc/run) -p hb -v4
bash <(curl -fsSL https://sq.yolo2.cc/run) -p hb -v6

# SSH 来源省份加北京、上海、广东，自动去重
bash <(curl -fsSL https://sq.yolo2.cc/run) -p bsg -s 100

# 绑定已有 NodeQuality 报告
bash <(curl -fsSL https://sq.yolo2.cc/run) -p hb \
  --nq https://nodequality.com/r/REPORT_TOKEN

# 精确使用自己注册的社区节点
bash <(curl -fsSL https://sq.yolo2.cc/run) \
  --node sqn_YOUR_ROUTE_KEY -s 200
```

## 支持参数

| 参数 | 说明 |
| --- | --- |
| `-p, --province VALUE` | `auto`、`bsg`、省份代码、中文省级名称或多个省份，最多 5 个 |
| `-s, --speed VALUE` | 测速档位和最高速率：`100/200/400` Mbps，默认 `200` |
| `-v4, --ipv4` | 只测 IPv4 |
| `-v6, --ipv6` | 只测 IPv6；不能与 `-v4` 同时使用 |
| `--nq URL` | 绑定已有 NodeQuality 报告 URL 或报告 token |
| `--node ROUTE_KEY` | 精确使用自己的 SQ 节点；不传 `-p` 时采用节点登记省份 |
| `-l, --list-provinces` | 显示支持的省份代码并退出 |
| `-h, --help` | 显示帮助并退出 |
| `--version` | 显示脚本和探针版本并退出 |

当前公开测速只提供单线程模式，不提供 `--mode`、多线程或自定义测试时长参数。

## 省份选择

| 代码 | 地区 | 代码 | 地区 | 代码 | 地区 | 代码 | 地区 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `bj` | 北京 | `tj` | 天津 | `he` | 河北 | `sx` | 山西 |
| `nm` | 内蒙古 | `ln` | 辽宁 | `jl` | 吉林 | `hl` | 黑龙江 |
| `sh` | 上海 | `js` | 江苏 | `zj` | 浙江 | `ah` | 安徽 |
| `fj` | 福建 | `jx` | 江西 | `sd` | 山东 | `ha` | 河南 |
| `hb` | 湖北 | `hn` | 湖南 | `gd` | 广东 | `gx` | 广西 |
| `hi` | 海南 | `cq` | 重庆 | `sc` | 四川 | `gz` | 贵州 |
| `yn` | 云南 | `xz` | 西藏 | `sn` | 陕西 | `gs` | 甘肃 |
| `qh` | 青海 | `nx` | 宁夏 | `xj` | 新疆 |  |  |

以上是当前公共测速接受的地区代码，节点可用性会变化，不保证每个地区的三网及 IPv4/IPv6
始终有节点。香港、澳门、台湾的公共覆盖尚未确认，暂不列入公共测速范围；已注册的自有节点
仍可通过 `--node ROUTE_KEY` 精确使用。

- 多个省份可以用英文逗号、中文逗号或顿号分隔，单次最多选择 5 个；`all` 已关闭。
- `bsg` 表示“SSH 来源省份 + 北京 + 上海 + 广东”，重复地区会自动去除；来源省份无法
  识别或暂未开放公共测速时，只测试北京、上海和广东。
- 城市名不会静默转换。输入“武汉”会提示改用 `hb` 或“湖北”，避免混淆节点位置。
- 不传 `-p` 时读取 `SSH_CONNECTION`、`SSH_CLIENT` 或登录会话来源。使用跳板机时通常只能
  识别跳板机地址，应手动指定省份；海外、内网或无法定位的来源会直接退出，提示先用 `-l`
  查看代码，再用 `-p` 指定。

完整列表也可以直接查看：

```bash
bash <(curl -fsSL https://sq.yolo2.cc/run) --list-provinces
```

## 流量消耗

每个省份默认分别测试电信、联通和移动。下表是线路达到所选速率上限时的最大参考值，包含约
2 秒预热和每方向 5 秒正式测试；实际速率较低或部分节点不可用时，消耗会更少。

| 档位 | 单省单种 IP | 单省 IPv4 + IPv6 | 5 省 IPv4 + IPv6 |
| ---: | ---: | ---: | ---: |
| 100 Mbps | 约 525 MB | 约 1.05 GB | 约 5.25 GB |
| 200 Mbps | 约 1.05 GB | 约 2.10 GB | 约 10.50 GB |
| 400 Mbps | 约 2.10 GB | 约 4.20 GB | 约 21.00 GB |

预计消耗达到 10 GB 时，交互运行必须再次确认；非交互任务会直接停止。测速结束后还会显示
出口网卡在测试期间的接收、发送和合计增量，其中可能包含同期其它进程的流量。完整计算见
[`docs/capacity.md`](docs/capacity.md)。

## 如何看结果

- `200Mbps ✓` 表示 5 秒平均速率达到 200 Mbps 档位的 98%，不表示线路峰值只有 200 Mbps。
- 速度达到档位的 80% 显示绿色，30% 到 80% 显示橙色，低于 30% 显示红色。
- 延迟四舍五入显示为整数毫秒；按原始测量值判色：不超过 100ms 为绿色，超过 100ms 且
  不超过 200ms 为黄色（网页为橙色），超过 200ms 为红色。
- 延迟是对同一测速节点连续建立 3 次 TCP 连接后成功样本的平均往返时间，不是 ICMP ping、
  HTTP 响应时间或满载延迟。当前版本不输出抖动。

## NodeQuality 联合报告

传入 `--nq` 后，SpeedQuality 会先核对当前服务器与 NodeQuality 报告的身份：完整 IP 可见时
匹配完整 IP，IPv4 被隐藏时至少匹配前两段和 ASN。身份不一致或无法确认时拒绝绑定，只生成
独立 SQ 报告。

脚本会取 NodeQuality 报告中最晚的有效检测时间。与本次测速相差不超过 60 分钟时正常显示；
超过 60 分钟或无法取得时间时仍可绑定，但会用橙色提示时间风险。联合报告保留 NodeQuality
原有分页和内容，只在最后增加“速度质量”页，并保留 NodeQuality 原始报告链接。该功能不代表
SpeedQuality 与 NodeQuality 存在官方合作关系。

身份、时间和快照解析都由 `sqprobe` 完成，测速服务器不需要 Python、pip 或虚拟环境。

## 提供测速节点

愿意共享带宽的用户可以安装 `sq-node`：

```bash
bash <(curl -fsSL https://sq.yolo2.cc/install-node)
```

安装器会检测 Linux 架构、公网 IPv4/IPv6、地区和运营商，生成默认额度，并配置 systemd
后台服务。节点可以设为公开调度、只允许 Route Key 精确使用，或暂停服务。日常管理直接运行：

```bash
sudo sq-node
```

节点安装、云安全组、额度、注销、更新和 Route Key 的完整说明见
[`docs/sq-node.md`](docs/sq-node.md)。自行实现兼容节点可阅读
[`docs/node-protocol.md`](docs/node-protocol.md)。

## 文档与开发

- [公开 Worker 部署](deploy/cloudflare-worker/README.md)
- [客户端与服务端 API](docs/api.md)
- [节点协议](docs/node-protocol.md)
- [社区节点安全边界](docs/community-node-security.md)
- [容量与流量估算](docs/capacity.md)
- [日志与故障排查](docs/observability.md)
- [功能状态与发布路线](docs/roadmap.md)
- [参与开发](CONTRIBUTING.md)

本地运行报告页面预览：

```bash
cd deploy/cloudflare-worker
npm run preview:venv
```

随后打开 `http://127.0.0.1:4173/report-standalone.html` 查看独立 SQ 报告，或打开
`http://127.0.0.1:4173/report.html` 查看 NodeQuality 联合报告。预览脚本使用项目目录中的
Python 虚拟环境，不修改系统 Python。

## 参考与致谢

SpeedQuality 的全球网测兼容层在调研过程中参考了
[MiaM1ku/taierspeedtest](https://github.com/MiaM1ku/taierspeedtest) 对相关客户端通信流程和
测速方式的公开研究，感谢项目作者的工作。SpeedQuality 的公开探针、流量控制、节点验证、
调度接口、报告系统和自建节点协议由本项目维护；第三方测速服务仍受其自身鉴权、限流和
使用规则约束。

第三方来源、许可证和使用边界见 [`THIRD_PARTY.md`](THIRD_PARTY.md)。

## 许可证

SpeedQuality 的原创代码采用 [MIT License](LICENSE)。仓库包含或调用的第三方组件及服务
继续遵循各自的许可证和使用规则。
