package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Candidate DTOs deliberately contain no scoring keys, weights or pass threshold.
type candidateOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}
type candidateItem struct {
	ID       string            `json:"id"`
	Position int               `json:"position"`
	Type     string            `json:"type"`
	Body     string            `json:"body"`
	Passage  string            `json:"passage"`
	Options  []candidateOption `json:"options"`
	Selected []string          `json:"selected_option_ids"`
	Revision int               `json:"revision"`
}
type candidateScore struct {
	Percent string `json:"percent"`
	Passed  bool   `json:"passed"`
}
type candidateAttempt struct {
	ID        string          `json:"id"`
	Status    string          `json:"status"`
	Title     string          `json:"title"`
	Started   time.Time       `json:"started_at"`
	Deadline  time.Time       `json:"deadline_at"`
	ServerNow time.Time       `json:"server_now"`
	Finished  *time.Time      `json:"finished_at,omitempty"`
	Items     []candidateItem `json:"items"`
	Score     *candidateScore `json:"score,omitempty"`
	Remaining int             `json:"attempts_remaining"`
}
type attemptInvite struct {
	Org, ID, Candidate, Version, Status string
	Expiry                              time.Time
	Max, Duration                       int
	ShuffleQuestions, ShuffleOptions    bool
}

func validInviteToken(token string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(raw) == 32 && base64.RawURLEncoding.EncodeToString(raw) == token
}
func (a *App) candidateRoute(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
		if !validInviteToken(r.PathValue("token")) {
			problem(w, 404, "invite_unavailable", "Приглашение недоступно. Уточните ссылку у HR")
			return
		}
		if r.Method != "GET" && !a.originAllowed(r) {
			problem(w, 403, "origin_rejected", "Источник запроса не разрешён")
			return
		}
		next(w, r)
	}
}

// All mutations, including the expiry worker, lock invitation -> attempt.
// clock_timestamp is read after lock acquisition, so waiting cannot extend a deadline.
func lockInvite(ctx context.Context, tx pgx.Tx, token string) (attemptInvite, error) {
	var i attemptInvite
	err := tx.QueryRow(ctx, `SELECT i.organization_id,i.id,i.candidate_id,i.test_version_id,i.status,i.expires_at,i.max_attempts,
 v.duration_seconds,v.shuffle_questions,v.shuffle_options
 FROM invitations i JOIN test_versions v ON v.organization_id=i.organization_id AND v.id=i.test_version_id
 JOIN organizations o ON o.id=i.organization_id JOIN candidates c ON c.organization_id=i.organization_id AND c.id=i.candidate_id
 WHERE i.token_hash=$1 AND i.status='issued' AND o.status='active' AND c.status='active' AND v.status='published'
 FOR UPDATE OF i`, digest(token)).Scan(&i.Org, &i.ID, &i.Candidate, &i.Version, &i.Status, &i.Expiry, &i.Max, &i.Duration, &i.ShuffleQuestions, &i.ShuffleOptions)
	return i, err
}
func databaseNow(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var now time.Time
	err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now)
	return now, err
}
func shuffle[T any](values []T) error {
	for n := len(values) - 1; n > 0; n-- {
		k, err := rand.Int(rand.Reader, big.NewInt(int64(n+1)))
		if err != nil {
			return err
		}
		values[n], values[int(k.Int64())] = values[int(k.Int64())], values[n]
	}
	return nil
}

