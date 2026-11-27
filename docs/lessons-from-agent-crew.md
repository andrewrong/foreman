# 来自 agent-crew 的教训（给 foreman 的设计输入）

本文回答一份问卷，证据全部来自 agent-crew 仓库本身：`AGENTS.md`、`rules/*.md`、`docs/design/*.md`、
`docs/design/ratchet.json`、git log、`gh issue/pr`（只读）、`crew usage`（只读形式）、以及 dispatcher
窗口自己记的错误清单（不在仓库里，下文称"错误清单"，共约 215 行，每行一个可避免的错误）。

约定：
- "owner" 指项目所有者；"dispatcher" 是写 issue、启动 drive 的窗口会话；"worker/author" 是写代码的会话。
- 数字后面不带说明的，来自上面列的来源并已核对；核对不了的标 **未核实**。
- 时间为 2026-10-07（首个 commit）到 2026-11-27。截至本文：1700 个 commit，issue/PR 编号到 3365，
  `internal/crew` 一个 Go package：380 个源文件 104k 行，519 个测试文件 167k 行，4508 个 `Test*` 函数，全仓库 Go 约 272k 行。
- 先说结论：agent-crew 里大部分"治理"代码，是在补一个更早的设计错误——
  **把状态放在会话的记忆里、把规格放在散文里、把规则放在会话启动时读一次的文本里**。
  foreman 想避免重蹈，核心是反过来：状态在表里，规格是结构化字段，规则变成程序检查。

---

## 0. 今天（2026-11-27）的事故：状态记忆 bug

这是整份文档最好的单个例子，因为它同时暴露了"状态在记忆里"和"人工纠偏会过度"。

**现象**（PR 3365 `docs/design/ask-plan-close.md` 的 Frame，plan item P46，owner ask 8494）：

- 一个 ask 被 `settle --new` 之后，状态永远是 `open`。它只有通过 `crew ask issue`（把草稿挂成 issue）或
  `settle --into` 才会关；"plan item 完成"和"issue 合并"都不会关它。agent-crew 上 open 列表一路堆到
  **33 条，最早回到 11-08**。
- 会话启动 hook（`printAskCounts`，`window_lang.go`）把旧的 system 行算成 unprocessed：**190**，而
  `crew ask <project> list --unprocessed` 说的是 **0**。hook 和命令用了两套口径。
- 一个 plan item 的所有关联 issue 都已 merged 且 verified，状态仍是 `in_progress`，要人工 `crew plan complete`；
  patrol 要等 24 小时才提一句。
- 新开的 dispatcher 窗口读到这些"活的"工作，把已完成的当成待办；而它在手工清理时又**过度纠正**：
  "The dispatcher completed several items wrongly by hand"（设计原文），把还欠着的 item 当成完成关掉了。

**为什么会这样（设计错在哪）**：

1. "完成"是一个**人为动作**（`complete`），不是从事实推出来的状态。事实（issue 合并、verify 通过）在 GitHub 和
   `crew.db` 里，状态却在另一张表，没人负责对齐。
2. 同一个"未处理数"有两个实现（hook 一份、CLI 一份），口径漂移没有测试守着。
3. 窗口读到的是"账本原始行"，不是"派生视图"。给 AI 看原始账本，它会照字面当真。

**修法**（3365，尚未合并，所以效果 **未核实**）：不新增任何写；`planResolvedAsks` 在读取时从 item 状态派生"已解决的 ask"；
`PlanItem.Ready` 标"可以complete 但不自动 complete"（`complete` 的语义仍是"这个 item 不会再有新 issue"，因此永不自动）；
hook 和 patrol 共用同一个 `planLiveOfProject`，保证两边的 Ready 一致。

**对 foreman 的启示**：任何"完成/关闭"状态要么由事实**派生**，要么由**一个函数**写（agent-crew 后来的 task model
就是这么改的：`docs/design/task-model.md`，"one table, six states, one writer"，旧队列的状态在 32 处、11 个函数里被写）。
需要人工确认的完成点（`complete`）与可自动推出的"就绪"点（`Ready`）要分成两个字段。

---

## 1. Prose issues（散文 issue）

### 1.1 三个真实的伤害

1. **作者读不到"后来改过的"issue（#561，2026-10-27）**。dispatcher 在作者停下后编辑了 issue #559（作者自己要求的编辑）；
   下一次 drive 用固定文本 `driveResumeText` resume 旧会话，文本里完全不提 issue，作者没重读，同样原因再停一次，
   decision 角色再等一次。修法：run 记录里存 `issue_digest`（title+body 的 SHA-256），resume 时比较，不同就把新文本拼进消息；
   同一 digest 下作者连停两次不带 PR，drive 拒绝第三次并报"需要 dispatcher 行动"。
2. **验收条目写成散文，verify 看不懂/做不到**。错误清单里同一类错误出现了 **十几次**：
   - #1239、#1361、#1362（2026-11-02/03）：验收项让沙箱里的 verify 去跑 vitest/web 测试，沙箱没有 `node_modules`、`npx` EPERM，
     verify 回 `cannot_check`，gate 把 `cannot_check` 当失败，drive 停。同一错误隔一小时又犯一次。
   - #368、#359（2026-10-15）：验收项让 verify 跑 `crew cap` / `crew check`（要写记录、读服务目录），同样 `cannot_check`。
   - #1474（11-04）：12 个 S4 草稿都要 verify 跑整包 `go test -count=2 ./internal/crew`，verify 手册禁止，全部 fail，事后逐个改了 12 个 issue。
   - #2037/#2038、#1612、#2028：验收项写"上面提到的两个文件""Change 一节里的那句话"，pre-verify 拒绝——"验收项必须自包含，
     verify 只看 Acceptance 一节"。
3. **"只改这些文件"类的验收项让必要的修改变成失败**。#1559 item 2 "only changes of test files" 因为必须多包一层而 fail；
   #2028/#2039 的 "services.toml and no other file" 漏了两个 pin 测试（`roles_real_test.go` 对 `roles.toml` 的 blob 哈希、
   `services_own_test.go`），review 阻塞，多一轮。#2245 草稿点名 `issue_modules.go`（796 行，无 ceiling），worker 推到 852，
   ratchet 拒绝，PR 2247 关掉重来。

还有一类：把**测试名**钉死在验收里。2026-11-26 owner 决定"设计和 issue 只钉做什么，不钉怎么做"（PR 3290，`318b78f`）：
Files、测试名、"关于现有代码的句子"都只是"期望"，worker 做真实的事、报告偏差、修正设计。但三处旧规则句子漏改，
于是有 #3361/#3363（`rules/acceptance.md` 的 "at most two items of the form a test named X"，
`rules/verify.md` 的 "a named test that does not exist is fail"；而 `acceptance.md` 的内容还被 `roles_real_test.go` 以
git blob 哈希钉住，改一句话必须同时改 pin，所以一个 issue 被"跨 module"拒绝，拆成两条）。
这是一个小而典型的循环：**规则文本、重复它的代码、钉住它的测试，是三个地方**。

### 1.2 运行前不可知的字段

下面这些东西，issue 作者在写 issue 时**不可能知道**，却被要求写：

