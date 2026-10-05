package telegram

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/IvanTurko/physics-school-bot/internal/domain"
	tg "github.com/IvanTurko/physics-school-bot/internal/provider/telegram"
)

// registry records the registrations, grants and replies in order; the step
// named by fail is recorded and fails.
type registry struct {
	fail  string
	calls []string
}

func (r *registry) EnsureUser(_ context.Context, tgID int64, username, name string) (domain.User, error) {
	r.calls = append(r.calls, fmt.Sprintf("user %d %s %s", tgID, username, name))
	if r.fail == "user" {
		return domain.User{}, errors.New("disk is full")
	}
	return domain.User{ID: 3}, nil
}

func (r *registry) GrantDemo(_ context.Context, userID int64) (time.Time, error) {
	r.calls = append(r.calls, fmt.Sprintf("grant %d", userID))
	if r.fail == "grant" {
		return time.Time{}, errors.New("disk is full")
	}
	return time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC), nil
}

func (r *registry) SendMessage(_ context.Context, m tg.SendMessage) (tg.Message, error) {
	r.calls = append(r.calls, fmt.Sprintf("send %d %s", m.ChatID, m.Text))
	return tg.Message{}, nil
}

// demo sends /demo to a Demo over r from user 1001.
func (r *registry) demo() {
	m := &tg.Message{From: &tg.User{ID: 1001, Username: "vasya", FirstName: "Вася"}, Chat: tg.Chat{ID: 1001}, Text: "/demo"}
	NewDemo(r, r, r, time.FixedZone("MSK", 3*60*60), quiet).Handle(context.Background(), m)
}

func TestDemoGrantsRights(t *testing.T) {
	r := &registry{}
	r.demo()
	want := []string{"user 1001 vasya Вася", "grant 3", "send 1001 " + fmt.Sprintf(demoGranted, "08.10 12:00")}
	if !slices.Equal(r.calls, want) {
		t.Fatalf("calls = %q, want %q", r.calls, want)
	}
}

func TestDemoFailures(t *testing.T) {
	for _, tc := range []struct {
		name, fail string
		want       []string
	}{
		{"user not saved", "user", []string{"user 1001 vasya Вася", "send 1001 " + replyFailed}},
		{"rights not saved", "grant", []string{"user 1001 vasya Вася", "grant 3", "send 1001 " + replyFailed}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &registry{fail: tc.fail}
			r.demo()
			if !slices.Equal(r.calls, tc.want) {
				t.Fatalf("calls = %q, want %q", r.calls, tc.want)
			}
		})
	}
}
