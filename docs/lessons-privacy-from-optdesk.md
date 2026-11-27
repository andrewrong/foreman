# 从 optdesk 学到的隐私与防泄漏经验（给 foreman 开源前参考）

范围：只依据 optdesk 仓库里真实存在的文件、提交、PR、issue。找不到证据的地方写“未找到”。路径一律相对 optdesk 仓库根目录；开发机称“本机”。optdesk 仓库当前是公开的（`gh repo view` 显示 PUBLIC），所以下面的“漏”都是已经公开可见的。

## 0. 先给结论

1. optdesk 的主防线是两个程序化检查：`scripts/hygiene`（自写扫描器）和 CI 里的 gitleaks。其余大部分是规则文字（`AGENTS.md`）加设计评审。
2. 它真的抓到过“长得像账号”的测试数据（见 1.2），说明检查有效。
3. 它也真的漏过两类东西：提交进历史的编译产物（内含本机绝对路径，至今仍在历史里）；以及被扫描器豁免目录 `docs/design/` 里的本机绝对路径（当前 HEAD 仍有）。
4. 历史没有清理过：未找到任何 filter-repo、BFG、改写历史、强推的记录。
5. “新仓库 + 去个人化搬运”是它最干净的一步：旧私有仓库的历史根本没带过来。

## 1. 真实的泄漏与险情

### 1.1 编译产物被误提交（真实泄漏，已公开，未清）

- `63bd8c68`（#1838 的实现提交）把 `cmd/ot/ot` 这个约 38 MB 的二进制带进了提交。
- `c10b6329`（#1838）“Remove the cmd/ot/ot binary committed by mistake”删除它。
- 之后 `cmd/ot/ot` 又出现一次（约 39 MB），`7c6d3972`（#2031）“ot: remove a build artifact”再次删除。
- 我对删除前的那个 blob 做了 `grep -a -c` 检查：里面含有 macOS 用户目录或外接卷前缀形式的本机绝对路径字符串（Go 二进制会嵌入构建路径）。这些 blob 仍在可达历史里（`git rev-list --all` 共 7510 个提交，没有任何改写迹象）。
- 根因：`.gitignore` 只写了 `/ot`（锚定仓库根），没有覆盖 `cmd/ot/ot`。`checks.yml` 里有“单文件不超过 1 MB”的检查（`no file over 1 MB`，`scripts/precheck` 里的 `sizeStep` 同款），但它检查的是工作树，只能让“带着二进制的 PR”变红，不能保证没人合并进去；这个二进制进了 main，说明当时这条检查没拦住（原因未找到：可能当时尚无该检查，或在 PR 之外被推入；我没有逐个核对检查加入的先后）。
- 教训：二进制产物的路径要在 `.gitignore` 里写成不锚定的 `**/<name>`；大小检查要作用在“PR 的 diff”而不是只看最终树。

### 1.2 测试数据“长得像账号”（险情，被检查拦下）

hygiene 的账号号规则是 `\b[DU]U?\d{6,8}\b`（券商账号形状）。多次提交为了让它通过而改测试数据：

- `0a06daeb`（#1076）“Use a fixture account name that the hygiene check does not take for an account id”。
- `cc9e243e`（#866）“drop the example account ids from a comment so hygiene passes”。
- `a338f566`（#4170）“test subject no longer looks like an account id”。
- `2a9fee39`（#1223）里还有“fixture host without a dot for the hygiene scan”（测试用主机名去掉点，以免被扫描当成主机）。
- 设计文档 `docs/design/account-identity.md` 第 18 行把做法写成规则：测试里的假账号在运行时由两段拼出（形如 `"U" + "000" + "0001"`），因为任何已提交的 U 加 6 到 8 位数字都会让检查失败。
- 判断：这些是险情而不是泄漏——没有证据表明其中任何一处是真实账号。我无法从仓库内证明“当初那些值不是真的”，只能说提交说明的口径是“长得像”。

### 1.3 设计文档里的本机绝对路径（真实泄漏，HEAD 仍在）

