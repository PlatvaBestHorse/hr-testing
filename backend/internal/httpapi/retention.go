package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/PlatvaBestHorse/hr-testing/backend/internal/privacy"
	"github.com/jackc/pgx/v5"
)

type retentionPolicy struct {
	Enabled       bool       `json:"enabled"`
	CandidateDays *int       `json:"candidate_days"`
	AuditDays     *int       `json:"audit_days"`
	Purpose       string     `json:"purpose"`
	Basis         string     `json:"legal_basis"`
	Revision      int        `json:"revision"`
	Updated       *time.Time `json:"updated_at"`
}
type retentionCandidate struct {
	ID       string    `json:"id"`
	Name     string    `json:"full_name"`
	Activity time.Time `json:"last_activity_at"`
}
type retentionReceipt struct {
	Erased       int       `json:"candidates_erased"`
	Skipped      int       `json:"candidates_skipped"`
	AuditDeleted int       `json:"audit_events_deleted"`
	Completed    time.Time `json:"completed_at"`
}
type retentionPreview struct {
	ID             string               `json:"id"`
	Revision       int                  `json:"policy_revision"`
	AsOf           time.Time            `json:"as_of"`
	Expires        time.Time            `json:"expires_at"`
	Candidates     []retentionCandidate `json:"candidates"`
	AuditCount     int                  `json:"audit_events"`
	CandidateLimit int                  `json:"candidate_limit"`
	AuditLimit     int                  `json:"audit_limit"`
}

const retentionConfirmation = "УДАЛИТЬ ПО ПРАВИЛАМ"
const candidateActivity = `greatest(c.created_at,c.updated_at,
 coalesce((SELECT max(i.issued_at) FROM invitations i WHERE i.organization_id=c.organization_id AND i.candidate_id=c.id),c.created_at),
 coalesce((SELECT max(greatest(a.started_at,coalesce(a.finished_at,a.started_at))) FROM attempts a WHERE a.organization_id=c.organization_id AND a.candidate_id=c.id),c.created_at),
 coalesce((SELECT max(s.recorded_at) FROM candidate_consents s WHERE s.organization_id=c.organization_id AND s.candidate_id=c.id),c.created_at))`
const retentionEligible = `c.erased_at IS NULL AND NOT c.retention_hold AND c.status<>'erasure_pending' AND ` + candidateActivity + `<$2
 AND NOT EXISTS(SELECT 1 FROM attempts a WHERE a.organization_id=c.organization_id AND a.candidate_id=c.id AND a.status='in_progress')
 AND NOT EXISTS(SELECT 1 FROM invitations i WHERE i.organization_id=c.organization_id AND i.candidate_id=c.id AND i.status='issued' AND i.expires_at>$3)`

