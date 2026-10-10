# 领域设计（DDD）：限界上下文、聚合、接口与规则

> 依据 [foundation.md](foundation.md) 的产品设计，给出代码层面的结构。目标：职责清晰、高内聚低耦合、
> 扩展点都是接口、规则是可测试的纯函数。原则仍是：不碰 agent loop；大脑、worker、渠道、存储、判断模型都可插拔。

---

## 1. 总体结构：六边形（端口与适配器）

```
                ┌──────────────────────────── adapters（可替换） ────────────────────────────┐
  飞书 / console │ 渠道        存储(JSON/SQLite/飞书多维表格)   大脑(L2/L3 agent)   判断(Jev/LLM/规则) │
  agent / 人 / bot│ worker 驱动  触达(私聊/加急/邮件)          时钟(真实/虚拟)     看板同步          │
                └──────────────▲──────────────────────────────▲──────────────────────────────┘
                               │ 实现                          │ 实现
                ┌──────────────┴──────────── ports（接口） ─────┴──────────────────────────────┐
                │ Repository · WorkerDriver · Channel · Reacher · Brain · Judge · Clock · EventBus │
                └──────────────▲──────────────────────────────────────────────────────────────┘
                               │ 依赖
                ┌──────────────┴──────────── application（用例） ─────────────────────────────┐
                │ 命令处理（派活、回复、验收、立项…）· 唤醒编排 · 简报组装 · 权限守卫 · 事务        │
                └──────────────▲──────────────────────────────────────────────────────────────┘
                               │ 依赖
                ┌──────────────┴──────────── domain（纯逻辑，无 IO） ──────────────────────────┐
                │ 聚合 · 值对象 · 领域事件 · 规则（检测器、打分、进度、找人）· 领域服务             │
                └─────────────────────────────────────────────────────────────────────────────┘
```

依赖只向内：domain 不依赖任何外部包（只依赖标准库）；application 依赖 domain 与 ports；adapters 实现 ports。
这样换存储、换大脑、换判断模型、加 worker 类型，都只动 adapters。

---

## 2. 限界上下文

| 上下文 | 负责 | 核心聚合 / 对象 | 对外提供 |
|---|---|---|---|
| **Workforce（人力）** | 谁能干活：档案、联系方式、规矩、状态 | `Worker`、`WorkerState` | 查询人力、记录状态事实 |
| **Portfolio（项目）** | 项目、成员与角色、节奏、项目卡 | `Project` | 立项、成员变动、节奏 |
| **Planning（计划）** | 事项、层级、依赖、截止、完成标准 | `Item` | 拆解、改期、完成 |
| **Delegation（委托）** | 把事交给人力的约定及其生命周期 | `Assignment` | 派活、回复、验收 |
| **Attention（注意力）** | 什么需要处理、多急、处理结果 | `Signal`（派生）、`Resolution` | 议程、推迟、处理记录 |
| **Insight（洞察）** | 进度、健康度、预测、速度统计、找人 | 纯计算（无聚合） | 项目状态、候选人 |
| **Cognition（认知）** | 唤醒编排、分层路由、简报、提示与工具集 | `Wake`、`Briefing` | 处理一次唤醒 |
| **Communication（沟通）** | 会话、渠道、触达路线 | `Conversation`（已有 hub）、`Route` | 发消息、找到人 |
| **Identity & Access（身份与权限）** | 用户身份、角色、命令授权 | `User`、`Policy` | 谁能做什么 |

上下文之间**只通过 ID 引用和领域事件**协作，不直接互相调用对方的聚合方法。

共享内核（`domain/shared`）只放最小公共物：强类型 ID、`Actor`、`Clock`、`DomainEvent` 信封、`Period`、`Priority`。

---

## 3. 共享内核

```go
package shared

type (
    WorkerID     string // "zhangsan", "cc-connect", "owner"
    ProjectID    string // "P1"; 根项目 = "org"
    ItemID       string // "I12"
    AssignmentID string // "A7"
    BotID        string
)

// Actor 是执行命令的一方：一个人（通过某个 bot）、bot 自身的某一层大脑、或系统规则。
type Actor struct {
    UserID string    // 平台身份（人发起时）
    Worker WorkerID  // 如果他同时是 worker
    Role   Role      // owner | admin | member | visitor | system
    Via    string    // "chat" | "brain:L2" | "brain:L3" | "rule:overdue" | "cli"
}

type Clock interface{ Now() time.Time } // 真实时钟 / 评测用的虚拟时钟

type Period struct{ From, Until *time.Time }       // 半开区间；nil 表示不限
func (p Period) Contains(t time.Time) bool

type Priority int // P0..P3
func (p Priority) Weight() float64                 // P0=8, P1=4, P2=2, P3=1

// DomainEvent 由聚合产生，由应用层持久化后发布。
type DomainEvent interface {
    Name() string                // "assignment.delivered"
    Aggregate() string           // "assignment:A7"
    OccurredAt() time.Time
    Actor() Actor
}
```

---

## 4. 各上下文的聚合、值对象与规则

### 4.1 Workforce（人力）