func (a *App) startAttempt(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RequestID  string `json:"request_id"`
		Consent    bool   `json:"consent"`
		NewAttempt bool   `json:"new_attempt"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !uuid(in.RequestID) || !in.Consent {
		problem(w, 422, "consent_required", "Подтвердите правила прохождения и обработку ответов")
		return
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	i, err := lockInvite(r.Context(), tx, r.PathValue("token"))
	if err != nil {
		a.failure(w, err)
		return
	}
	var id string
	// Lost start response or repeated click returns precisely the same attempt.
	err = tx.QueryRow(r.Context(), `SELECT attempt_id FROM start_receipts WHERE organization_id=$1 AND invitation_id=$2 AND request_id=$3`, i.Org, i.ID, in.RequestID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(r.Context(), `SELECT id FROM attempts WHERE organization_id=$1 AND invitation_id=$2 AND (status='in_progress' OR NOT $3) ORDER BY attempt_no DESC LIMIT 1 FOR UPDATE`, i.Org, i.ID, in.NewAttempt).Scan(&id)
	}
	if err == nil {
		if err = a.expireOne(r.Context(), tx, i.Org, id); err != nil {
			a.failure(w, err)
			return
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO start_receipts(organization_id,invitation_id,request_id,attempt_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, i.Org, i.ID, in.RequestID, id); err != nil {
			a.failure(w, err)
			return
		}
		a.replyAttempt(w, r, tx, i, id)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		a.failure(w, err)
		return
	}
	now, err := databaseNow(r.Context(), tx)
	if err != nil {
		a.failure(w, err)
		return
	}
	var used int
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM attempts WHERE organization_id=$1 AND invitation_id=$2`, i.Org, i.ID).Scan(&used); err != nil {
		a.failure(w, err)
		return
	}
	if !i.Expiry.After(now) || used >= i.Max {
		problem(w, 409, "attempt_limit", "Срок приглашения или число попыток исчерпаны")
		return
	}
	deadline := now.Add(time.Duration(i.Duration) * time.Second)
	if i.Expiry.Before(deadline) {
		deadline = i.Expiry
	}
	err = tx.QueryRow(r.Context(), `INSERT INTO attempts(organization_id,invitation_id,candidate_id,test_version_id,attempt_no,started_at,deadline_at,start_request_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`, i.Org, i.ID, i.Candidate, i.Version, used+1, now, deadline, in.RequestID).Scan(&id)
	if err != nil {
		a.failure(w, err)
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO start_receipts(organization_id,invitation_id,request_id,attempt_id) VALUES($1,$2,$3,$4)`, i.Org, i.ID, in.RequestID, id); err != nil {
		a.failure(w, err)
		return
	}
	type sourceItem struct {
		ID, Type string
		Options  []string
		Category []byte
	}
	rows, err := tx.Query(r.Context(), `SELECT ti.id,q.type,coalesce(jsonb_agg(o.id ORDER BY o.position),'[]'::jsonb),
 jsonb_build_object('id',coalesce(c.id::text,''),'name',coalesce(c.name,'Без категории'))
 FROM test_items ti JOIN question_versions q ON q.organization_id=ti.organization_id AND q.id=ti.question_version_id
 JOIN questions root ON root.organization_id=q.organization_id AND root.id=q.question_id
 LEFT JOIN categories c ON c.organization_id=root.organization_id AND c.id=root.category_id
 JOIN question_options o ON o.organization_id=q.organization_id AND o.question_version_id=q.id
 WHERE ti.organization_id=$1 AND ti.test_version_id=$2 GROUP BY ti.id,ti.position,q.type,c.id,c.name ORDER BY ti.position`, i.Org, i.Version)
	if err != nil {
		a.failure(w, err)
		return
	}
	sources := []sourceItem{}
	for rows.Next() {
		var s sourceItem
		err = rows.Scan(&s.ID, &s.Type, &s.Options, &s.Category)
		if err != nil {
			break
		}
		sources = append(sources, s)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		a.failure(w, err)
		return
	}
	if len(sources) == 0 {
		problem(w, 409, "test_unavailable", "В тесте отсутствуют вопросы")
		return
	}
	if i.ShuffleQuestions {
		err = shuffle(sources)
	}
	for pos, s := range sources {
		// TFU labels keep their defined order, regardless of the general option setting.
		if err == nil && i.ShuffleOptions && s.Type != "verbal_tfu" {
			err = shuffle(s.Options)
		}
		if err != nil {
			a.failure(w, err)
			return
		}
		order, _ := json.Marshal(s.Options)
		_, err = tx.Exec(r.Context(), `INSERT INTO attempt_items(organization_id,attempt_id,test_item_id,position,option_order,category_snapshot) VALUES($1,$2,$3,$4,$5,$6)`, i.Org, id, s.ID, pos+1, order, s.Category)
		if err != nil {
			a.failure(w, err)
			return
		}
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO candidate_consents(organization_id,candidate_id,document_version,purpose,is_granted,evidence)
 VALUES($1,$2,'development-passage-v1','test_passage',true,jsonb_build_object('attempt_id',$3::text))`, i.Org, i.Candidate, id)
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO audit_events(organization_id,action,object_type,object_id) VALUES($1,'attempt.start','attempt',$2)`, i.Org, id)
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	a.replyAttempt(w, r, tx, i, id)
}

func (a *App) getAttempt(w http.ResponseWriter, r *http.Request) {
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	i, err := lockInvite(r.Context(), tx, r.PathValue("token"))
	if err != nil {
		a.failure(w, err)
		return
	}
	var id string
	err = tx.QueryRow(r.Context(), `SELECT id FROM attempts WHERE organization_id=$1 AND invitation_id=$2 ORDER BY attempt_no DESC LIMIT 1 FOR UPDATE`, i.Org, i.ID).Scan(&id)
	if err != nil {
		a.failure(w, err)
		return
	}
	if err = a.expireOne(r.Context(), tx, i.Org, id); err != nil {
		a.failure(w, err)
		return
	}
	a.replyAttempt(w, r, tx, i, id)
}

func snapshot(ctx context.Context, tx pgx.Tx, i attemptInvite, id string) (candidateAttempt, error) {
	out := candidateAttempt{Items: []candidateItem{}}
	var show, passed bool
	var percent string
	err := tx.QueryRow(ctx, `SELECT a.id,a.status,v.title,a.started_at,a.deadline_at,a.finished_at,clock_timestamp(),
 v.show_candidate_score,coalesce(a.score_percent::text,''),coalesce(a.passed,false),
 greatest(0,$3-(SELECT count(*) FROM attempts WHERE organization_id=$1 AND invitation_id=$4))
 FROM attempts a JOIN test_versions v ON v.organization_id=a.organization_id AND v.id=a.test_version_id
 WHERE a.organization_id=$1 AND a.id=$2`, i.Org, id, i.Max, i.ID).Scan(&out.ID, &out.Status, &out.Title, &out.Started, &out.Deadline, &out.Finished, &out.ServerNow, &show, &percent, &passed, &out.Remaining)
	if err != nil {
		return out, err
	}
	if show && (out.Status == "submitted" || out.Status == "expired") {
		out.Score = &candidateScore{Percent: percent, Passed: passed}
	}
	if out.Status != "in_progress" {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT ai.id,ai.position,q.type,q.body,coalesce(q.passage,''),
 coalesce(aa.selected_option_ids,'{}'::uuid[]),coalesce(aa.revision,0),
 (SELECT jsonb_agg(jsonb_build_object('id',o.id,'label',o.label) ORDER BY ord.n)
 FROM jsonb_array_elements_text(ai.option_order) WITH ORDINALITY ord(id,n)
 JOIN question_options o ON o.organization_id=ai.organization_id AND o.id=ord.id::uuid)
 FROM attempt_items ai JOIN test_items ti ON ti.organization_id=ai.organization_id AND ti.id=ai.test_item_id
 JOIN question_versions q ON q.organization_id=ti.organization_id AND q.id=ti.question_version_id
 LEFT JOIN attempt_answers aa ON aa.organization_id=ai.organization_id AND aa.attempt_item_id=ai.id
 WHERE ai.organization_id=$1 AND ai.attempt_id=$2 ORDER BY ai.position`, i.Org, id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var item candidateItem
		if err = rows.Scan(&item.ID, &item.Position, &item.Type, &item.Body, &item.Passage, &item.Selected, &item.Revision, &item.Options); err != nil {
			return out, err
		}
		out.Items = append(out.Items, item)
	}
	return out, rows.Err()
}
func (a *App) replyAttempt(w http.ResponseWriter, r *http.Request, tx pgx.Tx, i attemptInvite, id string) {
	out, err := snapshot(r.Context(), tx, i, id)
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, out)
}

