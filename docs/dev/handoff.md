# 交接：bot-connect 现状、待办与分工

> 写于 2026-10-12。接手前请按「必读」顺序读完，再动代码。

## 1. 这是什么

bot-connect 是一人一个的 IM bot（飞书），角色是委托人（principal）的 PM / 幕僚长：
接住委托人的目标，拆解、找人（优先 agent，必要时找人或别的 bot）、派活、跟进、验收、提前暴露风险、汇报。
bot 自己是 owner，推动一切；委托人只在需要他拍板、审批、提供信息的地方出现。

## 2. 不可违背的原则

1. **不碰 agent loop。** 我们只做脚手架（harness）：工具、规则、状态、信号、提示。brain 是可插拔的（目前是 Claude Code CLI）。
2. **worker 是协议，不是实现。** agent 会话、人、别的 bot（Dots / Muse / Grok …）都是 worker，别写死。
3. **规则进代码，判断交给模型。** 能算的（日期、预测、依赖、风险、闸门）在代码里算；brain 做理解、沟通、拆解和判断；Jev 做快而窄的分类。
4. **agent 优先。** 找人必须有理由（审批、决定、现实动作、关系、判断、知识、能力）。
5. **一个连续的主会话。** 所有消息进同一个 brain 会话；每一轮的权限按这一轮的发起人算。
6. **不需要兼容旧逻辑。** 现在没有外部用户，该改就改。
7. **密钥只放 git-ignore 的配置里**（`config.toml`），永远不要提交、打印到日志或对话里。

## 3. 必读

| 顺序 | 文档 | 讲什么 |
|---|---|---|
| 1 | [docs/foundation.md](../foundation.md) | 整体思路、分层 |
| 2 | [docs/domain.md](../domain.md) | DDD 领域模型、注意力信号、主会话、联系人（§8 是一个完整例子） |
| 3 | [docs/decisions.md](../decisions.md) | brain 的决策能力：计划、探索型、triage、reflection |
| 4 | [docs/dev/benchmark.md](benchmark.md) | 怎么测 brain 的能力 |
| 5 | [docs/testing-guide.md](../testing-guide.md) | 三层测试、golden case、飞书手工测试剧本 |
| 6 | [docs/dev/goals.md](goals.md) | 北极星指标、golden case 列表 |

## 4. 代码地图

```
internal/domain/        纯领域：shared, workforce, portfolio(项目/计划), planning(事项/计划节点/分析),
                        delegation(委托状态机), insight(节奏/预测/找人), attention(风险检测), inbox(信号), world
internal/app/           用例：commands, delegation, plan(计划/探索/自动派发), triage(收纳), inbox(信号/唤醒), loop(Tick)
internal/judge/         Jev（TypeSafe System One）：消息分类、泄露检查、语义找人、计划审视、探索打转检查、收纳分桶、
                        诚实检查；bench.go 是组件基准
internal/assemble/      把 app、hub、judge 接起来（bot 和评测共用）；claims.go 是诚实检查的调用记录
internal/hub/           IM ↔ brain：主会话、每轮权限、出站检查
internal/brain/         brain 适配器和 prompt（persona + 协议）
internal/tools/         brain 能用的工具（pm.go / plan.go / triage.go / tools.go）
internal/sim/           评测框架：虚拟时钟、脚本化的人和 agent、检查、长程运行（longrun.go）
evals/golden|long|jev   决策点 case、长程场景、Jev 标注数据
cmd/bot-connect/        CLI：bot run / eval run|jev|list / project|item|inbox
```

## 5. 现状

### 已完成

- 领域模型：项目（含时间段角色）、事项、委托（work / approval / decision / clarification / action / review）、
  worker 状态（声明/观察/推断）、注意力信号与唤醒策略、风险检测、预测与节奏、找人（规则 + Jev 语义）。
- 计划：节点（执行者 / 理由 / 产出 / 复利 / 周期性）、结构检查、并行批次、关键路径；
  草案 → 待确认 → 执行中 → 完成；人确认自己的部分、委托人同意；探索性节点马上派出，其余确认后按依赖自动派发。
- 三种计划：task（路径已知）、goal（先判断可行性）、explore（试 → 给人看 → 学到 → 继续/转向/停/收敛）。
- 收纳：`triage` 工具把一堆事分进 8 个桶；Jev 在入口判断规模、是否做不到、是否一条消息多件事、是否授权。
- 诚实检查：回复声称“已做了”的事，对照本轮工具调用和结果，Jev 判断有没有依据；没有就附核对说明并给 brain 发信号。
- 委托人风格 `[bot.style]`（风险偏好、先问的事、预算、并行上限）。
- 暂停的项目不报截止风险、不自动派发。
- 评测：能力标签 + 按能力汇总、`--repeat`、Jev 组件基准、长程场景（虚拟两周、脚本化的人/委托人、agent 可用 haiku 写真实交付）。

### 测评数据（最近一次）

