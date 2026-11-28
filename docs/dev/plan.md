# 开发计划（v1）

分工：Opus 会话拆任务、做探测、审查、合并；Sonnet 子代理按任务写代码。
额度上限：owner 的 Claude 7 天额度到 0.93 时停止派任务（留给 owner 自用）；每次合并后用一次 haiku 调用读 rate_limit_event 复查。2026-11-28 读数 0.90，12-01 11:00 重置。
状态：ready = spec 已定，可以派；blocked:<探测> = 等探测结论改完 spec 再派；done。
每个任务一个分支 task/<编号>-<短名>，审查通过后由 Opus 会话合入开发分支 dev/v1。
探测结论见 docs/probes/phase-1.md，相关决定 D-066。

## 第 1 阶段：真机探测（Opus 会话做，结论写回 spec 并把 verified: false 改掉）

| 编号 | 探测 | 影响的 spec | 状态 |
|---|---|---|---|
| P1 | srt 沙箱：会话仓库（clone --shared 自镜像）中带 pre-commit hook 的 git commit；denyRead 凭据目录；api.github.com 被拒；allowLocalBinding；同卷 cp -c 的 Go 缓存 | access.yaml R0、D-056、test:sandbox-probe-* | done |
| P2 | claude -p：stream-json 中 rate_limit_event 的字段（five_hour、seven_day）；--settings 的 advisorModel 是否生效；--max-turns；result 中的 usage | resources.yaml windows_rule、skills.yaml advisor、protocol-run | done |
| P3 | GitHub PAT：开 PR、update-branch、rerun Actions、条件请求 304 不扣额度、check-runs 在 PAT 下的可读性 | forge.yaml FG-*、OP-* | done（私有仓库读取未验证） |

## 第 2 阶段：地基（交给 Sonnet）

| 编号 | 任务 | spec | 验收（测试名） | 状态 |
|---|---|---|---|---|
| T1 | Go 骨架：go.mod、cmd/foreman（只有 version 子命令）、Makefile（build/test/lint）、internal/clock（Clock 接口、Real、Fake） | modules.yaml clock、cli；C-05 | go build/test/vet 通过；Fake 时钟的单测 | done |
| T2 | 把 tools/smcheck.rb 移植为 Go 测试：读取 spec/design/state-machine.yaml 与 protocol-run.yaml，实现与 ruby 版相同的全部检查 | state-machine.yaml；C-02 | test:state-table-exhaustive；对 ruby 版做过的变异逐一给出 Go 测试用例（报错一致） | done |
| T3 | 仓库检查：package 行数（C-03）、模块依赖方向（MD-*）、AGENTS.md 体积（C-11）、测试里禁 time.Sleep、禁执行 gh | modules.yaml；C-03、C-04、C-05、C-11 | check:size-limits、test:module-deps、check:agents-md-size、check:no-sleep-in-tests、check:no-gh-exec | done |
| T4 | spec lint：F1 id 唯一与引用可解析、F3 enforced_by 格式、F4、F5 禁用词、F7/F8、F10 phase 标签 | format-rules.yaml；C-12 | check:spec-lint、check:spec-phase-tags | done |
| T5 | internal/spec/model 与 validate、ears：Requirement/Spec/Step 类型与入库校验 | entities.yaml；format-rules F2 | test:spec-validate-requirement、test:derived-phase-total | done |
| T6 | internal/store：sqlite 打开、迁移、单写者 | entities.yaml；D-002 | test:store-single-writer | done |
| T7a | internal/pipeline/state 的纯决策函数 Decide：由 tablegen 从 state-machine.yaml 生成转移表（含表 B、escalate、HOLD-1、ignored）；不碰数据库 | state-machine.yaml；C-01、C-02 | 生成表与 smcheck 展开结果逐条一致；穷举所有 (配置, 事件, 守卫取值) 有且只有一个结果 | done |
| T7b | Transition：在 store 的单写事务中读取步骤与需求、调用 Decide、写回状态与 effects（TX-1）；补 T6 的 panic 回滚 | state-machine.yaml TX-1；entities.yaml | test:only-transition-writes-state | done |
| T8 | internal/harness/api 与 scripted 假模型；里程碑 M1：scripted 模型 + 假 GitHub 跑通一个需求 | protocol-run.yaml；D-037 | M1 端到端测试 | T8a done（harness）；M1 见下 |
| T11 | internal/resource（meter、ledger、gate、history）：资源注册表、预占与结算、Gate 的 Allow/Wait、窗口与 pacing | resources.yaml；D-027、D-030、D-051 | test:quota-all-windows-gate、test:quota-daily-pacing、test:budget-fallback-default 等 resources.yaml 引用的测试 | done |
| T9 | internal/forge | forge.yaml | test:forge-* | done |
| T10 | internal/sandbox（T10b；workspace 已由 T10a 完成） | access.yaml；D-045、D-056 | test:sandbox-*、test:session-* | done |

