package telegram

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	tg "github.com/IvanTurko/physics-school-bot/internal/provider/telegram"
)

var quiet = slog.New(slog.DiscardHandler)

func TestDispatcherKeepsOrderPerUser(t *testing.T) {
	var mu sync.Mutex
	var seen []int64
	d := NewDispatcher(context.Background(), func(_ context.Context, u tg.Update) {
		time.Sleep(time.Millisecond) // give a reordering a chance to show
		mu.Lock()
		seen = append(seen, u.UpdateID)
		mu.Unlock()
	}, quiet)

	for i := range int64(20) {
		d.Push(1, tg.Update{UpdateID: i})
	}
	d.Shutdown(time.Now().Add(time.Second))

	if len(seen) != 20 {
		t.Fatalf("handled %d updates, want 20", len(seen))
	}
	for i, id := range seen {
		if id != int64(i) {
			t.Fatalf("order = %v", seen)
		}
	}
}

func TestDispatcherRunsUsersInParallel(t *testing.T) {
	release := make(chan struct{})
	started := make(chan int64, 2)
	d := NewDispatcher(context.Background(), func(_ context.Context, u tg.Update) {
		started <- u.UpdateID
		<-release
	}, quiet)

	d.Push(1, tg.Update{UpdateID: 1})
	d.Push(2, tg.Update{UpdateID: 2})
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("a slow user blocks another one")
		}
	}
	close(release)
	d.Shutdown(time.Now().Add(time.Second))
}

func TestDispatcherShutdownCancelsAtDeadline(t *testing.T) {
	d := NewDispatcher(context.Background(), func(ctx context.Context, _ tg.Update) { <-ctx.Done() }, quiet)
	d.Push(1, tg.Update{})

	done := make(chan struct{})
	go func() {
		d.Shutdown(time.Now().Add(10 * time.Millisecond))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not cancel a stuck handler")
	}
}

func TestDispatcherSurvivesPanic(t *testing.T) {
	var handled int
	d := NewDispatcher(context.Background(), func(_ context.Context, u tg.Update) {
		if u.UpdateID == 1 {
			panic("boom")
		}
		handled++
	}, quiet)
	d.Push(1, tg.Update{UpdateID: 1})
	d.Push(1, tg.Update{UpdateID: 2})
	d.Shutdown(time.Now().Add(time.Second))
	if handled != 1 {
		t.Fatalf("handled = %d, want the update after the panic handled", handled)
	}
}

func TestKeyTakesPressesAndPrivateMessagesWithSender(t *testing.T) {
	private := tg.Update{Message: &tg.Message{From: &tg.User{ID: 7}, Chat: tg.Chat{ID: 7, Type: tg.ChatPrivate}}}
	group := tg.Update{Message: &tg.Message{From: &tg.User{ID: 7}, Chat: tg.Chat{ID: -100, Type: "supergroup"}}}

	if id, ok := Key(private); !ok || id != 7 {
		t.Errorf("private: %d, %v; want 7, true", id, ok)
	}
	if _, ok := Key(group); ok {
		t.Error("group message accepted")
	}
	if _, ok := Key(tg.Update{Message: &tg.Message{Chat: tg.Chat{ID: 7, Type: tg.ChatPrivate}}}); ok {
		t.Error("message without a sender accepted")
	}
	if id, ok := Key(tg.Update{CallbackQuery: &tg.CallbackQuery{From: tg.User{ID: 1}}}); !ok || id != 1 {
		t.Errorf("button press: %d, %v; want 1, true", id, ok)
	}
}
