// Package pm is the bot's project-management scaffold: goals, tasks,
// assignments to workers (agents, people, the owner), roles, risks, and the
// control loop that watches deadlines and follows up with people. The brain
// judges and decides through tools; this package keeps the state and makes
// the things that must happen happen (risk detection, reminders, expiry,
// escalation).
package pm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/chenhg5/bot-connect/internal/plan"
	"github.com/chenhg5/bot-connect/internal/workforce"
)

type data struct {
	Seq         map[string]int                   `json:"seq"`
	Goals       map[string]*plan.Goal            `json:"goals"`
	Tasks       map[string]*plan.Task            `json:"tasks"`
	Assignments map[string]*workforce.Assignment `json:"assignments"`
	Risks       map[string]*plan.Risk            `json:"risks"`
	Roles       map[string]*plan.Role            `json:"roles"`
	Facts       map[string]*workforce.State      `json:"facts"`               // declared / inferred facts per worker
	Escalated   map[string]bool                  `json:"escalated,omitempty"` // risk ids escalated to the owner
	Unresponded map[string]bool                  `json:"unresponsive,omitempty"`
}

func newData() data {
	return data{Seq: map[string]int{}, Goals: map[string]*plan.Goal{}, Tasks: map[string]*plan.Task{},
		Assignments: map[string]*workforce.Assignment{}, Risks: map[string]*plan.Risk{}, Roles: map[string]*plan.Role{},
		Facts: map[string]*workforce.State{}, Escalated: map[string]bool{}, Unresponded: map[string]bool{}}
}

func (s *Service) load() error {
	s.d = newData()
	b, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &s.d); err != nil {
		return fmt.Errorf("%s: %w", s.path, err)
	}
	fresh := newData() // fill maps missing from older files
	if s.d.Seq == nil {
		s.d.Seq = fresh.Seq
	}
	if s.d.Goals == nil {
		s.d.Goals = fresh.Goals
	}
	if s.d.Tasks == nil {
		s.d.Tasks = fresh.Tasks
	}
	if s.d.Assignments == nil {
		s.d.Assignments = fresh.Assignments
	}
	if s.d.Risks == nil {
		s.d.Risks = fresh.Risks
	}
	if s.d.Roles == nil {
		s.d.Roles = fresh.Roles
	}
	if s.d.Facts == nil {
		s.d.Facts = fresh.Facts
	}
	if s.d.Escalated == nil {
		s.d.Escalated = fresh.Escalated
	}
	if s.d.Unresponded == nil {
		s.d.Unresponded = fresh.Unresponded
	}
	return nil
}

func (s *Service) saveLocked() {
	b, err := json.MarshalIndent(s.d, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(s.path), 0o700)
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, s.path)
	}
}

func (s *Service) nextID(prefix string) string {
	s.d.Seq[prefix]++
	return fmt.Sprintf("%s%d", prefix, s.d.Seq[prefix])
}
