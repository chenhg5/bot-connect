# 从“触发的工具”到“协作者”：bot-connect 下一步方向调研

> 原则与分层的定稿见 [docs/architecture.md](../architecture.md)。
>
> 2026-10-10 · 调研输入：《提升CEO级认知》对话记录、Hermes Agent、OpenClaw、OpenAI Dots、Meta Muse、
> loop engineering 讨论、自进化（ACE / skill library / Experience Funnel）论文、主动性研究（π-Bench、CHIIR 2026
> workshop、HEARTBEAT 记忆污染）、METR time horizon、本地项目 agentflow 与 bot-connect 现状。

---

## 0. 一句话结论

**bot-connect 不造 agent loop。** 大脑用现成的 agent（Claude Code / Codex / pi / …），它们的循环每几个月就变强一次，
我们白拿。bot-connect 做的是**包在任意 agent loop 外面的那层框架**：worker 管理（worker 可以是 agent 会话、agentflow 流程，
**也可以是人**）、目标管理、唤醒与长周期运行、自我迭代、权限与安全边界。框架通过**工具、状态、skill、prompt 注入和硬性检查**
让一个普通的 agent 表现得像一个对目标负责、会主动推进的“幕僚长（chief of staff）”。

主动性不来自更频繁地说话，而来自三样东西：

1. **外置的状态**：目标、承诺、观察项、实验与决策日志都存成结构化状态，而不是堆在上下文里；
2. **一组嵌套的循环**：任务循环（分钟）→ 复盘循环（小时/天）→ 现实反馈循环（天/周），每一层都有触发器、停止条件和预算；
3. **可审阅的学习**：它对你、对任务分派、对“什么值得做”的判断，会随着预测误差慢慢变好，而每一次改变都能被你看到、否决。

现阶段能可靠做到的是：**跟进到底（L1）+ 守望与定时（L2）+ 有分寸的预判（L3）**；“持有目标、自己找下一步（L4）”可以做成
**人机协作的 CEO 循环**（它决策和设计实验，你和 worker 执行，现实给反馈），但不应宣称它能独立完成开放式目标；
“自进化（L5）”应从**可审阅的记忆 / 打法本（playbook）**做起，而不是黑盒自我修改。

---

## 1. 外部在做什么（2026 年的格局）

### 1.1 个人 agent 产品：从“回答”到“常驻”

| 项目 | 定位 | 主动性机制 | 记忆 / 学习 | 边界与安全 | 值得借鉴 |
|---|---|---|---|---|---|
| **OpenClaw**（Steinberger，后加入 OpenAI） | 本地优先的个人 agent，住在你已有的 IM 里 | **Heartbeat**：默认每 30 分钟读一份短 `HEARTBEAT.md` 清单，无事则回静默 token（`HEARTBEAT_OK`）不打扰；cron 管定时 | `SOUL.md` / `USER.md` 每次加载，日记式日志按需检索；Markdown 文件即真相 | 曾有 13.5 万实例暴露公网；SOUL.md 是“软约束”，可被注入绕过 | 静默 token、廉价模型做巡检再升级、渠道复用、文件即记忆 |
| **Hermes Agent**（Nous Research，约 25 万 star，B 轮 9000 万美元做企业版） | “The agent that grows with you” | 内置 cron，结果投递到任意渠道；Bot Mode 里每个 Bot 有自己的 routines | `MEMORY.md`（2,200 字符）+ `USER.md`（1,375 字符）**硬上限**、会话开始冻结注入；回合结束后**后台复盘**决定存记忆 / 建技能；技能是“程序性记忆”，用 patch 增量改；FTS5 搜历史会话 | `write_approval` 可把记忆 / 技能写入改为待审；写入前扫注入与外泄；项目技能需信任后才加载 | **有界记忆 + 后台复盘 + 技能增量修补 + 可选审批**；多 Bot 房间“有话才说” |
| **OpenAI Dots**（2026-09-29） | 常驻、有自己云电脑的 agent，“你的延伸而非对话助手”；**本身是调度者**：可以把活交给 Codex、ChatGPT Work 去做调研、分析、写文档、写软件；规划中是“一队 dots 协作” | 你不在时做**只读的主动调研**，不能发消息、改内容、操控电脑；把结果和需要人判断的决定带回来 | 从反馈中学习偏好 | 内置“何时自主 / 何时请示”规则 + Custom Rules（允许 / 需批准 / 禁止）+ 自动复核 + Activity View；敏感操作强制同意 | **前台调度 + 后台执行**的结构与我们一致；后台只读、前台行动需授权；规则三档；活动视图 |
| **OpenAI Symphony**（开源规范） | 把 Linear 这类项目看板变成 coding agent 的控制面 | 每个未完成任务分配一个 agent，人审结果 | 看板即状态 | 每个 Codex agent 在隔离沙箱里 | **“看板 = 外置状态 + 派活面板”**，人和 agent 在同一块板上 |
| **Meta Muse**（2026-09-08） | 走向“personal superintelligence” | 给定目标后制定计划并**自己推进**，关掉 App 也继续 | 记住对你重要的事 | 推理与外部动作隔离，所有对外交互经过 agent 无法绕过的 Sentinel | 目标 → 计划 → 持续推进；动作网关 |

