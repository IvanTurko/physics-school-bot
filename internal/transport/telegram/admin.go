package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/IvanTurko/physics-school-bot/internal/clock"
	"github.com/IvanTurko/physics-school-bot/internal/domain"
	tg "github.com/IvanTurko/physics-school-bot/internal/provider/telegram"
)

// Console draws the admin's screens and answers button presses.
type Console interface {
	Outbox
	AnswerCallback(ctx context.Context, id, text string) error
}

// Admins tells who is an admin now.
type Admins interface {
	Admins(ctx context.Context) ([]int64, error)
}

// Decisions are what the admin decides about bookings.
type Decisions interface {
	Confirm(ctx context.Context, id int64) error
	AdminCancel(ctx context.Context, id int64) error
	MarkDone(ctx context.Context, id int64) error
	MarkNoShow(ctx context.Context, id int64) error
}

// Catalog counts and pages the bookings of the admin's lists.
type Catalog interface {
	CountBookings(ctx context.Context) (map[domain.Filter]int, error)
	ListBookings(ctx context.Context, f domain.Filter, offset, limit int) ([]domain.BookingView, error)
}

// Schedule keeps the slots for the admin.
type Schedule interface {
	OpenSlots(ctx context.Context, starts []time.Time) (int, error)
	CountSlots(ctx context.Context) (int, error)
	ListSlots(ctx context.Context, offset, limit int) ([]domain.SlotView, error)
	DeleteFreeSlot(ctx context.Context, id int64) (bool, error)
}

// Dialogs counts and pages a student's dialog with the bot.
type Dialogs interface {
	CountDialog(ctx context.Context, userID int64) (domain.User, int, error)
	ListDialog(ctx context.Context, userID int64, offset, limit int) ([]domain.Line, error)
}

// Cards shows and redraws booking cards in the background.
type Cards interface {
	ShowCard(chatID, bookingID int64)
	Redraw(bookingID int64)
}

// Answers to the admin; a press that worked needs none: the card or the screen shows it.
const (
	noRights    = "Нет доступа"
	handled     = "Уже обработано"
	started     = "Урок уже начался"
	pressFailed = "Не получилось, попробуйте ещё раз"
	slotGone    = "Слот уже занят или удалён"
)

// Texts of /addslot and the slots screen.
const (
	slotsAdded = "🗓 Добавлено слотов: %d, уже были: %d"
	slotsPast  = "Это время уже прошло"
	slotsUsage = "Формат: /addslot 07.10 18:00 19:00"
	slotsTitle = "🗓 Слоты: %d\nДобавить: /addslot 07.10 18:00 19:00"
)

// pageSize is how many bookings or slots a page shows.
const pageSize = 5

// A dialog page: dialogSize lines of at most lineSize characters fit one message even if all are emoji.
const (
	dialogSize = 8
	lineSize   = 200
)

// speakers mark who said a line of a dialog.
var speakers = map[domain.Role]string{domain.RoleUser: "👤", domain.RoleAssistant: "🤖"}

// decision is a card button: the move it asks for and the call that makes it.
type decision struct {
	label, action string
	to            domain.Status
	run           func(d Decisions, ctx context.Context, id int64) error
}

// decisions are the buttons of a card, each shown while its move is allowed.
var decisions = []decision{
	{"Подтвердить", "confirm", domain.StatusConfirmed, Decisions.Confirm},
	{"Отменить", "cancel", domain.StatusCancelled, Decisions.AdminCancel},
	{"Провели", "done", domain.StatusDone, Decisions.MarkDone},
	{"Не пришёл", "noshow", domain.StatusNoShow, Decisions.MarkNoShow},
}

// Admin handles the admin's commands and buttons.
type Admin struct {
	console  Console
	bookings Decisions
	catalog  Catalog
	schedule Schedule
	dialogs  Dialogs
	cards    Cards
	admins   Admins
	clock    clock.Clock
	tz       *time.Location
	log      *slog.Logger
}

// NewAdmin creates the handler of the admin's commands and buttons; it reads and shows times in tz.
func NewAdmin(console Console, d Decisions, catalog Catalog, schedule Schedule, dialogs Dialogs, cards Cards, admins Admins, c clock.Clock, tz *time.Location, log *slog.Logger) *Admin {
	return &Admin{console: console, bookings: d, catalog: catalog, schedule: schedule, dialogs: dialogs, cards: cards, admins: admins, clock: c, tz: tz, log: log}
}

