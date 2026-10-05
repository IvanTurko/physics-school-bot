package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/IvanTurko/physics-school-bot/internal/clock"
	"github.com/IvanTurko/physics-school-bot/internal/domain"
	tg "github.com/IvanTurko/physics-school-bot/internal/provider/telegram"
)

// notifyQueue is how much work waits in the queue before new work is dropped.
const notifyQueue = 100

// History keeps the dialog the model reads.
type History interface {
	Append(ctx context.Context, userID int64, msgs ...domain.Message) error
}

// Outbox sends messages and edits the ones already sent.
type Outbox interface {
	Sender
	EditMessageText(ctx context.Context, m tg.EditMessageText) error
}

// CardStore keeps where the admins' cards went, and the bookings they show.
type CardStore interface {
	AddCard(ctx context.Context, c domain.Card) error
	Cards(ctx context.Context, bookingID int64) ([]domain.Card, error)
	Booking(ctx context.Context, id int64) (domain.BookingView, error)
}

// Notifier sends the admins' booking cards and redraws them, and sends the student's news, in the
// background; the student's news also goes to their dialog history.
type Notifier struct {
	send    Outbox
	history History
	cards   CardStore
	admins  Admins
	clock   clock.Clock
	tz      *time.Location
	log     *slog.Logger

	queue  chan func(ctx context.Context)
	done   chan struct{}
	cancel context.CancelFunc
}

// NewNotifier starts a Notifier that shows times in tz; ctx bounds the sending,
// and Shutdown cancels it at its deadline.
func NewNotifier(ctx context.Context, send Outbox, history History, cards CardStore, admins Admins, c clock.Clock, tz *time.Location, log *slog.Logger) *Notifier {
	ctx, cancel := context.WithCancel(ctx)
	n := &Notifier{
		send: send, history: history, cards: cards, admins: admins, clock: c, tz: tz, log: log,
		queue: make(chan func(ctx context.Context), notifyQueue), done: make(chan struct{}), cancel: cancel,
	}
	go n.run(ctx)
	return n
}

// BookingCreated tells the admins about a new booking.
func (n *Notifier) BookingCreated(v domain.BookingView) {
	n.toAdmins(v, "🆕 Новая запись на пробный урок\n"+at(v.Start, n.tz))
}

// BookingRescheduled tells the admins the student moved the booking.
func (n *Notifier) BookingRescheduled(v domain.BookingView, oldStart time.Time) {
	n.toAdmins(v, fmt.Sprintf("🔁 Перенос: %s → %s", at(oldStart, n.tz), at(v.Start, n.tz)))
}

// BookingCancelled tells the admins the student cancelled the booking.
func (n *Notifier) BookingCancelled(v domain.BookingView) {
	n.toAdmins(v, "❌ Отмена учеником: "+at(v.Start, n.tz))
}

// BookingConfirmed tells the student the admin confirmed the booking.
func (n *Notifier) BookingConfirmed(v domain.BookingView) {
	n.toStudent(v, "✅ Администратор подтвердил вашу запись на пробный урок: %s.")
}

// BookingCancelledByAdmin tells the student the admin cancelled the booking.
func (n *Notifier) BookingCancelledByAdmin(v domain.BookingView) {
	n.toStudent(v, "❌ Администратор отменил вашу запись на пробный урок %s. Чтобы выбрать другое время, просто напишите.")
}

// BookingMarked redraws the booking's cards; nobody is told how the lesson went.
func (n *Notifier) BookingMarked(v domain.BookingView) { n.Redraw(v.ID) }

// Redraw redraws every card of the booking as the booking is now.
func (n *Notifier) Redraw(id int64) { n.push(id, func(ctx context.Context) { n.sync(ctx, id) }) }

// ShowCard sends the chat the booking's card as it is now, and keeps it redrawn.
func (n *Notifier) ShowCard(chatID, id int64) {
	n.push(id, func(ctx context.Context) { n.show(ctx, chatID, id) })
}

// Shutdown does the queued work until deadline, then drops the rest; nothing may
// be pushed after it.
func (n *Notifier) Shutdown(deadline time.Time) {
	close(n.queue)
	if !drain(n.done, deadline, n.cancel) {
		n.log.Warn("notifications not done by the deadline, dropped")
	}
}

