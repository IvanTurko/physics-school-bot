package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/IvanTurko/physics-school-bot/internal/domain"
	tg "github.com/IvanTurko/physics-school-bot/internal/provider/telegram"
)

// reminderText is the reminder; it takes the lesson's time.
const reminderText = "⏰ Напоминаем о пробном уроке: %s. Если планы изменились, напишите — перенесём или отменим."

// Reminder sends the reminders and keeps each one in the student's history.
type Reminder struct {
	send    Sender
	history History
	tz      *time.Location
	log     *slog.Logger
}

// NewReminder creates a Reminder that shows times in tz.
func NewReminder(send Sender, history History, tz *time.Location, log *slog.Logger) *Reminder {
	return &Reminder{send: send, history: history, tz: tz, log: log}
}

// Remind sends the student the reminder and keeps it in their history; a student
// the bot cannot reach counts as reminded.
func (r *Reminder) Remind(ctx context.Context, v domain.BookingView) error {
	text := fmt.Sprintf(reminderText, at(v.Start, r.tz))
	_, err := r.send.SendMessage(ctx, tg.SendMessage{ChatID: v.User.TelegramID, Text: text})
	if unreachable(err) {
		r.log.Info("student unreachable, reminder skipped", "booking", v.ID)
		return nil
	}
	if err != nil {
		return err
	}
	if err := r.history.Append(ctx, v.UserID, domain.Message{Role: domain.RoleAssistant, Content: text}); err != nil {
		r.log.Error("cannot save reminder to history", "user", v.UserID, "err", err)
	}
	return nil
}

// unreachable reports whether Telegram refuses the chat for good: the user blocked
// the bot or deleted the account.
func unreachable(err error) bool {
	var api *tg.APIError
	return errors.As(err, &api) && api.Code == http.StatusForbidden
}
