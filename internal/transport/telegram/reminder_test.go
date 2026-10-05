package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/IvanTurko/physics-school-bot/internal/domain"
	tg "github.com/IvanTurko/physics-school-bot/internal/provider/telegram"
)

// lostHistory fails every append.
type lostHistory struct{}

func (lostHistory) Append(context.Context, int64, ...domain.Message) error {
	return errors.New("disk is full")
}

// delivering is a sender that lets every message through at once.
func delivering() *sender {
	s := &sender{release: make(chan struct{})}
	close(s.release)
	return s
}

func TestReminderTellsStudent(t *testing.T) {
	s, saved := delivering(), diary{}
	if err := NewReminder(s, saved, msk, quiet).Remind(context.Background(), booking); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(s.sent) != 1 || s.sent[0].ChatID != 1001 || !strings.Contains(s.sent[0].Text, "07.10 19:00") {
		t.Fatalf("sent %+v, want one message to chat 1001 at 07.10 19:00", s.sent)
	}
	if h := saved[3]; len(saved) != 1 || len(h) != 1 || h[0].Role != domain.RoleAssistant || h[0].Content != s.sent[0].Text {
		t.Fatalf("history %+v, want the sent text as the assistant's, for user 3 only", saved)
	}
}

func TestReminderNotSent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		wantErr bool
	}{
		{"blocked", &tg.APIError{Code: 403}, false},
		{"server error", &tg.APIError{Code: 500}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saved := diary{}
			err := NewReminder(&sender{err: tc.err}, saved, msk, quiet).Remind(context.Background(), booking)
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, want error: %t", err, tc.wantErr)
			}
			if len(saved) != 0 {
				t.Errorf("history %v, want nothing: the reminder was not sent", saved)
			}
		})
	}
}

func TestReminderCountsWithoutHistory(t *testing.T) {
	if err := NewReminder(delivering(), lostHistory{}, msk, quiet).Remind(context.Background(), booking); err != nil {
		t.Fatalf("err = %v, want nil: the reminder was sent", err)
	}
}
