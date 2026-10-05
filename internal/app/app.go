// Package app assembles the bot from its parts.
package app

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/IvanTurko/physics-school-bot/internal/agent"
	"github.com/IvanTurko/physics-school-bot/internal/clock"
	"github.com/IvanTurko/physics-school-bot/internal/config"
	"github.com/IvanTurko/physics-school-bot/internal/provider/llm"
	"github.com/IvanTurko/physics-school-bot/internal/provider/sheets"
	tgapi "github.com/IvanTurko/physics-school-bot/internal/provider/telegram"
	"github.com/IvanTurko/physics-school-bot/internal/service"
	"github.com/IvanTurko/physics-school-bot/internal/storage/sqlite"
	"github.com/IvanTurko/physics-school-bot/internal/transport/telegram"
	"github.com/IvanTurko/physics-school-bot/internal/worker"
	"github.com/IvanTurko/physics-school-bot/pkg/httpx"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// ShutdownGrace is how long the bot finishes started work and queued cards
// after a stop signal.
const ShutdownGrace = 30 * time.Second

// Deps are the parts New takes from outside; a zero field takes the real one.
type Deps struct {
	Clock   clock.Clock
	Log     *slog.Logger
	Minutes <-chan time.Time // ticks the reminders
	Syncs   <-chan time.Time // ticks the sheet sync

	GoogleTokens oauth2.TokenSource // authorizes the sheet's requests
}

// App is the assembled bot.
type App struct {
	log        *slog.Logger
	db         *sql.DB
	poll       *tgapi.Client
	dispatcher *telegram.Dispatcher
	notifier   *telegram.Notifier
	background []func(ctx context.Context)
}

// New opens the database and builds every part of the bot.
func New(ctx context.Context, cfg config.Config, deps Deps) (*App, error) {
	if deps.Clock == nil {
		deps.Clock = clock.Real{}
	}
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	if deps.Minutes == nil {
		deps.Minutes = time.NewTicker(time.Minute).C // never stopped: it lives as long as the bot
	}
	if deps.Syncs == nil {
		deps.Syncs = time.NewTicker(time.Minute).C // never stopped: it lives as long as the bot
	}
	if cfg.SpreadsheetID != "" && deps.GoogleTokens == nil {
		tokens, err := googleTokens(ctx, cfg.GoogleKeyPath)
		if err != nil {
			return nil, err
		}
		deps.GoogleTokens = tokens
	}

	db, err := sqlite.Open(ctx, cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("cannot open database: %w", err)
	}
	store := sqlite.New(db, deps.Clock)

	poll := tgapi.New(cfg.TelegramAPIURL, cfg.TelegramToken,
		httpx.Retrying(httpx.New(httpx.WithTimeout(tgapi.PollTimeout)), tgapi.PollPolicy()))
	send := tgapi.New(cfg.TelegramAPIURL, cfg.TelegramToken,
		httpx.Retrying(httpx.New(httpx.WithTimeout(tgapi.SendTimeout)), tgapi.SendPolicy()))
	model := llm.New(cfg.LLMBaseURL, cfg.LLMAPIKey, cfg.LLMModel,
		httpx.Retrying(httpx.New(httpx.WithTimeout(llm.Timeout)), llm.Policy()))

	// Detach handlers and cards from the stop signal: Shutdown gives them the grace.
	lasting := context.WithoutCancel(ctx)
	access := service.NewAccess(store, deps.Clock, cfg.AdminIDs)
	notifier := telegram.NewNotifier(lasting, send, store, store, access, deps.Clock, cfg.TZ, deps.Log)
	bookings := service.New(store, deps.Clock, cfg.TZ, notifier)
	ag := agent.New(model, store, deps.Clock, cfg.TZ, agent.BookingTools(bookings, cfg.TZ)...)
	bot := telegram.NewBot(send, ag, store, cfg.DemoMode, deps.Log)
	admin := telegram.NewAdmin(send, bookings, store, store, store, notifier, access, deps.Clock, cfg.TZ, deps.Log)
	demo := telegram.NewDemo(send, store, access, cfg.TZ, deps.Log)
	reminders := worker.NewReminders(store, telegram.NewReminder(send, store, cfg.TZ, deps.Log), deps.Clock, cfg.DemoMode, deps.Log)
	handle := func(ctx context.Context, u tgapi.Update) {
		switch {
		case u.CallbackQuery != nil:
			admin.Press(ctx, u.CallbackQuery)
		case cfg.DemoMode && u.Message.Text == "/demo":
			demo.Handle(ctx, u.Message)
		case u.Message.Text == "/admin":
			admin.Open(ctx, u.Message)
		case strings.HasPrefix(u.Message.Text, "/addslot"):
			admin.AddSlots(ctx, u.Message)
		default:
			bot.Handle(ctx, u)
		}
	}

	a := &App{
		log:        deps.Log,
		db:         db,
		poll:       poll,
		dispatcher: telegram.NewDispatcher(lasting, handle, deps.Log),
		notifier:   notifier,
		background: []func(ctx context.Context){
			func(ctx context.Context) { reminders.Run(ctx, deps.Minutes) },
		},
	}
	if cfg.DemoMode {
		a.background = append(a.background, worker.NewDemoSlots(store, deps.Clock, cfg.TZ, deps.Log).Run)
	}
	if cfg.SpreadsheetID != "" {
		sheet := sheets.New(cfg.SheetsAPIURL, cfg.SpreadsheetID, deps.GoogleTokens,
			httpx.Retrying(httpx.New(httpx.WithTimeout(sheets.Timeout)), sheets.Policy()))
		sheetSync := worker.NewSheetSync(store, sheet, cfg.TZ, deps.Log)
		a.background = append(a.background, func(ctx context.Context) { sheetSync.Run(ctx, deps.Syncs) })
	}
	return a, nil
}

// googleTokens returns tokens of the service account whose key is at path.
func googleTokens(ctx context.Context, path string) (oauth2.TokenSource, error) {
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read Google key: %w", err)
	}
	jwt, err := google.JWTConfigFromJSON(key, sheets.Scope)
	if err != nil {
		return nil, fmt.Errorf("invalid Google key %q: %w", path, err)
	}
	// Give the token request a timeout: it bypasses httpx.
	return jwt.TokenSource(context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Timeout: sheets.Timeout})), nil
}

// Run serves updates until ctx ends, then finishes started work within the
// shutdown grace.
func (a *App) Run(ctx context.Context) error {
	me, err := a.poll.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("cannot reach Telegram: %w", err)
	}
	a.log.Info("bot started", "username", me.Username)

	var workers sync.WaitGroup
	for _, run := range a.background {
		workers.Go(func() { run(ctx) })
	}

	telegram.Poll(ctx, a.poll, func(u tgapi.Update) {
		if user, ok := telegram.Key(u); ok {
			a.dispatcher.Push(user, u)
		}
	}, a.log)

	a.log.Info("stopping")
	deadline := time.Now().Add(ShutdownGrace)
	a.dispatcher.Shutdown(deadline) // before the notifier: handlers still push cards
	a.notifier.Shutdown(deadline)
	workers.Wait()
	return nil
}

// Close releases the database.
func (a *App) Close() error { return a.db.Close() }
