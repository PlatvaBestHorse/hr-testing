package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type testItemInput struct {
	QuestionVersionID string `json:"question_version_id"`
	Points            string `json:"points"`
}
type testInput struct {
	Title            string          `json:"title"`
	Description      string          `json:"description"`
	Duration         int             `json:"duration_seconds"`
	PassPercent      string          `json:"pass_percent"`
	ShuffleQuestions bool            `json:"shuffle_questions"`
	ShuffleOptions   bool            `json:"shuffle_options"`
	ShowScore        bool            `json:"show_candidate_score"`
	Items            []testItemInput `json:"items"`
	BaseVersion      int             `json:"base_version_no"`
}
type testItem struct {
	ID                string `json:"id"`
	QuestionID        string `json:"question_id"`
	QuestionVersionID string `json:"question_version_id"`
	QuestionVersionNo int    `json:"question_version_no"`
	Body              string `json:"body"`
	Type              string `json:"type"`
	Position          int    `json:"position"`
	Points            string `json:"points"`
}
type testRecord struct {
	ID               string        `json:"id"`
	Status           string        `json:"status"`
	VersionID        string        `json:"version_id"`
	VersionNo        int           `json:"version_no"`
	LatestVersionNo  int           `json:"latest_version_no"`
	VersionStatus    string        `json:"version_status"`
	Title            string        `json:"title"`
	Description      string        `json:"description"`
	Duration         int           `json:"duration_seconds"`
	PassPercent      string        `json:"pass_percent"`
	ShuffleQuestions bool          `json:"shuffle_questions"`
	ShuffleOptions   bool          `json:"shuffle_options"`
	ShowScore        bool          `json:"show_candidate_score"`
	QuestionCount    int           `json:"question_count"`
	MaxPoints        string        `json:"max_points"`
	Items            []testItem    `json:"items"`
	Versions         []versionInfo `json:"versions"`
}

var decimalPattern = regexp.MustCompile(`^[0-9]{1,6}(\.[0-9]{1,2})?$`)

func decimalCents(value string, min, max int64) (int64, bool) {
	value = strings.TrimSpace(value)
	if !decimalPattern.MatchString(value) {
		return 0, false
	}
	parts := strings.Split(value, ".")
	whole, _ := strconv.ParseInt(parts[0], 10, 64)
	fraction := int64(0)
	if len(parts) == 2 {
		tail := parts[1]
		if len(tail) == 1 {
			tail += "0"
		}
		fraction, _ = strconv.ParseInt(tail, 10, 64)
	}
	n := whole*100 + fraction
	return n, n >= min && n <= max
}
func validateTest(in *testInput) []string {
	in.Title = strings.TrimSpace(in.Title)
	in.Description = strings.TrimSpace(in.Description)
	errors := []string{}
	if !validText(in.Title, 250) {
		errors = append(errors, "Название обязательно, максимум 250 символов")
	}
	if utf8.RuneCountInString(in.Description) > 10000 {
		errors = append(errors, "Описание — до 10000 символов")
	}
	if in.Duration < 60 || in.Duration > 28800 {
		errors = append(errors, "Время теста — от 1 до 480 минут")
	}
	if _, ok := decimalCents(in.PassPercent, 0, 10000); !ok {
		errors = append(errors, "Проходной процент — от 0 до 100 с двумя знаками после точки")
	}
	if len(in.Items) > 200 {
		errors = append(errors, "В тесте допускается максимум 200 вопросов")
	}
	seen := map[string]bool{}
	var total int64
	for i, item := range in.Items {
		if !uuid(item.QuestionVersionID) {
			errors = append(errors, fmt.Sprintf("Задание %d: выберите опубликованный вопрос", i+1))
		}
		key := strings.ToLower(item.QuestionVersionID)
		if seen[key] {
			errors = append(errors, fmt.Sprintf("Задание %d: вопрос повторяется", i+1))
		}
		seen[key] = true
		points, ok := decimalCents(item.Points, 1, 99999999)
		if !ok {
			errors = append(errors, fmt.Sprintf("Задание %d: вес от 0.01 до 999999.99, максимум два знака после точки", i+1))
		}
		total += points
	}
	if total > 9999999999 {
		errors = append(errors, "Сумма весов не должна превышать 99999999.99")
	}
	return errors
}
func testProblems(w http.ResponseWriter, errors []string) {
	write(w, 422, map[string]any{"code": "test_validation_failed", "message": "Проверьте параметры и вопросы теста", "errors": errors})
}

