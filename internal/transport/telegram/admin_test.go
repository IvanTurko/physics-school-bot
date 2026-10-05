package telegram

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IvanTurko/physics-school-bot/internal/clock"
	"github.com/IvanTurko/physics-school-bot/internal/domain"
	tg "github.com/IvanTurko/physics-school-bot/internal/provider/telegram"
)

// desk records the calls in order; Admins gives admin 1, ListBookings one booking, ListSlots
// a taken slot and a free one, and ListDialog two lines.
type desk struct {
	err       error // what a decision, an opening or a deletion returns
	adminsErr error // what Admins returns
	countErr  error
	listErr   error
	editErr   error
	sendErr   error
	gone      bool // the slot to delete is taken or deleted
	lines     int  // how many lines the dialog has
	count     map[domain.Filter]int
	calls     []string
}

func (d *desk) addf(format string, args ...any) {
	d.calls = append(d.calls, fmt.Sprintf(format, args...))
}

func (d *desk) Admins(context.Context) ([]int64, error) { return []int64{1}, d.adminsErr }

func (d *desk) decided(action string, id int64) error {
	d.addf("%s %d", action, id)
	return d.err
}

func (d *desk) Confirm(_ context.Context, id int64) error     { return d.decided("confirm", id) }
func (d *desk) AdminCancel(_ context.Context, id int64) error { return d.decided("cancel", id) }
func (d *desk) MarkDone(_ context.Context, id int64) error    { return d.decided("done", id) }
func (d *desk) MarkNoShow(_ context.Context, id int64) error  { return d.decided("noshow", id) }

func (d *desk) AnswerCallback(_ context.Context, _, text string) error {
	d.addf("answer %s", text)
	return nil
}

func (d *desk) SendMessage(_ context.Context, m tg.SendMessage) (tg.Message, error) {
	d.addf("send %d %s %s", m.ChatID, m.Text, describeButtons(m.ReplyMarkup))
	return tg.Message{}, d.sendErr
}

func (d *desk) EditMessageText(_ context.Context, m tg.EditMessageText) error {
	d.addf("edit %d/%d %s %s", m.ChatID, m.MessageID, m.Text, describeButtons(m.ReplyMarkup))
	return d.editErr
}

func (d *desk) CountBookings(context.Context) (map[domain.Filter]int, error) {
	return d.count, d.countErr
}

func (d *desk) ListBookings(_ context.Context, f domain.Filter, offset, limit int) ([]domain.BookingView, error) {
	d.addf("list %s %d %d", f, offset, limit)
	v := domain.BookingView{Booking: domain.Booking{ID: 7}, Start: time.Date(2026, 10, 7, 16, 0, 0, 0, time.UTC), User: domain.User{Name: "Иван"}}
	return []domain.BookingView{v}, d.listErr
}

func (d *desk) OpenSlots(_ context.Context, starts []time.Time) (int, error) {
	times := make([]string, len(starts))
	for i, t := range starts {
		times[i] = at(t, msk)
	}
	d.addf("open %s", strings.Join(times, " "))
	return 1, d.err
}

func (d *desk) CountSlots(context.Context) (int, error) { return 10, d.countErr }

func (d *desk) ListSlots(_ context.Context, offset, limit int) ([]domain.SlotView, error) {
	d.addf("slots %d %d", offset, limit)
	start := time.Date(2026, 10, 7, 16, 0, 0, 0, time.UTC)
	return []domain.SlotView{
		{Slot: domain.Slot{ID: 3, Start: start}, BookingID: 7, Name: "Иван"},
		{Slot: domain.Slot{ID: 4, Start: start.Add(time.Hour)}},
	}, d.listErr
}

func (d *desk) DeleteFreeSlot(_ context.Context, id int64) (bool, error) {
	d.addf("delete %d", id)
	return !d.gone, d.err
}

func (d *desk) CountDialog(context.Context, int64) (domain.User, int, error) {
	return domain.User{Name: "Иван", Username: "ivan"}, d.lines, d.countErr
}

