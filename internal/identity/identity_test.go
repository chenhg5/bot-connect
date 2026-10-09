package identity

import (
	"context"
	"errors"
	"testing"
)

type dir struct{ calls int }

func (d *dir) LookupUser(ctx context.Context, id string) (User, error) {
	d.calls++
	if id == "ou_missing" {
		return User{}, errors.New("no scope")
	}
	return User{ID: id, Name: "Jack", UnionID: "on_jack", Email: "jack@corp.com"}, nil
}

func TestResolveRolesAndCache(t *testing.T) {
	r := NewResolver(NewStaticPolicy([]string{"ou_me"}, []string{"jack@corp.com"}))
	d := &dir{}
	r.AddDirectory("feishu", d)

	me := r.Resolve(context.Background(), User{Platform: "feishu", ID: "ou_me"})
	if me.Role != RoleOwner {
		t.Fatalf("owner by id: %+v", me)
	}
	jack := r.Resolve(context.Background(), User{Platform: "feishu", ID: "ou_jack"})
	if jack.Role != RoleAdmin || jack.Name != "Jack" || !jack.Resolved {
		t.Fatalf("admin by email via directory: %+v", jack)
	}
	_ = r.Resolve(context.Background(), User{Platform: "feishu", ID: "ou_jack"})
	if d.calls != 2 {
		t.Fatalf("lookups should be cached, got %d calls", d.calls)
	}
	x := r.Resolve(context.Background(), User{Platform: "feishu", ID: "ou_missing", Name: "from-event"})
	if x.Role != RoleVisitor || x.Name != "from-event" || x.Resolved {
		t.Fatalf("unresolvable user: %+v", x)
	}
}
