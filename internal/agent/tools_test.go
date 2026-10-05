package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IvanTurko/physics-school-bot/internal/agent"
	"github.com/IvanTurko/physics-school-bot/internal/domain"
	"github.com/IvanTurko/physics-school-bot/internal/provider/llm"
	"github.com/IvanTurko/physics-school-bot/internal/service"
	"github.com/IvanTurko/physics-school-bot/internal/storage/sqlite"
)

var msk = time.FixedZone("MSK", 3*60*60)

// bookings answers every operation with view or err and records whose it was.
type bookings struct {
	err   error
	view  domain.BookingView
	slots []domain.Slot
	users []int64
}

func (b *bookings) FreeSlots(context.Context) ([]domain.Slot, error) { return b.slots, b.err }

func (b *bookings) Book(_ context.Context, userID int64, _ service.BookInput) (domain.BookingView, error) {
	b.users = append(b.users, userID)
	return b.view, b.err
}

func (b *bookings) MyBooking(_ context.Context, userID int64) (domain.BookingView, error) {
	b.users = append(b.users, userID)
	return b.view, b.err
}

func (b *bookings) Reschedule(_ context.Context, userID int64, _ string) (domain.BookingView, error) {
	b.users = append(b.users, userID)
	return b.view, b.err
}

func (b *bookings) Cancel(_ context.Context, userID int64) error {
	b.users = append(b.users, userID)
	return b.err
}

func find(t *testing.T, tools []agent.Tool, name string) agent.Tool {
	i := slices.IndexFunc(tools, func(tl agent.Tool) bool { return tl.Definition().Name == name })
	if i < 0 {
		t.Fatalf("no tool %s", name)
	}
	return tools[i]
}

func call(id, name, args string) llm.ToolCall {
	return llm.ToolCall{ID: id, Type: "function", Function: llm.FunctionCall{Name: name, Arguments: args}}
}

func TestToolsActForTheDialogUser(t *testing.T) {
	b := &bookings{view: domain.BookingView{Booking: domain.Booking{Phone: "+79991234567"}}}
	model := &scripted{answers: []llm.Message{
		{Role: "assistant", ToolCalls: []llm.ToolCall{
			call("c1", "book_trial", `{"slot_time":"2026-10-07 19:00","grade":9,"goal":"ОГЭ","phone":"89991234567"}`),
			call("c2", "get_my_booking", `{}`),
			call("c3", "reschedule_my_booking", `{"slot_time":"2026-10-08 19:00"}`),
			call("c4", "cancel_my_booking", `{}`),
		}},
		text("Готово."),
	}}
	a, _, user := setup(t, model, agent.BookingTools(b, msk)...)

	if _, err := a.Handle(ctx, user, "запишите"); err != nil {
		t.Fatal(err)
	}
	if got, want := len(model.requests[0].Tools), len(agent.BookingTools(b, msk)); got != want {
		t.Errorf("offered %d tools, want %d", got, want)
	}
	if want := []int64{user, user, user, user}; !slices.Equal(b.users, want) {
		t.Fatalf("users = %v, want %v", b.users, want)
	}
}

func TestToolWithoutArgumentsHasEmptyRequired(t *testing.T) {
	b, err := json.Marshal(find(t, agent.BookingTools(&bookings{}, msk), "get_free_slots").Definition().Parameters)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"properties":{},"required":[],"type":"object"}`; string(b) != want {
		t.Fatalf("schema = %s, want %s", b, want)
	}
}

func TestToolRefusalCodes(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{domain.ErrInvalidPhone, "invalid_phone"},
		{domain.ErrInvalidGrade, "invalid_grade"},
		{domain.ErrInvalidGoal, "invalid_goal"},
		{domain.ErrInvalidSlotTime, "invalid_time_format"},
		{domain.ErrSlotNotFound, "slot_not_found"},
		{domain.ErrSlotTaken, "slot_taken"},
		{domain.ErrTooSoon, "too_soon"},
		{domain.ErrSameSlot, "same_slot"},
		{domain.ErrAlreadyBooked, "already_booked"},
		{domain.ErrNoActiveBooking, "no_active_booking"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			b := &bookings{err: fmt.Errorf("context: %w", tc.err)}
			got, err := find(t, agent.BookingTools(b, msk), "get_my_booking").Call(ctx, 1, `{}`)
			if want := `{"error":"` + tc.code + `","ok":false}`; err != nil || got != want {
				t.Fatalf("got %s, %v; want %s", got, err, want)
			}
		})
	}
}

func TestBookingAnswerFormatsPhone(t *testing.T) {
	b := &bookings{view: domain.BookingView{
		Booking: domain.Booking{Phone: "+79991234567"},
		Start:   time.Date(2026, 10, 7, 16, 0, 0, 0, time.UTC),
	}}
	got, err := find(t, agent.BookingTools(b, msk), "get_my_booking").Call(ctx, 1, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"booking":{"time":"2026-10-07 19:00","day":"среда, 7 октября","phone":"+7 999 123-45-67"},"ok":true}`; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestToolInvalidArguments(t *testing.T) {
	got, err := find(t, agent.BookingTools(&bookings{}, msk), "book_trial").Call(ctx, 1, `{"grade":"девятый"}`)
	if err != nil || !strings.Contains(got, "invalid_arguments") {
		t.Fatalf("got %s, %v; want invalid_arguments", got, err)
	}
}