| 字段 | 为什么不可知 | 实际后果 |
|---|---|---|
| `Files:`（要改的文件） | 真正要碰的文件由实现决定（生成文件、共享 fake、pin 测试） | #2809：一个新的 `Machine` 调用要改每个回答它的 fake、harness allowlist、`writes.md` pin；验收写"只限五个设计文件"，verify 失败 |
| 行数是否越过 ratchet | 取决于 worker 写多少 | #2374（patrol.go 已在 ceiling 1530，worker 1562 失败）、#2803（cap_keeper.go 792→821）都是 dispatcher 没数行 |
| 哪些测试"钉"了这个值 | 隐式耦合，只有 grep 才知 | 同一错误 ≥8 次：改 `projects/*.toml` 却没跑 `TestPairfobDeclarationIsWhatTheOwnerDecided` / `TestTheCommittedRolesCarryTheCutoff` 等 pin |
| 谁来合并（要不要 owner） | 取决于真实 diff 触到哪些路径 | #1655 被队列自动合并：草稿写了"waived"却没写"The owner merges this pull request."；后来改成 task model 的 D2：**由 crew 从真实 diff 算，不由规格里的键说了算** |
| 验证"真机行为"的结果 | 只有跑了才知 | 例：PR 2411 的 `Probed on the real machine:` 行缺 `-> <result>`，gate 在合并时才拒绝 |
| 原因（Cause） | 排查前只是怀疑 | 窗口常把错误的原因写成事实。2026-11-25 的设计 `cause-proof.md`：`Cause:` 后必须有一行 `Proof: repro: <cmd> => <line>` 或 `Proof: code: <path>:<line>`，否则 `crew issue check` 拒绝；没证明的只能写 `Cause (suspected)`，且只能出现在"去找原因"的 issue 里；手工关 fault 必须带 `--class real|wrong|noise|duplicate` 以便度量"原因写错"的比例 |

### 1.3 执行中发现的新事实怎么往下游流——以及怎么没流

- **没流的**：worker 发现要多改 `Files:` 之外的文件，旧规则是"只报告不编辑"，于是整条 issue 停给人。2026-11-24 的
  `stop-fixer.md` 改成：程序检查那个文件，干净就自己加进 Files 并记一笔，只有冲突才给会话。
- **没流的**：approve 会话对已批准 head 的 finding（authority 失败）——`crew queue retry` 只重跑 gate，finding 永远到不了作者
  （错误清单 11-18 13:45Z；同类的 #3345：`EventChecksRed` 不带 payload，被拒的 adoption 文本也进不了下一轮）。
- **没流的**：dispatcher 评论 PR 会被队列当成"owner 说话"而暂停 issue（#1152 / PR 1936 / PR 1797，三次）。所以规则是
  "改 issue 再 retry，绝不评论 worker 的 PR"。
- **流得好的**：把 CI 失败日志直接回灌作者（`resume --checks`，#3079 修了彩色日志的 escape）；`issue_digest`；
  task model 的 `rounds/<n>.json` 把退回原因写成文件（#3345 补齐缺的两种原因）。

### 1.4 我建议的结构化 issue 模式

agent-crew 最终走到了这个方向（`spec.toml`：`title/why/change`、`[[acceptance]]` 每项带 `text/kind/path/literal`，
kind 为 `test|live|command|behaviour|literal`，`live` 的 `argv` 必须在只读 allowlist 上，不是 shell 字符串；
`crew issue rules` 现在有 **39 条**检查）。如果从头设计，我会保留/丢弃：

**保留（必填、机器可校验）**

- `id`、`title`、`version`（只在 version+1 时改字段；`kind` 和 `id` 不变）。
- `why`（一句，谁需要、删掉它会怎样——agent-crew 的设计文档要求的就是这个）。
- `behaviour[]`：每项一个**可观察的行为/结果/边界**，自包含（不得写"上面""下面"，不得引用别的文件段落），带 `kind`：
  - `test`（用 table case，不写测试名）、`live`（只读 argv）、`judged_by_reading`（沙箱里跑不了的，如 web 测试、真实 socket）、
    `ci_only`（`make test` 绿，由 gate 读 CI，不让 verify 跑）。
  - 每项必须声明 verify 在**自己的沙箱里能否执行**；不能的自动标 `judged_by_reading`，不允许落成 `cannot_check`。
- `boundaries[]`：真实边界（"不碰 `internal/foo/`"），不是文件清单。
- `depends_on[]`：id 列表（agent-crew 曾因 `Depends on #169` 少个冒号而提前启动 #171、#173，错误清单 11-07；正则只读 `Depends on: #n`——结构化后无此问题）。
- `proof`：`cause` 与 `proof` 成对；没有 proof 就只能是 `suspected` 且 `task = find_cause`。
- `size_budget`：每个预期触及的文件的当前行数和 ceiling，由程序**生成**，不是人写。

**丢弃/改为程序推导**

- 手写的 `Files:`（改为"预期文件"，仅作提示；边界以真实 diff 为准，即 "pin what, not how"）。
- 手写的"谁合并"（改为从 diff 计算）。
- 手写的 `Second opinion / Consultation / Standing / Plan / Effect / Checked against / Probed on the real machine` 一长串行。
  agent-crew 的 issue 头部现在有 8–10 个这样的行，错误清单里至少 12 条是格式写错（缺 `Standing:`、逗号 `S-01, S-05`、
  waived 同时写了 `Brief: below`、`Probed ... -> ` 缺箭头）。能推导的推导，不能推导的变成枚举字段。
- 验收项里的测试名、文件名、"no other file"。

**两条硬规则**：(a) 先 `check`，再创建；管道 `check | tail && create` 会吞掉 check 的失败码（错误清单 11-04 两次）。
(b) 草稿只能有一个登记入口；重复登记产生了两个 issue（#2408/#2409；#2007 重复）。

---

## 2. Ping-pong 循环、冗余阶段、token 成本

### 2.1 真实的循环

1. **设计评审的 reject 链（optdesk PR 1784，`design-loop.md`）**：第 2 到 12 轮，30 条 note，其中 21 条是同 5 条的重述；
   15 条 blocking finding 落在上一轮刚改过的文字上；**没有一条新 finding 落在未改动的文字上**；修完一轮后仍有 19 条 blocking。
   每个 session 约 $1.76，且必须读全文。后果：#2503 "design session: a cap on consecutive heads judged changes"
   （patrol 在连续 3 个 head 上提示，5 个上停止）；#1780 "crewd judges up to 20 heads of one design pull request in 24 hours"。
   `findings` 原来是一个平的字符串数组，note、重述、新的 blocking 无法区分，下一轮 session 也无法判断什么会关掉一条 finding；
   之后改成带 section 和 close 条件的对象（#2499/#2510）。
2. **设计 PR 反复送给 owner（#1571，2026-11-05）**：同一个设计三次被 owner 的 design review 打回；
   dispatcher 的 sub-agent pre-check 两次说 ok，design session 仍找到 3 个实质性缺陷（policy 在真实机器上跑不起来）——
   pre-check 查的是一致性，不是"能不能真跑"。规则变成：设计 PR 只有在一个**没写过它的**新 sub-agent 在**该确切 head**上说 ok 才交给 owner；
   并且 pre-check 的 brief 要求"模拟一次真实会话启动"。