- `docs/design/arch-generated.md` 第 17 行含一条以 用户目录前缀开头的本机绝对路径（含本机用户名；本文不复述）。
- 原因：`scripts/hygiene/main.go` 的 `exempt()` 把整个 `docs/design/` 和 `scripts/hygiene/` 豁免了（原因是设计文档里要“引用”这些模式，比如 `docs/design/week1.md` 第 196 行就把规则本身写了进去）。豁免是整目录，不是按行，所以真路径也被放过。
- 我只在 HEAD 里统计到 `docs/design/` 下 4 个文件含这两种路径前缀；其中至少 1 处是真实路径，其余 3 处是“grep 这些模式应为 0”的规则描述。历史里含该路径的提交至少 5 个（`git log --all -S`）。
- 教训：不要按目录整体豁免；要豁免就用“行内标记”或只豁免明确的规则描述文件。

### 1.4 日志与错误文本里的秘密（险情，靠代码和测试处理）

没有找到“真实日志泄漏了某个秘密”的事故记录。找到的是一串事前加固，每条都带测试：

- `internal/profile/logging.go`：日志 handler 只放行白名单字段，并把形如账号号的字符串值替换成 `[redacted]`。
- `6de4fa7d`/`a5f63c36`（#1649，#1651）：accountcapture 的失败日志只记“净化后的错误类别”。
- （#1473，#1474）：runner 的失败日志里的错误文本先脱敏。
- （#1847，#1886）：`ot backfill` 的错误文本脱敏；（#4259，#4291）：serve 构建失败那一行脱敏。
- 测试：`cmd/ot/env_test.go` 里的 `TestNoCommandLeaksDSN` 和 `TestLeakTableCoversCommands`（每个会用到 DSN 的命令都必须在“泄漏表”里有一行，用哨兵值注入失败，确认输出不含 DSN；新增命令忘了登记会让测试失败）；`cmd/ot/ownertoken_test.go` 的 `TestOwnerLoginLeaksNoToken`；`internal/runner/reconcile/runkey_log_test.go` 里对 DSN 密码、URL userinfo、key=value、Authorization 头的四个脱敏测试。
- 注意：这些都是“代码层脱敏”，不是仓库扫描。

### 1.5 截图、提交作者、测试夹具

- 截图：`.github/workflows/ui-shots.yml` 只渲染 `web/` 的夹具页面并作为 CI artifact 上传。夹具是合成数据，所以截图不含真实数据。未找到把真实页面截图提交进仓库的记录。
- 提交作者：全部 2180 个主线提交的作者都是同一个 GitHub noreply 地址（`git log --format` 统计，没有真实邮箱）；提交者是 GitHub 自己的 noreply。`AGENTS.md` 写了“Commit with the identity the history uses”。hygiene 的邮箱规则允许 noreply 地址、拒绝其他邮箱（`scripts/hygiene/main.go` 的 `noreply()`）。
- 夹具：任何 `fixtures/` 目录下的文件必须列在同目录的 `MANIFEST.synthetic-v1.json` 里，否则 hygiene 失败（“fixture not listed in …”）。这把“新夹具必须被人明确声明为合成数据”变成程序化检查。

### 1.6 机器上的其他私有物

- 估值文档、账号映射、网关端口表都不进仓库：见第 3 节。
- `.claude/worktrees/` 等代理工作目录在本机存在，没有被 `.gitignore` 覆盖，而是靠本机的 `.git/info/exclude`（本机私有、不随仓库走）。换一台机器克隆后不会有这条保护。我只确认了 `git ls-files` 没有列出它们。

## 2. 防护清单：程序化 vs 规则文字，有效 vs 绕过

### 2.1 程序化检查