| 项 | 结果 |
|---|---|
| golden（21 题 × 2 次） | **sonnet 42/42**，opus 40/42，MiniMax-M2.7 31/42 |
| 平均每次耗时 | sonnet 20s，MiniMax 30s，opus 32s |
| Jev scope 分类 | 89%（规划闸门 98%，做不到 3/3） |
| Jev 收纳分桶 | 97% |
| Jev 诚实检查 | 16/16（**在同一份数据上调过，不算验证**，需要留出集） |
| 长程 L01（sonnet） | 前三次 0/5 完成，暴露的问题见下面 6.1；第四次修复后的运行在交接时还没出结果 |

结论：**模型是最大的变量**，brain 应换成 sonnet（线上还在用 MiniMax，换不换等委托人确认，会消耗他的 Claude 额度）。

## 6. 还差什么（按优先级）

### 6.1 正在进行 / 必须先收尾

1. **提交工作区里未提交的改动**：交付全文不再截断（`app/delegation.go`）、新工具 `assignment`、prompt 里“验收前先看全文”、
   长程场景里 haiku agent 只输出文字。`make check` 已通过。
2. **L01 跑通并稳定**：`bot-connect eval run --config <sonnet 配置> --suite long`。前几次的问题和处理：
   - 假交付是占位句 → 改成 haiku 真写；
   - 委托人不回话 → 场景加了 `[principal]` 脚本；
   - “悄悄过截止”判定只看标题 → 改成看 id / 标题 / 项目 + 风险词，并用虚拟时间；
   - brain 只能看到交付前 400 字 → 已修（6.1.1）；
   - 推迟后旧截止一直报风险 → 暂停的项目不再报。
   之后要看的：完成率、给委托人发了多少条（上限 40，第三次是 38，偏多）、有没有反复用不同工具催同一件事。
3. **线上 brain 换 sonnet**（等委托人确认）：`config.toml` 的 `[brain]` 去掉 `provider`，`model = "sonnet"`；重启见第 7 节。

### 6.2 brain 行为上已知的问题

- 同一件事换着工具反复催（delegate / agent_task / contact 一起上）。可考虑：对同一事项的跟进，代码合并成一次，或给出“已在跟进”的提示。
- 给委托人的消息偏多。可考虑：低优先级的进展合并进每日摘要（已有 batch 唤醒，可用起来）。
- 日期：模型对“下周三”有自己的理解，偶尔和我们的约定冲突（我们：周一说“下周三”= 下周的周三）。代码已有护栏，但仍会出现 brain 反问委托人。
- MiniMax 下：过度规划、反复追问、声称没成功的操作；sonnet 下基本没有。换模型后重新评估这些护栏是否还需要那么强。

### 6.3 测评（benchmark）

1. 每项能力扩到 10–20 道题（换说法、换领域、换人名的变体）；留出一部分只用来验收（建议放 `evals/heldout`，调 prompt 时不看）。
2. Jev 数据集：消息意图、回复动作、泄露检查；每份都要有留出集。诚实检查的 16 条已经调过，需要新数据验证。
3. 长程场景再加 3–4 个：L02 人请假和换人、L03 需求中途变更、L04 探索型产品（多轮转向后收敛）、L05 多项目并行抢人。
4. 跑法建议：golden 用 `--repeat 3` 看可靠性；长程每晚跑；结果存档对比（现在只输出到目录，没有历史趋势）。
5. Jev 判分本身要和人工标注对一次（拿 30 条转录让人判，算一致率）。

### 6.4 脚手架功能

- 联系渠道：飞书加急、电话、邮件（`contact` 已有抽象，渠道没接）。
- 快慢两层 brain：寒暄 / 状态查询走快模型或直接模板，规划和验收走 sonnet。
- 其他 bot 作为 worker（A2A 协议映射已在 `delegation.Status.A2A()`，没有实际接入）。
- 复利：`routine` 提醒有了，“把重复工作做成 worker/skill”的落地流程还没有（需要 worker 模板 + 让 agent 写 skill）。
- 计划的历史与回顾：计划完成后自动复盘（预测 vs 实际、哪些节点返工），写进项目卡片。

## 7. 怎么跑

```bash
cd ~/code/bot-connect
make check                                   # gofmt + vet + 全部单元测试
go build -o bin/bot-connect ./cmd/bot-connect

# 线上 bot（飞书）：先停旧进程再启动
pkill -f "bin/bot-connect bot run"
nohup ./bin/bot-connect bot run --config config.toml >> ~/.bot-connect/bot-run.out 2>&1 &

# 评测（需要一份评测配置，见下）
bot-connect eval list
bot-connect eval run --config eval.toml --repeat 3 --out eval-out          # golden
bot-connect eval run --config eval.toml --case G20,G25 --out eval-out      # 指定 case
bot-connect eval run --config eval.toml --suite long --out eval-out        # 长程（几十分钟）
bot-connect eval jev --config eval.toml                                    # Jev 组件基准
```

**评测配置**：复制 `config.toml`，`[brain]` 设 `agent = "claudecode"`、`model = "sonnet"`、不写 `provider`（用本机 Claude 登录）；
保留 `[judge]`（Jev 的 key，没有 key 时 Jev 相关的 case 会失败）。评测里的 worker、人都是脚本化的，不需要真实的飞书。
转录在 `--out` 目录下，每个 case 一个 `.md`，`bot-connect.log` 是运行日志。

