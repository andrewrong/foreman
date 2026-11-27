# 来自 agent-crew 的教训（二）：规则系统、看板、常驻进程、与 owner 的交互

本文是 `lessons-from-agent-crew.md` 的续篇，回答另一份问卷：规则怎么组织与送达、AGENTS.md 为什么长到 10 万字节、
skills 到底有没有用、看板（web）的架构与坑、crewd 常驻进程的恢复问题、以及 owner 实际怎么被打扰。
上一篇已经讲过的（散文 issue、ping-pong、sandbox、多 harness、测试与 CI、合并）不再重复，这里只在需要时引用章节号。

证据来源：agent-crew 仓库（`git log`、`AGENTS.md`、`rules/`、`skills/`、`roles.toml`、`web/`、`internal/crew/`、
`docs/decisions/`、`docs/design/`）、dispatcher 窗口的记忆笔记（91 条，不在仓库里）、dispatcher 的错误清单
（215 行，同样不在仓库里，下称"错误清单"）、`crew ask <project> list` 的计数（只看计数，不引用 owner 原文）。
数据截至 2026-11-27，HEAD 为 `dec2b66`，全史 1704 个 commit。核对不了的标 **未核实**。

先说总判断，后面逐条给证据：

1. 规则体系的真正问题不是"规则太少"，而是**同一句话存在于四个地方**（规则文本、重复它的 Go 代码、钉住文本的测试、
   设计文档），每改一句要同步四处，漏一处就是一轮 CI 红或一轮 review。
2. AGENTS.md 的长度不是被"规则"撑大的，而是被"清单"撑大的：每加一个机制就往里加一句"这个机制写哪里、谁不许碰"。
   清单应该是程序生成的数据，不是散文。
3. skills 的使用有测量机制（hook + 运行记录），但我没有拿到可引用的调用数；"好还是坏"在 agent-crew 里**没有对照实验**，
   不能说它改善了结果，也不能说它变差了。
4. 看板的架构（crewd 写快照、`crew serve` 只读快照）是对的，是 owner 两次发火才立起来的；最大的坑是
   "CI 绿但首次加载永远在骨架屏"，以及把构建产物提交进仓库。
5. crewd 的"原地 re-exec"解决了"要不要请 owner 重启"，但留下了三类后遗症：僵尸 drive、旧二进制读新配置而崩、
   账本数据库损坏后没有人自动修。
6. 与 owner 的交互里，真正被使用的是"一个词的回答"（merge、a/b/c 选项、retry 按钮）；
   账本、plan、字母按钮、各种 WAIT 行大部分是维护负担。

---

## 1. 规则系统：项目规则、角色规则、skills 的组织与送达

### 1.1 三层结构和送达方式

| 层 | 文件 | 谁读 | 怎么送达 |
|---|---|---|---|
| 项目规则 | `AGENTS.md`（HEAD 24,996 字节，110 行） | 所有在仓库里干活的会话 | 靠 harness 自己读仓库根的约定文件 |
| 角色规则 | `rules/<role>.md`，共 30 个文件，合计约 175 KB（含 `rules/task/`、`rules/triage/`） | 由 crew 启动的某个角色 | crew 在启动时把文件写进该次运行的目录，用 `--append-system-prompt-file` 之类的参数交给 harness（首个实现 `ed99094`，2026-10-09，#52） |
| skills | `skills/<name>/`，11 个，全部是 mattpocock/skills v1.3.2 的拷贝，`skills/skills.lock` 记 sha256 和"交付给哪些角色" | 角色按 `roles.toml` 的 `skills = [...]` 拿到 | 同上，作为插件目录交给 harness（`--plugin-dir`） |

`roles.toml` 374 行，声明每个角色的 harness、model、`rules = [...]`、`skills = [...]`、`deny = [...]`。
项目声明（`projects/*.toml`）可以追加规则、allow/deny 和 skills，但不能新增角色。

这套组织本身是合理的：规则是文件，交付是确定性的，角色和规则的对应关系写在一处。
真正的问题出在**时效**和**重复**。

### 1.2 时效：规则只在进程启动时送一次

错误清单 2026-11-02 一条（我认为是整个规则体系最该先修的一个事实）：

- dispatcher 窗口在 10-29 启动；`rules/dispatcher.md` 在 10-30（PR #883，`42627d2`）把并行数从 2 改成 4；
  窗口用 `/clear` 续命，**claude 进程没换**，所以窗口按 10-29 的文本又干了四天。
- 后果：agent-crew 的 4 个并行槽大半天只用了 2 个，owner 看到的是"空转"。
- 修法是后补的：patrol 能报告"窗口启动时的规则和仓库当前的不同"（"rules changed since start"），
  之后又为"crewd 自己重启窗口"立了设计（错误清单 11-03：我把 `crew window reopen --start dispatcher` 这条命令交给了 owner，
  而 owner 11-01 已经说过要 crew 自己重启窗口）。

教训：**规则的版本必须是会话可见、可比较的。** foreman 应该给每次送达的规则文本算一个哈希，写进会话记录，
由程序比较"会话手里的哈希"和"仓库当前的哈希"；不一致就是一个状态，由程序处理，而不是靠 dispatcher 记得。

### 1.3 重复：一句话住在四个地方，改一次要同步四次

最近的一个例子，2026-11-27（同一天，所以是最新鲜的证据）：

- owner 说"acceptance 的条目不指名测试"（`rules/acceptance.md` 的末行 `An item names no test`）。
- PR #3362（`50d41de`）改了 5 个文件：`rules/dispatcher.md`、`rules/verify.md`、`rules/task/design-author.md`、
  `internal/crew/verify.go`、`internal/crew/verify_test.go`，每个文件只改了 1 行。
- 然后 PR #3364（`73d85d1`，标题里直接写 "pin updated"）又改了 `rules/acceptance.md` 和 `internal/crew/roles_real_test.go`
  各 1 行——因为 `roles_real_test.go` 里存着每个规则文件的 git blob 哈希，规则一改它就红。
- 同一个意思，两个 PR，七处改动，其中两处是"为了让测试通过而改测试"。

这不是个例。我数到的量：

- 全史 1704 个 commit 里 163 个触及 `rules/`，77 个触及 `rules/dispatcher.md`。
- 有 88 个测试文件引用 `rules/` 路径；另有 99 行测试代码字面引用 `AGENTS.md`（只是路径出现，不一定读内容，**未逐个核实**）。
- 名字里带 `Pin` 的测试函数 9 个，但"钉文本"的测试大多不叫 pin：`roles_real_test.go` 里的 blob 表、
  `rules_dispatcher_queue_test.go`、`acceptance_test.go`（字面 prompt 钉住 reviewer 的句子，设计文档 growth-cap Entry 5 专门提到）、
  `declaration_own_test.go`、`declaration_pairfob_test.go`、`declaration_option_trader_test.go`、
  `TestTheRolesFileLeavesTheObservedProjectsAndTheRulesAsTheyWere` 等。
