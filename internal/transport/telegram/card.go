package telegram

import (
	"fmt"
	"time"

	"github.com/IvanTurko/physics-school-bot/internal/domain"
	tg "github.com/IvanTurko/physics-school-bot/internal/provider/telegram"
)

// list is one of the admin's booking lists: its button, and the title of a card in it.
type list struct {
	filter       domain.Filter
	label, title string
}

// lists are the admin's booking lists in the menu's order.
var lists = []list{
	{domain.FilterNew, "🆕 Новые", "🆕 Ждёт подтверждения"},
	{domain.FilterConfirmed, "✅ Подтверждённые", "✅ Подтверждена"},
	{domain.FilterAwaiting, "⏳ Ждут отметки", "⏳ Ждёт отметки"},
	{domain.FilterDone, "🎓 Проведённые", "🎓 Урок проведён"},
	{domain.FilterNoShow, "🚫 Не пришли", "🚫 Не пришёл"},
	{domain.FilterCancelled, "❌ Отменённые", "❌ Отменена"},
}

func listOf(f domain.Filter) (list, bool) {
	for _, l := range lists {
		if l.filter == f {
			return l, true
		}
	}
	return list{}, false
}

// card shows a booking to the admins under title, with a button for each decision
// allowed now and one to the student's dialog.
func card(title string, v domain.BookingView, now time.Time) (string, *tg.InlineKeyboardMarkup) {
	var row []tg.InlineKeyboardButton
	for _, d := range decisions {
		if domain.CheckMove(v, d.to, now) == nil {
			row = append(row, button(d.label, "bk:%s:%d", d.action, v.ID))
		}
	}
	row = append(row, button("💬 Диалог", "dg:%d", v.UserID))
	return title + "\n" + details(v), &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{row}}
}

func at(t time.Time, tz *time.Location) string { return t.In(tz).Format("02.01 15:04") }

func details(v domain.BookingView) string {
	return fmt.Sprintf("%d класс, %s\n%s\n%s", v.Grade, v.Goal, domain.FormatPhone(v.Phone), who(v.User))
}

// who is the user's name with their @username, if they have one.
func who(u domain.User) string {
	if u.Username == "" {
		return u.Name
	}
	return u.Name + " @" + u.Username
}
