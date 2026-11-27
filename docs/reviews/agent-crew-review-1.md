# foreman 设计评审（第 1 轮）：以 agent-crew 的实际经验对照

评审对象：`spec/constitution.yaml`（C-01..C-13）、`spec/decisions.yaml`（D-001..D-034）、`spec/open-questions.yaml`（Q-004、Q-005）、`spec/design/*.yaml`（11 个）。
`ruby tools/smcheck.rb` 输出 `configs=101 events=29 rows=63 (expanded 68)` 与 `OK`，所以本文不质疑状态表的穷举性，只质疑它没覆盖的东西。
事故记录来自 `docs/lessons-*.md` 四份；agent-crew 的事实以仓库 main 为准，凡我自己重新量过的数字会写"本次实测"，其余引用 lessons 文档的写"lessons"。

约定：每条发现有 id `ACR-nn`，严重度 `blocking`（不改就不该开始实现）、`major`（实现前应改，或在设计里写明接受的理由）、`minor`。
"位置"是 spec 文件加条目 id。"最小修复"是能让该发现关闭的最小改动，不是完整设计。

## 0. 总评

foreman 的方向与 agent-crew 的教训一致：状态只在表里（C-01、D-033/034），一个写者（D-004），凭据不进沙箱（C-08），配置随运行快照（C-13），提问格式唯一（D-012），GitHub 访问收口到一个客户端（D-031），时间可注入（C-05）。这些在 agent-crew 里都是事故之后才补的，foreman 一开始就有，是实质性的进步。

但有三类问题反复出现：

1. **只有"状态机"，没有"事实回流"**：失败日志、finding、discovery 怎么进入下一次会话，协议没写全（ACR-01、07、15）。
2. **并发的合并路径没有串行化，而 agent-crew 为此付出过最多的代价**（ACR-02、03、16）。
3. **仪式成本没有预算**：每个需求固定经过 intake、planner、逐步 review、integrate review，没有小改动的快车道，也没有 H4 的基线（ACR-30、31）。

本次实测的一个数字值得先说：`crew usage --days 14 agent-crew`（只读）合计 2441.5 美元，其中 dispatcher 窗口 40.1%、advisor 窗口 17.4%（合计 57.5%），worker 14.3%，design 12.0%，approve 8.2%，review 5.8%，verify 2.2%。D-002 的理由"窗口约占 57%"因此被**确认**（口径是 crew 按 token 折算的估算值，不是账单）。但这个数字同时说明：把窗口拿掉并不会让成本消失，foreman 把同样的"思考"搬进了 intake、planner、rescuer、improver 四个 Opus 角色（见 ACR-30、31）。

## 1. 逐个真实事故：现在的设计还会不会发生

下表先给结论，后面每条有详细发现。"覆盖"表示设计里已有对应条目并足够；"部分"表示有条目但有缺口；"会发生"表示没有对应条目。

| 事故（lessons 出处） | 结论 | 发现 |
|---|---|---|
| 2026-11-27 状态记忆：ask 永不关闭、两套计数、窗口读原始账本、人工过度纠正（一 §0） | 部分 | ACR-05、06、07 |
| issue 在作者停下后被编辑，作者没重读（#561，一 §1.1） | 部分 | ACR-08 |
| 验收项沙箱里跑不了，`cannot_check` 当失败（#1239、#368、#1474 等，一 §1.1） | 部分 | ACR-09 |
| 手写 `Files:`、"只改这些文件"（#1559、#2028、#2245，一 §1.1–1.2） | 覆盖（boundaries、hints 无约束力） | ACR-27（只缺行数余量） |
| 谁来合并由规格里的键决定（#1655，一 §1.2） | 部分 | ACR-04 |
| 原因写成事实（一 §1.2，cause-proof） | 覆盖（E-BUG cause_status、cause_proof） | ACR-26（Proof 类型未定义） |
| CI 失败日志到不了作者（#3345，一 §1.3） | 会发生 | ACR-01 |
| dispatcher 评论 PR 被当作 owner 的话（#1152，一 §1.3） | 覆盖（没有评论通道，只有按钮） | ACR-25 |
| 设计评审 reject 链：30 条 note、21 条重述（optdesk PR 1784，一 §2.1） | 部分 | ACR-15 |
| CI 红链：lint/vet 在 CI 才发现（一 §2.1(3)，#3256） | 会发生 | ACR-10 |
| main 红 1 小时无人知（一 §2.1(3)，11-24） | 会发生 | ACR-02 |
| resume cap、`retry` 对 waiting_owner 无声无效（一 §2.1(4)） | 覆盖（owner 事件走按钮，计数在表里） | ACR-25 |
| spin hold 误报、CI 排队被当失败（#1258、#1262，一 §2.1(5)） | 会发生 | ACR-24 |
| pin 测试循环（一 §2.1(6)，二 §1.3） | 覆盖一半 | ACR-35 |
| `blocked_overlap`：改名、文档并发（#1335、#1814，一 §3.1） | 会发生 | ACR-16 |
| worker 无权写 `.github/`（#647/#648，一 §3.1） | 覆盖一半 | ACR-22 |
| deny 规则按文本误杀（#1368，一 §3.1） | 不适用（沙箱内不设 deny 规则） | 无 |
| 并发数来自过期规则（一 §3.3，二 §1.2） | 覆盖（CF-04 加 C-13 快照，Run 记录 config_rev 与 prompt_hash） | 无 |
| 新增 roles 键杀掉在跑的 drive（#430、#1391，一 §3.3，二 §5.2(c)） | 大部分覆盖（无 per-issue drive 进程，配置按 Run 快照） | ACR-13（自更新） |
| resume 11 次、上下文 22k 到 454k（#429，一 §4.1） | 未定义 | ACR-31 |
| owner 的要求在 renew 里丢失、账本 83% 噪声（一 §4.1(4)，二 §6.2） | 会发生（intake） | ACR-14 |
| 沙箱 host 白名单漏掉重定向落点（#1843、#3260，一 §5.2） | 会发生 | ACR-18 |
| 新 Go 依赖在沙箱里拉不到（#1541，一 §5.2） | 会发生 | ACR-18 |
| worker 带凭据、读不可信 issue（一 §5.3） | 覆盖（C-08、FG-9） | 无 |
| 沙箱探针练不到 commit+push（#2779，三 §4.5） | 部分 | ACR-29 |
| fake 太多、首次真实数据路径出错；crew 根目录等于项目 clone（#1762 等，一 §7.1） | 会发生 | ACR-17 |
| 测试里的 sleep 与真实时钟（#1445、#2997，一 §7.3） | 覆盖（C-05） | 无 |
| runner OOM exit 137、flaky 基线（#3067、#3269、#1314，一 §7.2） | 会发生 | ACR-10 |
| 最终合并串行化、base 移动后重复检查（#809、#841，一 §8.1） | 会发生 | ACR-02 |
| 直接推 main 四次（一 §8.3） | 会发生 | ACR-03 |
| 共享 clone 被污染（一 §8.2） | 会发生 | ACR-17 |
| AGENTS.md 4.3k 涨到 100k 字节，随后搬家到 113k 的 writes.md（二 §2） | 部分 | ACR-33 |
| 规则只在进程启动时送一次（二 §1.2） | 覆盖（C-13、prompt_hash） | 无 |
| 看板首页永远骨架屏、构建产物入库（二 §4.3–4.4） | 部分 | ACR-38 |
| 重启后什么都没起来、数据库损坏（二 §5.2(a)） | 会发生 | ACR-11、12 |
| re-exec 僵尸与自更新（二 §5.2(b)） | 会发生 | ACR-13 |
| owner"找不到 retry/T1" （二 §6.4） | 部分 | ACR-25 |
| 二进制误提交、文档里的本机路径、为过扫描器改测试数据（三 §1.1–1.3） | 会发生 | ACR-19 |
| 日志与错误文本里的秘密（三 §1.4，二） | 覆盖（D-026 错误文本不含值） | 无 |
| 备份文件留在 secrets 目录、新旧位置并存（四 §4.2–4.3） | 覆盖（D-026 写明备份不放该目录、一个密钥一份来源，缺检查） | ACR-19 |

### ACR-01｜blocking｜state-machine.yaml S31、S50、S58、S60，protocol-run.yaml PF-CTX