共同趋势：**常驻、渠道化、会自己醒来、有记忆、带权限边界**。差异在于主动性做到多深、边界如何硬化。
另一个趋势：**前台负责判断与沟通、后台调度执行者**（Dots → Codex，Symphony 的看板 → agent，Hermes 的子 agent / Bot 房间）已成常态，
bot-connect 的“大脑 + worker”结构在方向上是对的。

### 1.2 研究界对“主动性”的态度：它是判断，不是功能

- **CHIIR 2026 workshop**：主动性“不只是更早行动或预测更准”，而是一种**时机合适、透明、可争辩、与用户目标一致**的主动。
  关注点：何时介入、该推断多少、如何解释、如何保留用户控制、隐私、依赖与幸福感，评估要超越任务准确率。
- **π-Bench**（100 个多轮任务）：**完成任务 ≠ 主动**，两者明显可分；推断“没说出口的需求”对当前 agent 仍然困难；
  **之前的交互能显著帮助后续的主动推断**——这直接支持“长期记忆 / 用户模型”的价值。
- **“Mind Your HEARTBEAT!”**：heartbeat 后台执行与前台对话**共用同一个 session**，后台读到的普通社交错误信息（不需要注入）
  就能进入短期上下文（误导率最高 61%），再被例行记忆保存带入长期记忆（最高 91%），跨会话影响行为（最高 76%）。
  → **后台与前台必须隔离、记忆要有来源（provenance）**。
- 自主系统治理论文：软约束（SOUL.md 一类）挡不住注入，**权限必须在工具调用层硬性检查**——这一点 bot-connect 已经做对了。
- OpenHands 的“standing intents”提案被拆分：运行时（意图评估、心跳、信号源、预算、静默时段）与展示（**每次 agent 主动发起的运行
  要标明触发原因、可按会话关闭**）分开。说明业界对“主动”的共识正在形成：**可归因、可关闭、有上限**。

### 1.3 Loop engineering：从写 prompt 到设计循环

- 定义：设计让 agent 反复“目标 → 行动 → 观察 → 调整”的控制系统，开发者从“每步提示”变为“设计提示、检查、纠偏、停止的系统”。
  组成：**触发器**（cron / 事件 hook / 目标驱动）、**明确可验证的停止条件**、工具与子 agent、**迭代与成本上限**。
- 一个好用的区分：**harness 决定 agent 能做什么（能力空间），loop 决定它随时间如何在这个空间里移动。**
  bot-connect 到目前为止主要在做 harness（worker、工具、权限、隔离），loop 只有“任务回报 + 定时任务”。
- **Andrew Ng 的三层循环**（The Batch，2026-06）：
  - 内层：agentic coding loop（分钟级，spec + 验收测试，写 → 测 → 修）；
  - 中层：developer feedback loop（小时级，人看产物、改 spec、改范围、定方向）；
  - 外层：external feedback loop（天到周，真实用户、A/B、市场信号回流到产品方向）。
  **agent 越快，越需要更慢、更好的人类 / 现实反馈。**

### 1.4 自进化：主流做法是“把经验蒸馏成可读的状态”，不是改权重

- **ACE（Agentic Context Engineering，ICLR 2026）**：上下文是不断演化的 playbook；Generator 执行 → Reflector 从轨迹提炼教训 →
  Curator 以**增量条目**写回（每条有 id 与 helpful / harmful 计数）。避免两种失败：**简短化偏差**（摘要丢掉领域细节）和
  **上下文坍缩**（反复重写把细节磨没）。
- **Skill library 一族**（SkillRL、SAGE、SkillOS、AutoSkill、Trace2Skill，及 2026 的生命周期综述）：原始轨迹冗余嘈杂，
  要提炼成可复用、可验证、可淘汰的技能，并对技能库做治理（收集 → 推荐 → 演化 → 退役）。
- **Experience Funnel**：快的“显式文本状态”适应 + 慢的“策略内化”交替；显式状态可读可改、上手快。
- Hermes 是这些想法最落地的产品化版本：**有界的记忆文件、回合后后台复盘、技能增量 patch、写入可审批**。

### 1.5 能力天花板（决定我们能承诺什么）

