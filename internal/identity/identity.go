// Package identity answers "who sent this?" independently of the IM channel.
//
//   - A platform reports the raw sender (its own user id, plus whatever the
//     event carries) and may implement Directory to look users up.
//   - Policy decides the sender's Role for this bot (owner / admin / visitor).
//   - Resolver combines both, with a cache, and is what the framework uses.
//
// Nothing downstream ever sees an anonymous sender: every message, turn, tool
// call and task carries a User with id, name (when resolvable) and role.
package identity

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type Role string

const (
	RoleOwner   Role = "owner"   // the person this bot belongs to
	RoleAdmin   Role = "admin"   // trusted: may command workers like the owner
	RoleMember  Role = "member"  // may use their own isolated workspaces and ask read-only workers
	RoleVisitor Role = "visitor" // anyone else: talk, ask, leave a message
)

// Privileged reports whether the role may command workers.
func (r Role) Privileged() bool { return r == RoleOwner || r == RoleAdmin }

type User struct {
	Platform string `json:"platform,omitempty"`
	ID       string `json:"id"`                 // platform user id (Feishu: open_id, per app)
	UnionID  string `json:"union_id,omitempty"` // cross-app stable id when the platform has one
	Name     string `json:"name,omitempty"`
	Email    string `json:"email,omitempty"`
	Role     Role   `json:"role"`
	// Resolved is true once a Directory lookup has succeeded.
	Resolved bool `json:"resolved,omitempty"`
}

// Privileged is shorthand for u.Role.Privileged().
func (u User) Privileged() bool { return u.Role.Privileged() }

// Display returns a human-readable label even when the name is unknown.
func (u User) Display() string {
	if u.Name != "" {
		return u.Name
	}
	if len(u.ID) > 8 {
		return u.ID[:3] + "…" + u.ID[len(u.ID)-6:]
	}
	return u.ID
}

// System is the requester of framework-originated work.
var System = User{ID: "system", Name: "system", Role: RoleOwner}

// Directory is an optional platform capability: describe a user by id.
type Directory interface {
	LookupUser(ctx context.Context, id string) (User, error)
}

// OwnerSource is an optional platform capability: who owns this bot by
// default (e.g. the owner of the Feishu app), used when none is configured.
type OwnerSource interface {
	DefaultOwners(ctx context.Context) []string
}

// Policy assigns roles. The default is StaticPolicy (config lists); a company
// deployment could plug in a policy backed by its own directory or RBAC.
type Policy interface {
	Role(u User) Role
}

// StaticPolicy matches users by platform id, union id or email. "*" in
// members makes everyone (who isn't owner/admin) a member.
type StaticPolicy struct {
	mu      sync.RWMutex
	owners  map[string]bool
	admins  map[string]bool
	members map[string]bool
}

func NewStaticPolicy(owners, admins []string, members ...string) *StaticPolicy {
	p := &StaticPolicy{owners: map[string]bool{}, admins: map[string]bool{}, members: map[string]bool{}}
	for _, o := range owners {
		p.owners[o] = true
	}
	for _, a := range admins {
		p.admins[a] = true
	}
	for _, m := range members {
		p.members[m] = true
	}
	return p
}

func (p *StaticPolicy) AddOwner(id string) {
	p.mu.Lock()
	p.owners[id] = true
	p.mu.Unlock()
}

func (p *StaticPolicy) HasOwners() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.owners) > 0
}

func (p *StaticPolicy) Role(u User) Role {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, k := range []string{u.ID, u.UnionID, u.Email} {
		if k != "" && p.owners[k] {
			return RoleOwner
		}
	}
	for _, k := range []string{u.ID, u.UnionID, u.Email} {
		if k != "" && p.admins[k] {
			return RoleAdmin
		}
	}
	if p.members["*"] {
		return RoleMember
	}
	for _, k := range []string{u.ID, u.UnionID, u.Email} {
		if k != "" && p.members[k] {
			return RoleMember
		}
	}
	return RoleVisitor
}

// Resolver turns a raw sender into a full User: directory lookup (cached)
// merged with what the event carried, then the policy's role.
type Resolver struct {
	Policy Policy
	TTL    time.Duration

	mu    sync.Mutex
	dirs  map[string]Directory // platform → directory
	cache map[string]cached
}

type cached struct {
	u  User
	at time.Time
}

func NewResolver(p Policy) *Resolver {
	return &Resolver{Policy: p, TTL: time.Hour, dirs: map[string]Directory{}, cache: map[string]cached{}}
}

func (r *Resolver) AddDirectory(platform string, d Directory) {
	r.mu.Lock()
	r.dirs[platform] = d
	r.mu.Unlock()
}

// Resolve fills in the user from the platform directory (best effort) and
// assigns the role. raw.Platform and raw.ID must be set.
func (r *Resolver) Resolve(ctx context.Context, raw User) User {
	key := raw.Platform + "/" + raw.ID
	r.mu.Lock()
	c, ok := r.cache[key]
	d := r.dirs[raw.Platform]
	r.mu.Unlock()

	u := raw
	if ok && time.Since(c.at) < r.TTL {
		u = merge(raw, c.u)
	} else if d != nil {
		lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		found, err := d.LookupUser(lctx, raw.ID)
		cancel()
		if err != nil {
			slog.Debug("identity lookup failed", "platform", raw.Platform, "id", raw.ID, "err", err)
		} else {
			found.Resolved = true
			u = merge(raw, found)
		}
		// Cache misses too, so a missing scope doesn't cost a call per message.
		r.mu.Lock()
		r.cache[key] = cached{u: u, at: time.Now()}
		r.mu.Unlock()
	}
	u.Role = r.Policy.Role(u)
	return u
}

// merge keeps the event's fields and fills gaps from the directory record.
func merge(event, dir User) User {
	out := event
	if out.Name == "" {
		out.Name = dir.Name
	}
	if out.UnionID == "" {
		out.UnionID = dir.UnionID
	}
	if out.Email == "" {
		out.Email = dir.Email
	}
	out.Resolved = out.Resolved || dir.Resolved
	return out
}