失败的检查结果到不了下一次会话。S50（`check_fail` 回到 `queued(code)`）的效果只有 `inc:check_fail`，S31、S58、S60（`l1_fail`）只有 `add_fix_step`，都没有 `add_findings`。PF-CTX 的九个小节里没有"上一次失败的输出"。coder 重跑时不知道为什么失败，会在 3 次上限内盲改，然后进入 rescuer。
证据：agent-crew #3345（`EventChecksRed` 不带 payload，被拒文本进不了下一轮）；#3079 把 CI 日志回灌作者是它"流得好"的唯一一处；#3260 说明日志本身要能读到（foreman 的 OP-10 已由 foremand 下载，所以这点是对的）。
最小修复：S50、S31、S58、S60 加效果 `add_findings`（kind=`check`，内容是经 privguard 的日志尾部 N 行加失败测试名）；PF-CTX 增加小节"上一次失败（≤ N 行，已脱敏）"；加测试 `test:check-fail-reaches-next-context`，断言 check_fail 后新 Run 的 context.md 含该小节。

### ACR-02｜blocking｜state-machine.yaml reconciler `running(merge)`，resources.yaml RES-SLOTS，forge.yaml FG-7、OP-05

没有每个仓库一把的合并锁，也没有"main 红则不合并"的闸。reconciler 的合并是"update-branch、等 L1、带 head sha 合并"。两个需求同时到 `integrate/merge`：A 合并后 B 落后于 base，B 必须再 update-branch 再等 L1，期间 C 又合并，B 的 L1 绿了也合不进去。RES-SLOTS 只有 `session`、`l0-check` 两种槽。FG-7 只轮询打开的 PR，main 上的 push run 失败没有事件，要等 nightly（D-008）。
证据：AGENTS.md 第 7 步的 final-stage slot 就是为此而设（#809 verify 放锁外，#738 等锁者放弃 drive 槽，#2336 bug 优先拿锁）；#841/#847 的 `merge-if-disjoint` 是为了省掉重复检查（owner 2026-10-30 的决定）；#1413 base 红时拦截 head 不含红 commit 的 PR；2026-11-24 main 红 1 小时无人知，gate 还放行了含首个红 commit 的 head（lessons 一 §2.1(3)）。
最小修复：RES-SLOTS 增加 `merge/<owner>/<repo>`，容量 1，从 `update-branch` 持有到合并或放弃；轮询加上 base 分支最近一次 push run（OP-08），红则 Gate 返回 `Wait(reason=red_base)` 并创建 P0 的 bug 需求；可选的 `merge-if-disjoint`：PR 的真实改动文件与 base 自合并基以来的改动文件不相交且 GitHub 报 MERGEABLE 时不再重跑 L1，记录 `combination_tested=false`。测试 `test:one-merge-at-a-time`、`test:red-base-blocks-merge`。

### ACR-03｜blocking｜decisions.yaml D-009，forge.yaml OP-12，modules.yaml workspace

D-009 把"main 分支保护"当作沙箱外的锁之一，但这个能力可能根本拿不到，而且 spec 没写替代控制。本次实测：`gh api repos/<agent-crew>/branches/main/protection` 返回 403，文本为"Upgrade to GitHub Pro or make this repository public to enable this feature"（该仓库是私有的）。这解释了为什么 agent-crew 在 11-09 之后又发生了第 4 次直接推送。foreman 自己的仓库是私有还是公开、什么套餐，spec 没提。
证据：直接推 main 四次：`9f634a9`、`8859b20`、`84cc649`（11-09，原因是 worker 分支追踪 `origin/main` 加 `push.default=upstream`），以及 `799ecad`（11-27，`git worktree add -b X <path> origin/main` 之后 `git push origin X` 落到 main，绕过 PR 与 gate）。
最小修复：(1) 接入项目时调用 OP-12，403 或 404 记为 `protection: unavailable` 并作为 owner 可见事实（不是静默通过）；(2) 无论保护是否可用，workspace 模块强制：所有 worktree `--no-track` 创建、克隆里设 `push.default=nothing`、foremand 只用显式 refspec `HEAD:refs/heads/<分支>` 推送、推完用 `ls-remote` 断言 main 未变；(3) 把"仓库公开或升级套餐"列为 owner 决定。测试 `test:push-refspec-only`、`test:worktree-no-track`。

### ACR-04｜blocking｜decisions.yaml D-005 的 H6、D-010，constitution.yaml 总则

"改护栏触发 H6"没有机器可读的定义。improver（Opus 加 advisor）可以提交修改 `prompts/reviewer.md`、`prompts/rescuer.md`、`internal/stage/review`、`.github/`、隐私扫描配置、沙箱策略的需求，只要走完 Sonnet 的 reviewer 就合并。reviewer 自己的提示词就在被改的集合里，独立性在这里消失。
证据：AGENTS.md 说"路径清单说不全"，所以靠 `owner_only` 加判断；`8859b20`（改了 `rules/dispatcher.md` 与 `rules/hub.md` 的直接推送，没评审）；同时 owner 合并词曾达每天约 13 个，其中 40 个等 owner 的 PR 里 33 个只因为 `AGENTS.md/rules/projects/roles.toml` 在 `owner_only`（一 §2.2），说明清单太宽也不行。
最小修复：新增数据文件 `spec/guardrails.yaml`（路径与 glob：constitution、spec/、prompts/reviewer、prompts/rescuer、sandbox 策略、privguard 配置、`.github/**`、internal/stage/review、internal/pipeline/state），由**真实 diff** 判断而不是由需求字段判断，命中则必须走 H6，且同一需求不得同时改动 reviewer 与被 reviewer 度量的指标。测试 `test:guardrail-diff-needs-h6`。清单要短（agent-crew 的 `advisor_ok` 思路：宁可窄而强）。

### ACR-05｜major｜entities.yaml E-REQ `derived_phase`

`derived_phase` 不是全函数，至少有三个洞，在洞里需求没有任何阶段：(a) 所有 PR 已合并、所有 Step 为 done，但反馈报告尚未生成（`done` 要求报告已生成，`integrating` 要求至少一个 PR 打开，其余都不成立）；(b) 所有 work 步骤完成、integrate 步骤尚未创建（DT-2 之前）；(c) H09/H16 `drop_all_steps` 之后所有 Step 为 dropped，但效果没有写 `dropped_reason`，于是 `dropped` 不成立，而 `intake` 要求"尚无 Step"。agent-crew 2026-11-27 的 §0 事故的根子就是"完成"没有从事实全函数地推出。
最小修复：加兜底分支（例如 `reporting`、`stalled`）并让 `drop_all_steps` 同时写 `dropped_reason`；加测试 `test:derived-phase-total`，对 (Step 状态组合 × PR 状态 × 报告状态) 穷举，断言恰有一个 phase。

### ACR-06｜major｜entities.yaml E-ASK，state-machine.yaml 表 B，tools/smcheck.rb

OwnerAsk 没有 `status`：它只有 `answer` 与 `deadline`。需求被丢弃、步骤被 drop、hold 已被别的路径解除时，没有任何规则关闭这条 ask，会重演 agent-crew 的 33 条陈旧 open ask（最早回到 11-08）。另外，表 B 里 `owner_answer` 只在 `[owner, parked]` 下有行，陈旧的点按（ask 已关闭后到达）既不在表里也不在 `ignored`，smcheck 对 req_hold 表只检查"已有行的守卫穷尽"，不检查 (hold, event) 全覆盖。计数口径：spec 没有规定"未处理 ask 数"由哪一处计算，web、Telegram、CLI 各算一份就会重演 hook 的 190 与 CLI 的 0。
证据：lessons 一 §0（`printAskCounts` 190 对 `list --unprocessed` 0；#3365、#3369、#3371 一天内连补四个 PR）；二 §6.2。
最小修复：E-ASK 增加 `status ∈ {open, answered, moot, expired}`，只由 Transition 写（subject 进入终态或 hold 被别的路径解除时置 `moot`）；全仓一个 SQL 视图 `v_open_asks`，web、Telegram、CLI 只读它（测试 `test:ask-counts-single-source`）；smcheck 增加对表 B 的 (hold, event) 全覆盖检查，未列出的组合必须在 `ignored` 里并记录日志。

### ACR-07｜major｜entities.yaml E-DISC，protocol-run.yaml PF-CTX