func (d *desk) ListDialog(_ context.Context, user int64, offset, limit int) ([]domain.Line, error) {
	d.addf("dialog %d %d %d", user, offset, limit)
	at := time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
	return []domain.Line{
		{Role: domain.RoleUser, Text: strings.Repeat("я", lineSize), At: at},
		{Role: domain.RoleAssistant, Text: strings.Repeat("я", lineSize+1), At: at.Add(time.Minute)},
	}, d.listErr
}

func (d *desk) ShowCard(chatID, bookingID int64) { d.addf("show %d %d", chatID, bookingID) }

func (d *desk) Redraw(bookingID int64) { d.addf("redraw %d", bookingID) }

func (d *desk) admin() *Admin { return NewAdmin(d, d, d, d, d, d, d, clock.NewFake(today), msk, quiet) }

// press presses the button with data as the user from, on message 50 in their chat.
func (d *desk) press(from int64, data string) {
	q := &tg.CallbackQuery{ID: "cb", From: tg.User{ID: from}, Message: &tg.Message{MessageID: 50, Chat: tg.Chat{ID: from}}, Data: data}
	d.admin().Press(context.Background(), q)
}

func TestPressDecides(t *testing.T) {
	for _, tc := range []struct{ name, data, want string }{
		{"confirm", "bk:confirm:7", "confirm 7"},
		{"cancel", "bk:cancel:7", "cancel 7"},
		{"done", "bk:done:7", "done 7"},
		{"no show", "bk:noshow:7", "noshow 7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &desk{}
			d.press(1, tc.data)
			if want := []string{tc.want, "answer "}; !slices.Equal(d.calls, want) {
				t.Fatalf("calls = %q, want %q", d.calls, want)
			}
		})
	}
}

func TestPressRefusals(t *testing.T) {
	const decided = "confirm 7"
	for _, tc := range []struct {
		name           string
		from           int64
		data           string
		err, adminsErr error
		want           []string
	}{
		{"not an admin", 99, "bk:confirm:7", nil, nil, []string{"answer " + noRights}},
		{"admins not read", 1, "bk:confirm:7", nil, errors.New("disk is full"), []string{"answer " + pressFailed}},
		{"unknown action", 1, "bk:x:7", nil, nil, []string{"answer " + pressFailed}},
		{"invalid booking ID", 1, "bk:confirm:x", nil, nil, []string{"answer " + pressFailed}},
		{"invalid card ID", 1, "cd:x", nil, nil, []string{"answer " + pressFailed}},
		{"unknown list", 1, "ls:x:0", nil, nil, []string{"answer " + pressFailed}},
		{"invalid page", 1, "ls:new:x", nil, nil, []string{"answer " + pressFailed}},
		{"invalid slots page", 1, "sl:x", nil, nil, []string{"answer " + pressFailed}},
		{"invalid slot ID", 1, "sd:x:1", nil, nil, []string{"answer " + pressFailed}},
		{"invalid deletion page", 1, "sd:4:x", nil, nil, []string{"answer " + pressFailed}},
		{"invalid user ID", 1, "dg:x", nil, nil, []string{"answer " + pressFailed}},
		{"invalid dialog page", 1, "dp:3:x", nil, nil, []string{"answer " + pressFailed}},
		{"already handled", 1, "bk:confirm:7", domain.ErrAlreadyHandled, nil, []string{decided, "answer " + handled}},
		{"lesson started", 1, "bk:confirm:7", domain.ErrAlreadyStarted, nil, []string{decided, "redraw 7", "answer " + started}},
		{"failure", 1, "bk:confirm:7", errors.New("disk is full"), nil, []string{decided, "answer " + pressFailed}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &desk{err: tc.err, adminsErr: tc.adminsErr}
			d.press(tc.from, tc.data)
			if !slices.Equal(d.calls, tc.want) {
				t.Fatalf("calls = %q, want %q", d.calls, tc.want)
			}
		})
	}
}

const menuButtons = "[🆕 Новые · 10=ls:new:0 ✅ Подтверждённые · 0=ls:confirmed:0 ⏳ Ждут отметки · 0=ls:awaiting:0 " +
	"🎓 Проведённые · 0=ls:done:0 🚫 Не пришли · 0=ls:no_show:0 ❌ Отменённые · 0=ls:cancelled:0 🗓 Слоты=sl:0]"

