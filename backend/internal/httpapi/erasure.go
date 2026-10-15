package httpapi

import (
	"context"
	"encoding/json"
	"github.com/PlatvaBestHorse/hr-testing/backend/internal/privacy"
	"github.com/jackc/pgx/v5"
	"net/http"
	"time"
)

const erasureConfirmation = "УДАЛИТЬ КАНДИДАТА"

type erasurePreview struct {
	ID          string    `json:"id"`
	Candidate   string    `json:"candidate_id"`
	Name        string    `json:"full_name"`
	Invitations int       `json:"invitations"`
	Attempts    int       `json:"attempts"`
	Answers     int       `json:"answers"`
	Comments    int       `json:"comments"`
	Active      int       `json:"active_attempts"`
	Hold        bool      `json:"retention_hold"`
	Expires     time.Time `json:"expires_at"`
}

// Hash the complete candidate graph in PostgreSQL: previews persist no contacts,
// answers or free text. A save/start/comment/hold change invalidates the preview.
func erasureSnapshot(ctx context.Context, tx pgx.Tx, org, id string) (erasurePreview, string, error) {
	out := erasurePreview{Candidate: id}
	var fingerprint string
	err := tx.QueryRow(ctx, `SELECT c.full_name,c.retention_hold,
 (SELECT count(*) FROM invitations WHERE organization_id=$1 AND candidate_id=$2),
 (SELECT count(*) FROM attempts WHERE organization_id=$1 AND candidate_id=$2),
 (SELECT count(*) FROM attempt_answers aa JOIN attempt_items ai ON ai.organization_id=aa.organization_id AND ai.id=aa.attempt_item_id JOIN attempts a ON a.organization_id=ai.organization_id AND a.id=ai.attempt_id WHERE a.organization_id=$1 AND a.candidate_id=$2),
 (SELECT count(*) FROM result_comments rc JOIN attempts a ON a.organization_id=rc.organization_id AND a.id=rc.attempt_id WHERE a.organization_id=$1 AND a.candidate_id=$2),
 (SELECT count(*) FROM attempts WHERE organization_id=$1 AND candidate_id=$2 AND status='in_progress'),
 encode(sha256(convert_to(jsonb_build_array(to_jsonb(c),
 (SELECT jsonb_agg(to_jsonb(i) ORDER BY i.id) FROM invitations i WHERE i.organization_id=$1 AND i.candidate_id=$2),
 (SELECT jsonb_agg(to_jsonb(a) ORDER BY a.id) FROM attempts a WHERE a.organization_id=$1 AND a.candidate_id=$2),
 (SELECT jsonb_agg(to_jsonb(aa) ORDER BY aa.attempt_item_id) FROM attempt_answers aa JOIN attempt_items ai ON ai.organization_id=aa.organization_id AND ai.id=aa.attempt_item_id JOIN attempts a ON a.organization_id=ai.organization_id AND a.id=ai.attempt_id WHERE a.organization_id=$1 AND a.candidate_id=$2),
 (SELECT jsonb_agg(to_jsonb(rc) ORDER BY rc.id) FROM result_comments rc JOIN attempts a ON a.organization_id=rc.organization_id AND a.id=rc.attempt_id WHERE a.organization_id=$1 AND a.candidate_id=$2),
 (SELECT jsonb_agg(to_jsonb(s) ORDER BY s.id) FROM candidate_consents s WHERE s.organization_id=$1 AND s.candidate_id=$2),
 (SELECT jsonb_agg(to_jsonb(m) ORDER BY m.id) FROM outbound_messages m JOIN invitations i ON i.organization_id=m.organization_id AND i.id=m.invitation_id WHERE i.organization_id=$1 AND i.candidate_id=$2)
 )::text,'UTF8')),'hex')
 FROM candidates c WHERE c.organization_id=$1 AND c.id=$2`, org, id).Scan(&out.Name, &out.Hold, &out.Invitations, &out.Attempts, &out.Answers, &out.Comments, &out.Active, &fingerprint)
	return out, fingerprint, err
}