// Open answers /admin with the menu.
func (a *Admin) Open(ctx context.Context, m *tg.Message) { a.command(ctx, m, a.menu) }

// AddSlots answers /addslot 07.10 18:00 19:00 by opening the day's slots.
func (a *Admin) AddSlots(ctx context.Context, m *tg.Message) {
	day := strings.TrimPrefix(m.Text, "/addslot")
	a.command(ctx, m, func(ctx context.Context) (string, *tg.InlineKeyboardMarkup, error) { return a.addSlots(ctx, day) })
}

// command answers the admin's command m with what do makes.
func (a *Admin) command(ctx context.Context, m *tg.Message, do func(ctx context.Context) (string, *tg.InlineKeyboardMarkup, error)) {
	log := a.log.With("tg_id", m.From.ID)
	text, buttons, err := a.allowed(ctx, m.From.ID, do)
	if err != nil {
		log.Error("cannot answer command", "text", m.Text, "err", err)
		text, buttons = replyFailed, nil
	}
	if _, err := a.console.SendMessage(ctx, tg.SendMessage{ChatID: m.Chat.ID, Text: text, ReplyMarkup: buttons}); err != nil {
		log.Error("cannot send reply", "err", err)
	}
}

// allowed runs do for an admin and answers anyone else noRights.
func (a *Admin) allowed(ctx context.Context, user int64, do func(ctx context.Context) (string, *tg.InlineKeyboardMarkup, error)) (string, *tg.InlineKeyboardMarkup, error) {
	ok, err := a.isAdmin(ctx, user)
	if err != nil {
		return "", nil, err
	}
	if !ok {
		return noRights, nil, nil
	}
	return do(ctx)
}

func (a *Admin) addSlots(ctx context.Context, day string) (string, *tg.InlineKeyboardMarkup, error) {
	starts, err := domain.ParseSlotDay(day, a.clock.Now(), a.tz)
	switch {
	case errors.Is(err, domain.ErrPastSlot):
		return slotsPast, nil, nil
	case errors.Is(err, domain.ErrInvalidSlotTime):
		return slotsUsage, nil, nil
	}
	opened, err := a.schedule.OpenSlots(ctx, starts)
	if err != nil {
		return "", nil, err
	}
	return fmt.Sprintf(slotsAdded, opened, len(starts)-opened), nil, nil
}

// Press handles a pressed button and answers the press; the cards are redrawn
// when the booking changes.
func (a *Admin) Press(ctx context.Context, q *tg.CallbackQuery) {
	log := a.log.With("tg_id", q.From.ID)
	if err := a.console.AnswerCallback(ctx, q.ID, a.press(ctx, log, q)); err != nil {
		log.Error("cannot answer button", "err", err)
	}
}

// press returns what to answer the admin.
func (a *Admin) press(ctx context.Context, log *slog.Logger, q *tg.CallbackQuery) string {
	ok, err := a.isAdmin(ctx, q.From.ID)
	if err != nil {
		log.Error("cannot read admins", "err", err)
		return pressFailed
	}
	if !ok {
		return noRights
	}
	kind, rest, _ := strings.Cut(q.Data, ":")
	switch kind {
	case "bk": // bk:<action>:<booking ID>
		if d, id, ok := parseDecision(rest); ok {
			return a.decide(ctx, log, d, id)
		}
	case "cd": // cd:<booking ID>
		if id, err := strconv.ParseInt(rest, 10, 64); err == nil {
			a.cards.ShowCard(q.Message.Chat.ID, id)
			return ""
		}
	case "ls": // ls:<filter>:<page>
		if l, p, ok := parsePage(rest); ok {
			text, buttons, err := a.page(ctx, l, p)
			return a.draw(ctx, log, q.Message, text, buttons, err)
		}
	case "sl": // sl:<page>
		if p, err := strconv.Atoi(rest); err == nil {
			text, buttons, err := a.slots(ctx, p)
			return a.draw(ctx, log, q.Message, text, buttons, err)
		}
	case "sd": // sd:<slot ID>:<page>
		if id, p, ok := parseIDAndPage(rest); ok {
			return a.deleteSlot(ctx, log, q.Message, id, p)
		}
	case "dg": // dg:<user ID>
		if user, err := strconv.ParseInt(rest, 10, 64); err == nil {
			return a.showDialog(ctx, log, q.Message.Chat.ID, user)
		}
	case "dp": // dp:<user ID>:<page>
		if user, p, ok := parseIDAndPage(rest); ok {
			text, buttons, err := a.dialog(ctx, user, p)
			return a.draw(ctx, log, q.Message, text, buttons, err)
		}
	case "menu":
		text, buttons, err := a.menu(ctx)
		return a.draw(ctx, log, q.Message, text, buttons, err)
	}
	log.Error("unknown button", "data", q.Data)
	return pressFailed
}

