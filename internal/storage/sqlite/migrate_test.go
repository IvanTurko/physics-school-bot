package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestMigrateRefusesUnnumberedFile(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})

	fsys := fstest.MapFS{"migrations/init.sql": {Data: []byte("SELECT 1")}}
	if err := migrate(context.Background(), db, fsys); err == nil || !strings.Contains(err.Error(), "invalid migration name") {
		t.Fatalf("err = %v, want invalid migration name", err)
	}
}
