package e2e

import (
	"strings"
	"testing"

	"github.com/IvanTurko/physics-school-bot/internal/testkit"
)

// bookSlot has the user book the slot through the model.
func bookSlot(h *testkit.Harness) {
	h.LLM.Reply(testkit.ToolCall("book_trial", bookTrial))
	h.LLM.Reply(testkit.Text("Готово!"))
	h.TG.UserSays(user, "запишите на среду 19:00, 9 класс, ОГЭ, 8 999 123 45 67")
}

func TestAdminConfirmsBooking(t *testing.T) {
	const admin2 = 2
	h := testkit.Start(t, testkit.Options{Now: now, AdminIDs: []int64{admin, admin2}})
	h.AddSlot(slot)
	bookSlot(h)
	h.TG.WaitMessage(admin2) // admin 1 gets the card first

	h.TG.UserPresses(admin, "Подтвердить")
	// «Готово!» and the confirmation, sent once the cards are redrawn
	h.TG.WaitMessage(user)
	h.TG.WaitMessage(user)
	if card := h.TG.Sent(admin2)[0]; !strings.Contains(card, "Подтверждена") || !strings.Contains(card, "08.10 19:00") {
		t.Errorf("second admin's card = %q, want it confirmed, at 08.10 19:00", card)
	}
}

func TestAdminOpensBookingFromList(t *testing.T) {
	h := start(t)
	bookSlot(h)
	h.TG.WaitMessage(admin) // the new booking's card

	h.TG.UserSays(admin, "/admin")
	h.TG.WaitMessage(admin) // the menu
	h.TG.UserPresses(admin, "🆕 Новые · 1")
	h.TG.UserPresses(admin, "08.10 19:00 · Ученик")
	if card := h.TG.WaitMessage(admin); !strings.Contains(card, "Ждёт подтверждения") {
		t.Errorf("opened card = %q, want the booking as it is now", card)
	}
}

func TestAdminReadsDialog(t *testing.T) {
	h := start(t)
	bookSlot(h)
	h.TG.WaitMessage(admin) // the new booking's card
	h.TG.WaitMessage(user)  // «Готово!», saved to the history before it is sent
	h.TG.UserPresses(admin, "💬 Диалог")
	if dialog := h.TG.WaitMessage(admin); !strings.Contains(dialog, "запишите на среду") || !strings.Contains(dialog, "Готово!") {
		t.Errorf("dialog = %q, want the student's request and the bot's reply", dialog)
	}
}

func TestAdminAddsSlots(t *testing.T) {
	h := start(t)
	h.TG.UserSays(admin, "/addslot 08.10 20:00")
	h.TG.WaitMessage(admin) // the reply: the slot is open before the student asks

	h.LLM.Reply(testkit.ToolCall("get_free_slots", "{}"))
	h.LLM.Reply(testkit.Text("Есть среда 20:00."))
	h.TG.UserSays(user, "когда можно?")
	h.LLM.WaitRequest() // the request answered with the tool call
	msgs := h.LLM.WaitRequest().Messages
	if result := msgs[len(msgs)-1].Content; !strings.Contains(result, `"time":"2025-10-08 20:00"`) {
		t.Errorf("get_free_slots result = %s, want the slot added at 2025-10-08 20:00", result)
	}
}
