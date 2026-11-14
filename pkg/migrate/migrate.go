// Package migrate applies versioned SQL migrations (goose) to a service's Postgres schema.
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"

	"github.com/pressly/goose/v3"

	// Registers the "pgx" database/sql driver used to open the migration connection.
	_ "github.com/jackc/pgx/v5/stdlib"
)

// StartEnvVar turns on automatic migrations when a service starts.
const StartEnvVar = "MIGRATE_ON_START"

// EnabledOnStart reports whether MIGRATE_ON_START is set to "true".
func EnabledOnStart() bool {
	return os.Getenv(StartEnvVar) == "true"
}

// Up applies every pending migration found at the root of fsys.
//
// The DSN must carry the service's search_path (for example "...&search_path=iam"),
// so the migration history table and all objects are created in the service's own schema.
// It returns the migrations that were applied; running it again returns an empty list.
func Up(ctx context.Context, dsn string, fsys fs.FS) ([]*goose.MigrationResult, error) {
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open migration connection: %w", err)
	}
	defer sqlDB.Close()

	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, fsys)
	if err != nil {
		return nil, fmt.Errorf("load migrations: %w", err)
	}
	results, err := provider.Up(ctx)
	if err != nil {
		return results, fmt.Errorf("apply migrations: %w", err)
	}
	return results, nil
}
