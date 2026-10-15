// Package privacy implements the same erasure scopes for HTTP and offline recovery.
package privacy

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// Serialize issuance, card edits and erasure before taking workflow row locks.
func LockCandidate(ctx context.Context, tx pgx.Tx, org, candidate string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('candidate-privacy:'||$1||':'||$2,0))`, org, candidate)
	return err
}

// Existing passage mutations lock invitation -> attempt. New issuance is guarded
// by LockCandidate; no invitation can appear while this lock set is acquired.
func LockWorkflow(ctx context.Context, tx pgx.Tx, org, candidate string) error {
	for _, query := range []string{
		`SELECT id FROM invitations WHERE organization_id=$1 AND candidate_id=$2 ORDER BY id FOR UPDATE`,
		`SELECT id FROM candidates WHERE organization_id=$1 AND id=$2 FOR NO KEY UPDATE`,
		`SELECT id FROM attempts WHERE organization_id=$1 AND candidate_id=$2 ORDER BY id FOR UPDATE`,
	} {
		rows, err := tx.Query(ctx, query, org, candidate)
		if err != nil {
			return err
		}
		for rows.Next() {
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func Record(ctx context.Context, tx pgx.Tx, org, candidate, scope string) error {
	_, err := tx.Exec(ctx, `INSERT INTO candidate_erasures(organization_id,candidate_id,scope) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, org, candidate, scope)
	return err
}

// Caller holds the workflow locks (or is operating on a fresh offline recovery DB).
// Shared question/test versions and their images are never removed.
func Purge(ctx context.Context, tx pgx.Tx, org, candidate string) error {
	for _, query := range []string{
		`DELETE FROM result_comments WHERE organization_id=$1 AND attempt_id IN (SELECT id FROM attempts WHERE organization_id=$1 AND candidate_id=$2)`,
		`DELETE FROM answer_receipts WHERE organization_id=$1 AND attempt_item_id IN (SELECT ai.id FROM attempt_items ai JOIN attempts a ON a.organization_id=ai.organization_id AND a.id=ai.attempt_id WHERE a.organization_id=$1 AND a.candidate_id=$2)`,
		`DELETE FROM attempt_answers WHERE organization_id=$1 AND attempt_item_id IN (SELECT ai.id FROM attempt_items ai JOIN attempts a ON a.organization_id=ai.organization_id AND a.id=ai.attempt_id WHERE a.organization_id=$1 AND a.candidate_id=$2)`,
		`DELETE FROM attempt_items WHERE organization_id=$1 AND attempt_id IN (SELECT id FROM attempts WHERE organization_id=$1 AND candidate_id=$2)`,
		`DELETE FROM start_receipts WHERE organization_id=$1 AND invitation_id IN (SELECT id FROM invitations WHERE organization_id=$1 AND candidate_id=$2)`,
		`DELETE FROM attempts WHERE organization_id=$1 AND candidate_id=$2`,
		`DELETE FROM outbound_messages WHERE organization_id=$1 AND invitation_id IN (SELECT id FROM invitations WHERE organization_id=$1 AND candidate_id=$2)`,
		`DELETE FROM invitations WHERE organization_id=$1 AND candidate_id=$2`,
		`DELETE FROM candidate_consents WHERE organization_id=$1 AND candidate_id=$2`,
		`DELETE FROM candidates WHERE organization_id=$1 AND id=$2`,
	} {
		if _, err := tx.Exec(ctx, query, org, candidate); err != nil {
			return err
		}
	}
	return nil
}

func Redact(ctx context.Context, tx pgx.Tx, org, candidate string) error {
	for _, query := range []string{
		`UPDATE candidates SET full_name='Контактные данные удалены',email=NULL,phone=NULL,status='archived',erased_at=clock_timestamp(),updated_at=clock_timestamp(),retention_hold=false,retention_hold_reason='' WHERE organization_id=$1 AND id=$2 AND erased_at IS NULL`,
		`UPDATE invitations SET status='revoked' WHERE organization_id=$1 AND candidate_id=$2`,
		`UPDATE outbound_messages m SET recipient='redacted',status=CASE WHEN status IN ('pending','failed') THEN 'cancelled' ELSE status END WHERE m.organization_id=$1 AND EXISTS(SELECT 1 FROM invitations i WHERE i.organization_id=m.organization_id AND i.id=m.invitation_id AND i.candidate_id=$2)`,
		`DELETE FROM candidate_consents WHERE organization_id=$1 AND candidate_id=$2`,
	} {
		if _, err := tx.Exec(ctx, query, org, candidate); err != nil {
			return err
		}
	}
	return nil
}