func TestToolFailureEndsTurn(t *testing.T) {
	failure := errors.New("disk is full")
	_, err := find(t, agent.BookingTools(&bookings{err: failure}, msk), "cancel_my_booking").Call(ctx, 1, `{}`)
	if !errors.Is(err, failure) {
		t.Fatalf("err = %v, want the failure itself", err)
	}
}

func TestFreeSlotsToolWritesBookableTime(t *testing.T) {
	b := &bookings{slots: []domain.Slot{{Start: time.Date(2026, 10, 7, 16, 0, 0, 0, time.UTC)}}}
	got, err := find(t, agent.BookingTools(b, msk), "get_free_slots").Call(ctx, 1, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"ok":true,"slots":[{"time":"2026-10-07 19:00","day":"среда, 7 октября"}]}`; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestHandleGivesModelTheProfile(t *testing.T) {
	for _, tc := range []struct {
		status domain.Status
		want   string
	}{
		{domain.StatusNew, "ждёт подтверждения администратора"},
		{domain.StatusConfirmed, "подтверждена администратором"},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			model := &scripted{answers: []llm.Message{text("Вы записаны на среду.")}}
			a, store, user := setup(t, model)
			addBooking(t, store, user, time.Date(2026, 10, 7, 16, 0, 0, 0, time.UTC), tc.status)

			if _, err := a.Handle(ctx, user, "когда урок?"); err != nil {
				t.Fatal(err)
			}
			system := model.requests[0].Messages[0].Content
			for _, want := range []string{"среда, 7 октября, 16:00, " + tc.want, "+7 999 123-45-67"} {
				if !strings.Contains(system, want) {
					t.Errorf("system prompt lacks %q", want)
				}
			}
		})
	}
}

func TestHandleTellsModelTrialHeld(t *testing.T) {
	for _, tc := range []struct {
		status domain.Status
		held   bool
	}{
		{domain.StatusDone, true},
		{domain.StatusNoShow, false},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			model := &scripted{answers: []llm.Message{text("Расскажу о занятиях.")}}
			a, store, user := setup(t, model)
			addBooking(t, store, user, time.Date(2026, 10, 7, 16, 0, 0, 0, time.UTC), tc.status)

			if _, err := a.Handle(ctx, user, "дорого"); err != nil {
				t.Fatal(err)
			}
			system := model.requests[0].Messages[0].Content
			if got := strings.Contains(system, "Пробный урок уже прошёл."); got != tc.held {
				t.Errorf("trial held in system prompt = %v, want %v", got, tc.held)
			}
		})
	}
}

func addBooking(t *testing.T, store *sqlite.Store, user int64, start time.Time, status domain.Status) {
	t.Helper()
	if _, err := store.AddSlots(ctx, []time.Time{start}); err != nil {
		t.Fatal(err)
	}
	slot, err := store.SlotAt(ctx, start)
	if err != nil {
		t.Fatal(err)
	}
	booking := domain.Booking{UserID: user, SlotID: slot.ID, Grade: 9, Goal: domain.GoalOGE, Phone: "+79991234567", Status: status}
	if _, err := store.InsertBooking(ctx, booking); err != nil {
		t.Fatal(err)
	}
}