```go
package workforce

type Kind string  // agent_session | agentflow | human | bot | a2a | command
type Trust string // controlled | external

// Worker：聚合根。相对稳定的档案。
type Worker struct {
    ID          WorkerID
    Name        string
    Kind        Kind
    Trust       Trust
    Description string
    Skills      []string
    Principal   string        // owner | self | 对方主人
    Interaction Interaction   // 能否续上下文 / 反问 / 拒绝 / 还价 / 主动报进度
    Contact     []Route       // 触达路线（升级顺序），值对象
    Norms       Norms         // 工作时间、静默时段、提醒上限、接谁的活
    Consent     Consent       // 同意接哪类活、来自谁、到何时
    Driver      DriverRef     // 由哪个 WorkerDriver 驱动（kind + 配置键）

    // 能做什么（见第 7 节“找人的边界”）
    Capabilities []Capability // agent：工具、可访问的系统与权限级别、工作目录；人：会用的系统
    Authority    []Authority  // 人：能批准什么（合并到主干、生产发布、花钱 ≤ X…）
    Physical     bool         // 人：能做现实世界的动作（打电话、到场、签字、拍摄…）
}

type Capability struct {
    System string // "repo:tapnow" | "gcloud-logging" | "feishu-docs" | "browser" …
    Access string // read | write | admin
    Note   string
}
type Authority struct {
    Action string // 与审批规则里的 Action 对应，如 "merge:main"、"deploy:prod"、"spend"
    Scope  string // 适用范围，如 "repo:tapnow"、"≤ 1000 CNY"
}

// 规则方法（纯函数）
func (w Worker) AcceptsFrom(a Actor) bool
func (w Worker) CanReceiveWork() bool                       // 有同意（人）或受控（agent）
func (n Norms) Allows(u Urgency, now time.Time) error       // 工作时间 / 静默时段
func (n Norms) NudgeAllowed(history []time.Time, now time.Time) bool

// WorkerState：独立聚合（高频变化，和档案分开存，避免争用）。
type WorkerState struct {
    Worker WorkerID
    Facts  []Fact // 维度 × 来源（声明 / 观测 / 推断），带置信度与有效期
}
func (s *WorkerState) Record(f Fact) []DomainEvent          // 推断类自动加有效期上限
func (s WorkerState) Get(d Dim, now time.Time) (Fact, bool) // 声明 > 观测 > 推断，取最新
func (s WorkerState) Available(now time.Time) (bool, string)
```

**不变量**：推断事实最长 24 小时、置信度 ≤ 0.8；同一维度同一来源只保留最新。

### 4.2 Portfolio（项目）

```go
package portfolio

type Status string // proposed | active | paused | done | cancelled

// Project：聚合根。成员与项目卡属于它（数量小、随项目一起变）。
type Project struct {
    ID        ProjectID
    Parent    ProjectID         // 根项目 "org" 无 parent
    Title     string
    Objective Objective         // 值对象：目标描述 + 衡量指标
    Priority  Priority
    Timebox   Period
    Status    Status
    Members   []Membership      // 实体（在聚合内）
    Cadence   []Cadence         // 值对象：站会 / 周复盘 / 里程碑回顾…
    Card      Card              // 值对象：范围、决策、约定、链接（有字数上限）
    Home      ConversationRef   // 项目消息默认发到哪里
    Policies  []ApprovalRule    // 审批规则；子项目继承上级（根项目 = 公司级规则）
}

// ApprovalRule：哪些动作必须由人批准。由代码在执行动作时强制检查，不靠大脑自觉。
type ApprovalRule struct {
    Action    string     // "merge:main" | "deploy:prod" | "publish:external" | "spend" | "grant:access" | "delete:data" …
    Scope     string     // 适用范围（仓库、金额上限…）
    Approvers []string   // 角色（如 "code_owner"、"oncall"）或 worker id
    Quorum    int        // 默认 1
}

type Membership struct {
    Worker WorkerID
    Role   string     // “设计负责人”
    Duties []string   // “首页”“视觉验收”
    Period Period
}

type Cadence struct {
    Kind  string // standup | weekly_review | milestone_review | custom
    Spec  string // 日程表达式（复用 schedule 的语法）
    Layer string // 默认用哪层大脑（L2 / L3）
}

// 命令方法（改状态 + 产生事件，校验不变量）
func (p *Project) AddMember(m Membership, by Actor) ([]DomainEvent, error)
func (p *Project) EndMembership(w WorkerID, role string, at time.Time, by Actor) ([]DomainEvent, error)
func (p *Project) SetPriority(pr Priority, by Actor) []DomainEvent
func (p *Project) UpdateCard(delta CardDelta, by Actor) ([]DomainEvent, error) // 增量修改，超上限报错
func (p *Project) Transition(to Status, by Actor) ([]DomainEvent, error)

// 规则方法
func (p Project) ActiveMembers(now time.Time) []Membership
func (p Project) Covers(duty string, now time.Time) []Membership // 职责匹配（精确 / 包含；语义匹配交给 Insight 的 Matcher）
```

**不变量**：同一 worker 同一角色的有效期不重叠；根项目不能被取消；项目卡不超过上限。

### 4.3 Planning（计划）

```go
package planning

type Status string // todo | active | blocked | done | dropped
type Kind string   // task | milestone

// Item：聚合根。每个事项独立（数量多、各自变化）。
type Item struct {
    ID         ItemID
    Project    ProjectID
    Parent     ItemID        // 子事项
    Kind       Kind
    Title      string
    Acceptance []Criterion   // 值对象：完成标准逐条列出，验收时逐条核对
    Due        *time.Time
    Estimate   time.Duration
    Priority   *Priority     // 为空则继承项目
    DependsOn  []ItemID
    Status     Status
    Owner      WorkerID      // 当前负责的人力（由委托决定）
    Assignment AssignmentID  // 当前有效委托
    Blocked    *Blocker      // 值对象：卡在什么、从何时起
    Signals    Activity      // 值对象：开始时间、最近进展时间
    Needs      []Need        // 做这件事需要什么：能力（系统 / 权限）、审批、决定、现实动作…（见第 7 节）
}

type Need struct {
    Kind   NeedKind // capability | approval | decision | physical | relationship | judgment | knowledge
    Detail string   // “gcloud-logging:read”、“deploy:prod”、“选 A 还是 B（涉及成本）”…
}

func (i *Item) Reschedule(due time.Time, by Actor) []DomainEvent
func (i *Item) Attach(a AssignmentID, w WorkerID) []DomainEvent
func (i *Item) Detach(reason string) []DomainEvent            // 委托结束且未完成 → 回到 todo
func (i *Item) Block(b Blocker) []DomainEvent
func (i *Item) Complete(by Actor) ([]DomainEvent, error)      // 只有已验收的委托或主人可以完成
func (i *Item) Drop(by Actor, reason string) []DomainEvent

func (i Item) Open() bool
func (i Item) EffectivePriority(p Project) Priority
```

**跨聚合不变量由领域服务保证**：

```go
// DependencyGraph：依赖无环、跨项目依赖可见性。
type DependencyGraph interface {
    Validate(item ItemID, deps []ItemID) error
}
```

### 4.4 Delegation（委托）

