// Package plan holds what the bot manages as a PM: goals (outcomes with
// metrics), tasks (deliverables with deadlines, estimates and dependencies,
// each executed through workforce assignments) and risks.
//
// Judgement (is this feasible? what next? who should do it?) belongs to the
// brain. What must happen reliably belongs here: deadlines are watched,
// schedule risks are detected by rule as soon as the numbers say so, and the
// brain is woken with them, so a slipping deadline is never discovered on
// the day it passes.
package plan

import (
	"time"

	"github.com/chenhg5/bot-connect/internal/identity"
)

type GoalStatus string

const (
	GoalActive    GoalStatus = "active"
	GoalPaused    GoalStatus = "paused"
	GoalAchieved  GoalStatus = "achieved"
	GoalAbandoned GoalStatus = "abandoned"
)

// Goal is an outcome, not a task: it stays until achieved or dropped, and is
// reviewed on a cadence.
type Goal struct {
	ID          string        `json:"id"`
	Bot         string        `json:"bot,omitempty"`
	Title       string        `json:"title"`
	Why         string        `json:"why,omitempty"`
	Metrics     []Metric      `json:"metrics,omitempty"`
	Constraints []string      `json:"constraints,omitempty"` // what must not be traded away
	Due         *time.Time    `json:"due,omitempty"`
	Review      string        `json:"review,omitempty"` // schedule spec for reviews, e.g. "0 9 * * 1"
	Status      GoalStatus    `json:"status"`
	Owner       identity.User `json:"owner"`
	ConvKey     string        `json:"conv,omitempty"` // where reviews and risks are reported
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

// Metric is a measurable signal of a goal.
type Metric struct {
	Name    string    `json:"name"`
	Target  float64   `json:"target"`
	Current float64   `json:"current"`
	Unit    string    `json:"unit,omitempty"`
	Source  string    `json:"source,omitempty"` // where the number comes from
	At      time.Time `json:"at,omitempty"`
}

type TaskStatus string

const (
	TaskTodo    TaskStatus = "todo"    // not assigned yet
	TaskActive  TaskStatus = "active"  // an assignment is open
	TaskBlocked TaskStatus = "blocked" // waiting on a dependency, an answer, or a decision
	TaskDone    TaskStatus = "done"
	TaskDropped TaskStatus = "dropped"
)

// Task is a deliverable. Who does it, and how it is going, lives in its
// current workforce assignment; the task keeps what and by when.
type Task struct {
	ID         string        `json:"id"`
	Bot        string        `json:"bot,omitempty"`
	GoalID     string        `json:"goal,omitempty"`
	ParentID   string        `json:"parent,omitempty"`
	Title      string        `json:"title"`
	Done       string        `json:"done,omitempty"` // acceptance criteria
	Due        *time.Time    `json:"due,omitempty"`
	Estimate   time.Duration `json:"estimate,omitempty"`
	Priority   int           `json:"priority,omitempty"`
	DependsOn  []string      `json:"depends_on,omitempty"`
	Status     TaskStatus    `json:"status"`
	Assignee   string        `json:"assignee,omitempty"`   // worker id
	Assignment string        `json:"assignment,omitempty"` // current assignment id
	StartedAt  time.Time     `json:"started_at,omitempty"`
	LastSignal time.Time     `json:"last_signal,omitempty"` // last progress from the worker
	Blocked    string        `json:"blocked_on,omitempty"`  // what it waits for
	BlockedAt  time.Time     `json:"blocked_at,omitempty"`
	Owner      identity.User `json:"owner"`
	ConvKey    string        `json:"conv,omitempty"`
	CreatedAt  time.Time     `json:"created_at"`
	UpdatedAt  time.Time     `json:"updated_at"`
}

func (t Task) Open() bool { return t.Status != TaskDone && t.Status != TaskDropped }

type RiskKind string

const (
	RiskSchedule    RiskKind = "schedule"    // the deadline is in danger
	RiskFeasibility RiskKind = "feasibility" // may not be doable as asked
	RiskDependency  RiskKind = "dependency"  // something it waits on is late
	RiskResource    RiskKind = "resource"    // nobody suitable / available
	RiskQuality     RiskKind = "quality"
	RiskExternal    RiskKind = "external"
)

type Level int

const (
	Low Level = iota
	Medium
	High
)

func (l Level) String() string { return [...]string{"low", "medium", "high"}[l] }

type RiskStatus string

const (
	RiskOpen      RiskStatus = "open"
	RiskAccepted  RiskStatus = "accepted"  // owner knows and accepts it
	RiskMitigated RiskStatus = "mitigated" // something was done about it
	RiskClosed    RiskStatus = "closed"    // no longer applies
)

// Risk is something that may stop a task or goal from landing as promised.
type Risk struct {
	ID       string     `json:"id"`
	Bot      string     `json:"bot,omitempty"`
	Subject  string     `json:"subject"` // task or goal id
	Kind     RiskKind   `json:"kind"`
	Level    Level      `json:"level"`
	Rule     string     `json:"rule,omitempty"` // detector that raised it ("" = raised by the brain or a worker)
	Summary  string     `json:"summary"`
	Options  []string   `json:"options,omitempty"` // ways out: extend, reassign, cut scope, add help…
	RaisedBy string     `json:"raised_by"`
	Status   RiskStatus `json:"status"`
	At       time.Time  `json:"at"`
	Notified time.Time  `json:"notified,omitempty"` // when the owner was told
}