| 检查 | 位置 | 内容 | 实测效果 |
| --- | --- | --- | --- |
| hygiene 扫描器 | `scripts/hygiene/main.go`，CI 在 `.github/workflows/checks.yml` 的 `repository hygiene` 步骤，`scripts/precheck` 也跑 | 扫描 `git ls-files` 的所有文本文件：账号号形状、两种 macOS 本机路径前缀、非 noreply 邮箱、未登记夹具 | 有效：多次拦下账号形状（1.2）。漏：整目录豁免（1.3）；不扫 IPv4、主机端口、token 形状、长十六进制；只扫 HEAD 工作树，不扫历史 |
| gitleaks | `.github/workflows/hygiene.yml` 的 `gitleaks` 作业，版本和 SHA256 在文件里固定（8.30.1），带 `--redact`，PR 时扫 `base..head` 的提交范围，push 和每日定时扫全部历史 | 通用密钥规则 | 未找到仓库自带的 `.gitleaks.toml`，所以用的是默认规则；没有任何一次“gitleaks 抓到真密钥”的记录（未找到）。`#447/#448`（`9a974da8`、`230f59d2`）只是给它加缓存和重试 |
| 环境变量检查 | `hygiene.yml` 的 `environment` 作业 | CI 环境里不能有 `OT_*` 或名字含 `DSN` 的变量 | 防止测试悄悄依赖真实配置 |
| 文件大小与换行 | `checks.yml`，`scripts/precheck` | 单文件不超过 1 MB；不许 CRLF | 对 1.1 没挡住 |
| 日志与错误脱敏测试 | 见 1.4 | 哨兵值注入 | 有效，且新增命令必须登记 |
| 测试环境隔离 | `AGENTS.md` Tests 节与 CI | 测试不连真实券商；需要数据库的测试带 `pg` 构建标签，只在 CI 跑 | 规则加 CI 环境检查 |
| 演示档案 | `README.md`，`cmd/ot/demo.go` | 没有 `OT_HOME` 时进入演示档案，不加载任何真实值 | 让“克隆下来就跑”不碰私有配置 |
| 依赖漏洞 | `.github/workflows/govulncheck.yml`、`.github/dependabot.yml` | 不属隐私，仅列出 | - |
| PR 描述检查 | `checks.yml` 的 `Contracts:` 行检查 | 只管契约，不管隐私 | 说明“PR 正文可被机器检查”这条路是通的，却没有用于隐私 |

### 2.2 只有规则文字的部分

- `AGENTS.md` Pull requests 节：“No secrets, API keys, account ids, tokens, broker credentials, chat ids or personal data in code, commits, configuration or fixtures.” 其中账号号有 hygiene 兜底；密钥有 gitleaks 兜底；聊天 id、个人数据、“broker credentials”里非标准形状的部分没有程序化检查（未找到）。
- `AGENTS.md` 的“A new direct dependency is named in the pull request description … the owner's word is needed only for a library that touches secrets, the network or the sandbox.”：依赖评审里的人工规则。
- `.github/PULL_REQUEST_TEMPLATE.md`：没有隐私一栏。
- 评审侧（crew 一侧，不在 optdesk 仓库里）：设计评审和 PR 评审是人或评审代理读；“估值文档不进 issue、夹具、PR”属于这一类。仓库内没有对应的程序化检查（未找到）。

### 2.3 crew 侧的机制（只描述，不在 optdesk 仓库内，未逐条核对实现）

- `crew report publish`：发布前扫描文本中的 token 形状、长十六进制或 base64、绝对路径、IPv4、host:port、邮箱，命中就不发。
- 状态页规则：不得写入秘密、服务数据、日志行、路径。
- 窗口（人机交互的代理会话）从不读取 `.env` 或 `secrets/`。
- 以上来自任务说明，我在 optdesk 仓库里无法验证；对 foreman 的启示是：把“发布前的文本扫描”做成程序，比只写规则可靠。

### 2.4 绕过与漏网总结

1. 整目录豁免（`docs/design/`）：漏了真路径。
2. 只看工作树的检查挡不住历史里的二进制。
3. hygiene 不扫 IP、主机端口、token、长十六进制；gitleaks 只靠默认规则。
4. 账号形状只覆盖一种券商（以 `U` 或 `DU` 开头加数字），对其他形状的个人标识无能为力。
5. 为通过检查而“改写测试数据让它不像账号”，是在对抗扫描器的形状，而不是证明数据是合成的；真正的保证来自夹具清单文件加运行时拼接。

## 3. 私有配置与仓库分离

