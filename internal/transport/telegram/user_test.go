package telegram

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/IvanTurko/physics-school-bot/internal/agent"
	"github.com/IvanTurko/physics-school-bot/internal/domain"
	tg "github.com/IvanTurko/physics-school-bot/internal/provider/telegram"
)

// recorder keeps the texts sent while their ctx is alive and ignores typing.
type recorder struct{ sent []string }

func (r *recorder) SendMessage(ctx context.Context, m tg.SendMessage) (tg.Message, error) {
	if err := ctx.Err(); err != nil {
		return tg.Message{}, err
	}
	r.sent = append(r.sent, m.Text)
	return tg.Message{}, nil
}

func (r *recorder) SendTyping(context.Context, int64) error { return nil }

// typist records when each typing and message is sent, counted from start.
type typist struct {
	start time.Time
	mu    sync.Mutex
	calls []string // "4s typing to 42" or "9s <text>"
}

func (ty *typist) SendTyping(_ context.Context, chatID int64) error {
	ty.record(fmt.Sprintf("typing to %d", chatID))
	return nil
}

func (ty *typist) SendMessage(_ context.Context, m tg.SendMessage) (tg.Message, error) {
	ty.record(m.Text)
	time.Sleep(5 * time.Second) // send slowly, so typing left running lands after the reply
	return tg.Message{}, nil
}

func (ty *typist) record(what string) {
	ty.mu.Lock()
	defer ty.mu.Unlock()
	ty.calls = append(ty.calls, fmt.Sprintf("%v %s", time.Since(ty.start), what))
}

func (ty *typist) Calls() []string {
	ty.mu.Lock()
	defer ty.mu.Unlock()
	return slices.Clone(ty.calls)
}

type users struct{ err error }

func (u users) EnsureUser(context.Context, int64, string, string) (domain.User, error) {
	return domain.User{ID: 7}, u.err
}

type agentFunc func(ctx context.Context, userID int64, text string) (agent.Reply, error)

func (f agentFunc) Handle(ctx context.Context, userID int64, text string) (agent.Reply, error) {
	return f(ctx, userID, text)
}

func message(text string) tg.Update {
	return tg.Update{Message: &tg.Message{From: &tg.User{ID: 42}, Chat: tg.Chat{ID: 42, Type: tg.ChatPrivate}, Text: text}}
}

func TestBotRepliesWithAgentAnswer(t *testing.T) {
	r := &recorder{}
	bot := NewBot(r, agentFunc(func(ctx context.Context, userID int64, text string) (agent.Reply, error) {
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > Deadline {
			t.Errorf("deadline = %v, %v; want at most %v ahead", deadline, ok, Deadline)
		}
		if userID != 7 || text != "привет" {
			t.Errorf("agent got user %d, %q", userID, text)
		}
		return agent.Reply{Text: "Здравствуйте!"}, nil
	}), users{}, false, quiet)

	bot.Handle(context.Background(), message("привет"))
	if !slices.Equal(r.sent, []string{"Здравствуйте!"}) {
		t.Fatalf("sent %q, want the agent's answer", r.sent)
	}
}

func TestBotTypesUntilItReplies(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ty := &typist{start: time.Now()}
		bot := NewBot(ty, agentFunc(func(context.Context, int64, string) (agent.Reply, error) {
			time.Sleep(9 * time.Second) // not a multiple of 4s, so no tick races the reply
			return agent.Reply{Text: "Здравствуйте!"}, nil
		}), users{}, false, quiet)

		bot.Handle(context.Background(), message("привет"))
		want := []string{"0s typing to 42", "4s typing to 42", "8s typing to 42", "9s Здравствуйте!"}
		if calls := ty.Calls(); !slices.Equal(calls, want) {
			t.Errorf("calls %q, want typing to chat 42 at 0s, 4s and 8s, then the reply at 9s", calls)
		}
	})
}

func TestBotAnswersWithoutModel(t *testing.T) {
	for _, tc := range []struct{ name, text, want string }{
		{"non-text", "", textOnly},
		{"start", "/start", greeting},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &recorder{}
			bot := NewBot(r, agentFunc(func(context.Context, int64, string) (agent.Reply, error) {
				t.Error("the message reached the model")
				return agent.Reply{}, nil
			}), users{}, false, quiet)

			bot.Handle(context.Background(), message(tc.text))
			if !slices.Equal(r.sent, []string{tc.want}) {
				t.Fatalf("sent %q, want %q", r.sent, tc.want)
			}
		})
	}
}

func TestBotApologizesWhenUserNotSaved(t *testing.T) {
	r := &recorder{}
	NewBot(r, nil, users{err: errors.New("disk is full")}, false, quiet).Handle(context.Background(), message("привет"))
	if !slices.Equal(r.sent, []string{replyFailed}) {
		t.Fatalf("sent %q, want the apology", r.sent)
	}
}

func TestBotApologizesPastTheDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &recorder{}
	bot := NewBot(r, agentFunc(func(context.Context, int64, string) (agent.Reply, error) {
		cancel()
		return agent.Reply{}, context.Canceled
	}), users{}, false, quiet)

	bot.Handle(ctx, message("привет"))
	if !slices.Equal(r.sent, []string{replyFailed}) {
		t.Fatalf("sent %q, want the apology on a fresh deadline", r.sent)
	}
}