func TestCommands(t *testing.T) {
	failed := errors.New("disk is full")
	for _, tc := range []struct {
		name                     string
		cmd                      func(*Admin, context.Context, *tg.Message)
		from                     int64
		text                     string
		err, adminsErr, countErr error
		want                     []string
	}{
		{"menu", (*Admin).Open, 1, "/admin", nil, nil, nil, []string{"send 1 🛠 Админка " + menuButtons}},
		{"not an admin", (*Admin).Open, 99, "/admin", nil, nil, nil, []string{"send 99 " + noRights + " none"}},
		{"admins not read", (*Admin).Open, 1, "/admin", nil, failed, nil, []string{"send 1 " + replyFailed + " none"}},
		{"counts not read", (*Admin).Open, 1, "/admin", nil, nil, failed, []string{"send 1 " + replyFailed + " none"}},
		{"slots added", (*Admin).AddSlots, 1, "/addslot 07.10 18:00 19:00 20:00", nil, nil, nil, []string{"open 07.10 18:00 07.10 19:00 07.10 20:00", "send 1 🗓 Добавлено слотов: 1, уже были: 2 none"}},
		{"past slot", (*Admin).AddSlots, 1, "/addslot 06.10 12:00", nil, nil, nil, []string{"send 1 " + slotsPast + " none"}},
		{"no times", (*Admin).AddSlots, 1, "/addslot 07.10", nil, nil, nil, []string{"send 1 " + slotsUsage + " none"}},
		{"slots not opened", (*Admin).AddSlots, 1, "/addslot 07.10 18:00", failed, nil, nil, []string{"open 07.10 18:00", "send 1 " + replyFailed + " none"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &desk{err: tc.err, adminsErr: tc.adminsErr, countErr: tc.countErr, count: map[domain.Filter]int{domain.FilterNew: 10}}
			tc.cmd(d.admin(), context.Background(), &tg.Message{From: &tg.User{ID: tc.from}, Chat: tg.Chat{ID: tc.from}, Text: tc.text})
			if !slices.Equal(d.calls, tc.want) {
				t.Fatalf("calls = %q, want %q", d.calls, tc.want)
			}
		})
	}
}

const slotsPage = "edit 1/50 🗓 Слоты: 10\nДобавить: /addslot 07.10 18:00 19:00 " +
	"[07.10 19:00 · Иван=cd:7 07.10 20:00 · 🗑 удалить=sd:4:1 ◀=sl:0 ↩ Меню=menu]"

func TestPressDrawsScreen(t *testing.T) {
	const firstPage = "edit 1/50 🆕 Новые: 10 [07.10 19:00 · Иван=cd:7 ▶=ls:new:1 ↩ Меню=menu]"
	failed := errors.New("disk is full")
	for _, tc := range []struct {
		name                       string
		data                       string
		countErr, listErr, editErr error
		want                       []string
	}{
		{"first page", "ls:new:0", nil, nil, nil, []string{"list new 0 5", firstPage, "answer "}},
		{"last page", "ls:new:1", nil, nil, nil, []string{"list new 5 5", "edit 1/50 🆕 Новые: 10 [07.10 19:00 · Иван=cd:7 ◀=ls:new:0 ↩ Меню=menu]", "answer "}},
		{"menu", "menu", nil, nil, nil, []string{"edit 1/50 🛠 Админка " + menuButtons, "answer "}},
		{"counts not read", "ls:new:0", failed, nil, nil, []string{"answer " + pressFailed}},
		{"page not read", "ls:new:0", nil, failed, nil, []string{"list new 0 5", "answer " + pressFailed}},
		{"screen not drawn", "ls:new:0", nil, nil, failed, []string{"list new 0 5", firstPage, "answer " + pressFailed}},
		{"slots", "sl:1", nil, nil, nil, []string{"slots 5 5", slotsPage, "answer "}},
		{"slot count not read", "sl:1", failed, nil, nil, []string{"answer " + pressFailed}},
		{"slot page not read", "sl:1", nil, failed, nil, []string{"slots 5 5", "answer " + pressFailed}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &desk{countErr: tc.countErr, listErr: tc.listErr, editErr: tc.editErr, count: map[domain.Filter]int{domain.FilterNew: 10}}
			d.press(1, tc.data)
			if !slices.Equal(d.calls, tc.want) {
				t.Fatalf("calls = %q, want %q", d.calls, tc.want)
			}
		})
	}
}

