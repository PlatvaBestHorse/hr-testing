package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type resultComment struct {
	ID        string    `json:"id"`
	Author    string    `json:"author_user_id"`
	Name      string    `json:"author_name"`
	Body      string    `json:"body"`
	Created   time.Time `json:"created_at"`
	Updated   time.Time `json:"updated_at"`
	Editable  bool      `json:"can_edit"`
	Deletable bool      `json:"can_delete"`
}

func commentAccess(ctx context.Context, tx pgx.Tx, p principal, id string, lock bool) error {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE OF a"
	}
	var status string
	err := tx.QueryRow(ctx, "SELECT a.status"+resultsFrom+" AND a.id=$4"+suffix, p.OrganizationID, p.Role, p.UserID, id).Scan(&status)
	if err != nil {
		return err
	}
	if status != "submitted" && status != "expired" {
		return errReportPending
	}
	return nil
}
func (a *App) listComments(w http.ResponseWriter, r *http.Request, p principal) {
	id := r.PathValue("id")
	if !uuid(id) {
		problem(w, 404, "not_found", "Результат не найден")
		return
	}
	page, ok := listPage(w, r)
	if !ok {
		return
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	if err = commentAccess(r.Context(), tx, p, id, false); err != nil {
		a.reportFailure(w, err)
		return
	}
	var total int
	if err = tx.QueryRow(r.Context(), "SELECT count(*) FROM result_comments WHERE organization_id=$1 AND attempt_id=$2", p.OrganizationID, id).Scan(&total); err != nil {
		a.failure(w, err)
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT c.id,c.author_user_id,u.display_name,c.body,c.created_at,c.updated_at FROM result_comments c JOIN users u ON u.id=c.author_user_id WHERE c.organization_id=$1 AND c.attempt_id=$2 ORDER BY c.created_at,c.id LIMIT 20 OFFSET $3`, p.OrganizationID, id, (page-1)*20)
	if err != nil {
		a.failure(w, err)
		return
	}
	items := []resultComment{}
	for rows.Next() {
		var c resultComment
		if err = rows.Scan(&c.ID, &c.Author, &c.Name, &c.Body, &c.Created, &c.Updated); err != nil {
			rows.Close()
			a.failure(w, err)
			return
		}
		c.Editable = c.Author == p.UserID
		c.Deletable = c.Editable || p.Role == "org_admin"
		items = append(items, c)
	}
	err = rows.Err()
	rows.Close()
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items, "total": total, "page": page, "page_size": 20})
}
func (a *App) saveComment(w http.ResponseWriter, r *http.Request, p principal) {
	id, cid := r.PathValue("id"), r.PathValue("comment")
	create := r.Method == "POST"
	remove := r.Method == "DELETE"
	if !uuid(id) || (!create && !uuid(cid)) {
		problem(w, 404, "not_found", "Комментарий не найден")
		return
	}
	var in struct {
		Body    string    `json:"body"`
		Updated time.Time `json:"base_updated_at"`
	}
	if !remove {
		if !readJSON(w, r, &in) {
			return
		}
		in.Body = strings.TrimSpace(in.Body)
		if !validText(in.Body, 5000) {
			problem(w, 422, "validation_error", "Комментарий от 1 до 5000 символов")
			return
		}
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	if err = commentAccess(r.Context(), tx, p, id, true); err != nil {
		a.reportFailure(w, err)
		return
	}
	action := "result.comment.create"
	if create {
		var count int
		if err = tx.QueryRow(r.Context(), "SELECT count(*) FROM result_comments WHERE organization_id=$1 AND attempt_id=$2", p.OrganizationID, id).Scan(&count); err != nil {
			a.failure(w, err)
			return
		}
		if count >= 100 {
			problem(w, 422, "comment_limit", "Максимум 100 комментариев к результату")
			return
		}
		err = tx.QueryRow(r.Context(), "INSERT INTO result_comments(organization_id,attempt_id,author_user_id,body) VALUES($1,$2,$3,$4) RETURNING id", p.OrganizationID, id, p.UserID, in.Body).Scan(&cid)
	} else {
		var author string
		var updated time.Time
		err = tx.QueryRow(r.Context(), "SELECT author_user_id,updated_at FROM result_comments WHERE organization_id=$1 AND attempt_id=$2 AND id=$3 FOR UPDATE", p.OrganizationID, id, cid).Scan(&author, &updated)
		if err != nil {
			a.failure(w, err)
			return
		}
		if author != p.UserID && (!remove || p.Role != "org_admin") {
			problem(w, 403, "forbidden", "Можно редактировать только свой комментарий")
			return
		}
		if remove {
			action = "result.comment.delete"
			_, err = tx.Exec(r.Context(), "DELETE FROM result_comments WHERE organization_id=$1 AND id=$2", p.OrganizationID, cid)
		} else {
			if !updated.Equal(in.Updated) {
				problem(w, 409, "version_conflict", "Комментарий изменён. Обновите список")
				return
			}
			action = "result.comment.update"
			_, err = tx.Exec(r.Context(), "UPDATE result_comments SET body=$3,updated_at=clock_timestamp() WHERE organization_id=$1 AND id=$2", p.OrganizationID, cid, in.Body)
		}
	}
	if err == nil {
		err = a.audit(r.Context(), tx, p, action, "result_comment", cid)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	if remove {
		w.WriteHeader(204)
	} else {
		status := 200
		if create {
			status = 201
		}
		write(w, status, map[string]string{"id": cid})
	}
}