- 秘密都放在仓库外：`.gitignore` 里写了 `.env`、`.env.*`，并注明“secrets live in $OT_HOME, never in the tree”。`ot` 只读取 `OT_HOME` 与 `OT_PROFILE` 两个环境变量（`README.md`）。
- 读取顺序：`cmd/ot/env.go` 的 `envFile` 在设置了服务秘密目录变量时读“服务名.env”，否则读 `$OT_HOME/.env`。对应提交 `792aaff0`、`fa5edb6f`（#1086，#1110）。依任务说明，`.env` 是遗留方式，现在由每个服务的独立秘密目录提供；部署文档 `docs/deploy/serve.md`、`docs/deploy/scheduler.md` 的相应小节仍写 `.env`（文档可能落后于实际做法，我没有核对到更新记录）。
- 部署文档里的入口配置只写占位符，如 `<OT_HOME of ot>`，并有一节专门写“秘密不在入口里”（`docs/deploy/serve.md`、`docs/deploy/scheduler.md`、`docs/deploy/pyside.md`）。
- 示例配置：`pyside/examples/gateways.example.toml`。头部注释写明“复制到服务配置目录，副本留在仓库之外，填你自己的名字和端口”，端口写成占位值，并说明“条目里不含账号，网关自己报告账号”。
- 账号映射：`docs/design/account-identity.md` 规定账号别名与账号号的对应只存在 `$OT_HOME/accounts.toml`，“never committed”；仓库只提交写着 `<U-number>` 的示例，不提交数字串；代码里别名是公开概念、真实号只在本机文件。未知账号直接拒绝，错误文本不带号（同文第 23 行）。
- 网关账号不在仓库里命名：提交 `7e63602d`、`60da831e`（#1977，#1979）只在合同注释里说明 `account` 字段是券商账号。
- 估值等个人文档：保存在仓库外，依任务说明从不放进 issue、夹具或 PR；仓库内没有对应的程序化检查（未找到）。
- 默认安全：没有私有配置时进入演示档案，不会因为缺配置而去读别处。
- 对 foreman 的启示：约定一个单一的“家目录”环境变量；仓库内只放 `*.example`；缺配置时有演示模式；示例文件里的值必须是占位符而不是“看着像真的”的值。

## 4. AI 代理特有的风险与拦截

optdesk 的大量提交由代理完成（提交信息末尾带代理署名行，`git log` 中可见大量此类行）。找到的、与代理相关的风险和做法：

1. 代理会把本机路径写进文档：1.3 就是典型，路径来自代理对另一个工具目录的 grep 描述。拦截只有 hygiene，而它豁免了设计文档。
2. 代理会提交构建产物：1.1。
3. 代理为过检查而改测试数据：1.2。是好事也是风险；好在规则文字加夹具清单让“合成”可检查。
4. 提交身份：`AGENTS.md` 要求用历史里的身份提交；仓库不保存个人邮箱，作者用 noreply 地址。提交说明和 PR 描述末尾固定写一行代理署名（无个人信息）。
5. PR 正文：模板要求 `Contracts:` 行等，`checks.yml` 读取 PR 正文并机器校验；`edited` 事件也触发。隐私上没有对 PR 正文做扫描（未找到）。
6. 代理会话和记忆：任务说明里窗口不读 `.env`、`secrets/`；这是代理侧的限制，不在仓库里，我无法在仓库内验证。
7. 模块边界：`AGENTS.md` 规定一个 issue 只改自己模块的路径，需要别的文件只在 PR 描述里报告。这间接限制了代理“顺手”改配置或扫描器豁免。`docs/design/ratchet.json` 注明“owner-only”，代理不得提高上限；同类“owner-only 路径”概念也用于 `.github/**`（从提交信息 `#13` 标题 “owner_only” 可见）。把安全相关文件（扫描器、工作流、豁免清单）设成 owner-only，是值得照抄的一条。
8. 日志：代理写的新命令若会用到 DSN，必须在泄漏表登记，否则测试失败（1.4）。这是对“代理忘了”最有效的拦法：让遗漏本身变成红灯。
9. 未找到：对 issue 正文、PR 评论、提交说明做自动隐私扫描的程序（仓库内没有）。

## 5. 历史清理

- 未找到：任何 `filter-repo`、BFG、改写历史、强制推送的记录（对 `*.md`、`*.go`、`*.yml` 全文搜索无命中；提交总数 7510，所有引用共 4543 个，未见被改写的痕迹）。
- 实际做法是“从头开始”：optdesk 是新仓库，旧的私有仓库（option-trader）历史没有迁过来。首个提交是 `16479a3b Initial commit`。
- 搬运方式：`docs/carryover-notes.md` 和 `docs/carryover-manifest.txt`。只复制白名单里的 6 个文件，记录来源提交与每个文件的 sha256；先做机械清洗（去掉所有指向私有仓库的 issue/PR 编号和链接），再通读一遍去掉个人语句（所在城市与时区、交易的股票代码等，笔记里只列“改了哪一节”，不引用原文）；对设置中带长数字串的哈希值，因为“去个人化扫描会把它当成 id”，一律换成 `pending-recompute`。相关 issue 是 #29（“de-personalised”）。
- 坑：
  1. 清洗是“人读加扫描”，没有第二个独立评审的记录（未找到）。
  2. 搬运后产生的新泄漏（1.1、1.3）无法靠“从头开始”避免；一旦进了公开历史，没有清理手段。
  3. 二进制 blob 的清理成本最高：它们让仓库变大，并含路径。如果要清，必须改写历史并强推，所有克隆和 PR 引用都会失效；optdesk 没有选这条路。
  4. 扫描器无法回溯：hygiene 只扫工作树；gitleaks 在 push 和定时任务里扫全历史，但只认密钥规则，不认本机路径。