func readPolicy(ctx context.Context, tx pgx.Tx, org string) (retentionPolicy, error) {
	var p retentionPolicy
	err := tx.QueryRow(ctx, `SELECT enabled,candidate_days,audit_days,purpose,legal_basis,revision,updated_at FROM retention_policies WHERE organization_id=$1`, org).Scan(&p.Enabled, &p.CandidateDays, &p.AuditDays, &p.Purpose, &p.Basis, &p.Revision, &p.Updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, nil
	}
	return p, err
}
func lockRetentionOrg(ctx context.Context, tx pgx.Tx, org string) error {
	var id string
	return tx.QueryRow(ctx, `SELECT id FROM organizations WHERE id=$1 FOR NO KEY UPDATE`, org).Scan(&id)
}
func (a *App) confirmRetentionAdmin(w http.ResponseWriter, r *http.Request, p principal, tx pgx.Tx) bool {
	cookie, err := r.Cookie("hr_session")
	if err != nil {
		problem(w, 401, "unauthorized", "Сессия завершена")
		return false
	}
	var allowed bool
	err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM auth_sessions s JOIN memberships m ON m.organization_id=s.organization_id AND m.user_id=s.user_id JOIN users u ON u.id=s.user_id JOIN organizations o ON o.id=s.organization_id WHERE s.token_hash=$1 AND s.organization_id=$2 AND s.user_id=$3 AND s.expires_at>clock_timestamp() AND m.role='org_admin' AND m.status='active' AND u.status='active' AND o.status='active')`, digest(cookie.Value), p.OrganizationID, p.UserID).Scan(&allowed)
	if err != nil {
		a.failure(w, err)
		return false
	}
	if !allowed {
		problem(w, 401, "unauthorized", "Права или сессия изменились. Войдите снова")
		return false
	}
	return true
}
func (a *App) getRetention(w http.ResponseWriter, r *http.Request, p principal) {
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	policy, err := readPolicy(r.Context(), tx, p.OrganizationID)
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, policy)
}
func (a *App) saveRetention(w http.ResponseWriter, r *http.Request, p principal) {
	var in struct {
		Enabled       bool   `json:"enabled"`
		CandidateDays *int   `json:"candidate_days"`
		AuditDays     *int   `json:"audit_days"`
		Purpose       string `json:"purpose"`
		Basis         string `json:"legal_basis"`
		Revision      int    `json:"base_revision"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Purpose = strings.TrimSpace(in.Purpose)
	in.Basis = strings.TrimSpace(in.Basis)
	for _, days := range []*int{in.CandidateDays, in.AuditDays} {
		if days != nil && (*days < 1 || *days > 36500) {
			problem(w, 422, "invalid_policy", "Укажите срок от 1 до 36500 дней либо оставьте его пустым")
			return
		}
	}
	if in.Revision < 0 || utf8.RuneCountInString(in.Purpose) > 2000 || utf8.RuneCountInString(in.Basis) > 2000 || (in.Enabled && (!validText(in.Purpose, 2000) || !validText(in.Basis, 2000) || (in.CandidateDays == nil && in.AuditDays == nil))) {
		problem(w, 422, "invalid_policy", "Для включения укажите срок, цель и утверждённое основание хранения")
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
	current, err := readPolicy(r.Context(), tx, p.OrganizationID)
	if err != nil {
		a.failure(w, err)
		return
	}
	if in.Revision != current.Revision {
		problem(w, 409, "revision_conflict", "Правила изменились. Обновите страницу")
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO retention_policies(organization_id,enabled,candidate_days,audit_days,purpose,legal_basis,revision,updated_by)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(organization_id) DO UPDATE SET enabled=excluded.enabled,candidate_days=excluded.candidate_days,audit_days=excluded.audit_days,purpose=excluded.purpose,legal_basis=excluded.legal_basis,revision=excluded.revision,updated_by=excluded.updated_by,updated_at=clock_timestamp()`, p.OrganizationID, in.Enabled, in.CandidateDays, in.AuditDays, in.Purpose, in.Basis, current.Revision+1, p.UserID)
	if err == nil {
		err = a.audit(r.Context(), tx, p, "retention.policy.update", "organization", p.OrganizationID)
	}
	policy, readErr := readPolicy(r.Context(), tx, p.OrganizationID)
	if err == nil {
		err = readErr
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, policy)
}
func (a *App) previewRetention(w http.ResponseWriter, r *http.Request, p principal) {
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
	policy, err := readPolicy(r.Context(), tx, p.OrganizationID)
	if err != nil {
		a.failure(w, err)
		return
	}
	if !policy.Enabled {
		problem(w, 422, "policy_disabled", "Сначала задайте и включите правила хранения")
		return
	}
	now, err := databaseNow(r.Context(), tx)
	if err != nil {
		a.failure(w, err)
		return
	}
	out := retentionPreview{Revision: policy.Revision, AsOf: now, Expires: now.Add(15 * time.Minute), Candidates: []retentionCandidate{}, CandidateLimit: 100, AuditLimit: 1000}
	ids := []string{}
	auditIDs := []int64{}
	var auditCutoff *time.Time
	if policy.CandidateDays != nil {
		cutoff := now.Add(-time.Duration(*policy.CandidateDays) * 24 * time.Hour)
		rows, e := tx.Query(r.Context(), `SELECT c.id,c.full_name,`+candidateActivity+` FROM candidates c WHERE c.organization_id=$1 AND `+retentionEligible+` ORDER BY `+candidateActivity+`,c.id LIMIT 100`, p.OrganizationID, cutoff, now)
		if e != nil {
			a.failure(w, e)
			return
		}
		for rows.Next() {
			var c retentionCandidate
			if err = rows.Scan(&c.ID, &c.Name, &c.Activity); err != nil {
				break
			}
			ids = append(ids, c.ID)
			out.Candidates = append(out.Candidates, c)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			a.failure(w, err)
			return
		}
	}
	if policy.AuditDays != nil {
		cutoff := now.Add(-time.Duration(*policy.AuditDays) * 24 * time.Hour)
		auditCutoff = &cutoff
		rows, e := tx.Query(r.Context(), `SELECT id FROM audit_events WHERE organization_id=$1 AND occurred_at<$2 ORDER BY occurred_at,id LIMIT 1000`, p.OrganizationID, cutoff)
		if e != nil {
			a.failure(w, e)
			return
		}
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil {
				break
			}
			auditIDs = append(auditIDs, id)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			a.failure(w, err)
			return
		}
	}
	out.AuditCount = len(auditIDs)
	err = tx.QueryRow(r.Context(), `INSERT INTO retention_previews(organization_id,actor_user_id,policy_revision,as_of,expires_at,candidate_ids,audit_ids,audit_cutoff) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`, p.OrganizationID, p.UserID, policy.Revision, now, out.Expires, ids, auditIDs, auditCutoff).Scan(&out.ID)
	if err == nil {
		err = a.audit(r.Context(), tx, p, "retention.preview", "retention_preview", out.ID)
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
func (a *App) applyRetention(w http.ResponseWriter, r *http.Request, p principal) {
	var in struct {
		Confirmation string `json:"confirmation"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	if !uuid(id) {
		problem(w, 404, "not_found", "Предпросмотр не найден")
		return
	}
	if in.Confirmation != retentionConfirmation {
		problem(w, 422, "confirmation_required", "Введите фразу УДАЛИТЬ ПО ПРАВИЛАМ")
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
	var ids []string
	var auditIDs []int64
	var revision int
	var asOf, expires time.Time
	var applied *time.Time
	var previous []byte
	err = tx.QueryRow(r.Context(), `SELECT candidate_ids,audit_ids,policy_revision,as_of,expires_at,applied_at,result FROM retention_previews WHERE organization_id=$1 AND id=$2 AND actor_user_id=$3 FOR UPDATE`, p.OrganizationID, id, p.UserID).Scan(&ids, &auditIDs, &revision, &asOf, &expires, &applied, &previous)
	if err != nil {
		a.failure(w, err)
		return
	}
	if applied != nil {
		var result retentionReceipt
		if json.Unmarshal(previous, &result) != nil {
			problem(w, 500, "internal_error", "Не удалось прочитать результат операции")
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			a.failure(w, err)
			return
		}
		write(w, 200, result)
		return
	}
	now, err := databaseNow(r.Context(), tx)
	if err != nil {
		a.failure(w, err)
		return
	}
	policy, err := readPolicy(r.Context(), tx, p.OrganizationID)
	if err != nil {
		a.failure(w, err)
		return
	}
	if !expires.After(now) || !policy.Enabled || policy.Revision != revision {
		problem(w, 409, "preview_stale", "Предпросмотр истёк или правила изменились. Сформируйте новый")
		return
	}
	result := retentionReceipt{}
	for _, candidate := range ids {
		if policy.CandidateDays == nil {
			result.Skipped++
			continue
		}
		cutoff := asOf.Add(-time.Duration(*policy.CandidateDays) * 24 * time.Hour)
		erased, e := a.eraseContacts(r.Context(), tx, p, candidate, cutoff, now)
		if e != nil {
			a.failure(w, e)
			return
		}
		if erased {
			result.Erased++
		} else {
			result.Skipped++
		}
	}
	if len(auditIDs) > 0 {
		cookie, e := r.Cookie("hr_session")
		if e != nil {
			problem(w, 401, "unauthorized", "Сессия завершена")
			return
		}
		err = tx.QueryRow(r.Context(), `SELECT prune_audit_events($1,$2)`, id, digest(cookie.Value)).Scan(&result.AuditDeleted)
		if err != nil {
			a.failure(w, err)
			return
		}
	}
	result.Completed, err = databaseNow(r.Context(), tx)
	if err != nil {
		a.failure(w, err)
		return
	}
	body, _ := json.Marshal(result)
	_, err = tx.Exec(r.Context(), `INSERT INTO audit_events(organization_id,actor_user_id,action,object_type,object_id,details) VALUES($1,$2,'retention.apply','retention_preview',$3,$4)`, p.OrganizationID, p.UserID, id, body)
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE retention_previews SET applied_at=$3,result=$4 WHERE organization_id=$1 AND id=$2`, p.OrganizationID, id, result.Completed, body)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, result)
}
func (a *App) eraseContacts(ctx context.Context, tx pgx.Tx, p principal, id string, cutoff, now time.Time) (bool, error) {
	if err := privacy.LockCandidate(ctx, tx, p.OrganizationID, id); err != nil {
		return false, err
	}
	// Invitation -> candidate order avoids deadlocks with start/submit/expiry.
	rows, err := tx.Query(ctx, `SELECT id FROM invitations WHERE organization_id=$1 AND candidate_id=$2 ORDER BY id FOR UPDATE`, p.OrganizationID, id)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var invitation string
		if err = rows.Scan(&invitation); err != nil {
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return false, err
	}
	var candidate string
	err = tx.QueryRow(ctx, `SELECT id FROM candidates WHERE organization_id=$1 AND id=$2 FOR NO KEY UPDATE`, p.OrganizationID, id).Scan(&candidate)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var eligible bool
	err = tx.QueryRow(ctx, `SELECT `+retentionEligible+` FROM candidates c WHERE c.organization_id=$1 AND c.id=$4`, p.OrganizationID, cutoff, now, id).Scan(&eligible)
	if err != nil || !eligible {
		return false, err
	}
	err = privacy.Redact(ctx, tx, p.OrganizationID, id)
	if err == nil {
		err = privacy.Record(ctx, tx, p.OrganizationID, id, "contacts")
	}
	if err == nil {
		err = a.audit(ctx, tx, p, "candidate.contacts.erase", "candidate", id)
	}
	return err == nil, err
}
func (a *App) setRetentionHold(w http.ResponseWriter, r *http.Request, p principal) {
	id := r.PathValue("id")
	if !uuid(id) {
		problem(w, 404, "not_found", "Кандидат не найден")
		return
	}
	var in struct {
		Hold   bool   `json:"hold"`
		Reason string `json:"reason"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if utf8.RuneCountInString(in.Reason) > 2000 || (in.Hold && !validText(in.Reason, 2000)) {
		problem(w, 422, "validation_error", "Укажите причину запрета очистки, до 2000 символов")
		return
	}
	if !in.Hold {
		in.Reason = ""
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
	if err = privacy.LockCandidate(r.Context(), tx, p.OrganizationID, id); err != nil {
		a.failure(w, err)
		return
	}
	var key string
	err = tx.QueryRow(r.Context(), `UPDATE candidates SET retention_hold=$3,retention_hold_reason=$4 WHERE organization_id=$1 AND id=$2 RETURNING id`, p.OrganizationID, id, in.Hold, in.Reason).Scan(&key)
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO audit_events(organization_id,actor_user_id,action,object_type,object_id,details) VALUES($1,$2,'candidate.retention_hold','candidate',$3,jsonb_build_object('hold',$4::boolean))`, p.OrganizationID, p.UserID, id, in.Hold)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	w.WriteHeader(204)
}
func (a *App) retentionHolds(w http.ResponseWriter, r *http.Request, p principal) {
	rows, err := a.Pool.Query(r.Context(), `SELECT id,full_name,retention_hold_reason FROM candidates WHERE organization_id=$1 AND retention_hold ORDER BY full_name,id LIMIT 100`, p.OrganizationID)
	if err != nil {
		a.failure(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]string{}
	for rows.Next() {
		var id, name, reason string
		if err = rows.Scan(&id, &name, &reason); err != nil {
			a.failure(w, err)
			return
		}
		items = append(items, map[string]string{"id": id, "full_name": name, "reason": reason})
	}
	if err = rows.Err(); err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items})
}