- 错误清单 215 行里有 19 行含 "pin"，18 行含 "rule says / the rule / rule:"。典型：
  - 11-03 PR #1287：改 `rules/acceptance.md`，跑了 `-run` 过滤的测试，漏了钉它 blob 的那个，CI 红。
  - 11-03 PR #1313、11-06 PR 1855、11-11 #2307：改 `projects/*.toml` 没跑它的 pin 测试，"规则里明明写了要先
    `git grep` 出 pin 测试"。**同类错误在清单里至少出现 8 次**，每一次规则文本都已经存在。
  - 11-08 #2028/#2039、11-11 #2309/#2310/#2321：acceptance 写了"no other file"，漏掉了每次改 `services.toml` 必改的
    两个 pin 测试，review 因此阻塞一轮。

还有一类更隐蔽：**设计文档为了避免这种事，开始在设计里记录"哪些测试钉了这个"**。
`docs/design/ci-durations.md` 的 Entry 1 写着三个 migration pin 测试的名字；`docs/design/keeper-restart.md` 要求"取下一个空闲的
migration 号"；`ddb8e6b`（#3200）让 migration 测试"从 `listMigrations` 取号，重号即失败"，
`f1087c9`（#3198）/`94b02fb`（#3226）又加了 "ci-precheck"：drive 在等 CI 之前先跑 gofmt、ratchet 和 pins。
也就是说，**为了管住"钉文本的测试"，又写了一层"预跑钉文本的测试"的程序**。这是治理成本滚雪球的典型。

### 1.4 为什么要钉文本，为什么钉错了

钉文本的初衷是对的：规则文本一变，行为可能就变，所以让测试在文本变化时叫醒人。
但 agent-crew 的做法有三处问题：

1. **钉的是整段文字，不是意图。** blob 哈希相等 = 一个标点都不能动。想让 owner 的一句话生效，必须同时改文本和哈希，
   哈希更新变成机械动作，没人再读它到底钉了什么。`no-useless-tests.md` 和提交 `e92b2c4`、`93afe8c`、`77484f4`（
   "Cut the tests that only pin README wording"）说明团队已经发现这类测试没有价值并开始删除，但删得很慢（#2999–#3005 三个 PR 只清了几个包）。
2. **代码里重复了规则的句子。** `verify.go` 里有一份 verify prompt，与 `rules/verify.md` 的同一句话各存一份；
   `acceptance_test.go` 再存一份字面量。改规则要改三处。
3. **"pin updated"成了 commit message 的惯用语。** 当一个动作被命名成惯用语，说明它已经是流程里的固定成本。

`docs/design/pin-what-followup.md`（#3290 / `318b78f`，2026-11-26 到 27）是对此的最新回应：规则改成"设计和 issue 钉
what，不钉 how"。我的判断：方向对，但它仍然是**再加一条规则**去约束"写规则的人"，没有减少钉文本的测试数量。

### 1.5 哪些规则被遵守，哪些被忽略

可观测的证据只有错误清单（dispatcher 自己记的"可避免错误"，约 215 行，2026-11-02 到 11-27）。我按"规则文本是否已存在"分：

**规则存在但反复违反（说明文本不是有效的约束）：**

- "改 `projects/*.toml` / `rules/` 之前 `git grep` 钉它的测试"：至少 8 次。
- "gh 写操作要单独一个 Bash 调用，不要串联"（记忆 `bare-gh-writes` 两条，重复条目）：反复被分类器拒绝。
- "dispatcher 写的 PR 不要 register 草稿"（记忆 `registered-draft-no-hand-issue`）：错误清单里至少 3 次——
  11-08、11-11、11-14 都是 dispatcher 先 register 又手工开了 issue，crewd 又自动 issue 一次，出现重复 PR
  （#2408/#2409，#2712/#2714，#2715/#2717），其中 #2714 还跑了一个 drive 然后 STOP。
- "合并前先看运行中的 drive"（记忆 `roles-key-breaks-running-drives`）：10-20 写进记忆，11-03 又在 PR #1391 上违反，
  #1778 #1780 #1787 因此停掉。
- "不要 `git checkout origin/main` 到共享克隆"：11-14 又发生一次，crewd 合并后的快进被卡住。
- "worktree 推送用 `--no-track` 和 `HEAD:refs/heads/X`"：记忆 `worktree-push-to-main`，11-27 `799ecad` 直接落到了 main。

**规则存在且遵守得好的，基本都是"有程序挡着"的：**

- gate 拒绝合并：没有 review 记录就不能合并，从未被绕过（上一篇 8.3 讲的"直接推 main 四次"是绕过 gate，
  而不是 gate 失败）。
- `crew issue check`：把"一个 issue 的 N 个常见毛病"变成命令，错误清单里它抓到的比人抓到的多（#1006 之后的条目）。
- sandbox 的 deny 列表：worker 不能 `gh pr merge`，这条从没被违反，因为不是规则而是权限。

两类对比是这份文档最想传达的一句话：**"规则写在文本里"和"规则由程序挡住"，遵守率完全不是一个量级。**
记忆笔记里已有一条把它总结出来了：`rules-must-become-checks`（"只在 ADR/文档/记忆里的决定会被漏掉；用代码或测试强制"），
但这条自己也只是一条记忆。

### 1.6 记忆笔记本身是第四套规则

dispatcher 的记忆目录里有 91 条笔记，MEMORY.md 里每条一行索引。它们的角色是"owner 说过的话 + 我犯过的错"。
问题：

- 会过期且没人负责：`fewer-subagents` 一条被标 "superseded"，仍在索引里；`bare-gh-writes` 重复两条；
  错误清单 11-02 记："用 `codex exec` 做查询，依据的是一条已作废的记忆，owner 问'codex是什么鬼'"。
- 与仓库规则冲突时没有仲裁：`rules/dispatcher.md` 说一个数，记忆说另一个，会话读哪个取决于顺序。
- 它们不在仓库里，所以新窗口、云端 sandbox 里的 author、公开仓库的读者都看不到。
  AGENTS.md 的原文就承认了这一点："What a window was told by the owner is not in the repository."

foreman 应该有一个唯一的"owner 决定"表（`docs/decisions/` 的结构化版本），记忆笔记里只能放"如何使用"，不能放决定本身。
agent-crew 的 `docs/decisions/` 已经走到这一步（14 个文件，如 `2026-11-01-owner-mainly-on-ios.md`），
但那些文件一律只有一句 owner 原话和一个 `standing: S-07` 编号——**没有适用范围、没有检查方式、没有过期条件**。
一句话决定加编号，比散文强，但离"程序能检查"还差一步。

---

## 2. AGENTS.md 的增长

### 2.1 数字

