package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type resultRecord struct {
	ID            string     `json:"id"`
	CandidateName string     `json:"candidate_name"`
	TestTitle     string     `json:"test_title"`
	VacancyTitle  string     `json:"vacancy_title"`
	Status        string     `json:"status"`
	ScorePoints   *string    `json:"score_points"`
	MaxPoints     *string    `json:"max_points"`
	Percent       *string    `json:"score_percent"`
	Passed        *bool      `json:"passed"`
	Started       time.Time  `json:"started_at"`
	Finished      *time.Time `json:"finished_at"`
	TestVersion   int        `json:"test_version_no"`
	AttemptNo     int        `json:"attempt_no"`
}

var moscow = time.FixedZone("MSK", 3*3600)

const resultsFrom = ` FROM attempts a JOIN invitations i ON i.organization_id=a.organization_id AND i.id=a.invitation_id
 JOIN candidates c ON c.organization_id=a.organization_id AND c.id=a.candidate_id
 JOIN test_versions v ON v.organization_id=a.organization_id AND v.id=a.test_version_id
 LEFT JOIN vacancies j ON j.organization_id=i.organization_id AND j.id=i.vacancy_id
 WHERE a.organization_id=$1 AND ($2<>'assessor' OR EXISTS(SELECT 1 FROM vacancy_assessors va WHERE va.organization_id=i.organization_id AND va.vacancy_id=i.vacancy_id AND va.user_id=$3))`
const resultColumns = `a.id,c.full_name,v.title,coalesce(j.title,''),a.status,a.score_points::text,a.max_points::text,a.score_percent::text,a.passed,a.started_at,a.finished_at,v.version_no,a.attempt_no`
const resultFilters = ` AND ($4='' OR strpos(lower(c.full_name||' '||v.title||' '||coalesce(j.title,'')),lower($4))>0)
 AND ($5='' OR a.status=$5) AND ($6='' OR a.passed::text=$6)
 AND ($7::timestamptz IS NULL OR a.started_at>=$7) AND ($8::timestamptz IS NULL OR a.started_at<$8)
 AND ($9='' OR a.candidate_id::text=$9) AND ($10='' OR v.test_id::text=$10) AND ($11='' OR i.vacancy_id::text=$11)`

func resultArgs(w http.ResponseWriter, r *http.Request, p principal) ([]any, bool) {
	q := r.URL.Query()
	search := strings.TrimSpace(q.Get("search"))
	status := q.Get("status")
	passed := q.Get("passed")
	if len(search) > 500 || (status != "" && status != "in_progress" && status != "submitted" && status != "expired" && status != "cancelled") || (passed != "" && passed != "true" && passed != "false") {
		problem(w, 400, "invalid_filter", "Проверьте фильтры результатов")
		return nil, false
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
			problem(w, 400, "invalid_filter", "Дата должна иметь формат ГГГГ-ММ-ДД")
			return nil, false
		}
		if date.end {
			t = t.AddDate(0, 0, 1)
		}
		*date.target = &t
	}
	if from != nil && to != nil && !from.Before(*to) {
		problem(w, 400, "invalid_filter", "Дата окончания раньше даты начала")
		return nil, false
	}
	ids := []string{q.Get("candidate_id"), q.Get("test_id"), q.Get("vacancy_id")}
	for n, id := range ids {
		if id != "" && !uuid(id) {
			problem(w, 400, "invalid_filter", "Проверьте идентификаторы фильтров")
			return nil, false
		}
		ids[n] = strings.ToLower(id)
	}
	return []any{p.OrganizationID, p.Role, p.UserID, search, status, passed, from, to, ids[0], ids[1], ids[2]}, true
}
func scanResult(row interface{ Scan(...any) error }) (resultRecord, error) {
	var i resultRecord
	err := row.Scan(&i.ID, &i.CandidateName, &i.TestTitle, &i.VacancyTitle, &i.Status, &i.ScorePoints, &i.MaxPoints, &i.Percent, &i.Passed, &i.Started, &i.Finished, &i.TestVersion, &i.AttemptNo)
	return i, err
}
func queryResults(ctx context.Context, tx pgx.Tx, args []any, limit, offset int) ([]resultRecord, error) {
	params := append(append([]any{}, args...), limit, offset)
	rows, err := tx.Query(ctx, "SELECT "+resultColumns+resultsFrom+resultFilters+" ORDER BY a.started_at DESC,a.id LIMIT $12 OFFSET $13", params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []resultRecord{}
	for rows.Next() {
		item, err := scanResult(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func (a *App) listResults(w http.ResponseWriter, r *http.Request, p principal) {
	page, ok := listPage(w, r)
	if !ok {
		return
	}
	args, ok := resultArgs(w, r, p)
	if !ok {
		return
	}
	tx, err := a.Pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	var total int
	err = tx.QueryRow(r.Context(), "SELECT count(*)"+resultsFrom+resultFilters, args...).Scan(&total)
	if err != nil {
		a.failure(w, err)
		return
	}
	items, err := queryResults(r.Context(), tx, args, 20, (page-1)*20)
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items, "total": total, "page": page, "page_size": 20})
}