discoveries 只追加，没有证据字段，也没有撤回。会话写入的 `answer`、`spec_conflict`、`note` 是 AI 的断言，之后原样进入每个后续 context.md 的"已有 discoveries"。错误的一条会永远污染该需求，而且违反了 foreman 自己的"没有证明的原因只能是怀疑"（E-BUG）。
证据：lessons 一 §4.1(6)（把未经验证的 sub-agent 结论当事实交给 owner）；一 §0（给 AI 看原始账本，它会照字面当真）；agent-crew 的 cause-proof 设计要求 `Proof:` 行。
最小修复：`kind ∈ {answer, spec_conflict, extra_file}` 必须带 `evidence`（文件加行号、命令加输出行）；加 `supersedes: <discovery id>`；context.md 只渲染未被取代的条目；reviewer 可把 discovery 标 `rejected`。测试 `test:context-hides-superseded-discoveries`。

### ACR-08｜major｜protocol-run.yaml PF-RESULT、PP-3，protocol-plan.yaml PV-1..7，entities.yaml E-RUN

Run 记录了 `config_rev` 与 `prompt_hash`，没有记录 `spec_version`。结果以 `run_id` 幂等，但不检查它基于的 spec 是否还是当前版本。replan 期间"running 步骤跑完当前 Run 后进入 waiting(hold)"，该 Run 的结果仍按旧验收文本入库；planner 对 `keep` 的步骤也没有"验收文本变了则不得 keep"的校验。
证据：#561（issue 在作者停下后被编辑，下一次 resume 用固定文本，作者没重读，同样原因再停一次；修法是存 `issue_digest`）。
最小修复：E-RUN 增加 `spec_version`；结果入库时若该步骤引用的任一 acceptance 文本在 spec_version 之后变过，则结果按 `bad_result(stale)` 处理并重新入队；protocol-plan 增加 PV-8：被 `spec_patch` 改过的 acceptance 所引用的步骤不得 `keep`。

### ACR-09｜major｜protocol-run.yaml PF-RESULT.acceptance，entities.yaml E-ACC，state-machine.yaml dims.types

foreman 没有 verify 阶段，也没有任何一处声明"这是有意取消的"。验收的结果是 coder 自报的 `pass|fail`加一段自由文本 evidence，`verify: command` 没有执行者也没有只读 argv 白名单，coder 无法表达"我在沙箱里做不到"（枚举里没有 `cannot_check`，只能写 fail 或虚报 pass）。
证据：agent-crew 的 verify 一次只要 0.11 美元（本次实测：499 次共 54.6 美元，占 2.2%），是全系统最便宜的阶段；`cannot_check` 造成的停机至少 #1239、#1361、#1362、#368、#359、#1474；"真机行为只有跑了才知道"（PR 2411 的 `Probed on the real machine`）。
最小修复：(1) 在程序化的 `check` 阶段里由 foremand 在沙箱内执行每个 `verify ∈ {test, command}` 的条目（argv 必须在只读白名单上），与 coder 自报的结果比对，不一致即 `check_fail`；(2) `verify: reading` 的条目由 reviewer 逐条判断并写进 findings；(3) PF-RESULT 增加 `cannot_check`，等价于 `reading`；(4) 在 decisions.yaml 增加一条"不设独立 verify 会话"的决定与理由，或者恢复一个便宜的 verify。

### ACR-10｜major｜decisions.yaml D-007，state-machine.yaml S30..S34、S31、S60，forge.yaml OP-09

L0 有 build、vet、gofmt、改动 package 的 -short 测试，**没有 lint**；L1 有 lint。每个只在 L1 才出现的 lint 失败都变成 `add_fix_step`，即一个完整的 code、check、review 轮次。另外 L1 失败不区分真失败、runner 被杀、基线本来就红：S31/S60 一律生成 fix 步骤，OP-09（`RunRerunFailed`）定义了但没有任何转移使用它。
证据：2026-11-25 一天 22 个 PR 的 `test` run 里 7 个失败，其中 4 个死在 lint、1 个 vet（#3256 precheck）；lint 步骤 exit 137 使 3 个 head 的测试结果未知（#3067）；main 的 push run 5 次在第一次尝试被杀、第二次都过（#3269）；`flaky_base`（#1314、#1374、#2665、#2676）。fix 轮次的成本倍数：0 轮 169 个 PR 平均 0.75 美元、1 轮 43 个 2.42、2 轮 10 个 6.27、3 轮 1 个 16.07（lessons 一 §2.2，`crew usage --by pr`，未重新实测）。
最小修复：L0 运行与 L1 完全相同的 lint 命令（`test:l0-covers-l1` 比较 workflow 里的命令与 L0 配置）；`l1_fail` 先分类：job 结论为 cancelled、退出码 137、没有失败的测试、或同样的失败在 base 上也有，则用 OP-09 重跑一次，不进入 fix 步骤；只有重跑仍失败才 `add_fix_step`。

### ACR-11｜major｜decisions.yaml D-004、D-011

唯一的权威记录是三个 sqlite 文件，没有启动自检、备份、WAL checkpoint 的规定；"前期不做开机自启"加上唯一的 Telegram 发送者就是 foremand，意味着 foremand 死了没有任何东西告诉 owner。
证据：2026-11-11 机器重启后什么都没起来，同时 `crew.db` 报 "database disk image is malformed (11)"，由 `sqlite3 .recover` 手工修复；随后 `e9e72fa`（共享 flock）、`5cebdfc`（持久提交加每小时 checkpoint）、`17f4d5d`（启动时检查并修复）；patrol --watch 把"窗口叫不醒"推给 owner（#2533）。foreman 的单写者（C-01、D-004）消除了 agent-crew 的"多个 hook 同时打开"，这点是对的，但损坏之后的恢复仍然没有。
最小修复：foremand 启动的第一步固定为（取锁、`PRAGMA integrity_check`、孤儿进程回收、配置 revision 检查），每步一行日志，`foreman status` 能打印；每小时 `wal_checkpoint` 加 `VACUUM INTO` 到仓库之外的备份目录（保留 7 份）；web"现在"页显示 foremand 最近心跳时间，超过阈值变红（owner 看页面就知道，不依赖进程自己报死）。

### ACR-12｜major｜decisions.yaml D-011、D-026，access.yaml

D-026 默认的 `secrets_dir` 取自 agent-crew 的 `services.toml`，在本机上是数据卷；D-011 日后要做开机自启。agent-crew 的结论是 launchd 启动的程序打不开数据卷上的服务、数据和密钥（`rules/service.md`："a program that launchd starts may not open anything on the data volume that holds the services, their data and their keys"），所以才有 keeper 加 tmux。两个决定合起来会在做自启的那一天失败，而且错误会表现为"读不到密钥"。
证据：lessons 四 §4.4。
最小修复：D-011 写明启动方式（keeper/终端起 foremand，沿用 agent-crew 的做法），或 D-026 写明 `secrets_dir` 必须在系统卷；加入接入检查 `foreman doctor`：从实际启动环境里检查能否打开 secrets 目录（只验权限位和可达，不读内容）。

### ACR-13｜major｜decisions.yaml D-010、D-011，缺失条目

foreman 合并自己的仓库（D-010 自我改进），但没有任何条目规定"合并之后 foremand 怎么换成新二进制、坏了怎么回退"。agent-crew 的原地 re-exec 留下三类后遗症：僵尸 drive（#1307，2026-11-11 PR 2431 合并时 live 证明没跑）、旧二进制读新配置而崩（#430、#1391）、启动期 re-exec 使步骤"start not confirmed"（#1324）。foreman 的 C-13（Run 快照配置）和 PP-1（会话独立进程组、foremand 退出不影响）已经避开了前两类，剩下"自己换自己"本身。
最小修复：新增决定：自更新 = 从合并后的 commit 构建到版本化目录，用 scripted harness 跑 `foreman selfcheck`，原子切换符号链接并保留上一个二进制；新进程 60 秒内健康检查不过则执行上一个；数据库迁移单向，库里记录 `min_binary_version`，旧二进制遇到更高版本只读并报错而不是崩。测试 `test:selfupdate-rollback`。

### ACR-14｜major｜skills.yaml role intake，decisions.yaml D-002，entities.yaml（无 draft 实体）