`git log -- AGENTS.md` 共 160 次提交（2026-10-08 到 2026-11-26）。按每 14 次提交抽样的字节数：

| 日期 | 提交 | 字节 | 行 |
|---|---|---|---|
| 10-08 | `27a102e` | 4,304 | 37 |
| 10-19 | `7c68a7a` | 11,947 | 60 |
| 10-27 | `a0a2522` | 24,947 | 60 |
| 10-30 | `95da8c6` | 37,208 | 61 |
| 11-01 | `12df76f` | 50,402 | 65 |
| 11-03 | `2327d23` | 68,216 | 65 |
| 11-07 | `e462c07` | 80,863 | 65 |
| 11-10 | `e68446b` | 91,387 | 65 |
| 11-12 | `d6c99ca` | 99,950 | 65 |
| 11-26 | `c60a86c` | 24,996 | 110 |

说明：任务说明里说"约 34.6k"，我量到的 HEAD 是 24,996 字节、110 行；10 万字节是 11-12 的峰值。
也可能 34.6k 是别的口径（字符数或含别的文件），**未核实**。

两个读数：

1. **字节增长了 23 倍，行数只增长了 1.8 倍。** 峰值时 65 行、10 万字节，平均每行 1,500 字节；
   最长一行 1,824 字节。换句话说，每个条目是一整段话塞进一个 markdown 列表项。
   任何会按"行"计量、按"条"审阅的工具，在这个文件面前都失效。人也一样：
   没有人能"读一遍"一个 10 万字节、65 个条目的文件。
2. **11-14 的"瘦身"不是瘦身，是搬家。** `d7a5447`（2026-11-14，#2648）"move the list of writes out of AGENTS.md
   into docs/writes.md"。结果：AGENTS.md 缩到 25k，但 `docs/writes.md` 现在 113,311 字节、**只有 41 行**
   （平均每行近 2.8 KB）。总字数没少，只是不再在每个会话的必读文件里。这是有意义的（对上下文成本）。
   但"每次新增一次写入 → 更新 docs/writes.md 的一行"这条纪律仍在 AGENTS.md 里写着，
   并且有测试守着（`internal/docmeta/` 下有 `repo_test.go`、`sections_test.go` 等）。

### 2.2 增长的种类

160 个提交标题里（粗略关键词，**不是严格分类**）：

- 23 个含 restart / reap / deploy / keeper；13 个含 write(s)；8 个含 telegram；
  典型标题："AGENTS.md: record the reap guards, the Telegram letter buttons and the Stop-hook owner-question check"、
  "AGENTS.md: the Telegram merge word posts each judged pull request that waits only for authority"、
  "AGENTS.md: name the OTEL receiver of crewd in the list of writes"、
  "AGENTS.md: the automatic clean removes stale runs and stale temporary directories"。
- 这些标题的共同形状：**"为机制 X 在 AGENTS.md 加一句"**。每个机制新增一句"这个机制写在哪、谁不许碰"。
  一旦机制数量上去，文件里就是 N 个机制的 N 个句子，每句里又塞着它的前提和例外。

按内容分，我把增长归为四类：

| 类别 | 例子 | 本质 |
|---|---|---|
| A. 禁令清单（"no agent runs X"） | `crew cap --apply`、`crew policy lock`、`crew mode set`、`crew window reload --start`、`crew approve --start`、`restart_asks` ……现在 HEAD 里有 30 多条 | 本质是**权限表**，应该是数据 |
| B. 写入清单（谁写哪里） | 搬到 `docs/writes.md` 之前占了 AGENTS.md 的大半 | 本质是**登记表**，应该由程序从代码里提取 |
| C. 流程描述（"How a change lands" 的 7 步） | step 4、5、7 里一个又一个括号和"owner decision, 2026-10-30" | 本质是**状态机**，应该是 `crew gate` 的代码 |
| D. 一次性事故的补丁句 | "the fast-forward of the clone after a merge: No agent starts it by any means other than the merge" | 本质是**事故记录**，应该在 postmortem 里，不在常驻规则里 |

### 2.3 事后看，哪些应该是程序检查，哪些应该删

**应该是程序检查（在 AGENTS.md 里本来就不该存在）：**

- 全部禁令（A 类）。当前这些禁令大多靠"终端检查"（stdin 是不是终端）强制，AGENTS.md 里却自己承认
  "The terminal check cannot enforce this, so it is a rule"。也就是说，它们实际上**没有强制，只有一句话**。
  更好的做法：把"哪些命令是 owner 独有"做成数据表（命令前缀 → 允许的主体），
  由 harness 的 deny 规则和命令自己的 `--apply` 检查两处共同读它；文本说明由这张表**生成**。
- 写入清单（B 类）。让 `Machine` 接口的每个写方法带一个注册名，测试检查"代码里出现的注册名集合 = docs 里登记的集合"。
  agent-crew 里已经有这个方向的测试，但清单本身是人写的 113 KB 散文。
- "A transaction wraps one read-modify-write and never a session"之类的编码规则：用 lint，不用散文。
- "No shell scripts in this repository"：一个 CI 步骤（`git ls-files | grep '\.sh$'` 加一个白名单）。
- "No secrets or chat ids…": 扫描器（gitleaks 之类，成熟库）。
- "`gofmt -l .` 无输出、`go vet` 通过"：本来就是 CI。

**应该删（现在已经没有读者）：**

- D 类的补丁句。事故留在 commit message 和 `docs/` 里就够了。
- 每个 "(owner decision, 2026-10-30, …)" 的日期括号：决定的出处应在 decisions 表里。
- 对"另一个条目已经说过了"的再次声明，如 "the existing statement that no agent starts `crew host up` is unchanged"。

**保留**（正文短，读者每次都需要）：

- 对 owner 说中文，代码用英文。
- `projects/*.toml` 是 owner 的决定，不要为让检查通过而改它。
- 观测代码只读。
- 新依赖要在 PR 里说明许可证。

### 2.4 如果重做：具体的长度上限和机制

建议（对 foreman）：

1. **硬上限**：`AGENTS.md` ≤ 6,000 字节、≤ 80 行、每行 ≤ 200 字节。理由：agent-crew 的数据说明，
   一个会话在上下文里能真正"遵守"的约束数量大约是十几条（见 1.5 的违反记录：规则写得越多，被违反的越是后加的细节条）。
2. **机制**：一个 CI 检查（Go 测试即可）在 PR 上算字节数和最长行，超限即红。
   不要用"ratchet"（只允许不增长）式的渐进上限：agent-crew 的 `docs/design/ratchet.json` 对 `.go` 文件的上限
   就是例子，文件越长上限越被改成"当前长度 + 1"，上限变成了对现状的记录，而不是约束
   （`window_start.go` 的 ceiling 是 2327 行，`drive.go` 2101 行，见 ratchet.json）。AGENTS.md 要的是**绝对数**，不是相对数。
