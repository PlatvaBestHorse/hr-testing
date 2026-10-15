package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type questionOption struct {
	ID      string `json:"id,omitempty"`
	Label   string `json:"label"`
	Correct bool   `json:"is_correct"`
	ImageID string `json:"image_id"`
}
type questionInput struct {
	CategoryID  string           `json:"category_id"`
	Difficulty  int              `json:"difficulty"`
	Type        string           `json:"type"`
	Body        string           `json:"body"`
	Passage     string           `json:"passage"`
	Explanation string           `json:"explanation"`
	ImageID     string           `json:"image_id"`
	Options     []questionOption `json:"options"`
	BaseVersion int              `json:"base_version_no"`
}
type versionInfo struct {
	Number    int       `json:"version_no"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}
type question struct {
	ID              string           `json:"id"`
	CategoryID      string           `json:"category_id"`
	CategoryName    string           `json:"category_name"`
	Difficulty      int              `json:"difficulty"`
	Status          string           `json:"status"`
	VersionID       string           `json:"version_id"`
	VersionNo       int              `json:"version_no"`
	LatestVersionNo int              `json:"latest_version_no"`
	VersionStatus   string           `json:"version_status"`
	Type            string           `json:"type"`
	Body            string           `json:"body"`
	Passage         string           `json:"passage"`
	Explanation     string           `json:"explanation"`
	ImageID         string           `json:"image_id"`
	CreatedAt       time.Time        `json:"created_at"`
	Options         []questionOption `json:"options"`
	Versions        []versionInfo    `json:"versions"`
}

func validateQuestion(in *questionInput) string {
	in.Body = strings.TrimSpace(in.Body)
	in.Passage = strings.TrimSpace(in.Passage)
	in.Explanation = strings.TrimSpace(in.Explanation)
	if in.ImageID != "" && !uuid(in.ImageID) {
		return "Некорректное изображение вопроса"
	}
	in.ImageID = strings.ToLower(in.ImageID)
	if in.CategoryID != "" && !uuid(in.CategoryID) {
		return "Выберите доступную категорию"
	}
	if !validText(in.Body, 10000) {
		return "Текст вопроса обязателен, максимум 10000 символов"
	}
	if utf8.RuneCountInString(in.Passage) > 20000 || utf8.RuneCountInString(in.Explanation) > 10000 {
		return "Вводный текст или пояснение слишком длинные"
	}
	if in.Difficulty < 1 || in.Difficulty > 5 {
		return "Сложность должна быть от 1 до 5"
	}
	if in.Type != "single_choice" && in.Type != "multiple_choice" && in.Type != "verbal_tfu" {
		return "Выберите поддерживаемый тип вопроса"
	}
	if len(in.Options) < 2 || len(in.Options) > 10 {
		return "Добавьте от 2 до 10 вариантов ответа"
	}
	correct := 0
	seen := map[string]bool{}
	for i := range in.Options {
		if in.Options[i].ImageID != "" && !uuid(in.Options[i].ImageID) {
			return "Некорректное изображение варианта ответа"
		}
		in.Options[i].ImageID = strings.ToLower(in.Options[i].ImageID)
		in.Options[i].Label = strings.TrimSpace(in.Options[i].Label)
		label := in.Options[i].Label
		if !validText(label, 2000) {
			return "Каждый вариант ответа должен содержать текст, максимум 2000 символов"
		}
		key := strings.ToLower(label)
		if seen[key] {
			return "Варианты ответа не должны повторяться"
		}
		seen[key] = true
		if in.Options[i].Correct {
			correct++
		}
	}
	if in.Type == "verbal_tfu" {
		if len(in.Options) != 3 {
			return "Для вербального вопроса нужны ровно три варианта"
		}
		for i, label := range []string{"Верно", "Неверно", "Недостаточно информации"} {
			if in.Options[i].Label != label {
				return "Вербальные варианты: Верно, Неверно, Недостаточно информации"
			}
		}
	}
	if in.Type == "multiple_choice" && correct < 2 {
		return "Для множественного выбора отметьте минимум два правильных ответа"
	}
	if in.Type != "multiple_choice" && correct != 1 {
		return "Отметьте ровно один правильный ответ"
	}
	return ""
}

const questionColumns = `q.id,coalesce(q.category_id::text,''),coalesce(c.name,''),q.difficulty,q.status,
v.id,v.version_no,v.status,v.type,v.body,coalesce(v.passage,''),coalesce(v.explanation,''),v.created_at,coalesce(v.image_id::text,'')`

func scanQuestion(row interface{ Scan(...any) error }) (question, error) {
	q := question{Options: []questionOption{}, Versions: []versionInfo{}}
	err := row.Scan(&q.ID, &q.CategoryID, &q.CategoryName, &q.Difficulty, &q.Status, &q.VersionID, &q.VersionNo, &q.VersionStatus, &q.Type, &q.Body, &q.Passage, &q.Explanation, &q.CreatedAt, &q.ImageID)
	q.LatestVersionNo = q.VersionNo
	return q, err
}

func (a *App) listQuestions(w http.ResponseWriter, r *http.Request, p principal) {
	query := r.URL.Query()
	search := strings.TrimSpace(query.Get("search"))
	categoryID := query.Get("category_id")
	status := query.Get("status")
	if utf8.RuneCountInString(search) > 200 || (categoryID != "" && !uuid(categoryID)) || (status != "" && status != "draft" && status != "active" && status != "archived") {
		problem(w, 400, "invalid_filter", "Проверьте фильтры")
		return
	}
	page := 1
	if query.Get("page") != "" {
		n, err := strconv.Atoi(query.Get("page"))
		if err != nil || n < 1 || n > 100000 {
			problem(w, 400, "invalid_page", "Некорректная страница")
			return
		}
		page = n
	}
	where := ` FROM questions q JOIN LATERAL (SELECT * FROM question_versions WHERE organization_id=q.organization_id AND question_id=q.id ORDER BY version_no DESC LIMIT 1) v ON true
LEFT JOIN categories c ON c.organization_id=q.organization_id AND c.id=q.category_id
WHERE q.organization_id=$1 AND ($2='' OR v.body ILIKE '%' || $2 || '%') AND ($3='' OR q.category_id::text=$3) AND ($4='' OR q.status=$4)`
	var total int
	if err := a.Pool.QueryRow(r.Context(), `SELECT count(*)`+where, p.OrganizationID, search, categoryID, status).Scan(&total); err != nil {
		a.failure(w, err)
		return
	}
	rows, err := a.Pool.Query(r.Context(), `SELECT `+questionColumns+where+` ORDER BY q.created_at DESC,q.id LIMIT 20 OFFSET $5`, p.OrganizationID, search, categoryID, status, (page-1)*20)
	if err != nil {
		a.failure(w, err)
		return
	}
	defer rows.Close()
	items := []question{}
	for rows.Next() {
		q, err := scanQuestion(rows)
		if err != nil {
			a.failure(w, err)
			return
		}
		items = append(items, q)
	}
	if err = rows.Err(); err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items, "total": total, "page": page, "page_size": 20})
}

func (a *App) getQuestion(w http.ResponseWriter, r *http.Request, p principal) {
	id := r.PathValue("id")
	if !uuid(id) {
		problem(w, 404, "not_found", "Вопрос не найден")
		return
	}
	version := 0
	if value := r.URL.Query().Get("version_no"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 {
			problem(w, 400, "invalid_version", "Некорректная версия")
			return
		}
		version = n
	}
	q, err := scanQuestion(a.Pool.QueryRow(r.Context(), `SELECT `+questionColumns+` FROM questions q
JOIN LATERAL (SELECT * FROM question_versions WHERE organization_id=q.organization_id AND question_id=q.id AND ($3=0 OR version_no=$3) ORDER BY version_no DESC LIMIT 1) v ON true
LEFT JOIN categories c ON c.organization_id=q.organization_id AND c.id=q.category_id WHERE q.organization_id=$1 AND q.id=$2`, p.OrganizationID, id, version))
	if err != nil {
		a.failure(w, err)
		return
	}
	rows, err := a.Pool.Query(r.Context(), `SELECT id,label,is_correct,coalesce(image_id::text,'') FROM question_options WHERE organization_id=$1 AND question_version_id=$2 ORDER BY position`, p.OrganizationID, q.VersionID)
	if err != nil {
		a.failure(w, err)
		return
	}
	for rows.Next() {
		var option questionOption
		if err = rows.Scan(&option.ID, &option.Label, &option.Correct, &option.ImageID); err != nil {
			rows.Close()
			a.failure(w, err)
			return
		}
		q.Options = append(q.Options, option)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		a.failure(w, err)
		return
	}
	rows, err = a.Pool.Query(r.Context(), `SELECT version_no,status,created_at FROM question_versions WHERE organization_id=$1 AND question_id=$2 ORDER BY version_no DESC`, p.OrganizationID, id)
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
		q.Versions = append(q.Versions, v)
	}
	if err = rows.Err(); err != nil {
		a.failure(w, err)
		return
	}
	if len(q.Versions) > 0 {
		q.LatestVersionNo = q.Versions[0].Number
	}
	write(w, 200, q)
}

func (a *App) saveQuestion(w http.ResponseWriter, r *http.Request, p principal, create bool) {
	var in questionInput
	if !readJSON(w, r, &in) {
		return
	}
	if message := validateQuestion(&in); message != "" {
		problem(w, 422, "validation_error", message)
		return
	}
	if !create && in.BaseVersion < 1 {
		problem(w, 422, "version_required", "Укажите исходную версию вопроса")
		return
	}
	id := r.PathValue("id")
	if !create && !uuid(id) {
		problem(w, 404, "not_found", "Вопрос не найден")
		return
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	imageIDs := []string{}
	imageSet := map[string]bool{}
	for _, img := range append([]questionOption{{ImageID: in.ImageID}}, in.Options...) {
		if img.ImageID != "" && !imageSet[img.ImageID] {
			imageSet[img.ImageID] = true
			imageIDs = append(imageIDs, img.ImageID)
		}
	}
	if len(imageIDs) > 0 {
		var found int
		if err = tx.QueryRow(r.Context(), "SELECT count(*) FROM attachments WHERE organization_id=$1 AND id=ANY($2::uuid[]) AND data IS NOT NULL", p.OrganizationID, imageIDs).Scan(&found); err != nil {
			a.failure(w, err)
			return
		}
		if found != len(imageIDs) {
			problem(w, 422, "invalid_image", "Изображение не найдено в вашей организации")
			return
		}
	}
	if in.CategoryID != "" {
		var found bool
		err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM categories WHERE organization_id=$1 AND id=$2)`, p.OrganizationID, in.CategoryID).Scan(&found)
		if err != nil {
			a.failure(w, err)
			return
		}
		if !found {
			problem(w, 422, "invalid_category", "Категория не найдена в вашей организации")
			return
		}
	}
	var categoryValue any
	if in.CategoryID != "" {
		categoryValue = in.CategoryID
	}
	number := 1
	action := "question.create"
	if create {
		err = tx.QueryRow(r.Context(), `INSERT INTO questions(organization_id,category_id,difficulty,created_by) VALUES ($1,$2,$3,$4) RETURNING id`, p.OrganizationID, categoryValue, in.Difficulty, p.UserID).Scan(&id)
	} else {
		var status string
		err = tx.QueryRow(r.Context(), `SELECT status FROM questions WHERE organization_id=$1 AND id=$2 FOR UPDATE`, p.OrganizationID, id).Scan(&status)
		if err != nil {
			a.failure(w, err)
			return
		}
		if status == "archived" {
			problem(w, 409, "archived", "Архивный вопрос нельзя редактировать")
			return
		}
		var latest int
		err = tx.QueryRow(r.Context(), `SELECT max(version_no) FROM question_versions WHERE organization_id=$1 AND question_id=$2`, p.OrganizationID, id).Scan(&latest)
		if err != nil {
			a.failure(w, err)
			return
		}
		if in.BaseVersion != latest {
			problem(w, 409, "version_conflict", "Вопрос уже изменён. Обновите страницу перед сохранением")
			return
		}
		number = latest + 1
		action = "question.version.create"
		_, err = tx.Exec(r.Context(), `UPDATE questions SET category_id=$3,difficulty=$4,status='draft',updated_at=now() WHERE organization_id=$1 AND id=$2`, p.OrganizationID, id, categoryValue, in.Difficulty)
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	var versionID string
	err = tx.QueryRow(r.Context(), `INSERT INTO question_versions(organization_id,question_id,version_no,type,body,passage,explanation,image_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`, p.OrganizationID, id, number, in.Type, in.Body, in.Passage, in.Explanation, optionalUUID(in.ImageID)).Scan(&versionID)
	if err != nil {
		a.failure(w, err)
		return
	}
	for i, option := range in.Options {
		key := fmt.Sprintf("%c", 'A'+i)
		if in.Type == "verbal_tfu" {
			key = []string{"true", "false", "unknown"}[i]
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO question_options(organization_id,question_version_id,option_key,label,is_correct,position,image_id) VALUES ($1,$2,$3,$4,$5,$6,$7)`, p.OrganizationID, versionID, key, option.Label, option.Correct, i+1, optionalUUID(option.ImageID)); err != nil {
			a.failure(w, err)
			return
		}
	}
	if err = a.audit(r.Context(), tx, p, action, "question", id); err != nil {
		a.failure(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		a.failure(w, err)
		return
	}
	write(w, 201, map[string]any{"id": id, "version_no": number})
}
func (a *App) createQuestion(w http.ResponseWriter, r *http.Request, p principal) {
	a.saveQuestion(w, r, p, true)
}
func (a *App) createVersion(w http.ResponseWriter, r *http.Request, p principal) {
	a.saveQuestion(w, r, p, false)
}

func (a *App) publishQuestion(w http.ResponseWriter, r *http.Request, p principal) {
	id := r.PathValue("id")
	if !uuid(id) {
		problem(w, 404, "not_found", "Вопрос не найден")
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
	err = tx.QueryRow(r.Context(), `SELECT status FROM questions WHERE organization_id=$1 AND id=$2 FOR UPDATE`, p.OrganizationID, id).Scan(&status)
	if err != nil {
		a.failure(w, err)
		return
	}
	if status == "archived" {
		problem(w, 409, "archived", "Архивный вопрос нельзя публиковать")
		return
	}
	var versionID, versionStatus string
	var number int
	err = tx.QueryRow(r.Context(), `SELECT id,version_no,status FROM question_versions WHERE organization_id=$1 AND question_id=$2 ORDER BY version_no DESC LIMIT 1 FOR UPDATE`, p.OrganizationID, id).Scan(&versionID, &number, &versionStatus)
	if err != nil {
		a.failure(w, err)
		return
	}
	if number != in.BaseVersion {
		problem(w, 409, "version_conflict", "Вопрос изменён. Обновите страницу")
		return
	}
	if versionStatus != "published" {
		var content questionInput
		err = tx.QueryRow(r.Context(), `SELECT coalesce(q.category_id::text,''),q.difficulty,v.type,v.body,coalesce(v.passage,''),coalesce(v.explanation,'') FROM questions q JOIN question_versions v ON v.organization_id=q.organization_id AND v.question_id=q.id WHERE q.organization_id=$1 AND q.id=$2 AND v.id=$3`, p.OrganizationID, id, versionID).Scan(&content.CategoryID, &content.Difficulty, &content.Type, &content.Body, &content.Passage, &content.Explanation)
		if err != nil {
			a.failure(w, err)
			return
		}
		rows, err := tx.Query(r.Context(), `SELECT label,is_correct FROM question_options WHERE organization_id=$1 AND question_version_id=$2 ORDER BY position`, p.OrganizationID, versionID)
		if err != nil {
			a.failure(w, err)
			return
		}
		for rows.Next() {
			var option questionOption
			if err = rows.Scan(&option.Label, &option.Correct); err != nil {
				rows.Close()
				a.failure(w, err)
				return
			}
			content.Options = append(content.Options, option)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			a.failure(w, err)
			return
		}
		if message := validateQuestion(&content); message != "" {
			problem(w, 422, "validation_error", message)
			return
		}
		if _, err = tx.Exec(r.Context(), `UPDATE question_versions SET status='published',published_at=now() WHERE organization_id=$1 AND id=$2`, p.OrganizationID, versionID); err != nil {
			a.failure(w, err)
			return
		}
		if _, err = tx.Exec(r.Context(), `UPDATE questions SET status='active',updated_at=now() WHERE organization_id=$1 AND id=$2`, p.OrganizationID, id); err != nil {
			a.failure(w, err)
			return
		}
		if err = a.audit(r.Context(), tx, p, "question.publish", "question", id); err != nil {
			a.failure(w, err)
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, map[string]any{"id": id, "version_no": number, "status": "published"})
}

func (a *App) archiveQuestion(w http.ResponseWriter, r *http.Request, p principal) {
	id := r.PathValue("id")
	if !uuid(id) {
		problem(w, 404, "not_found", "Вопрос не найден")
		return
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	result, err := tx.Exec(r.Context(), `UPDATE questions SET status='archived',updated_at=now() WHERE organization_id=$1 AND id=$2`, p.OrganizationID, id)
	if err != nil {
		a.failure(w, err)
		return
	}
	if result.RowsAffected() == 0 {
		problem(w, 404, "not_found", "Вопрос не найден")
		return
	}
	if err = a.audit(r.Context(), tx, p, "question.archive", "question", id); err != nil {
		a.failure(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		a.failure(w, err)
		return
	}
	w.WriteHeader(204)
}

func (a *App) summary(w http.ResponseWriter, r *http.Request, p principal) {
	var questions, tests, candidates, attempts int
	err := a.Pool.QueryRow(r.Context(), `SELECT
(SELECT count(*) FROM questions WHERE organization_id=$1 AND status<>'archived'),
(SELECT count(*) FROM tests t WHERE t.organization_id=$1 AND t.status='active' AND EXISTS(SELECT 1 FROM test_versions v WHERE v.organization_id=t.organization_id AND v.test_id=t.id AND v.status='published')),
(SELECT count(*) FROM candidates WHERE organization_id=$1 AND status='active'),
(SELECT count(*) FROM attempts WHERE organization_id=$1 AND status IN ('submitted','expired'))`, p.OrganizationID).Scan(&questions, &tests, &candidates, &attempts)
	if err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, map[string]int{"questions": questions, "tests": tests, "candidates": candidates, "attempts": attempts})
}