func TestPressDeletesSlot(t *testing.T) {
	failed := errors.New("disk is full")
	for _, tc := range []struct {
		name          string
		gone          bool
		err, countErr error
		want          []string
	}{
		{"deleted", false, nil, nil, []string{"delete 4", "slots 5 5", slotsPage, "answer "}},
		{"slot gone", true, nil, nil, []string{"delete 4", "slots 5 5", slotsPage, "answer " + slotGone}},
		{"deletion failed", false, failed, nil, []string{"delete 4", "answer " + pressFailed}},
		{"slot gone, count not read", true, nil, failed, []string{"delete 4", "answer " + pressFailed}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &desk{gone: tc.gone, err: tc.err, countErr: tc.countErr}
			d.press(1, "sd:4:1")
			if !slices.Equal(d.calls, tc.want) {
				t.Fatalf("calls = %q, want %q", d.calls, tc.want)
			}
		})
	}
}

// dialogText is the desk's dialog with its count left to fill in; a line of lineSize characters
// stays whole, a longer one is cut.
var dialogText = "💬 Иван @ivan · сообщений: %d\n\n👤 06.10 12:00\n" + strings.Repeat("я", lineSize) +
	"\n\n🤖 06.10 12:01\n" + strings.Repeat("я", lineSize) + "…"

func TestPressShowsDialog(t *testing.T) {
	page := fmt.Sprintf(dialogText, 20)
	failed := errors.New("disk is full")
	for _, tc := range []struct {
		name                       string
		data                       string
		lines                      int
		countErr, listErr, sendErr error
		want                       []string
	}{
		{"latest page", "dg:3", 20, nil, nil, nil, []string{"dialog 3 0 8", "send 1 " + page + " [◀=dp:3:1]", "answer "}},
		{"middle page", "dp:3:1", 20, nil, nil, nil, []string{"dialog 3 8 8", "edit 1/50 " + page + " [◀=dp:3:2 ▶=dp:3:0]", "answer "}},
		{"one page", "dg:3", 2, nil, nil, nil, []string{"dialog 3 0 8", "send 1 " + fmt.Sprintf(dialogText, 2) + " none", "answer "}},
		{"dialog count not read", "dg:3", 20, failed, nil, nil, []string{"answer " + pressFailed}},
		{"dialog page not read", "dg:3", 20, nil, failed, nil, []string{"dialog 3 0 8", "answer " + pressFailed}},
		{"dialog not sent", "dg:3", 20, nil, nil, failed, []string{"dialog 3 0 8", "send 1 " + page + " [◀=dp:3:1]", "answer " + pressFailed}},
		{"arrow, count not read", "dp:3:1", 20, failed, nil, nil, []string{"answer " + pressFailed}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &desk{lines: tc.lines, countErr: tc.countErr, listErr: tc.listErr, sendErr: tc.sendErr}
			d.press(1, tc.data)
			if !slices.Equal(d.calls, tc.want) {
				t.Fatalf("calls = %q, want %q", d.calls, tc.want)
			}
		})
	}
}

func TestPressOpensCard(t *testing.T) {
	d := &desk{}
	d.press(1, "cd:7")
	if want := []string{"show 1 7", "answer "}; !slices.Equal(d.calls, want) {
		t.Fatalf("calls = %q, want %q", d.calls, want)
	}
}

func describeButtons(m *tg.InlineKeyboardMarkup) string {
	if m == nil {
		return "none"
	}
	var got []string
	for _, row := range m.InlineKeyboard {
		for _, b := range row {
			got = append(got, b.Text+"="+b.CallbackData)
		}
	}
	return fmt.Sprint(got)
}