3. **CI 红链**：2026-11-25 一天内 22 次 PR 的 `test` run 里 7 次失败，4 次死在 lint，1 次 `go vet` 编译错
   （#3256）。作者被送回去修，再 push，再等 CI，每圈一个完整的 worker + review + verify。
   修法是在 drive 里、worker 的 workspace 上先跑 gofmt、ratchet、pin 测试，现在加上 `go vet` 和 lint（#3256 precheck）。
   另外 main 变红 1 小时没人知道（2026-11-24，owner 对此很生气）：crewd 检测到了（base fault #838），patrol 也打印了
   "queue: base red"，但那行排在输出最后，被 `| head` 截掉；gate 还放行了含首个红 commit 的 head。
4. **Retry/resume 循环**：`crew drive` 曾在作者连续无 PR 结束时一直 resume；#561 加了上限；
   2026-11-11 optdesk #1447 撞到 automatic resume cap（24 小时 6 次），dispatcher 把 `crew resume --start` 交给 owner
   （应该自己 `crew queue retry --write`）。规则 #2321/#2322 补上。
   `crew queue retry` 对 `waiting_owner`（gate_refused:authority）**静默无效**（错误清单 11-04、11-05 各一次，
   "dry run 没有写入"的最后一行提示也被加进需求）。
5. **spin hold**：#1258 被 spin ticket 62 暂停（3 次 "checks queued with no runner" 的**误报**停止），dispatcher 没看到，
   空转 1 小时（错误清单 11-02 19:19–20:20）。#1262 把"有 runner 的排队"算等待，不算 spin 尝试。
6. **author ↔ review 因 pin 测试的循环**：每次改 `projects/*.toml` 或 `rules/` 就有 pin 测试失败，"-run 过滤漏了"。
   错误清单里至少 8 条（#1287、#1313、#1345、#1626/1627、#1799/1803、#1855、#2028、#2329）。

### 2.2 冗余阶段/角色

从成本看（`crew usage --days 14 agent-crew`，14 天、共 2426 美元 **注：cost_usd 为 crew 按 token 折算的估算值，计价口径未核实**）：

| 角色 | 14 天成本 | 占比 | runs |
|---|---|---|---|
| dispatcher 窗口 | 968.7 | 39.9% | （窗口，非 run） |
| advisor 窗口 | 425.5 | 17.5% | |
| worker（作者） | 346.0 | 14.3% | 292 |
| design session | 292.1 | 12.0% | 167 |
| approve session | 199.6 | 8.2% | 103 |
| review | 140.5 | 5.8% | 468 |
| verify | 53.9 | 2.2% | 493 |

- **窗口（dispatcher+advisor）占 57%，真正写代码的 worker 只占 14%**。dispatcher 窗口内部（`--by=source`）：
  cross-session（别的窗口发来的消息）占 34%，owner 的提问 10%，wake round 9%，sub-agent 9%，renew 8.5%。
  即：**花在"协调"上的钱是花在"写"上的四倍**。
- 评审类（design+approve+review+verify）合计 28%，约为 worker 的两倍。approve 会话（一个 head 一次，输出几乎是固定的判断）
  单价（$1.9/次）比 review 的（$0.3/次）高六倍，因为它要读全文；design 约 $1.7/次，且上限 5 个连续 changes 之前能连跑。
- **每个 PR 的成本随 fix round 倍增**（`crew usage --by pr agent-crew`，223 个 PR，总计 310.4 美元；该口径包含哪些角色 **未核实**）：

  | fix rounds | PR 数 | 平均成本 |
  |---|---|---|
  | 0 | 169 | $0.75 |
  | 1 | 43 | $2.42（3.2×） |
  | 2 | 10 | $6.27（8.3×） |
  | 3 | 1 | $16.07（21×） |

  最贵的单个 PR（3058）$21.9、9 个 session、2 rounds。**一轮 fix 就是一个完整的 worker+review+verify，所以 owner 说
  "消耗 token 的，你以为是免费吗"（2026-10-29，#649 在 worker 启动之后才改 issue，指望 review 把它退回补上）。**
- 冗余：
  - **二次意见（Second opinion）/ advisor 每 issue 一份**：2026-11-05 owner 对"收尾速度"不满：机器侧很快（3 天 169 个 PR，中位 34 分钟），
    代价是仪式——一个设计三个版本、8 个 issue、每个 issue 一份二次意见、每 2–3 个 PR 一次 owner 的话。结论写成规则
    "governance cost proportional"：设计到第 2 版或 issue 超过 4 条就提议"更少更大的 PR、设计 verdict 当二次意见、一条消息问完所有合并"。
  - **owner 合并词**：每天约 13 个（`relax-merge.md`：11-03..11-05 合并的 169 个 PR 里 40 个等 owner，33 个只因为
    `AGENTS.md/rules/projects/roles.toml` 在 `owner_only`），放宽后目标 2–3 个。
  - **Gemini 额外评审**（`[review.extra]`）：为了"不同厂商独立性"加的，owner 2026-11-27 因"额度消耗太快"关闭（#3241）。
    投入：#3095 零写入测试、#3174 适配器、#3194/#3217 声明、#3219/#3220/#3236/#3238/#3240 一串适配 bug（keychain not found、
    PATH 找不到、要在被评审 head 的 checkout 里启动、usage limit 要等不是停）——**仅 agy 一项就有 ≥10 个 issue，试用一周后关**。
  - **Codex**：2026-11-05 owner 不再用 Codex（"codex不要，我后面不准备用"），独立性改由**独立的 Anthropic 会话**（design / approve / advisor）提供。
    此前为了 Codex 做了：skills 交给 Codex（#558/#569）、Codex 窗口的 service tier（#617）、登录提示被 shell function 绕过（#525）、
    Codex 周额度读取（#605）、两份 Codex 声明（#609、#628）、"Codex review 失败要说明原因"（#588）；最后一份 `codex-cleanup.md`
    把声明和 pin 测试删掉，适配器本身因 agy 试用还留着。
  - **Gemini/Codex 的价值判断（未核实）**：没有统计过"第二厂商评审抓到了第一厂商漏的 bug"的次数，所以无法证明它值回成本。
    错误清单里的实际漏判来自"评审读不到真实机器"（见 §7），不是"同一模型有盲区"。

### 2.3 我的建议

- 阶段的存在理由要写成"它抓过什么"，并定期统计；agent-crew 到 11-27 才有 `crew usage --by pr`（#2949）。**一开始就记录每阶段的成本和"是否改变了结论"**。
- 评审者看的上下文要缩小：只读 diff 的"自上次评审以来的变化"（agent-crew 的 integration review 和 §5 步 5 的规则）。
- fix round 前先在作者本地跑一遍和 CI 同样的检查（precheck）。这是单位成本最低的去环路手段。
- 设计评审要有轮数硬上限（5）和 finding 去重（section + close 条件）。

---

## 3. 卡住的任务 / 死路

### 3.1 真实的状态和原因

agent-crew 的队列有 `needs_attention` 与 `waiting_owner` 两种"停"，以及 `crew queue plan` 的若干行；后来 task model 定为六个状态
（waiting、running、stopped、owner、done、dropped），每个非 running 都带 **reason**，由**一个** `Transition` 函数（40 行转移表、21 种事件、30 种原因）写。