// toStudent writes to the booking's student; format takes the lesson's time.
func (n *Notifier) toStudent(v domain.BookingView, format string) {
	m := tg.SendMessage{ChatID: v.User.TelegramID, Text: fmt.Sprintf(format, at(v.Start, n.tz))}
	n.push(v.ID, func(ctx context.Context) {
		n.sync(ctx, v.ID)
		n.sendStudent(ctx, v.UserID, m)
	})
}

// toAdmins sends the booking's card under title to every admin.
func (n *Notifier) toAdmins(v domain.BookingView, title string) {
	text, buttons := card(title, v, n.clock.Now())
	m := tg.SendMessage{Text: text, ReplyMarkup: buttons}
	n.push(v.ID, func(ctx context.Context) {
		n.sync(ctx, v.ID)
		n.sendAdmins(ctx, v.ID, m)
	})
}

// push queues do without waiting; id names the booking in the log when the queue is full.
func (n *Notifier) push(id int64, do func(ctx context.Context)) {
	select {
	case n.queue <- do:
	default:
		n.log.Error("notification queue full, work dropped", "booking", id)
	}
}

func (n *Notifier) run(ctx context.Context) {
	defer close(n.done)
	for do := range n.queue {
		do(ctx)
	}
}

// sync redraws every card of the booking as the booking is now.
func (n *Notifier) sync(ctx context.Context, id int64) {
	cards, err := n.cards.Cards(ctx, id)
	if err != nil {
		n.log.Error("cannot read cards", "booking", id, "err", err)
		return
	}
	v, err := n.cards.Booking(ctx, id)
	if err != nil {
		n.log.Error("cannot read booking", "booking", id, "err", err)
		return
	}
	text, buttons := n.current(v)
	for _, c := range cards {
		err := n.send.EditMessageText(ctx, tg.EditMessageText{ChatID: c.ChatID, MessageID: c.MessageID, Text: text, ReplyMarkup: buttons})
		if err != nil {
			n.log.Error("cannot update card", "booking", id, "chat", c.ChatID, "err", err)
		}
	}
}

func (n *Notifier) show(ctx context.Context, chatID, id int64) {
	v, err := n.cards.Booking(ctx, id)
	if err != nil {
		n.log.Error("cannot read booking", "booking", id, "err", err)
		return
	}
	text, buttons := n.current(v)
	n.sendCard(ctx, id, tg.SendMessage{ChatID: chatID, Text: text, ReplyMarkup: buttons})
}

// current is the booking's card as it is now, titled by the list it is in.
func (n *Notifier) current(v domain.BookingView) (string, *tg.InlineKeyboardMarkup) {
	now := n.clock.Now()
	l, _ := listOf(domain.FilterOf(v, now))
	return card(l.title+"\n"+at(v.Start, n.tz), v, now)
}

// sendAdmins sends the card m to every current admin.
func (n *Notifier) sendAdmins(ctx context.Context, id int64, m tg.SendMessage) {
	admins, err := n.admins.Admins(ctx)
	if err != nil {
		n.log.Error("cannot read admins", "err", err)
		return
	}
	for _, chat := range admins {
		m.ChatID = chat
		n.sendCard(ctx, id, m)
	}
}

// sendCard sends a card of the booking and remembers where it went.
func (n *Notifier) sendCard(ctx context.Context, id int64, m tg.SendMessage) {
	sent, ok := n.deliver(ctx, m)
	if !ok {
		return
	}
	if err := n.cards.AddCard(ctx, domain.Card{BookingID: id, ChatID: m.ChatID, MessageID: sent.MessageID}); err != nil {
		n.log.Error("cannot save card", "booking", id, "chat", m.ChatID, "err", err)
	}
}

// sendStudent sends m to the student and keeps its text in their history.
func (n *Notifier) sendStudent(ctx context.Context, user int64, m tg.SendMessage) {
	if _, ok := n.deliver(ctx, m); !ok {
		return
	}
	if err := n.history.Append(ctx, user, domain.Message{Role: domain.RoleAssistant, Content: m.Text}); err != nil {
		n.log.Error("cannot save notification to history", "user", user, "err", err)
	}
}

// deliver sends m and returns the sent message and whether sending worked; a failure is logged.
func (n *Notifier) deliver(ctx context.Context, m tg.SendMessage) (tg.Message, bool) {
	sent, err := n.send.SendMessage(ctx, m)
	if err != nil {
		n.log.Error("cannot send notification", "chat", m.ChatID, "err", err)
	}
	return sent, err == nil
}
