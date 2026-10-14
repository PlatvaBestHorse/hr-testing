package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/PlatvaBestHorse/hr-testing/backend/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	// Session lock serializes runners, including before the registry exists.
	if _, err = conn.Exec(ctx, `SELECT pg_advisory_lock(814230019)`); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock(814230019)`)
	if _, err = conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	names, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		body, err := migrations.Files.ReadFile(name)
		if err != nil {
			return err
		}
		h := sha256.Sum256(body)
		checksum := hex.EncodeToString(h[:])
		var saved string
		err = conn.QueryRow(ctx, `SELECT checksum FROM schema_migrations WHERE name=$1`, name).Scan(&saved)
		if err == nil {
			if saved != checksum {
				return fmt.Errorf("migration %s has changed after application", name)
			}
			continue
		}
		if err != pgx.ErrNoRows {
			return err
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, string(body)); err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO schema_migrations (name,checksum) VALUES ($1,$2)`, name, checksum)
		}
		if err != nil {
			tx.Rollback(context.Background())
			return fmt.Errorf("migration %s failed: %w", name, err)
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

// RedactError deliberately omits driver errors, which may expose connection details.
func RedactError(err error) string {
	if err == nil {
		return ""
	}
	if strings.Contains(err.Error(), "has changed after application") {
		return err.Error()
	}
	return "database operation failed; check connectivity, role privileges and migration SQL"
}
