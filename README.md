# SpeedQuality

在VPS上查看指定省份电信、联通、移动的单线程速度，生成可分享的测速报告。支持Linux amd64、arm64。

[使用指南](docs/usage.md) · [节点地图](https://sq.yolo2.cc/nodes) · [自建节点](docs/sq-node.md) · [问题反馈](https://github.com/rtw1248/speedquality/issues)

> 目前处于公开测试阶段，实际可测地区取决于节点状态。

## 快速开始

通过SSH登录VPS，在Bash或Zsh终端执行：

```bash
bash <(curl -fsSL https://sq.yolo2.cc/run)
```

默认按SSH来源选择省份，使用200Mbps档位，测试可用的IPv4和IPv6。识别失败时用`-p`指定省份，或加`-i`打开菜单。

浏览器打开[使用页面](https://sq.yolo2.cc/run)可查看指南并复制命令；[fish用法](docs/usage.md#快速运行)见详细说明。

<details>
<summary>也可以使用GitHub入口</summary>

```bash
bash <(curl -fsSL https://github.com/rtw1248/speedquality/raw/refs/heads/main/run.sh)
```

两种入口参数相同，均通过SpeedQuality平台获取节点、下载测速程序和生成报告。

</details>

## 主要特点

- **按省份测速：**支持中文名称、省份代码和北上广快捷写法`bsg`，一次最多5个省份。
- **单线程、可控流量：**100、200、400Mbps三个限速档位，默认200Mbps。
- **节点可对比：**报告显示实际节点的城市、编号和来源，方便比较不同时段。
- **IPv4和IPv6：**自动测试可用协议，也可单独选择。
- **方便分享：**终端彩色结果、手机网页报告，支持复制文本、NodeSeek和Markdown格式。
- **关联NodeQuality：**校验后保留NQ原有分页，追加“速度质量”页，用一个链接分享两份结果。

## 常用命令

```bash
# 湖北，100Mbps，仅IPv4
bash <(curl -fsSL https://sq.yolo2.cc/run) -p hb -s 100 -v4

# 湖北加北上广，默认200Mbps
bash <(curl -fsSL https://sq.yolo2.cc/run) -p hb,bsg

# 关联已有NodeQuality报告，将链接替换为你的报告地址
bash <(curl -fsSL https://sq.yolo2.cc/run) -p hb --nq https://nodequality.com/r/REPORT_TOKEN
```

`-v6`仅测IPv6，`-i`打开菜单，`-l`列出省份，`--help`查看帮助。更多用法见[完整参数](docs/usage.md#支持参数)和[省份代码](docs/usage.md#省份选择)。

## 结果与流量

- **上传：**VPS→测速节点；**下载：**测速节点→VPS。
- `200Mbps ✓`表示达到本次档位的判定标准，不代表线路的未限速峰值。
- 默认200Mbps，单省三网、IPv4＋IPv6全测的最高参考流量约2.10GB；开始前会给出估算。
- 普通测速使用一次性临时程序，完成后清理；同一条命令里的所有测试共用一次下载。

详细说明：[结果含义](docs/usage.md#如何看结果) · [流量估算](docs/usage.md#流量消耗) · [运行与清理](docs/usage.md#运行环境与清理) · [NQ关联规则](docs/usage.md#nodequality-联合报告)

## 提供测速节点

愿意提供节点的用户可以安装`sq-node`，它会创建后台服务。教育网也可自建并通过Route Key使用，公共教育网测速暂未开放：

```bash
bash <(curl -fsSL https://sq.yolo2.cc/install-node)
```

节点支持公开共享、仅持有Route Key的用户使用，以及暂停服务。安装、额度、管理和卸载见[自建节点指南](docs/sq-node.md)。

## 更多文档

| 内容 | 入口 |
| --- | --- |
| 平台部署 | [VPS / Docker](deploy/vps/README.md) · [Cloudflare Worker](deploy/cloudflare-worker/README.md) |
| 开发与接口 | [参与开发](CONTRIBUTING.md) · [API](docs/api.md) · [节点协议](docs/node-protocol.md) |
| 运行与排查 | [容量说明](docs/capacity.md) · [故障排查](docs/observability.md) · [节点安全](docs/community-node-security.md) |
| 后续计划 | [功能状态与路线](docs/roadmap.md) |

## 参考与许可证

相关通信流程与测速方式的调研参考了[MiaM1ku/taierspeedtest](https://github.com/MiaM1ku/taierspeedtest)，感谢原作者的公开研究。

原创代码采用[MIT License](LICENSE)；第三方来源、许可证和使用边界见[第三方说明](THIRD_PARTY.md)。NQ报告关联由SQ提供，不代表与NodeQuality存在官方合作关系。
