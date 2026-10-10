# bot 当 PM：目标、任务、风险、人员触达

> 原则见 [architecture.md](architecture.md)：不碰 agent loop；bot 的“PM 能力”= 脚手架提供的状态与硬规则 + 交给大脑的
> 工具与 skill。代码接口：`internal/workforce`（worker 协议）、`internal/plan`（目标 / 任务 / 风险）、`internal/reach`（触达）。

## 1. 角色

bot 像一个 PM：接需求、判断可行性、拆任务、找合适的 worker（agent、流程、人、别的 bot）、盯进度、**提前暴露风险**、
验收、对目标负责、定期复盘。分工：

| PM 职责 | 大脑（判断，软） | 脚手架（保证，硬） |
|---|---|---|
| 接需求 | 澄清目标与完成标准，**评估可行性与工期**，有风险当场说 | 记录任务、截止、预估 |
| 拆解与分派 | 拆成任务、选 worker、写清 brief | 只允许派给可达、已授权、有容量的 worker；外部 worker 只收最小信息 |
| 跟进 | 看进度、回答 worker 的问题、决定是否换人 | 跟踪任务生命周期；人按规矩提醒（工作时间、上限、间隔） |
| 风险 | 给出应对选项，告诉需求方，必要时升级主人 | **规则检测**进度风险并唤醒大脑；高风险未被处理时升级 |
| 验收 | 按完成标准验收、要求返工 | 交付 ≠ 完成；返工计入能力记录 |
| 目标 | 复盘指标、调整任务与优先级 | 定时复盘唤醒、指标存储 |

## 2. 对象模型

```
Goal（结果，有指标、约束、复盘节奏）
 └── Task（交付物：完成标准、截止、预估、依赖、优先级）
       └── Assignment（这件活交给某个 worker 的一次委托：发出 → 接受/拒绝/还价 → 进行 ⇄ 卡住 → 交付 → 验收）
             └── Worker（档案 + 多维状态；agent 会话 / agentflow / 人 / bot …）
Risk（挂在 Task 或 Goal 上：类型、等级、应对选项、状态）
Person（人员名册：身份、技能、工作时间、触达路线、同意接什么活）
```

- Task 管“做什么、何时要”，Assignment 管“谁在做、做到哪”。一个 Task 被拒或换人，就有多个 Assignment。
- 原来的 `worker.Task`（agent 队列里的一次执行）是 agent worker 内部的事，通过 `worker.AgentWorker` 适配成 Assignment。

## 3. 风险意识

风险有两个来源，缺一不可：

**① 接活时的判断（大脑，软）。** 协议规则 + skill：接到有截止的需求，先估工期与可行性；若“按现有资源做不完 / 做不到 /
需求有矛盾”，**当场说出来并给选项**（砍范围、延期、加人、换做法），而不是先答应。用 `risk_raise` 记录。

**② 过程中的检测（脚手架，硬）。** `plan.Assess` 按规则扫描所有未完成任务：

| 规则 | 条件 | 等级 |
|---|---|---|
| overdue | 过了截止仍未完成 | 高 |
| will_miss | 现在 + 剩余工作量（预估 × 该 worker 的历史拖延系数 − 已用时间）晚于截止 | 高 |
| thin_slack | 能按时完成，但余量不足预估的 1/4 | 中 |
| unassigned | 没人接，且离截止不到预估的 1.5 倍（已来不及则为高） | 中 / 高 |
| silent | 进行中，但超过 max(预估/4, 2 小时) 没有进展信号 | 中 |
| stuck | 阻塞超过 4 小时 | 中 |
| late_dep | 依赖项预计完成太晚，留给本任务的时间不够 | 高 |

检测到新风险（或等级上升）→ 以“[系统事件·风险]”唤醒大脑（和任务回报同一机制），附上规则、数据和建议选项。
大脑必须告诉需求方：**发生了什么、为什么、有哪些选项、它建议哪个**。高风险在一定时间内没有被处理（接受 / 缓解）→
升级给主人。同一风险不重复打扰，只在等级变化或状态变化时再提。

“拖延系数”来自 worker 的能力记录（实际 / 预估），所以风险判断会随合作历史变准——这是第一个“自我迭代”的落地点。

## 4. 人员触达

### 4.1 名册与路线

人当 worker，需要知道**怎么找到他**、**什么时候可以找**、**他同意接什么**。配置示例：

```toml
[[people]]
id = "zhangsan"
name = "张三"
identities = ["feishu:ou_xxx", "email:zhangsan@corp.com"]   # 用来认出他发来的消息
skills = ["设计稿", "前端评审"]
timezone = "Asia/Shanghai"
work_hours = [{ start = "10:00", end = "19:00", days = [1, 2, 3, 4, 5] }]
quiet_hours = [{ start = "12:00", end = "13:30" }]
max_nudges_per_day = 2
min_nudge_interval = "3h"
accept_from = ["owner"]        # 谁能通过 bot 给他派活
consent = true                 # 本人已同意接 bot 派的活（没同意只能发“请求”，不能记为他的任务）

[[people.contact]]             # 按升级顺序
channel = "feishu_dm"
address = "ou_xxx"

[[people.contact]]
channel = "feishu_urgent_app"  # 飞书应用内加急
address = "ou_xxx"
min_urgency = "urgent"

[[people.contact]]
channel = "feishu_urgent_phone" # 飞书电话加急（占租户额度）
address = "ou_xxx"
min_urgency = "critical"
approval = true                 # 每次使用都要主人批准
```