3. **每条规则必须带一个"谁来强制"字段**：`enforced-by: <test name | deny rule | hook | none>`。
   `none` 的数量本身有上限（比如 5）。这样"只有一句话"的规则很快暴露。
4. **新增规则必须同时删掉一条**（或者转成程序检查）。agent-crew 里这条规则的缺位正是 160 次提交都在加的原因。
5. **长清单走数据**：禁令表、写入表放在 `.toml`/`.json`，文本说明由模板生成，生成物标 "generated, do not edit"。
   agent-crew 里 `rules/generated.toml` 已经有这个形状，但只是一个小例子。
6. **规则文本和代码里的同一句话，只留一份**：运行时由代码读规则文件，而不是在代码里再写一遍；
   不钉文本，钉**行为**（如"verify 拒绝名字出现在 acceptance 项里的测试"），用表驱动的行为测试。

---

## 3. Skills

### 3.1 现状

- 11 个 skill 目录，全部来自 mattpocock/skills v1.3.2，`skills/skills.lock` 记录名称、来源、tag、内容哈希、交付角色：
  `tdd`（worker、reviewer）、`codebase-design`（worker、reviewer、window）、`pr`（worker）、`diagnosing-bugs`（diagnose）、
  `code-review`（reviewer）、`domain-modeling`、`grilling`、`improve-codebase-architecture`、`prototype`、`to-spec`（都给 window）。
- `roles.toml` 对 worker 的说明：owner 在 2026-10-11 要求 worker 拿到 skills，选哪些由 dispatcher 决定；
  当时选了 `tdd`、`codebase-design`、`pr`。
- 记忆 `matt-skills-are-the-owners-tools`：owner 2026-10-31 说"我要用 matt 的工具"，并责怪 dispatcher 把这件事"忘了"。
  这说明 skills 对 owner 是**信念性的投入**，不是从数据里选出来的。

### 3.2 怎么测量"使用"

规则里提到的"0 calls in 14 days"来自这条链，我读了实现：

1. **运行记录**（worker/reviewer/verify 等 crew 启动的会话）：`6eefa64`（#996，2026-10-31）让运行记录带
   `skills_invoked`；`serve_usage.go` 里 `skillRecordsFrom = 2026-11-01`，注释说明"只有这天之后的记录才能说一个 skill 没被调用；
   不上报 Skill 调用的 harness 的 skill 无法测量"。
2. **窗口**（dispatcher、advisor 等长驻窗口）：`9487124`（#1366，2026-11-03）装一个 PreToolUse hook，匹配 `Skill` 工具，
   每次调用往 `crew.db` 的 `skill_calls` 表插一行，保留 14 天（`window_skill_hook.go` 的 `skillCallsKeep`）。
3. **展示**：看板 Skills 卡（#1337、#1401）按（项目、角色、skill）列出"交付给了几次运行 / 调用了几次"；
   `fb6d885`（#1601）把"被声明但 0 次调用"的 skill 单独列出。
4. `crew skills probe`（#1052）让你看一次真实会话到底拿到了哪些 skills。这条来自记忆
   `verify-config-takes-effect`：2026-10-27 发现 Codex worker 悄悄用着默认的 `low` effort 且**一个 skill 都没拿到**，测试全绿。

### 3.3 证据：改善还是变差

**我没有拿到可引用的调用数。** 看板的 Skills 卡读的是运行时数据库，我只读了代码，没有（也不该）读 `crew.db` 的原始行；
`crew usage` 的只读输出给出用量与成本，不含 skill 计数。所以 14 天内每个 skill 的调用数**未核实**。

能从仓库看到的间接事实：

- **规则强迫调用，说明模型默认不调用。** 一连串提交都是"把规则改得更强硬"：`6103a5e`（#228）"worker 的规则说用 tdd skill"、
  `5210812`（#272）"rules/worker.md：点出 Skill 工具和 `crew:tdd`"、`c788cd5`（#239）"诊断规则说要加载它被交付的 skill"、
  `877d28d`（#1580，11-05）"在 author brief 里**在第一次编辑前**点出要调用的 skills"。
  四个提交都在解决同一个问题：skill 被交付了，会话不去调用。
- **0 次调用的 skill 仍然被交付。** #1601 之所以要加"0 calls"一栏，是因为 owner 想知道交付的东西有没有被用。
  记录之后没有发现把 skill 从 `skills.lock` 删除的提交（我查了 `git log -- skills/` 的标题，**未逐条核实**）。
  也就是说，测量机制存在，**但没有"测量 → 删除"的闭环**。
- **没有对照实验。** 没有哪次提交比较过"有 tdd / 无 tdd 的 worker 合并率、review 轮数"。
  `crew usage --by harness` 能给出每个 harness 的合并率，但与 skill 无关。
- **成本**：每个被交付的 skill 的 `SKILL.md` 进上下文（大小**未核实**）；`codebase-design`
  同时交付给 worker、reviewer、window 三类。对 200K 压缩阈值的 worker（`roles.toml` 的 `compact_window`）是有分量的固定开销。
- **风险**：skill 是从上游拷贝的，`skills.lock` 用 sha256 固定了内容，这点做得好（能证明"没改过"）。
  但 skill 内部的措辞是英文通用写法，与 agent-crew 自己的规则有时冲突；例如 `pr` skill 的模板与 `rules/worker.md` 的
  PR 模板要放在一起（roles.toml 注释里专门写了"whose template rules/worker.md puts after the spec"）。
  冲突的仲裁靠"规则里写谁先"，没有程序检查。

### 3.4 给 foreman 的建议

1. **默认零 skill**。每个 skill 必须附"它改变哪个可测指标"的一行，90 天内没有数据支持就删。
2. **测量必须闭环**：写一条 `skills.lock` 的规则——"交付 N 个运行、调用 0 次"时，下一次 `check` 失败，
   让人显式地保留（带理由）或删除。agent-crew 已经有测量，只差这一步。
3. **优先把 skill 的作用做成角色规则的短句**（如 tdd = "先写失败的测试"），而不是整个目录；
   只有当 skill 带有脚本或模板（如 `pr` 的模板）才值得做成 skill。
4. **做一次 A/B**：同一批 issue 一半带 skill 一半不带，比较 review 轮数和合并率。agent-crew 有足够多的运行可以做，但没做。

---

## 4. 看板（web/）

### 4.1 架构

- 前端：`web/` 下 React 19 + TypeScript + Vite 8 + Tailwind 4 + shadcn/ui（radix 底座、Nova 风格）+ zustand + recharts +
  `@tanstack/react-table`，测试用 vitest + testing-library + jsdom。依赖版本全部固定。栈的选择写在
  `rules/frontend-stack.md`（owner 2026-10-23 的决定，核心句："严禁在已有依赖可以正确表达的情况下自行设计实现组件和行为"）。