intake 是"交互窗口"（Opus），而 D-002 的前提是"不设常驻 AI 协调窗口"。intake 的草稿在 submit 之前存在哪里，spec 没写：E-REQ 的 `spec` 必填字段在入库时校验，草稿没有位置。对话中 owner 说过的话如果只在会话里，会话重启就丢，这正是 agent-crew 建 ask ledger 的原因，而 ledger 后来 83% 是噪声（8,508 行里跳过 7,039 行）：根因是账本没有可信的"发言人"字段。
证据：lessons 二 §6.2；一 §4.1(4)（owner 2026-10-31 的要求在 renew 里丢失）；窗口 renew 的成本（#2993 把阈值从 70% 降到 40%）。
最小修复：每份草稿一个 intake 会话，submit 时结束；owner 的每一轮话由 harness 的 user message 事件（不是文本猜测）写成 `intake_turns(req_id, speaker, text, at)`；允许 `status: draft` 的需求持有未通过校验的草稿；会话从存储的草稿重建，不从 transcript 重建。

### ACR-15｜major｜protocol-run.yaml PF-CTX、PF-RESULT，entities.yaml E-FIND

reviewer 的输入没有定义：PF-CTX 对所有角色是同一份九节，没有"自上次评审以来的 diff"。E-FIND 有 `round`、`close_when`，但没有"第二轮起 finding 必须落在新改动的行上"的规则，也没有去重键。`review_rounds` 上限 2 使其有界，却不能防止用一轮的钱买一堆重述。reviewer 的 context 里还有 coder 的 `note` 与 evidence，会产生锚定。
证据：optdesk PR 1784 的设计评审：第 2 到 12 轮、30 条 note 里 21 条是同 5 条的重述，15 条 blocking 落在上一轮刚改过的文字上，**没有一条新 finding 落在未改动的文字上**，每个 session 约 1.76 美元；#2503、#1780 的连续 head 上限；#2499/#2510 把 findings 改成带 section 和 close 条件的对象。agent-crew 的 integration review 只看自上次评审 commit 以来的变化。
最小修复：reviewer 的 context.md 只含 spec、当前步骤的验收、自上次评审 commit 起的 diff、未关闭的 findings，不含 coder 的自述；程序规则：round ≥ 2 的 finding 若 location 不在 delta 内则存为 `minor`；去重键为 (path, close_when 的哈希)。

### ACR-16｜major｜protocol-plan.yaml PV-4，decisions.yaml D-016，modules.yaml

并行步骤是否相交按 planner 声明的 `touches`（模块名）判断。模块粒度太粗（`pipeline` 4 个 package、`stage` 6 个），PV-4 会把同模块的无关步骤全部串行化，等于关掉 D-016；同时对声明之外的文件一无所知。跨需求的重叠 spec 没有处理。
证据：#1335（"a rename of the pull request has no old name"，文件其实不相交，所有 issue 被挡）；#1284 只因与 #1272 的 PR 重叠，三个槽空（11-03，owner 要求"能一起并行的任务开始并行"）；lessons 一 §8.5(3) 建议按真实 diff 的文件集判断。
最小修复：`touches` 只作排序提示；真正的冲突在 squash 合回时由 `git merge-tree` 在真实文件集上判定，冲突才串行重做；跨需求由 ACR-02 的合并锁加 `merge-if-disjoint` 处理。PV-4 的"自动加依赖"改为仅当两个步骤的声明相交**且**历史上该模块冲突率高时才用。

### ACR-17｜major｜design/config.yaml CF-01，modules.yaml workspace，decisions.yaml D-010

CF-01 的"仓库路径"没有说 foremand 是否直接使用 owner 的工作检出。若是，owner 的 `git checkout` 与 foremand 的 fetch、快进相互污染；若 foreman 的第一个项目就是它自己（D-010 自我管理），那么 foremand 的运行目录与项目 clone 是同一个目录，这正是 agent-crew 最贵的一类 bug 的成因。
证据：共享 clone 被污染四次（11-05 sub-agent 的 detached HEAD 使 `crew issue check` 读过期 HEAD 约 25 分钟；11-07；11-11 ref-lock 竞争；11-14，lessons 一 §8.2、二 §1.5）；第一个非 crew 根目录的项目 optdesk 一小时内撞到 #1762、#1764、#1765/1766/1769，owner："跑了最多的资源，错了最多的 bug"。
最小修复：workspace 拥有 `~/.foreman/projects/<p>/repo.git`（镜像），所有 worktree 从它创建，永不触碰 owner 的检出；CF-01 的 `repo` 是远程地址；增加 L2 测试 `test:project-not-daemon-root`（在与 foremand 源码目录不同的临时仓库上跑完整流水线冒烟）；"流水线完成"的验收包含一次在第二个项目上的运行。

### ACR-18｜major｜access.yaml R0、`access_request.routing`

R0 的主机清单是静态的，每个漏掉的重定向落点变成一次 R2 的 owner 询问（S79：`hold` 加 H6）。包仓库的 zip 重定向到别的主机、CI 日志重定向到 blob 存储，都是配置者提前列不全的。依赖也有同样的问题：新增的 Go 模块在沙箱里拉不到。
证据：#1843（`proxy.golang.org` 把 zip 重定向到 `storage.googleapis.com`，worker 无 PR 而停；foreman 的 R0 已列该主机，说明它学到了）；#3260（API 302 到 blob 存储，不在 `githubHosts`，lint 失败不能自动修）；#1541（新依赖只能人工预填模块缓存）。
最小修复：R0 按"主机族"列（并保留明确例外），沙箱代理把被拒主机写入日志，聚合成每天一条的 H6 询问而不是每次一条；新增 go.mod 依赖由 foremand 在沙箱外预取（只下载，不执行）。

### ACR-19｜major｜constitution.yaml C-09，decisions.yaml D-014/D-018，缺失 `design/privguard.yaml`

privguard 没有设计条目：P1–P7 在 spec 里找不到，C-09 的 `check:privguard` 没有可验证的内容。optdesk 的五条硬规则（lessons 三 §6）和三个真实泄漏都没有落到任何条目：编译产物误提交（`cmd/ot/ot` 约 38 MB，`.gitignore` 只写了锚定的 `/ot`，至今在公开历史里）、扫描器按目录整体豁免 `docs/design/`（真实本机路径漏过）、为过扫描器把测试数据改得"不像账号"。此外 coder 提交用什么身份、sandbox 里 git 的全局配置怎么处理，spec 也没写。
证据：lessons 三 §1.1–1.3、§6；一 §5.2（git 在不可读的全局配置上会直接死，所以其他角色要把 `GIT_CONFIG_GLOBAL` 设为 null device）；四 §4.2–4.3（旧位置没清、备份留在 secrets 目录，D-026 写了规则但没有检查）。
最小修复：新增 `design/privguard.yaml`，每条规则一个测试：无目录级豁免（豁免表只允许行内标记，`test:privguard-no-dir-exemption`）、只许源码类文件入库（白名单加魔数拒绝 ELF/Mach-O/PE）、大小检查作用于 PR 范围、`git log base..head` 的作者与提交者必须是 noreply、PR 标题与正文与 commit message 同样扫描、泄漏表测试；sandbox 启动固定 `GIT_CONFIG_GLOBAL=/dev/null` 并用 `-c user.name/user.email` 注入项目声明里的 noreply 身份；检查 secrets 目录无同名键的第二份来源、无 `*.bak*`（只比键名，不读值）。

### ACR-20｜major｜prod-access.yaml PA-2、PA-3、incident_flow，decisions.yaml D-021

三处：(a) PA-2 `restart` 没有触发条件，diagnoser 一提议就每服务每小时最多 2 次自动重启，而 PA-1 有"24 小时内有部署且健康检查失败"；(b) PA-3 `data_fix` 的 `dry_run_output`、`affected_rows` 全是 AI 自己写的，foremand 没有执行干跑，H3 询问展示给 owner 的数字无法核对；(c) diagnoser 读的线上日志是不可信文本（用户输入会进日志），可以注入"提议重启"。
证据：agent-crew 把重启与回滚都放在 crew 自己的动作里并限速（`cap_deploy_rate.go`，一小时内部署加回滚达到 10 次就挂起，#3128），AGENTS.md 明确禁止 agent 触发 `crew service restart`；lessons 一 §4.1(6)（AI 自报的数字当事实）。
最小修复：PA-2 加条件（连续 N 次 `prod.health` 失败）；H3 询问里同时展示 foremand 用同一谓词通过 `prod.sql_ro` 计算的 `count(*)` 和 AI 自报的数字，不一致标红；context.md 里线上日志以明确的"不可信数据"分隔符包裹，提议的合法性由条件判定而不是由文本判定。

