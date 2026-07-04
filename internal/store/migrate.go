package store

import (
	"database/sql"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx" for goose
	"github.com/michaelpeterswa/washington-fish-api/internal/store/migrations"
	"github.com/pressly/goose/v3"
)

// Migrate applies all pending goose migrations embedded in
// internal/store/migrations against databaseURL. Run via `wfa-worker migrate`.
func Migrate(databaseURL string) error {
	if databaseURL == "" {
		return fmt.Errorf("store: DATABASE_URL is empty")
	}

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("store: open db for migrate: %w", err)
	}
	defer func() { _ = db.Close() }()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("store: set goose dialect: %w", err)
	}
	if err := goose.Up(db, "."); err != nil {
		return fmt.Errorf("store: goose up: %w", err)
	}
	return nil
}
