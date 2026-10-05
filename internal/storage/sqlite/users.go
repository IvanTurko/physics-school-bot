package sqlite

import (
	"context"
	"time"

	"github.com/IvanTurko/physics-school-bot/internal/domain"
)

// EnsureUser returns the user with this Telegram ID, creating it on first
// contact and refreshing the username and name on every one.
func (s *Store) EnsureUser(ctx context.Context, tgID int64, username, name string) (domain.User, error) {
	var u domain.User
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO users (tg_id, username, name, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (tg_id) DO UPDATE SET username = excluded.username, name = excluded.name
		RETURNING id, tg_id, username, name`,
		tgID, username, name, s.clock.Now().Unix(),
	).Scan(&u.ID, &u.TelegramID, &u.Username, &u.Name)
	if err != nil {
		return domain.User{}, err
	}
	return u, nil
}

// SetDemoUntil makes the user a demo admin until the given time.
func (s *Store) SetDemoUntil(ctx context.Context, userID int64, until time.Time) error {
	_, err := s.q(ctx).ExecContext(ctx, `UPDATE users SET demo_until = ? WHERE id = ?`, until.Unix(), userID)
	return err
}

// DemoAdmins returns the Telegram IDs of the users whose demo rights last past now.
func (s *Store) DemoAdmins(ctx context.Context, now time.Time) ([]int64, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT tg_id FROM users WHERE demo_until > ?`, now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