- METR time horizon：前沿模型在**有明确成功标准**的软件 / ML / 安全任务上，50% 成功率的任务长度约 12–16 小时以上（已顶到
  测量上限），**80% 成功率约 3 小时**；翻倍周期约 4–6 个月。
- 但这些都是“干净”的可自动评分任务。《提升CEO级认知》里的判断是对的：**还没有公开、可信的证据表明 agent 能独立在开放式、
  反馈延迟、因果模糊的现实目标（例如“赚 100 万”）上跑通可重复的闭环。** 缺的不是智力，而是**目标持续存在、世界状态更新、
  开放式评估（没有 test=pass/fail）**这一层。
- 对 bot-connect 的含义：**worker 可以被信任去做“小时级、定义清楚”的活；bot 的价值在于把模糊目标拆成这种活、
  并负责中层与外层循环。**

---

## 2. 把《提升CEO级认知》里的思想翻译成产品语言

那份对话的核心有四个，正好对应 bot 的四个缺口：

| 对话中的概念 | 产品化含义 | bot-connect 现在 |
|---|---|---|
| “给我一个任务”vs“给我一个目标，我自己找下一步” | **Goal（持续存在的目标）**，有指标、约束、复盘节奏 | 只有任务和定时任务 |
| Reality State（项目、开源数据、内容数据、业务、个人时间） | **世界状态快照**：可观测指标 + 来源 + 更新时间 | 只有 worker 状态 |
| Experiment / Decision Journal（假设、预期、实际、预测误差、教训） | **实验与决策日志**：每次判断都有可检验的预测 | 无 |
| “context 是刚发生了什么，state 才是长期记忆”；每天跑 State → Observation → Iteration → Strategy | **状态外置、每轮按需拼上下文**，避免越跑越偏 | 已有雏形：每轮重新拼 `<bot-connect-context>` |
| Agent 给你派任务（Task #172：你去拍 3 条视频） | **人也是执行节点**：bot 可以给主人 / 同事派活并跟进 | 只有 bot → worker |
| Decision Constitution（目标函数 + 约束：健康、家庭、信誉、法律） | **宪章**：bot 判断“值不值得做”的原则，写在配置里可审阅 | 只有 persona |
| “它应该会阻止你：不要做” | **敢于反对**：基于目标与证据给出“不建议”的意见 | 无 |

---

## 3. 我们到底要做一个怎样的 bot

### 3.1 定位：不造 agent loop，做 agent loop 外面的那层

“前台判断 + 后台调度”已是行业常态（Dots 会把活交给 Codex，Symphony 用看板给 agent 派活），所以“调度 agent”本身不是差异化。
bot-connect 的差异化在于**它是一个框架，而不是一个 agent**：

1. **不拥有 agent loop**：大脑和 worker 都是现成 agent，循环质量随模型和厂商一起进步，我们白拿；换大脑不丢任何东西，
   因为目标、承诺、worker、记忆、权限都在框架里，不在某个 agent 的会话里。
2. **劳动力是混合的，worker 是协议不是实现**：可以是 agent 会话、agentflow 流程、**人**（你自己、同事、外部协作者）、
   另一个 bot-connect bot，或第三方 bot（Dots、Muse、Grok bot、Hermes……），用同一套派活、跟进、回报、验收机制管理
   （见 3.4 与 [architecture.md](../architecture.md) 的 worker 协议）。
3. **面向组织而不只是个人**：同事可以在飞书里找你的 bot，在受限权限下提问、委托、留言；它是你的**对外接口**。
4. **看得到你真实的工作现场**：你本地的 Claude Code / Codex 会话、按模板临时组建的 worker、它们的进度与卡点。
5. **边界是硬的**：角色 × worker 访问级别 × 工具层检查 × 模板。主动性越强越需要可信，这是地基。

目标形态：**一个替你持有目标与承诺、调度混合劳动力（agent 与人）、对外替你接待、对内替你把关的协作者**，
它的“大脑”可以随时换成更好的 agent。

### 3.1.1 框架与大脑的分工：什么靠“软引导”，什么必须“硬保证”

框架让现成 agent 表现出主动性和责任心，手段分两类：

| 软引导（影响判断，依赖模型遵守） | 硬保证（框架执行，不依赖模型） |
|---|---|
| 协议 / persona prompt、每轮拼装的上下文块 | **唤醒**：定时、heartbeat、事件触发（agent 不能自己醒来） |
| skill：派活、复盘、写实验、写承诺的“做法” | **状态**：目标、承诺、任务、实验的存储与 schema 校验 |
| 工具描述与返回里的提示（如“记得登记承诺”） | **到期检查**：承诺 / 任务 / 实验到期由框架发现并唤醒大脑 |
| 学习产物（记忆、打法条目）以文本注入 | **权限**：谁能派给谁、哪些动作需批准、预算与打扰上限 |
| | **审计与归因**：每次主动行为的触发原因 |