func (a *App) previewErasure(w http.ResponseWriter, r *http.Request, p principal) {
	id := r.PathValue("id")
	if !uuid(id) {
		problem(w, 404, "not_found", "Кандидат не найден")
		return
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	if err = lockRetentionOrg(r.Context(), tx, p.OrganizationID); err != nil {
		a.failure(w, err)
		return
	}
	if !a.confirmRetentionAdmin(w, r, p, tx) {
		return
	}
	if err = privacy.LockCandidate(r.Context(), tx, p.OrganizationID, id); err == nil {
		err = privacy.LockWorkflow(r.Context(), tx, p.OrganizationID, id)
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	out, hash, err := erasureSnapshot(r.Context(), tx, p.OrganizationID, id)
	if err != nil {
		a.failure(w, err)
		return
	}
	now, err := databaseNow(r.Context(), tx)
	if err != nil {
		a.failure(w, err)
		return
	}
	out.Expires = now.Add(15 * time.Minute)
	err = tx.QueryRow(r.Context(), `INSERT INTO erasure_previews(organization_id,actor_user_id,candidate_id,fingerprint,expires_at) VALUES($1,$2,$3,$4,$5) RETURNING id`, p.OrganizationID, p.UserID, id, hash, out.Expires).Scan(&out.ID)
	if err == nil {
		err = a.audit(r.Context(), tx, p, "candidate.erasure.preview", "candidate", id)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, out)
}

func (a *App) applyErasure(w http.ResponseWriter, r *http.Request, p principal) {
	id := r.PathValue("id")
	if !uuid(id) {
		problem(w, 404, "not_found", "Предпросмотр не найден")
		return
	}
	var in struct {
		Confirmation string `json:"confirmation"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Confirmation != erasureConfirmation {
		problem(w, 422, "confirmation_required", "Введите УДАЛИТЬ КАНДИДАТА")
		return
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	if err = lockRetentionOrg(r.Context(), tx, p.OrganizationID); err != nil {
		a.failure(w, err)
		return
	}
	if !a.confirmRetentionAdmin(w, r, p, tx) {
		return
	}
	var candidate, hash string
	var expiry time.Time
	var applied *time.Time
	var previous []byte
	err = tx.QueryRow(r.Context(), `SELECT candidate_id,fingerprint,expires_at,applied_at,result FROM erasure_previews WHERE organization_id=$1 AND actor_user_id=$2 AND id=$3 FOR UPDATE`, p.OrganizationID, p.UserID, id).Scan(&candidate, &hash, &expiry, &applied, &previous)
	if err != nil {
		a.failure(w, err)
		return
	}
	if applied != nil {
		var receipt map[string]any
		if json.Unmarshal(previous, &receipt) != nil {
			problem(w, 500, "internal_error", "Не удалось прочитать квитанцию")
			return
		}
		write(w, 200, receipt)
		return
	}
	if err = privacy.LockCandidate(r.Context(), tx, p.OrganizationID, candidate); err == nil {
		err = privacy.LockWorkflow(r.Context(), tx, p.OrganizationID, candidate)
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	out, current, err := erasureSnapshot(r.Context(), tx, p.OrganizationID, candidate)
	if err != nil {
		a.failure(w, err)
		return
	}
	now, err := databaseNow(r.Context(), tx)
	if err != nil {
		a.failure(w, err)
		return
	}
	if out.Hold {
		problem(w, 409, "retention_hold", "Установлен запрет удаления. Сначала рассмотрите основание хранения")
		return
	}
	if !expiry.After(now) || hash != current {
		problem(w, 409, "preview_stale", "Данные изменились или предпросмотр истёк. Сформируйте новый")
		return
	}
	if err = privacy.Purge(r.Context(), tx, p.OrganizationID, candidate); err == nil {
		err = privacy.Record(r.Context(), tx, p.OrganizationID, candidate, "all")
	}
	receipt := map[string]any{"candidate_id": candidate, "erased": true, "completed_at": now}
	body, _ := json.Marshal(receipt)
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE erasure_previews SET applied_at=$4,result=$5 WHERE organization_id=$1 AND actor_user_id=$2 AND id=$3`, p.OrganizationID, p.UserID, id, now, body)
	}
	if err == nil {
		err = a.audit(r.Context(), tx, p, "candidate.erasure.apply", "candidate", candidate)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, receipt)
}