```go
package delegation

// Assignment：聚合根。状态机与历史（已实现于 workforce.Assignment，迁入此处）。
type Assignment struct {
    ID        AssignmentID
    Item      ItemID
    Worker    WorkerID
    Kind      AskKind        // work | approval | decision | clarification | action | review
    Why       NeedKind       // 交给人时必填：为什么必须是人（见第 7 节）；交给 agent 时为空
    Options   []string       // decision / approval 类：给对方的选项，便于一键回复
    Requester Actor
    Brief     Brief          // 值对象：目标、背景、完成标准、证据要求、截止、预估、优先级
    Status    Status         // offered … verified / declined / failed / cancelled / released / expired
    Ref       string         // worker 侧编号（agent 任务号等）
    History   []Step
}

// 每个动作是一个方法，内部走同一张状态转换表，非法转换返回错误。
func (a *Assignment) Accept(eta *time.Time, by Actor) ([]DomainEvent, error)
func (a *Assignment) Decline(reason string, by Actor) ([]DomainEvent, error)
func (a *Assignment) Counter(due time.Time, note string, by Actor) ([]DomainEvent, error)
func (a *Assignment) Ask(q string, by Actor) ([]DomainEvent, error)
func (a *Assignment) Answer(text string, by Actor) ([]DomainEvent, error)
func (a *Assignment) Progress(note string, eta *time.Time, by Actor) ([]DomainEvent, error)
func (a *Assignment) Deliver(result string, evidence []string, by Actor) ([]DomainEvent, error)
func (a *Assignment) Verify(by Actor) ([]DomainEvent, error)
func (a *Assignment) Revise(note string, by Actor) ([]DomainEvent, error)
func (a *Assignment) Cancel / Release / Fail / Expire(...)
func (a *Assignment) Nudged(at time.Time) []DomainEvent

func (a Assignment) Open() bool
func (a Assignment) LastSignOfLife() time.Time
func (a Assignment) NudgesSince(t time.Time) []time.Time
```

**不变量**：状态转换合法；只有被委托方能接受 / 拒绝 / 还价 / 交付；只有请求方（或主人）能验收 / 返工 / 取消；
一个 Item 同时最多一个未结束的委托（由应用层查询仓库保证）。

### 4.5 Attention（注意力）

议程不是存下来的聚合，而是**每次从世界快照计算出来的读模型**；只有“处理结果”需要持久化。

```go
package attention

// World：一次计算用的只读快照，由应用层从各仓库组装。规则只读它，不碰 IO。
type World struct {
    Now         time.Time
    Projects    map[ProjectID]portfolio.Project
    Items       map[ItemID]planning.Item
    Assignments map[AssignmentID]delegation.Assignment
    Workers     map[WorkerID]workforce.Worker
    States      map[WorkerID]workforce.WorkerState
    Pace        insight.Pace          // 各 worker 的历史速度
    Waiting     []Waiting             // 有人在等 bot：未答的问题、待验收的交付、待回复的还价、未回的消息
}

// Signal：一件需要注意的事。Key 稳定（同一件事每次计算得到同一个 Key），用于去重与处理记录。
type Signal struct {
    Key      string        // "will_miss:I7"
    Kind     string        // overdue | will_miss | waiting_on_us | unassigned | silent | stuck | overload | cadence_due …
    Subject  string        // "item:I7" | "assignment:A12" | "worker:zhangsan" | "project:P1"
    Project  ProjectID
    Facts    map[string]any // 规则给出的数据（预计完成时间、截止、阻塞了几件…）
    Suggest  []string      // 建议动作类型：verify | follow_up | reassign | cut_scope | ask_owner …
    People   []WorkerID
}

// Detector：一条规则。扩展点——加规则 = 注册一个 Detector。
type Detector interface {
    Name() string
    Detect(w World) []Signal
}

// Scorer：打分策略。扩展点——可替换权重或整个公式。
type Scorer interface {
    Score(s Signal, w World) float64
}

// Tiering：按分数与类型分档（Now / Today / Watch），并限制每次唤醒的 Now 数量。
type Tiering interface {
    Tier(scored []Scored, w World) Agenda
}

// Resolution：聚合根。某个 Signal 的处理结果。
type Resolution struct {
    Key     string
    Outcome string        // done | acted (等对方) | deferred | handed_to_owner | dismissed
    Until   *time.Time    // deferred / acted：到点重新出现
    Note    string
    By      Actor
    At      time.Time
}
func (r Resolution) Suppresses(s Signal, now time.Time) bool // 处理过且未到期、或事实未变化 → 不再上议程

// 领域服务：把规则、打分、分档、处理结果组合起来。
type AgendaService struct {
    Detectors []Detector
    Scorer    Scorer
    Tiering   Tiering
}
func (s AgendaService) Build(w World, rs []Resolution) Agenda
```

**规则清单（第一批 Detector）**：逾期、预计延期（用 Pace）、余量不足、无人负责、长时间无进展、阻塞过久、依赖延期、
有人在等我们、委托未回复、人力超载（跨项目）、关键人不可用、节奏到点。

### 4.6 Insight（洞察）：纯计算的领域服务

```go
package insight

type Pace interface{ Factor(w WorkerID) float64 }           // 实际 / 预估，样本不足时为 1
type PaceStats struct{ ... }                                  // 由委托历史计算的读模型，实现 Pace

type Progress struct{ Done, Planned, Forecast float64; NextMilestone *ItemID; Health Health; Reasons []string }
type ProgressService interface {
    Project(p ProjectID, w attention.World) Progress
}

// 找人：从“需要”到候选人。Matcher 可组合，是扩展点。
type Need struct {
    Project ProjectID
    Text    string        // “定埋点方案”
    Skills  []string
    Item    *ItemID       // 续做优先原委托人
}
type Candidate struct{ Worker WorkerID; Score float64; Reasons []string; Blockers []string }

type Matcher interface {                                     // 角色职责 / 技能 / 上下文连续性 / 语义（L1 判断）
    Match(n Need, w attention.World) []Candidate
}
type StaffingService struct {
    Matchers []Matcher                                       // 按顺序合并打分
    Filters  []Filter                                        // 不可用 / 无同意 / 无权限 → 剔除（写明原因）
}
func (s StaffingService) Candidates(n Need, w attention.World) []Candidate
```

