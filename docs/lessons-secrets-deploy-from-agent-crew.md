# 来自 agent-crew 的教训：服务密钥、配置与部署（给 foreman 的设计输入）

范围：只依据 agent-crew 仓库 main（`dec2b66`）的 `rules/service.md`、`docs/writes.md`、`AGENTS.md`、`services.toml`、
`projects/agent-crew.toml`、`internal/crew/cap*.go`、`internal/crew/token.go`、`docs/design/telegram-approve.md`、
`docs/onboarding.md`、`rules/new-project.md`，加 `gh issue/pr`（只读）和 dispatcher 窗口的错误清单
（不在仓库里，下文称"错误清单"）。

隐私约定：本文只写目录、文件名、权限位、键名，不写任何值；没有打开过任何 `.env`、token 文件或 `secrets/` 下的内容，
对这些目录只做过 `ls -l`。"owner" 指项目所有者。核对不了的标 **未核实**。

先说结论：

1. agent-crew 把**密钥放在代码和服务目录之外的一个单独目录**，由 `services.toml` 的 `secrets` 键指向；crew 自己**不打开**
   里面的服务文件，只把目录名交给服务。唯一例外是 crew 自己要用的 token，且读之前先验权限。
2. 配置分三层：代码在仓库、无密钥配置在 `<root>/<service>/config/`、密钥在 secrets 目录。改**登记**走 PR（`services.toml`
   是 owner_only），改**密钥值**不走 PR，是 owner 手改文件。
3. foreman 可以抄这套"目录 + 权限 + 不打开 + 只验权限位"的骨架，但不要抄 launchd 之外的 tmux 常驻那一套，也要避开下面几个已经踩过的坑。

---

## 1. 密钥和配置放在哪里

### 1.1 目录布局（权限只来自 `ls -l`，没读内容）

两个互不相同的目录，别混：

| 目录 | 谁用 | 目录权限 | 文件权限 | 属主 |
|---|---|---|---|---|
| `<data>/service-secrets/`（`services.toml` 的 `secrets`） | 被部署的**业务服务** | `drwx------` | 全部 `-rw-------` | owner 账号 |
| `<runner home>/secrets/`（`<data>/tmp/project/crew/secrets/`） | crew **自己的角色**与 board | `drwx------` | 全部 `-rw-------` | owner 账号 |

`service-secrets/` 里 25 个文件，命名规则：

- `<service>.env`：服务的环境变量文件，一个服务一个（如 `ocr-service.env`、`telegram_platform.env`）。
- `<service>.<别的>`：服务的其他凭据文件，名字必须以 `<service>.` 开头（如 `math_trainer.google_adc.json`）。
- 一个项目有多个服务时按 `<project>-<svc>.env` 拆（`optdesk-scheduler.env`、`optdesk-serve.env`）。
- crew 自己的 Telegram bot token 也放在这里：`crewd.telegram.token`（#1956，#1938 设计）。
- 备份文件 `optdesk-*.env.bak.<时间戳>` 也躺在同一目录（见 4.3）。

`<runner home>/secrets/` 里是 `claude-<role>.token`（acceptance、decide、diagnose、ops、reviewer、verify、window、worker）
和 `serve.token`（board 的 actions token）。每个角色一个独立 token 文件（#96："a sandboxed Claude Code role logs in with a
long-lived token from a file"）。

### 1.2 谁写、谁读

- **写**：只有 owner 手工放入。`rules/service.md`："The owner puts the keys there; no agent reads, prints or copies them."
  `AGENTS.md`："Never read a credential file"，例外只有两个：角色 token 文件只为"把 token 交给该角色的会话"而读，
  窗口 token `claude-window.token` 由 `crew window exec` 读了交给 claude 进程；两处 token 都不打印、不记录、不存。
- **crew 读什么**：对业务服务，**什么都不读**。`services.toml` 的注释写明："crew opens no file in it; it hands the directory to the
  compose project ... as CREW_SECRETS"。服务程序自己打开自己的 `<service>.env`。
- **crew 自己要用的 token**（board、Telegram bot）：读前验权限。`internal/crew/token.go` 的 `tokenFileCheck` 要求
  普通文件、`perm & 0o077 == 0`；另一处检查要求 `perm == 0o600`。`telegramToken()` 从 `services.toml` 的 `secrets` 目录读
  `crewd.telegram.token`；设计里的测试（`TestTelegramConfigAndToken`）规定：链接、0644、短于 32 字符一律拒绝，
  错误文本不含 token。
