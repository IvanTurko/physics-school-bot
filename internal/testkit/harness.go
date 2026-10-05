package testkit

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/IvanTurko/physics-school-bot/internal/app"
	"github.com/IvanTurko/physics-school-bot/internal/clock"
	"github.com/IvanTurko/physics-school-bot/internal/config"
	"github.com/IvanTurko/physics-school-bot/internal/domain"
	"github.com/IvanTurko/physics-school-bot/internal/storage/sqlite"
	"golang.org/x/oauth2"
)

// Options set up the bot a Harness runs.
type Options struct {
	Now      time.Time // where the bot's clock starts
	AdminIDs []int64
	Demo     bool // DEMO_MODE: /demo works, and the demo schedule fills slots
}

// Harness runs the whole bot against fake Telegram, model and Sheets servers.
type Harness struct {
	TG    *FakeTelegram
	LLM   *FakeLLM
	Sheet *FakeSheets
	Clock *clock.Fake // the bot's clock, which stands still until moved

	t       testing.TB
	tz      *time.Location
	slots   *sqlite.Store
	cancel  context.CancelFunc
	done    chan struct{}  // closed when Run returns
	minutes chan time.Time // ticks the reminders
	syncs   chan time.Time // ticks the sheet sync
	err     error
}

// Start runs the bot until the test ends.
func Start(t testing.TB, o Options) *Harness {
	t.Helper()
	h := &Harness{
		TG: newFakeTelegram(t), LLM: newFakeLLM(t), Sheet: newFakeSheets(t), Clock: clock.NewFake(o.Now),
		t: t, tz: time.FixedZone("MSK", 3*60*60), done: make(chan struct{}),
		minutes: make(chan time.Time), syncs: make(chan time.Time),
	}
	path := filepath.Join(t.TempDir(), "bot.db")

	db, err := sqlite.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	h.slots = sqlite.New(db, h.Clock)

	// One ctx for New and Run: the stop must reach what New started, too.
	ctx, cancel := context.WithCancel(t.Context())
	h.cancel = cancel
	// DemoMode only when asked: its ticker would run beside the fake clock.
	a, err := app.New(ctx, config.Config{
		TelegramToken:  "123:test",
		TelegramAPIURL: h.TG.srv.URL,
		AdminIDs:       o.AdminIDs,
		LLMBaseURL:     h.LLM.srv.URL,
		LLMAPIKey:      "test",
		LLMModel:       "test",
		SheetsAPIURL:   h.Sheet.srv.URL,
		SpreadsheetID:  "sheet",
		DBPath:         path,
		TZ:             h.tz,
		DemoMode:       o.Demo,
	}, app.Deps{
		Clock: h.Clock, Log: slog.New(slog.DiscardHandler), Minutes: h.minutes, Syncs: h.syncs,
		GoogleTokens: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "token"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		h.err = a.Run(ctx)
		close(h.done)
	}()
	t.Cleanup(func() {
		h.Stop()
		h.Wait()
		if err := a.Close(); err != nil {
			t.Error(err)
		}
	})
	return h
}

// AddSlot adds a free slot at a time in the school's zone, like 2025-10-08 19:00.
func (h *Harness) AddSlot(at string) {
	h.t.Helper()
	start, err := domain.ParseSlotTime(at, h.tz)
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.slots.AddSlots(h.t.Context(), []time.Time{start}); err != nil {
		h.t.Fatal(err)
	}
}

// Remind runs a pass of the reminders and waits until it ends.
func (h *Harness) Remind() {
	h.t.Helper()
	h.pass(h.minutes, "reminders")
}

// Sync runs a pass of the sheet sync and waits until it ends.
func (h *Harness) Sync() {
	h.t.Helper()
	h.pass(h.syncs, "sheet sync")
}

// pass runs a pass of the worker that reads ticks and waits until it ends.
func (h *Harness) pass(ticks chan<- time.Time, name string) {
	h.t.Helper()
	for range 2 { // the worker takes the second tick only when the first pass ends
		select {
		case ticks <- time.Time{}:
		case <-time.After(wait):
			h.t.Fatalf("%s not run within %v", name, wait)
		}
	}
}

// Stop cancels the run as a stop signal does and waits until the bot stops
// polling; handlers already started go on.
func (h *Harness) Stop() {
	h.t.Helper()
	h.cancel()
	if !h.TG.await(context.Background(), func() bool { return h.TG.polling == 0 }) {
		h.t.Fatalf("bot still polls %v after the stop", wait)
	}
}

// Wait waits for the run to return after Stop.
func (h *Harness) Wait() {
	h.t.Helper()
	select {
	case <-h.done:
	case <-time.After(wait):
		h.t.Fatalf("bot still runs %v after the stop", wait)
	}
	if h.err != nil {
		h.t.Fatalf("run failed: %v", h.err)
	}
}
