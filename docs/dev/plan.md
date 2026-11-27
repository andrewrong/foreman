# 开发计划（v1）

分工：Opus 会话拆任务、做探测、审查、合并；Sonnet 子代理按任务写代码。
状态：ready = spec 已定，可以派；blocked:<探测> = 等探测结论改完 spec 再派；done。
每个任务一个分支 task/<编号>-<短名>，审查通过后由 Opus 会话合入开发分支 dev/v1。
探测结论见 docs/probes/phase-1.md，相关决定 D-066。

## 第 1 阶段：真机探测（Opus 会话做，结论写回 spec 并把 verified: false 改掉）

| 编号 | 探测 | 影响的 spec | 状态 |
|---|---|---|---|
| P1 | srt 沙箱：会话仓库（clone --shared 自镜像）中带 pre-commit hook 的 git commit；denyRead 凭据目录；api.github.com 被拒；allowLocalBinding；同卷 cp -c 的 Go 缓存 | access.yaml R0、D-056、test:sandbox-probe-* | done（项目缓存克隆耗时待补） |
| P2 | claude -p：stream-json 中 rate_limit_event 的字段（five_hour、seven_day）；--settings 的 advisorModel 是否生效；--max-turns；result 中的 usage | resources.yaml windows_rule、skills.yaml advisor、protocol-run | done |
| P3 | GitHub PAT：开 PR、update-branch、rerun Actions、条件请求 304 不扣额度、check-runs 在 PAT 下的可读性 | forge.yaml FG-*、OP-* | done（私有仓库读取未验证） |

## 第 2 阶段：地基（交给 Sonnet）

| 编号 | 任务 | spec | 验收（测试名） | 状态 |
|---|---|---|---|---|
| T1 | Go 骨架：go.mod、cmd/foreman（只有 version 子命令）、Makefile（build/test/lint）、internal/clock（Clock 接口、Real、Fake） | modules.yaml clock、cli；C-05 | go build/test/vet 通过；Fake 时钟的单测 | ready |
| T2 | 把 tools/smcheck.rb 移植为 Go 测试：读取 spec/design/state-machine.yaml 与 protocol-run.yaml，实现与 ruby 版相同的全部检查 | state-machine.yaml；C-02 | test:state-table-exhaustive；对 ruby 版做过的变异逐一给出 Go 测试用例（报错一致） | ready |
| T3 | 仓库检查：package 行数（C-03）、模块依赖方向（MD-*）、AGENTS.md 体积（C-11）、测试里禁 time.Sleep、禁执行 gh | modules.yaml；C-03、C-04、C-05、C-11 | check:size-limits、test:module-deps、check:agents-md-size、check:no-sleep-in-tests、check:no-gh-exec | ready |
| T4 | spec lint：F1 id 唯一与引用可解析、F3 enforced_by 格式、F4、F5 禁用词、F7/F8、F10 phase 标签 | format-rules.yaml；C-12 | check:spec-lint、check:spec-phase-tags | ready |
| T5 | internal/spec/model 与 validate、ears：Requirement/Spec/Step 类型与入库校验 | entities.yaml；format-rules F2 | test:spec-validate-requirement、test:derived-phase-total | ready |
| T6 | internal/store：sqlite 打开、迁移、单写者 | entities.yaml；D-002 | test:store-single-writer | ready |
| T7 | internal/pipeline/state：Transition，转移表由 state-machine.yaml 生成为 Go 数据（生成物标 generated） | state-machine.yaml；C-01 | test:only-transition-writes-state、test:state-table-exhaustive（复用 T2） | ready（T2、T5、T6 之后） |
| T8 | internal/harness/api 与 scripted 假模型；里程碑 M1：scripted 模型 + 假 GitHub 跑通一个需求 | protocol-run.yaml；D-037 | M1 端到端测试 | ready |
| T9 | internal/forge | forge.yaml | test:forge-* | ready |
| T10 | internal/sandbox、internal/workspace | access.yaml；D-045、D-056 | test:sandbox-*、test:session-* | ready |

里程碑 M2（真实 Claude 与 GitHub 跑通 foreman-sandbox 上的一个需求）在 T8–T10 之后另行拆分。
