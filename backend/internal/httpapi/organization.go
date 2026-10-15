package httpapi

import (
	"context"
	"net/http"
	"strings"
)

type organizationSettings struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	Status   string `json:"status"`
	Days     int    `json:"invitation_default_days"`
	Revision int    `json:"revision"`
}

func (a *App) getOrganization(w http.ResponseWriter, r *http.Request, p principal) {
	var out organizationSettings
	err := a.Pool.QueryRow(r.Context(), `SELECT id,name,slug,status,coalesce((settings->>'invitation_default_days')::integer,7),settings_revision FROM organizations WHERE id=$1`, p.OrganizationID).Scan(&out.ID, &out.Name, &out.Slug, &out.Status, &out.Days, &out.Revision)
	if err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, out)
}
func (a *App) saveOrganization(w http.ResponseWriter, r *http.Request, p principal) {
	var in struct {
		Name         string `json:"name"`
		Status       string `json:"status"`
		Days         int    `json:"invitation_default_days"`
		Revision     int    `json:"base_revision"`
		Confirmation string `json:"confirmation"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if !validText(in.Name, 250) || in.Days < 1 || in.Days > 365 || (in.Status != "active" && in.Status != "suspended" && in.Status != "archived") {
		problem(w, 422, "validation_error", "Проверьте название, статус и срок от 1 до 365 дней")
		return
	}
	if in.Status != "active" && in.Confirmation != "ЗАКРЫТЬ ДОСТУП" {
		problem(w, 422, "confirmation_required", "Для закрытия доступа введите ЗАКРЫТЬ ДОСТУП")
		return
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	var revision int
	if err = tx.QueryRow(r.Context(), "SELECT settings_revision FROM organizations WHERE id=$1 FOR NO KEY UPDATE", p.OrganizationID).Scan(&revision); err != nil {
		a.failure(w, err)
		return
	}
	if !a.confirmRetentionAdmin(w, r, p, tx) {
		return
	}
	if revision != in.Revision {
		problem(w, 409, "version_conflict", "Настройки уже изменены. Обновите страницу")
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE organizations SET name=$2,status=$3,settings=jsonb_set(settings,'{invitation_default_days}',to_jsonb($4::integer),true),settings_revision=settings_revision+1 WHERE id=$1`, p.OrganizationID, in.Name, in.Status, in.Days)
	if err == nil && in.Status != "active" {
		_, err = tx.Exec(r.Context(), "DELETE FROM auth_sessions WHERE organization_id=$1", p.OrganizationID)
	}
	if err == nil {
		err = a.audit(r.Context(), tx, p, "organization.update", "organization", p.OrganizationID)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	if in.Status != "active" {
		a.clearSession(w)
	}
	write(w, 200, map[string]any{"revision": revision + 1, "status": in.Status})
}
