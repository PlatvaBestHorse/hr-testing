package database

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostgresFoundation(t *testing.T) {
	url, appURL := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_APP_DATABASE_URL")
	if url == "" || appURL == "" {
		t.Skip("run make integration with a local development database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	owner, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err = Migrate(ctx, owner); err != nil {
		t.Fatal(RedactError(err))
	}
	if err = Migrate(ctx, owner); err != nil {
		t.Fatal("repeat migration:", RedactError(err))
	}
	var count int
	if err = owner.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != 13 {
		t.Fatalf("migration registry: count=%d err=%v", count, err)
	}
	app, err := Open(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err = (Readiness{Pool: app}).Check(ctx); err != nil {
		t.Fatal("app readiness failed")
	}
	var super, bypass, ownerRole bool
	err = app.QueryRow(ctx, `SELECT rolsuper, rolbypassrls, pg_has_role(current_user, 'hr_owner', 'MEMBER') FROM pg_roles WHERE rolname = current_user`).Scan(&super, &bypass, &ownerRole)
	if err != nil || super || bypass || ownerRole {
		t.Fatal("app role must not own schema, bypass RLS or be superuser")
	}
	// Keep all fixtures inside rolled-back transactions.
	tx, err := app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var orgA, orgB, category string
	if err = tx.QueryRow(ctx, `INSERT INTO organizations (slug,name) VALUES ('integration-a-' || gen_random_uuid(), 'A') RETURNING id`).Scan(&orgA); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, `INSERT INTO organizations (slug,name) VALUES ('integration-b-' || gen_random_uuid(), 'B') RETURNING id`).Scan(&orgB); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, `INSERT INTO categories (organization_id,name) VALUES ($1,'Category A') RETURNING id`, orgA).Scan(&category); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO questions (organization_id,category_id) VALUES ($1,$2)`, orgA, category); err != nil {
		t.Fatal("same-tenant relation failed")
	}
	_, err = tx.Exec(ctx, `INSERT INTO questions (organization_id,category_id) VALUES ($1,$2)`, orgB, category)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatal("cross-tenant category relation was not rejected by FK")
	}
	tx.Rollback(ctx)
	tx, err = app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	_, err = tx.Exec(ctx, `UPDATE schema_migrations SET checksum='invalid'`)
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatal("app must not modify migration history")
	}
	tx.Rollback(ctx)
	tx, err = app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	_, err = tx.Exec(ctx, `CREATE TABLE foundation_should_not_exist (id integer)`)
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatal("app must not create tables")
	}
}
