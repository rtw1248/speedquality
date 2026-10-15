# SpeedQuality

**看看你的VPS到本省三网的单线程速度。**

SpeedQuality（SQ）支持按省份测试电信、联通、移动的延迟和上传、下载速度，完成后生成可分享的网页报告。

[使用文档](docs/usage.md) · [节点地图](https://sq.yolo2.cc/nodes) · [自建节点](docs/sq-node.md) · [问题反馈](https://github.com/rtw1248/speedquality/issues)

## 开始测速

通过SSH登录VPS，在Bash或Zsh终端执行：

```bash
bash <(curl -fsSL https://sq.yolo2.cc/run)
```

支持Linux amd64、arm64。默认按SSH来源选择省份，使用200Mbps档位，测试可用的IPv4和IPv6。
需要自己选择时加`-i`打开菜单，或用`-p`指定省份。

## 能做什么

- **自动测本省，也能选其他地区**。根据SSH连接来源选择省份，支持中文、省份代码和多个省份组合；`bsg`可快速选择北京、上海、广东。
- **看三网单线程表现**。分别测量电信、联通、移动的延迟、上传和下载，支持IPv4、IPv6；可选100、200、400Mbps三个限速档位。
- **比较不同时段的节点**。报告显示“武汉电信1号”这样的固定节点名称和来源，早晚对比时可以先确认是否测到同一端点。
- **测完就能分享**。终端直接看彩色结果，网页报告适配手机；支持复制文本、NodeSeek和Markdown格式，报告链接可在Telegram等平台展示预览图。
- **与NodeQuality放在一起看**。通过`--nq`关联已有NQ报告，校验服务器身份并提示检测时间差，在原有分页后追加“速度质量”。
- **查看覆盖，也能贡献节点**。节点地图展示各地区、运营商的近期覆盖；可部署`sq-node`公开共享，或通过Route Key定向使用自己的节点。

## 常用命令

```bash
# 湖北，100Mbps，只测IPv4
bash <(curl -fsSL https://sq.yolo2.cc/run) -p hb -s 100 -v4

# 湖北加北上广
bash <(curl -fsSL https://sq.yolo2.cc/run) -p hb,bsg

# 关联已有NodeQuality报告，将链接替换为你的报告地址
bash <(curl -fsSL https://sq.yolo2.cc/run) --nq https://nodequality.com/r/REPORT_TOKEN
```

`-v6`仅测IPv6，`-i`打开菜单，`-l`查看省份代码，`--help`查看完整帮助。
经过跳板机或无法识别SSH来源时，用`-p`指定想测的地区。[更多参数与示例](docs/usage.md)

<details>
<summary>使用GitHub地址运行</summary>

```bash
bash <(curl -fsSL https://github.com/rtw1248/speedquality/raw/main/run.sh)
```

参数相同；仍通过SpeedQuality平台获取节点、下载程序和生成报告。

</details>

## 测速说明

上传指VPS→测速节点，下载指测速节点→VPS。`200Mbps ✓`表示达到所选档位的判定标准，不代表线路的未限速峰值。

开始传输后预热2秒，再测量5秒。节点拒绝或传输提前中断显示“失败”；正常完成的低速结果按实测值保留。

测速前会估算流量，结束后显示实际流量。默认200Mbps，单省三网、IPv4＋IPv6全测的最高参考消耗约2.10GB；流量有限可选`-s 100 -v4`。

普通测速无需安装后台服务。同一条命令里的所有测试共用一次临时程序下载，结束后清理。[运行与清理说明](docs/usage.md#运行环境与清理)

## 自建测速节点

安装`sq-node`后，可公开共享或仅允许持有Route Key的用户使用，并设置总带宽、单任务最高档位和流量额度。

```bash
bash <(curl -fsSL https://sq.yolo2.cc/install-node)
```

安装会创建节点后台服务。支持自建教育网节点，通过Route Key定向测速；公共教育网测速暂未开放。[部署与管理说明](docs/sq-node.md)

## 文档与反馈

目前处于公开测试阶段，实际可测地区取决于节点状态。遇到问题，请在[Issues](https://github.com/rtw1248/speedquality/issues)中附上运行参数、报错和报告链接。

[完整使用说明](docs/usage.md) · [常见问题与排查](docs/observability.md) · [VPS部署](deploy/vps/README.md) · [Cloudflare部署](deploy/cloudflare-worker/README.md) · [参与开发](CONTRIBUTING.md)

## 致谢与许可证

相关通信流程与测速方式参考了[MiaM1ku/taierspeedtest](https://github.com/MiaM1ku/taierspeedtest)的公开研究，感谢原作者。NQ关联功能由SQ提供，与NodeQuality无官方合作关系。

原创代码采用[MIT License](LICENSE)，第三方来源与使用说明见[THIRD_PARTY.md](THIRD_PARTY.md)。