**brain 会话日志**：评测时 brain 是 Claude Code 进程，完整会话在 `~/.claude/projects/*bot-connect-eval*/*.jsonl`，
可以看到每次工具调用的完整参数（转录里的参数是截断的）。

## 8. 分工建议

| 线 | 负责什么 | 交付物 | 依赖 |
|---|---|---|---|
| A. 收尾与上线 | 6.1：提交、L01 稳定、线上换 sonnet、飞书手工走一遍剧本 | 线上 bot 跑在 sonnet，L01 通过 | 委托人确认换模型 |
| B. 测评 | 6.3：题库扩充、留出集、Jev 数据集、长程 L02–L05、结果存档 | 每项能力 ≥10 题、`--repeat 3` 报告、趋势记录 | 无 |
| C. brain 行为 | 6.2：跟进合并、消息节流、日期；每次改动都用 B 的基准验证 | 基准不降，打扰次数下降 | B |
| D. 脚手架 | 6.4：渠道、快慢两层、bot-as-worker、复利落地、计划复盘 | 各自带单元测试和至少一个 golden / 长程 case | 无 |

规则：**任何改动 brain 行为的 PR，都附上改动前后的基准对比**（至少相关能力的 `--repeat 3` 结果）。

## 9. 坑

- 不要 `grep` 打印 `config.toml`：里面有 Jev key 和模型服务商的 key。要看配置就用遮掩（`sed -E 's/(key|token|secret)[^=]*=.*/\1=***/I'`）。
- 评测里的 Jev 判分问题，问题文本会被模型看到——不要把标注数据里的例子写进生产问题（会泄露答案）。
- `config.toml` 和 `~/.bot-connect/state.json` 的字段名在改名后是 `principal*`；旧数据有备份在 `~/.bot-connect/backup-owner-rename/`。
- brain 的权限按每轮发起人算；在同事的回合里它只有同事的权限，测试时注意 `from`。
- 长程场景里 agent 用 haiku 写交付，会调用本机 `claude` CLI，消耗本机账号额度。

## 10. 什么时候能用自己的 bot 迭代自己

目标：bot-connect 的 bot 当 bot-connect 项目的 PM，worker 是开在 bot-connect 仓库上的 coding agent，
委托人只做方向判断和合并审批。分四步，每一步有明确的门槛：

### 阶段 0（现在）：人 + coding agent 开发，基准已具备

### 阶段 1：bot 管理自己的研发（狗粮）

做法：在飞书里对 bot 说“把 X 做了”，bot 起草计划，派给 bot-connect 仓库上的 agent worker，
agent 在分支上改代码、跑 `make check` 和相关评测，交付 PR；bot 按完成标准验收（含基准结果），委托人审批合并。

门槛：
- 线上 brain 用 sonnet；golden `--repeat 3` 每项关键能力可靠性 ≥ 90%；L01 通过。
- 有一个“bot-connect 开发”worker 模板：工作在 worktree、能跑 `make check` 和 `eval run`、能开 PR，不能直接推 main。
- 合并 main 需要委托人审批（`approval: merge:main @ repo:bot-connect`，已有审批规则机制）。
- 验收标准里固定包含：单元测试通过 + 相关能力基准不下降。

### 阶段 2：bot 自己提出改进

做法：每周一次节奏（cadence）：bot 跑全量基准 → 把失败按原因分类（模型波动 / prompt / 工具 / 规则 / 评测本身的问题）→
对能修的起草探索型计划（假设 → 改动 → 重跑验证）→ 拿结果给委托人看。

门槛：
- 有**留出集**，而且留出集由人维护，bot 不能读也不能改（防止它对着答案调）。
- 失败原因分类有 Jev 数据集，准确率 ≥ 85%。
- 预算和并发上限（`[bot.style]`）生效：每周花多少、同时跑几个 agent。
- 长程场景至少 4 个，结果有历史趋势。

### 阶段 3：小改动自主合并

做法：不碰受保护部分、基准全部不降的小改动，bot 自己合并；其余仍要委托人审批。

门槛：
- 受保护路径清单（原则、权限、身份、密钥处理、评测框架与留出集、审批规则本身）——改这些永远要人批。
- 自动回滚：合并后线上指标（北极星：每周闭环委托数；打扰次数；诚实检查触发次数）变差就回滚并告诉委托人。
- 连续一个月阶段 2 的提议被采纳率 ≥ 70%。

### 最先可以做的三件事

1. 给 bot-connect 仓库配一个 worker 模板（worktree + `make check` + `eval run` + 开 PR）。
2. 把 `make bench`（golden `--repeat 3` + Jev 基准）做成 PR 的必跑检查，结果贴进 PR。
3. 在线上 bot 里建项目“bot-connect 自迭代”（explore 型），第一轮让它自己读本文档第 6 节，提出它认为优先的两件事，委托人来选。