### ACR-21｜major｜decisions.yaml D-010，skills.yaml improver

改进是否达标"再跑 10 个需求对比指标"。agent-crew 的数据显示每个 PR 的成本分布极度偏斜（169 个零轮、43 个一轮、10 个二轮、1 个三轮），10 个样本的均值没有统计意义；指标本身（是什么、基线是什么）没有定义；"回退需求"由低质量比较自动生成会引入新的抖动。
证据：lessons 一 §2.3（agent-crew 到 11-27 才有 `crew usage --by pr`，#2949；"一开始就记录每阶段成本和是否改变了结论"）；二 §3.3（测量存在、闭环缺，没有对照实验）。
最小修复：在 spec 里定义指标（每合并需求成本、零 fix 轮次比例、每阶段是否改变了结论）；样本不少于 30 或使用序贯检验；回退只在护栏类指标（零 fix 轮次比例）明显变差时自动触发；Run 记录增加"该阶段是否改变了结论"字段，从第一天开始记。

### ACR-22｜minor｜forge.yaml `auth.initial`

细粒度 PAT 的权限清单里没有 `workflows`。据 GitHub 的公开行为，推送修改 `.github/workflows/*` 的提交需要该权限，否则被拒；而允许 AI 修改 CI 又是 ACR-04 里的护栏类改动。**来源说明：这不是 agent-crew 的实测证据**（agent-crew 通过 gh 的登录推送），请在一次性测试仓库上确认。
最小修复：明确选一边：要么不授予 `workflows`、foremand 在推送前拒绝触及 `.github/**` 的 diff 并转 H6；要么授予并把 `.github/**` 列入 `guardrails.yaml`。

### ACR-23｜minor｜open-questions.yaml Q-004/Q-005，state-machine.yaml S18、S34、S72、S74、H17，entities.yaml E-ASK `h_item`

两个未决问题已经写进状态表：H7 出现在 4 行里，`parked` 出现在 H17，而 D-005 仍写 H1–H6，修改 H 清单本身是 H6。D-034 对 D-005、D-006、D-016、D-033 都是 `amends`，读 D-005 单条会得到过时的阶梯。
最小修复：这两个问题按 lessons 的证据先决：Q-004 取 H7（"持续基础设施故障通常是配置问题"，agent-crew 的 stop-fixer 同样是"起不来的会话 60 分钟后给 owner 一整包"），Q-005 取 `parked`；表里引用未决问题的行标 `pending: Q-004`，spec-lint 在存在未标注的引用时失败。

### ACR-24｜minor｜state-machine.yaml S33、dims.waiting_reasons.ci

`waiting(ci)` 35 分钟到期一律计入 `infra`（最多 6 次，退避合计约 7 小时）。runner 正忙于别的任务（排队有 runner）与"没有 runner"被一视同仁。
证据：#1258/#1262（"checks queued with no runner" 的误报停止，空转 1 小时，11-02 19:19–20:20）。
最小修复：run 处于 queued 且有 runner 在线、或 in_progress 时延长 deadline、不计 infra；只有"找不到 run"或"queued 且无 runner"才计。

### ACR-25｜minor｜decisions.yaml D-012、D-013，state-machine.yaml 表 A/B

owner 终止或重试一个需求/步骤的事件没有定义：表 A 的 `drop` 是步骤级且没说谁发；表 B 只有通过 ask 的 `owner_answer`；D-013 的三个页面没有动作按钮。agent-crew 的"找不到 retry"隔了几周才出现看板按钮（`e1480cf`，#3310）。
最小修复：增加事件 `owner_drop`、`owner_retry`，只由看板按钮或 Telegram 按钮产生；每条 ask 的 id 同时是 Telegram callback 数据和 web 卡片锚点；Telegram 消息正文不依赖 500 字符之后的内容。

### ACR-26｜minor｜entities.yaml E-BUG、E-SPEC、C-02

`Proof`、`Ref`、`Counters` 作为类型出现但没有定义（`Counters` 只是指向 state-machine）；C-02 要求每个非终态都有 "owner"，Step 没有 owner 字段。C-12 要求引用可解析，现在无法解析。
最小修复：补 E-PROOF（kind: repro|code，字段 cmd、output_line 或 path、line）、E-REF，C-02 里的"owner"改成 deadline 加出口事件，或在 Step 上定义责任角色字段。

### ACR-27｜minor｜protocol-run.yaml PF-CTX，constitution.yaml C-03

PF-CTX 只给"相关文件路径"，没有给 C-03 的余量。coder 无从知道某个 package 离 3000 行上限还有多少，会在 check 阶段才撞上。
证据：#2245（`issue_modules.go` 796 行，worker 推到 852，ratchet 拒绝，PR 2247 关掉重来）、#2374、#2803（都是没数行）。
最小修复：context.md 对每个触及的 package 列出非测试行数、测试行数与余量，由程序生成。

### ACR-28｜minor｜state-machine.yaml 效果 `kill_run`，protocol-run.yaml PP-1

`kill_run` 只"标记"。会话是独立进程组，但会话内部再 `setsid` 起的后台进程会逃出进程组。
证据：lessons 一 §4.1(3)（renew 后上一会话启动的 option poll 活过了 `/clear`；同一个 037e770 发了两个 review）。该证据来自窗口会话，不是 worker，所以只标 minor。
最小修复：终止后扫描环境变量 `TMPDIR` 指向运行目录的残留进程并终止。

### ACR-29｜minor｜modules.yaml sandbox/probe，decisions.yaml D-026

探针要覆盖"真的 commit"。agent-crew 给沙箱加 home 的 deny 后，verify 的只读探针没能练到 pre-commit hook 与 git 凭据，于是 worker 的 commit 与 push 全部被挡（#2779 revert PR 2732）；结论是"worker 的真 commit 加 push 是 worker 沙箱改动的唯一证明"。
最小修复：probe 包的策略格子加入"在 worktree 里执行一次真实 `git commit`（含 hook）"。

## 2. 被 agent-crew 证明不可行或代价过高的机制

### ACR-30｜major｜skills.yaml roles、state-machine.yaml S42/S43、decisions.yaml D-006

没有小改动的快车道，每个需求的固定成本很高。流程是 intake（Opus，交互）、planner（Opus）、每个 work 步骤一次 coder、一次 L0、一次 reviewer，最后 integrate 步骤再来一次完整 review。单步需求的 step review 与 integrate review 看的是几乎同一份 diff。
证据（本次实测的每次平均）：worker 1.18 美元/次（296 次 348 美元）、review 0.30、design 1.74、approve 1.94、verify 0.11；`--by pr` 的 223 个 PR 中 169 个零 fix 轮次、平均 0.75 美元。我的估算（假设 intake 与 planner 各约一个 design 会话的价格）：单步需求的下限约 5 美元以上，是 agent-crew 无 fix 轮次 PR（0.75 美元，不含窗口）的数倍；这是估算，不是测量。owner 在 2026-11-05 因"仪式"不满，结论是治理成本要与改动成正比。
最小修复：允许 `size: s` 的需求由 intake 直接产出单个 Step（跳过 planner，校验规则不变），step 级 review 与 integrate review 合并成一次；在 spec 里给出各阶段成本估算表。

### ACR-31｜major｜decisions.yaml D-005 的 H4、design/config.yaml CF-05，state-machine.yaml dims.counters

H4"单需求 token 累计超过预算 2 倍"里的"预算"在 spec 里没有定义，无法执行。更大的问题是计数器在 `rescue_retry` 时清零（`check_fail`、`review_rounds`、`merge_conflict`），而 `rescues` 上限 2：单个步骤的最坏会话数约为 3 个周期 × (1 + 3 + 2 + 2 次 coder，3 次 review) 外加 2 次 rescuer，约 35 次会话，还没算需求级的 replan（2）、`req_rescues`（2）、`fix_steps`（3）。
证据：fix 轮次使成本为 3.2 倍、8.3 倍、21 倍（lessons 一 §2.2）；最贵的 PR 3058 为 21.9 美元、9 个会话；#429（一个 Opus worker 跨 11 次 resume，上下文 22k 到 454k，约 75M cached tokens，对策是 200k 处压缩）。
最小修复：需求的预算 = 各步骤估算之和（取该角色近 20 次运行的 p75，由 RC 预留公式已有数据），H4 以此为基线；`rescue_retry` 只额外给 1 轮而不是全部清零；明确新 Run 是新会话还是 resume，以及 coder 会话的 token 压缩阈值。