| 状态/原因 | 真实例子 | 原因 |
|---|---|---|
| `needs_attention: start not confirmed` | #1324，2026-11-03 | crewd 在 drive 启动时 re-exec，是 crew 自己的缺陷 |
| `needs_attention` drive 起不来 | #2811，11-18 | 用 `--ask 6810`（origin 为 system 的 ask）登记，drive 跳过 system ask，3 次起不来；crew 却**接受了 attach** |
| `blocked_overlap` | #1335 被 PR #1337 阻塞 | "a rename of the pull request has no old name"（dist 资产重命名，其实文件不相交），所有 issue 被挡；后来要人工 force |
| `blocked_overlap` | #1814 阻塞约 20 分钟（11-06）在 dispatcher 自己的 PR 1816 后面；PR 1925 在 1924 之后变 CONFLICTING 且 push 没有 CI | 设计 PR 都改 `docs/design/modules.md` |
| `waiting_owner` + `gate_refused:authority` | #1457、#1595 | `crew queue retry` 对它**无声无效**；另外 head 早于 `rules/design.md` 且改了 `docs/design/**` 的 PR **永远拿不到 design verdict**，队列也无法重驱 |
| 没有 design verdict | optdesk 1186 | patrol 行只写"ended without a verdict"，不写原因（失败的 post），要去读队列日志才知道 |
| `resume cap` | optdesk #1447，6 resume/24h | 见 §2.1(4) |
| `no_new_commit` | #2799、#2861、#3125 | 一个 resume 的 run 结束在一个不在 PR 上的 commit 上，被当成"没有新提交"，改为 `unpushed_head` |
| 设计 PR 的 changes 无人看 | PR 2064 | 11-08 23:54 +08 的 round 2 changes，11-09 05:23 才有人处理：**5.5 小时**。patrol 对"dispatcher 自己的设计 PR、head 上有 changes verdict"不提任何项 |
| 谁都以为别人做了 | #3298 | "an author run that opened a pull request is not a failed session"：作者开了 PR 却被当失败 |
| worker 无权写 | #647/#648，2026-10-29 | worker 的 deny list 里有 `.github/**`、`.claude/**`、`projects/**`，drive 起来后没有 PR 就结束，白烧 token |
| worker 被 deny 误杀 | #1368，11-03 | 4 次把原因归咎于 auto-mode classifier，**真正原因是 worker 的 deny 规则 `Bash(*crew gate*)` 匹配到了 commit message 里的 "crew gate"**（所以 issue 标题不能含 crew gate） |

### 3.2 兜底机制（效果各异）

- **stop-fixer**（`stop-fixer.md`，11-24）：每种停都有一个会话处理，且"每个 issue 最多 4 次动作，起不来的会话 60 分钟后直接给 owner 一整包"。
  有效的部分是"一整包"；**没解决的**是"approve 会话的 finding 回到作者"（§1.3）。
- **spin hold / `crew ticket`**：三次同一 head 同一原因 → 开 hold ticket 并停止重启（#913），#1158/#1262 修误报。
- **flaky_base**：检查失败而 base 也有同样失败，不送给作者而是重跑一次（#1314、#1374、#2665、#2676 cancel 也重跑一次）。
- **patrol --watch**：每 30 分钟把窗口叫不醒的情况推给 owner（#2533）。

### 3.3 没抓到的

- **空槽**：dispatcher 下午合并后让两个槽都空着（11-02，~30 分钟）、#1247/#1249 合并后队列空到 owner 问（10–20 分钟）、
  并发 1/6 持续数小时（11-05 15:57，owner："并发率如此之低"）。patrol 的"有空槽但无 issue"是事后（#1252）才加，且**当天没唤醒**
  （原因未查）。
- **并发数从规则里读**：`rules/dispatcher.md` 写"agent-crew 的数字是 4"，而窗口启动 prompt 带的是旧的 2，半天空着一半槽
  （11-02）。根因：窗口规则只在进程启动时读一次，`/clear` 式 renew 保持同一个 claude 进程，窗口拿着 10-29 的文本过了 4 天。
  所以后来有"renew 时重读规则"、"并发数取自 `crew queue plan`"。
- **新增 roles.toml 键杀掉正在运行的 drive**：#430 合并 `[roles.worker] compact_window` 后，所有在重编译前启动的 option-trader drive 死于
  `is not a key of a session role`（2026-10-20）；#1391 又来一次（`reviewer.skills`，三个 issue stop `other`，11-03）。
  因为 drive 进程内存里是旧二进制，却**每一步重读** `roles.toml` 和声明。
- **main 变红**：见 §2.1(3)，owner 的原话是"你作为开发系统，你竟然不知道？"。

---

## 4. AI 忘记后续动作（上下文丢失、compact、resume 增长）

### 4.1 具体模式

1. **resume 一个会话 11 次，上下文 22k → 454k，从不 compact**（#429 正文，2026-10-20）：option-trader 的 Opus worker 做 #930，
   一个 session 跨 11 次 resume，每个 turn 重读整个上下文，"一个 issue 约 75M cached tokens"；最近三天 13 个 worker session 超过 200k。
   对策 #429：worker compaction 在 200k，并给无头 session 一套降 token 的基线设置。
2. **窗口上下文大小 × turn 数 = 成本**（#2993，2026-11-24）：renew 阈值从 70% 降到 40%（`renewThreshold` of `compact_window`）。
   代价是"窗口大约两倍频繁 renew，每次 renew 要重写一次 state page 和冷缓存"。
   同时 owner 否决了"调低 autoCompactWindow"（`docs/decisions/2026-11-01-no-lower-autocompact-window.md`：
   "为此才有状态页"）。
3. **/clear 之后的残留进程**：renew 循环 5 次，阻塞它的是上一个会话启动的一个 option poll（72 分钟旧，活过了 /clear，
   不在新会话的任务列表里）（11-02 23:2x）。11-03 08:3x 又一次：renew 后用 grep 找不到后台 job `brvvk1tfi`，
   重发了 #1313 链，同一个 037e770 发了**两个 review**。
4. **一个 owner 要求在 renew 里丢失**：owner 2026-10-31（asks 1939/1947）要求 review 强制使用某套 code-review skill，
   一直没做（`skills.lock` 里仍是 none），直到 11-03 才被发现——"Lost across renews"。
5. **记住了但是记错的数字/时间**：state page 里写**编造的时间** —— 错误清单里"run `date` before writing a time"出现 **≥10 次**
   （11-03、11-05 两次、11-06 两次、11-09、11-11 三次、11-18…），其中 11-05 把 04:38 写成 00:5x，报告了一个并不存在的 3.5 小时停滞，
   还让一个 sub-agent 做了诊断。2026-11-02 还把"runner 日志 7.3 GB"当成现状讲出去，实测是 3.6 GB 的旧 runner.sh 日志，不是 pool。
6. **把 sub-agent 的结论当事实**：11-03 对 owner 和 option dispatcher 说"草稿 V1 从未登记"，其实已是 #1714，
   那是一个 sub-agent 读 ledger 的结果，未经自己验证。后来 owner 的规则："不要把没验证的 sub-agent 结果交给我"。
7. **跨窗口的"已经说过了"**：owner 明确说过"crew 要自己重启窗口"（11-01 asks 2404/2409），11-03 dispatcher 仍把
   `crew window reopen/reload --start dispatcher` 命令交给 owner。教训："交给 owner 一条命令前，先搜 ask ledger 里有没有'自动'的话"。
8. **晚到的 merge word 不跨窗口**（11-02）：state page 里记着"owner 预先说过可以合并"，classifier 拒绝了 `--owner-approved`。
   AGENTS.md 本来就写了"新窗口不采信前一个会话的话"。
9. **过度纠正**：今天的 §0。

### 4.2 哪些对策有效

