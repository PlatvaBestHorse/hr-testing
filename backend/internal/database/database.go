package database

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
)

func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	c, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("invalid database configuration")
	}
	c.MaxConns = 10
	c.MinConns = 1
	return pgxpool.NewWithConfig(ctx, c)
}

type Readiness struct{ Pool *pgxpool.Pool }

func (r Readiness) Check(ctx context.Context) error {
	var complete bool
	// Require the last migration, not merely an open connection.
	err := r.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name = '0015_candidate_privacy.sql')`).Scan(&complete)
	if err != nil {
		return err
	}
	if !complete {
		return fmt.Errorf("database migrations are incomplete")
	}
	return nil
}
