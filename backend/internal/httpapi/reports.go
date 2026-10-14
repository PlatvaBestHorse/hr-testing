package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

type reportOption struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Selected bool   `json:"selected"`
	Correct  bool   `json:"is_correct"`
}
type reportItem struct {
	ID             string         `json:"id"`
	Position       int            `json:"position"`
	Type           string         `json:"type"`
	Body           string         `json:"body"`
	Passage        string         `json:"passage"`
	Explanation    string         `json:"explanation"`
	Version        int            `json:"question_version_no"`
	CategoryID     string         `json:"category_id"`
	CategoryName   string         `json:"category_name"`
	CategoryFrozen bool           `json:"category_frozen"`
	Options        []reportOption `json:"options"`
	Correct        bool           `json:"correct"`
	Answered       bool           `json:"answered"`
	Earned         string         `json:"score_points"`
	Maximum        string         `json:"max_points"`
}
type reportCategory struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Questions int    `json:"questions"`
	Answered  int    `json:"answered"`
	Correct   int    `json:"correct"`
	Earned    string `json:"score_points"`
	Maximum   string `json:"max_points"`
	Percent   string `json:"score_percent"`
}
type attemptReport struct {
	resultRecord
	Deadline         time.Time        `json:"deadline_at"`
	PassPercent      string           `json:"pass_percent"`
	CategoriesFrozen bool             `json:"categories_frozen"`
	Questions        []reportItem     `json:"questions"`
	Categories       []reportCategory `json:"categories"`
	Answered         int              `json:"answered"`
	Correct          int              `json:"correct"`
}

var errReportPending = errors.New("report not finalized")

func readReport(ctx context.Context, tx pgx.Tx, p principal, id string) (attemptReport, error) {
	out := attemptReport{CategoriesFrozen: true, Questions: []reportItem{}, Categories: []reportCategory{}}
	meta, err := scanResult(tx.QueryRow(ctx, "SELECT "+resultColumns+resultsFrom+" AND a.id=$4", p.OrganizationID, p.Role, p.UserID, id))
	if err != nil {
		return out, err
	}
	out.resultRecord = meta
	if meta.Status != "submitted" && meta.Status != "expired" {
		return out, errReportPending
	}
	if err = tx.QueryRow(ctx, `SELECT a.deadline_at,v.pass_percent::text FROM attempts a JOIN test_versions v ON v.organization_id=a.organization_id AND v.id=a.test_version_id WHERE a.organization_id=$1 AND a.id=$2`, p.OrganizationID, id).Scan(&out.Deadline, &out.PassPercent); err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `SELECT ai.id,ai.position,q.type,q.body,coalesce(q.passage,''),coalesce(q.explanation,''),q.version_no,
 coalesce(ai.category_snapshot->>'id',c.id::text,''),coalesce(ai.category_snapshot->>'name',c.name,'Без категории'),ai.category_snapshot IS NOT NULL,
 coalesce(aa.selected_option_ids,'{}'::uuid[]), (ti.points*100)::bigint,
 ARRAY(SELECT o.id FROM question_options o WHERE o.organization_id=ti.organization_id AND o.question_version_id=ti.question_version_id AND o.is_correct ORDER BY o.id)
 = ARRAY(SELECT x FROM unnest(coalesce(aa.selected_option_ids,'{}'::uuid[])) x ORDER BY x),
 (SELECT jsonb_agg(jsonb_build_object('id',o.id,'label',o.label,'selected',o.id=ANY(coalesce(aa.selected_option_ids,'{}'::uuid[])),'is_correct',o.is_correct) ORDER BY ord.n)
 FROM jsonb_array_elements_text(ai.option_order) WITH ORDINALITY ord(id,n) JOIN question_options o ON o.organization_id=ai.organization_id AND o.id=ord.id::uuid)
 FROM attempt_items ai JOIN test_items ti ON ti.organization_id=ai.organization_id AND ti.id=ai.test_item_id
 JOIN question_versions q ON q.organization_id=ti.organization_id AND q.id=ti.question_version_id
 JOIN questions root ON root.organization_id=q.organization_id AND root.id=q.question_id
 LEFT JOIN categories c ON c.organization_id=root.organization_id AND c.id=root.category_id
 LEFT JOIN attempt_answers aa ON aa.organization_id=ai.organization_id AND aa.attempt_item_id=ai.id
 WHERE ai.organization_id=$1 AND ai.attempt_id=$2 ORDER BY ai.position`, p.OrganizationID, id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	type total struct {
		category    reportCategory
		earned, max int64
	}
	totals := []total{}
	indices := map[string]int{}
	for rows.Next() {
		var item reportItem
		var selected []string
		var max int64
		if err = rows.Scan(&item.ID, &item.Position, &item.Type, &item.Body, &item.Passage, &item.Explanation, &item.Version, &item.CategoryID, &item.CategoryName, &item.CategoryFrozen, &selected, &max, &item.Correct, &item.Options); err != nil {
			return out, err
		}
		item.Answered = len(selected) > 0
		earned := int64(0)
		if item.Correct {
			earned = max
			out.Correct++
		}
		if item.Answered {
			out.Answered++
		}
		item.Earned = centsText(earned)
		item.Maximum = centsText(max)
		out.CategoriesFrozen = out.CategoriesFrozen && item.CategoryFrozen
		out.Questions = append(out.Questions, item)
		key := item.CategoryID + "\n" + item.CategoryName
		n, exists := indices[key]
		if !exists {
			n = len(totals)
			indices[key] = n
			totals = append(totals, total{category: reportCategory{ID: item.CategoryID, Name: item.CategoryName}})
		}
		t := &totals[n]
		t.category.Questions++
		if item.Answered {
			t.category.Answered++
		}
		if item.Correct {
			t.category.Correct++
		}
		t.earned += earned
		t.max += max
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	for _, t := range totals {
		t.category.Earned = centsText(t.earned)
		t.category.Maximum = centsText(t.max)
		t.category.Percent = centsText(scorePercent(t.earned, t.max))
		out.Categories = append(out.Categories, t.category)
	}
	return out, nil
}
func (a *App) reportFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, errReportPending) {
		problem(w, 409, "report_pending", "Подробный отчёт доступен после завершения теста")
		return
	}
	a.failure(w, err)
}
func (a *App) getResult(w http.ResponseWriter, r *http.Request, p principal) {
	id := r.PathValue("id")
	if !uuid(id) {
		problem(w, 404, "not_found", "Результат не найден")
		return
	}
	tx, err := a.Pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	report, err := readReport(r.Context(), tx, p, id)
	if err != nil {
		a.reportFailure(w, err)
		return
	}
	if err = a.audit(r.Context(), tx, p, "result.view", "attempt", id); err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, report)
}