| 对策 | 效果 |
|---|---|
| **State page**（`rules/state-page.md`）：每个 requester 窗口一份，四节（在跑的 / 等 owner 的 / 最近决定 / 下一步），每次进展**整页重写**；JOB 行带 relaunch 命令 | 有效，是 renew 能成立的前提。但页是"凭记忆写"的，所以 #1224：renew 时把 queue 的事实交给新会话，`--ready` 拒绝"漏掉了活 issue"的页——**把事实核对放到程序里** |
| **Ask ledger**（#459）：hook 把 owner 的每句话逐字记账，板上显示 unprocessed / to-split | 有效但引出了今天的 bug（状态不会关）。**账本是事实，不是待办清单**，待办要从事实派生 |
| **Plan items（P-xx）**：owner 的诉求、议题的显式列表 | 有效；今天暴露"完成"靠人工 |
| **把 owner 的决定写进 memory 并要求"别重问"**（如 gemini-dropped、no-codex） | 部分有效；2026-11-27 dispatcher 仍给 sub-agent 布置了 P32（Gemini）的任务，owner 不得不提醒。教训："布置任何 plan item 前，先检查 commit 历史和 memory 里有没有停止决定" |
| **规则变成检查**（memory "rules must become checks"） | 最有效。例：`crew issue check` 的 39 条；`window_bash_guard.go`（长前台命令拒绝并提示后台跑，owner "不要在前台运行命令"变成程序）；ratchet 的 `t.Parallel` 检查 |
| 在窗口里**限制工具输出体积** / 减少 claude.ai connector（`window-context.md`）：connector 工具列表约 95k、AGENTS.md 34.6k、消息 38k | 有效，直接减小起步上下文 |
| renew 前守卫（owner 闲、ask 账本、sub-agent、wake round） | 有效，但 #2560 说明"renew 是否发生"自己也会算错（要看 window record 里是否记了新 session） |
| **"一个需求一个分支一个工作树一个作者会话"**（need 2026-11-26-agent-crew-8143；#3336 fix round 在自己的 worktree 里延续第一次 run 的会话） | 正在落地，效果 **未核实** |

**共同点**：有效的对策都是"事实在程序里，窗口只读派生视图"；无效的是"提醒窗口记住"。

---

## 5. 沙箱（srt）：太紧和太松

### 5.1 现状

`roles.toml` 里 `sandbox = "srt"` 的角色：worker、acceptance、diagnose、verify、ops。**review、approve、design 原来没有 srt**
（`outputOf` in `review.go`）：磁盘可读、网络开放、整个 runner home 可写，而它们读的恰恰是不可信文本（issue、PR、diff）。
`docs/design/sandbox-tightening.md`（2026-11-04/05，revision 3）要把它们也放进沙箱，并把"home 默认可读"改成"拒读 `~/` 与 runner home，
再开窄口"。先做了 measurement 0（真跑 worker/verify/review 各一次，读 srt 违规日志）再设计。

### 5.2 太紧：具体的坑

- **网络**：
  - #1843（optdesk，2026-11-06）：`proxy.golang.org` 返回 module 但把 zip 重定向到 `storage.googleapis.com`，不在 `[sandbox] hosts` 里，
    `go get -tool` 全 403，worker 没 PR 就停。pairfob 早就列了。
  - #3260（2026-11-26，fault 879）：worker 读失败 CI job 的日志，API 302 到 `productionresultssa8.blob.core.windows.net`，
    不在 `githubHosts`（现在是 4 项）里；lint/ratchet 失败不能自动修，停给人（optdesk #4009、#3995）。**白名单必须包含"重定向落点"**，
    而重定向落点不是配置者能提前列出的。
  - 新的 Go 依赖：verify 沙箱无法拉，只能人工预填模块缓存（#1541，11-04）。
  - 验收里的真实 socket 测试：verify 不能 bind 端口（telegram_platform #15，11-09）；写成"按代码判断，CI 跑"。
    该 PR #18 的 CI 因为没给时间限制 hung >30 分钟。
- **Go cache / mod cache / tmp**：
  - #1046：owner 把 Go build cache 从内置盘搬走（约 **40 GB/天** 写入），沙箱只允许写 `sandbox.write` 里的路径，六个声明要同时列新旧两个路径。
  - #2224：run 在会话启动前要创建 `sandbox.temp_env` 的目录；
  - #1854/#3287：mise shim 读 `~/.config/mise/config.toml`，在沙箱里要显式指过去；carve-out 一度依赖"planning 进程的 PATH"，换个 PATH 就挂。
  - 验证没有 `node_modules`、`npx` EPERM：web 测试在 agent-crew 里一律"按阅读判断，CI 跑"（11-03 #1361/#1362）。
- **git/gh**：worker 要 `git push` 和 `gh pr create`，所以 carve-out 里要放 `~/.gitconfig`（及其 `include.path`/`includeIf`）、gh config 目录、
  pre-commit hook 读的路径——**凭据因此对 worker 可读**（"phase 1 仍如此，phase 2 由 crew 来 push"，`## Left out`）。
  measurement 0 发现：git 在不可读的全局配置上会**直接死**，所以 verify/review 等其他角色要把 `GIT_CONFIG_GLOBAL` 设为 null device。
- **端口**：verify 不能 bind；`crew sandbox probe <project> <role>` 把策略的每个格子跑一遍，不匹配就 exit 1。
- **deny 规则按文本匹配**：`Bash(*crew gate*)` 匹配到 commit message（见 §3.1，4 次误判成 classifier）。

### 5.3 太松：具体的坑

- review/approve/design 没沙箱（上节）。
- worker 是一个**能碰 GitHub 凭据**的进程，而它读不可信 issue 文本（prompt injection 路径）。
- 一个 worker 把代码直接推上了 main（见 §8：3 次 + 今天 1 次），说明"沙箱能写"和"remote 允许直推"叠加起来就没有边界。
- verify 本来该只读，但 verify 手册要特别禁止"整包 go test"，说明沙箱本身不挡"资源/时间"。
- 沙箱不管**不是文件的东西**：`crew gate` 在 worker 里被一条 Bash deny 规则挡，而不是由 gate 自己认"我被谁调用了"。
  #1668 加了"sandboxed session 里 ask/cap/queue/gate 一律拒绝"。

### 5.4 我会怎么画边界

1. **能力分级而不是路径列表**：`read-only-analysis`（review/approve/design/verify，无网络，无凭据，只读 clone）、
   `author`（可写自己 worktree 和 tmp，网络只到包仓库和 forge，**无 push 凭据**）、`ops`（单独审批）。
2. **凭据永远不进沙箱**：作者只提交并写 `pr.md`，由 foreman 进程 push、开 PR（agent-crew 的 phase 2 就是这个，推迟了）。
   一旦这样，GitHub 的 hosts 白名单也可以拿掉。
3. **网络用代理 + 域名族**（`*.githubusercontent.com`、`*.blob.core.windows.net` 的具体 account 由代理放行日志学习，
   而不是人工逐个补），并记录被拒的 host，让"白名单补丁"变成一条 patrol 项而不是一次 worker 停摆。
4. **缓存只读种子 + per-job 可写层**：agent-crew 的 CI runner 设计（"warm Go cache baked into runner image，read-only seed，per-job writable layer，nothing flows back"）
   同理适用于沙箱的 GOCACHE/GOMODCACHE，避免 PR 之间互相投毒。