type answerInput struct {
	RequestID string   `json:"request_id"`
	Expected  int      `json:"expected_revision"`
	Selected  []string `json:"selected_option_ids"`
}
type answerReceipt struct {
	Revision int       `json:"revision"`
	Selected []string  `json:"selected_option_ids"`
	Saved    time.Time `json:"saved_at"`
}

func normalizeSelection(ids []string) ([]string, bool) {
	out := append([]string{}, ids...)
	for n, id := range out {
		if !uuid(id) {
			return nil, false
		}
		out[n] = strings.ToLower(id)
	}
	slices.Sort(out)
	for n := 1; n < len(out); n++ {
		if out[n] == out[n-1] {
			return nil, false
		}
	}
	return out, true
}
func (a *App) saveAnswer(w http.ResponseWriter, r *http.Request) {
	var in answerInput
	if !readJSON(w, r, &in) {
		return
	}
	selected, ok := normalizeSelection(in.Selected)
	if !ok || !uuid(in.RequestID) || in.Expected < 0 || len(selected) > 10 || !uuid(r.PathValue("id")) || !uuid(r.PathValue("item")) {
		problem(w, 422, "invalid_answer", "Проверьте варианты ответа")
		return
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	i, err := lockInvite(r.Context(), tx, r.PathValue("token"))
	if err != nil {
		a.failure(w, err)
		return
	}
	var status string
	var deadline time.Time
	id, item := r.PathValue("id"), r.PathValue("item")
	err = tx.QueryRow(r.Context(), `SELECT status,deadline_at FROM attempts WHERE organization_id=$1 AND invitation_id=$2 AND id=$3 FOR UPDATE`, i.Org, i.ID, id).Scan(&status, &deadline)
	if err != nil {
		a.failure(w, err)
		return
	}
	var kind string
	var allowed []string
	err = tx.QueryRow(r.Context(), `SELECT q.type,ai.option_order FROM attempt_items ai
 JOIN test_items ti ON ti.organization_id=ai.organization_id AND ti.id=ai.test_item_id
 JOIN question_versions q ON q.organization_id=ti.organization_id AND q.id=ti.question_version_id
 WHERE ai.organization_id=$1 AND ai.attempt_id=$2 AND ai.id=$3`, i.Org, id, item).Scan(&kind, &allowed)
	if err != nil {
		a.failure(w, err)
		return
	}
	var receipt answerReceipt
	var expected int
	err = tx.QueryRow(r.Context(), `SELECT expected_revision,selected_option_ids,revision,saved_at FROM answer_receipts WHERE organization_id=$1 AND attempt_item_id=$2 AND request_id=$3`, i.Org, item, in.RequestID).Scan(&expected, &receipt.Selected, &receipt.Revision, &receipt.Saved)
	if err == nil {
		if expected != in.Expected || !slices.Equal(receipt.Selected, selected) {
			problem(w, 409, "request_reused", "Идентификатор запроса уже использован")
			return
		}
		// A receipt confirms a previously accepted write, even after submission.
		if err = tx.Commit(r.Context()); err != nil {
			a.failure(w, err)
			return
		}
		write(w, 200, receipt)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		a.failure(w, err)
		return
	}
	now, err := databaseNow(r.Context(), tx)
	if err != nil {
		a.failure(w, err)
		return
	}
	if status != "in_progress" || !deadline.After(now) {
		if status == "in_progress" {
			err = a.finishAttempt(r.Context(), tx, i.Org, id, "expired", deadline)
		}
		if err == nil {
			err = tx.Commit(r.Context())
		}
		if err != nil {
			a.failure(w, err)
			return
		}
		problem(w, 409, "attempt_finished", "Попытка завершена. Новые ответы не принимаются")
		return
	}
	if kind != "multiple_choice" && len(selected) > 1 {
		problem(w, 422, "invalid_answer", "Выберите один вариант")
		return
	}
	for _, option := range selected {
		if !slices.Contains(allowed, option) {
			problem(w, 422, "invalid_answer", "Вариант не относится к этому вопросу")
			return
		}
	}
	var revision int
	err = tx.QueryRow(r.Context(), `SELECT revision FROM attempt_answers WHERE organization_id=$1 AND attempt_item_id=$2`, i.Org, item).Scan(&revision)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		a.failure(w, err)
		return
	}
	if revision != in.Expected {
		problem(w, 409, "revision_conflict", "Ответ изменён в другой вкладке. Обновите сохранённые ответы")
		return
	}
	receipt = answerReceipt{Revision: revision + 1, Selected: selected, Saved: now}
	_, err = tx.Exec(r.Context(), `INSERT INTO attempt_answers(organization_id,attempt_item_id,selected_option_ids,revision,saved_at) VALUES($1,$2,$3,$4,$5)
 ON CONFLICT(organization_id,attempt_item_id) DO UPDATE SET selected_option_ids=excluded.selected_option_ids,revision=excluded.revision,saved_at=excluded.saved_at`, i.Org, item, selected, receipt.Revision, now)
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO answer_receipts(organization_id,attempt_item_id,request_id,expected_revision,selected_option_ids,revision,saved_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, i.Org, item, in.RequestID, in.Expected, selected, receipt.Revision, now)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE attempts SET revision=revision+1 WHERE organization_id=$1 AND id=$2`, i.Org, id)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, receipt)
}

func scorePercent(earned, max int64) int64 {
	if max == 0 {
		return 0
	}
	return (earned*10000 + max/2) / max
}
func centsText(n int64) string { return fmt.Sprintf("%d.%02d", n/100, n%100) }
func (a *App) finishAttempt(ctx context.Context, tx pgx.Tx, org, id, status string, finished time.Time) error {
	// Exact set comparison: no partial credit for a missing or extra option.
	var earned, max, threshold int64
	err := tx.QueryRow(ctx, `SELECT
 coalesce(sum(CASE WHEN
 ARRAY(SELECT o.id FROM question_options o WHERE o.organization_id=ti.organization_id AND o.question_version_id=ti.question_version_id AND o.is_correct ORDER BY o.id)
 = ARRAY(SELECT x FROM unnest(coalesce(aa.selected_option_ids,'{}'::uuid[])) x ORDER BY x)
 THEN ti.points*100 ELSE 0 END),0)::bigint,
 coalesce(sum(ti.points*100),0)::bigint,(v.pass_percent*100)::bigint
 FROM attempts a JOIN test_versions v ON v.organization_id=a.organization_id AND v.id=a.test_version_id
 JOIN attempt_items ai ON ai.organization_id=a.organization_id AND ai.attempt_id=a.id
 JOIN test_items ti ON ti.organization_id=ai.organization_id AND ti.id=ai.test_item_id
 LEFT JOIN attempt_answers aa ON aa.organization_id=ai.organization_id AND aa.attempt_item_id=ai.id
 WHERE a.organization_id=$1 AND a.id=$2 GROUP BY v.pass_percent`, org, id).Scan(&earned, &max, &threshold)
	if err != nil {
		return err
	}
	percent := scorePercent(earned, max)
	_, err = tx.Exec(ctx, `UPDATE attempts SET status=$3,finished_at=$4,score_points=$5,max_points=$6,score_percent=$7,passed=$8 WHERE organization_id=$1 AND id=$2 AND status='in_progress'`, org, id, status, finished, centsText(earned), centsText(max), centsText(percent), percent >= threshold)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(organization_id,action,object_type,object_id) VALUES($1,$2,'attempt',$3)`, org, "attempt."+status, id)
	return err
}
func (a *App) expireOne(ctx context.Context, tx pgx.Tx, org, id string) error {
	var status string
	var deadline time.Time
	err := tx.QueryRow(ctx, `SELECT status,deadline_at FROM attempts WHERE organization_id=$1 AND id=$2 FOR UPDATE`, org, id).Scan(&status, &deadline)
	if err != nil {
		return err
	}
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return err
	}
	if status == "in_progress" && !deadline.After(now) {
		return a.finishAttempt(ctx, tx, org, id, "expired", deadline)
	}
	return nil
}
func (a *App) submitAttempt(w http.ResponseWriter, r *http.Request) {
	if !uuid(r.PathValue("id")) {
		problem(w, 404, "not_found", "Попытка не найдена")
		return
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	i, err := lockInvite(r.Context(), tx, r.PathValue("token"))
	if err != nil {
		a.failure(w, err)
		return
	}
	var status string
	var deadline time.Time
	id := r.PathValue("id")
	err = tx.QueryRow(r.Context(), `SELECT status,deadline_at FROM attempts WHERE organization_id=$1 AND invitation_id=$2 AND id=$3 FOR UPDATE`, i.Org, i.ID, id).Scan(&status, &deadline)
	if err != nil {
		a.failure(w, err)
		return
	}
	now, err := databaseNow(r.Context(), tx)
	if err != nil {
		a.failure(w, err)
		return
	}
	if status == "in_progress" {
		if !deadline.After(now) {
			status = "expired"
			now = deadline
		} else {
			status = "submitted"
		}
		if err = a.finishAttempt(r.Context(), tx, i.Org, id, status, now); err != nil {
			a.failure(w, err)
			return
		}
	}
	a.replyAttempt(w, r, tx, i, id)
}

// Called immediately on boot and then periodically: closed browsers never postpone scoring.
func (a *App) ExpireDue(ctx context.Context) error {
	rows, err := a.Pool.Query(ctx, `SELECT organization_id,invitation_id,id FROM attempts WHERE status='in_progress' AND deadline_at<=clock_timestamp() ORDER BY deadline_at LIMIT 100`)
	if err != nil {
		return err
	}
	type due struct{ org, invite, id string }
	items := []due{}
	for rows.Next() {
		var d due
		if err = rows.Scan(&d.org, &d.invite, &d.id); err != nil {
			break
		}
		items = append(items, d)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, d := range items {
		tx, e := a.Pool.Begin(ctx)
		if e != nil {
			return e
		}
		var status string
		e = tx.QueryRow(ctx, `SELECT status FROM invitations WHERE organization_id=$1 AND id=$2 FOR UPDATE`, d.org, d.invite).Scan(&status)
		if e == nil && status == "revoked" {
			_, e = tx.Exec(ctx, `UPDATE attempts SET status='cancelled',finished_at=greatest(started_at,clock_timestamp()) WHERE organization_id=$1 AND id=$2 AND status='in_progress'`, d.org, d.id)
		} else if e == nil {
			e = a.expireOne(ctx, tx, d.org, d.id)
		}
		if e == nil {
			e = tx.Commit(ctx)
		} else {
			tx.Rollback(context.Background())
		}
		if e != nil {
			return e
		}
	}
	return nil
}