### ACR-32｜major｜modules.yaml harness，decisions.yaml D-029，resources.yaml RES-MODEL

v1 的 harness 模块包含 claude、codex、gemini、scripted 四个适配器，且为 codex、gemini 单独定义了额度窗口。agent-crew 为第二厂商付出的是持续成本：仅 agy 就有至少 10 个 issue（#3095、#3174、#3194、#3217、#3219、#3220、#3236、#3238、#3240 等，试用一周后关闭，#3241）；Codex 在 2026-11-05 被 owner 放弃前，相关 issue 有 #558/#569、#617、#525、#605、#609、#628、#588；`harness.go` 到了 1830 行的 ratchet 上限。owner 放弃两者都是因为额度而不是质量，而且没有统计过"第二厂商评审抓到了第一厂商漏掉的 bug"的次数。
最小修复：v1 只做 claude 与 scripted，从 modules.yaml 删掉 codex 与 gemini 包以及 D-029 里对应的窗口；gemini 只作为 owner 手动调用的外部评审（第 4 节）；独立性来自另一个只读 session。重新引入第二厂商需要先有"它抓到了什么"的数据。

### ACR-33｜major｜constitution.yaml C-11，decisions.yaml 总则（amends），design/config.yaml CD-02

C-11 只限制 AGENTS.md（≤ 6000 字节），提示词 `prompts/<role>.md`、profile 规则、生成的清单没有上限。agent-crew 的实际轨迹：AGENTS.md 在 35 天里从 4,304 字节增长到峰值 99,950 字节（11-12，160 次提交），然后被"搬家"到 `docs/writes.md`（本次实测 113,311 字节、41 行），当前 AGENTS.md 24,996 字节、110 行；`rules/` 29 个文件合计约 157 KB，其中 `rules/dispatcher.md` 44,223 字节。decisions.yaml 同样有这个形状：只追加，一天 34 条，D-034 一条就 `amends` 四条旧决定，读者必须"合读"。
最小修复：`check:prompts-size` 对每个提示词文件与总量设绝对上限；decisions 的 `amends` 链深度 ≤ 1，被再次 amends 的条目必须以 `supersedes` 重写；提供 `foreman decisions --current` 渲染当前有效文本；长清单（禁令、写入表）一律走数据文件加生成，生成物标 "generated, do not edit"。

### ACR-34｜major｜decisions.yaml 全部 `enforced_by: program:<模块>`

34 条决定里 17 条的 `enforced_by` 是 `program:<模块>`（本次实测：program 17、test 7、check 6、none 3、sandbox 1）。模块名不是检查，它只说明"某个模块会实现"，没有任何测试断言该性质。宪法对 `none` 有"最多 3 条"的限制，对 `program:` 没有，而它们同样没有被程序挡住。
证据：lessons 二 §1.5："规则写在文本里"与"规则由程序挡住"，遵守率不是一个量级；记忆笔记 `rules-must-become-checks` 自己也只是一条记忆。
最小修复：spec-lint 规定 `program:` 条目必须同时列出至少一个 `test:` 或 `check:`，否则按 `none` 计数。

### ACR-35｜minor｜constitution.yaml C-07、C-08，decisions.yaml D-009

C-07（禁止三类测试）标 `none`，但其中"grep 源码或文案"一类可以机械检查：agent-crew 的 ratchet 用 go/ast 拒绝缺 `t.Parallel()` 的测试，被评价为"机械、便宜、真有效"（lessons 一 §7.4）。同类检查可以拒绝 `_test.go` 里读取 `prompts/`、`spec/`、`AGENTS.md` 并比较内容或哈希的测试。C-08、D-009、FG-9 标 `sandbox`，没有指向任何探针测试。
最小修复：C-07 增加 `check:no-text-pin-tests`（go/ast）；`sandbox` 条目写成 `sandbox + test:sandbox-probe-<名>`。

### ACR-36｜minor｜decisions.yaml D-007、access.yaml R0、resources.yaml RES-DISK

L0 固定 ≤ 120 秒、GOMAXPROCS=2、2GB，对 foreman 自己的小 package 是现实的，对接入的大项目则不是：agent-crew 的 `internal/crew` 本地整包 `go test` 要 132 秒，CI 里冷编译比本地多 90–110 秒。每任务一个可写缓存层意味着每个步骤都冷编译自己改动的 package；磁盘写入量没有被计量（owner 曾因 Go build cache 每天约 40 GB 写入把它搬出内置盘，#1046）。
最小修复：L0 预算按项目配置（CF-01）；RES-DISK 增加按天的写入量计量；缓存的只读种子使用 APFS clone。

### ACR-37｜minor｜skills.yaml，decisions.yaml D-019

起步就给 7 个角色分配约 12 次 skill 交付，与 lessons 二 §3.4 的"默认零、先量再留"不一致。`writing-for-agents` 不在 agent-crew 的 `skills/`（本次实测 10 个目录：code-review、codebase-design、diagnosing-bugs、domain-modeling、grilling、improve-codebase-architecture、pr、prototype、tdd、to-spec），skills.yaml 声称来源是同一份拷贝，需要核对上游 v1.3.2。"90 天指标无变化"没有阈值与基线；"20 次运行调用 0 次"依赖 harness 上报 Skill 调用（agent-crew 因此有 `skillRecordsFrom`）。
最小修复：起步只留 coder 的 tdd 与 reviewer 的 code-review；其余在有对照数据后再加；写明无变化的阈值；harness 适配器声明是否上报 Skill 调用。

### ACR-38｜minor｜decisions.yaml D-013

D-013 的测试只有 `test:web-no-github-import`。agent-crew 的 11-01 事故（CI 绿、七个视图完成、首页永远骨架屏，#1078）和 PR 1379/1381/1386（图表不可见，连续三次只查 DOM 就说 ok）说明需要空后端的冒烟与有限的加载状态。
最小修复：前端加载状态为 `idle|loading|ready|empty|error` 的有限状态机，超时进入 `error`；L1 增加冒烟：以空运行目录为后端，断言首页 5 秒内出现"没有项目"一类的可读文字；构建产物不提交（D-013 已有）。

## 3. `verified: false` 条目：能否用 agent-crew 经验确认或反驳

| # | 位置 | 判定 | 证据 |
|---|---|---|---|
| V1 | access.yaml R0 GOCACHE 共享只读层加每任务可写层（macOS） | 未知 | agent-crew 的 srt 用 `sandbox.write` 路径清单授予可写目录（#1046 六个声明要同时列新旧缓存路径，#2224 会话前要创建 `temp_env` 目录），所以"每任务一个可写路径"可行；"共享只读层"来自 Linux runner 镜像（"warm Go cache baked into runner image，read-only seed，per-job writable layer"），在 macOS 沙箱里没有任何实现可对照 |
| V2 | access.yaml R0 localhost 绑定 | 部分确认 | srt 设置有 `allowLocalBinding`（`internal/crew/sandbox.go:99`，由策略 `LocalBinding` 填充，:118），agy 适配器需要它并在 srt 内跑通（agy-harness.md Interface）；默认的 verify 策略没有开，所以才有 telegram_platform #15 的 bind 失败。结论：可行，但要按角色显式开启（ACR-39） |
| V3 | access.yaml R0 每任务开发数据库 | 未知 | agent-crew 没有对应实现；最近的旁证是 optdesk 的 `pg` 构建标签测试只在 CI 跑，且 CI 环境检查要求不得有 `OT_*`/`DSN` 变量 |
| V4 | resources.yaml RES-MODEL `granularity`（utilization 精度） | 部分确认 | agent-crew 把 utilization 当 `float` 解析（`harness.go` `levelWindow.Utilization` 经 `jsonFloat`），所以类型是浮点而不是整数；实际精度没有量过（读它要看 transcript，AGENTS.md 禁止），仍未知 |
| V5 | resources.yaml RES-GH-ACTIONS billing 端点 | 未知 | agent-crew 没有调用任何 billing 或 timing 端点（`internal/crew` 里找不到），也使用自托管 runner，所以没有 Actions 分钟的体验可参考 |
| V6 | forge.yaml OP-07 check-runs 与细粒度 PAT | 部分确认 | agent-crew 在三处读 `repos/<slug>/commits/<head>/check-runs?per_page=100`（`drive.go:1710`、`gate_derived.go:52`、`review_checks.go:12`），端点本身可用；但它走 gh 的登录，令牌类型我不能打开凭据去确认，所以"细粒度 PAT 能否读"仍未知 |
| V7 | forge.yaml `auth.later` GitHub App 的限额数值 | 未知 | agent-crew 没用过 App。它的限额耗尽记录（2026-10-30 REST、11-02 GraphQL，看板刷新器一小时 1177 次 REST 与 595 次 GraphQL）只证明限额问题真实存在，不提供 App 的数值 |
| V8 | protocol-run.yaml PP-5 `can_stop_midrun` | 部分确认 | agent-crew 对 Claude 只在 review 的 decide 路径使用 `--max-turns 2`（`review.go:1659`）；worker 靠 `compact_window`（200k）与超时，没有"超 token 即终止"的用法。所以 `max_turns` 加超时的兜底对 Claude 成立，`can_stop_midrun` 未知 |
| V9 | decisions.yaml D-002 的 rationale（"口径未核实"） | 确认 | 本次实测 14 天：dispatcher 40.1%、advisor 17.4%，合计 57.5%，worker 14.3%，口径同样是 crew 估算的 `cost_usd` |

