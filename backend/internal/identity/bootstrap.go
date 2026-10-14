package identity

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
)

type BootstrapInput struct {
	OrganizationName string `json:"organization_name"`
	DisplayName      string `json:"display_name"`
	Email            string `json:"email"`
	Password         string `json:"password"`
}

func ValidEmail(email string) bool {
	a, err := mail.ParseAddress(email)
	return err == nil && a.Address == email && len(email) <= 320
}

func Bootstrap(ctx context.Context, pool *pgxpool.Pool, in BootstrapInput) error {
	in.OrganizationName = strings.TrimSpace(in.OrganizationName)
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	if !ValidEmail(in.Email) || in.OrganizationName == "" || in.DisplayName == "" || utf8.RuneCountInString(in.OrganizationName) > 250 || utf8.RuneCountInString(in.DisplayName) > 250 {
		return errors.New("invalid account details")
	}
	hash, err := HashPassword(in.Password)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(814230020)`); err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organizations) OR EXISTS(SELECT 1 FROM users)`).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return errors.New("bootstrap is already complete; use organization team management")
	}
	var orgID, userID string
	if err = tx.QueryRow(ctx, `INSERT INTO organizations(slug,name) VALUES ('default',$1) RETURNING id`, in.OrganizationName).Scan(&orgID); err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, `INSERT INTO users(email,password_hash,display_name) VALUES ($1,$2,$3) RETURNING id`, in.Email, hash, in.DisplayName).Scan(&userID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO memberships(organization_id,user_id,role) VALUES ($1,$2,'org_admin')`, orgID, userID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(organization_id,actor_user_id,action,object_type,object_id) VALUES ($1,$2,'organization.bootstrap','organization',$1)`, orgID, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
