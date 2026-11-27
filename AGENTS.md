# foreman — 开发会话规则

foreman 是 agent-crew 的 Go 重写：程序驱动的 AI 开发流水线。spec/ 是唯一权威来源。

## 先读
- spec/constitution.yaml（C-01..C-13，硬约束）
- spec/design/modules.yaml（模块、package、依赖方向、行数上限）
- 当前任务在 docs/dev/plan.md 中引用的 spec 条目；只读这些，不要通读全部 spec
- 本机路径在 .local/paths.md（不进仓库）

## 写代码
- Go 1.27，module 路径 github.com/<owner>/foreman；只在 modules.yaml 列出的 package 里写
- package 非测试 ≤ 3000 行、测试 ≤ 4500 行、单文件 ≤ 600 行（C-03）
- 测试里禁止 time.Sleep 与真实时钟，用 internal/clock（C-05）
- 禁止的测试：只断言 mock 被调用、grep 源码或文案、断言常量等于自身（C-07）
- 测试名与 spec/tests.yaml 一致：test:foo-bar → TestFooBar，写在对应 package
- 访问 GitHub 只经 internal/forge，访问模型只经 internal/harness（C-10）
- 不执行 gh 命令（check:no-gh-exec）；GitHub 用 HTTP 客户端

## 改 spec
- spec 与代码冲突时停下，报告冲突，不要自行改 spec 迁就代码
- decisions.yaml 只追加：新条目用 supersedes/amends，旧条目只改 status（F7、F8）
- 改完运行：ruby tools/smcheck.rb </dev/null；ruby tools/testtrace.rb --write </dev/null
- 所有 ruby 调用都加 </dev/null（否则可能等待 stdin 卡住）
- YAML 里不要用裸 yes/no/on/off 作取值，会被解析成布尔

## 隐私（仓库公开，C-09）
- tracked 文件、commit message、PR 文本里不得出现：用户目录或外接卷绝对路径、
  真实邮箱、用户名、主机名、IP、token。本机路径只写在 .local/
- 提交前运行：git grep -nE '/Users/|/Volumes/|/private/tmp|@gmail' -- . ':!spec/design/privguard.yaml' ':!AGENTS.md'
- 提交身份用仓库已配置的 noreply 身份，不要改 git config

## 凭据
- 不读取、不打印任何 token 文件内容；只允许在命令里以 $(cat 文件) 注入请求头
- 不登录任何服务，不读 agy-home、~/.ssh、~/.config/gh 下的凭据
- GitHub PAT 只授权 foreman-sandbox 仓库，没有 workflows 权限

## git
- 每个任务一个分支 task/<编号>-<短名>，从 design/v1-spec 或其后的开发分支切出
- 只在任务要求时 commit；不 push、不开 PR、不合并（由 owner 或 Opus 会话决定）
- commit message 末尾加协作者行（由会话提供）

## 完成任务时
- go build ./... 与 go test ./... 通过；go vet ./... 无输出；gofmt -l . 无输出
- 报告：做了什么、对应的 test 名、哪些 spec 条目没能满足及原因、遗留问题
- 不确定的地方写进报告，不要猜测后当作事实
