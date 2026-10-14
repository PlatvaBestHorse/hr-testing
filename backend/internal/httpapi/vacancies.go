package httpapi

import (
	"context"
	"net/http"
	"strings"
	"unicode/utf8"
)

type vacancyRecord struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Department string   `json:"department"`
	Status     string   `json:"status"`
	Assessors  []string `json:"assessor_ids"`
}

const vacancyScope = ` AND ($2<>'assessor' OR EXISTS(SELECT 1 FROM vacancy_assessors va WHERE va.organization_id=v.organization_id AND va.vacancy_id=v.id AND va.user_id=$3))`
const vacancyColumns = `v.id,v.title,coalesce(v.department,''),v.status,ARRAY(SELECT user_id::text FROM vacancy_assessors va WHERE va.organization_id=v.organization_id AND va.vacancy_id=v.id ORDER BY user_id)`

func (a *App) listVacancies(w http.ResponseWriter, r *http.Request, p principal) {
	page, ok := listPage(w, r)
	if !ok {
		return
	}
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	status := r.URL.Query().Get("status")
	if utf8.RuneCountInString(search) > 200 || (status != "" && status != "open" && status != "closed" && status != "archived") {
		problem(w, 400, "invalid_filter", "Проверьте фильтры вакансий")
		return
	}
	from := ` FROM vacancies v WHERE v.organization_id=$1` + vacancyScope + ` AND ($4='' OR v.title ILIKE '%'||$4||'%') AND ($5='' OR v.status=$5)`
	var total int
	if err := a.Pool.QueryRow(r.Context(), `SELECT count(*)`+from, p.OrganizationID, p.Role, p.UserID, search, status).Scan(&total); err != nil {
		a.failure(w, err)
		return
	}
	rows, err := a.Pool.Query(r.Context(), `SELECT `+vacancyColumns+from+` ORDER BY v.created_at DESC,v.id LIMIT 20 OFFSET $6`, p.OrganizationID, p.Role, p.UserID, search, status, (page-1)*20)
	if err != nil {
		a.failure(w, err)
		return
	}
	defer rows.Close()
	items := []vacancyRecord{}
	for rows.Next() {
		var v vacancyRecord
		if err = rows.Scan(&v.ID, &v.Title, &v.Department, &v.Status, &v.Assessors); err != nil {
			a.failure(w, err)
			return
		}
		items = append(items, v)
	}
	if err = rows.Err(); err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items, "total": total, "page": page, "page_size": 20})
}
func (a *App) getVacancy(w http.ResponseWriter, r *http.Request, p principal) {
	id := r.PathValue("id")
	if !uuid(id) {
		problem(w, 404, "not_found", "Вакансия не найдена")
		return
	}
	var v vacancyRecord
	err := a.Pool.QueryRow(r.Context(), `SELECT `+vacancyColumns+` FROM vacancies v WHERE v.organization_id=$1`+vacancyScope+` AND v.id=$4`, p.OrganizationID, p.Role, p.UserID, id).Scan(&v.ID, &v.Title, &v.Department, &v.Status, &v.Assessors)
	if err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, v)
}
func (a *App) saveVacancy(w http.ResponseWriter, r *http.Request, p principal, create bool) {
	var in struct {
		Title      string   `json:"title"`
		Department string   `json:"department"`
		Status     string   `json:"status"`
		Assessors  []string `json:"assessor_ids"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	in.Department = strings.TrimSpace(in.Department)
	if in.Status == "" {
		in.Status = "open"
	}
	if in.Assessors == nil {
		in.Assessors = []string{}
	}
	if !validText(in.Title, 250) || utf8.RuneCountInString(in.Department) > 200 || (in.Status != "open" && in.Status != "closed" && in.Status != "archived") || len(in.Assessors) > 100 {
		problem(w, 422, "validation_error", "Проверьте название, подразделение, статус и назначенных оценщиков")
		return
	}
	seen := map[string]bool{}
	for i, id := range in.Assessors {
		if !uuid(id) || seen[strings.ToLower(id)] {
			problem(w, 422, "invalid_assessor", "Выберите разных оценщиков своей организации")
			return
		}
		in.Assessors[i] = strings.ToLower(id)
		seen[in.Assessors[i]] = true
	}
	id := r.PathValue("id")
	if !create && !uuid(id) {
		problem(w, 404, "not_found", "Вакансия не найдена")
		return
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	var v vacancyRecord
	action := "vacancy.create"
	if create {
		err = tx.QueryRow(r.Context(), `INSERT INTO vacancies(organization_id,title,department,status) VALUES ($1,$2,$3,$4) RETURNING id,title,department,status`, p.OrganizationID, in.Title, in.Department, in.Status).Scan(&v.ID, &v.Title, &v.Department, &v.Status)
	} else {
		action = "vacancy.update"
		err = tx.QueryRow(r.Context(), `UPDATE vacancies SET title=$3,department=$4,status=$5 WHERE organization_id=$1 AND id=$2 RETURNING id,title,department,status`, p.OrganizationID, id, in.Title, in.Department, in.Status).Scan(&v.ID, &v.Title, &v.Department, &v.Status)
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	var count int
	err = tx.QueryRow(r.Context(), `SELECT count(*) FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.organization_id=$1 AND m.user_id=ANY($2::uuid[]) AND m.role='assessor' AND m.status='active' AND u.status='active'`, p.OrganizationID, in.Assessors).Scan(&count)
	if err != nil {
		a.failure(w, err)
		return
	}
	if count != len(in.Assessors) {
		problem(w, 422, "invalid_assessor", "Назначьте активных оценщиков своей организации")
		return
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM vacancy_assessors WHERE organization_id=$1 AND vacancy_id=$2`, p.OrganizationID, v.ID); err != nil {
		a.failure(w, err)
		return
	}
	for _, userID := range in.Assessors {
		if _, err = tx.Exec(r.Context(), `INSERT INTO vacancy_assessors(organization_id,vacancy_id,user_id) VALUES ($1,$2,$3)`, p.OrganizationID, v.ID, userID); err != nil {
			a.failure(w, err)
			return
		}
	}
	v.Assessors = in.Assessors
	if err = a.audit(r.Context(), tx, p, action, "vacancy", v.ID); err != nil {
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
	write(w, status, v)
}
func (a *App) createVacancy(w http.ResponseWriter, r *http.Request, p principal) {
	a.saveVacancy(w, r, p, true)
}
func (a *App) updateVacancy(w http.ResponseWriter, r *http.Request, p principal) {
	a.saveVacancy(w, r, p, false)
}
func (a *App) listAssessors(w http.ResponseWriter, r *http.Request, p principal) {
	rows, err := a.Pool.Query(r.Context(), `SELECT u.id,u.email,u.display_name,m.role,m.status FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.organization_id=$1 AND m.role='assessor' AND m.status='active' AND u.status='active' ORDER BY u.display_name,u.id`, p.OrganizationID)
	if err != nil {
		a.failure(w, err)
		return
	}
	defer rows.Close()
	items := []member{}
	for rows.Next() {
		var m member
		if err = rows.Scan(&m.ID, &m.Email, &m.Name, &m.Role, &m.Status); err != nil {
			a.failure(w, err)
			return
		}
		items = append(items, m)
	}
	if err = rows.Err(); err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items})
}
