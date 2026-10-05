package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/IvanTurko/physics-school-bot/internal/domain"
)

// Append adds messages to the user's history in one transaction, so a tool call
// and its results are either both saved or neither.
func (s *Store) Append(ctx context.Context, userID int64, msgs ...domain.Message) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := s.clock.Now().Unix()
	for _, m := range msgs {
		calls, err := encodeCalls(m.ToolCalls)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO messages (user_id, role, content, tool_calls, tool_call_id, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			userID, m.Role, m.Content, calls, m.ToolCallID, now)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Last returns up to n latest messages, oldest first, starting at a user message
// so that no tool result comes without its call.
func (s *Store) Last(ctx context.Context, userID int64, n int) ([]domain.Message, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT role, content, tool_calls, tool_call_id FROM messages
		WHERE user_id = ? ORDER BY id DESC LIMIT ?`, userID, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []domain.Message
	for rows.Next() {
		var m domain.Message
		var calls sql.NullString
		if err := rows.Scan(&m.Role, &m.Content, &calls, &m.ToolCallID); err != nil {
			return nil, err
		}
		if m.ToolCalls, err = decodeCalls(calls); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	slices.Reverse(msgs)
	start := slices.IndexFunc(msgs, func(m domain.Message) bool { return m.Role == domain.RoleUser })
	if start < 0 {
		return nil, nil
	}
	return msgs[start:], nil
}

// storedCall is a tool call as the tool_calls column keeps it; the tags pin the
// format, so renaming a domain field does not break saved dialogs.
type storedCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func encodeCalls(calls []domain.ToolCall) (sql.NullString, error) {
	if len(calls) == 0 {
		return sql.NullString{}, nil
	}
	stored := make([]storedCall, len(calls))
	for i, c := range calls {
		stored[i] = storedCall{ID: c.ID, Name: c.Name, Arguments: c.Arguments}
	}
	b, err := json.Marshal(stored)
	if err != nil {
		return sql.NullString{}, err
	}
	return sql.NullString{String: string(b), Valid: true}, nil
}

func decodeCalls(column sql.NullString) ([]domain.ToolCall, error) {
	if !column.Valid {
		return nil, nil
	}
	var stored []storedCall
	if err := json.Unmarshal([]byte(column.String), &stored); err != nil {
		return nil, fmt.Errorf("cannot decode tool_calls: %w", err)
	}
	calls := make([]domain.ToolCall, len(stored))
	for i, c := range stored {
		calls[i] = domain.ToolCall{ID: c.ID, Name: c.Name, Arguments: c.Arguments}
	}
	return calls, nil
}

// talkSQL matches the student's and the bot's texts, leaving out tool calls and their results.
const talkSQL = `role IN ('user', 'assistant') AND tool_calls IS NULL`

// CountDialog returns the user and how many lines their dialog has.
func (s *Store) CountDialog(ctx context.Context, userID int64) (domain.User, int, error) {
	var u domain.User
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT username, name, (SELECT COUNT(*) FROM messages WHERE user_id = users.id AND `+talkSQL+`)
		FROM users WHERE id = ?`, userID).Scan(&u.Username, &u.Name, &n)
	return u, n, err
}

// ListDialog returns limit of the lines of the user's dialog, offset back from the latest, oldest first.
func (s *Store) ListDialog(ctx context.Context, userID int64, offset, limit int) ([]domain.Line, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT role, content, created_at FROM messages
		WHERE user_id = ? AND `+talkSQL+` ORDER BY id DESC LIMIT ? OFFSET ?`, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var lines []domain.Line
	for rows.Next() {
		var l domain.Line
		var at int64
		if err := rows.Scan(&l.Role, &l.Text, &at); err != nil {
			return nil, err
		}
		l.At = time.Unix(at, 0)
		lines = append(lines, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	slices.Reverse(lines)
	return lines, nil
}
