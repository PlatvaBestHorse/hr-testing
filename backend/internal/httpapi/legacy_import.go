package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type legacyChoice struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}
type legacyQuestion struct {
	ID          string         `json:"id"`
	Type        string         `json:"type"`
	Category    string         `json:"category"`
	Difficulty  string         `json:"difficulty"`
	Passage     string         `json:"passage"`
	Text        string         `json:"text"`
	Choices     []legacyChoice `json:"choices"`
	Correct     string         `json:"correct"`
	Explanation string         `json:"explanation"`
}
type legacyTest struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Minutes     int      `json:"minutes"`
	Passing     *float64 `json:"passing"`
	QuestionIDs []string `json:"questionIds"`
	Shuffle     *bool    `json:"shuffle"`
	Published   *bool    `json:"published"`
	ShowScore   *bool    `json:"showScore"`
}
type legacyDocument struct {
	Version   int              `json:"version"`
	Questions []legacyQuestion `json:"questions"`
	Tests     []legacyTest     `json:"tests"`
}
type importIssue struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}
type importPreviewTest struct {
	ID        string  `json:"source_id"`
	Title     string  `json:"title"`
	Count     int     `json:"question_count"`
	Minutes   int     `json:"minutes"`
	Passing   float64 `json:"passing"`
	Shuffle   bool    `json:"shuffle"`
	Published bool    `json:"published"`
	ShowScore bool    `json:"show_score"`
}
type importPreview struct {
	Valid          bool                `json:"valid"`
	Hash           string              `json:"source_sha256"`
	Questions      int                 `json:"question_count"`
	Tests          int                 `json:"test_count"`
	Categories     []string            `json:"categories"`
	TestSettings   []importPreviewTest `json:"tests"`
	Errors         []importIssue       `json:"errors"`
	Warnings       []string            `json:"warnings"`
	AlreadyApplied bool                `json:"already_applied"`
}
type importObject struct {
	SourceID string `json:"source_id"`
	ID       string `json:"id"`
}
type importReceipt struct {
	ID                string         `json:"id"`
	Hash              string         `json:"source_sha256"`
	Questions         []importObject `json:"questions"`
	Tests             []importObject `json:"tests"`
	CategoriesCreated int            `json:"categories_created"`
}

