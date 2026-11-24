package db

import (
	"context"
	"database/sql"
	"github.com/pressly/goose/v3"
	"testing"
)

func TestEmbeddedMigrationsApply(t *testing.T) {
	database, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	goose.SetBaseFS(FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.Up(database, "migrations"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := database.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM goals`).Scan(&count); err != nil {
		t.Fatal(err)
	}
}
