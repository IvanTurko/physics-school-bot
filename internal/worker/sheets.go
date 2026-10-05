package worker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/IvanTurko/physics-school-bot/internal/domain"
)

// Leads keeps the bookings and which version of each the sheet shows.
type Leads interface {
	Unsynced(ctx context.Context) ([]domain.Lead, error)
	MarkSynced(ctx context.Context, bookingID int64, version int) error
}

// Sheet writes rows of cells by row number, counted from 1.
type Sheet interface {
	WriteRows(ctx context.Context, rows map[int64][]any) error
}

// header is the sheet's first row; booking N takes row N+1.
var header = []any{"№", "Создана", "Имя", "Telegram", "Телефон", "Класс", "Цель", "Урок", "Статус"}

var statusNames = map[domain.Status]string{
	domain.StatusNew:       "🆕 Ждёт подтверждения",
	domain.StatusConfirmed: "✅ Подтверждена",
	domain.StatusCancelled: "❌ Отменена",
	domain.StatusDone:      "🎓 Урок проведён",
	domain.StatusNoShow:    "🚫 Не пришёл",
}

// SheetSync keeps the sheet up to date with the bookings.
type SheetSync struct {
	leads Leads
	sheet Sheet
	tz    *time.Location
	log   *slog.Logger
}

// NewSheetSync creates a SheetSync that writes times in tz.
func NewSheetSync(leads Leads, sheet Sheet, tz *time.Location, log *slog.Logger) *SheetSync {
	return &SheetSync{leads: leads, sheet: sheet, tz: tz, log: log}
}

// Tick writes the bookings changed since they were last written and records
// their versions; with nothing changed it sends nothing.
func (s *SheetSync) Tick(ctx context.Context) error {
	leads, err := s.leads.Unsynced(ctx)
	if err != nil {
		return fmt.Errorf("cannot read unsynced bookings: %w", err)
	}
	if len(leads) == 0 {
		return nil
	}
	rows := map[int64][]any{1: header}
	for _, l := range leads {
		rows[l.ID+1] = s.row(l)
	}
	if err := s.sheet.WriteRows(ctx, rows); err != nil {
		return fmt.Errorf("cannot write sheet: %w", err)
	}
	for _, l := range leads {
		if err := s.leads.MarkSynced(ctx, l.ID, l.Version); err != nil {
			return fmt.Errorf("cannot mark booking %d synced: %w", l.ID, err)
		}
	}
	return nil
}

func (s *SheetSync) row(l domain.Lead) []any {
	return []any{
		l.ID, at(l.Booked, s.tz), l.User.Name, handle(l.User.Username), domain.FormatPhone(l.Phone),
		l.Grade, l.Goal, at(l.Start, s.tz), statusNames[l.Status],
	}
}

func at(t time.Time, tz *time.Location) string { return t.In(tz).Format("02.01 15:04") }

func handle(username string) string {
	if username == "" {
		return ""
	}
	return "@" + username
}

// Run syncs at every tick until ctx ends.
func (s *SheetSync) Run(ctx context.Context, ticks <-chan time.Time) {
	every(ctx, ticks, s.Tick, s.log, "sheet sync")
}