原则：**必须可靠发生的事不能依赖模型“记得”去做。** 例如“答应了明天提醒”——判断是否要登记承诺是软的（skill + 工具），
但承诺一旦登记，到期唤醒和跟进是硬的（框架）。这也是对“只用 skill / prompt / 工具”的一点补充：除此之外，
框架还需要一个**很薄的控制循环**（调度器 + 状态机：什么时候唤醒大脑、带着哪些状态），它不是 agent loop，不需要聪明，只需要可靠。
我们已经有它的雏形：hub 的会话队列、定时任务、任务回报。

另一个现实约束：主动判断的质量取决于大脑模型。快而便宜的模型（如之前测过的 MiniMax highspeed）出现过“声称做了但没调工具”。
所以框架要把好行为做成**结构上更容易**的那条路：唤醒时给清单而不是开放问题，承诺 / 实验用结构化工具而不是自由文本，
关键结论要求附证据字段。

### 3.2 “协作者”区别于“工具”的五个特征

| 特征 | 工具（cc-connect） | 协作者（bot-connect 目标） |
|---|---|---|
| 驱动 | 你说一句，它动一下 | 持有目标与承诺，**到点 / 遇事自己醒来** |
| 记忆 | 会话内 | 外置状态：目标、承诺、观察项、实验、你的偏好 |
| 责任 | 执行完即结束 | **对结果负责**：跟进、催、复盘、告诉你没做成的原因 |
| 关系 | 单向命令 | 双向：会问你、会给你派活、会说“不建议” |
| 成长 | 不变 | 判断随预测误差改进，每次改变可审阅 |

### 3.3 主动性阶梯（用来定义“现在能做到哪”）

| 级别 | 含义 | 例子 | 现状 |
|---|---|---|---|
| **L0 被动** | 问了才答 | 回答问题 | ✅ |
| **L1 跟进到底** | 接了的事负责到完成，结果主动回报，不丢事 | 任务回报、失败说明原因 | ✅ 大部分（缺：worker 反问、bot 自己的承诺清单） |
| **L2 守望** | 按你设的规则定时 / 遇事醒来，没事不出声 | 定时任务、CI 红了提醒、PR 等 review | 🟡 只有定时任务；缺事件源与 heartbeat |
| **L3 预判** | 从状态与历史推断没说出口的需要，提前准备好，再请你决定 | “你下午要评审这个 PR，我先让 reviewer 过了一遍”；早报 | ❌ |
| **L4 持有目标** | 拿着一个目标，自己设计实验、分配资源、周期复盘、调整方向 | 每周 CEO Review：瓶颈在分发，不在功能 | ❌ |
| **L5 自我改进** | 判断方式随结果变好：更懂你、分派更准、预测更准 | “这类活交给 codex 模板更快”“你更喜欢先看结论” | ❌ |

**诚实的能力判断**：在当前模型能力下，L1–L3 可以做得可靠；L4 应当做成**人机协作**——bot 负责状态、拆解、实验设计、复盘和
提醒，现实世界的动作与最终判断由你（以及 worker）完成；L5 从**可审阅的显式状态**起步（记忆、打法本、分派统计），不碰黑盒自改。

---

### 3.4 worker 也可以是人

《提升CEO级认知》里最关键的一步，是把关系倒过来：agent 做决策和实验设计，**人去执行现实世界的动作并回传反馈**
（“Task #172：72 小时内拍 3 条不同 hook 的视频”）。现实目标里，大量关键动作只能由人完成：拍视频、和客户聊、做决定、
线下沟通、审批付款。只能调度 agent 的 bot 会卡在这些环节上。

所以 worker 应该是一个统一的抽象，至少三种实现：

| | agent 会话 | agentflow 流程 | 人 |
|---|---|---|---|
| 派活 | 指令写进会话 | 启动 run（带输入） | 飞书私信 / 卡片：要做什么、为什么、截止、验收标准 |
| 进度 | 读会话 / 任务状态 | run 状态与 metrics | 对方回复；bot 按约定时间询问进度 |
| 需要决定时 | worker 反问 → bot 问主人 | human 节点 → bot 在 IM 里问 | 对方直接问 bot |
| 完成 | 最终消息 + 证据 | 结构化结果 | 对方回报 + 证据（链接、数据、截图） |
| 时间尺度 | 分钟–小时 | 分钟–天 | 小时–周 |
| 跟进方式 | 队列与超时 | 引擎推进 | **提醒**，而不是轮询；有打扰成本 |

