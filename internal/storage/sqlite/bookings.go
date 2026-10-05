package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/IvanTurko/physics-school-bot/internal/domain"
	driver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

type txKey struct{}

// InTx runs fn in one immediate transaction; store calls made with fn's ctx join it.
func (s *Store) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(context.WithValue(ctx, txKey{}, tx)); err != nil {
		return err
	}
	return tx.Commit()
}

type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// q returns the transaction ctx carries, or the database outside one.
func (s *Store) q(ctx context.Context) querier {
	if tx, ok := ctx.Value(txKey{}).(*sql.Tx); ok {
		return tx
	}
	return s.db
}

// freeSQL matches a slot that no new or confirmed booking holds.
const freeSQL = `id NOT IN (SELECT slot_id FROM bookings WHERE status IN ('new', 'confirmed'))`

// AddSlots creates slots at starts, skipping every time that has a slot, even a deleted one,
// and returns how many it created.
func (s *Store) AddSlots(ctx context.Context, starts []time.Time) (int, error) {
	return s.putSlots(ctx, `INSERT INTO slots (starts_at) VALUES (?) ON CONFLICT (starts_at) DO NOTHING`, starts)
}

// OpenSlots creates the missing slots at starts, restores the deleted ones and returns how many it opened.
func (s *Store) OpenSlots(ctx context.Context, starts []time.Time) (int, error) {
	return s.putSlots(ctx, `INSERT INTO slots (starts_at) VALUES (?)
		ON CONFLICT (starts_at) DO UPDATE SET deleted_at = NULL WHERE deleted_at IS NOT NULL`, starts)
}

