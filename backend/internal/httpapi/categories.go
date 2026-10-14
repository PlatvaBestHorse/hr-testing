package httpapi

import (
	"context"
	"net/http"
	"strings"
	"unicode/utf8"
)

type category struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (a *App) listCategories(w http.ResponseWriter, r *http.Request, p principal) {
	rows, err := a.Pool.Query(r.Context(), `SELECT id,name,coalesce(description,'') FROM categories WHERE organization_id=$1 ORDER BY name,id`, p.OrganizationID)
	if err != nil {
		a.failure(w, err)
		return
	}
	defer rows.Close()
	items := []category{}
	for rows.Next() {
		var c category
		if err = rows.Scan(&c.ID, &c.Name, &c.Description); err != nil {
			a.failure(w, err)
			return
		}
		items = append(items, c)
	}
	if err = rows.Err(); err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items})
}

func (a *App) saveCategory(w http.ResponseWriter, r *http.Request, p principal, create bool) {
	var in struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	if !validText(in.Name, 150) || utf8.RuneCountInString(in.Description) > 2000 {
		problem(w, 422, "validation_error", "Название категории обязательно, максимум 150 символов. Описание — до 2000 символов")
		return
	}
	id := r.PathValue("id")
	if !create && !uuid(id) {
		problem(w, 404, "not_found", "Категория не найдена")
		return
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	var c category
	action := "category.create"
	if create {
		err = tx.QueryRow(r.Context(), `INSERT INTO categories(organization_id,name,description) VALUES ($1,$2,$3) RETURNING id,name,description`, p.OrganizationID, in.Name, in.Description).Scan(&c.ID, &c.Name, &c.Description)
	} else {
		action = "category.update"
		err = tx.QueryRow(r.Context(), `UPDATE categories SET name=$3,description=$4 WHERE organization_id=$1 AND id=$2 RETURNING id,name,description`, p.OrganizationID, id, in.Name, in.Description).Scan(&c.ID, &c.Name, &c.Description)
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	if err = a.audit(r.Context(), tx, p, action, "category", c.ID); err != nil {
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
func (a *App) createCategory(w http.ResponseWriter, r *http.Request, p principal) {
	a.saveCategory(w, r, p, true)
}
func (a *App) updateCategory(w http.ResponseWriter, r *http.Request, p principal) {
	a.saveCategory(w, r, p, false)
}
func (a *App) deleteCategory(w http.ResponseWriter, r *http.Request, p principal) {
	id := r.PathValue("id")
	if !uuid(id) {
		problem(w, 404, "not_found", "Категория не найдена")
		return
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	result, err := tx.Exec(r.Context(), `DELETE FROM categories WHERE organization_id=$1 AND id=$2`, p.OrganizationID, id)
	if err != nil {
		a.failure(w, err)
		return
	}
	if result.RowsAffected() == 0 {
		problem(w, 404, "not_found", "Категория не найдена")
		return
	}
	if err = a.audit(r.Context(), tx, p, "category.delete", "category", id); err != nil {
		a.failure(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		a.failure(w, err)
		return
	}
	w.WriteHeader(204)
}