主人自己是一个隐含的 person：路线默认是和 bot 的私聊。

### 4.2 选路规则（`reach.Plan` + `Dispatcher`，硬）

- 每条消息有紧急度：normal（派活、回答）/ reminder（提醒）/ urgent（逾期、阻塞别人）/ critical（必须现在找到人）。
- 只用 `min_urgency` ≤ 消息紧急度的路线；从最升级的可用路线开始尝试，失败就往下退。
- 工作时间外、静默时段内只发 critical。
- `approval = true` 的路线（打电话、短信）每次都要主人批准。
- 提醒受 `max_nudges_per_day` / `min_nudge_interval` 限制；超了就**升级给主人**而不是继续催。

### 4.3 渠道

| 渠道 | 实现方式 | 备注 |
|---|---|---|
| feishu_dm | 现有平台层（SDK / lark-cli）发私聊 | 首个实现 |
| feishu_urgent_app / sms / phone | 对已发出的消息调用飞书“加急”接口 | 需要应用开通相应权限；短信 / 电话消耗租户额度，接入前需核实权限与费用 |
| email / sms / phone | 外部服务（SMTP、短信 / 语音服务商） | 按需接入 |
| a2a / webhook | 别的 bot 或系统 | bot 当 worker 时使用 |

### 4.4 人怎么回话

不需要新界面：被派活的人直接在飞书里回复 bot（私聊或卡片按钮）。

- hub 认出这个人（名册的 `identities`），上下文块里列出**他名下的未完成任务**；
- 大脑用 `assignment_respond` 替他登记：接受 / 拒绝 / 还价 / 进度 / 提问 / 交付。这个工具对被派活的人开放，但**只能操作他自己的任务**；
- 他看不到主人的其他工作，只看到派给他的 brief。

## 5. 给大脑的工具（规划）

| 工具 | 谁能用 | 作用 |
|---|---|---|
| `goal_create` / `goal_update` / `goal_list` | owner / admin | 目标、指标、约束、复盘节奏 |
| `task_create` / `task_update` / `task_list` | owner / admin | 任务、截止、预估、依赖、优先级 |
| `assign` | owner / admin | 把任务派给某个 worker（发出 brief），返回 assignment |
| `assignment_update` | owner / admin | 验收 / 要求返工 / 取消 / 改截止 / 回答问题 |
| `assignment_respond` | 被派活的人（只限自己的） | 接受 / 拒绝 / 还价 / 进度 / 提问 / 交付 |
| `workforce_list` | owner / admin | 所有 worker 的档案 + 一句话状态，供选人 |
| `worker_note` | owner / admin | 记录一条推断状态（如“他这周很忙”），自动过期 |
| `reach_person` | owner / admin | 按紧急度联系某人（走选路规则与审批） |
| `risk_raise` / `risk_update` / `risk_list` | owner / admin | 记录、接受、缓解、关闭风险 |
| `status_report` | owner / admin | 生成项目 / 目标的现状（进度、风险、下一步） |

已有的 `delegate`、`worker_create` 保留：`delegate` 相当于“建一个无截止的任务并派给 agent worker”的快捷方式。

## 6. 落地计划

每个里程碑都能独立用起来，并且有验收标准。

**M0 接口（本次完成）**
`workforce`：档案、多维状态（来源 / 置信度 / 有效期）、Assignment 生命周期（与 A2A 对应）、Worker / Events 接口；
`plan`：目标、任务、风险、规则检测；`reach`：路线、紧急度、时间窗、选路、分发；`worker.AgentWorker`：现有 agent worker 适配。

**M1 任务与风险（agent worker）**
- 存储：plan / assignment 的 JSON store（与 schedules 同模式，CLI 可查）；
- 工具：`task_*`、`assign`、`assignment_update`、`risk_*`、`workforce_list`；
- 控制循环：每分钟跑 `plan.Assess`，新风险以系统事件唤醒大脑；高风险未处理升级主人；
- 协议：接活先评估可行性；收到风险事件必须告知需求方并给选项；
- CLI：`task list|get`、`risk list`、`assignment list`；
- 验收：建一个带截止的任务派给 agent，模拟拖慢 → bot 在截止前主动提出风险并给选项。

**M2 人类 worker（先主人自己，再同事）**
- 配置 `[[people]]` + 主人隐含 person；`reach` 的 feishu_dm 实现；
- `assignment_respond`（被派活的人在飞书里回复即可）；提醒与升级（工作时间、静默、上限）；
- 同事需要 `consent`；发给同事的只有 brief；
- 验收：bot 给你派“今天 18 点前确认设计稿”，到点没回 → 温和提醒 → 再到点升级 / 报风险。

**M3 加急与更多渠道**
- 飞书加急（应用内 / 短信 / 电话），需审批的路线走飞书卡片批准；
- email；按需接短信 / 语音服务商；
- 验收：critical 风险在静默时段外通过电话加急找到人，且每次都有主人批准记录。

**M4 目标与复盘**
- `goal_*`，指标更新（手动 / 由 worker 回报 / 外部数据源）；
- 定时复盘：对照指标 → 风险与瓶颈 → 调整任务；决策记录带预测，复盘时算误差；
- 验收：一个真实目标（例如 bot-connect 的开源增长）跑满两轮周复盘。

**M5 更多 worker 实现**
- agentflow worker（human 节点走 bot 问人）；bot 作为 worker（A2A / IM）；第三方 bot 按其开放能力接入；
- worker 反问（agent 的 input_required）；
- 验收：一个任务由 agent、人、agentflow 混合完成，状态在同一张任务表里。