人当 worker 有几条和 agent 完全不同的规则，必须由框架硬性保证：

1. **人可以拒绝**：给人的是“请求”，不是命令；可接受、拒绝、改期、转交。对同事派活要尊重对方的角色和意愿，
   默认只能派给主人自己；派给同事需要对方在配置里或首次对话中同意（opt-in），并受主人授权约束。
2. **打扰有社交成本**：催办有频率上限、静默时段，催之前先看对方是否已经回过；措辞代表主人，语气要克制。
3. **人不能被“创建”**：没有模板 / 实例化，只有名册（谁、擅长什么、可用时间、联系渠道、愿意接哪类活）。
4. **隐私对称**：派给同事的任务内容只包含完成任务需要的信息，不泄露主人的其他工作。
5. **验收看证据**：和 agent 一样，完成要附证据；现实反馈（数据、结果）进入观察与实验记录。

这一步把 bot-connect 从“调度 agent 的工具”变成“协调一个人机混合小团队的协作者”，也是 Dots、Hermes 这些
个人 agent 目前都没有做的方向：它们把人当成用户，而我们把人也当成团队成员。

## 4. 架构：把“主动”拆成状态、循环、闸门、学习四层

```
                       ┌──────────────────────── 学习层 ────────────────────────┐
                       │ USER.md / MEMORY.md（有界） · 打法本条目（有用/有害计数）    │
                       │ 分派统计（模板×任务类型→成败/耗时） · 预测误差 → 校准        │
                       │ 一律以“提案 diff”形式写入，主人可批 / 可驳                 │
                       └────────────▲──────────────────────────────┬────────────┘
                                    │ 复盘（后台，只读）              │ 注入（每轮按需）
┌──────── 状态层（外置、结构化）────────┐                           │
│ Goals：目标、指标、约束、复盘节奏         │◄───── 外层循环（周） ─────┤
│ Commitments：bot 答应的事 / 等别人的事   │◄───── 中层循环（天） ─────┤
│ Watches：守望项（条件 + 停止条件 + 预算） │◄───── 守望循环（分钟）────┤
│ Experiments / Decision Journal          │                           │
│ Reality：指标快照（来源 + 时间）          │                           ▼
│ Tasks / Workers / Sessions（已有）        │──────────► 前台对话（大脑每轮拼上下文）
└───────────────────────────────────────┘                           │
                                    ┌──────────── 闸门层 ────────────┐│
                                    │ 后台只读；对外动作需授权          ││
                                    │ 打扰预算 · 静默时段 · 静默 token ││
                                    │ 合并成摘要 · 每次主动都标明原因    │◄┘
                                    │ 工具层硬权限（已有）· 审计（已有） │
                                    └────────────────────────────────┘
```

### 4.1 状态层：解决“上下文越来越多会跑偏”

《提升CEO级认知》里你问的“上下文越来越多，它怎么不偏离目标”，答案就是**不要让上下文承担记忆**：

- 每一轮（包括后台醒来）都是**一个新的、短的上下文**，由状态层按需拼装：目标卡片 + 相关承诺 + 最近观察 + 相关打法条目。
- 长链路的中间产物落到状态（实验记录、观察快照、决策日志），而不是留在会话里。
- 这正是 bot-connect 每轮重新生成 `<bot-connect-context>` 的思路，只需要把“worker 状态”扩展为完整的状态层。

建议的最小数据模型（JSON 文件，和 schedules.json 同级，CLI 可查可改，大脑通过工具读写）：

```text
Goal        { id, title, why, metrics[], constraints[], review: "weekly Mon 09:00", status }
Commitment  { id, what, owner: bot|me|<colleague>, due, source_conv, status, evidence }
Watch       { id, check(自然语言/脚本), every, stop_when, quiet_hours, budget, last_result }
Experiment  { id, goal, hypothesis, prediction, action_plan, assignee, deadline, result, error, lesson }
Observation { metric, value, source, at }   // Reality 的时间序列
```

### 4.2 循环层：三层循环 + 守望

| 循环 | 周期 | 触发 | 做什么 | 停止 / 预算 |
|---|---|---|---|---|
| 任务循环 | 分钟–小时 | 派活 | worker 执行（已有）；agentflow 跑结构化流程 | task_timeout、max_instances（已有） |
| 守望循环 | 分钟 | heartbeat / 事件 | **廉价模型、后台只读会话**检查 Watches：CI、PR、worker 会话闲置且有未提交改动、同事未回复的提问、承诺到期 | 无事回静默 token；每日打扰上限；静默时段 |
| 中层复盘 | 天 | 定时 | 早报 / 晚报：昨天你的 agent 们做成了什么、卡在哪、今天建议做什么；清理承诺 | 一条消息，结论在前 |
| 外层复盘 | 周 | 定时 | **CEO Review**：对照 Goal 的指标，判断瓶颈，结束 / 新开实验，给你（和 worker）派下周的活 | 产出决策日志条目 |