### 4.7 Cognition（认知）：唤醒编排

这一层是“大脑分层”的落地，属于应用层（编排），只依赖端口。

```go
package cognition

type Trigger struct {
    Kind     string        // message | event | cadence | rule
    Conv     ConversationRef
    Message  *Inbound      // Kind=message
    Event    DomainEvent   // Kind=event
    Cadence  *CadenceRef   // Kind=cadence
    Signals  []attention.Signal // Kind=rule
}

// Judge：L1 判断端口。Jev / LLM 结构化输出 / 规则启发式 都实现它。
type Judge interface {
    Ask(ctx context.Context, state any, qs map[string]Question) (map[string]Answer, error)
}
type Question struct {
    Type         string // noul | choice | score
    Instructions any
    Criteria     any
}
type Answer struct {
    Noul        float64
    Choice      string
    Score       float64
    Probabilities map[string]float64
    Confidence  float64
}

// Interpreter：用 Judge 把一次唤醒解释成意图（批量提问，一次请求）。
type Interpretation struct {
    Intent      string              // chat | status | new_request | reply | plan_change | approval | other
    Reply       *ReplyIntent        // 对哪个委托、什么动作、可能的时间
    Urgent      float64
    NeedsDepth  string              // fast | deep | owner
    Confidence  float64
}
type Interpreter interface {
    Interpret(ctx context.Context, t Trigger, view ActorView) (Interpretation, error)
}

// Router：决定交给哪层（策略可替换）。
type Layer string // L0 | L2 | L3
type Router interface {
    Route(t Trigger, in Interpretation, ag attention.Agenda) Plan // 例如：L0 落状态 + L2 确认；L2 先回 + L3 异步
}

// BriefingBuilder：按层组装简报（不同层不同切片）。
type BriefingBuilder interface {
    Build(layer Layer, t Trigger, ag attention.Agenda, view ActorView) Briefing
}

// Brain：L2 / L3 的端口（现有 brain.Adapter 演进而来）。工具集按层与调用者权限给出。
type Brain interface {
    Name() string
    Run(ctx context.Context, b Briefing, tools ToolSet) (Outcome, error)
}

// Orchestrator：唤醒的完整流程（应用服务）。
type Orchestrator struct {
    Interpreter Interpreter
    Router      Router
    Briefs      BriefingBuilder
    Brains      map[Layer]Brain
    Commands    CommandBus   // 工具调用 → 命令 → 应用服务
    Agenda      AgendaQuery
}
func (o *Orchestrator) Handle(ctx context.Context, t Trigger) error
```

`ActorView`：按调用者权限裁剪后的世界视图（主人看全部；被派活的人只看自己的委托；访客只看公开信息）。
**权限在组装视图和执行命令两处检查**，大脑拿到的从来不是全量数据。

### 4.8 Communication（沟通）

已有的 hub / Platform / reach 基本保留，统一成端口：

```go
type Channel interface {                       // 飞书 / console / Slack …
    Name() string
    Start(ctx context.Context, on func(Inbound)) error
    Reply(ctx context.Context, conv ConversationRef, text string) error
}
type DirectMessenger interface {               // 可选能力：主动私聊某人
    SendUser(ctx context.Context, user string, text string) (MessageRef, error)
}
type Reacher interface {                       // 一种触达方式：私聊 / 加急 / 邮件 / 电话 / A2A
    Channel() RouteKind
    Send(ctx context.Context, r Route, m Message) (Receipt, error)
}
type ContactService interface {                // 规则：按紧急度与规矩选路线、记账、升级
    Reach(ctx context.Context, w workforce.Worker, m Message) (Receipt, error)
}
```

---

## 5. Worker 驱动：委托怎么真正到达执行者

`WorkerDriver` 是 Delegation 上下文的出站端口，每种 worker 一个实现，回报通过事件进入应用层：

```go
type WorkerDriver interface {
    Kind() workforce.Kind
    Observe(ctx context.Context, w workforce.Worker) []workforce.Fact    // 可观测状态
    Offer(ctx context.Context, w workforce.Worker, a delegation.Assignment) error
    Notify(ctx context.Context, w workforce.Worker, a delegation.Assignment, s delegation.Step) error
}

// 回报入口（应用层实现）：driver 收到执行者的动静后调用。
type DelegationInbox interface {
    Report(ctx context.Context, id AssignmentID, r Report) error         // accept / progress / deliver / fail …
}
```

| 实现 | Offer | 回报来源 |
|---|---|---|
| `agentsession`（现 worker.Manager） | 进队列执行 | 任务结束 → Report(deliver/fail) |
| `human` | ContactService 发出请求消息 | 本人在聊天里回复 → 认知层解释 → `respond` 命令 |
| `agentflow` | 启动 run | run 结束 / human 节点 → Report |
| `bot` / `a2a` | 调对方的协议端点 | 对方回调 / 轮询 → Report |

新增一种 worker = 实现一个 `WorkerDriver` 并注册，领域层不变。

---

## 6. 应用层：命令、仓库、事件

### 6.1 命令（工具调用最终都变成命令）

```go
type Command interface{ Name() string }

// 例：
type Delegate struct{ Item ItemID; Worker WorkerID; Brief Brief }
type Respond  struct{ Assignment AssignmentID; Action string; Note string; ETA, Due *time.Time; Result string; Evidence []string }
type Review   struct{ Assignment AssignmentID; Accept bool; Note string }
type Defer    struct{ SignalKey string; Until time.Time; Note string }
type SetupProject struct{ ... }
…

type CommandBus interface {
    Dispatch(ctx context.Context, by Actor, c Command) (Result, error)
}
```

每个命令处理器的固定步骤：**授权（Guard）→ 加载聚合 → 调聚合方法（校验不变量、产生事件）→ 在一个工作单元里保存
聚合与事件 → 发布事件**。工具层只做参数解析与结果格式化，不含业务逻辑。

### 6.2 授权

```go
type Guard interface {
    Authorize(by Actor, c Command, w attention.World) error // 角色 × 项目成员关系 × 资源归属
}
```

