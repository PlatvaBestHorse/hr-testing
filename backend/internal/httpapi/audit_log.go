package httpapi

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type auditRecord struct {
	ID         string         `json:"id"`
	ActorID    string         `json:"actor_user_id"`
	ActorName  string         `json:"actor_name"`
	Action     string         `json:"action"`
	ObjectType string         `json:"object_type"`
	ObjectID   string         `json:"object_id"`
	Occurred   time.Time      `json:"occurred_at"`
	Details    map[string]any `json:"details"`
}

var auditCode = regexp.MustCompile(`^[a-z][a-z0-9_.]{0,119}$`)

func safeAuditDetails(raw []byte) map[string]any {
	var source map[string]any
	json.Unmarshal(raw, &source)
	out := map[string]any{}
	for _, key := range []string{"row_count", "policy_revision", "candidates_erased", "candidates_skipped", "audit_events_deleted"} {
		if n, ok := source[key].(float64); ok && n >= 0 && n <= 1e9 && n == float64(int64(n)) {
			out[key] = int64(n)
		}
	}
	if value, ok := source["format"].(string); ok && (value == "csv" || value == "xlsx") {
		out["format"] = value
	}
	for _, key := range []string{"date_from", "date_to"} {
		if value, ok := source[key].(string); ok {
			if _, err := time.Parse("2006-01-02", value); err == nil {
				out[key] = value
			}
		}
	}
	if value, ok := source["hold"].(bool); ok {
		out["hold"] = value
	}
	return out
}
func (a *App) listAudit(w http.ResponseWriter, r *http.Request, p principal) {
	page, ok := listPage(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	action, kind, actor := q.Get("action"), q.Get("object_type"), strings.ToLower(q.Get("actor_id"))
	if (action != "" && !auditCode.MatchString(action)) || (kind != "" && !auditCode.MatchString(kind)) || (actor != "" && actor != "system" && !uuid(actor)) {
		problem(w, 400, "invalid_filter", "Проверьте фильтры журнала")
		return
	}
	var from, to *time.Time
	for _, date := range []struct {
		key    string
		target **time.Time
		end    bool
	}{{"date_from", &from, false}, {"date_to", &to, true}} {
		value := q.Get(date.key)
		if value == "" {
			continue
		}
		t, err := time.ParseInLocation("2006-01-02", value, moscow)
		if err != nil || t.Year() < 2000 || t.Year() > 2100 {
			problem(w, 400, "invalid_filter", "Проверьте даты журнала")
			return
		}
		if date.end {
			t = t.AddDate(0, 0, 1)
		}
		*date.target = &t
	}
	if from != nil && to != nil && !from.Before(*to) {
		problem(w, 400, "invalid_filter", "Дата окончания раньше начала")
		return
	}
	args := []any{p.OrganizationID, action, kind, actor, from, to}
	clause := ` FROM audit_events e LEFT JOIN users u ON u.id=e.actor_user_id
 WHERE e.organization_id=$1 AND ($2='' OR e.action=$2) AND ($3='' OR e.object_type=$3)
 AND ($4='' OR ($4='system' AND e.actor_user_id IS NULL) OR e.actor_user_id::text=$4)
 AND ($5::timestamptz IS NULL OR e.occurred_at>=$5) AND ($6::timestamptz IS NULL OR e.occurred_at<$6)`
	var total int
	if err := a.Pool.QueryRow(r.Context(), "SELECT count(*)"+clause, args...).Scan(&total); err != nil {
		a.failure(w, err)
		return
	}
	args = append(args, (page-1)*50)
	rows, err := a.Pool.Query(r.Context(), `SELECT e.id::text,coalesce(e.actor_user_id::text,''),coalesce(u.display_name,'Система'),e.action,e.object_type,coalesce(e.object_id::text,''),e.occurred_at,e.details`+clause+` ORDER BY e.occurred_at DESC,e.id DESC LIMIT 50 OFFSET $7`, args...)
	if err != nil {
		a.failure(w, err)
		return
	}
	defer rows.Close()
	items := []auditRecord{}
	for rows.Next() {
		var item auditRecord
		var raw []byte
		if err = rows.Scan(&item.ID, &item.ActorID, &item.ActorName, &item.Action, &item.ObjectType, &item.ObjectID, &item.Occurred, &raw); err != nil {
			a.failure(w, err)
			return
		}
		item.Details = safeAuditDetails(raw)
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items, "total": total, "page": page, "page_size": 50})
}
func (a *App) auditOptions(w http.ResponseWriter, r *http.Request, p principal) {
	var actions, kinds []string
	err := a.Pool.QueryRow(r.Context(), `SELECT coalesce(array_agg(DISTINCT action ORDER BY action),'{}'),coalesce(array_agg(DISTINCT object_type ORDER BY object_type),'{}') FROM audit_events WHERE organization_id=$1`, p.OrganizationID).Scan(&actions, &kinds)
	if err != nil {
		a.failure(w, err)
		return
	}
	rows, err := a.Pool.Query(r.Context(), `SELECT DISTINCT u.id,u.display_name FROM audit_events e JOIN users u ON u.id=e.actor_user_id WHERE e.organization_id=$1 ORDER BY u.display_name,u.id`, p.OrganizationID)
	if err != nil {
		a.failure(w, err)
		return
	}
	defer rows.Close()
	actors := []map[string]string{}
	for rows.Next() {
		var id, name string
		if err = rows.Scan(&id, &name); err != nil {
			a.failure(w, err)
			return
		}
		actors = append(actors, map[string]string{"id": id, "name": name})
	}
	if err = rows.Err(); err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, map[string]any{"actions": actions, "object_types": kinds, "actors": actors})
}
