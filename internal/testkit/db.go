// Package testkit holds helpers shared by the tests of several packages.
package testkit

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/IvanTurko/physics-school-bot/internal/storage/sqlite"
)

// NewDB opens a fresh migrated SQLite in a temporary directory, closed when the
// test ends.
func NewDB(t testing.TB) *sql.DB {
	t.Helper()
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}
