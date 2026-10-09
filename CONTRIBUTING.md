# 参与 SpeedQuality 开发

感谢你考虑为 SpeedQuality 提交改进。项目的 Bash 客户端、测速探针、社区节点服务、
公开 Worker、公开协议、示例 Provider 和文档接受社区贡献。生产环境的私有 Node Core
由平台维护者单独管理。

## 提交前先选择入口

| 情况 | 推荐入口 |
| --- | --- |
| 明确且很小的修复 | 直接提交 Pull Request |
| 可复现的缺陷 | Bug Report Issue |
| 新功能、协议变化或行为调整 | Feature Request Issue，先确认设计 |
| 使用问题 | GitHub Discussions；尚未开启时使用 Issue |
| 安全漏洞 | GitHub Private Vulnerability Reporting |

安全问题不要公开披露利用步骤、节点地址、Token、Route Key、真实用户 IP 或其它敏感数据。
仓库创建后，维护者应在 **Settings > Security > Code security** 中开启 Private
Vulnerability Reporting。

## 外部贡献流程

1. Fork 公开仓库，并从最新的 `main` 创建一个简短分支，例如
   `fix/ipv6-detection` 或 `feat/report-export`。
2. 一个 Pull Request 只解决一个清晰问题。协议或用户行为变化应先关联对应 Issue。
3. 修改实现，同时更新受影响的测试、帮助文本和文档。
4. 在本地运行与改动范围相符的检查。
5. 推送到自己的 Fork，向上游 `main` 创建 Pull Request。
6. 填写 PR 模板，说明问题、实现、验证方法、兼容性和风险。
7. GitHub Actions 全部通过后，由维护者进行代码和产品行为审查。
8. 根据 review 更新同一分支。不要为同一个改动反复创建新 PR。
9. 维护者使用 squash 或 rebase 合并，并决定进入哪个发布版本。

外部贡献者不需要获得 Cloudflare、生产数据库、GitHub Release Secret 或私有 Core 权限。
来自 Fork 的 PR 工作流也不会得到这些 Secret。

### 权限逐步开放

| 阶段 | GitHub 权限 | 可以做什么 |
| --- | --- | --- |
| 外部贡献者 | 无仓库权限 | Fork、Issue、Discussion、Pull Request |
| 持续贡献者 | Triage | 整理 Issue、复现问题、维护标签，不直接修改代码 |
| 代码维护者 | Write | 管理分支和 PR，仍受 `main` 保护规则约束 |
| 发布维护者 | 单独授权 | 创建版本、管理 Release 和执行受控部署 |
| 平台运营者 | 最小生产权限 | 管理 Cloudflare、D1、R2、Core 和 Secret |

权限根据持续贡献、审查质量和实际职责逐步授予。代码 Write 权限不自动附带生产权限，发布
维护者也不需要读取全部运行时 Secret。

## 本地检查

项目需要 Go 1.22+、Node.js 22+、npm、Bash 和 ShellCheck。

完整检查：

```bash
bash -n run.sh install-node.sh deploy/cloudflare-worker/preview-local.sh \
  tests/test.sh tests/fixtures/*.sh
shellcheck run.sh install-node.sh deploy/cloudflare-worker/preview-local.sh \
  tests/test.sh tests/fixtures/*.sh
bash tests/test.sh

(cd probe && go test ./... && go vet ./...)
(cd sq-node && go test ./... && go test -race ./... && go vet ./...)
(cd deploy/cloudflare-worker && npm ci && npm test && npm run check)
(cd examples/fixture-provider && npm ci && npm test)
```

文档修正可以只运行相关检查。修改共享协议、会话、租约、节点服务或报告生成时，应运行
完整检查。

## 代码和协议要求

- 不提交真实节点 IP、API Token、Route Key、Cloudflare ID、数据库导出、用户报告原文或
  Secret。
- 不在日志中新增完整客户端 IP、认证头、JWT、Cookie 或请求正文。
- 保持 Bash 客户端可在项目声明支持的 Linux 环境运行，不依赖修改系统 Python。
- Go 和 JavaScript 依赖应有明确用途，并提交对应锁文件变化。
- 公共 API 和节点协议变化必须说明向后兼容策略、版本边界和回滚方式。
- D1 结构变化使用新的迁移文件，已发布迁移不得改写。
- 用户可见参数、默认值和输出变化应同步更新 README、`--help` 和测试。
- 引入第三方代码或协议研究成果时，应确认许可证并更新第三方说明。
- 不把私有 Node Core 的实现、生产规则或凭据复制到公开仓库。

## Pull Request 验收标准

维护者会重点检查：

- 用户问题是否清楚且改动范围合理；
- 测速结果、流量上限、租约和失败行为是否保持一致；
- 输入校验、认证、限流和敏感日志是否可靠；
- 是否覆盖了有实际回归价值的测试；
- 文档和命令示例是否能直接使用；
- 是否会破坏旧客户端、旧节点或数据库回滚；
- GitHub Actions 是否全部通过。

界面修改应附桌面和移动视口截图。并发、安全、流量计算或节点生命周期修改应说明测试
环境及边界条件。

## 私有 Core 相关贡献

公开仓库提供 `docs/api.md`、`docs/node-protocol.md` 和
`examples/fixture-provider/`，开发者可以基于这些内容实现自用测速后端或提交协议改进。

涉及官方调度 Core 的问题按以下方式处理：

- 普通行为问题可在公开 Issue 中描述可观察现象，并删除 Secret 和真实节点信息；
- 安全问题使用 Private Vulnerability Reporting；
- 协议变更先在公开 Issue 讨论，再分别修改公开协议和私有实现；
- 私有 Core 的代码审查和部署由获得明确权限的长期维护者完成。

## 许可证

SpeedQuality 使用 MIT License。提交 Pull Request 表示你有权提交这些内容，并同意贡献
按仓库的 MIT License 分发。第一阶段不要求签署 CLA 或 DCO；若以后出现公司级贡献或
版权归属需求，再单独评估。

## 发布权限

合并代码不会自动获得发布权限。版本号、Git tag、签名 Release、Cloudflare 部署、D1
迁移和生产开关由平台维护者执行。这样可以让外部开发者参与大部分代码，同时把生产密钥
和最终上线责任保持在最小范围内。

## 版本号与发布节奏

版本使用 `主版本.次版本.修订号`，按改动内容升级，不按发布时间、用户量或提交次数升级。

| 改动 | 版本示例 |
| --- | --- |
| 修复故障、安全问题、兼容性问题，或调整文案、排版和性能 | `1.1.0` → `1.1.1` |
| 新增可独立使用的功能，同时兼容已有命令、API 和节点 | `1.0.x` → `1.1.0` |
| 移除已有接口，或对协议、配置作出无法兼容的重大调整 | `1.x.x` → `2.0.0` |

`v1.0.17` 完成当前的交互入口和 `bsg` 行为调整。之后首次新增功能时发布 `v1.1.0`，
该功能版本内的修复使用 `v1.1.1`、`v1.1.2` 等版本；进一步新增功能时进入 `v1.2.0`。
修订号不限于一位，`1.0.9` 后可以继续 `1.0.10`；发布新次版本时修订号归零。

改变已有参数含义或默认行为时，必须在发布说明中写明前后差异，优先提供兼容或迁移方式。
产品版本升级不自动改变 API 或节点协议版本；协议调整应另外说明旧客户端、旧节点的兼容范围。
仅修改文档通常不新建 Release。已发布的版本标签和下载文件不覆盖，修复通过新版本发布。
