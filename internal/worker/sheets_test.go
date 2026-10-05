package worker_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/IvanTurko/physics-school-bot/internal/domain"
	"github.com/IvanTurko/physics-school-bot/internal/worker"
)

// ledger keeps the unsynced bookings and the versions marked; readErr and
// markErr fail its calls.
type ledger struct {
	leads            []domain.Lead
	marked           []string // "3@2"
	readErr, markErr error
}

func (l *ledger) Unsynced(context.Context) ([]domain.Lead, error) { return l.leads, l.readErr }

func (l *ledger) MarkSynced(_ context.Context, id int64, version int) error {
	if l.markErr != nil {
		return l.markErr
	}
	l.marked = append(l.marked, fmt.Sprintf("%d@%d", id, version))
	return nil
}

// paper records the rows written; err fails the write.
type paper struct {
	rows map[int64][]any
	err  error
}

func (p *paper) WriteRows(_ context.Context, rows map[int64][]any) error {
	p.rows = rows
	return p.err
}

var msk = time.FixedZone("MSK", 3*60*60)

// lead is booking id at version; its times are in UTC, and the sheet writes them in MSK.
func lead(id int64, version int, status domain.Status, u domain.User) domain.Lead {
	v := domain.BookingView{
		Booking: domain.Booking{ID: id, Grade: 9, Goal: domain.GoalOGE, Phone: "+79161234567", Status: status},
		Start:   time.Date(2026, 10, 7, 13, 0, 0, 0, time.UTC),
		User:    u,
	}
	return domain.Lead{BookingView: v, Booked: time.Date(2026, 10, 5, 11, 2, 0, 0, time.UTC), Version: version}
}

// syncOnce runs one pass of the sheet sync.
func syncOnce(leads *ledger, sheet *paper) error {
	return worker.NewSheetSync(leads, sheet, msk, slog.New(slog.DiscardHandler)).Tick(context.Background())
}

func TestSheetSyncWritesChangedBookings(t *testing.T) {
	leads := &ledger{leads: []domain.Lead{
		lead(1, 1, domain.StatusNew, domain.User{Name: "Иван", Username: "ivan"}),
		lead(3, 2, domain.StatusConfirmed, domain.User{Name: "Мария"}),
	}}
	sheet := &paper{}
	if err := syncOnce(leads, sheet); err != nil {
		t.Fatal(err)
	}
	want := map[int64][]any{
		1: {"№", "Создана", "Имя", "Telegram", "Телефон", "Класс", "Цель", "Урок", "Статус"},
		2: {int64(1), "05.10 14:02", "Иван", "@ivan", "+7 916 123-45-67", 9, domain.GoalOGE, "07.10 16:00", "🆕 Ждёт подтверждения"},
		4: {int64(3), "05.10 14:02", "Мария", "", "+7 916 123-45-67", 9, domain.GoalOGE, "07.10 16:00", "✅ Подтверждена"},
	}
	if !reflect.DeepEqual(sheet.rows, want) {
		t.Errorf("rows %v, want %v", sheet.rows, want)
	}
	if !slices.Equal(leads.marked, []string{"1@1", "3@2"}) {
		t.Errorf("marked %v, want 1@1 and 3@2", leads.marked)
	}
}

func TestSheetSyncSendsNothingUnchanged(t *testing.T) {
	sheet := &paper{}
	if err := syncOnce(&ledger{}, sheet); err != nil {
		t.Fatal(err)
	}
	if sheet.rows != nil {
		t.Fatalf("wrote %v with nothing changed", sheet.rows)
	}
}

func TestSheetSyncReportsFailures(t *testing.T) {
	failure := errors.New("disk is full")
	one := []domain.Lead{lead(1, 1, domain.StatusNew, domain.User{Name: "Иван"})}
	for _, tc := range []struct {
		name  string
		leads *ledger
		sheet *paper
	}{
		{"read fails", &ledger{readErr: failure}, &paper{}},
		{"write fails", &ledger{leads: one}, &paper{err: failure}},
		{"mark fails", &ledger{leads: one, markErr: failure}, &paper{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := syncOnce(tc.leads, tc.sheet); !errors.Is(err, failure) || tc.leads.marked != nil {
				t.Fatalf("err = %v, marked %v; want the failure and nothing marked", err, tc.leads.marked)
			}
		})
	}
}
