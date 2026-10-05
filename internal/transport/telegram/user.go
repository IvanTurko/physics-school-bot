package telegram

import (
	"context"
	"log/slog"
	"time"

	"github.com/IvanTurko/physics-school-bot/internal/agent"
	"github.com/IvanTurko/physics-school-bot/internal/domain"
	tg "github.com/IvanTurko/physics-school-bot/internal/provider/telegram"
)

// Sender sends messages to Telegram.
type Sender interface {
	SendMessage(ctx context.Context, m tg.SendMessage) (tg.Message, error)
}

// Typist sends messages and shows the bot typing.
type Typist interface {
	Sender
	SendTyping(ctx context.Context, chatID int64) error
}

// Agent answers a user's message.
type Agent interface {
	Handle(ctx context.Context, userID int64, text string) (agent.Reply, error)
}

// Users finds or registers the user behind a Telegram account.
type Users interface {
	EnsureUser(ctx context.Context, tgID int64, username, name string) (domain.User, error)
}

// Deadline bounds the handling of one message, all model and tool calls included.
const Deadline = 60 * time.Second

// typingEvery is under the 5 seconds Telegram shows typing for.
const typingEvery = 4 * time.Second

// Canned answers that need no model.
const (
	textOnly    = "Пока понимаю только текст, напишите, пожалуйста 🙂"
	replyFailed = "Извините, не получилось ответить. Попробуйте, пожалуйста, ещё раз через минуту."
	greeting    = "Здравствуйте! Я ИИ-ассистент онлайн-школы физики. Расскажу о занятиях, отвечу на вопросы и запишу на бесплатный пробный урок.\n\n" +
		"Напишите, в каком классе ученик и к чему готовится, или просто задайте вопрос 🙂\n\n" +
		"Номер телефона, если вы его оставите, используется только для связи по записи на урок."
	demoGreeting = greeting + "\n\nЭто демо-версия: /demo → вы администратор → /admin — заявки и слоты."
)

// Bot handles the users' messages.
type Bot struct {
	send       Typist
	agent      Agent
	users      Users
	startReply string
	log        *slog.Logger
}

// NewBot creates the handler of user messages; in demo mode /start points to /demo.
func NewBot(send Typist, a Agent, users Users, demo bool, log *slog.Logger) *Bot {
	b := &Bot{send: send, agent: a, users: users, startReply: greeting, log: log}
	if demo {
		b.startReply = demoGreeting
	}
	return b
}

// Key returns the user an update belongs to, and false for updates the bot
// ignores: messages outside a private chat.
func Key(u tg.Update) (int64, bool) {
	if q := u.CallbackQuery; q != nil {
		return q.From.ID, true
	}
	m := u.Message
	if m == nil || m.From == nil || m.Chat.Type != tg.ChatPrivate {
		return 0, false
	}
	return m.From.ID, true
}

// Handle answers one message that Key accepted.
func (b *Bot) Handle(ctx context.Context, u tg.Update) {
	m := u.Message
	ctx, cancel := context.WithTimeout(ctx, Deadline)
	defer cancel()
	log := b.log.With("tg_id", m.From.ID)

	switch m.Text {
	case "":
		b.reply(ctx, log, m.Chat.ID, textOnly)
		return
	case "/start":
		b.reply(ctx, log, m.Chat.ID, b.startReply)
		return
	}

	user, err := b.users.EnsureUser(ctx, m.From.ID, m.From.Username, m.From.FullName())
	if err != nil {
		log.Error("cannot register user", "err", err)
		b.reply(ctx, log, m.Chat.ID, replyFailed)
		return
	}

	typing, stop := context.WithCancel(ctx)
	go b.keepTyping(typing, m.Chat.ID)
	r, err := b.agent.Handle(ctx, user.ID, m.Text)
	stop() // before the reply, so typing does not outlive it
	if err != nil {
		log.Error("cannot answer", "err", err)
		// Send the apology on a fresh deadline: the handling one may be spent.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tg.SendTimeout)
		defer cancel()
		b.reply(ctx, log, m.Chat.ID, replyFailed)
		return
	}
	b.reply(ctx, log, m.Chat.ID, r.Text)
}

// keepTyping shows the bot typing in the chat until ctx ends.
func (b *Bot) keepTyping(ctx context.Context, chatID int64) {
	t := time.NewTicker(typingEvery)
	defer t.Stop()
	for {
		_ = b.send.SendTyping(ctx, chatID) // no log: a lost typing costs nothing
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (b *Bot) reply(ctx context.Context, log *slog.Logger, chatID int64, text string) {
	if _, err := b.send.SendMessage(ctx, tg.SendMessage{ChatID: chatID, Text: text}); err != nil {
		log.Error("cannot send reply", "err", err)
	}
}
