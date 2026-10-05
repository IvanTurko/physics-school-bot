package service

import (
	"context"
	"slices"
	"time"

	"github.com/IvanTurko/physics-school-bot/internal/clock"
)

// DemoTerm is how long /demo makes a user an admin.
const DemoTerm = 24 * time.Hour

// AccessStore keeps the demo admins' rights.
type AccessStore interface {
	SetDemoUntil(ctx context.Context, userID int64, until time.Time) error
	DemoAdmins(ctx context.Context, now time.Time) ([]int64, error)
}

// Access knows who is an admin: the configured ones and the demo ones within their term.
type Access struct {
	store  AccessStore
	clock  clock.Clock
	admins []int64
}

// NewAccess creates Access over the configured admins.
func NewAccess(store AccessStore, c clock.Clock, admins []int64) *Access {
	return &Access{store: store, clock: c, admins: admins}
}

// Admins returns the Telegram IDs of the admins now, each once.
func (a *Access) Admins(ctx context.Context) ([]int64, error) {
	demo, err := a.store.DemoAdmins(ctx, a.clock.Now())
	if err != nil {
		return nil, err
	}
	ids := slices.Concat(a.admins, demo)
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

// GrantDemo makes the user an admin for DemoTerm and returns when it ends.
func (a *Access) GrantDemo(ctx context.Context, userID int64) (time.Time, error) {
	until := a.clock.Now().Add(DemoTerm)
	if err := a.store.SetDemoUntil(ctx, userID, until); err != nil {
		return time.Time{}, err
	}
	return until, nil
}