- **谁读不到**：沙箱会话（worker、reviewer 等）对 `<runner home>/secrets/` 是 deny（`internal/crew/sandbox_probe.go` 的
  `probeSecrets`，四种场景都期望 deny）；`sandbox_allow.go` 里 "a carve-out never reopens them"，即白名单放行不能重开
  secrets 目录。#117 把"没有会话能读 .env 搬去的目录"写进声明。

### 1.3 怎么注入进程

两条路，由服务的 `way` 决定（`services.toml` 每个服务一项）：

- **docker**（默认）：crew 跑 `docker compose` 时设三个环境变量：`CREW_SERVICE_DIR`、`CREW_SECRETS`、`CREW_IMAGE`
  （`cap_docker.go`:52，值是 `<service>:<短 commit>`）。仓库里的 `compose.yaml` 用 `${CREW_SECRETS}/<service>.env` 引用，
  **密钥值从不经过 crew 的进程环境**（#362、#368）。
- **host**（设备、钥匙串、GUI 会话等容器跑不了的）：**不走 launchd**。`rules/service.md`："a program that launchd starts may
  not open anything on the data volume that holds the services, their data and their keys"。每个 host 服务跑在 owner 终端
  或 keeper 启动的 tmux 会话里（`tmux -L crew`，会话名即服务名），命令是 `crew host exec <service>`；它设
  `CREW_SERVICE_DIR`、`CREW_SECRETS` 和条目里的 `env` 表，然后把自己替换成 `run` 程序。**`env` 表只放无密钥的值**，
  密钥由程序自己读 `<service>.env`。#389 是这条路的设计（"Host services in tmux under a keeper ... instead of launchd"）。
- 备注：`cap_boot.go` 里有写 `launchd` plist（0600）并用 `osascript` 登记的路径，但它是 keeper 自己的开机引导，
  不是业务服务的注入手段。**未核实**其 plist 的内容与现网 plist 的状态：按规矩没有打开 plist。

---

## 2. 配置与代码分离，改配置要不要 PR

每个服务一个目录 `<root>/<service>/`（`services.toml` 的 `root`），里面四样：

- `app/`：正在运行的发行版代码（docker：仓库在部署 commit 的检出；host：`current` 指向某一发行版）；
- `data/`：服务写的数据；
- `config/`：**无密钥的配置**，键名保留、值留空（"keeps the names of its keys with empty values"）；
- `deploy.log`：每次部署/回滚一行，格式 crew 能读。

硬规则（`rules/service.md`）：`data/` 和 `config/` 永远不在 `app/` 或镜像里，部署和回滚都不改它们；密钥不进 git、
不进镜像、不进日志；服务的任何东西都不放在 owner 的仓库检出里。

改动要不要 PR：

| 改什么 | 走 PR 吗 |
|---|---|
| 登记服务、改 `way`、`run`、`health`、`deploy`、`secrets` 目录 | 要。`services.toml` 在 `projects/agent-crew.toml` 的 `[merge] owner_only` 里，owner 合并（AGENTS.md step 7 的"改变 crew 代表 owner 合并或运行什么"） |
| 改服务代码 | 要，走正常 gate；部署由 crew 在合并后自动做 |
| 改 `config/` 里无密钥的配置 | 属于服务自己的数据，不进 agent-crew 仓库；是否 PR 取决于该服务的仓库，**未核实**有无统一规则 |
| 改 `<service>.env` 的值 | 不走 PR，owner 直接编辑文件（#3275 前后的 optdesk 就是这样，记忆里写明"owner commands must edit the service-secrets files directly"） |
| 改 agent 的权限、读写范围 | 要，且 `docs/writes.md` 有一条登记；新增写到仓库之外的路径是 owner 的 |

自动部署（crew 自己的动作，不是 agent 的）：合并后对 `way = "docker"` 或 `"host"` 的服务自动部署，失败**自动回滚**到上一次成功部署的
commit（owner，2026-10-15）；`deploy = "owner"` 的服务只提案不自动部；第一次部署永远是 owner 的。部署取默认分支上 `crew gate`
合并过的 commit，从干净检出构建，不用工作区。限速：`cap_deploy_rate.go` 里一小时内自动部署加回滚达到 10 次就挂起，写一行
`held the deploy of pull request #N: deploy rate limit ...`（#3128）。