关键原则（来自 OpenClaw / Dots / HEARTBEAT 论文）：

1. **后台与前台分离**：守望和复盘用独立的后台会话，只读工具；结论以“提案”进入前台，避免记忆污染。
2. **安静是默认**：没事不说话（静默 token），有事攒成摘要，紧急的才即时打断。
3. **每次主动都说清楚为什么**：“因为你设了 X 守望 / 因为承诺 Y 今天到期”，并且一键可关。
4. **廉价巡检、按需升级**：巡检用快模型（pi + MiniMax highspeed 已验证 1.5–4 秒），需要真干活才派 worker。

### 4.3 闸门层：主动性越强，边界越要硬

我们已有：角色权限、worker 访问级别、工具层硬检查、审计、模板（大脑不能提权）。新增：

- **动作三档**（借鉴 Dots Custom Rules）：允许 / 需批准 / 禁止；后台循环默认只能“读 + 起草 + 提案”，对外发消息、改代码、给同事派活需批准，
  批准以飞书卡片一键完成。
- **打扰预算**：每人每天主动消息上限、静默时段、合并策略；统计“主动消息被采纳率”，低了就自动收敛。
- **记忆来源标记**：每条记忆 / 打法条目记录来源（哪次对话、哪个后台观察），后台来源的条目需更高门槛才能进长期记忆。

### 4.4 学习层：可审阅的自进化

分三类学习对象，全部以**显式、可读、可回滚**的形式存在：

1. **关于你**（USER.md，有上限）：偏好、汇报风格、作息、什么事该打扰你。
2. **关于做事**（打法本条目，ACE 式增量 + 有用 / 有害计数）：例如“bot-connect 的发版先跑 make check”“这类重构交给 codex 模板”。
   加上**分派统计**：模板 × 任务类型 → 成功率、耗时、返工次数，用于路由。
3. **关于判断**（决策日志的预测误差）：它预测“这个视频 1 万播放”，实际 12 万——误差和教训进入下一轮复盘的上下文。

写入机制借鉴 Hermes：回合 / 任务结束后后台复盘提出修改 → 以 diff 发给主人（或在早报里汇总）→ 批准才生效。
这样“自进化”不会变成不可控的漂移，也能直接回答“它是不是越来越懂我”。

### 4.5 和 agentflow 的关系

- **bot = 判断与关系**（持有目标、和人沟通、决定做什么）；**agentflow = 可重复流程的执行引擎**（确定性推进、结构化结果、
  人类审批节点、metrics）；**worker = 具体干活的 agent 会话**。
- 自然的集成方式：bot 把 agentflow run 当作一种 worker 来派发；agentflow 的 **human 节点**由 bot 在飞书里问人、收集回答再推进；
  run 的结构化结果和 metrics 回流成 Observation，进入复盘。
- 实验（Experiment）落地为 agentflow 工作流：例如“写 3 个视频脚本（agent）→ 你拍（human）→ 拉取数据（shell）→ 复盘（agent）”。
  这正是 CEO 对话里 Task #172 的可执行版本。

---

### 4.6 框架能力清单（每项都是“工具 + 状态 + skill + 硬检查”的组合）

| 框架能力 | 硬的部分（框架） | 软的部分（给大脑的工具 / skill / prompt） | 现状 |
|---|---|---|---|
| **worker 管理** | 名册、模板、实例上限、队列、隔离、回收；人类 worker 的同意与催办上限 | 路由规则、派活 skill（写清目标 / 背景 / 完成标准）、验收 skill | agent worker ✅ 模板 ✅；人类 worker ❌；agentflow worker ❌ |
| **目标管理** | Goal / Experiment / Observation 存储与 schema；复盘定时唤醒 | CEO Review skill、实验设计 skill、宪章注入 | ❌ |
| **承诺与跟进** | Commitment 存储、到期唤醒、未完成升级 | “说了要做就登记”的协议规则 + 工具 | ❌（只有任务回报） |
| **唤醒与长周期** | 定时、heartbeat、事件源、后台会话隔离、预算 / 静默时段 | 守望清单（自然语言）、静默 token 约定 | 定时 ✅；其余 ❌ |
| **自我迭代** | 记忆 / 打法本存储、上限、来源标记、审批流、跨大脑注入（agentskills 格式） | 复盘后提出修改的 skill；打法条目的使用与反馈 | ❌ |
| **权限与安全边界** | 角色 × 访问级别 × 工具层检查、动作三档、审计归因 | 何时请示的规则说明 | 角色 / 访问级别 / 审计 ✅；动作三档 ❌ |

