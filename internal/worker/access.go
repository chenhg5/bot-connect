package worker

import (
	"github.com/chenhg5/bot-connect/internal/identity"
)

// Scope is one caller's view of the worker pool through one bot.
type Scope struct {
	Names    map[string]bool        // the bot's workers (nil = all); instances / sub-workers follow their base
	Caller   identity.User          // who is asking (role decides the rest)
	AskRoles map[identity.Role]bool // roles that may hand work to read-only shared workers
}

// Access is what the caller may do with one worker.
type Access struct {
	See      bool // appears in list_workers
	Status   bool // only name / description / idle-busy (no task contents)
	Read     bool // read_worker, its sessions, its task results
	Delegate bool // hand it work
}

// access applies the rules:
//
//	                      owner        admin        member                 visitor
//	shared worker         all          all          —                      public: status
//	shared, readonly      all          all          ask (if ask_roles)     ask (if ask_roles)
//	shared, per_user      all          all          delegate → own copy     —
//	instance of user U    all          see+read     U only: all            —
func (s Scope) access(w *workerState) Access {
	if s.Names != nil && !s.Names[w.base()] {
		return Access{}
	}
	role := s.Caller.Role
	if w.UserID != "" { // a member's private instance
		switch {
		case w.UserID == s.Caller.ID || role == identity.RoleOwner:
			return Access{See: true, Read: true, Delegate: true}
		case role == identity.RoleAdmin:
			return Access{See: true, Read: true}
		}
		return Access{}
	}
	if role.Privileged() {
		return Access{See: true, Read: true, Delegate: true}
	}
	top := w.Base == "" && w.Parent == ""
	switch {
	case top && w.Spec.PerUser != "" && role == identity.RoleMember:
		return Access{See: true, Status: true, Delegate: true} // routed to the member's own copy
	case top && w.Spec.Access == "readonly" && s.AskRoles[role]:
		return Access{See: true, Status: true, Delegate: true}
	case top && w.Spec.Public:
		return Access{See: true, Status: true}
	}
	return Access{}
}

// base is the configured worker this one derives from.
func (w *workerState) base() string {
	if w.Base != "" {
		return w.Base
	}
	return w.Spec.Name
}

// Access reports what the scope's caller may do with a worker.
func (m *Manager) Access(name string, s Scope) Access {
	m.mu.Lock()
	defer m.mu.Unlock()
	if w := m.workers[name]; w != nil {
		return s.access(w)
	}
	return Access{}
}

// Visible reports whether the caller may read a worker in full.
func (m *Manager) Visible(name string, s Scope) bool { return m.Access(name, s).Read }

// TaskVisible: a caller sees a task if they requested it or may read its worker.
func (m *Manager) TaskVisible(t Task, s Scope) bool {
	return t.Requester.ID == s.Caller.ID || m.Access(t.Worker, s).Read
}