// putSlots runs query for each start and returns how many slots it changed.
func (s *Store) putSlots(ctx context.Context, query string, starts []time.Time) (int, error) {
	changed := 0
	for _, t := range starts {
		res, err := s.q(ctx).ExecContext(ctx, query, t.Unix())
		if err != nil {
			return 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		changed += int(n)
	}
	return changed, nil
}

// FreeSlots returns up to limit slots in [from, to) that are not deleted and no booking holds,
// earliest first.
func (s *Store) FreeSlots(ctx context.Context, from, to time.Time, limit int) ([]domain.Slot, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `
		SELECT id, starts_at FROM slots
		WHERE starts_at >= ? AND starts_at < ? AND deleted_at IS NULL AND `+freeSQL+`
		ORDER BY starts_at LIMIT ?`, from.Unix(), to.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var slots []domain.Slot
	for rows.Next() {
		var slot domain.Slot
		var start int64
		if err := rows.Scan(&slot.ID, &start); err != nil {
			return nil, err
		}
		slot.Start = time.Unix(start, 0)
		slots = append(slots, slot)
	}
	return slots, rows.Err()
}

// SlotAt returns the slot starting at start, or ErrSlotNotFound.
func (s *Store) SlotAt(ctx context.Context, start time.Time) (domain.Slot, error) {
	slot := domain.Slot{Start: start}
	err := s.q(ctx).QueryRowContext(ctx, `SELECT id FROM slots WHERE starts_at = ? AND deleted_at IS NULL`, start.Unix()).Scan(&slot.ID)
	if err != nil {
		return domain.Slot{}, orMissing(err, domain.ErrSlotNotFound)
	}
	return slot, nil
}

// DeleteFreeSlot deletes the slot unless it is already deleted or held, and reports whether it did.
func (s *Store) DeleteFreeSlot(ctx context.Context, id int64) (bool, error) {
	res, err := s.q(ctx).ExecContext(ctx,
		`UPDATE slots SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL AND `+freeSQL, s.clock.Now().Unix(), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// aheadSQL matches a slot that is not deleted and starts after now; its parameter is now.
const aheadSQL = `s.deleted_at IS NULL AND s.starts_at > ?`

// CountSlots counts the slots ahead that are not deleted.
func (s *Store) CountSlots(ctx context.Context) (int, error) {
	var n int
	err := s.q(ctx).QueryRowContext(ctx, `SELECT COUNT(*) FROM slots s WHERE `+aheadSQL, s.clock.Now().Unix()).Scan(&n)
	return n, err
}

// ListSlots returns limit of the slots ahead that are not deleted, from offset, earliest first.
func (s *Store) ListSlots(ctx context.Context, offset, limit int) ([]domain.SlotView, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `
		SELECT s.id, s.starts_at, COALESCE(b.id, 0), COALESCE(u.name, '') FROM slots s
		LEFT JOIN bookings b ON b.slot_id = s.id AND b.status IN ('new', 'confirmed')
		LEFT JOIN users u ON u.id = b.user_id
		WHERE `+aheadSQL+` ORDER BY s.starts_at LIMIT ? OFFSET ?`,
		s.clock.Now().Unix(), limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var slots []domain.SlotView
	for rows.Next() {
		var v domain.SlotView
		var start int64
		if err := rows.Scan(&v.ID, &start, &v.BookingID, &v.Name); err != nil {
			return nil, err
		}
		v.Start = time.Unix(start, 0)
		slots = append(slots, v)
	}
	return slots, rows.Err()
}

// activeSQL matches a new or confirmed booking whose lesson is ahead; its parameter is now.
const activeSQL = `b.status IN ('new', 'confirmed') AND s.starts_at > ?`

// ActiveBooking returns the user's booking that waits for its lesson.
func (s *Store) ActiveBooking(ctx context.Context, userID int64) (domain.BookingView, error) {
	v, err := scanView(s.q(ctx).QueryRowContext(ctx, viewQuery+`
		WHERE b.user_id = ? AND `+activeSQL, userID, s.clock.Now().Unix()))
	if err != nil {
		return domain.BookingView{}, orMissing(err, domain.ErrNoActiveBooking)
	}
	return v, nil
}

// InsertBooking saves a new booking; a slot another booking holds gives ErrSlotTaken.
func (s *Store) InsertBooking(ctx context.Context, b domain.Booking) (domain.BookingView, error) {
	var id int64
	now := s.clock.Now().Unix()
	err := s.q(ctx).QueryRowContext(ctx, `
		INSERT INTO bookings (user_id, slot_id, grade, goal, phone, status, created_at, reminded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		b.UserID, b.SlotID, b.Grade, b.Goal, b.Phone, b.Status, now, now,
	).Scan(&id)
	if err != nil {
		return domain.BookingView{}, orTaken(err)
	}
	return s.Booking(ctx, id)
}

// UpdateBooking saves a booking's slot and status and raises its version; a new slot restarts
// the reminders, and a slot another booking holds gives ErrSlotTaken.
func (s *Store) UpdateBooking(ctx context.Context, b domain.Booking) (domain.BookingView, error) {
	_, err := s.q(ctx).ExecContext(ctx,
		`UPDATE bookings SET version = version + 1, reminded_at = IIF(slot_id = ?, reminded_at, ?), slot_id = ?, status = ? WHERE id = ?`,
		b.SlotID, s.clock.Now().Unix(), b.SlotID, b.Status, b.ID)
	if err != nil {
		return domain.BookingView{}, orTaken(err)
	}
	return s.Booking(ctx, b.ID)
}

// Profile returns the user's active booking, latest booking of any status and
// whether one of their lessons was held.
func (s *Store) Profile(ctx context.Context, userID int64) (domain.Profile, error) {
	last, err := scanView(s.q(ctx).QueryRowContext(ctx, viewQuery+`
		WHERE b.user_id = ? ORDER BY b.id DESC LIMIT 1`, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Profile{}, nil
	}
	if err != nil {
		return domain.Profile{}, err
	}

	p := domain.Profile{Last: &last.Booking}
	err = s.q(ctx).QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM bookings WHERE user_id = ? AND status = 'done')`, userID).Scan(&p.TrialHeld)
	if err != nil {
		return domain.Profile{}, err
	}
	active, err := s.ActiveBooking(ctx, userID)
	if errors.Is(err, domain.ErrNoActiveBooking) {
		return p, nil
	}
	if err != nil {
		return domain.Profile{}, err
	}
	p.Active = &active
	return p, nil
}

const (
	viewColumns = `b.id, b.user_id, b.slot_id, b.grade, b.goal, b.phone, b.status, s.starts_at,
	       u.tg_id, u.username, u.name`
	viewTables = `
	FROM bookings b JOIN slots s ON s.id = b.slot_id JOIN users u ON u.id = b.user_id`
	viewQuery = `
	SELECT ` + viewColumns + viewTables
)

// Booking returns the booking with this ID.
func (s *Store) Booking(ctx context.Context, id int64) (domain.BookingView, error) {
	return scanView(s.q(ctx).QueryRowContext(ctx, viewQuery+` WHERE b.id = ?`, id))
}

// Upcoming returns the active bookings whose lesson is ahead, earliest first.
func (s *Store) Upcoming(ctx context.Context) ([]domain.Lesson, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `
		SELECT `+viewColumns+`, b.created_at, b.reminded_at`+viewTables+`
		WHERE `+activeSQL+` ORDER BY s.starts_at`,
		s.clock.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var lessons []domain.Lesson
	for rows.Next() {
		var booked, reminded int64
		v, err := scanView(rows, &booked, &reminded)
		if err != nil {
			return nil, err
		}
		lessons = append(lessons, domain.Lesson{BookingView: v, Booked: time.Unix(booked, 0), Reminded: time.Unix(reminded, 0)})
	}
	return lessons, rows.Err()
}

// MarkReminded records that the booking's student was reminded now.
func (s *Store) MarkReminded(ctx context.Context, id int64) error {
	_, err := s.q(ctx).ExecContext(ctx, `UPDATE bookings SET reminded_at = ? WHERE id = ?`, s.clock.Now().Unix(), id)
	return err
}

// Unsynced returns the bookings changed since they were last written to the sheet.
func (s *Store) Unsynced(ctx context.Context) ([]domain.Lead, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `
		SELECT `+viewColumns+`, b.created_at, b.version`+viewTables+`
		WHERE b.synced_version < b.version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var leads []domain.Lead
	for rows.Next() {
		var booked int64
		var version int
		v, err := scanView(rows, &booked, &version)
		if err != nil {
			return nil, err
		}
		leads = append(leads, domain.Lead{BookingView: v, Booked: time.Unix(booked, 0), Version: version})
	}
	return leads, rows.Err()
}

// MarkSynced records that the sheet shows the booking at version; a later
// change keeps it unsynced.
func (s *Store) MarkSynced(ctx context.Context, id int64, version int) error {
	_, err := s.q(ctx).ExecContext(ctx, `UPDATE bookings SET synced_version = ? WHERE id = ?`, version, id)
	return err
}

// filterSQL is the admin's list a booking is in; its parameter is now.
const filterSQL = `CASE WHEN b.status IN ('new', 'confirmed') AND s.starts_at <= ? THEN 'awaiting' ELSE b.status END`

// CountBookings returns how many bookings each of the admin's lists holds now.
func (s *Store) CountBookings(ctx context.Context) (map[domain.Filter]int, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `
		SELECT `+filterSQL+`, COUNT(*) FROM bookings b JOIN slots s ON s.id = b.slot_id GROUP BY 1`,
		s.clock.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := make(map[domain.Filter]int)
	for rows.Next() {
		var f domain.Filter
		var n int
		if err := rows.Scan(&f, &n); err != nil {
			return nil, err
		}
		counts[f] = n
	}
	return counts, rows.Err()
}

// ListBookings returns limit bookings of the list from offset, closest to now first.
func (s *Store) ListBookings(ctx context.Context, f domain.Filter, offset, limit int) ([]domain.BookingView, error) {
	now := s.clock.Now().Unix()
	rows, err := s.q(ctx).QueryContext(ctx, viewQuery+`
		WHERE `+filterSQL+` = ? ORDER BY ABS(s.starts_at - ?), b.id LIMIT ? OFFSET ?`,
		now, f, now, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var views []domain.BookingView
	for rows.Next() {
		v, err := scanView(rows)
		if err != nil {
			return nil, err
		}
		views = append(views, v)
	}
	return views, rows.Err()
}

// scanView reads a booking view; more takes the columns after the view's.
func scanView(row interface{ Scan(dest ...any) error }, more ...any) (domain.BookingView, error) {
	var v domain.BookingView
	var start int64
	err := row.Scan(append([]any{
		&v.ID, &v.UserID, &v.SlotID, &v.Grade, &v.Goal, &v.Phone, &v.Status, &start,
		&v.User.TelegramID, &v.User.Username, &v.User.Name,
	}, more...)...)
	if err != nil {
		return domain.BookingView{}, err
	}
	v.Start = time.Unix(start, 0)
	return v, nil
}

// orMissing names what a query found no row for.
func orMissing(err, missing error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return missing
	}
	return err
}

// orTaken turns a clash on bookings_slot_active into ErrSlotTaken.
func orTaken(err error) error {
	var e *driver.Error
	if errors.As(err, &e) && e.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE {
		return domain.ErrSlotTaken
	}
	return err
}