注意“跨大脑注入”：Claude Code、Codex、pi 都支持 skill / 上下文文件，但格式和加载方式不同。框架应**持有** skill 与记忆
（统一用 agentskills.io 格式），在每次调用时按大脑的方式注入，这样换大脑不丢积累。

## 5. 路线图建议

### 阶段 A：信任地基（1–2 周）——让人敢把事交出去

1. **秒级回执**：消息到达即由框架加飞书表情回应（不经过大脑）；处理中状态可见。
2. **快速失败 + 备用模型**：模型故障 / 额度用尽时 10 秒内用人话说明，并切换 fallback provider（今天 5 分钟沉默就是反例）。
3. **worker 反问**：worker 遇到需要决定的事 → 任务进入 `waiting_input` → bot 在会话里问人 → 回答回传继续。
4. **人类 worker（先从主人自己开始）**：bot 可以把活派给你，带截止与验收标准，到期跟进；之后再扩展到同意接活的同事。
5. **承诺清单（Commitments）**：bot 说“完成后告诉你 / 明天提醒你 / 我去问一下张三”时，必须落成承诺条目，到期未完成自动跟进。
6. **主动行为可归因**：每条非回复消息带“为什么发”的来源标签；审计里区分 user / schedule / watch / review 触发。

### 阶段 B：守望与预判（L2→L3，2–3 周）

1. **Watches + heartbeat**：后台只读会话、廉价模型、静默 token、预算与静默时段；首批信号源全部来自我们独有的视角：
   - worker 会话：长时间闲置但有未提交改动、任务失败未处理、本地会话出现报错；
   - GitHub：自己的 PR 被 review / CI 失败 / issue 被 @（经 `gh`）；
   - 飞书：同事在群里提到你但你没回、发给 bot 的留言积压；
   - 承诺到期。
2. **早报 / 晚报**：你的 agent 劳动力今日战报 + 待你决定的事 + 建议的下一步（这是 L3 最便宜的落地方式）。
3. **动作三档 + 飞书卡片审批**：后台提案 → 一键批准 / 驳回。
4. **度量**：回执延迟、主动消息采纳率、被主人追问“怎么样了”的次数（应趋近 0）。

### 阶段 C：持有目标（L4，人机协作版，3–4 周）

1. **Goals + Reality**：先只支持一个目标领域，选**反馈可量化、周期短**的——例如你的开源项目增长（stars、issue、安装量、
   npm 下载）或内容实验（播放、完播、涨粉），与 CEO 对话的建议一致。
2. **每周 CEO Review**：对照指标找瓶颈 → 关闭 / 新开实验 → 给你和 worker 派下周的活（人也是执行节点，有截止和跟进）。
3. **决策日志**：每个决策必须写预测；复盘时计算误差并写教训。
4. **agentflow 集成**：实验用工作流执行，human 节点走飞书。
5. **宪章**：目标函数与约束（健康、家庭、信誉、法律、不可接受的财务风险）写入配置，bot 引用它来说“不建议”。

### 阶段 D：可审阅的自进化（L5 起步，持续）

1. 有界的 USER.md / MEMORY.md + 回合后后台复盘 → diff 提案 → 主人批准。
2. 打法本条目（有用 / 有害计数，增量更新，防坍缩）。
3. 分派统计驱动模板路由；预测误差驱动复盘提示。
4. **验证方法**（呼应 CEO 对话的 “benchmark” 思路）：跑 3 个月，看三条曲线——主人纠正次数是否下降、主动消息采纳率是否上升、
   决策预测误差是否收敛。三条都向好，才说明“它越来越懂你”是真的。

---

## 6. 风险与反模式

| 风险 | 表现 | 对策 |
|---|---|---|
| 打扰过度 | 主动变成噪音，用户关掉 | 静默默认、预算、摘要合并、采纳率自动收敛 |
| 记忆污染 | 后台读到的错误信息改变前台行为 | 前后台会话分离、记忆来源标记、后台写入需审批 |
| 宣称能力过度 | “帮你赚钱”式承诺失败，信任崩塌 | 用阶梯说话：L4 是协作，不是自治；所有判断带证据与预测 |
| 成本失控 | heartbeat × 多 bot × 大模型 | 廉价模型巡检、空清单零调用、每日 token 预算 |
| 目标函数偏移 | 优化成短期指标机器（CEO 对话的警告） | 宪章约束 + 多指标 + 每周人类复核 |
| 越权 | 主动行动触达同事、外部系统 | 工具层硬权限（已有）、动作三档、对外动作必须批准 |
| 不可解释 | 不知道它为什么突然说话 / 做事 | 每次主动标明触发原因，活动视图（审计 + `bot-connect activity`） |