// The parent question rows are locked before reading their immutable published versions.
func (a *App) validateTestSources(ctx context.Context, tx pgx.Tx, org string, items []testItemInput) ([]string, error) {
	ids := []string{}
	for _, item := range items {
		ids = append(ids, item.QuestionVersionID)
	}
	if len(ids) == 0 {
		return []string{}, nil
	}
	rows, err := tx.Query(ctx, `SELECT q.id FROM questions q WHERE q.organization_id=$1 AND q.id IN
      (SELECT question_id FROM question_versions WHERE organization_id=$1 AND id=ANY($2::uuid[])) ORDER BY q.id FOR SHARE`, org, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	type source struct {
		question, status, cardStatus string
		content                      questionInput
	}
	sources := map[string]*source{}
	rows, err = tx.Query(ctx, `SELECT v.id,v.question_id,v.status,q.status,coalesce(q.category_id::text,''),q.difficulty,v.type,v.body,coalesce(v.passage,''),coalesce(v.explanation,'')
       FROM question_versions v JOIN questions q ON q.organization_id=v.organization_id AND q.id=v.question_id WHERE v.organization_id=$1 AND v.id=ANY($2::uuid[])`, org, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		s := &source{}
		if err = rows.Scan(&id, &s.question, &s.status, &s.cardStatus, &s.content.CategoryID, &s.content.Difficulty, &s.content.Type, &s.content.Body, &s.content.Passage, &s.content.Explanation); err != nil {
			break
		}
		sources[id] = s
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.Query(ctx, `SELECT question_version_id,label,is_correct FROM question_options WHERE organization_id=$1 AND question_version_id=ANY($2::uuid[]) ORDER BY question_version_id,position`, org, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var option questionOption
		if err = rows.Scan(&id, &option.Label, &option.Correct); err != nil {
			break
		}
		if src := sources[id]; src != nil {
			src.content.Options = append(src.content.Options, option)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	issues := []string{}
	seen := map[string]bool{}
	for i, item := range items {
		src, ok := sources[strings.ToLower(item.QuestionVersionID)]
		if !ok || src.status != "published" || src.cardStatus == "archived" {
			issues = append(issues, fmt.Sprintf("Задание %d: опубликованная версия недоступна в вашей организации", i+1))
			continue
		}
		if seen[src.question] {
			issues = append(issues, fmt.Sprintf("Задание %d: две редакции одного вопроса недопустимы", i+1))
		}
		seen[src.question] = true
		if message := validateQuestion(&src.content); message != "" {
			issues = append(issues, fmt.Sprintf("Задание %d: %s", i+1, message))
		}
	}
	return issues, nil
}

const testColumns = `t.id,t.status,v.id,v.version_no,v.status,v.title,coalesce(v.description,''),v.duration_seconds,v.pass_percent::text,v.shuffle_questions,v.shuffle_options,v.show_candidate_score,
 (SELECT count(*) FROM test_items WHERE organization_id=t.organization_id AND test_version_id=v.id),
 (SELECT coalesce(sum(points),0)::text FROM test_items WHERE organization_id=t.organization_id AND test_version_id=v.id),
 (SELECT max(version_no) FROM test_versions WHERE organization_id=t.organization_id AND test_id=t.id)`

func scanTest(row interface{ Scan(...any) error }) (testRecord, error) {
	t := testRecord{Items: []testItem{}, Versions: []versionInfo{}}
	err := row.Scan(&t.ID, &t.Status, &t.VersionID, &t.VersionNo, &t.VersionStatus, &t.Title, &t.Description, &t.Duration, &t.PassPercent, &t.ShuffleQuestions, &t.ShuffleOptions, &t.ShowScore, &t.QuestionCount, &t.MaxPoints, &t.LatestVersionNo)
	return t, err
}
func (a *App) listTests(w http.ResponseWriter, r *http.Request, p principal) {
	page, ok := listPage(w, r)
	if !ok {
		return
	}
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	published := r.URL.Query().Get("published_only") == "true"
	status := r.URL.Query().Get("status")
	if status == "published" {
		published = true
	}
	if utf8.RuneCountInString(search) > 200 || (status != "" && status != "draft" && status != "published" && status != "archived") {
		problem(w, 400, "invalid_filter", "Проверьте фильтры тестов")
		return
	}
	from := ` FROM tests t JOIN LATERAL(SELECT * FROM test_versions WHERE organization_id=t.organization_id AND test_id=t.id AND (NOT $3 OR status='published') ORDER BY version_no DESC LIMIT 1) v ON true
 WHERE t.organization_id=$1 AND ($2='' OR v.title ILIKE '%'||$2||'%') AND (NOT $3 OR t.status='active')
 AND ($4='' OR ($4='archived' AND t.status='archived') OR ($4<>'archived' AND t.status='active' AND v.status=$4))`
	var total int
	if err := a.Pool.QueryRow(r.Context(), `SELECT count(*)`+from, p.OrganizationID, search, published, status).Scan(&total); err != nil {
		a.failure(w, err)
		return
	}
	rows, err := a.Pool.Query(r.Context(), `SELECT `+testColumns+from+` ORDER BY t.created_at DESC,t.id LIMIT 20 OFFSET $5`, p.OrganizationID, search, published, status, (page-1)*20)
	if err != nil {
		a.failure(w, err)
		return
	}
	defer rows.Close()
	items := []testRecord{}
	for rows.Next() {
		t, err := scanTest(rows)
		if err != nil {
			a.failure(w, err)
			return
		}
		items = append(items, t)
	}
	if err = rows.Err(); err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items, "total": total, "page": page, "page_size": 20})
}
func (a *App) getTest(w http.ResponseWriter, r *http.Request, p principal) {
	id := r.PathValue("id")
	if !uuid(id) {
		problem(w, 404, "not_found", "Тест не найден")
		return
	}
	version := 0
	if v := r.URL.Query().Get("version_no"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			problem(w, 400, "invalid_version", "Некорректная версия")
			return
		}
		version = n
	}
	t, err := scanTest(a.Pool.QueryRow(r.Context(), `SELECT `+testColumns+` FROM tests t JOIN LATERAL(SELECT * FROM test_versions WHERE organization_id=t.organization_id AND test_id=t.id AND ($3=0 OR version_no=$3) ORDER BY version_no DESC LIMIT 1) v ON true WHERE t.organization_id=$1 AND t.id=$2`, p.OrganizationID, id, version))
	if err != nil {
		a.failure(w, err)
		return
	}
	rows, err := a.Pool.Query(r.Context(), `SELECT i.id,v.question_id,v.id,v.version_no,v.body,v.type,i.position,i.points::text FROM test_items i JOIN question_versions v ON v.organization_id=i.organization_id AND v.id=i.question_version_id WHERE i.organization_id=$1 AND i.test_version_id=$2 ORDER BY i.position`, p.OrganizationID, t.VersionID)
	if err != nil {
		a.failure(w, err)
		return
	}
	for rows.Next() {
		var item testItem
		if err = rows.Scan(&item.ID, &item.QuestionID, &item.QuestionVersionID, &item.QuestionVersionNo, &item.Body, &item.Type, &item.Position, &item.Points); err != nil {
			break
		}
		t.Items = append(t.Items, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		a.failure(w, err)
		return
	}
	rows, err = a.Pool.Query(r.Context(), `SELECT version_no,status,created_at FROM test_versions WHERE organization_id=$1 AND test_id=$2 ORDER BY version_no DESC`, p.OrganizationID, id)
	if err != nil {
		a.failure(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var v versionInfo
		if err = rows.Scan(&v.Number, &v.Status, &v.CreatedAt); err != nil {
			a.failure(w, err)
			return
		}
		t.Versions = append(t.Versions, v)
	}
	if err = rows.Err(); err != nil {
		a.failure(w, err)
		return
	}
	t.LatestVersionNo = t.Versions[0].Number
	write(w, 200, t)
}
func (a *App) saveTest(w http.ResponseWriter, r *http.Request, p principal, create bool) {
	var in testInput
	if !readJSON(w, r, &in) {
		return
	}
	errors := validateTest(&in)
	if !create && in.BaseVersion < 1 {
		errors = append(errors, "Укажите исходную версию теста")
	}
	if len(errors) > 0 {
		testProblems(w, errors)
		return
	}
	id := r.PathValue("id")
	if !create && !uuid(id) {
		problem(w, 404, "not_found", "Тест не найден")
		return
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	number := 1
	action := "test.create"
	if create {
		err = tx.QueryRow(r.Context(), `INSERT INTO tests(organization_id,created_by) VALUES ($1,$2) RETURNING id`, p.OrganizationID, p.UserID).Scan(&id)
	} else {
		var status string
		err = tx.QueryRow(r.Context(), `SELECT status FROM tests WHERE organization_id=$1 AND id=$2 FOR UPDATE`, p.OrganizationID, id).Scan(&status)
		if err != nil {
			a.failure(w, err)
			return
		}
		if status == "archived" {
			problem(w, 409, "archived", "Архивный тест нельзя редактировать")
			return
		}
		var latest int
		err = tx.QueryRow(r.Context(), `SELECT max(version_no) FROM test_versions WHERE organization_id=$1 AND test_id=$2`, p.OrganizationID, id).Scan(&latest)
		if err != nil {
			a.failure(w, err)
			return
		}
		if in.BaseVersion != latest {
			problem(w, 409, "version_conflict", "Тест уже изменён. Обновите страницу")
			return
		}
		number = latest + 1
		action = "test.version.create"
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	errors, err = a.validateTestSources(r.Context(), tx, p.OrganizationID, in.Items)
	if err != nil {
		a.failure(w, err)
		return
	}
	if len(errors) > 0 {
		testProblems(w, errors)
		return
	}
	var versionID string
	err = tx.QueryRow(r.Context(), `INSERT INTO test_versions(organization_id,test_id,version_no,title,description,duration_seconds,pass_percent,shuffle_questions,shuffle_options,show_candidate_score) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`, p.OrganizationID, id, number, in.Title, in.Description, in.Duration, in.PassPercent, in.ShuffleQuestions, in.ShuffleOptions, in.ShowScore).Scan(&versionID)
	if err != nil {
		a.failure(w, err)
		return
	}
	for i, item := range in.Items {
		if _, err = tx.Exec(r.Context(), `INSERT INTO test_items(organization_id,test_version_id,question_version_id,position,points) VALUES ($1,$2,$3,$4,$5)`, p.OrganizationID, versionID, item.QuestionVersionID, i+1, item.Points); err != nil {
			a.failure(w, err)
			return
		}
	}
	if _, err = tx.Exec(r.Context(), `UPDATE tests SET updated_at=now() WHERE organization_id=$1 AND id=$2`, p.OrganizationID, id); err != nil {
		a.failure(w, err)
		return
	}
	if err = a.audit(r.Context(), tx, p, action, "test", id); err != nil {
		a.failure(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		a.failure(w, err)
		return
	}
	write(w, 201, map[string]any{"id": id, "version_no": number})
}
func (a *App) createTest(w http.ResponseWriter, r *http.Request, p principal) {
	a.saveTest(w, r, p, true)
}
func (a *App) createTestVersion(w http.ResponseWriter, r *http.Request, p principal) {
	a.saveTest(w, r, p, false)
}
func (a *App) publishTest(w http.ResponseWriter, r *http.Request, p principal) {
	id := r.PathValue("id")
	if !uuid(id) {
		problem(w, 404, "not_found", "Тест не найден")
		return
	}
	var in struct {
		BaseVersion int `json:"base_version_no"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	var status string
	if err = tx.QueryRow(r.Context(), `SELECT status FROM tests WHERE organization_id=$1 AND id=$2 FOR UPDATE`, p.OrganizationID, id).Scan(&status); err != nil {
		a.failure(w, err)
		return
	}
	if status == "archived" {
		problem(w, 409, "archived", "Архивный тест нельзя публиковать")
		return
	}
	var versionID, versionStatus string
	var number int
	if err = tx.QueryRow(r.Context(), `SELECT id,version_no,status FROM test_versions WHERE organization_id=$1 AND test_id=$2 ORDER BY version_no DESC LIMIT 1 FOR UPDATE`, p.OrganizationID, id).Scan(&versionID, &number, &versionStatus); err != nil {
		a.failure(w, err)
		return
	}
	if number != in.BaseVersion {
		problem(w, 409, "version_conflict", "Тест изменён. Обновите страницу")
		return
	}
	if versionStatus != "published" {
		items := []testItemInput{}
		rows, err := tx.Query(r.Context(), `SELECT question_version_id,points::text FROM test_items WHERE organization_id=$1 AND test_version_id=$2 ORDER BY position`, p.OrganizationID, versionID)
		if err != nil {
			a.failure(w, err)
			return
		}
		for rows.Next() {
			var item testItemInput
			if err = rows.Scan(&item.QuestionVersionID, &item.Points); err != nil {
				break
			}
			items = append(items, item)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			a.failure(w, err)
			return
		}
		var draft testInput
		if err = tx.QueryRow(r.Context(), `SELECT title,coalesce(description,''),duration_seconds,pass_percent::text,shuffle_questions,shuffle_options,show_candidate_score FROM test_versions WHERE organization_id=$1 AND id=$2`, p.OrganizationID, versionID).Scan(&draft.Title, &draft.Description, &draft.Duration, &draft.PassPercent, &draft.ShuffleQuestions, &draft.ShuffleOptions, &draft.ShowScore); err != nil {
			a.failure(w, err)
			return
		}
		draft.Items = items
		errors := validateTest(&draft)
		if len(items) == 0 {
			errors = append(errors, "Добавьте хотя бы один вопрос")
		}
		sourceErrors, sourceErr := a.validateTestSources(r.Context(), tx, p.OrganizationID, items)
		if sourceErr != nil {
			a.failure(w, sourceErr)
			return
		}
		errors = append(errors, sourceErrors...)
		if len(errors) > 0 {
			testProblems(w, errors)
			return
		}
		if _, err = tx.Exec(r.Context(), `UPDATE test_versions SET status='published',published_at=now() WHERE organization_id=$1 AND id=$2`, p.OrganizationID, versionID); err != nil {
			a.failure(w, err)
			return
		}
		if err = a.audit(r.Context(), tx, p, "test.publish", "test", id); err != nil {
			a.failure(w, err)
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, map[string]any{"id": id, "version_id": versionID, "version_no": number, "status": "published"})
}
func (a *App) archiveTest(w http.ResponseWriter, r *http.Request, p principal) {
	id := r.PathValue("id")
	if !uuid(id) {
		problem(w, 404, "not_found", "Тест не найден")
		return
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	result, err := tx.Exec(r.Context(), `UPDATE tests SET status='archived',updated_at=now() WHERE organization_id=$1 AND id=$2`, p.OrganizationID, id)
	if err != nil {
		a.failure(w, err)
		return
	}
	if result.RowsAffected() == 0 {
		problem(w, 404, "not_found", "Тест не найден")
		return
	}
	if err = a.audit(r.Context(), tx, p, "test.archive", "test", id); err != nil {
		a.failure(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		a.failure(w, err)
		return
	}
	w.WriteHeader(204)
}
func (a *App) availableQuestionVersions(w http.ResponseWriter, r *http.Request, p principal) {
	page, ok := listPage(w, r)
	if !ok {
		return
	}
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	if utf8.RuneCountInString(search) > 200 {
		problem(w, 400, "invalid_filter", "Поиск — до 200 символов")
		return
	}
	from := ` FROM questions q JOIN LATERAL(SELECT * FROM question_versions WHERE organization_id=q.organization_id AND question_id=q.id AND status='published' ORDER BY version_no DESC LIMIT 1) v ON true WHERE q.organization_id=$1 AND q.status<>'archived' AND ($2='' OR v.body ILIKE '%'||$2||'%')`
	var total int
	if err := a.Pool.QueryRow(r.Context(), `SELECT count(*)`+from, p.OrganizationID, search).Scan(&total); err != nil {
		a.failure(w, err)
		return
	}
	rows, err := a.Pool.Query(r.Context(), `SELECT v.id,q.id,v.version_no,v.body,v.type`+from+` ORDER BY q.created_at DESC,q.id LIMIT 20 OFFSET $3`, p.OrganizationID, search, (page-1)*20)
	if err != nil {
		a.failure(w, err)
		return
	}
	defer rows.Close()
	items := []testItem{}
	for rows.Next() {
		var item testItem
		if err = rows.Scan(&item.QuestionVersionID, &item.QuestionID, &item.QuestionVersionNo, &item.Body, &item.Type); err != nil {
			a.failure(w, err)
			return
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items, "total": total, "page": page, "page_size": 20})
}
