package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/PlatvaBestHorse/hr-testing/backend/internal/identity"
)

type member struct {
	ID     string `json:"id"`
	Email  string `json:"email"`
	Name   string `json:"display_name"`
	Role   string `json:"role"`
	Status string `json:"status"`
}

func (a *App) listTeam(w http.ResponseWriter, r *http.Request, p principal) {
	rows, err := a.Pool.Query(r.Context(), `SELECT u.id,u.email,u.display_name,m.role,m.status FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.organization_id=$1 ORDER BY u.display_name,u.id`, p.OrganizationID)
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

func (a *App) createMember(w http.ResponseWriter, r *http.Request, p principal) {
	var in struct {
		Email    string `json:"email"`
		Name     string `json:"display_name"`
		Role     string `json:"role"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	in.Name = strings.TrimSpace(in.Name)
	if !identity.ValidEmail(in.Email) || !validText(in.Name, 250) || !roleAllowed(in.Role) {
		problem(w, 422, "validation_error", "Проверьте имя, почту и роль сотрудника")
		return
	}
	hash, err := identity.HashPassword(in.Password)
	if err != nil {
		problem(w, 422, "invalid_password", "Пароль должен содержать минимум 12 символов и не более 1024 байт")
		return
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	var locked string
	if err = tx.QueryRow(r.Context(), `SELECT id FROM organizations WHERE id=$1 FOR UPDATE`, p.OrganizationID).Scan(&locked); err != nil {
		a.failure(w, err)
		return
	}
	var id string
	err = tx.QueryRow(r.Context(), `INSERT INTO users(email,display_name,password_hash) VALUES ($1,$2,$3) RETURNING id`, in.Email, in.Name, hash).Scan(&id)
	if err != nil {
		a.failure(w, err)
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO memberships(organization_id,user_id,role) VALUES ($1,$2,$3)`, p.OrganizationID, id, in.Role); err != nil {
		a.failure(w, err)
		return
	}
	if err = a.audit(r.Context(), tx, p, "team.member.create", "user", id); err != nil {
		a.failure(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		a.failure(w, err)
		return
	}
	write(w, 201, member{ID: id, Email: in.Email, Name: in.Name, Role: in.Role, Status: "active"})
}

func (a *App) updateMember(w http.ResponseWriter, r *http.Request, p principal) {
	id := r.PathValue("id")
	if !uuid(id) {
		problem(w, 404, "not_found", "Сотрудник не найден")
		return
	}
	var in struct {
		Role   string `json:"role"`
		Status string `json:"status"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !roleAllowed(in.Role) || (in.Status != "active" && in.Status != "disabled") {
		problem(w, 422, "validation_error", "Проверьте роль и статус сотрудника")
		return
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	var locked string
	if err = tx.QueryRow(r.Context(), `SELECT id FROM organizations WHERE id=$1 FOR UPDATE`, p.OrganizationID).Scan(&locked); err != nil {
		a.failure(w, err)
		return
	}
	var m member
	err = tx.QueryRow(r.Context(), `SELECT u.id,u.email,u.display_name,m.role,m.status FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.organization_id=$1 AND m.user_id=$2 FOR UPDATE OF m`, p.OrganizationID, id).Scan(&m.ID, &m.Email, &m.Name, &m.Role, &m.Status)
	if err != nil {
		a.failure(w, err)
		return
	}
	if m.Role == "org_admin" && m.Status == "active" && (in.Role != "org_admin" || in.Status != "active") {
		var count int
		err = tx.QueryRow(r.Context(), `SELECT count(*) FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.organization_id=$1 AND m.role='org_admin' AND m.status='active' AND u.status='active'`, p.OrganizationID).Scan(&count)
		if err != nil {
			a.failure(w, err)
			return
		}
		if count <= 1 {
			problem(w, 409, "last_admin", "Нельзя отключить или понизить роль последнего администратора")
			return
		}
	}
	if _, err = tx.Exec(r.Context(), `UPDATE memberships SET role=$3,status=$4 WHERE organization_id=$1 AND user_id=$2`, p.OrganizationID, id, in.Role, in.Status); err != nil {
		a.failure(w, err)
		return
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM auth_sessions WHERE organization_id=$1 AND user_id=$2`, p.OrganizationID, id); err != nil {
		a.failure(w, err)
		return
	}
	if err = a.audit(r.Context(), tx, p, "team.member.update", "user", id); err != nil {
		a.failure(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		a.failure(w, err)
		return
	}
	m.Role = in.Role
	m.Status = in.Status
	write(w, 200, m)
}
