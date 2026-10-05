package service_test

import (
	"slices"
	"testing"

	"github.com/IvanTurko/physics-school-bot/internal/service"
)

func TestAdminsIncludeDemoAdminsForTheirTerm(t *testing.T) {
	f := setup(t)
	access := service.NewAccess(f.store, f.clock, []int64{1, 2})
	for _, tgID := range []int64{1, 3} {
		until, err := access.GrantDemo(ctx, f.user(t, tgID))
		if err != nil {
			t.Fatal(err)
		}
		if want := now.Add(service.DemoTerm); !until.Equal(want) {
			t.Fatalf("until = %v, want %v", until, want)
		}
	}
	if got := admins(t, access); !slices.Equal(got, []int64{1, 2, 3}) {
		t.Fatalf("admins = %v, want the configured and the demo ones, each once", got)
	}

	f.clock.Advance(service.DemoTerm)
	if got := admins(t, access); !slices.Equal(got, []int64{1, 2}) {
		t.Fatalf("admins after the term = %v, want only the configured ones", got)
	}
}

func admins(t *testing.T, a *service.Access) []int64 {
	ids, err := a.Admins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return ids
}