---

## 7. 下一步建议

先做 **阶段 A**（秒级回执、快速失败 + 备用模型、worker 反问、人类 worker、承诺清单）。它们改动小、体感强，并且是之后一切主动性
的前提：**一个会主动找你的协作者，首先得是一个你敢把事交给它的协作者。** 阶段 A 完成后进入 Watches + 早报，再用一个
可量化目标（建议：bot-connect 自己的开源增长）跑第一轮 CEO Review 实验。

---

## 参考

- 《提升CEO级认知》对话记录（本地文档）
- Hermes Agent：[GitHub](https://github.com/nousresearch/hermes-agent) · [Docs](https://hermes-agent.nousresearch.com/docs/) ·
  [Memory](https://hermes-agent.nousresearch.com/docs/user-guide/features/memory) ·
  [Skills](https://hermes-agent.nousresearch.com/docs/user-guide/features/skills) ·
  [Bot Mode](https://hermes-agent.nousresearch.com/docs/user-guide/bot-mode) ·
  [TechCrunch 融资](https://techcrunch.com/2026/07/13/hermes-agent-maker-nous-research-in-talks-for-new-funding-at-1-5b-valuation/) ·
  [TWiT 访谈](https://twit.tv/posts/tech/what-makes-hermes-agent-stand-out-open-source-innovation-nous-researchs-jeffrey) ·
  [评测（偏商业立场）](https://www.eesel.ai/blog/hermes-agent-review)
- OpenClaw：[Heartbeat 文档](https://openclaw-ai.com/en/docs/gateway/heartbeat) ·
  [Four Things OpenClaw Got Right](https://deadneurons.substack.com/p/four-things-openclaw-got-right) ·
  [Steinberger 加入 OpenAI](https://www.theregister.com/2026/02/16/open_ai_grabs_openclaw) ·
  [Fortune](https://fortune.com/2026/02/19/openclaw-who-is-peter-steinberger-openai-sam-altman-anthropic-moltbook)
- OpenAI Dots：[Introducing dots](https://openai.com/index/introducing-dots/) ·
  [调用 Codex / ChatGPT Work 的报道](https://focus.hidubai.com/openai-launches-dots-ai-agent-that-works-on-tasks-for-users-over-long-periods/) ·
  [Symphony：看板驱动 Codex](https://openai.com/index/open-source-codex-orchestration-symphony) ·
  [VentureBeat](https://venturebeat.com/technology/openai-launches-dots-always-on-ai-agent-coworkers-and-chatgpt-space-where-they-can-collaborate-with-human-teams) ·
  [控制边界分析](https://opentools.ai/news/openai-dots-always-on-agents-launch-availability-limits)
- Meta Muse：[Axios](https://www.axios.com/2026/09/08/meta-debuts-muse-personal-ai-agent) ·
  [TechCrunch](https://techcrunch.com/2026/09/23/everything-new-coming-to-metas-ai-agent-muse/)
- 主动性研究：[CHIIR 2026 workshop 报告](https://arxiv.org/abs/2608.18638) · [π-Bench](https://arxiv.org/abs/2605.14678) ·
  [Mind Your HEARTBEAT!](https://arxiv.org/abs/2603.23064) · [自主系统治理](https://arxiv.org/pdf/2603.07191) ·
  [Always-On Agents 综述](https://arxiv.org/pdf/2606.30306) · [OpenHands standing intents](https://github.com/OpenHands/OpenHands/issues/18054)
- Loop engineering：[IBM](https://www.ibm.com/think/topics/loop-engineering) ·
  [ADTmag](https://adtmag.com/articles/2026/07/01/loop-engineering-emerges-as-developers-put-ai-coding-agents-on-repeat.aspx) ·
  [Loop Engineering 论文](https://arxiv.org/pdf/2608.21884) ·
  [Andrew Ng 三层循环解读](https://explainx.ai/blog/andrew-ng-three-loops-0-to-1-products-2026)
- 自进化：[ACE](https://arxiv.org/html/2510.04618v1) · [SkillRL](https://huggingface.co/papers/2602.08234) ·
  [SkillOS](https://huggingface.co/papers/2605.06614) · [Dynamic Agent Skills 综述](https://arxiv.org/pdf/2607.10113) ·
  [Experience Funnel](https://arxiv.org/abs/2609.08919)
- 能力边界：[METR Time Horizons](https://metr.org/time-horizons/) · [METR（Wikipedia）](https://en.wikipedia.org/wiki/METR)

> 注：部分产品细节来自媒体与第三方博客（已在正文标注倾向），数字以官方页面为准；论文多为摘要级阅读，引用具体数字前应核对原文。
