# 第 2 轮评审处理（gemini-review-2.json）

范围：核对第 1 轮修复 + 找修复引入的新问题。结果：39 条第 1 轮意见中 15 条 fixed，其余 partially_fixed 或 fix_introduced_problem；新发现 8 blocking、6 major、2 minor。
处理原则（owner 2026-11-27）：只修 blocking；major 能低成本修的一并修，其余记入遗留，实现阶段处理。R2-Bn / R2-Mn 为 findings 顺序编号。

## blocking（全部已修）

| 编号 | 问题 | 落点 |
|---|---|---|
| R2-B1 | H6 护栏与 H4 预算在状态机中无落点 | S00（dispatch 守卫 budget: over → H4）；S47 guardrail_hit → H6；E-PR.guardrail_approved_sha；G-05 改为 L0 check:forbidden-paths 直接 check_fail |
| R2-B2 | parked 下的 owner_answer 被列入忽略；H16 不清 parked | ignored 去掉 parked；H16 加 set:parked=false、kill_runs；smcheck 新增"忽略不得与行重叠" |
| R2-B3 | ci_extensions 不被重置、按轮询触发会 12 分钟耗尽 | S29–S32 reset:ci_extensions；只在 35m deadline 到期发出；H14 的 cause_counter 改为"创建 ask 那一行 at_cap 的计数" |
| R2-B4 | S45 多步无界环、合并中途释放合并槽 | 改设计消除（不是由检查守住：多步环检查豁免 quota/hold 等待，恢复旧 S45 时 smcheck 仍通过）：合并槽与 base-health 在 S08 的 lease 中取得；S45/S48 改为 running(merge) 自环并计 merge_waits（上限 6）；S45b/S48b 到上限 → H7 |
| R2-B5 | 需求级 retry 不给额度；H23 丢 integrate 后 DT-2 重建；grant_one 抬高 0 值计数 | H07 grant_one:fix_steps,planner_failures；integrate 不提供 drop_step；grant_one 定义为 min(c, cap−1)；dims.counters 去掉 rescue_retry |
| R2-B6 | 增量评审无起点、S46 无上限、merge-if-disjoint 说法矛盾 | E-STEP.last_reviewed_sha；S42/S52/S29/S43 写入；S46 计 rereviews（上限 2）+ S46b escalate；E-PR.combination_tested 删除，统一为"不相交只重跑 L1" |
| R2-B7 | 模块依赖规则导致合理导入全部违规 | 同模块内 package 可互相导入；MD-7 跨模块边 pipeline→stage/role、role→stage/context；MD-8 只约束 state；MD-9 config→store |
| R2-B8 | 协议与实体五处冲突 | PF-RESULT.discoveries 加 evidence/supersedes 并修示例；删 rejected_by；reviewer/rescuer 字段限定 status == ok；E-RUN harness/prompt_hash 只对 AI 角色必填；E-FIND 加 kind、failing_tests、log_tail |

另：smcheck 发现并修复 R1 访问申请可无限重复（新增 req_counter access_requests 上限 5、S82 escalate）。

## smcheck 新增检查（均以变异副本验证能报错）

- 显式忽略项不得与任何行重叠（两张表）
- 所有状态的自环必须递增计数
- 多步循环：迭代删除"递增了环内无人重置的计数"的边，剩余图中仍有环即报错（deps/quota/hold 等待与 S09 告警计时器豁免）。局限：hold 包括 rescue/replan 这类自动处理，经过它们的环只由需求级计数（rescues、replans、req_rescues）约束，本检查看不到；quota 等待也被豁免，所以 R2-B4 那种经 waiting(quota) 的环检查不出——实现阶段把检查移到 Go 测试时补需求级计数建模

## major

| 编号 | 处理 |
|---|---|
| R2-M1（GEM-1 跨文件） | 已修：access R0 补镜像 objects 只读与模型 API 主机；D-056 已写明两类例外（D-045 的"唯一"措辞由 D-056 取代口径） |
| R2-M2（决定矛盾） | 已修：D-060 取代 D-049、D-061 取代 D-010、D-062 取代 D-048（更正 D-018→D-015）、D-063 取代 D-039（与 G-05 一致，并把 D-058 中 D-032 的覆盖改为 test:module-deps）、D-064 取代 D-037（不再引用 D-029）；skills、resources、format-rules 文件头引用已更新 |
| R2-M3（表 B 缺口） | 遗留：drained 需同时等待 waiting(rescue) 的 rescuer；首次拆解不应计 replans；H21 [仅本次] 需字段承载；H22 的 access_denied 目标步骤需写明 |
| R2-M4（协议） | 部分修：三份协议已补 compat 规则；遗留 single_step 的 required_when 与 type 字段、cannot_check 与 check 比对的语义 |
| R2-M5（prod-access） | 延后：phase: v2，实现 v2 前按 ACR-20 修 |
| R2-M6 / minor | 遗留：testtrace 应排除 phase: v2 的测试；FG-9 裸 sandbox；CF-01 改为远程地址；derived_phase 次序；access.yaml 头部 .yaml/.toml |

## PAT 实测（2026-11-27，foreman-sandbox）

- 已验证：触及 .github/workflows/** 的 push 被拒（无 workflow 权限）；普通 push 与删除分支成功；token 到期日 2027-02-25。
- 未验证：check-runs、actions runs、commit status 的读取返回 200，但仓库是公开的，任何 token 都能读，不能说明 PAT 权限；私有仓库读取、开 PR、Actions rerun 留到第 1 阶段探测。细粒度 PAT 的权限列表里没有 Checks。