里程碑 M2（真实 Claude 与 GitHub 跑通 foreman-sandbox 上的一个需求）在 T8–T10 之后另行拆分。

## 遗留（审查时记录，后续任务处理）

- T5 未实现的 entities.yaml 约束：spec_version 递增、current 必须有引用、必填列表是否允许为空（change、repro、current）、E-STEP 全部约束（max_len、min_items、required_when、code_role）、Acceptance/Unknown id 唯一、E-REF 模块名对照 modules.yaml。需要先在 entities.yaml 写清「必填列表能否为空」与 spec_version 起始值，再开任务。
- T6：Write 中 fn panic 时事务不回滚、写连接被占住；T7 前补 recover + rollback。
- T7a：S19 hold_released 在 hold 仍为 set 时经 HOLD-1 回到 waiting(hold)；reconciler 必须只在 hold 清除后发 hold_released（T7b/reconciler 任务加断言）。无 HOLD-1 而进入 waiting(hold) 的行保留当前 stage，resume_stage 由 record_resume_stage effect 写入。
- T10a：integrate 的 push 停止标志（默认分支被改动后停止推送）只在内存；需要持久化到 global.db（reconciler 任务）。reconciler 必须先 Mirror.Update 再 SyncFromMirror。
- T11 未实现：unknown_capacity（无百分比的模型按近 7 天 p95 估算）、额度错误时把累计值记为新上限、RES-GH-ACTIONS 夜间隔天规则（pipeline）、告警去重（notify）、floor 时的磁盘清理；SessionSlots 默认 4 待 CF-04 定义；claude status=rejected 未当作耗尽（取值集合未核实）。pacing 的当日基线不持久化，重启后偏宽松。
- T10b：sandbox.New 拒绝位于 denyWrite 目录内的会话仓库与运行临时目录，所以 denyWrite 不能是整个 FOREMAN_HOME，只能是其中的 projects/<p>/repo.git、db、config、secrets 等子目录；会话仓库与 runs/ 放在 FOREMAN_HOME 下但不在 denyWrite 内（需在 access.yaml 写明目录布局）。RunTmp 路径 ≤ 70 字节（srt 的 unix socket 路径上限）。srt 让沙箱内 .git/config 与 .git/hooks 只读（有利：会话改不了 hooks）。
- T9：PRListOpen 不分页（只取 100 条）；ETag 缓存键是完整 URL，不区分 token；JobLogs 最多 32 MiB 不缓存，调用方必须过 privguard；gate.New 在 Ledger 为 nil 时自建内存 ledger（forge 测试用）。
- T8a：claude 参数含 --permission-mode bypassPermissions（D-056：沙箱内放开），因此 claude 会话必须只经 sandbox/launch 启动——stage/reconciler 任务要加测试：没有沙箱包装的 claude 调用被拒绝。skills 以文本注入提示词（--disable-slash-commands 关闭了原生 skills），M-3「skill 调用次数」指标因此测不到，需要换度量方式或改用原生加载（下次探测确认 Skill 工具在 --disable-slash-commands 下能否工作）。todo.json 校验未实现。真实 CLI 冒烟测试（haiku，1 轮）通过。
- T7b：包级 Transition 用真实时钟与默认配置，daemon 必须用 NewEngine 注入 clock 与 skills.yaml 的模型配置；LogTail 未经 privguard（privguard 模块未实现，调用方必须先脱敏）；PV-2/5/7/8 留给 planner 校验器；默认 deadline（running 60m、rescue 2h、ask 72h、parked 7d）待 CF-02 配置化。

