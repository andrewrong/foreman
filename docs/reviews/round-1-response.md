# 第 1 轮评审处理（agent-crew-review-1.md + gemini-review-1.json）

处理规则（D-025、D-028）：每条写 采纳 / 不采纳 / 延后 / owner 决定，附理由。Gemini 的事实性断言须回到 spec 原文或 agent-crew 仓库核对后才作为依据。
GEM-n 为 gemini-review-1.json 的 findings 顺序编号（从 1 起）；ACR-n 见 agent-crew-review-1.md。

## 已完成

| 编号 | 处理 | 落点 |
|---|---|---|
| ACR-01 | 采纳 | state-machine `add_findings:check`（S31/S32/S50/S51/S58/S60/S61）；PF-CTX"上一次失败" |
| ACR-02、GEM-9（锁部分） | 采纳 | RES-SLOTS `merge/<repo>`、RES-BASE；reconciler running(merge) |
| ACR-03、ACR-17 | 采纳 | D-038 |
| ACR-04、ACR-22 | 采纳 | spec/guardrails.yaml、D-039 |
| ACR-23 | 采纳 | D-035、D-036（owner 2026-11-27） |
| ACR-32 | 采纳（owner 选：保留接口，v1 只做 claude + scripted） | D-037 |

## 待改：blocking（已核对属实）

| 编号 | 问题 | 核对 | 计划 |
|---|---|---|---|
| GEM-5 | S31/S56/S58/S60 只守卫 fix_steps，从不 inc；DT-3 无去重 | 属实：全文件无 `inc:fix_steps` | 四行加 `inc_req:fix_steps`；smcheck 增加"守卫用到的计数必须在某行被递增"；DT-3 以 (head sha) 去重，存在未终态 fix 步骤时不再触发 |
| GEM-3 | rescuer.decision 无 drop_requirement，H09 依赖它；smcheck 从已有行反推枚举 | 属实 | PF-RESULT 区分 step_rescuer / req_rescuer 两个枚举；smcheck 的守卫枚举改从协议文件读取 |
| GEM-2 | H14 恢复不重置计数；R2 申请冻结整个需求且缺 [永久允许][仅本次][拒绝] | 属实 | H14 按 h_item 重置对应计数（H7→infra，H5→rescues）；R2 改为步骤级 waiting(access)，补 access_granted/denied 行 |
| GEM-4 | H01 立即启动 planner，不等同需求 running 步骤；replan_effects_on_steps 漏 waiting(ci/rescue/fix) | 属实 | hold=replan 先进入 `replan_draining`，所有 running 步骤结束（或 kill）后才 create_run:planner；PV-1 改为"以 planner 启动时快照为准，期间变 done 的步骤自动视为 keep" |
| GEM-6 | reconciler 外部调用撞限额无 quota 路径；setsid 后写 pid 前崩溃导致双开 | 属实 | running 增加 `lease_wait` 事件（不扣 infra、延长 deadline）；启动顺序改为：先写 Run 行（pid 空）+ 运行目录锁文件，由启动器写 pid 文件；恢复时以锁文件 + pid_start 判存活 |
| GEM-7 | layer 3 模块间无允许边；MD-6 不允许导入 resource/meter | 属实 | 写出 layer 3 的有向边：stage→pipeline/state，role→pipeline/state，pipeline/sched→stage、role 改为经接口注入（sched 定义接口，cmd 装配）；MD-6 加 resource/meter |
| GEM-1 | 模型凭据必然在沙箱内；worktree 共享 .git 可被改 hooksPath 后由 foremand 在沙箱外执行 | 属实（.git 部分）；凭据部分：claude CLI 必须持凭据 | ① C-08 修正：模型凭据是唯一例外，用专用于 foreman 的可吊销 token，GitHub/线上凭据仍不进沙箱；② 每个需求一个 `git clone --shared` 的独立仓库代替 worktree（会话只能写自己的 .git）；foremand 只用 `git -c core.hooksPath=/dev/null -c core.fsmonitor= fetch <clone> <branch>` 取提交，从不在会话仓库里执行带其配置的 git 命令 |

## 待改：major