例：被派活的人只能对**自己的**委托 `Respond`；访客不能发任何改状态的命令；对外部 worker 派活需主人或项目负责人。

### 6.3 仓库与工作单元

```go
type Projects interface {
    Get(ctx context.Context, id ProjectID) (portfolio.Project, error)
    Save(ctx context.Context, p portfolio.Project, version int) error  // 乐观锁
    List(ctx context.Context, f ProjectFilter) ([]portfolio.Project, error)
}
type Items interface{ Get; Save; List(f ItemFilter); OpenByProject(p ProjectID) … }
type Assignments interface{ Get; Save; OpenFor(i ItemID) (*delegation.Assignment, error); ByWorker(w WorkerID, open bool) … }
type Workers interface{ Get; List; Save }
type WorkerStates interface{ Get; Save }
type Resolutions interface{ Get(key string); Save; Active(now time.Time) }
type EventLog interface{ Append(ctx context.Context, evs []DomainEvent) error; Since(cursor) … }

type UnitOfWork interface {
    Do(ctx context.Context, fn func(tx Tx) error) error // Tx 暴露上面的仓库；提交时一起写聚合与事件
}
```

实现：先做单文件 JSON（单进程、文件锁），之后 SQLite；飞书多维表格作为**同步适配器**订阅事件，而不是主存储。

### 6.4 领域事件与订阅者

```
assignment.offered / accepted / declined / countered / asked / answered / progressed /
           delivered / verified / revised / failed / cancelled / released / expired / nudged
item.created / rescheduled / attached / detached / blocked / completed / dropped
project.created / member_added / member_ended / priority_changed / card_updated / status_changed
worker.fact_recorded
attention.resolved / deferred
conversation.message_received
cadence.due
```

| 订阅者 | 做什么 |
|---|---|
| Delegation → Planning 同步 | `assignment.*` 更新对应 Item（开始时间、进展、阻塞、完成、回到 todo） |
| Attention | 事件写入“有人在等”与唤醒队列 |
| Cognition | 需要大脑处理的事件触发唤醒（交付待验收、拒绝、还价、提问…） |
| Insight | 更新速度统计（PaceStats） |
| Telemetry | 写审计日志；研发指标（北极星、护栏）从这里离线计算 |
| Board sync（可选） | 同步到飞书多维表格 / 任务 |

跨上下文的一致性靠事件（最终一致）；同一聚合内的一致性靠聚合方法（强一致）。

---

## 7. 找人的边界：agent 优先，人只在必要时介入

### 7.1 原则

**能由 agent 闭环的事，不找人。** 找人有成本：等待（小时到天）、打扰（社交成本）、出错（人会忘）。所以：

1. 默认交给 agent；
2. 只有出现下表中的**必要理由**时才找人；
3. 找人时只把**必须由人完成的那一小块**交出去，其余仍由 agent 做（例如 agent 准备好发布，人只按“批准”）；
4. 每次找人都写明理由，进入审计与研发指标（每闭环一件事需要人介入几次）。

### 7.2 必须找人的理由（NeedKind）

| 理由 | 含义 | 例子 | 通常找谁 |
|---|---|---|---|
| `approval` 权限 / 审批 | 规则要求人批准，或动作不可逆 / 有风险 | 合并到主干、生产发布、花钱、对外发布、开权限、删数据 | 规则里的审批人（角色解析到人） |
| `decision` 决策 / 澄清 | 需求有歧义；取舍涉及成本、优先级、价值判断；目标冲突 | 换区域还是加重试（涉及费用）；“上线”指灰度还是全量 | 主人或项目负责人 |
| `physical` 现实世界动作 | 需要人的身体、身份或在场 | 给客户打电话、签字、到场、拍视频、采购 | 能做该动作的人 |
| `capability` 能力缺口 | 没有任何 agent 拥有所需的工具、系统访问或凭证 | 只能在后台网页操作的系统；需要 2FA；供应商门户 | 有该系统权限的人 |
| `relationship` 代表与关系 | 需要以人的身份与外部的人沟通，关系本身重要 | 回复客户投诉、和合作方谈条件 | 对接人（agent 可起草） |
| `judgment` 专业判断 / 验收 | agent 的核验不够：审美、法务、领域专家判断，或需求方本人确认 | 设计稿审美、合同条款、报障人确认已修好 | 对应专家或需求方 |
| `knowledge` 隐性知识 | 信息不在任何系统里，只在某人脑子里 | “当初为什么这么设计”“客户口头答应过什么” | 知情人 |

另外一条**事后理由**：agent 尝试后失败（多次失败、反问说缺权限 / 缺信息），由 agent 的反问内容归类到上面某一类，再找人。

### 7.3 哪些由框架决定，哪些由大脑判断

| 由框架硬性保证（代码） | 由 L1 判断（Jev） | 由 L3 推理 |
|---|---|---|
| 审批规则：动作执行前检查，命中就自动生成审批委托，agent 不能绕过 | 某个请求 / 步骤是否涉及审批、现实动作、对外沟通（noul） | 把一件事拆成“agent 能做的”和“必须人做的”两部分 |
| 能力匹配：哪个 agent 拥有所需系统与权限（读档案） | agent 反问的原因属于哪一类（choice） | 能自己决定的就决定（依据主人的长期指示），需要人时把问题缩到最小、给选项 |
| 审批人解析：规则里的角色 → 当前担任该角色的人（考虑时间段与状态） | 候选人与需求的职责匹配度（score，语义层面的补充） | 选谁（在框架给出的候选里），以及怎么开口 |
| 找人顺序：候选列表里 agent 排在人前面；交给人必须填 `Why`，否则命令被拒 | — | 什么时候升级：等太久就换人或找主人 |
| 人的规矩：工作时间、提醒上限、同意范围 | — | — |

大脑从一开始就知道这些边界：协议里有“何时找人”的固定说明（上表的理由与原则）；简报里带上**与当前事项相关的**
审批规则、agent 能力摘要、候选人及其职责。具体某件事怎么拆、找谁，仍由大脑结合情况判断。

### 7.4 对数据结构的影响

