package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"github.com/PlatvaBestHorse/hr-testing/backend/internal/privacy"
	"github.com/jackc/pgx/v5"
	"net/http"
	"time"
)

type invitationRecord struct {
	ID            string    `json:"id"`
	CandidateID   string    `json:"candidate_id"`
	CandidateName string    `json:"candidate_name"`
	TestVersionID string    `json:"test_version_id"`
	TestTitle     string    `json:"test_title"`
	TestVersionNo int       `json:"test_version_no"`
	VacancyID     string    `json:"vacancy_id"`
	VacancyTitle  string    `json:"vacancy_title"`
	Status        string    `json:"status"`
	MaxAttempts   int       `json:"max_attempts"`
	UsedAttempts  int       `json:"used_attempts"`
	ExpiresAt     time.Time `json:"expires_at"`
	IssuedAt      time.Time `json:"issued_at"`
}

func (a *App) listInvitations(w http.ResponseWriter, r *http.Request, p principal) {
	page, ok := listPage(w, r)
	if !ok {
		return
	}
	candidateID := r.URL.Query().Get("candidate_id")
	status := r.URL.Query().Get("status")
	if (candidateID != "" && !uuid(candidateID)) || (status != "" && status != "issued" && status != "revoked" && status != "expired" && status != "completed") {
		problem(w, 400, "invalid_filter", "Проверьте фильтры приглашений")
		return
	}
	from := ` FROM invitations i JOIN candidates c ON c.organization_id=i.organization_id AND c.id=i.candidate_id JOIN test_versions v ON v.organization_id=i.organization_id AND v.id=i.test_version_id LEFT JOIN vacancies vac ON vac.organization_id=i.organization_id AND vac.id=i.vacancy_id`
	effective := `CASE WHEN i.status='issued' AND i.expires_at<=now() THEN 'expired' ELSE i.status END`
	where := ` WHERE i.organization_id=$1 AND ($2='' OR i.candidate_id::text=$2) AND ($3='' OR ` + effective + `=$3)`
	var total int
	if err := a.Pool.QueryRow(r.Context(), `SELECT count(*)`+from+where, p.OrganizationID, candidateID, status).Scan(&total); err != nil {
		a.failure(w, err)
		return
	}
	rows, err := a.Pool.Query(r.Context(), `SELECT i.id,i.candidate_id,c.full_name,i.test_version_id,v.title,v.version_no,coalesce(i.vacancy_id::text,''),coalesce(vac.title,''),`+effective+`,i.max_attempts,(SELECT count(*) FROM attempts WHERE organization_id=i.organization_id AND invitation_id=i.id),i.expires_at,i.issued_at`+from+where+` ORDER BY i.issued_at DESC,i.id LIMIT 20 OFFSET $4`, p.OrganizationID, candidateID, status, (page-1)*20)
	if err != nil {
		a.failure(w, err)
		return
	}
	defer rows.Close()
	items := []invitationRecord{}
	for rows.Next() {
		var i invitationRecord
		if err = rows.Scan(&i.ID, &i.CandidateID, &i.CandidateName, &i.TestVersionID, &i.TestTitle, &i.TestVersionNo, &i.VacancyID, &i.VacancyTitle, &i.Status, &i.MaxAttempts, &i.UsedAttempts, &i.ExpiresAt, &i.IssuedAt); err != nil {
			a.failure(w, err)
			return
		}
		items = append(items, i)
	}
	if err = rows.Err(); err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items, "total": total, "page": page, "page_size": 20})
}
func (a *App) issueInvitation(w http.ResponseWriter, r *http.Request, p principal) {
	var in struct {
		CandidateID   string    `json:"candidate_id"`
		TestVersionID string    `json:"test_version_id"`
		VacancyID     string    `json:"vacancy_id"`
		MaxAttempts   int       `json:"max_attempts"`
		ExpiresAt     time.Time `json:"expires_at"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.MaxAttempts == 0 {
		in.MaxAttempts = 1
	}
	if !uuid(in.CandidateID) || !uuid(in.TestVersionID) || (in.VacancyID != "" && !uuid(in.VacancyID)) || in.MaxAttempts < 1 || in.MaxAttempts > 20 || in.ExpiresAt.IsZero() {
		problem(w, 422, "validation_error", "Выберите кандидата, опубликованный тест, срок и число попыток от 1 до 20")
		return
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	if err = privacy.LockCandidate(r.Context(), tx, p.OrganizationID, in.CandidateID); err != nil {
		a.failure(w, err)
		return
	}
	var candidateName, status string
	err = tx.QueryRow(r.Context(), `SELECT full_name,status FROM candidates WHERE organization_id=$1 AND id=$2 FOR SHARE`, p.OrganizationID, in.CandidateID).Scan(&candidateName, &status)
	if err != nil {
		a.failure(w, err)
		return
	}
	if status != "active" {
		problem(w, 422, "candidate_inactive", "Кандидат находится в архиве")
		return
	}
	var tstatus, vstatus, title string
	var number int
	err = tx.QueryRow(r.Context(), `SELECT t.status,v.status,v.title,v.version_no FROM test_versions v JOIN tests t ON t.organization_id=v.organization_id AND t.id=v.test_id WHERE v.organization_id=$1 AND v.id=$2 FOR SHARE OF t,v`, p.OrganizationID, in.TestVersionID).Scan(&tstatus, &vstatus, &title, &number)
	if err != nil {
		a.failure(w, err)
		return
	}
	if tstatus != "active" || vstatus != "published" {
		problem(w, 422, "test_unavailable", "Приглашение выдаётся на опубликованный активный тест")
		return
	}
	var vacancyValue any
	vacancyTitle := ""
	if in.VacancyID != "" {
		vacancyValue = in.VacancyID
		err = tx.QueryRow(r.Context(), `SELECT title,status FROM vacancies WHERE organization_id=$1 AND id=$2 FOR SHARE`, p.OrganizationID, in.VacancyID).Scan(&vacancyTitle, &status)
		if err != nil {
			a.failure(w, err)
			return
		}
		if status != "open" {
			problem(w, 422, "vacancy_closed", "Выберите открытую вакансию")
			return
		}
	}
	var validExpiry bool
	if err = tx.QueryRow(r.Context(), `SELECT $1::timestamptz>clock_timestamp() AND $1::timestamptz<=clock_timestamp()+interval '365 days'`, in.ExpiresAt).Scan(&validExpiry); err != nil {
		a.failure(w, err)
		return
	}
	if !validExpiry {
		problem(w, 422, "invalid_expiry", "Ссылка должна действовать в будущем, не более 365 дней")
		return
	}
	var bytes [32]byte
	if _, err = rand.Read(bytes[:]); err != nil {
		problem(w, 500, "internal_error", "Не удалось создать приглашение")
		return
	}
	token := base64.RawURLEncoding.EncodeToString(bytes[:])
	i := invitationRecord{CandidateID: in.CandidateID, CandidateName: candidateName, TestVersionID: in.TestVersionID, TestTitle: title, TestVersionNo: number, VacancyID: in.VacancyID, VacancyTitle: vacancyTitle, Status: "issued", MaxAttempts: in.MaxAttempts, ExpiresAt: in.ExpiresAt}
	err = tx.QueryRow(r.Context(), `INSERT INTO invitations(organization_id,candidate_id,test_version_id,vacancy_id,token_hash,max_attempts,expires_at,issued_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id,issued_at`, p.OrganizationID, in.CandidateID, in.TestVersionID, vacancyValue, digest(token), in.MaxAttempts, in.ExpiresAt, p.UserID).Scan(&i.ID, &i.IssuedAt)
	if err != nil {
		a.failure(w, err)
		return
	}
	if err = a.audit(r.Context(), tx, p, "invitation.issue", "invitation", i.ID); err != nil {
		a.failure(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		a.failure(w, err)
		return
	}
	write(w, 201, map[string]any{"invitation": i, "path": "/i/" + token})
}
func (a *App) revokeInvitation(w http.ResponseWriter, r *http.Request, p principal) {
	id := r.PathValue("id")
	if !uuid(id) {
		problem(w, 404, "not_found", "Приглашение не найдено")
		return
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	var status string
	if err = tx.QueryRow(r.Context(), `SELECT status FROM invitations WHERE organization_id=$1 AND id=$2 FOR UPDATE`, p.OrganizationID, id).Scan(&status); err != nil {
		a.failure(w, err)
		return
	}
	if status != "revoked" {
		if _, err = tx.Exec(r.Context(), `UPDATE invitations SET status='revoked' WHERE organization_id=$1 AND id=$2`, p.OrganizationID, id); err != nil {
			a.failure(w, err)
			return
		}
		if err = a.audit(r.Context(), tx, p, "invitation.revoke", "invitation", id); err != nil {
			a.failure(w, err)
			return
		}
		if _, err = tx.Exec(r.Context(), `UPDATE attempts SET status='cancelled',finished_at=greatest(started_at,clock_timestamp()) WHERE organization_id=$1 AND invitation_id=$2 AND status='in_progress'`, p.OrganizationID, id); err != nil {
			a.failure(w, err)
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		a.failure(w, err)
		return
	}
	w.WriteHeader(204)
}
func (a *App) publicInvitation(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	token := r.PathValue("token")
	if len(token) != 43 {
		problem(w, 404, "invite_unavailable", "Приглашение недоступно. Уточните ссылку у HR")
		return
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != token {
		problem(w, 404, "invite_unavailable", "Приглашение недоступно. Уточните ссылку у HR")
		return
	}
	// Public DTO is intentionally independent of HR test/question/candidate DTOs.
	var title, description, orgName, orgID string
	var duration, count, max, used int
	var expiry time.Time
	var active bool
	err = a.Pool.QueryRow(r.Context(), `SELECT v.title,coalesce(v.description,''),o.name,o.id,v.duration_seconds,
 (SELECT count(*) FROM test_items WHERE organization_id=i.organization_id AND test_version_id=v.id),i.max_attempts,
 (SELECT count(*) FROM attempts WHERE organization_id=i.organization_id AND invitation_id=i.id),i.expires_at,
 EXISTS(SELECT 1 FROM attempts WHERE organization_id=i.organization_id AND invitation_id=i.id AND status='in_progress' AND deadline_at>now())
 FROM invitations i JOIN test_versions v ON v.organization_id=i.organization_id AND v.id=i.test_version_id
 JOIN organizations o ON o.id=i.organization_id JOIN candidates c ON c.organization_id=i.organization_id AND c.id=i.candidate_id
 WHERE i.token_hash=$1 AND i.status='issued' AND i.expires_at>now() AND v.status='published' AND c.status='active' AND o.status='active'`, digest(token)).Scan(&title, &description, &orgName, &orgID, &duration, &count, &max, &used, &expiry, &active)
	if err == pgx.ErrNoRows || (err == nil && used >= max && !active) {
		problem(w, 404, "invite_unavailable", "Приглашение недоступно. Уточните ссылку у HR")
		return
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	remaining := max - used
	if remaining < 0 {
		remaining = 0
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	document, err := readConsent(r.Context(), tx, orgID)
	if err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, map[string]any{"consent_document": document, "title": title, "description": description, "organization_name": orgName, "duration_seconds": duration, "question_count": count, "max_attempts": max, "attempts_remaining": remaining, "can_resume": active, "expires_at": expiry})
}
