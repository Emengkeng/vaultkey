package db
 
import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"sort"
	"strings"
	"time"
)
 
//go:embed migrations/*.sql
var migrationsFS embed.FS
 
// Run applies all pending migrations in filename order.
// Safe to call every startup — already-applied migrations are skipped.
// Returns an error if any migration fails — caller should not start the server.
//
// Usage in main.go:
//
//	if err := db.Run(ctx, sqlDB); err != nil {
//	    log.Fatalf("migrations failed: %v", err)
//	}
func Run(ctx context.Context, database *sql.DB) error {
	if err := ensureMigrationsTable(ctx, database); err != nil {
		return fmt.Errorf("ensure migrations table: %w", err)
	}
 
	files, err := migrationFiles()
	if err != nil {
		return fmt.Errorf("list migration files: %w", err)
	}
 
	applied, err := appliedMigrations(ctx, database)
	if err != nil {
		return fmt.Errorf("fetch applied migrations: %w", err)
	}
 
	pending := 0
	for _, name := range files {
		if applied[name] {
			log.Printf("migrate: skip %s (already applied)", name)
			continue
		}
 
		pending++
		log.Printf("migrate: applying %s ...", name)
 
		if err := applyMigration(ctx, database, name); err != nil {
			return fmt.Errorf("migration %s failed: %w", name, err)
		}
 
		log.Printf("migrate: ✓ %s", name)
	}
 
	if pending == 0 {
		log.Printf("migrate: schema up to date (%d migration(s) checked)", len(files))
	} else {
		log.Printf("migrate: %d migration(s) applied", pending)
	}
 
	return nil
}
 
func ensureMigrationsTable(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
		    name        TEXT PRIMARY KEY,
		    applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`)
	if err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	return nil
}
 
func migrationFiles() ([]string, error) {
	var names []string
 
	err := fs.WalkDir(migrationsFS, "migrations", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".sql") {
			names = append(names, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
 
	// Lexicographic sort guarantees 001 < 002 < 003.
	sort.Strings(names)
	return names, nil
}
 
func appliedMigrations(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM schema_migrations ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
 
	applied := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		applied[name] = true
	}
	return applied, rows.Err()
}
 
// applyMigration runs one migration file inside a transaction.
// If the SQL fails, the transaction rolls back and the migration is
// NOT recorded — so it will be retried on next startup.
// Recording the migration in the same transaction as the DDL means
// we never get a half-applied migration that looks "done".
func applyMigration(ctx context.Context, db *sql.DB, name string) error {
	content, err := migrationsFS.ReadFile(name)
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
 
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
 
	if _, err := tx.ExecContext(ctx, string(content)); err != nil {
		return fmt.Errorf("exec sql: %w", err)
	}
 
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (name, applied_at)
		 VALUES ($1, $2)
		 ON CONFLICT (name) DO NOTHING`,
		name, time.Now().UTC(),
	); err != nil {
		return fmt.Errorf("record migration: %w", err)
	}
 
	return tx.Commit()
}