- 规模：`web/src` 约 9,600 行 TS/TSX（含测试），17 个视图组件（now、queue、pr、quota、abnormal、flow、windows、services、
  settings、tasks、plan、usage 等），Go 一侧 `serve*.go`、`board_snapshot*.go` 约 5,300 行，
  `serve*_test.go`/`board*_test.go` 另有 20 个文件。
- 发布方式：`vite build` 的产物**提交进仓库**（`internal/crew/board/dist/`，包含带哈希的 JS 和 CSS），
  由 Go 的 `embed` 嵌进二进制，`crew serve` 直接服务。
- 数据来源：**crewd 的快照**。`board_snapshot.go` 定义 `board-snapshot/1`：队列循环定期写
  `<runner home>/board/<project>.json`，字段是显式白名单，注释写明"旧 reader 会拒绝其他 revision，很多进程读这个快照，
  所以加字段只能是可选的、`omitempty`；改名或删字段必须升 revision，经过 issue"。`crew serve` 只读这个文件和
  几个只读的数据库端点（`/api/board/db`、`/api/overview`、`/api/usage`）。
- 动作：`serve_actions.go`（707 行）处理 merge、stop、deploy、rollback、restart 等"按钮"，
  按钮用 owner 浏览器独有的"动作令牌"校验；按下后"立刻回应，看板轮询结果"（`3011645`，#2516）。
  AGENTS.md 里把"持有该令牌的按下"定义为 owner 的亲手操作，与终端上的 `--apply` 同等。

### 4.2 "看板不读 GitHub"：被 owner 发火后才立起来

- 决定文件 `docs/decisions/2026-11-02-board-reads-no-github.md`：一行 owner 原话"看板只显示 crewd 的快照数据，从不读取 GitHub"。
- 记忆 `board-reads-no-github`：2026-11-02 owner **生气地重复了第二遍**；原因是看板自己的刷新器一小时内用掉
  **1177 次 REST 和 595 次 GraphQL**，吃的是全机共享的限额，而这句话已经出现在"某计划的第三批"里，dispatcher 没把它和当前问题连起来。
- 后续的对应物：`docs/decisions/2026-11-02-shared-resources-bounded.md`、`docs/decisions/2026-11-02-github-not-the-bottleneck.md`，
  以及 `serve_github.go`（215 行）——它现在**只读**`resource_history` 里已记录的用量，不再打 GitHub。

教训：**共享的外部配额必须有程序里的"预算持有者"，而不是靠"每个人自觉别用"。**
看板是最容易无意用掉配额的地方，因为浏览器每次刷新都会触发读。正确的结构就是现在这个：
一个进程（crewd）持有与 GitHub 的所有对话，其余全部读它写下的快照。foreman 应该一开始就这样做，
并且让 `crew serve` 的构造函数里**拿不到** GitHub 客户端（编译期隔离），而不是文档里写一句"不要读"。

### 4.3 11-01 的"骨架屏"事故

时间线（记忆 `open-the-ui-before-saying-done`，加提交记录）：

- 10-30 `61afb0b` 脚手架；10-31 起每天 3–6 个 PR 一个视图，11-01 一天做完七个视图（现在、队列、PR、配额、异常、流动、窗口）；
  `af8a653` 加按钮；`d32b5f1`（#1071）"crew serve 在 `/` 提供新看板，旧页面删除"。
- 同一天，dispatcher 报告"七个视图做完了"，依据是 CI 绿，加上手工 curl 了几个路由都 200。
- owner 打开后：**看板永远停在加载骨架屏**。原因：默认选中的项目没有快照，刷新时遇到 404 就抛出异常，
  骨架屏没有任何出路。
- 修复 `2f6a585`（#1078）：看板选"有快照的项目"，失败时显示出来。它同时更新了
  `internal/crew/board/dist/` 里被哈希重命名的 JS（49 行的压缩文件被整个替换）。

为什么 CI 没抓到：

1. 测试全部用 fixture。fixture 里**一定有快照**，"没有快照"这条路径不存在于任何测试数据。
2. 路由测试只证明"端点能回 200"，不证明"页面首次加载能渲染出文字"。
3. 流程里没有一步是"在浏览器里看一眼"。后来补上的规则（记忆）是"UI 改动在说完成前要在 Chrome 里打开并读渲染出的文字和控制台"，
   仍然是**一句话，不是检查**。`docs/design/ui-shots.md`（2026-11-18）是真正的程序化方向：
   用项目自己的 CI（Playwright）在 fixture 上渲染页面、上传 PNG，`crew review --post` 把 PNG 交给 reviewer 看。
   **但它的 fixture 仍然是 fixture**，无法捕获"空状态"这类真实数据缺失问题，除非 fixture 里**专门造一个空项目**。

给 foreman 的具体建议：

- 每个视图的测试必须包含三种数据：空（零项目、零快照）、正常、错误（端点 404/500），
  **并断言页面渲染出了某个"可读的状态文字"**，而不是只断言"没抛异常"。
- 前端 store 的"加载"状态必须是有限状态机（`idle | loading | ready | empty | error`），
  不允许有"永远 loading"的状态；加一个超时进入 `error`。
- 冒烟：CI 里用无头浏览器打开首页，**以空的运行目录为后端**，断言首页 5 秒内出现"没有项目"之类的文字。

### 4.4 提交构建产物的代价

`internal/crew/board/dist/` 提交在仓库里，每个改 UI 的 PR 都会改它。后果（来自提交记录和错误清单，**我没有逐个数**）：

- 同时有两个 UI PR 时，`dist/index.html` 和带哈希的 asset 文件名必然冲突，只能靠 `base_moved` 的"更新分支"重生成。
- reviewer 要在 diff 里滤掉 49 行一行的压缩 JS。
- 最后一类："源码对了、dist 没重建"的错误，需要一个测试去比较"源码构建结果 = 提交的 dist"（`a1fbaea` 之类的测试在做这类事）。

如果 foreman 要内嵌看板：**CI 在构建时生成 dist，不提交**；本地开发用 `go generate`；发布用 release 构建。
agent-crew 选择提交是因为"让 `go build ./...` 不依赖 node"，这是个合理诉求，但可以用 release artifact 解决，不必污染每个 PR。

### 4.5 owner 真正用哪些页面

**我没有访问日志，这一节的证据最薄。** 能说的：

- 看板在 11-01 一天做出 7 个视图，之后继续加到 17 个组件；但**没有任何记录说明哪个视图被打开过多少次**。
  `crew serve` 没有按视图计数的访问统计（我读了 `serve.go`，只有 `serve.log`，**未核实其中是否有路径**）。