| 编号 | 处理 | 计划 |
|---|---|---|
| ACR-30、GEM-11 | 采纳 | `size: s` 快车道：intake 直接产出单步骤，跳过 planner；单 work 步骤的需求不做 integrate review（step review 即最终 review）。intake 对 owner 的小需求可用 Sonnet |
| GEM-10 | 部分采纳 | owner 说明当前以周额度为主；改为"Gate 对适配器报告的每个窗口都检查"，claude 同时解析 five_hour 与 seven_day，配置只决定 pacing 用哪个窗口；k 改为按模型分开、只用 delta > 0 的样本，样本不足时用固定保守值 |
| GEM-8 | 采纳 | 增加 E-PR 实体（id、req_id、number、head_sha、steps）；修正 derived_phase；E-RUN.role 加 ci_watch、merge；S04 按 code_role 分发 |
| GEM-9（重审部分）、ACR-02（merge-if-disjoint） | 采纳 | update-branch 后若 base 新增改动与 PR 文件集相交，重跑 L1 并做增量 review；不相交只重跑 L1 |
| ACR-05 | 采纳 | derived_phase 加兜底 `reporting`、`stalled`；test:derived-phase-total |
| ACR-06 | 采纳 | E-ASK.status；视图 v_open_asks；smcheck 检查表 B (hold, event) 全覆盖 |
| ACR-07 | 采纳 | Discovery 加 evidence、supersedes |
| ACR-08 | 采纳 | E-RUN.spec_version；PV-8 |
| ACR-09 | 采纳 | check 阶段由 foremand 执行 verify∈{test,command}；PF-RESULT 增加 cannot_check |
| ACR-10 | 采纳 | L0 与 L1 用同一 lint；l1_fail 先分类，可疑的用 OP-09 重跑一次 |
| ACR-11 | 采纳 | 启动自检、每小时 VACUUM INTO 备份、web 心跳 |
| ACR-12 | 采纳 | D-011 写明由 keeper/终端启动；foreman doctor 检查 secrets 目录可达 |
| ACR-13 | 采纳 | 自更新：版本化目录 + selfcheck + 符号链接切换 + 60s 健康回退 |
| ACR-14 | 采纳 | intake 草稿实体与 intake_turns |
| ACR-15 | 采纳 | reviewer 上下文只含 delta + 未关闭 findings；round≥2 的 delta 外 finding 降级 |
| ACR-16 | 采纳 | touches 只作提示；冲突以 merge-tree 在真实文件集上判定 |
| ACR-18 | 采纳 | R0 按主机族；被拒主机按天聚合询问；新依赖沙箱外预取 |
| ACR-19、GEM-12（P1–P7） | 采纳 | 新增 design/privguard.yaml |
| ACR-20、GEM-12（prod） | 采纳 | PA-2 加程序条件；H3 展示 foremand 自算的行数；日志按不可信数据包裹；prod 工具走带每 run 鉴权的本地 RPC |
| ACR-21、GEM-14（D-010） | 采纳 | 指标定义、样本 ≥30 或序贯检验；只对护栏指标自动回退 |
| ACR-31 | 采纳 | 需求预算 = 各步骤 p75 之和；rescue_retry 只额外给 1 轮 |
| ACR-33、ACR-34 | 采纳 | 提示词体积上限；amends 链深度 ≤1；`program:` 必须附 test/check |
| GEM-12（协议缺失） | 采纳 | 补 intake、diagnoser、improver 的结果协议与示例；协议版本兼容规则；定义 F2 |
| GEM-13 | 采纳 | Telegram token 移到 secrets_dir；forge_etags 限体积、不缓存日志；FG-7 改为 list + 对进行中 PR 的 check-runs 按 ETag 查询 |

## 待改：minor

ACR-24、25、26、27、28、29、35、36、37、38、39、40：全部采纳，按各自"最小修复"处理。
GEM-14：C-03 增加"紧急修复（source=prod，P0）可超出 50 行，下一个需求必须收回"；D-008 bisect 补落点（ci.db 表、report 模块）。

## owner 决定