| 结构 | 新增 | 用途 |
|---|---|---|
| `Worker` | `Capabilities`、`Authority`、`Physical` | 框架据此判断 agent 能否闭环、谁有权批准、谁能做现实动作 |
| `Project` | `Policies []ApprovalRule`（继承上级） | 动作执行前的硬检查；审批人按角色解析 |
| `Item` | `Needs []Need` | 拆解时标出需要什么；找人与能力匹配据此过滤 |
| `Assignment` | `Kind`（work / approval / decision / clarification / action / review）、`Why`、`Options` | 给人的请求按类型呈现（审批可一键通过），理由可审计 |
| 命令 `Delegate` | 交给人时 `Why` 必填 | 迫使“找人”有理由；研发指标可统计每类理由的次数 |

---

## 7A. 注意力与唤醒：没有“触发器”，只有信号

借鉴 multigent 的 attention 设计：**外部来的任何东西（消息、@、委托回报、规则发现的风险、节奏到点、卡片点击）
都先变成一条注意力信号，进同一个收件箱；是否唤醒大脑、唤醒后先做什么，是两个独立的决定。**
连接器（飞书等）只负责“记录发生了什么、递送信号”，不决定大脑怎么工作。

### 7A.1 信号（Signal）

| 字段 | 含义 |
|---|---|
| `id` / `dedupe_key` | 同一外部事件只成一条信号（如 `feishu:om_xxx`、`rule:will_miss:I7`、`assignment:A3:delivered`） |
| `source` | 来源：私聊 / 群里 @ / 委托回报 / 规则 / 节奏 / 卡片 |
| `actor` | 谁引起的（已解析身份与角色） |
| `reason` | direct_message / mention / reply / delivered / declined / countered / asked / risk / cadence … |
| `summary` + `body` | 一句话摘要；完整内容（只在需要时展开） |
| `refs` | 关联的项目 / 事项 / 委托 / 人 |
| `reply_to` | 回复目标（哪个会话、哪条消息）——大脑回复时不用关心渠道 |
| `priority` / `level` | 由规则与 L1 判断给出 |
| `status` | pending → seen → handling → handled / ignored / deferred(until) / expired；终态不可被重新打开 |

**强信号与环境信息分开**：私聊、@、委托回报、风险、节奏是信号；普通群聊、外部系统流水只记录为可查询的历史，
不进收件箱、不唤醒。规则发现的风险也是信号（不再是单独的“议程”概念），和消息在同一个收件箱里排序。

### 7A.2 从信号到唤醒：三道闸

```
外部事件 ─► 连接器记录 + 生成信号（去重）
              │
              ▼
        ① 规则（L0）：身份与权限、是否强信号、去重、静默时段、同一会话合并
              │
              ▼
        ② 判断（L1，Jev 或规则退路）：紧急度、是否需要回应、属于哪个项目 / 委托、能否直接落地
              │      （例：“接了，周三给” → 直接登记，不唤醒大脑，只发确认）
              ▼
        ③ 唤醒策略（配置）：哪些信号可以“打断睡眠”立即唤醒，哪些等下次节奏批量处理
              │
              ▼
        唤醒大脑（带“焦点 + 待办”，不是整本简报）
```

唤醒策略是 bot 的工作节奏配置，而不是连接器的固定行为：

```toml
[wake]
direct_message = "now"        # now | batch | ignore
mention        = "now"
worker_reply   = "now"        # 委托方的回复 / 交付 / 提问
risk_high      = "now"
risk_medium    = "batch"
cadence        = "batch"
batch_every    = "30m"        # 批量唤醒的间隔（也是“心跳”）
```

### 7A.3 唤醒时大脑看到什么

不是每次都给整本简报，而是分层的一页：

1. **焦点**：这次唤醒的信号（完整内容 + 回复目标 + 关联对象的必要状态）。
2. **待办**：收件箱里其他未处理的强信号，每条一行（含分数、等了多久）。
3. **规则提示**：与焦点相关的风险、截止（算好的）。
4. **按需**：完整项目卡、人力名册、历史消息都通过工具取，不预先塞进上下文。

大脑可以**不先处理焦点**，去做更紧急的事——但有两条硬保证：
- **人在等就先回执**：有人私聊或 @ 时，框架在秒级给出“收到”（表情回应或一句快脑回复），这不需要大脑决策；
- **不处理就要留下状态**：焦点信号必须标记为 handled / ignored（附理由）/ deferred（附时间），没有标记的下次唤醒原样回来。

### 7A.4 一个连续的会话，权限跟着每一轮走

PM 要找人干活、向人咨询、在多个人之间协调，按人拆开会话就会丢上下文（实测：拆开后同一个还价被两边各处理一次，
主会话在主人回复前就“替主人同意”了）。所以采用**一个连续的主会话**：所有人的消息和框架的信号都进来，大脑始终有全貌。

隐私不再靠拆记忆，而是靠两层保证：

- **权限跟着这一轮的对象（硬）**：每次唤醒的工具权限取焦点信号的发起人——处理王五的消息时，这一轮只有王五的权限
  （只能操作他自己的委托，不能审批、不能改派）；主人侧（主人、管理员、框架信号）与其他人的信号**绝不放进同一轮**；
  回复回到信号的来源会话。由代码保证，不依赖大脑。
- **说话只给对方需要的（软）**：大脑记得别的对话，所以给同事回复时可能多说；由协议约束，之后由 L1 判断在发出前检查
  发给非主人的消息是否泄露了别人的工作、别的项目或主人的对话。

需要严格隔离的场景可以配置 `isolate_visitors = true`：同事的对话回到各自独立的会话（记忆分开）。

### 7A.5 联系人是一个工具，渠道是框架的事

大脑只表达**要找谁、说什么、多急、要不要回复、和哪件事有关**：

```
contact(person, message, urgency=normal|soon|urgent|critical, expect_reply=true, about=<item/assignment>)
reply(signal, message)          # 回复某条信号的来源（私聊、群、卡片），渠道由回复目标决定
```

