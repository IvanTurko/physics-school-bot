package sqlite

import (
	"context"

	"github.com/IvanTurko/physics-school-bot/internal/domain"
)

// AddCard remembers where a card of a booking went.
func (s *Store) AddCard(ctx context.Context, c domain.Card) error {
	_, err := s.q(ctx).ExecContext(ctx,
		`INSERT INTO cards (booking_id, chat_id, message_id) VALUES (?, ?, ?)`, c.BookingID, c.ChatID, c.MessageID)
	return err
}

// Cards returns where the cards of the booking went.
func (s *Store) Cards(ctx context.Context, bookingID int64) ([]domain.Card, error) {
	rows, err := s.q(ctx).QueryContext(ctx,
		`SELECT booking_id, chat_id, message_id FROM cards WHERE booking_id = ?`, bookingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cards []domain.Card
	for rows.Next() {
		var c domain.Card
		if err := rows.Scan(&c.BookingID, &c.ChatID, &c.MessageID); err != nil {
			return nil, err
		}
		cards = append(cards, c)
	}
	return cards, rows.Err()
}