| 编号 | 问题 |
|---|---|
| GEM Q3 | v1 范围 → owner 选"核心闭环 + 反馈报告"，记为 D-042；其余设计保留并标 phase: v2 |

## 不采纳

| 编号 | 理由 |
|---|---|
| GEM Q4(1) 合并为单库 | 三个库按写入频率与生命周期分开（D-004），单写者已避免锁竞争；迁移成本可由 store 包的 schema_version 管理 |
| GEM Q4(2) 改实时工具协议 | 文件协议是可恢复、可审计的基础；prod 读工具单独走本地 RPC（见上），不改主协议 |

## 处理结果

| 范围 | 落点 |
|---|---|
| GEM-1 | D-045；C-08 修正；modules workspace 改为会话仓库 + 集成目录 |
| GEM-2 | state-machine H14（reset:ask.cause_counter）、H21/H22（grant/deny）、H23（H7 的放弃该步骤）；E-ASK.cause_counter。与计划不同：R2 访问申请仍冻结整个需求（v1 步骤串行，D-042，影响相同），未改为步骤级 waiting(access) |
| GEM-3 | dims.guard_enums；PF-RESULT 两个 decision 枚举；smcheck 交叉检查 |
| GEM-4 | create_run_when_drained:planner；Run.snapshot_id；PV-9 |
| GEM-5 | S31/S56/S58/S60 inc:fix_steps；DT-3 去重；smcheck"计数从未递增"检查 |
| GEM-6 | S45/S37 lease_wait；reconciler 启动锁 launch.lock + pid.json |
| GEM-7 | modules MD-6、MD-7、MD-8 |
| GEM-8 | E-PR；derived_phase 全函数；E-RUN.role/model/scope/spec_version |
| GEM-9、ACR-02 | S46 needs_rereview |
| GEM-10 | RES-MODEL windows_rule、按模型的 k 与加权 token |
| GEM-12 | spec/format-rules.yaml（F2 等）；design/privguard.yaml（P1–P7）；design/protocol-intake.yaml；diagnoser、improver 协议随 phase: v2 延后 |
| GEM-13 | CF-08、FG-3、FG-7 |
| GEM-14、minor | D-049 |
| ACR-05/06/07/08/14 | entities.yaml；PV-8 |
| ACR-09/10/15/16/24/27/30/31/40 | D-046；state-machine S29/S38、reconciler；protocol-run role_overrides、cannot_check、PP-5 |
| ACR-11/12/13 | D-047 |
| ACR-18/39 | access.yaml R0 |
| ACR-19 | design/privguard.yaml |
| ACR-20 | phase: v2（prod-access 整体延后，D-042）；实现时按原计划处理 |
| ACR-21 | design/metrics.yaml |
| ACR-28 | reconciler kill_run 扫描残留进程 |
| ACR-33/34 | D-048、format-rules F4/F8/F9 |
| ACR-38 | phase: v2（web 延后） |

smcheck 新增三项检查（守卫枚举来自 spec、计数必须被递增、表 B 全覆盖与协议枚举交叉检查），并用三个变异副本确认能抓到对应错误。

## 遗留

- ~~F4~~ 已处理：D-058 为早期决定补检查项；spec/tests.yaml 由 tools/testtrace.rb 生成，有效条目无遗漏（4 条为不可测或 v2）。
- verified: false 仍待真机探测：R0 的 Go 缓存分层与每任务开发库、OP-07 与细粒度 PAT、Actions billing 端点、coder advisor 在 claude 适配器中的开启方式。
- ~~F8~~ 已处理：D-053～D-057 取代 D-005、D-006、D-007、D-009、D-016，D-059 取代 D-019 与 D-052；F8 规则明确为只约束有效条目，当前违规 0。
- advisor 复核后补齐的跨文件不一致：worktree 残留（D-050 取代 D-038）、D-029 与 windows_rule（D-051）、H7 的 [放弃该步骤] 无对应行（H23）、owner 按钮事件（H24、H25）、waiting(ci) 自环无上限（ci_extensions）、D-049 中只写在决定里的项已落到 constitution、skills、config、resources。
- 本轮修复本身未经独立评审。
