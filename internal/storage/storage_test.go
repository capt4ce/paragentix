package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOpenAppliesMigration(t *testing.T) {
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var version int
	if err := db.QueryRow("SELECT version FROM schema_migrations WHERE version = 1").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("version = %d", version)
	}
	if _, err := db.Exec("INSERT INTO sessions(id, profile_id, source, created_at, updated_at) VALUES ('s1','default','test',?,?)", Now(), Now()); err != nil {
		t.Fatal(err)
	}
	var title sql.NullString
	if err := db.QueryRow("SELECT title FROM sessions WHERE id = 's1'").Scan(&title); err != nil {
		t.Fatal(err)
	}
}