- owner 的行为间接证据：
  - 反复出现的需求都落在"一个按钮":`af8a653`（merge、stop、deploy、rollback 按钮）、`5322ff1`（#2508，Restart 按钮）、
    `e1480cf`（#3310，2026-11-26，owner 收件箱行带 retry 和 confirm 按钮）。
  - owner 说过"我找不到 retry 的具体位置"。这个提交 `e1480cf` 恰恰是对此的回应。说明前面的 retry 在**命令行**，
    看板上没有入口，而 owner 在手机上。
  - 11-01 的 `486db62`（#1179）"手机宽度下标签行在自身内部滚动"：说明标签（视图）已经多到手机放不下。
  - Quota 页（配额）被反复打磨：`c46cd44`、`a218464`、`quota-view.tsx` 466 行、测试 331 行，是最大的单个视图，
    说明 owner 在意额度；但"在意"≠"常看"。
  - 看板的 Skills 卡、Usage 页、流动页：被做出来是 dispatcher 的设计，而不是 owner 要求的；我没有 owner 使用的证据。
- 没有证据的页面不要做。foreman 应该先做 **2 个页面（现在 / 要我动手的）**，其余等到 owner 在手机上主动问
  "这个在哪看"再加。

### 4.6 owner 主要用 iOS 的含义

决定 `docs/decisions/2026-11-01-owner-mainly-on-ios.md`（S-07）：一行"负责人主要使用 iOS，手机上的功能先在 iOS Safari 里试"。
记忆 `owner-mainly-ios`：真实场景探测要在 iOS 模拟器的 Safari 里跑，慢的问题先看 iPhone 的路径。

这条决定的代价（我从提交记录推断，**未核实 owner 是否同意这个解读**）：

- 每个视图在 390px 宽度下必须可用：这是 `486db62` 的原因；表格（`@tanstack/react-table`）在手机上几乎总是错的选择，
  但看板仍用了。
- 触屏没有 hover：`chart.tsx` 的 tooltip 靠 hover。
- iOS Safari 对 SSE/长轮询、后台标签的处理与桌面不同：`3011645` 采用"按下立刻回应 + 轮询结果"，而不是 SSE，
  很可能是这个原因（**未核实**）。
- 通过 tailnet 访问（记忆 `tailnet-serve-tool`），所以 owner 的真实路径是：手机 → tailnet → 小型主机 → `crew serve`。
  这条路径上任何一环出问题，owner 看到的都是"看板打不开"，而不是具体错误。

建议：

1. foreman 的看板**以手机为第一目标**设计：单列、大按钮、没有表格、没有 hover。桌面只是"放大"。
2. 把"owner 主要在手机上"写进验收：每个 UI PR 带一张 390px 的截图（`ui-shots.md` 的机制可复用）。
3. **更好：别让 owner 去看板。** 用推送（见第 6 节）把"需要你的一句话"直接送到手机通知里；看板只做兜底。

---

## 5. crewd 常驻进程

### 5.1 它是什么

`crew daemon`（`internal/crew/daemon.go`，451 行，加 `approve_daemon.go` 489 行、`design_review_daemon.go` 445 行）：
一个长驻进程，负责队列循环、快照、Telegram 轮询、自动部署、批准会话、设计 review 会话、patrol、清理。
它由一个 "keeper"（`crew host up`）拉起，AGENTS.md 规定任何 agent 都不能启动 keeper。

### 5.2 重启恢复：三个具体事故

**(a) 重启后什么都没起来（2026-11-11）。** 设计文档 `docs/design/restart-self-heal.md`（`ae596b6`）开头写道：
机器在约 07:40Z 重启，**什么都没自动起来**，因为 keeper 是 owner 手动启动的；同时 `crew.db`
报 "database disk image is malformed (11)"，由 dispatcher 用 `sqlite3 .recover` 手工修好。
对应的修复是一串提交（2026-11-11 到 11-12）：
`e9e72fa` 每个打开 `crew.db` 的进程取一个共享 flock；`5cebdfc` 持久提交加每小时 checkpoint；
`17f4d5d` crewd 启动时检查并修复损坏的数据库（`BootID`、`RecoverCrewDB`）；`c5e5876` 不再"打开再关闭一个已存在的 crew.db"。
**根因**：数据库放在 WAL 模式，多个进程（hook 的 INSERT、crewd、CLI）同时打开，没有锁约定，且 checkpoint 无人负责；
断电后 WAL 未合并。上一个设计把"账本"放进 SQLite 是合理的（风险在运维而非选型），
但"每个 hook 一次 INSERT"意味着**任何会话的 hook 都能让数据库处在被打开状态**。

**(b) re-exec 之后的僵尸 drive。** 记忆 `crewd-reexecs-itself`：`daemonRun` 在 `bin/crew` 变化时
用 `syscall.Exec` 原地替换自己，**保留 pid**。好处：owner 不用手动重启（我曾几次错误地请 owner 重启，
因为看到旧 pid 就以为没重启）。坏处：re-exec 丢失了对存活 drive 子进程的 `Wait`，drive 结束后成为 crewd 的僵尸。
修复：`5dc723b`（#1307，11-02）"crewd 在 re-exec 后回收它领养的子进程"；错误清单 11-11 又记一次：
PR 2431（#2429 "crewd re-wait"）合并时，PR 描述写着"真实机器上的僵尸/re-exec 探测没有再跑"，
而 gate 不认识这个声明，所以仍然合并了。**live 证明要等到下一个跨越 re-exec 的 drive**，也就是说，这个修复上线时是未验证的。
同日 11-03 另一条："#1324 needs_attention 'start not confirmed' 是 crewd 在启动期间 10:14 的 re-exec"——
re-exec 本身能让一个正在启动的东西"没确认"。

**(c) 旧二进制读新配置而崩。** 记忆 `roles-key-breaks-running-drives`：
一个运行中的 `crew drive` 在内存里是旧二进制，但**每一步重读 `roles.toml` 和项目声明**。
2026-10-20 合并 PR #430（`[roles.worker]` 新增 `compact_window`）并更新共享克隆后，
所有在重建前启动的 option-trader drive 以 "is not a key of a session role" 崩掉（一个在第 2 轮 review 死掉）。
11-03 合并 #1391（新增 `reviewer.skills`）又发生：#1778 #1780 #1787 以 "other" 原因停止。
**第二次发生时，记忆里已经有这一条。** 这是"记忆不是检查"的又一个例子（回到 1.5）。

### 5.3 根因归纳

1. **没有"配置的 schema 版本"。** 二进制拒绝未知键（严格解析），配置却是共享的、每一步都重读。
   两者的更新时间点不一致，就是崩溃窗口。
2. **进程与代码的耦合没有边界。** 同一个检出目录既是源码，又是运行态（`bin/crew` 由它构建、`roles.toml` 由它读）。
   AGENTS.md 里 "A pull request … that changes the shape of … still reads the old shape: crewd re-executes the new binary and reads
   what the old one wrote within seconds of the merge; the dispatcher checks this at step 6" 是一整套靠人记住的协议。
