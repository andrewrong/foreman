# 第 1 阶段真机探测（2026-11-28）

环境：macOS（arm64）、Go 1.27.1、Claude Code 2.1.332、srt（@anthropic-ai/sandbox-runtime，agent-crew 安装的那一份）、测试仓库 foreman-sandbox（公开）。
探测目录在 FOREMAN_HOME 下（外置盘），本文件不写本机绝对路径。

## P1 沙箱（srt）

策略：allowWrite = 会话仓库 + 运行临时目录；denyWrite = foremand 的 home；denyRead = 凭据目录、~/.ssh、~/.config/gh；allowedDomains = proxy.golang.org、sum.golang.org；allowLocalBinding = true。

| 项 | 结果 |
|---|---|
| clone --shared 的会话仓库里 git commit，pre-commit hook 执行 | 通过：commit 成功，hook 写入 $TMPDIR |
| 读镜像对象（alternates） | 通过：commit 正常读取镜像对象 |
| 写 foremand 镜像 | 被拒（Operation not permitted） |
| 读凭据目录 | 被拒（Operation not permitted） |
| api.github.com | 不通（curl 000） |
| proxy.golang.org | 200 |
| 绑定 127.0.0.1 端口 | 成功 |
| **环境变量** | **srt 原样继承父进程环境：沙箱内看得到 6 个名字含 token/key 的变量**。结论：foremand 启动会话必须从空环境按白名单构造（PATH、HOME、TMPDIR、LANG、GOCACHE 等 + C-08 允许的两类凭据），不能继承 foremand 自己的环境 |

Go 缓存：
- owner 机器的 GOCACHE 有 306G，cp -c（APFS 克隆）整份克隆数小时级（逐文件元数据），不可用；跨卷时退化为整份复制，更不可用。
- 结论：每个项目一份专用缓存（FOREMAN_HOME/cache/<p>/，由 foremand 在沙箱外预热），会话用 cp -c 克隆这份项目缓存；实测 foreman-sandbox：空缓存首次 go test 3s，项目缓存 71M、1062 个文件，cp -c 克隆 < 1s。

## P2 claude -p

| 项 | 结果 |
|---|---|
| stream-json 事件 | system(init)、assistant、rate_limit_event、result |
| rate_limit_event | rate_limit_info.unifiedWindows 只含本次报告的窗口；本次只有 seven_day（utilization 0–1 小数、resetsAt 秒级时间戳、status、surpassedThreshold）；five_hour 不一定出现 → 「检查报告的每个窗口」正确，缺席的窗口沿用上次值 |
| utilization 精度 | 两位小数（0.89） |
| result | total_cost_usd、usage（含 cache_creation/read 与 1h/5m 拆分）、modelUsage 按模型列出、permission_denials、terminal_reason |
| advisor | --settings '{"advisorModel":"opus"}' 生效：assistant 出现 server_tool_use name=advisor 与 advisor_tool_result；modelUsage 同时出现 opus 与 sonnet；advisor 不出现在 init.tools 中，也不需要在 --tools 中列出 |
| 固定开销 | 默认启动（用户全部插件、MCP、skills、127 个工具）：一次 "pong" 消耗 cache 写 19221 + 读 11836 token；加 --strict-mcp-config --disable-slash-commands --setting-sources "" --tools <6 个>：写 3174 + 读 5205（约 1/4） |
| --bare | 不可用于订阅登录：--bare 只认 ANTHROPIC_API_KEY 或 apiKeyHelper，不读 OAuth/keychain |
| 插件 | --setting-sources "" 后 init 中仍有 4 个插件（来自 managed 或内置），需要在 P2 后续确认能否关闭 |

## P3 GitHub PAT

| 项 | 结果 |
|---|---|
| push 触及 .github/workflows/** | 被拒（PAT 无 workflow 权限） |
| 普通 push、删除分支（git push --delete 与 DELETE git/refs） | 成功 |
| 开 PR（POST pulls）、关 PR（PATCH state=closed） | 成功 |
| update-branch | 接口可调用（无新提交时返回说明信息） |
| rerun workflow run（POST actions/runs/{id}/rerun） | 201 |
| check-runs 读取 | 返回 PR 头提交的 check run（公开仓库；私有仓库仍未验证） |
| 条件请求 If-None-Match → 304 | x-ratelimit-used 不变：304 不计入 core 额度 |

探测用的 PR 与分支已关闭、删除。

## P4 claude 在 srt 内运行（2026-11-28，层 3）

Claude Code 2.1.332、srt（agent-crew 工具目录中的版本），haiku，foreman 专用 token（`claude setup-token`，经 policy 的 Model 凭据注入 `CLAUDE_CODE_OAUTH_TOKEN`）。

| 检查 | 结果 |
|---|---|
| 认证 | 成功；init 的 apiKeySource 为 none（不用钥匙串登录） |
| 会话仓库内 Write | 成功 |
| Bash 工具 | 先失败：EPERM mkdir（系统临时目录下 claude-<uid> 子目录）；该版本读 CLAUDE_CODE_TMPDIR（旧名 CLAUDE_TMPDIR 不够）；policy 加上后成功 |
| 读 secrets 目录 | Operation not permitted |
| 读 ~/.ssh | Operation not permitted |
| srt 自身 | PATH 中的 node 若是 mise shim，srt 启动失败；须给真实 node 目录 |

结论：层 3 可行。遗留：launcher 需要可配置的 node 目录（srt 是 node 脚本）；网络只放行了 api.anthropic.com，未见其他主机被拒。