var legacyID = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,80}$`)
var legacyDifficulty = map[string]int{"Лёгкий": 1, "Средний": 3, "Сложный": 5}

// Reject duplicate JSON keys so preview/apply cannot interpret an ambiguous file.
func uniqueJSONKeys(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	var walk func(int) bool
	walk = func(depth int) bool {
		if depth > 40 {
			return false
		}
		t, e := d.Token()
		if e != nil {
			return false
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return true
		}
		if delim != '{' && delim != '[' {
			return false
		}
		seen := map[string]bool{}
		for d.More() {
			if delim == '{' {
				k, e := d.Token()
				if e != nil {
					return false
				}
				s, ok := k.(string)
				if !ok || seen[s] {
					return false
				}
				seen[s] = true
			}
			if !walk(depth + 1) {
				return false
			}
		}
		end, e := d.Token()
		return e == nil && ((delim == '{' && end == json.Delim('}')) || (delim == '[' && end == json.Delim(']')))
	}
	if !walk(0) {
		return false
	}
	_, e := d.Token()
	return e == io.EOF
}
func parseLegacy(raw []byte) (legacyDocument, importPreview) {
	var doc legacyDocument
	out := importPreview{Categories: []string{}, TestSettings: []importPreviewTest{}, Errors: []importIssue{}, Warnings: []string{}}
	issue := func(path, code, msg string) {
		if len(out.Errors) < 100 {
			out.Errors = append(out.Errors, importIssue{path, code, msg})
		}
	}
	if !utf8.Valid(raw) || !uniqueJSONKeys(raw) {
		issue("/", "invalid_json", "Нужен UTF-8 JSON без повторяющихся ключей и лишних данных")
		return doc, out
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&doc) != nil {
		issue("/", "unsupported_schema", "Проверьте поля и типы CandidateTester version 1; неизвестные поля не пропускаются")
		return doc, out
	}
	canonical, _ := json.Marshal(doc)
	sum := sha256.Sum256(canonical)
	out.Hash = hex.EncodeToString(sum[:])
	if doc.Version != 1 {
		issue("/version", "unsupported_version", "Поддерживается CandidateTester version 1")
	}
	if len(doc.Questions) < 1 || len(doc.Questions) > 200 {
		issue("/questions", "count_limit", "Требуется от 1 до 200 вопросов")
	}
	if len(doc.Tests) > 20 {
		issue("/tests", "count_limit", "Допускается не более 20 тестов")
	}
	out.Questions = len(doc.Questions)
	out.Tests = len(doc.Tests)
	qs := map[string]bool{}
	cats := map[string]bool{}
	for i, q := range doc.Questions {
		path := fmt.Sprintf("/questions/%d", i)
		if !legacyID.MatchString(q.ID) || qs[q.ID] {
			issue(path+"/id", "invalid_id", "Нужен уникальный ID из латинских букв, цифр, _, . и -; до 80 символов")
		}
		qs[q.ID] = true
		cat := strings.TrimSpace(q.Category)
		if !validText(cat, 150) {
			issue(path+"/category", "invalid_category", "Название категории обязательно, до 150 символов")
		}
		cats[cat] = true
		in := questionInput{Body: q.Text, Passage: q.Passage, Explanation: q.Explanation, Type: "single_choice", Difficulty: legacyDifficulty[q.Difficulty]}
		if q.Type != "single" {
			issue(path+"/type", "unsupported_type", "В этом формате поддерживается тип single; другие типы не преобразуются автоматически")
		}
		found := false
		choices := map[string]bool{}
		for j, c := range q.Choices {
			if !legacyID.MatchString(c.ID) || choices[c.ID] {
				issue(fmt.Sprintf("%s/choices/%d/id", path, j), "invalid_id", "ID вариантов должны быть непустыми и уникальными")
			}
			choices[c.ID] = true
			correct := c.ID == q.Correct
			found = found || correct
			in.Options = append(in.Options, questionOption{Label: c.Text, Correct: correct})
		}
		if !found {
			issue(path+"/correct", "invalid_answer", "Правильный ответ должен ссылаться на ID одного из вариантов")
		}
		if msg := validateQuestion(&in); msg != "" {
			issue(path, "invalid_question", msg)
		}
	}
	for c := range cats {
		out.Categories = append(out.Categories, c)
	}
	sort.Strings(out.Categories)
	ts := map[string]bool{}
	for i, t := range doc.Tests {
		path := fmt.Sprintf("/tests/%d", i)
		if !legacyID.MatchString(t.ID) || ts[t.ID] {
			issue(path+"/id", "invalid_id", "ID тестов должны быть допустимыми и уникальными")
		}
		ts[t.ID] = true
		if !validText(t.Title, 250) || utf8.RuneCountInString(t.Description) > 10000 {
			issue(path, "invalid_text", "Название теста обязательно, до 250 символов; описание до 10000")
		}
		if t.Minutes < 1 || t.Minutes > 480 {
			issue(path+"/minutes", "invalid_duration", "Время — от 1 до 480 минут")
		}
		if t.Passing == nil {
			issue(path+"/passing", "required", "Укажите проходной процент")
		} else if _, ok := decimalCents(strconv.FormatFloat(*t.Passing, 'f', -1, 64), 0, 10000); !ok {
			issue(path+"/passing", "invalid_percent", "Проходной процент — 0–100, до двух десятичных знаков")
		}
		if t.Shuffle == nil || t.Published == nil || t.ShowScore == nil {
			issue(path, "required_settings", "Поля shuffle, published и showScore обязательны и имеют тип boolean")
		}
		if len(t.QuestionIDs) < 1 || len(t.QuestionIDs) > 200 {
			issue(path+"/questionIds", "count_limit", "В тесте должно быть от 1 до 200 вопросов")
		}
		seen := map[string]bool{}
		for j, id := range t.QuestionIDs {
			if !qs[id] || seen[id] {
				issue(fmt.Sprintf("%s/questionIds/%d", path, j), "invalid_reference", "Вопрос отсутствует в файле либо повторяется в тесте")
			}
			seen[id] = true
		}
		if t.Passing != nil && t.Shuffle != nil && t.Published != nil && t.ShowScore != nil {
			out.TestSettings = append(out.TestSettings, importPreviewTest{t.ID, t.Title, len(t.QuestionIDs), t.Minutes, *t.Passing, *t.Shuffle, *t.Published, *t.ShowScore})
		}
	}
	out.Warnings = append(out.Warnings, "Все вопросы создаются опубликованными версиями с весом 1.00. Сложность: Лёгкий → 1, Средний → 3, Сложный → 5.", "Статус тестов, перемешивание вопросов и показ баллов сохраняются из файла. Порядок вариантов не перемешивается.", "Результаты старых прохождений и изображения этот формат не содержит. Содержимое и ключи ответов требуют проверки автором.")
	out.Valid = len(out.Errors) == 0
	return doc, out
}
func (a *App) previewImport(w http.ResponseWriter, r *http.Request, p principal) {
	a.legacyImport(w, r, p, false)
}
func (a *App) applyImport(w http.ResponseWriter, r *http.Request, p principal) {
	a.legacyImport(w, r, p, true)
}
func (a *App) legacyImport(w http.ResponseWriter, r *http.Request, p principal, apply bool) {
	select {
	case a.importSlots <- struct{}{}:
		defer func() { <-a.importSlots }()
	default:
		problem(w, 429, "rate_limited", "Одновременно выполняются другие импорты. Повторите позже")
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if err := a.limitAccount(r.Context(), "import:"+p.OrganizationID+":"+p.UserID, "import:"+ip, 30, 120); err != nil {
		if errors.Is(err, errLimited) {
			w.Header().Set("Retry-After", "900")
			problem(w, 429, "rate_limited", "Слишком много проверок импорта. Повторите через 15 минут")
		} else {
			a.failure(w, err)
		}
		return
	}

	var in struct {
		Source       json.RawMessage `json:"source"`
		Hash         string          `json:"preview_sha256"`
		Confirmation string          `json:"confirmation"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	doc, out := parseLegacy(in.Source)
	if !apply && !out.Valid {
		write(w, 200, out)
		return
	}
	if apply && !out.Valid {
		write(w, 422, map[string]any{"code": "invalid_import", "message": "Исправьте ошибки файла до импорта", "errors": out.Errors})
		return
	}
	if apply && (in.Hash != out.Hash || in.Confirmation != "ИМПОРТИРОВАТЬ") {
		problem(w, 409, "preview_required", "Повторите проверку этого файла и подтвердите импорт")
		return
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	if err = lockRetentionOrg(r.Context(), tx, p.OrganizationID); err != nil {
		a.failure(w, err)
		return
	}
	if !a.confirmRetentionAdmin(w, r, p, tx) {
		return
	}
	hash, _ := hex.DecodeString(out.Hash)
	var previous []byte
	err = tx.QueryRow(r.Context(), "SELECT result FROM import_batches WHERE organization_id=$1 AND source_hash=$2", p.OrganizationID, hash).Scan(&previous)
	if err != nil && err != pgx.ErrNoRows {
		a.failure(w, err)
		return
	}
	if err == nil {
		if apply {
			var saved importReceipt
			if json.Unmarshal(previous, &saved) != nil {
				problem(w, 500, "internal_error", "Не удалось прочитать квитанцию")
				return
			}
			write(w, 200, saved)
		} else {
			out.AlreadyApplied = true
			write(w, 200, out)
		}
		return
	}
	qids := []string{}
	tids := []string{}
	for _, q := range doc.Questions {
		qids = append(qids, q.ID)
	}
	for _, t := range doc.Tests {
		tids = append(tids, t.ID)
	}
	var conflicts int
	if err = tx.QueryRow(r.Context(), `SELECT (SELECT count(*) FROM imported_questions WHERE organization_id=$1 AND legacy_id=ANY($2::text[]))+(SELECT count(*) FROM imported_tests WHERE organization_id=$1 AND legacy_id=ANY($3::text[]))`, p.OrganizationID, qids, tids).Scan(&conflicts); err != nil {
		a.failure(w, err)
		return
	}
	if conflicts > 0 {
		out.Errors = append(out.Errors, importIssue{"/", "source_id_conflict", "Некоторые ID уже импортированы из другого содержимого. Существующие объекты не перезаписываются"})
		out.Valid = false
	}
	if !apply {
		write(w, 200, out)
		return
	}
	if !out.Valid {
		write(w, 409, map[string]any{"code": "source_id_conflict", "message": "Эти ID уже импортированы; существующие объекты сохранены", "errors": out.Errors})
		return
	}
	receipt, err := a.insertLegacy(r.Context(), tx, p, doc, out.Hash)
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	write(w, 201, receipt)
}
func (a *App) insertLegacy(ctx context.Context, tx pgx.Tx, p principal, doc legacyDocument, hash string) (importReceipt, error) {
	out := importReceipt{Hash: hash, Questions: []importObject{}, Tests: []importObject{}}
	cats := map[string]string{}
	versions := map[string]string{}
	for _, q := range doc.Questions {
		cat := strings.TrimSpace(q.Category)
		catID := cats[cat]
		if catID == "" {
			err := tx.QueryRow(ctx, `INSERT INTO categories(organization_id,name) VALUES($1,$2) ON CONFLICT(organization_id,name) DO NOTHING RETURNING id`, p.OrganizationID, cat).Scan(&catID)
			if err == pgx.ErrNoRows {
				err = tx.QueryRow(ctx, "SELECT id FROM categories WHERE organization_id=$1 AND name=$2", p.OrganizationID, cat).Scan(&catID)
			} else if err == nil {
				out.CategoriesCreated++
			}
			if err != nil {
				return out, err
			}
			cats[cat] = catID
		}
		var id, v string
		err := tx.QueryRow(ctx, `INSERT INTO questions(organization_id,category_id,difficulty,created_by,status) VALUES($1,$2,$3,$4,'active') RETURNING id`, p.OrganizationID, catID, legacyDifficulty[q.Difficulty], p.UserID).Scan(&id)
		if err != nil {
			return out, err
		}
		err = tx.QueryRow(ctx, `INSERT INTO question_versions(organization_id,question_id,version_no,type,body,passage,explanation) VALUES($1,$2,1,'single_choice',$3,$4,$5) RETURNING id`, p.OrganizationID, id, strings.TrimSpace(q.Text), strings.TrimSpace(q.Passage), strings.TrimSpace(q.Explanation)).Scan(&v)
		if err != nil {
			return out, err
		}
		for i, c := range q.Choices {
			if _, err = tx.Exec(ctx, `INSERT INTO question_options(organization_id,question_version_id,option_key,label,is_correct,position) VALUES($1,$2,$3,$4,$5,$6)`, p.OrganizationID, v, fmt.Sprintf("%c", 'A'+i), strings.TrimSpace(c.Text), c.ID == q.Correct, i+1); err != nil {
				return out, err
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE question_versions SET status='published',published_at=now() WHERE organization_id=$1 AND id=$2`, p.OrganizationID, v); err != nil {
			return out, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO imported_questions(organization_id,legacy_id,question_id) VALUES($1,$2,$3)`, p.OrganizationID, q.ID, id); err != nil {
			return out, err
		}
		versions[q.ID] = v
		out.Questions = append(out.Questions, importObject{q.ID, id})
	}
	for _, t := range doc.Tests {
		var id, v string
		err := tx.QueryRow(ctx, `INSERT INTO tests(organization_id,created_by) VALUES($1,$2) RETURNING id`, p.OrganizationID, p.UserID).Scan(&id)
		if err != nil {
			return out, err
		}
		err = tx.QueryRow(ctx, `INSERT INTO test_versions(organization_id,test_id,version_no,title,description,duration_seconds,pass_percent,shuffle_questions,shuffle_options,show_candidate_score) VALUES($1,$2,1,$3,$4,$5,$6,$7,false,$8) RETURNING id`, p.OrganizationID, id, strings.TrimSpace(t.Title), strings.TrimSpace(t.Description), t.Minutes*60, strconv.FormatFloat(*t.Passing, 'f', 2, 64), *t.Shuffle, *t.ShowScore).Scan(&v)
		if err != nil {
			return out, err
		}
		for i, qid := range t.QuestionIDs {
			if _, err = tx.Exec(ctx, `INSERT INTO test_items(organization_id,test_version_id,question_version_id,position,points) VALUES($1,$2,$3,$4,1.00)`, p.OrganizationID, v, versions[qid], i+1); err != nil {
				return out, err
			}
		}
		if *t.Published {
			if _, err = tx.Exec(ctx, `UPDATE test_versions SET status='published',published_at=now() WHERE organization_id=$1 AND id=$2`, p.OrganizationID, v); err != nil {
				return out, err
			}
		}
		if _, err = tx.Exec(ctx, `INSERT INTO imported_tests(organization_id,legacy_id,test_id) VALUES($1,$2,$3)`, p.OrganizationID, t.ID, id); err != nil {
			return out, err
		}
		out.Tests = append(out.Tests, importObject{t.ID, id})
	}
	if err := tx.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&out.ID); err != nil {
		return out, err
	}
	data, _ := json.Marshal(out)
	rawHash, _ := hex.DecodeString(hash)
	if _, err := tx.Exec(ctx, `INSERT INTO import_batches(id,organization_id,actor_user_id,source_hash,result) VALUES($1,$2,$3,$4,$5)`, out.ID, p.OrganizationID, p.UserID, rawHash, data); err != nil {
		return out, err
	}
	return out, a.audit(ctx, tx, p, "import.apply", "import", out.ID)
}