3. **自我替换（re-exec）是对"不想请 owner 重启"的回答，但没有解决"旧进程的子孙"。**
   子进程的生命周期应该由一个不 re-exec 的小监督者持有（keeper 的职责本该如此），业务进程可以随便换。
4. **损坏恢复写在一个设计里，不是启动流程里的固定步骤。** 要等到 11-11 一次真实事故才补。

### 5.4 给 foreman 的建议

- **配置有版本**：`config_revision` 字段；二进制遇到更高 revision 时**降级为只读 + 明确报错**，而不是崩；
  遇到未知键时警告而不是拒绝（严格解析留给 `foreman check` 命令，不留给运行中的 drive）。
- **配置快照随运行走**：drive 启动时把它要读的配置复制到该次运行的目录（agent-crew 对规则已经这么做——
  `<home>/runs/<project>/<issue>-<n>.rules.md`，但对 `roles.toml` 和声明没有）。drive 之后只读自己的拷贝。
  这一条同时解决 1.2 的"规则过期"问题（那是另一个方向的同一个根因：**配置必须有明确的生效点**）。
- **keeper 不 re-exec，只管子进程；crewd 可以 re-exec 但必须先把子进程交还 keeper**。
- **数据库**：单写者。让 crewd 持有写，hook 通过 Unix socket 或一个追加文件交给它，不要让每个会话直接写 SQLite。
  agent-crew 的 AGENTS.md 要求"hook 用一次 INSERT、一个 WriteTx"，这是在**限制**多写者带来的风险，不是消除它。
- **启动自检**：crewd 启动时的第一步固定是（锁、数据库完整性、孤儿进程回收、配置版本），每步有日志行，
  `foreman status` 能打印"上次启动做了什么"。

---

## 6. 与 owner 的交互

### 6.1 渠道一览

| 渠道 | 方向 | 现状 |
|---|---|---|
| 终端里直接对话（窗口） | 双向 | owner 最直接的渠道，owner 的原话进"ask ledger" |
| ask ledger（`<runner home>/asks/`） | 记录 owner 说过的话 | 8,508 行（见 6.2） |
| plan（里程碑与条目） | 记录"欠着什么" | agent-crew 的 M1 有 35 条，30 条完成（`crew plan agent-crew` 的头部行） |
| Telegram（crewd 轮询） | 推送问题、接受点按/回复 | 58 个提交含 telegram；设计文档 6 份 |
| 看板按钮 | owner 在手机上按 | 见第 4 节 |
| owner WAIT 行 | dispatcher 在状态页写、crewd 转 Telegram | 带字母选项 a)/b) |

### 6.2 ask ledger：7,000+ 行跳过

`crew ask agent-crew list` 的计数（只数不读正文）：

| 状态 / 类别 | 行数 |
|---|---|
| skipped / system | 4,170 |
| skipped / wake | 1,538 |
| skipped / owner | 1,325 |
| merged / owner | 837 |
| issued / owner | 309 |
| issued / draft | 104 |
| unprocessed / system | 190 |
| open / owner | 13 |
| merged / telegram，skipped / telegram | 11、6 |
| 合计 | 约 8,508 |

读法：

- **跳过占 83% 以上**（4,170 + 1,538 + 1,325 + 6 ≈ 7,039 / 8,508）。
  账本把"每个会话收到的每条输入"都当成"可能是 owner 说的话"来记录，然后逐条判断"这不是 owner 说的"。
  列表最前面的条目就是例子：多数是**子 agent 的回报、另一个会话的跨会话消息、任务通知**被 hook 抓成"owner 的 prompt"，
  判断为 "not the owner" 再跳过。这是同一个根因：**没有可信的"发言人"字段**。
  hook 看到的只是"一段输入文本"，无法知道它来自人还是来自另一个 agent。
- 真正由 owner 触发了动作的只有 837（merged）+ 309（issued）= 1,146，占 13.5%。
- owner 类里仍有 1,325 条被跳过（占 owner 类 2,484 的 53%）：有的是问题、有的是闲聊、有的已就地回答；
  这些需要人读一眼才知道，**账本没有"这是闲聊"这样的低成本分类**。
- `unprocessed / system` 190 条与 `crew ask <project> list --unprocessed` 报 0 的**口径不一致**
  （上一篇第 0 节已写，设计 `docs/design/ask-plan-close.md` 的 PR #3365 正在修，**效果未核实**）。
- 账本里存着 owner 的原话，所以被 AGENTS.md 规定"只能通过 `crew ask list` 和看板读取，只显示预览和计数"，
  并对沙箱会话**禁止读取**。这个保护合理，但**也让 dispatcher 很难自己审计账本**（我自己读它就只能靠计数）。

给 foreman 的建议：

1. **发言人用不可伪造的通道区分。** 人的输入由 harness 的"user message"事件给出，不由文本内容猜；
   子 agent 回报、跨会话消息、任务通知在 hook 里就带 `source` 字段并**不进账本**。agent-crew 的 83% 噪声来自没有这一步。
2. **账本只记"owner 的决定"，不记"owner 说的每句话"。** 也就是 `docs/decisions/` 的表，而不是流水。
3. **一个 ask 要么关联一个 plan 条目、要么在 24 小时内被明确标"无新工作"**，由程序强制，
   不由 dispatcher 记得（`plan.md` 的设计就是这个意思，但实现花了一个月，且 11-27 才发现"完成"状态不会从事实推导）。

### 6.3 plan 条目

- 设计意图（`docs/design/plan.md`）：账本记"说过什么"，队列记"在跑什么"，**没人记"欠着什么"**，plan 是中间层。
- 现实：一个月内 plan 设计文档改了 4 次（`git log -- docs/design/plan.md`），相关实现 `plan.go`、`plan_ledger.go`、
  `plan_next.go`、`plan_state.go` 加测试，另有 `ask-plan-close.md`（#3365）、`PlanItem.Ready` 标志、`planResolvedAsks`（#3369）、
  patrol 的 Ready 行（#3370）、hook 的 Ready 展示（#3371）——**2026-11-27 一天就合并了 4 个 PR 在补 plan 的"关闭"语义**。
- 它解决的是真实问题（"owner 说过的话被漏了"），但解法让 dispatcher 多了一整套状态要维护，
  而 owner 的体验是：**更多问题**。（记忆 `milestone-board`：owner 11-13 要"每个项目的里程碑复选框"，
  这是 owner 要的是复选框，而不是 ledger → plan → issue → verify 的五层链。）

### 6.4 Telegram

- 做了什么（提交标题）：owner 问题在一分钟内推送（`telegram: post an owner question within a minute`）；
  合并通知每个开放 PR 一条；"合并词"（点按钮 = 合并）；回复记录 `crew telegram reply`；字母选项逐行显示
  （`9cb0056`，#2367）；Stop hook 拦截"提供了字母选项但没写 recommend"的回答（`c596ed4`，#2602）；夜间摘要；中文摘要。