### ACR-39｜minor｜access.yaml R0 localhost 条目

V2 的结论要写进 spec：local binding 默认关闭，按角色在 `roles.toml`/声明里显式开启，并由 `probe` 验证。
最小修复：R0 的条目改为"localhost 绑定：按角色声明，默认关"，并把 `verified: false` 改为注明 srt 的 `allowLocalBinding`。

### ACR-40｜minor｜protocol-run.yaml PP-5

V8 的结论要写进 spec：把 `max_turns` 加超时设为所有 harness 的默认兜底，`can_stop_midrun` 默认 false，实测通过才置 true。
最小修复：PP-5 去掉 `verified: false`，改成默认值加能力位的写法，并加测试 `test:budget-fallback-default`。

## 4. 用 Gemini 做对抗审查

**先说明：owner 于 2026-11-27 因额度消耗太快关闭了 Gemini 额外评审（agent-crew 的 `projects/agent-crew.toml` 回到 `profile = "claude-both"`，`claude-both-agy` 仍留在 `roles.toml`）。运行下面的命令之前，请 owner 确认额度；agy 有 5 小时与每周两个额度窗口，一次带全量 spec 的 `effort high` 评审并不便宜。** 我没有执行过下面的命令（不消耗额度），命令按 agent-crew 的代码与 issue 编写。

### 4.1 agent-crew 怎么调用

- 适配器：`internal/crew/harness_agy.go`（`agyHarness`，名字 `agy`，`Vendor()` 为 `google`，只能作评审者）与 `internal/crew/review_agy.go`（`agyCommand`、`agyReviewer`、`agyReply`、`agyReviewSettings`）。设计：`docs/design/agy-harness.md`。
- argv：`agy --model gemini-4-neon-high --effort high --output-format json --json-schema <文件> --print-timeout 40m --settings <deny 文件> -p <提示词>`，提示词是**最后一个参数**，标准输入为空（agy 不从 stdin 读提示词）。模型与 effort 来自 `roles.toml` 的 `[profiles.claude-both-agy]`。agy 版本本机是 1.5.17。
- 输出：stdout 是一个 JSON 对象，字段 `status`（必须是 `SUCCESS`）、`response`（散文）、`structured_output`（遵守 schema 的裁决）、`usage`（token，无费用）。
- 提示词长度上限：一个 argv 元素受操作系统限制，agent-crew 的 `agyArgLimit` 是 900 KiB，超过报错而不截断。
- 进程环境：`HOME=<runner home>/agy-home`（专用 HOME，登录放在这里，不用 owner 个人的 `~/.gemini`）；其余环境继承。crew 还把它放进 srt 沙箱（主机 `*.googleapis.com`、`accounts.google.com`、`antigravity.google`，`allowLocalBinding` 为真，唯一可写目录 `<agy-home>/.gemini/antigravity-cli`）。
- 四个已知的坑（都对应 issue）：
  1. **keychain**（#3236）：专用 HOME 下没有 `Library/Keychains`，macOS 对每次 agy 运行弹出 "Keychain Not Found" 对话框。修复是 owner 在终端里一次性执行 `LoginCommand` 打印的几行（`mkdir -p`、`HOME=… security create-keychain -p "" …/login.keychain-db`、`HOME=… security default-keychain -s …`，再 `HOME=… agy` 登录）。**本次实测：`agy-home/Library/Keychains` 目前不存在**，所以现在直接运行会弹对话框，对话框不点就可能一直等。
  2. **PATH**（#3238）：agy 安装在 `$HOME/.local/bin/agy`，crewd 的 PATH 里没有它，必须用绝对路径（crew 的修复是 `exec.LookPath("agy")` 失败时回退到该路径）。
  3. **工作目录**（#3240）：agy 必须在被评审内容的检出目录里启动，否则它读到的是启动目录；crew 的 `agyReviewerIn` 把检出目录作为命令的工作目录。
  4. **额度**（#3220）：额度错误要等待而不是失败。已知的文本片段：`RESOURCE_EXHAUSTED`、`MODEL_QUOTA_EXHAUSTED`、`exhausted your capacity`（后者可能带 `reset after 3h14m5s`，该时间格式来自 Gemini CLI 的文档，**未在本机见过真实输出**）。
- 只读性：agy 在不加任何限制时会自行写文件、执行 shell（探测记录：无 flag 的无头运行写了文件并运行了命令；`--mode plan` 不写但也不返回 `structured_output`）。所以 agent-crew 用 deny 设置文件关掉所有写和执行类工具。

### 4.2 可直接运行的命令

前置条件（不满足就不要运行，也不要让任何 agent 代替 owner 登录）：

1. owner 已在终端完成专用 HOME 的 keychain 与登录（见上，坑 1）。
2. 登录检查通过：下面的第 0 步输出 `SUCCESS`。若输出 `Authentication required` 并给出 URL，说明没有登录，owner 在自己的终端处理；不要读 `agy-home/.gemini` 下的任何令牌文件。
3. owner 已确认额度。