- 对 foreman：开源前最便宜的清理是“新建仓库、白名单搬运”；已有历史的，开源前就要用 `git filter-repo` 做一次，并在改写之后再跑一次全历史扫描（见第 6 节）。

## 6. 给新开源 Go 项目的五条硬规则（机器可检查）

1. **全历史密钥扫描，且不得有按目录的整体豁免。**
   CI 作业对 `git log` 全范围跑 gitleaks（固定版本加校验和，`--redact`），PR 作业扫 `base..head`，定时作业扫全部；仓库带一个 `.gitleaks.toml`，豁免只允许“规则加具体指纹”，不允许路径通配。检查方式：CI 红绿；另设一个测试断言 `.gitleaks.toml` 里没有 `paths` 通配条目。
2. **本机路径、私网地址、个人标识的文本扫描，作用于全部 tracked 文件，包括 `docs/`。**
   扫描项：各系统的用户目录前缀（macOS、Linux、Windows）与外接卷前缀、IPv4、`host:port`、非 noreply 邮箱、长十六进制或 base64、业务账号形状。豁免只能用行内标记（例如行尾带固定注释）并要求在清单文件里登记原因；测试里的“违规输入”在运行时拼接，不写进文件。optdesk 的 hygiene 做到了前半，漏在 `docs/design/` 整目录豁免和缺少 IP、token 规则。
3. **只允许“源码类”文件进入仓库：按白名单检查 tracked 文件。**
   CI 用 `git ls-files` 检查：除允许的扩展名和目录之外，任何文件（可执行文件、`.db`、`.env*`、`*.pem`、`*.key`、截图、归档）都失败；同时用 `file` 或魔数拒绝 ELF、Mach-O、PE 可执行格式；单文件大小上限用 `git diff --name-only base...head` 对 PR 范围而不是只对 HEAD。对应 optdesk 的 1.1。
4. **提交元数据检查：作者与提交者必须是 noreply 地址，提交信息和 PR 正文过同一套文本扫描。**
   CI 对 PR 范围内每个提交跑 `git log --format='%ae %ce'`，不是 noreply 就失败；对 PR 标题和正文、提交信息套规则 2 的扫描（optdesk 只对 PR 正文校验契约行，没有校验隐私）。在 `.github/` 和扫描器目录设 CODEOWNERS，改动它们必须所有者批准（对应 optdesk 的 owner-only 路径）。
5. **运行时秘密只能从仓库外读取，并有“泄漏表”测试。**
   程序只读取一个家目录变量；没有它时进入演示模式，不读任何真实值；代码中所有读取秘密的命令登记在一张表里，测试用哨兵值注入失败，断言 stdout、stderr 和日志都不含哨兵；表遗漏命令时测试失败（对应 optdesk 的 `TestNoCommandLeaksDSN` 和 `TestLeakTableCoversCommands`）。日志只放行白名单字段（`internal/profile/logging.go` 的做法）。CI 的环境检查要求运行环境里没有项目专属变量，防止测试悄悄读真配置。

补充一条流程建议（不是机器可查）：开源前先在新克隆上跑一次“全历史 gitleaks 加规则 2 的历史版（`git log -S` 或 `git grep` 遍历所有提交）”，把命中当作发布阻塞项；有命中就用 filter-repo 重写后再发布，而不是像 optdesk 那样靠后续删除文件。

## 附：证据缺口

- 未找到：gitleaks 抓到过真密钥的记录；仓库自带的 gitleaks 配置；历史改写记录；对 issue、PR 评论或提交信息的自动隐私扫描；PR 模板中的隐私栏。
- 无法在 optdesk 仓库内验证：crew 侧的发布前扫描、窗口不读秘密、独立秘密目录的现行实现（仅按任务说明转述）；估值文档在仓库外的保存。
- 1.1 中“当初检查为何没拦住二进制”的原因未核实。
- 1.2 中那些账号形状值是否曾是真实号，仓库内无法证明。