- 确凿被使用的：**合并通知 + 点按**（owner 一句话，机器去做）。ledger 里的 `telegram` 类只有 17 行
  （merged 11、skipped 6），**数量很少，说明 owner 通过 Telegram 回话的次数不多**，**未核实**是否另有按钮点按没入账本。
- 已知的负担：
  - owner 说过"我根本找不到 T1、T2"：dispatcher 在 WAIT 行里用 T1/T2 这类"字母/编号"指代选项或任务，
    而 owner 在手机上看到的是一段被截断在 500 字符的文本，找不到那个标号对应哪个按钮、哪个页面。
    这是**指代没有锚点**：编号只在 dispatcher 的状态页里存在，Telegram 消息里没有按钮、看板里没有同样的编号。
  - owner 说过"我找不到 retry 的具体位置"：retry 当时只在命令行（`crew queue retry`），
    直到 2026-11-26 `e1480cf`（#3310）才把 retry 和 confirm 放成看板上 owner 收件箱行的按钮。
    **owner 提出"找不到"，到有按钮，隔了好几周。**
  - 记忆 `steer-by-retry-not-comment`：PR 上的一条评论在 drive 停止后被当成"owner 说话"并 hold 住 issue，
    导致 dispatcher 的评论误 hold 了另一个 issue（#1152 hold 了 #1151）。**一个本该无副作用的动作有隐藏副作用。**
  - 记忆 `tg-question-explains`（11-12）：owner 说"只能看一下"不够，每条必须说明 PR 做什么、对 owner 意味着什么、检查了什么、
    推荐哪个选项。也就是说，推送的消息**缺乏足够的上下文让 owner 一步回答**。
  - 记忆 `no-questions-to-owner-abc-form`（11-05）：owner 说"很多问题不要总问我，要说清楚问题是什么，
    为什么之前那么设计，你的解决方法是什么，可能有多个方法 a、b、c"。他对 merge 词之外的提问本身就不满。
  - 记忆 `advance-merge-word-does-not-survive`：提前给的合并词在窗口 renew 之后失效。
  - 错误清单 11-07：dispatcher 告诉 owner "给 Telegram 回路发一条文字来测试"，但轮询只订阅 `callback_query`，
    文字永远收不到（"读代码再建议测试"）。

### 6.5 owner 真正用的和没用的

| 实际在用（有证据） | 负担（有证据） |
|---|---|
| 一个词的回答：同意 / merge / a / b（记忆 `owner-merge-is-a-word`；`advisor-relay-counts`） | 字母/编号选项没有锚点（"找不到 T1、T2"） |
| 看板上的按钮（merge、stop、deploy、rollback、restart、retry/confirm） | 命令行里的命令（"找不到 retry"） |
| 手机推送的合并通知 | 没有上下文的 WAIT 问题；500 字符截断 |
| 在窗口里直接对话 | 账本的 83% 噪声；plan 的五层链；"owner WAIT 行"的字母语法与 Stop hook 检查 |
| "一条命令，做完或说清停在哪"（记忆 `owner-commands-are-products`） | 把一个多步命令交给 owner 当"作业"（记忆 `owner-wants-ai-to-take-over`、`stuck-flow-is-mine`） |

### 6.6 给 foreman 的建议

1. **只有一种向 owner 发问的格式：一条消息、一个 id、2–3 个按钮、一个推荐。**
   id 就是 Telegram 的 callback 数据，也是看板里同名卡片的锚点；不要字母，不要"回复 T1"。
   按钮点击 = 回答，没有"输入文字"这条路径（agent-crew 的 Telegram 回复记录、`crew telegram reply`
   以及一整套"打字行是 owner 的话，只有该命令打印记录时才算"的规则，都是因为开放了文字回复）。
2. **一个问题 = 一个数据库行 = 一个明确的"停在这里"状态**；回答后由程序恢复，不要 dispatcher 去"记住要恢复"。
3. **问题里必须内嵌：做什么、改变什么、检查了什么、推荐什么**，用结构化字段，发送前由程序校验非空
   （agent-crew 是事后加 Stop hook 去拦，`c596ed4`；应该是发送接口本身就要求这些字段）。
4. **owner 的每一个"找不到"都当作一个缺陷**：新增能力必须同时有"手机上的入口"。
   agent-crew 的 retry 从命令行到按钮隔了几周。
5. **owner 的话 ≠ 评论、≠ 窗口里 agent 间的消息。** 只有一个明确的"owner 通道"（认证过的按钮/回复），
   其余一概不是（`steer-by-retry-not-comment` 的根因）。
6. **别做"owner WAIT 行"这种在文本里编码的协议。** 结构化数据（行）+ 渲染，不要"agent 写一行特定格式的字，
   crewd 去解析，Stop hook 检查有没有写对"。
   这套"字母选项语法"在 `core` 里有解析器（`71cad8f`，#2351），在 Stop hook 里有检查，在 Telegram 里有渲染，
   在 `asks` 里还有"设计条目的字母后缀（`9a`、`13b`）"的规则（`d11b224`、`f9f8e91`）。
   同一个字母，四处实现。

---

## 7. 汇总：foreman 的取舍清单

**一定要做：**

1. 规则版本化、送达时记录哈希，会话与仓库不一致由程序处理（1.2）。
2. AGENTS.md 硬上限 6 KB，每条带 `enforced-by`；清单类内容走数据 + 生成（2.4）。
3. 规则文本只有一份，代码读它；测试钉行为，不钉文本（1.3、1.4）。
4. 单一的发问格式与单一的 owner 通道；按钮即回答（6.6）。
5. 看板读不到 GitHub（编译期隔离），首页有空/错误状态，CI 里用空后端冒烟（4.2、4.3）。
6. 配置有 revision 并随运行快照；数据库单写者（5.4）。

**可以不做（agent-crew 做了但收益存疑）：**

1. 把 skill 当成默认交付物（3.4，先量，再留）。
2. 提交构建产物（4.4）。
3. 字母选项语法、`T1/T2` 之类的编号方案（6.4、6.6）。
4. ask → plan → issue → verify 的五层链（6.3；先做复选框）。
5. 把整套规则写成散文再用 blob 哈希钉住（1.4）。

**证据较薄的问题（再次声明）：**

- 第 3 节：没有 skill 调用数的实测，也没有对照实验；结论是"机制在、闭环缺"，不是"有用/没用"。
- 第 4.5 节：没有看板访问统计，只能从"按钮需求"和"owner 的抱怨"间接推。
- 第 6.4–6.5 节：没有 owner 使用 Telegram 与看板的频率对比；只有 ledger 里 17 行 `telegram` 来源与几条原话。
- 1.3 中"88 个测试文件引用 rules/、99 行引用 AGENTS.md"是按字面匹配的计数，部分只是路径出现，不一定读内容。