5. **沙箱探针是一等命令**（`crew sandbox probe`），在每次策略变更后自动跑；**先测量再设计**（measurement 0 找到 4 个设计前不知道的事实）。
6. **验收只能要求沙箱内能跑的东西**：由 `crew issue check` 的 `acceptance-js`、`acceptance-run` 在登记时拒绝，而不是 verify 运行时回 `cannot_check`。

---

## 6. 多 harness（claude / codex / gemini）造成的伤

- **一个接口三套语义**。`Runtime` 只允许 `ao.go` 一个适配器，但 harness（会话的模型提供者）在 `harness.go`（1830 行，ratchet 上限，
  在 ratchet ceiling 表里排第五）里，要处理：登录/token 位置、skills 如何交付、sandbox 的 CLI 标志、effort/tier、usage-limit 的读取、
  transcript 位置、窗口钩子。每加一个 harness，这几处都要加。
- **skills 交付方式不同**：Claude 读 plugin 目录，Codex 要把 skills 放在 prompt 前面（#558/#569 "resumed Codex session 要再交一次 skills"），
  agy 同样"rules 和 skills 放在 prompt 前面"（#3219）。resume 之后丢失是一类 bug。
- **登录/凭据**：Codex 的 login hint 被用户的 shell function `codex` 绕过（#525）；agy 用专用 HOME，无 keychain，
  macOS 每次弹 "Keychain Not Found"（#3236）；agy 不在 PATH，要按 `~/.local/bin` 找（#3238）。
- **额度语义不同**：Codex 看"周额度"，要在每次 session 后读（#605）；agy 的 usage limit 要"等待而非停止"（#3220）；
  Claude 的 5h/周窗口。`crew quota` 要按 harness 和 role 分别报告，**没有统一的"还剩多少"**。
- **评审工作目录**：agy 要在被评审 head 的 checkout 里启动（#3240）；Claude 评审用 detached worktree（#1389）。
- **独立性判定被复杂化**：`[review] floor`（vendor/model/session），gate 每次根据"记录的作者 + review 自己的 model 和 harness"重新计算独立级别，
  并且每个 reviewer 要匹配自己 harness 的候选。后来出现过："approve 会话的 judge model 与 dispatcher 的 model 相同，所以 dispatcher 写的设计 PR
  永远拿不到 approve 记录"（11-04，PR 1465/1526/1467/1529 全部要 owner 的 `--owner-approved`）；`relax-merge.md` 因此把 approve judge 换成另一个模型（claude-fable-5-1）。
- **成本结论**：第二厂商只在"额度充足"时才有意义。owner 11-05 删掉 Codex，11-27 关掉 Gemini，都是因为**额度**而不是因为质量。
  以后：先量化"第二评审抓到了什么"，再决定是否值得多维护一个适配器。独立性最便宜的来源是**另一个 session**（不同上下文、不同 prompt、只读），
  而不是另一个厂商。

---

## 7. 测试与 CI

### 7.1 为什么测试很多 bug 还是很多

数字：4508 个 `Test*`，测试代码 167k 行 / 源码 104k 行 = **1.6:1**；PR 的 `test` run 中位数 183 秒（最大 244 秒，25 个样本），
main 的 push run（带 `-race`）中位数 **656 秒**（最大 808，15 个样本）；最近 40 个 `test` run 6 个失败。

1. **fake 太多，没有 fake 管不到的路径**。AGENTS.md 规定"所有对机器的访问经 `Machine`，测试可以替换它"，于是几乎全部行为在 fake 上被测。
   后果集中在"真实数据首次路径"：
   - 2026-11-01，board 的七个视图 CI 全绿，owner 一开浏览器，默认 project 没有 snapshot，refresh 在 404 上抛异常，永远是 skeleton。
     所有我手测的 route 都返回 200。教训："open the UI before saying done"。
   - 2026-11-05，**第一个非 crew 根目录的项目**（optdesk）用 `crew design review`，一小时内撞到 #1762、#1764、#1765/1766/1769：
     因为 agent-crew 自己的 crew root 和项目 clone 是同一个目录，HEAD 就是被评审的 head，混淆从未显现。owner："跑了最多的资源，错了最多的 bug"。
     规则：凡带 `<project>` 参数的功能，必须在**不是 crew 根目录的 clone、在没 checkout 的 head** 上跑过一次才算完成；先 grep 同类（`Git(a.Root`）。
   - #2147：接了 `setTelegramAskPages`，验收只测"注册了"，没测"一次 pass 读第二个项目的页"；实际 passes 只对第一个排队的项目运行。
     "wiring fix 的验收要写出对每个服务项目的用户可见效果"。
   - #1546：合了一个 Makefile `lint` target，**从未运行**（golangci-lint v2 去掉了 `--out-format`），因为步骤 6 没跑新 target。规则：
     "一个检查要看到它失败一次才算证明"。
   - PR 1379/1381/1386：图表看不见（`--color-rest/graphql/runs` 解析为空），**连续三次**只查 DOM 就说 ok；owner 的浏览器里 main 上也是空的。
     "issue 必须写可见结果（颜色 X 的线可见），不是代码形状"。
   - #2410：`Probed on the real machine:` 的探针只测健康/端口/日志，没测 owner 实际用的功能。规则："探针必须包含 owner 真正用服务做的那一件事"。
2. **测试在测"形状"**。2026-11-25 owner 决定（`no-useless-tests.md`）：评审时无用测试算 blocking，四类不许加：只断言 mock 被调用/返回所设值的；
   grep 源码/文档/文案的；断言常量或配置等于自身的；与既有测试重复的。仓库里仍有大量 pin 测试（`TestTheRolesFileLeavesTheObservedProjectsAndTheRulesAsTheyWere`
   对 `rules/acceptance.md` 做 git blob 哈希），它们既是防护也是 §2.1(6) 的循环来源。**未核实**：有多少测试属于这四类（没人统计过）。
3. **没有真实集成测试层**。唯一真实的集成是 crewd/drive/gate 在真实 GitHub、真实 Claude 会话上跑。因此 gate 的 `Probed on the real machine`
   这类**人工步骤**被塞进流程里（AGENTS.md step 6："Tests replace the machine with fakes, so they cannot show what the change does on real data"）。
4. **单 package 的规模效应**：`internal/crew` 一个 package 380 个源文件，没有可验证的 module 边界，所以放弃"按 module 选测试"
   （`dev-constraints.md`：整包 PR 运行约 6 分钟，其中 4 分钟是这一个 package 的测试）。改为用 `docs/design/modules.md` 的 module 表 + `crew issue check`
   的 `modules-span`/`modules-no-module`/`modules-interface` 在**写 issue 时**强制"一个 issue 一个 module"，用 `ratchet.json`
   给大文件定 ceiling（800 行上限，新文件 600；frozen 文件各自有一个 ceiling，只降不升，如 `window_start.go` 2327、`drive.go` 2101）。
   坑：ceiling 太死（"a file over 800 lines may not grow" 曾卡住一个三行 bug 修复，所以改成每个 frozen 文件 +50 行的 ceiling）。

### 7.2 CI 慢 / OOM 的具体数字

