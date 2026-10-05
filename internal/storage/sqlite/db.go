// Package sqlite keeps the bot's data in SQLite.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/IvanTurko/physics-school-bot/internal/clock"

	_ "modernc.org/sqlite"
)

// dsnParams holds every setting: a PRAGMA run through db.Exec reaches one connection.
// _txlock=immediate takes the write lock at BeginTx, so writers queue on busy_timeout.
const dsnParams = "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate"

//go:embed migrations/*.sql
var migrations embed.FS

// Open opens the database at path, creating its directory, and applies the
// migrations it has not seen yet.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("cannot create database directory: %w", err)
	}
	db, err := sql.Open("sqlite", "file:"+path+dsnParams)
	if err != nil {
		return nil, err
	}
	if err := migrate(ctx, db, migrations); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return db, nil
}

// migrate applies migrations/NNNN_name.sql in order. Each file and the bump of
// user_version share a transaction, so a failed one leaves no trace.
func migrate(ctx context.Context, db *sql.DB, fsys fs.FS) error {
	var current int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("cannot read schema version: %w", err)
	}

	names, err := fs.Glob(fsys, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)

	for _, name := range names {
		base := filepath.Base(name)
		n, err := strconv.Atoi(strings.SplitN(base, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("invalid migration name %q (want NNNN_name.sql)", base)
		}
		if n <= current {
			continue
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		if err := apply(ctx, db, n, string(body)); err != nil {
			return fmt.Errorf("cannot apply migration %s: %w", base, err)
		}
		current = n
	}
	return nil
}

func apply(ctx context.Context, db *sql.DB, version int, body string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, body); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		return err
	}
	return tx.Commit()
}

// Store is the bot's data in SQLite.
type Store struct {
	db    *sql.DB
	clock clock.Clock
}

// New wraps an open database; clock stamps created_at.
func New(db *sql.DB, c clock.Clock) *Store {
	return &Store{db: db, clock: c}
}
