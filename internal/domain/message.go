// Package domain holds the bot's entities and rules, free of any infrastructure.
package domain

import "time"

// Role says who wrote a message in the dialog.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool" // the result of a tool call
)

// ToolCall is the model's request to run a tool.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // JSON, as the model wrote it
}

// Message is one entry of a dialog's history.
type Message struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall // set on an assistant message that asks for tools
	ToolCallID string     // set on a tool message: which call it answers
}

// Line is a message of a dialog as the admin reads it.
type Line struct {
	Role Role
	Text string
	At   time.Time
}

// User is a Telegram user known to the bot.
type User struct {
	ID         int64
	TelegramID int64 // the chat the bot writes to the user in
	Username   string
	Name       string
}
