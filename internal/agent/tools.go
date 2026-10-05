package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/IvanTurko/physics-school-bot/internal/domain"
	"github.com/IvanTurko/physics-school-bot/internal/provider/llm"
	"github.com/IvanTurko/physics-school-bot/internal/service"
)

// Bookings runs the student's booking operations.
type Bookings interface {
	FreeSlots(ctx context.Context) ([]domain.Slot, error)
	Book(ctx context.Context, userID int64, in service.BookInput) (domain.BookingView, error)
	MyBooking(ctx context.Context, userID int64) (domain.BookingView, error)
	Reschedule(ctx context.Context, userID int64, slotTime string) (domain.BookingView, error)
	Cancel(ctx context.Context, userID int64) error
}

// errInvalidArguments means the model wrote arguments that are not the
// tool's JSON.
var errInvalidArguments = errors.New("invalid tool arguments")

// codes name each refusal for the model; the system prompt explains every code.
var codes = []struct {
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
	{errInvalidArguments, "invalid_arguments"},
}

// BookingTools are the tools the model books trial lessons with; they show
// times in tz.
func BookingTools(b Bookings, tz *time.Location) []Tool {
	answer := func(v domain.BookingView, err error) (any, error) {
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "booking": bookingJSON{newSlotJSON(v.Start, tz), domain.FormatPhone(v.Phone)}}, nil
	}
	none := map[string]any{}
	slotTime := text("Время слота точно как в get_free_slots, например 2026-10-07 19:00")
	slotTime["pattern"] = `^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$`
	goal := text("Цель занятий")
	goal["enum"] = domain.Goals
	return []Tool{
		tool{
			def: function("get_free_slots", "Свободное время для пробного урока, ближайшее первым.", none),
			run: func(ctx context.Context, _ int64, _ string) (any, error) {
				slots, err := b.FreeSlots(ctx)
				out := make([]slotJSON, len(slots))
				for i, s := range slots {
					out[i] = newSlotJSON(s.Start, tz)
				}
				return map[string]any{"ok": true, "slots": out}, err
			},
		},
		tool{
			def: function("book_trial", "Записывает пользователя на пробный урок.", map[string]any{
				"slot_time": slotTime,
				"grade":     param("integer", "Класс ученика, 7–11"),
				"goal":      goal,
				"phone":     text("Телефон, как его написал пользователь"),
			}),
			run: func(ctx context.Context, userID int64, args string) (any, error) {
				var in struct {
					SlotTime string `json:"slot_time"`
					Grade    int    `json:"grade"`
					Goal     string `json:"goal"`
					Phone    string `json:"phone"`
				}
				if err := decode(args, &in); err != nil {
					return nil, err
				}
				return answer(b.Book(ctx, userID, service.BookInput(in)))
			},
		},
		tool{
			def: function("get_my_booking", "Текущая активная запись пользователя.", none),
			run: func(ctx context.Context, userID int64, _ string) (any, error) {
				return answer(b.MyBooking(ctx, userID))
			},
		},
		tool{
			def: function("reschedule_my_booking", "Переносит активную запись на другое свободное время.", map[string]any{
				"slot_time": slotTime,
			}),
			run: func(ctx context.Context, userID int64, args string) (any, error) {
				var in struct {
					SlotTime string `json:"slot_time"`
				}
				if err := decode(args, &in); err != nil {
					return nil, err
				}
				return answer(b.Reschedule(ctx, userID, in.SlotTime))
			},
		},
		tool{
			def: function("cancel_my_booking", "Отменяет активную запись.", none),
			run: func(ctx context.Context, userID int64, _ string) (any, error) {
				return map[string]any{"ok": true}, b.Cancel(ctx, userID)
			},
		},
	}
}

type tool struct {
	def llm.Function
	run func(ctx context.Context, userID int64, args string) (any, error) // the answer is ignored when the error is set
}

func (t tool) Definition() llm.Function { return t.def }

// Call turns a refusal into its code for the model; any other error ends the turn.
func (t tool) Call(ctx context.Context, userID int64, args string) (string, error) {
	v, err := t.run(ctx, userID, args)
	if err != nil {
		code, ok := codeOf(err)
		if !ok {
			return "", err
		}
		v = map[string]any{"ok": false, "error": code}
	}
	b, err := json.Marshal(v)
	return string(b), err
}

func codeOf(err error) (string, bool) {
	for _, c := range codes {
		if errors.Is(err, c.err) {
			return c.code, true
		}
	}
	return "", false
}

// function describes a tool whose arguments are the given JSON Schema
// properties, all required.
func function(name, description string, props map[string]any) llm.Function {
	required := slices.AppendSeq([]string{}, maps.Keys(props)) // [] rather than null: JSON Schema allows no null here
	slices.Sort(required)
	schema := map[string]any{"type": "object", "properties": props, "required": required}
	return llm.Function{Name: name, Description: description, Parameters: schema}
}

func text(description string) map[string]any { return param("string", description) }

func param(typ, description string) map[string]any {
	return map[string]any{"type": typ, "description": description}
}

func decode(args string, v any) error {
	if err := json.Unmarshal([]byte(args), v); err != nil {
		return fmt.Errorf("%w: %w", errInvalidArguments, err)
	}
	return nil
}

type slotJSON struct {
	Time string `json:"time"` // as book_trial takes it
	Day  string `json:"day"`  // «вторник, 7 октября»
}

type bookingJSON struct {
	slotJSON

	Phone string `json:"phone"` // +7 999 123-45-67: the form the model repeats to the user
}

func newSlotJSON(start time.Time, tz *time.Location) slotJSON {
	start = start.In(tz)
	return slotJSON{Time: start.Format(domain.SlotTimeLayout), Day: day(start)}
}
