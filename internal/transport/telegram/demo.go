package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	tg "github.com/IvanTurko/physics-school-bot/internal/provider/telegram"
)

// Grants makes demo admins.
type Grants interface {
	GrantDemo(ctx context.Context, userID int64) (time.Time, error)
}

// demoGranted answers /demo; it takes the time the rights end.
const demoGranted = "🔑 Вы администратор до %s: сюда будут приходить карточки новых записей, и на них запись можно подтвердить или отменить. Заявки и слоты — /admin."

// Demo handles /demo, which makes the sender an admin for a while.
type Demo struct {
	send   Sender
	users  Users
	grants Grants
	tz     *time.Location
	log    *slog.Logger
}

// NewDemo creates the /demo handler; it shows times in tz.
func NewDemo(send Sender, users Users, grants Grants, tz *time.Location, log *slog.Logger) *Demo {
	return &Demo{send: send, users: users, grants: grants, tz: tz, log: log}
}

// Handle makes the sender of m an admin and tells them until when.
func (d *Demo) Handle(ctx context.Context, m *tg.Message) {
	log := d.log.With("tg_id", m.From.ID)
	until, err := d.grant(ctx, m.From)
	text := fmt.Sprintf(demoGranted, at(until, d.tz))
	if err != nil {
		log.Error("cannot grant demo rights", "err", err)
		text = replyFailed
	}
	if _, err := d.send.SendMessage(ctx, tg.SendMessage{ChatID: m.Chat.ID, Text: text}); err != nil {
		log.Error("cannot send reply", "err", err)
	}
}

func (d *Demo) grant(ctx context.Context, from *tg.User) (time.Time, error) {
	u, err := d.users.EnsureUser(ctx, from.ID, from.Username, from.FullName())
	if err != nil {
		return time.Time{}, err
	}
	return d.grants.GrantDemo(ctx, u.ID)
}