---

## 3. 新服务的上手约定

`rules/service.md`、`docs/onboarding.md`、`rules/new-project.md` 合起来，顺序是：

1. owner 选三件事并写进条目：`way`（docker/host/own）、是否随开机启动（`boot` 或 compose `restart:`）、`health` URL 或无。
   没说就用默认并由 dispatcher 说明用了哪个默认。
2. 目录：`mkdir -p <root>/<service>`（host 服务的条目合并前这个目录必须存在，因为 keeper 用它作工作目录）；
   docker 服务的仓库里要有 `compose.yaml` 和 `Dockerfile`，端口只发布在 127.0.0.1，经 tailnet-serve 暴露
   （别的地址要在条目里写 `open_ports`，否则 `crew check` 报告）。compose 不写 `logging` 块，由 daemon 默认限 100 MB。
3. 密钥：owner 手工放 `<secrets>/<service>.env`（0600）。`config/` 里放键名空值的模板。
4. PR：dispatcher 写 `services.toml` 条目；owner_only，owner 合并。
5. 检查（全是只读/干跑，agent 可以跑）：
   - `crew check <project>`：声明与机器是否一致，一行一个待办；
   - `crew cap standard`：`services.toml` 里哪些服务满足 `rules/service.md`（`cap_standard.go`，其中 "secrets not declared" 是一条失败）；
   - `crew cap <service> status|logs`、`crew cap <service> rollback` 的干跑（`new-project.md` 第 13 项：部署方式已声明、有过首次部署、回滚有上一发行版）。
6. 首次部署：owner 的。dispatcher 把"停旧服务、备份数据、拷进 `data/`、再部署"写成按顺序的几行给 owner，agent 不执行。
   之后所有 `crew cap --apply`（restart/deploy/rollback）只有 owner 在终端跑，agent 只跑干跑并交出那一行。

模板：仓库里 `templates/project` 是项目声明模板；服务这边**没有**独立的服务模板文件，约定写在 `rules/service.md` 的文字里
（`docs/design/service-standards.md`、`deploy-standard.md` 是设计）。这本身是一个缺口，见第 5 节。

---

## 4. 踩过的坑

### 4.1 同一份密钥有两个副本，漂移
错误清单 2026-11-09，telegram_platform #4：compose 从 `CREW_SERVICE_DIR/.env` 读环境，没确认 crew 同时给了
`CREW_SECRETS` 并且里面有 `telegram_platform.env`；两份 token 副本漂移，误导了一次诊断。
教训写在清单里："list what crew passes to compose and keep one copy"。#362 就是为了把"键只放在 secrets 目录"写成规则。

### 4.2 旧位置没清，新旧并存
optdesk 的部署环境 2026-11-13 整体搬到 `service-secrets/optdesk-scheduler.env` 与 `optdesk-serve.env`，
服务目录里留下一个遗留的 `.env`，之后 owner 命令仍去读/写它，被 owner 问"为什么还在用"（记忆
`optdesk-env-in-service-secrets`）。搬完应当删旧文件，或让它在登记里被明确标成"不是来源"。

### 4.3 备份文件留在 secrets 目录
同一天搬迁产生的 `optdesk-scheduler.env.bak.202611131847`、`optdesk-serve.env.bak.202611131847` 至今躺在
`service-secrets/`（权限 0600，所以没有泄漏，但是旧值的第二份副本）。备份应放在目录之外，或明确保留期。

### 4.4 launchd 读不了数据卷
host 服务不走 launchd，原因是 launchd 起的程序打不开数据卷上的服务、数据、密钥（`rules/service.md`）。
所以 host 服务要靠 keeper + tmux，这让"重启机器后谁拉起"成为另一个问题：keeper 要由 owner 终端起，`AGENTS.md` 禁止 agent 起
`crew host up`。这是一个有代价的设计，见第 5 节。