```bash
# 0. 变量。AGY 必须在覆盖 HOME 之前解析；RUNNER_HOME 是 agent-crew 的 runner home
#    （含 agy-home 的目录，具体路径见 agent-crew 的 docs/design/agy-harness.md，Interface 的 Auth 一节）。
FOREMAN=<foreman 根目录，git 检出>
RUNNER_HOME=<agent-crew runner home>
AGY="$HOME/.local/bin/agy"
AGY_HOME="$RUNNER_HOME/agy-home"
WORK="$(mktemp -d)"; mkdir -p "$WORK/tmp"

# 0'. 登录检查（agent-crew 的 LoginCheck，20 秒）。期望 JSON 里 status 为 SUCCESS。
env HOME="$AGY_HOME" TMPDIR="$WORK/tmp" "$AGY" -p ok --output-format json --print-timeout 20s </dev/null

# 1. 只读快照：被评审内容拷到仓库之外并去掉写权限，cwd 设在这里。
mkdir "$WORK/snap" && cp -R "$FOREMAN/spec" "$FOREMAN/docs" "$WORK/snap/" && chmod -R a-w "$WORK/snap"

# 2. deny 文件：与 agent-crew 的 agyReviewSettings 一字不差。
cat > "$WORK/agy-settings.json" <<'EOF'
{"permissions":{"deny":["command(*)","unsandboxed(*)","write_file(*)","run_workflow(*)","notebook_execution(*)","sed_file(*)","invoke_subagent(*)","send_command_input(*)","schedule(*)","multi_replace_file_content(*)","write_to_file(*)"],"allow":["read_file(*)"]}}
EOF

# 3. 裁决的 JSON schema（格式见 4.3）。
cat > "$WORK/verdict.schema.json" <<'EOF'
{"type":"object","required":["verdict","answers","findings"],
 "properties":{
  "verdict":{"enum":["approve","changes"]},
  "answers":{"type":"array","items":{"type":"object","required":["q","answer"],
    "properties":{"q":{"enum":[1,2,3,4,5,6]},"answer":{"type":"string"}}}},
  "findings":{"type":"array","items":{"type":"object",
    "required":["severity","where","problem","close_when"],
    "properties":{"severity":{"enum":["blocking","major","minor"]},
      "where":{"type":"string"},"problem":{"type":"string"},"close_when":{"type":"string"}}}}}}
EOF

# 4. 提示词：头部（4.3）加全部 spec 全文。写成一个文件，量大小。
{ cat "$WORK/prompt-header.txt"
  for f in constitution.yaml decisions.yaml open-questions.yaml design/*.yaml; do
    printf '\n=== spec/%s ===\n' "$f"; cat "$FOREMAN/spec/$f"
  done; } > "$WORK/prompt.txt"
test "$(wc -c < "$WORK/prompt.txt")" -lt 921600 || { echo "prompt over 900 KiB"; exit 1; }

# 5. 运行：cwd 在快照里，专用 HOME，标准输入为空，输出与错误分开。
cd "$WORK/snap" && env HOME="$AGY_HOME" TMPDIR="$WORK/tmp" "$AGY" \
  --model gemini-4-neon-high --effort high \
  --output-format json --json-schema "$WORK/verdict.schema.json" \
  --print-timeout 40m --settings "$WORK/agy-settings.json" \
  -p "$(cat "$WORK/prompt.txt")" </dev/null >"$WORK/out.json" 2>"$WORK/err.txt"; echo "exit=$?"

# 6. 读结果。status 不是 SUCCESS 就是失败，看 err.txt 与 response 的原文（不要猜）。
jq -r '.status' "$WORK/out.json"
jq -r '.response' "$WORK/out.json"          # 散文
jq '.structured_output' "$WORK/out.json"    # 裁决
```

说明：

- **工作目录与 PATH**：cwd 是快照目录（agent-crew 的教训 3：agy 要在被评审内容所在目录启动）；用绝对路径调用 agy，不依赖 PATH，其余环境变量保持继承（不要清空环境，keychain 与 `security` 依赖系统路径）。
- **HOME 与 keychain**：只对这一条命令把 `HOME` 设为专用目录（用 `env HOME=…`），不要 `export`，否则后续的 git、gh 都会读不到你自己的配置。不要把 `HOME` 指到个人目录，也不要软链个人 keychain（#3236 明确否决）。
- **只读**：三层。deny 文件关掉写和执行类工具；cwd 在 `chmod -R a-w` 的快照里，deny 漏掉某个工具时也写不进 foreman 仓库；agy 自己的日志与对话库只写 `AGY_HOME/.gemini/antigravity-cli`，这是它启动必需的，无法避免。运行后用 `git -C "$FOREMAN" status --short` 确认没有变化。
- **提示词上限**：spec 约 80 KB，加头部远低于 900 KiB。想让 Gemini 同时对照事故，不要把四份 lessons（约 140 KB）塞进提示词，让它用 `read_file` 读快照里的 `docs/lessons-*.md`（deny 文件允许读）。
- **超时**：`--print-timeout 40m` 与 agent-crew 的 `reviewLimit` 一致。外层没有 `timeout`（macOS 默认没有），所以不要省略这个参数。
- **失败要原文**：非零退出或 `status` 不是 `SUCCESS` 时，把 `err.txt` 与 `response` 原文给 owner，不要概述。遇到额度错误就停，等重置，不要重试。
- **数据外发**：整份 spec 会发到 Google。spec 里没有凭据、聊天 id、IP 或邮箱（本次对 spec/ 做过本机路径与用户名的 grep，无命中），但跑之前仍应让 privguard 或人再看一遍头部与新增的文件。agy 被包装使用在消费级套餐上"不受支持"（agy-harness.md 的 Left out，没有官方回复），拒绝是已知风险。
- **结果入库**：`out.json` 与 `err.txt` 留在 `$WORK`，评审意见按 D-028 记入 `docs/reviews/`，不要把 `$WORK` 的绝对路径写进文档。

### 4.3 对抗评审的提示词结构

agent-crew 的做法（`rules/dispatcher.md`，"A big change is attacked first"，#1743）：一个**没读过对话**的只读 agent，拿到全文与仓库，回答六个问题并给出裁决；作者在文档里写 `## Adversarial`，逐点写"采纳或不采纳及理由"，并指向完整回答的路径。foreman 沿用这个结构，提示词头部（`prompt-header.txt`）如下，后面接 spec 全文：

```
你是一个独立的对抗评审者，没有读过任何对话。请批评下面的设计，不要礼貌，不要总结设计，不要夸奖。
只依据本提示词里的文本和工作目录里的文件（只读）。引用时给出文件名与条目 id（例如 decisions.yaml D-005、
design/state-machine.yaml S31）。不确定的地方写"无法判断"，不要编造 agent-crew 或 GitHub 的事实。

## 目标
foreman：由程序驱动的 AI 开发流水线。owner 提需求，AI 会话（intake、planner、coder、reviewer、rescuer、
diagnoser、improver）只产出结构化结果文件，由常驻进程 foremand 校验后推进状态机、负责所有 push 与合并。
它要避免 agent-crew 的已知问题：状态在会话记忆里、规格是散文、规则靠文本而不是程序挡住、治理成本与改动不成比例。
成功的标准：owner 在手机上只回答"一个词"的问题；一个小改动的成本与 agent-crew 的 0.75 美元（无 fix 轮次的 PR）同量级；
没有凭据进入沙箱；main 只经 foremand 合并。

## 全文
（以下是 spec/ 全部文件的全文，每个文件以 "=== spec/<文件> ===" 开头。）

## 六个问题（每个问题都必须回答，answers 里用 q=1..6）
1. 与目标的差距：按上面的成功标准，设计里哪一条最可能达不到？指出条目 id。
2. 最大的未命名风险：设计里没有任何条目提到、但最可能让它失败的一件事。
3. 更简单的做法：能达到同一目标、少一半机制的方案是什么？删掉哪些条目？
4. 锁定：哪些决定一旦实现就很难改（数据格式、协议、第三方库、厂商），代价是什么？
5. 最可能为假的一条声明：设计里哪个断言（尤其是"X 已被验证/不需要"）你认为最可能是错的？说明你会怎样证伪它。
6. 最可能的滑坡：哪一步最可能先出问题或在实现时被悄悄绕过？给出具体的失败过程。

## 输出格式
只输出一个符合给定 JSON schema 的对象：
- verdict："approve" 或 "changes"。只要存在 blocking finding 就必须是 "changes"。
- answers：六项，q 为 1 到 6，answer 不超过 600 字。
- findings：每项 severity（blocking/major/minor）、where（文件加条目 id）、problem（≤ 300 字）、
  close_when（怎样算解决，≤ 200 字）。blocking 必须附一个具体的失败场景。
不要在 JSON 之外输出任何内容。
```

作者端的处理规则（沿用 D-025 与 D-028）：每个 finding 在 `docs/reviews/` 里写"采纳、不采纳、延后"与理由；对不采纳的 blocking 要有第二个评审者同意；不要把 Gemini 的事实性断言（agent-crew 或 GitHub 的行为）当作证据，凡引用的数字回到 agent-crew 仓库核对一次。

## 5. 本文未能核实的部分

- `crew usage --by pr` 的成本分布（169/43/10/1 个 PR 对应 0/1/2/3 轮）与 223 个 PR 的口径，我没有重新运行；引用自 lessons 一 §2.2。
- 第二厂商评审是否抓到过第一厂商漏掉的 bug：没有统计，我无法判断。
- ACR-30 里单步需求的 5 美元下限是我的估算。
- ACR-22 的 `workflows` 权限是 GitHub 的公开行为，不是 agent-crew 的证据，需要在测试仓库确认。
- foreman 自己的仓库是私有还是公开、套餐是什么：spec 没写，ACR-03 因此只能给条件性结论。
- agy 的真实额度错误输出和 `reset after` 格式：本机没见过。
- 没有运行 agy，所以 4.2 的命令是按代码与 issue 写成的，未经本机执行；`agy-home` 里是否已有有效登录我没有检查（那需要触碰令牌）。
