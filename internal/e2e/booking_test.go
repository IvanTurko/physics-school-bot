package e2e

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IvanTurko/physics-school-bot/internal/testkit"
)

const (
	user, admin = 1001, 1
	slot        = "2025-10-08 19:00"
	bookTrial   = `{"slot_time":"2025-10-08 19:00","grade":9,"goal":"ОГЭ","phone":"8 999 123 45 67"}`
)

var now = time.Date(2025, 10, 6, 12, 0, 0, 0, time.FixedZone("MSK", 3*60*60))

func start(t *testing.T) *testkit.Harness {
	h := testkit.Start(t, testkit.Options{Now: now, AdminIDs: []int64{admin}})
	h.AddSlot(slot)
	return h
}

func TestBookingFlow(t *testing.T) {
	h := start(t)

	h.LLM.Reply(testkit.ToolCall("get_free_slots", "{}"))
	h.LLM.Reply(testkit.Text("Есть среда 19:00. Подходит?"))
	h.TG.UserSays(user, "9 класс, ОГЭ, хотим на неделе вечером")
	if system := h.LLM.WaitRequest().Messages[0].Content; !strings.Contains(system, "6 октября 2025, 12:00") {
		t.Errorf("system prompt lacks the school's time 6 октября 2025, 12:00")
	}
	msgs := h.LLM.WaitRequest().Messages
	if result := msgs[len(msgs)-1].Content; !strings.Contains(result, `"time":"`+slot+`"`) {
		t.Errorf("get_free_slots result = %s, want the slot at %s", result, slot)
	}

	h.LLM.Reply(testkit.ToolCall("book_trial", bookTrial))
	h.LLM.Reply(testkit.Text("Готово!"))
	h.TG.UserSays(user, "да, 8 999 123 45 67")
	if card := h.TG.WaitMessage(admin); !strings.Contains(card, "08.10 19:00") {
		t.Errorf("admin card = %q, want the slot at 08.10 19:00", card)
	}
}

// TestStopFinishesStartedBooking stops the bot while the model is thinking, so
// the rest of the turn runs after the stop.
func TestStopFinishesStartedBooking(t *testing.T) {
	h := start(t)

	h.TG.UserSays(user, "запишите на среду 19:00, 9 класс, ОГЭ, 8 999 123 45 67")
	h.LLM.WaitRequest()
	h.Stop()
	h.LLM.Reply(testkit.ToolCall("book_trial", bookTrial))
	h.LLM.Reply(testkit.Text("Готово!"))
	h.Wait()

	if got := h.TG.Sent(user); !slices.Equal(got, []string{"Готово!"}) {
		t.Errorf("sent to the user %q, want the model's answer", got)
	}
}
