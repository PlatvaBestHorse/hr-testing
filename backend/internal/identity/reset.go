package identity

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ResetPassword is a maintenance CLI operation, never a public API endpoint.
func ResetPassword(ctx context.Context, pool *pgxpool.Pool, email, password string) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var id string
	err = tx.QueryRow(ctx, `UPDATE users SET password_hash=$2,updated_at=now() WHERE lower(email)=$1 RETURNING id`, strings.ToLower(strings.TrimSpace(email)), hash).Scan(&id)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM auth_sessions WHERE user_id=$1`, id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM account_tokens WHERE user_id=$1`, id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE user_security SET pending_ciphertext=NULL,pending_until=NULL WHERE user_id=$1`, id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(organization_id,action,object_type,object_id) SELECT organization_id,'auth.password.reset_cli','user',user_id FROM memberships WHERE user_id=$1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