func (a *Admin) isAdmin(ctx context.Context, user int64) (bool, error) {
	admins, err := a.admins.Admins(ctx)
	return slices.Contains(admins, user), err
}

func parseDecision(s string) (decision, int64, bool) {
	action, rawID, _ := strings.Cut(s, ":")
	i := slices.IndexFunc(decisions, func(d decision) bool { return d.action == action })
	id, err := strconv.ParseInt(rawID, 10, 64)
	if i < 0 || err != nil {
		return decision{}, 0, false
	}
	return decisions[i], id, true
}

func parsePage(s string) (list, int, bool) {
	f, rawPage, _ := strings.Cut(s, ":")
	l, ok := listOf(domain.Filter(f))
	p, err := strconv.Atoi(rawPage)
	return l, p, ok && err == nil
}

func parseIDAndPage(s string) (int64, int, bool) {
	rawID, rawPage, _ := strings.Cut(s, ":")
	id, idErr := strconv.ParseInt(rawID, 10, 64)
	p, pageErr := strconv.Atoi(rawPage)
	return id, p, idErr == nil && pageErr == nil
}

// decide runs the decision on the booking and returns what to answer.
func (a *Admin) decide(ctx context.Context, log *slog.Logger, d decision, id int64) string {
	err := d.run(a.bookings, ctx, id)
	switch {
	case err == nil:
		return ""
	case errors.Is(err, domain.ErrAlreadyHandled):
		return handled
	case errors.Is(err, domain.ErrAlreadyStarted):
		a.cards.Redraw(id)
		return started
	}
	log.Error("cannot decide", "booking", id, "err", err)
	return pressFailed
}

// deleteSlot deletes the slot if it is free, redraws page p of the slots and returns what to answer.
func (a *Admin) deleteSlot(ctx context.Context, log *slog.Logger, m *tg.Message, id int64, p int) string {
	deleted, err := a.schedule.DeleteFreeSlot(ctx, id)
	if err != nil {
		log.Error("cannot delete slot", "slot", id, "err", err)
		return pressFailed
	}
	text, buttons, err := a.slots(ctx, p)
	answer := a.draw(ctx, log, m, text, buttons, err)
	if answer == "" && !deleted {
		return slotGone
	}
	return answer
}

// showDialog sends the chat the latest page of the user's dialog and returns what to answer.
func (a *Admin) showDialog(ctx context.Context, log *slog.Logger, chatID, user int64) string {
	text, buttons, err := a.dialog(ctx, user, 0)
	if err != nil {
		log.Error("cannot read screen", "err", err)
		return pressFailed
	}
	if _, err := a.console.SendMessage(ctx, tg.SendMessage{ChatID: chatID, Text: text, ReplyMarkup: buttons}); err != nil {
		log.Error("cannot send screen", "err", err)
		return pressFailed
	}
	return ""
}

// draw shows the screen on the message with the pressed button; err is the failure
// to read the screen, and any failure answers pressFailed.
func (a *Admin) draw(ctx context.Context, log *slog.Logger, m *tg.Message, text string, buttons *tg.InlineKeyboardMarkup, err error) string {
	if err != nil {
		log.Error("cannot read screen", "err", err)
		return pressFailed
	}
	err = a.console.EditMessageText(ctx, tg.EditMessageText{ChatID: m.Chat.ID, MessageID: m.MessageID, Text: text, ReplyMarkup: buttons})
	if err != nil {
		log.Error("cannot draw screen", "err", err)
		return pressFailed
	}
	return ""
}

// menu is the screen of the booking lists, each with how many bookings it holds, and a button to the slots.
func (a *Admin) menu(ctx context.Context) (string, *tg.InlineKeyboardMarkup, error) {
	counts, err := a.catalog.CountBookings(ctx)
	if err != nil {
		return "", nil, err
	}
	rows := make([][]tg.InlineKeyboardButton, 0, len(lists)+1)
	for _, l := range lists {
		rows = append(rows, []tg.InlineKeyboardButton{button(fmt.Sprintf("%s · %d", l.label, counts[l.filter]), "ls:%s:0", l.filter)})
	}
	rows = append(rows, []tg.InlineKeyboardButton{button("🗓 Слоты", "sl:0")})
	return "🛠 Админка", &tg.InlineKeyboardMarkup{InlineKeyboard: rows}, nil
}