框架负责其余一切：从这个人的联系方式里按紧急度与时间选渠道（飞书私聊 → 飞书加急 → 电话 / 邮件），
遵守工作时间、静默时段、提醒上限、需要主人批准的渠道；记录送达；对方的回复作为信号回到收件箱并关联到 `about`。
大脑的上下文里不出现渠道、地址、重试这些细节。

## 8. 附录：一次完整的执行流程

用一个接近真实的场景，把每一步“框架算什么、大脑判断什么、动了哪些数据”串起来。

### 8.0 初始状态

- 长期项目 **P3「TapNow 稳定性」**（P0）。成员：
  - 李四：媒体服务 code owner（公司级角色，`Authority: merge:main @ repo:tapnow`）；
  - 王五：本周 on-call（项目角色，期间 10/13–10/19，`Authority: deploy:prod`）；
  - Jerry：业务方，问题报告人；
  - agent `tapnow-dev`（worktree 模板，`Capabilities: repo:tapnow write`）；
  - agent `ops-reader`（`Capabilities: gcloud-logging read, metrics read`）。
- 公司级审批规则：`merge:main` 需要 code owner 批准；`deploy:prod` 需要 on-call 批准；涉及费用的变更需要主人决定（主人的长期指示）。

### 8.1 主人：“Jerry 说线上 view_media 从 10/4 起偶发 Vertex 500，定位一下，今天能修好最好”

**框架**：识别主人身份；把“今天”换算成截止 10/14 23:00；组装 P3 的项目卡与相关审批规则；
按能力匹配给出候选——读日志：`ops-reader`；改代码：`tapnow-dev`；没有 agent 能做 `deploy:prod`。

**L1 判断**：新需求；属于 P3；需要深想。→ 快脑先回“收到，我先让人查日志”，深脑开始拆解。

**L3 推理**：拆成 5 个事项，并标出每个事项需要什么——

| 事项 | Needs | 交给 |
|---|---|---|
| I31 查日志、复现、定位原因 | `capability: gcloud-logging:read` | agent `ops-reader` |
| I32 修复并提 PR | `capability: repo:tapnow:write` | agent `tapnow-dev`（依赖 I31） |
| I33 代码评审并合并 | `approval: merge:main` | 由规则解析审批人（依赖 I32） |
| I34 生产发布 | `approval: deploy:prod` | 由规则解析审批人（依赖 I33） |
| I35 确认问题消失 | `capability: metrics:read` + `judgment`（报障人确认） | agent 看指标；Jerry 确认（依赖 I34） |

只有 I31、I32 立刻派出；I33–I35 等依赖完成后再派。**此时一个人都没打扰**。

### 8.2 `ops-reader` 交付定位结果，但需要一个决定

agent 的结论：10/4 起 Vertex 某区域配额收紧，高峰期 500。两种修法：A）切换到另一个区域（每月多约 2000 元）；
B）加退避重试 + 降级到备用模型（不增加费用，延迟略增）。

**L1 判断**：这是带选项的交付；涉及费用 → `decision`。
**框架**：主人的长期指示规定“涉及费用的决定要问我”；生成一个 `Kind=decision, Why=decision, Options=[A, B]` 的委托给主人。
**L3 推理**：写清两个选项的影响并给推荐（B，先止血；A 作为后续评估）。
**主人**：在卡片上点“B”。→ I32 的任务说明更新为方案 B，`tapnow-dev` 开始实现。

### 8.3 `tapnow-dev` 提交 PR，CI 通过，想合并

**框架**：agent 调用“合并”动作 → 命中审批规则 `merge:main` → 动作被拦下，自动生成 I33 的审批委托；
审批人按角色解析为 code owner 李四；查李四状态：可联系、今天负载 3 件。
**L3 推理**：写最小的审批请求——PR 链接、改动摘要、测试结果、风险点、“今天要上线”。
**李四**：回复“LGTM”。**L1**：属于对审批委托的回复，动作是“批准”（0.95）。**框架**：登记批准，合并动作放行。

> 如果李四两小时没回、而截止是今天：规则产生 `waiting_on_human` 信号且分数很高 → 深脑在候选里找另一个有
> `merge:main` 权限的人，或请主人决定是否等待。

### 8.4 生产发布

**框架**：发布动作命中 `deploy:prod` → 审批人按角色解析：**本周 on-call 王五**（项目角色带时间段，下周就是别人）；
王五在工作时间、可联系 → 发出审批请求（一键批准）。
**王五**：批准。**框架**：放行发布流水线（由 agent 触发，人只负责批准）。

### 8.5 验证与收尾

`ops-reader` 对比发布前后的 500 比例（证据：指标截图与数据）→ 交付。
**L3**：按完成标准核对：500 比例回到 10/4 之前的水平，满足。
**框架**：给 Jerry 发一个 `Kind=review, Why=judgment` 的确认请求（“你那边还有报错吗？”）。Jerry 回复“没再出现了”。
→ I35 验收，P3 记录事件闭环；`ops-reader`、`tapnow-dev` 的速度统计更新；主人收到一条结论消息。

### 8.6 这件事里谁被打扰了、为什么

| 人 | 理由 | 被打扰几次 |
|---|---|---|
| 主人 | `decision`：涉及费用的取舍 | 1 次（一键选择）+ 1 条结论 |
| 李四 | `approval`：合并到主干 | 1 次 |
| 王五 | `approval`：生产发布 | 1 次 |
| Jerry | `judgment`：报障人确认 | 1 次 |

查日志、定位、写修复、跑 CI、触发发布、看指标，全部由 agent 完成。每次找人都有规则或理由支撑，并且只交出
必须由人完成的那一步。

### 8.7 为什么这样划分数据结构（结合这个流程）

划分原则：**每个结构回答一个问题，只有一类写入者、一种变化频率、一个生命周期。**