## M1

- 骨架 done（2026-11-28）：快车道端到端通过，命中 S04 S02 S40 S05 S41 S06 S42 S10 S07 S29 S08 S44；红 base 场景 S03 S13。
- L0 隔离（D-072 ②）done：L0 在 runs/<id>/l0 的一次性检出中运行，集成目录恢复 denyWrite。真实 srt 下的拒写未单独测（P1 已验证 denyWrite 与读取 alternates 在 srt 下成立）。
- 骨架未做（后续场景任务）：update-branch、合并前等 L1、S47 护栏、needs_rereview；L1 失败分类与重跑；access_request（现按 bad_result）；todo.json；LogTail 的 privguard；stream 中额度事件写入 meter；hold_released 路径；合回后会话仓库不刷新（多步骤需求看不到前一步）；周期性 FG-7 轮询（骨架在 Tick 内临时 refreshBase）。
- 重启恢复 done（task/m1-restart）：Compose 可在已有 FOREMAN_HOME 上重开（integrate.Open、mirror 校验、账本由 runs.reserved 重建、push 停止标志持久化到 global.db push_halt）；会话改为 launch.lock/pid.json/setsid 异步（internal/pipeline/recover、sched/session.go）。未做：D-047 第 4 步（配置 revision 检查，待 config 模块）、每小时备份与心跳、foreman status 命令；孤儿回收只覆盖 status=killing 的 Run 与 TMPDIR 扫描（macOS 上 ps -E 看不到 Apple 系统二进制的环境，会话内由它们 setsid 逃逸的子进程找不到）；dispatch 的 Transition 与 runs.reserved 写入之间崩溃时该 Run 没有预留记录，重启后按新租约申请；check/merge/ci 阶段仍在 Tick 内同步执行（重启后按幂等规则重做）。
- 场景任务（并行）：check_fail 重试、review_changes、l1_fail → fix 步骤、hold 与 owner ask、重启恢复、多步骤需求；退出条件 = 状态表中没有场景命中的行清单为空或逐条说明。
- 场景 A（步骤内重试）与场景 C（重启恢复、异步会话）done；合并后修复：脚本只在启动会话时选一次（生产 bug）。D-047 剩余：配置 revision 检查、wal_checkpoint 与 VACUUM INTO 备份、心跳、foreman status、doctor 密钥检查、自更新。macOS 上 ps -E 看不到系统自带程序的环境，逃逸的 sh/sleep 类子进程扫描不到。
- 场景 B（集成路径，task/m1-integrate）done：L1 失败分类与重跑（每 head 一次，OP-09，日志只取测试名）、l1_fail → fix 步骤全程（S31、S35、S30、S43）、ci_progress/lease_wait 只在 35m deadline 延长（Run 与租约保留）、update-branch 与 needs_rereview/只重跑 L1、S47 护栏与 H21 授权写 guardrail_approved_sha、L0 的 forbidden-paths（G-05）。生产 bug 修复：连续两个步骤共用会话分支时第二步合回冲突（SquashMergeBack 以 Step-Head 记录起点）。未做：privguard 过滤 L1 日志、queued 且有 runner 的判定（无 runner 接口，queued 一律按无 runner）、S45b/S48b 的 killing Run 收尾、guardrails.yaml 的 config 加载（现为 Config.Guardrails 传入）、l1_pass 后关闭 integrate 步骤的 check finding。
- 场景 B（L1 失败→fix 步骤、L1 分类重跑、deadline 延长、update-branch 与重新评审、护栏 H6、.github 禁改）done。生产 bug 修复：同一会话分支上的第二个步骤（fix）合回冲突（Step-Head trailer）。遗留：L1 再次通过后 integrate 步骤的 check finding 未关闭；update-branch 异步时可能重复调用；job 日志未经 privguard 不进入 finding（只取失败测试名）；kill_run 后的 Run 只在启动恢复时回收；护栏清单由调用方传入（缺配置加载）。git 调用加 gc.auto=0、maintenance.auto=false（测试中发现后台 gc 写目录）。
- 场景 D（多步骤需求，task/m1-multistep）done：size m 需求 S1 → S2 → integrate 经 Tick 走完（S02 S10 S30 S06 S43 S08 S44）；合回后会话仓库刷新（session.Refresh：mirror.ImportBranch 把需求分支从集成目录取进镜像，再用 clone --shared 在旁边新建会话仓库并 rename 替换；旧仓库只重命名与删除，不对它执行任何 git，D-056）；l1_pass 关闭 integrate 步骤的 check finding（state-machine.yaml implicit_writes 新增一行）。注意：Mirror.Update 的 --prune 会删掉镜像里尚未推到远端的需求分支，刷新每次合回都重新导入，不依赖它留存；会话中途未提交的内容在刷新时丢弃。
- 场景 D（多步骤需求、会话仓库安全刷新：合回后从镜像重建会话仓库，旧目录只改名删除，不在其上执行 git）done。遗留：步骤 reviewer 的 diff 基点仍是 mirror/main，应为步骤开始时的需求分支 head。
- privguard（P1–P4，internal/privguard）done，已接入 L0 日志、L1 job 日志与 findings 写入；仓库全部 tracked 文件扫描通过（含 owner 字面值的严格版本本地通过，值不进仓库）。未做：gitleaks、P5–P7、PR 标题正文扫描的调用点、独立 CLI。例外：RFC 2606 示例域名邮箱、127.x、go.sum 的 h1: 哈希。
- secrets 与 config 模块 done（internal/secrets、internal/config/schema、internal/config/revision，global 迁移 0005）。未接入 Compose：schema.Load、secrets 读 GitHub token、Run 创建时 Snapshot 待接。未做：CF-03 skills.toml、CF-08 notify.toml、foreman config 命令、沙箱拒读 secrets 目录的探测。默认值改为由 spec/design/config.yaml keys 表生成（D-074，configgen）；spec 未给数值的键为 owner 必填：secrets_dir、parallel.workers、role.*.timeout、项目 repo_path/github_repo/default_branch/l0。
- D-075 取代 D-069：forge 改回 go-github + githubv4（待额度重置后派任务；保留自写 ETag RoundTripper、Gate 包装、错误映射；完成后在 foreman-sandbox 重跑 live smoke）。
- 待做（owner 已同意方向）：modules.yaml 外部依赖白名单 + test:module-deps 检查 go.mod/import；排查 D-066..D-073 与新增依赖（BurntSushi/toml 未登记）是否 spec 迁就代码。待 owner 决定：AI 写的未确认决定不得取代已有决定（lint 强制）。