// page is the screen of page p of list l, a button for each booking.
func (a *Admin) page(ctx context.Context, l list, p int) (string, *tg.InlineKeyboardMarkup, error) {
	counts, err := a.catalog.CountBookings(ctx)
	if err != nil {
		return "", nil, err
	}
	views, err := a.catalog.ListBookings(ctx, l.filter, p*pageSize, pageSize)
	if err != nil {
		return "", nil, err
	}
	rows := make([][]tg.InlineKeyboardButton, len(views))
	for i, v := range views {
		rows[i] = []tg.InlineKeyboardButton{button(at(v.Start, a.tz)+" · "+v.User.Name, "cd:%d", v.ID)}
	}
	return fmt.Sprintf("%s: %d", l.label, counts[l.filter]), paged(rows, p, counts[l.filter], "ls:%s:%d", l.filter), nil
}

// slots is the screen of page p of the slots ahead: a taken slot's button opens its booking,
// a free slot's deletes it.
func (a *Admin) slots(ctx context.Context, p int) (string, *tg.InlineKeyboardMarkup, error) {
	count, err := a.schedule.CountSlots(ctx)
	if err != nil {
		return "", nil, err
	}
	views, err := a.schedule.ListSlots(ctx, p*pageSize, pageSize)
	if err != nil {
		return "", nil, err
	}
	rows := make([][]tg.InlineKeyboardButton, len(views))
	for i, v := range views {
		rows[i] = []tg.InlineKeyboardButton{a.slotButton(v, p)}
	}
	return fmt.Sprintf(slotsTitle, count), paged(rows, p, count, "sl:%d"), nil
}

func (a *Admin) slotButton(v domain.SlotView, p int) tg.InlineKeyboardButton {
	if v.BookingID == 0 {
		return button(at(v.Start, a.tz)+" · 🗑 удалить", "sd:%d:%d", v.ID, p)
	}
	return button(at(v.Start, a.tz)+" · "+v.Name, "cd:%d", v.BookingID)
}

// dialog is page p of the user's dialog, counted back from the latest.
func (a *Admin) dialog(ctx context.Context, user int64, p int) (string, *tg.InlineKeyboardMarkup, error) {
	u, count, err := a.dialogs.CountDialog(ctx, user)
	if err != nil {
		return "", nil, err
	}
	lines, err := a.dialogs.ListDialog(ctx, user, p*dialogSize, dialogSize)
	if err != nil {
		return "", nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "💬 %s · сообщений: %d", who(u), count)
	for _, l := range lines {
		fmt.Fprintf(&b, "\n\n%s %s\n%s", speakers[l.Role], at(l.At, a.tz), clip(l.Text, lineSize))
	}
	var nav []tg.InlineKeyboardButton
	if (p+1)*dialogSize < count {
		nav = append(nav, button("◀", "dp:%d:%d", user, p+1))
	}
	if p > 0 {
		nav = append(nav, button("▶", "dp:%d:%d", user, p-1))
	}
	if nav == nil {
		return b.String(), nil, nil
	}
	return b.String(), &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{nav}}, nil
}

// clip keeps the first n characters of s and marks a cut with an ellipsis.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// paged ends a page's rows with the arrows to the pages around p and the way back to the menu;
// an arrow's data is format filled with args and its page.
func paged(rows [][]tg.InlineKeyboardButton, p, count int, format string, args ...any) *tg.InlineKeyboardMarkup {
	var nav []tg.InlineKeyboardButton
	if p > 0 {
		nav = append(nav, button("◀", format, append(args, p-1)...))
	}
	if (p+1)*pageSize < count {
		nav = append(nav, button("▶", format, append(args, p+1)...))
	}
	if nav != nil {
		rows = append(rows, nav)
	}
	return &tg.InlineKeyboardMarkup{InlineKeyboard: append(rows, []tg.InlineKeyboardButton{button("↩ Меню", "menu")})}
}

// button is an inline button whose data is format filled with args.
func button(text, format string, args ...any) tg.InlineKeyboardButton {
	return tg.InlineKeyboardButton{Text: text, CallbackData: fmt.Sprintf(format, args...)}
}