### 4.5 沙箱的 deny 把 worker 的 commit/push 也挡了
#2779："revert: home deny of runs (PR #2732) blocks every worker's commit and push"。给沙箱加 home 目录 deny 后，
verify 的探针只读，没能练到 pre-commit hook 和 git 凭据；错误清单 2026-11-17："A worker proof (commit + push) is the only
proof for a worker sandbox change." 随后 #3275 为 optdesk 的会话单独 deny owner home。教训：任何收紧读写范围的改动，
要用真 commit + push 证明。

### 4.6 token 文件权限不合格
`tokenFileCheck` 存在，说明这是预期会发生的错误：0644、符号链接、过短的 token 都被拒绝，并且错误文本里不带 token。
`crew check` 对有 token 文件的角色只检查"目录存在、普通文件、权限位合格，不打开它；不合格的那一行带着 owner 要输入的命令"
（`docs/onboarding.md`）。

### 4.7 共享付费资源也是"密钥"的一部分
GitHub token 在 2026-10-30 被耗尽（REST），11-02 GraphQL 又被耗尽（记忆 `shared-resources-are-bounded`；#853 只修了轮询）。
owner 的原话要点：公共付费资源要先界定、能看到每小时用量、有预算。**未核实**这套用量卡现在是否已有逐小时历史
（#1172 记录里写过"History by hour is not part of this"）。

---

## 5. foreman 该不该复用，建议

foreman 要管的密钥：GitHub token、Telegram bot token、模型 API key、开发用凭据。

**建议：复用骨架，不复用实现，分四条。**

1. **复用：一个 0700 的 secrets 目录 + 每个服务/角色一个 0600 文件 + 命名约定 `<service>.env`、`<service>.<别的>`。**
   理由：这是 agent-crew 跑了一个多月、25 个文件没出过泄漏事故的做法；权限用 `ls -l` 就能审，不需要工具。
2. **复用：程序"只验权限位、不打开"。** 照 `tokenFileCheck`：普通文件、`perm & 0o077 == 0`、拒绝符号链接、拒绝过短。
   foreman 自己要用的 token（Telegram bot、GitHub）读前验，错误文本永不含值；业务服务的 env 文件 foreman 一律不开，
   只把目录名交出去。
3. **不复用 / 要改：**
   - 不要把备份放进 secrets 目录（4.3），不要保留旧位置（4.2）。
   - 一个密钥只有一份来源；加一条检查，扫"同名键在两处出现"（只比较键名，不读值）。agent-crew 现在只是错误清单里的一句话，
     不是检查（它自己的规则是"决定只在文档里就会被漏掉，要变成代码检查"）。
   - 给服务补一个模板文件和一条 `standard` 检查，否则约定只在文字里（第 3 节的缺口）。
   - 模型 API key 与 GitHub token 要有**用量与限额**登记（4.7），别等耗尽。
4. **按 foreman 自己的运行方式取舍启动方式。** agent-crew 因为 launchd 打不开数据卷而选了 tmux+keeper；
   如果 foreman 的 secrets 目录在系统盘而不是外接数据卷，launchd 注入 EnvironmentVariables 以外的"让程序自己读文件"
   方式更简单。**未核实** foreman 的目录会放在哪个卷，所以这条要 owner 决定。

要特别盯住的几件事：

- agent 角色对 secrets 目录必须是**沙箱 deny**，并且有探针证明（agent-crew 的 `sandbox_probe.go` 是做法）。白名单放行不得重开它。
- 开发凭据别和生产共用一份文件；同一个 token 给多个角色会让撤销变成全体停机，agent-crew 的做法是每角色一个 token。
- 改密钥值是 owner 的手工动作；改"哪个服务读哪个文件"是 PR。别让 agent 为了让检查通过而去改声明
  （AGENTS.md："Do not edit one to make `crew check` pass; report the difference instead"）。
- 任何新增"写 secrets 目录之外"的路径要在一份清单里登记（agent-crew 的 `docs/writes.md`）。

## 6. 未核实清单

- `<service>/config/` 的统一变更流程和 PR 要求。
- keeper 开机引导 plist 的现网内容与状态（没有打开）。
- 逐小时 GitHub API 用量卡是否已落地。
- `crew cap standard` 具体子命令名与现网输出（只在源码 `cap_standard.go` 里确认了检查项，没有运行）。
- 是否存在名为 deploy-standard 的运行时检查：只确认了设计文档 `docs/design/deploy-standard.md` 与 `service-standards.md` 存在。