- **CI 之前的度量（`dev-constraints.md`，2026-11-04）**：本地 `go test ./internal/crew` 132 秒，其中**一个测试**（look_test.go 的 60 秒 health-wait，
  fake `HTTPGet` 真的 sleep，`lookWaitHealth` 读真实时钟）睡 60 秒；其余 6579 个 test+subtest 串行 72 秒；**3074 个测试 0 个调用 `t.Parallel()`，
  29 处 `time.Sleep`**。CI 比本地多 90–110 秒，解释是"在一次性 arm64 容器里冷编译 200k 行"。
- **后来的做法**：#1445 把 60s 的测试降到 2s 内；S4a..n 一模块一个 issue 把 `t.Parallel()` 扫完（PR #1482–#1516；现在
  519 个测试文件里 502 个含 `t.Parallel()`，`ratchet` 用 go/ast 拒绝新增/改动但没 `t.Parallel()` 的 `Test*`，
  除非用 `t.Setenv`/`t.Chdir`）；#2997 两个部署故障测试真的等 20s 和 10s（占最慢 39 秒里的 30 秒，原因：`App` 没注入 `Now`/`Sleep`，
  `serviceSettled` 要 3 次好探测间隔 5s）。
- **owner 自己的 runner 的内存盘**：PR 测试的临时文件写到 `/pt`（1200m 内存盘），因为 SQLite 的 fsync 在 CI 磁盘上比 Mac 慢 25–60 倍
  （#1523）。但 `/pt` 算进内存，所以 go build 的 `GOTMPDIR` 要放回磁盘（#2719）；PR 的命令是 `go test -p 1 -parallel 2`，
  "按 owner 2026-11-04 的话限制在大约两个核"。
- **OOM**：
  - 2026-11-24：lint 步骤在 self-hosted runner 上 **exit 137**，main 上 3 次，导致那些 head 的 go test 结果**未知**（#3067）；
    修法 `GOGC=50` + `--concurrency=2`。
  - main 的 push run（带 `-race`）**5 次在 attempt 1 被杀**，无失败测试，attempt 2 都过（#3269）；原因判断：race detector
    乘大每个测试二进制的内存，`-p 2` 同时跑两个；修法 `-race -p 1 -parallel 2`。（**此判断是 issue 里的推断，修后是否消除 未核实**）
  - 用户所述的 "runner 内存 6g" 在仓库内找不到出处，runner 镜像在仓库外 —— **未核实**。
- **教训**：CI 的瓶颈随 agent 的产出增长，而不是随人的产出增长，所以要有 `ci_runs` 表（`ci-durations.md`：crewd 顺带记录每次 CI 的 started→completed，
  不增加任何 GitHub 调用）和 "30 slowest tests" 的 job summary；"一个 PR 的 CI 等待"是 drive 的主要延迟。
- **并行 fix**：owner 在 2026-10-30 说"测试要控制，不要越写越多"。`fewer-crew-tests`：每个 issue 要最少的新测试，table case 优先，不在本地跑整包。

### 7.3 sleep / 并发的教训

- 测试里的 `time.Sleep` 与真实时钟：#1445（60s）、#2997（20s+10s）。**规则：时钟和 Sleep 必须可注入**（`App.Now`、`App.Sleep`）。
  一个 issue 命名错了符号（`Machine.Sleep` 实为 `App.Sleep`，#2171）也说明"Checked against"要真去 grep。
- 并发测试的竞态：#2624 `TestDaemonTermLeavesDrivesRunning` 在 CI 里挂过一次（0 starts），本地 200/200 通过；根因读代码才发现：
  fixture 在"第一次 `a.Sleep` 调用（任何 goroutine）"发 SIGTERM，而 `queueBeat.rlock` 内部也 sleep 50ms，SIGTERM 可能先到。
  worker 的证明方式值得学：**临时加一个 `time.Sleep` 复现，修后消失，不提交**。#2727 `TestTelegramOneMergeAtATime` 当 worker 没拿到第一次 tap 时 hang。
  #3269 见上。
- 硬超时：socket 测试没有时间上限，CI hang > 30 分钟（telegram_platform #18）。

### 7.4 真正抓到 bug 的测试/检查

- **真机探针**（单一最有价值的一类）：optdesk 上的 `crew design review`、浏览器看 board、`crew sandbox probe`、`crew issue check` 对一个真实草稿跑。
- **table 测试遍历"所有原因"**：stop-fixer 要求"一个 table test 走遍 `queue_loop.go` 里每一个把条目置成 `needs_attention`/`waiting_owner` 的地方"
  和"`decide.go` 里每个 cause 常量、每个 releaser"。这类**穷举**防止"新增状态忘了路径"。
- **pin 测试**（正反两面：抓到过声明和规则的不一致，也制造了很多 CI 红）。
- **ratchet**（文件行数、`t.Parallel`、`t.Setenv`）：机械、便宜、真有效，抓到 #2245、#2374、#2803 的越界。
- **`crew issue check`** 在写 issue 时抓，成本远低于 verify/review 一轮。
- **precheck**（gofmt、ratchet、pins、vet、lint）在作者 workspace 里跑（#3256）。
- **review 对 head 的绑定**：评审、CI、verify 必须是**同一个 head**，否则不算数——这条阻止了很多"先批后改"。

---

## 8. 合并与冲突

### 8.1 机制

- **final-stage slot**（`AGENTS.md` step 7）：每个 `<owner>/<repo>` 一个独占文件锁，一次只有一个已批准的 PR 通过
  "从 base 更新 → 检查 → integration review → verify → late check → merge"，避免合并后下一个 PR 落后 base。
  verify 和 full-tests 在锁**外**（#809）；等锁的 drive 放弃自己的 drive slot（#738）；bug/priority issue 优先拿锁（#2336）；
  `[drive] parallel` 限制同一个仓库同时的 drive 数（#639，现在 agent-crew 是 4–6，当时曾经是 3）。
- **`base_moved = "merge-if-disjoint"`**（#841、#847，owner 2026-10-30）：base 动了、但 PR 改的文件与 base 自合并基以来改的文件不相交且 GitHub 报
  MERGEABLE 时，**不重跑检查/评审/verify**，直接用 expected head sha 合并；合并记录写 `combination_tested = false`。
  承认"合并后的组合是没测过的"，由 main 的 push 与夜间 full run 兜底。
- **integration review**：作者只能"一轮一个新 commit，不 amend、不 rebase、不 force-push"，下一轮评审只看自上次评审 commit 以来的变化；
  如果唯一变化是"干净、可复现的 base 合入"（crew 用 `git merge-tree` 复现并确认 PR patch 不变），评审只回答
  "base 带来的东西是否改变了已批准 patch 的行为"。
- **late check 对 CONFLICTING 的处理**：#911 把 CONFLICTING 的 head 送回 base 更新而不是等一个永远不来的 run；#829 每次评审后只更新一次。
- **red base**：#1413 base 红时，gate 拦下 head 不含红 commit 的 PR；#1412 crewd 对 push run 的失败 job 自动重跑一次。

### 8.2 工作树

- 每个 run 一个 worktree：`<runner home>/worktrees/<project>/<leaf>`；评审用被评审 head 的 detached worktree（#1389）；
  need 级"一个需求一个 worktree 一个会话"（8143）。
- **共享 clone 被污染**（反复）：sub-agent 在共享 clone 里 `git checkout`（11-05，detached HEAD），另一个人 `git checkout origin/main`
  （11-07 16:19），此后没有任何合并 fast-forward 了它，`crew issue check` 读到过期的 HEAD，otel 草稿被判"interface not landed"约 25 分钟。
  规则：每份 brief 都写"绝不在共享 clone 里 checkout/switch，用 worktree 或 `git show`"。
