package httpapi

import (
	"context"
	"github.com/PlatvaBestHorse/hr-testing/backend/internal/identity"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

type candidateRecord struct {
	ID            string     `json:"id"`
	Name          string     `json:"full_name"`
	Email         string     `json:"email"`
	Phone         string     `json:"phone"`
	Status        string     `json:"status"`
	CreatedAt     time.Time  `json:"created_at"`
	ErasedAt      *time.Time `json:"erased_at"`
	RetentionHold bool       `json:"retention_hold"`
}

const candidateScope = ` AND ($2<>'assessor' OR EXISTS(SELECT 1 FROM invitations i JOIN vacancy_assessors va ON va.organization_id=i.organization_id AND va.vacancy_id=i.vacancy_id WHERE i.organization_id=c.organization_id AND i.candidate_id=c.id AND va.user_id=$3))`

func (a *App) listCandidates(w http.ResponseWriter, r *http.Request, p principal) {
	page, ok := listPage(w, r)
	if !ok {
		return
	}
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	status := r.URL.Query().Get("status")
	if utf8.RuneCountInString(search) > 200 || (status != "" && status != "active" && status != "archived") {
		problem(w, 400, "invalid_filter", "Проверьте фильтры кандидатов")
		return
	}
	from := ` FROM candidates c WHERE c.organization_id=$1` + candidateScope + ` AND ($4='' OR c.full_name ILIKE '%'||$4||'%' OR coalesce(c.email,'') ILIKE '%'||$4||'%') AND ($5='' OR c.status=$5)`
	var total int
	if err := a.Pool.QueryRow(r.Context(), `SELECT count(*)`+from, p.OrganizationID, p.Role, p.UserID, search, status).Scan(&total); err != nil {
		a.failure(w, err)
		return
	}
	rows, err := a.Pool.Query(r.Context(), `SELECT c.id,c.full_name,coalesce(c.email,''),coalesce(c.phone,''),c.status,c.created_at,c.erased_at,c.retention_hold`+from+` ORDER BY c.created_at DESC,c.id LIMIT 20 OFFSET $6`, p.OrganizationID, p.Role, p.UserID, search, status, (page-1)*20)
	if err != nil {
		a.failure(w, err)
		return
	}
	defer rows.Close()
	items := []candidateRecord{}
	for rows.Next() {
		var c candidateRecord
		if err = rows.Scan(&c.ID, &c.Name, &c.Email, &c.Phone, &c.Status, &c.CreatedAt, &c.ErasedAt, &c.RetentionHold); err != nil {
			a.failure(w, err)
			return
		}
		items = append(items, c)
	}
	if err = rows.Err(); err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items, "total": total, "page": page, "page_size": 20})
}
func (a *App) getCandidate(w http.ResponseWriter, r *http.Request, p principal) {
	id := r.PathValue("id")
	if !uuid(id) {
		problem(w, 404, "not_found", "Кандидат не найден")
		return
	}
	var c candidateRecord
	err := a.Pool.QueryRow(r.Context(), `SELECT c.id,c.full_name,coalesce(c.email,''),coalesce(c.phone,''),c.status,c.created_at,c.erased_at,c.retention_hold FROM candidates c WHERE c.organization_id=$1`+candidateScope+` AND c.id=$4`, p.OrganizationID, p.Role, p.UserID, id).Scan(&c.ID, &c.Name, &c.Email, &c.Phone, &c.Status, &c.CreatedAt, &c.ErasedAt, &c.RetentionHold)
	if err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, c)
}
func (a *App) saveCandidate(w http.ResponseWriter, r *http.Request, p principal, create bool) {
	var in struct {
		Name   string `json:"full_name"`
		Email  string `json:"email"`
		Phone  string `json:"phone"`
		Status string `json:"status"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	in.Phone = strings.TrimSpace(in.Phone)
	if in.Status == "" {
		in.Status = "active"
	}
	if !validText(in.Name, 250) || (in.Email != "" && !identity.ValidEmail(in.Email)) || utf8.RuneCountInString(in.Phone) > 40 || (in.Status != "active" && in.Status != "archived") {
		problem(w, 422, "validation_error", "Проверьте имя, почту, телефон и статус кандидата")
		return
	}
	id := r.PathValue("id")
	if !create && !uuid(id) {
		problem(w, 404, "not_found", "Кандидат не найден")
		return
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	var c candidateRecord
	action := "candidate.create"
	if create {
		err = tx.QueryRow(r.Context(), `INSERT INTO candidates(organization_id,full_name,email,phone,status) VALUES ($1,$2,$3,$4,$5) RETURNING id,full_name,email,phone,status,created_at`, p.OrganizationID, in.Name, in.Email, in.Phone, in.Status).Scan(&c.ID, &c.Name, &c.Email, &c.Phone, &c.Status, &c.CreatedAt)
	} else {
		action = "candidate.update"
		err = tx.QueryRow(r.Context(), `UPDATE candidates SET full_name=$3,email=$4,phone=$5,status=$6,updated_at=now() WHERE organization_id=$1 AND id=$2 AND erased_at IS NULL RETURNING id,full_name,email,phone,status,created_at,erased_at,retention_hold`, p.OrganizationID, id, in.Name, in.Email, in.Phone, in.Status).Scan(&c.ID, &c.Name, &c.Email, &c.Phone, &c.Status, &c.CreatedAt, &c.ErasedAt, &c.RetentionHold)
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	if err = a.audit(r.Context(), tx, p, action, "candidate", c.ID); err != nil {
		a.failure(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		a.failure(w, err)
		return
	}
	status := 200
	if create {
		status = 201
	}
	write(w, status, c)
}
func (a *App) createCandidate(w http.ResponseWriter, r *http.Request, p principal) {
	a.saveCandidate(w, r, p, true)
}
func (a *App) updateCandidate(w http.ResponseWriter, r *http.Request, p principal) {
	a.saveCandidate(w, r, p, false)
}