| 结构 | 回答的问题 | 谁写 | 变化频率 | 在流程里的作用 |
|---|---|---|---|---|
| `Worker` 档案（含能力、权限） | 他 / 它能做什么、有权批准什么 | 配置 | 很少 | 11.1 决定哪些 agent 能闭环；11.3、11.4 解析审批人 |
| `WorkerState` | 现在能不能找 | 观测 / 声明 / 推断 | 随时 | 11.3 李四可联系、负载 3 件 |
| `Project`（成员、规则、节奏） | 这里谁负责什么、什么需要批准 | 主人 / 深脑 | 偶尔 | “本周 on-call 是王五”随时间段变化；规则从公司继承 |
| `Item`（含 Needs、依赖） | 要做什么、需要什么、什么顺序 | 深脑拆解 | 偶尔 | 5 个事项、依赖链、哪些需要人 |
| `Assignment`（含 Kind、Why） | 谁答应了做什么、为什么是他 | 双方 | 频繁 | 决定 / 审批 / 确认都是委托，可一键回复，理由可审计 |
| 信号（不存） | 现在什么需要注意 | 规则 | 每次巡检重算 | 李四不回 → `waiting_on_human` |
| 处理记录 | 这件事怎么处理过 | 大脑 / 主人 | 每次处理 | 避免重复打扰、到点重现 |
| 事件 | 发生过什么 | 各聚合 | 只追加 | 依赖完成 → 派出下游；更新速度统计；审计 |

几个关键的拆分理由：

- **事项与委托分开**：同一个事项会经历“agent 做 → 人批准”的多次委托（I33 只是一次审批委托），
  被拒绝或换人时事项与历史都还在。
- **审批规则放在项目上、由代码在动作执行时检查**：权限不能靠大脑记得——agent 想合并就会被拦下，并自动走审批。
- **角色带时间段、挂在项目上**：“谁是审批人”是随时间变化的项目事实（on-call 每周轮换）。
- **能力挂在 worker 档案上**：“agent 能不能闭环”由档案决定，框架据此把 agent 排在人前面。
- **找人必须写 `Why`**：让“找人”成为有成本、可统计的动作，研发指标（每闭环一件事的人工介入次数）才能衡量 agent
  的闭环能力是否在提升。

## 9. 扩展点汇总

| 扩展点（接口） | 加一种新东西 | 现有 / 首批实现 |
|---|---|---|
| `WorkerDriver` | 新 worker 类型 | agent 会话；人；（agentflow、bot、A2A） |
| `Channel` / `DirectMessenger` | 新聊天平台 | 飞书 SDK、lark-cli、console |
| `Reacher` | 新触达方式 | 飞书私聊、主人私聊；（加急、邮件、电话） |
| `Brain` | 新的 L2 / L3 大脑 | claudecode、codex、pi、command |
| `Judge` | 新的 L1 判断模型 | Jev；LLM 结构化输出；规则启发式 |
| `Detector` | 新的注意力规则 | 逾期、预计延期、在等我们… |
| `Scorer` / `Tiering` | 新的优先级策略 | 默认权重公式 |
| `Matcher` / `Filter` | 新的找人方式 | 角色职责、技能、续做、语义 |
| `Router` / `BriefingBuilder` | 新的分层策略 / 简报格式 | 默认策略 |
| 仓库 / `UnitOfWork` | 新存储 | JSON 文件；（SQLite） |
| 事件订阅者 | 新的副作用（同步、通知、统计） | 审计、速度统计；（飞书看板） |
| `Clock` | 时间源 | 真实时钟；虚拟时钟（评测） |

注册方式统一：每类扩展一个注册表（`Register(name, factory)`），由配置选择实现，和现在的 `brain.Register` 一致。

---

## 10. 包结构

```
internal/
  domain/
    shared/        ID、Actor、Clock、Period、Priority、DomainEvent
    workforce/     Worker、WorkerState、Fact、Norms、Route（值对象）
    portfolio/     Project、Membership、Cadence、Card、Objective
    planning/      Item、Criterion、Blocker、DependencyGraph
    delegation/    Assignment、Brief、Step、状态转换表
    attention/     World、Signal、Detector、Scorer、Tiering、Resolution、AgendaService
    insight/       Pace、Progress、Staffing（Matcher / Filter）
  app/
    commands/      命令定义与处理器、Guard
    queries/       World 组装、ActorView、读模型
    cognition/     Interpreter、Router、BriefingBuilder、Orchestrator
    events/        EventBus、订阅者注册
  ports/           所有接口（仓库、驱动、渠道、大脑、判断、时钟、触达）
  adapters/
    store/jsonfile/       仓库 + 工作单元
    drivers/agentsession/ drivers/human/  (drivers/agentflow/ drivers/a2a/)
    channels/feishu/ channels/larkcli/ channels/console/
    reach/         私聊、主人私聊（加急、邮件…）
    brains/        claudecode、codex、pi、command
    judges/        jev、llm、heuristic
  tools/           工具 = 命令 / 查询的薄封装（MCP、REST、CLI 共用）
  sim/             评测用：虚拟时钟、剧本化的人、假 worker、golden case 运行器
```

`domain/*` 只依赖 `domain/shared` 与标准库，可以单独跑单元测试；`sim/` 只依赖 ports，用来跑研发的 golden cases。

---

## 11. 和现有代码的迁移关系

| 现有 | 去向 |
|---|---|
| `workforce`（Profile、State、Assignment） | 拆分到 `domain/workforce` 与 `domain/delegation` |
| `plan`（Goal、Task、Risk、Role、Assess） | Goal → Project.Objective；Task → Item；Role → Membership；Assess 拆成多个 Detector |
| `pm`（服务、跟进、上下文） | 拆到 `app/commands`、`attention` 的 Detector、`cognition/BriefingBuilder`、`drivers/human` |
| `reach` | `adapters/reach` + `ContactService` |
| `worker`（Manager、模板、隔离） | `drivers/agentsession`（模板与实例管理保留在 driver 内） |
| `hub`（会话、合并、排队） | `channels` + `cognition` 的触发入口；会话队列保留 |
| `brain`（adapters、prompt） | `adapters/brains` + `cognition/BriefingBuilder` |
| `tools` / `toolserver` | `tools` 改为命令 / 查询的薄封装；toolserver 不变 |
| `schedule` | 作为 `Cadence` 的计时底层 |
| `identity`、`audit`、`config`、`cli`、`platform` | 保留，按需调整 |

迁移按上下文逐个进行，每一步都保持测试通过：先建 `domain/*`（纯逻辑 + 单元测试），再接仓库与命令，最后替换认知层。