## 开发顺序：逐层真实可运行（owner 2026-11-28）

下层在真实环境跑通后才往上做；fake 只用于真实环境造不出的失败（CI 失败、限额），不再用 fake 推进新功能。
每层有一个真实环境的运行入口（命令或带环境变量开关的测试），上层出问题时下层仍可单独运行和修改。

| 层 | 内容 | 真实环境 | 现状 |
|---|---|---|---|
| 1 | store、workspace（git） | 本机 sqlite、真实 git 仓库 | 已真实 |
| 2 | sandbox | 本机 srt | 探测通过；会话启动未在 srt 中验证 |
| 3 | harness | claude 在 srt 内用专用 token | 探测通过（P4）；修 CLAUDE_CODE_TMPDIR；待：launcher 的 node 目录配置、把探测改成带开关的真实测试 |
| 4 | forge | foreman-sandbox 仓库 | done：TestForgeLiveSmoke 覆盖 OP-01..OP-14 与 FG-8，83s 通过（2026-11-28）；rerun 在绿 CI 上 403 属预期；分支保护 403＝unavailable |
| 5 | 单个 stage | code 阶段：真实会话仓库 + 层 2–3 | done（2026-11-28）：TestLayer5CodeStageLive 在 foreman-sandbox 上通过——真实 srt + Sonnet，4 轮约 0.034 美元，code → L0 检查通过，约 23 秒。首次实跑发现并修复：沙箱可读系统临时目录（D-090）、会话不知 run_id 与结果格式（D-091）、超时未硬性执行、用量记为 0、结果里的缩写提交号 |
| 6 | 一个需求端到端 | foreman-sandbox 上从接需求到合并 | 未做 |
- smcheck 瘦身 done：1597 → 1080 行，YAML 只在 model.go 解析一次（tablegen、exhaustive 共用），tools/smcheck.rb 删除（T2 行为历史记录）。变异测试全部保留，只改了期望的错误文字格式。
- forge 改用 go-github v93 + githubv4（D-075）done，TestDepsRegistry（check:deps-registry）done。自写代码 1073 → 1217 行（spec 估计 600–900，未达到）。真实 GitHub 冒烟只覆盖 3 个只读操作（RateLimit、PRListOpen、RunsList）；写操作（开 PR、合并、rerun、update-branch、job 日志、GraphQL 批量）未在真实 GitHub 上验证，属于逐层计划第 4 层。GraphQL 非 200 错误未类型化。
- 解耦 done（D-078）：MD-10..MD-12 由 test:module-deps 用 go list 检查，MD-13/14 由 golangci-lint v2.15.0（go.mod tool）检查，均在 go test 中运行。pipeline 只依赖 clock、privguard、resource、spec、store，经 sched/ports.go 的接口调度，具体实现在 cmd/foreman/stages.go 注入；sched 导入 21 → 7 个包；stage 兄弟导入 3 → 0（共用代码在 stage/context）。合并后层 4 真实流程重跑通过（85s）。遗留：cmd/foreman 测试约 115s 超 C-06；约 90 个只读全局表改成了函数（gochecknoglobals 不认只读表）。
- 我自己的提交两次绕过了检查（spec lint 失败仍提交、提交含绝对路径）。程序化办法：仓库内的 pre-commit 钩子跑 spec lint + 隐私扫描，需要 owner 同意设置 core.hooksPath（AGENTS.md 禁止自行改 git config）。
- 沙箱白名单 done（D-080..D-082）：主目录与数据卷整体禁读，allowRead 只放会话仓库、运行目录、镜像（只读）、harness 与配置的工具链；所有路径 realpath 并在启动时自检。会话 git 用 foreman 生成的 .gitconfig（noreply 身份）。真实测试：TestSandboxReadAllowlist（真实 srt，假主目录，shell 逐项）、TestPushLiveGitHub（token 只在 git 进程环境里，经 http.extraHeader；断言 token 不在 argv 与任何文件）均已在本机跑通。
  - 未满足 D-081 ⑤：srt 0.0.78 CLI 不输出违规记录（只在库内存里），要满足须 foremand 自己跑 log stream，待定。
  - 遗留：cmd/foreman/main.go 还没把 runtime.toml 的新键接进 compose；harness 安装目录没有配置键，暂由 sandbox.read_paths 给出；镜像整体可读（D-081 ①）比 access.yaml R0 的"只 objects"宽；镜像 fetch 无凭据，只支持公开仓库。
  - TestRestartRecovery 在 go test ./... 全并行负载下卡死过一次（8 分钟），单跑与合并前后各 8 次都通过；记为偶发，待查。