- 11-11 的 `git fetch` 在共享 clone 里丢了 ref-lock 竞争（"cannot lock ref"），rebuild 失败；只改了测试文件所以没后果。
- **worktree + push.default=upstream 的组合坑**（见 8.3）。

### 8.3 绕过 gate 直接推 main（四次）

| 日期 | commit | 原因 |
|---|---|---|
| 2026-11-09 06:41 +08 | `9f634a9`（#2067 的 worker） | worker 的分支追踪 `origin/main`，clone 的 `push.default = upstream`，**main 没有 branch protection** |
| 2026-11-09 08:4x | `8859b20`（#2069；改了 `rules/dispatcher.md` 和 `rules/hub.md`，没评审） | 同上 |
| 2026-11-09 08:5x | `84cc649`（#2074） | 同上；dispatcher 自己 `git config --unset push.default`（可逆，owner 十分钟前被问过）来止血 |
| **2026-11-27** | `799ecad`（设计 pin-what-followup） | `git worktree add -b X <path> origin/main` 让新分支的 upstream 是 main，`git push origin X` 因 `push.default=upstream` 直接落在 main，绕过 PR、design session、gate；事后补了 5aaa898"judged after it reached main by a direct push" |

规则：`git worktree add --no-track -b X <path> origin/main`，推送用 `HEAD:refs/heads/X`，推完用 `git ls-remote` 确认 main 没动。
**根本问题**：这条边界靠"每个人都记得"，而 GitHub 层面应开 branch protection。**未核实**：当前仓库是否已启用（9 号之后仍发生第 4 次，说明没有）。

### 8.4 并行 issue 的合并问题

- **同文件重叠**：#1284 只因 AGENTS.md/patrol.go 与 #1272 的 PR #1285 重叠就等着，三个槽空（11-03 01:5x，owner："能一起并行的任务开始并行"）；
  后来规则"按核心文件判断重叠并立即 force"。
- **设计 PR 同时改 `docs/design/modules.md`**：1924 合并后 1925 变 CONFLICTING，其 push 没有 CI；dispatcher 先推了空 commit 再看 mergeable。
- **一个 base 修复被它自己挡住**：#1814 blocked_overlap 在 PR 1816（自己写的 base 修复）后面。
- **rename 没有 old name** 阻塞所有 issue（#1337）。
- **同一个 retry 重复开 PR**：`crew queue retry` #1466 产生了重复的 worker PR #1527。
- **issue 号和 PR 号共用一个编号空间**：dispatcher 把 PR 2409 当作重复的 issue #2409 关掉，classifier 拒绝 reopen，靠 `crew queue retry` 恢复（11-11）。
- **合并词**：gate 没有 `--owner-approved` 能放行的 PR，dispatcher 自己合；有 `--owner-approved` 的需要 owner 一句话，且**每个窗口都要重新问**（§4.1(8)）；
  PR #1375 批准+verify 通过后在 `waiting_owner` 躺了约 1 小时没人问 owner（11-03）。
- **late 合并 vs 重建**：合并后 crewd 自 re-exec 新二进制；改变 `queue/<project>.json` 等形状的 PR 必须能"读旧形状"（AGENTS.md 的规定）；
  `make build` 每次重启 board，所以"成批 rebuild"。

### 8.5 建议

1. 合并队列由**一个**进程持锁、串行化 final stage；并行的是"写"和"评审"，不是"合并"。
2. 默认用 `merge-if-disjoint`，但把"组合未测"显式记录并让 main 的 CI 红灯触发自动回滚/拦截后续合并。
3. 重叠按**真实 diff 的文件集**判断，而不是 issue 声明的 `Files:`。
4. main 必须有 branch protection，worktree 一律 `--no-track`。
5. 冲突/重叠的 PR 自动更新一次再评审，不让 dispatcher 手动处理。

---

## 9. 总结

### 9.1 要避免的三个坑

1. **把"状态"放在会话记忆或人工动作里**（§0、§4）。`complete` 要人点、ask 不会自动关、state page 凭记忆写、并发数在规则里、
   renew 之后规则不更新。每一个都变成过一个 bug 或一次 owner 的不满。状态必须在一张表里，由一个函数写，由事实派生。
2. **以散文做规格，再用一层层 review/verify 去补**（§1、§2）。39 条 `issue check`、一串 `Standing/Plan/Effect/Consultation` 行、
   verify 的 `cannot_check`、`Files:` 之外的必要修改、pin 测试——全部是"散文规格不可校验"的补丁。
   一轮 fix = 一个完整 worker+review+verify，成本 3 倍到 20 倍。
3. **让不可信/不可测的东西靠"约定"限制**（§5、§7、§8.3）。凭据在沙箱里、没 branch protection、测试全是 fake、
   "不要在前台跑命令"靠 memory——要么变成程序检查，要么变成权限。规则只写在文档/memory/ADR 里的，会被漏（owner 的话：
   "rules must become checks"）。

### 9.2 值得保留的三个设计

1. **一表六态一个 writer 的 task model + 每个非 running 状态都带 reason + 每种停都有归宿**
   （`task-model.md`、`stop-fixer.md`），并且是逐层上线、每层有默认关闭的开关，不影响正在跑的循环（owner 的约束）。
2. **"同一个 head 才算数"的 gate**：review、CI、verify 都必须针对同一个 head；评审和判断由**独立会话**做，
   记录是结构化输出，gate 读**记录**而不读正文，并按记录的作者重新计算独立性；加上 final-stage slot、
   `merge-if-disjoint` 显式承认"组合没测"。
3. **写入点只有一个且被登记**（`docs/writes.md`）+ **观察只读** + **所有机器访问经 `Machine`**：任何会写仓库之外的东西都有条目，
   加一个新写入或改变写入位置必须改条目并由 owner 合并；`ratchet`、`crew issue check`、`crew sandbox probe`、
   `crew usage --by pr` 这类"让规则变成可运行检查"的小工具，是比任何一份规则文字更耐用的资产。

### 9.3 若 foreman 从头做，我会先做的五件事

1. 结构化 issue（§1.4）+ 登记时检查；
2. 派生的状态视图（plan/ask/queue），不给窗口看原始账本；
3. 每阶段记成本和"是否改变了结论"，一开始就有 `--by pr`；
4. 凭据不进沙箱，作者提交、foreman push；
5. main 的 branch protection + 一个串行的 final stage。

---

## 附：未核实清单

- `#3365` 的修法是否真正消除问题（PR 未合并）。
- "runner 内存 6g"；#3269 修后 OOM 是否消失；owner runner 的真实规格（在仓库外）。
- `cost_usd` 的计价口径；`--by pr` 的统计包含哪些角色。
- 第二厂商评审"抓到了第一厂商漏的 bug"的次数（无统计）。
- 有多少测试属于 `no-useless-tests` 的四类。
- need 8143（一个需求一个会话）是否降低了上下文增长；precheck（#3256）是否降低了 CI 红链。
- 当前仓库是否开了 main 的 branch protection。
- "design session 上限 5 个 head"：设计文档写"patrol 在 3 上提示，5 上停止"，实际生效值未在代码里核对。
- 本文引用的错误清单、dispatcher memory 在仓库之外，读者无法复核，只能信其日期和编号。
