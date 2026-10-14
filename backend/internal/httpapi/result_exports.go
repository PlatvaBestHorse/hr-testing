package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

const maxExportRows = 10000

var resultStatusNames = map[string]string{"submitted": "Завершено", "expired": "Время вышло", "in_progress": "Проходит тест", "cancelled": "Отменено"}

func textPointer(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
func passedText(value *bool) string {
	if value == nil {
		return ""
	}
	if *value {
		return "Да"
	}
	return "Нет"
}
func pointerCell(value *string) exportCell {
	if value == nil {
		return exportCell{}
	}
	return number(*value)
}
func summarySheet(items []resultRecord) exportSheet {
	sheet := exportSheet{name: "Результаты", rows: [][]exportCell{cells("ID попытки", "Кандидат", "Тест", "Версия теста", "Попытка", "Вакансия", "Статус", "Начало (МСК)", "Завершение (МСК)", "Баллы", "Максимум", "Процент", "Порог достигнут")}}
	for _, i := range items {
		finished := ""
		if i.Finished != nil {
			finished = i.Finished.In(moscow).Format("2006-01-02 15:04:05 -0700")
		}
		row := cells(i.ID, i.CandidateName, i.TestTitle, strconv.Itoa(i.TestVersion), strconv.Itoa(i.AttemptNo), i.VacancyTitle, resultStatusNames[i.Status], i.Started.In(moscow).Format("2006-01-02 15:04:05 -0700"), finished)
		row = append(row, pointerCell(i.ScorePoints), pointerCell(i.MaxPoints), pointerCell(i.Percent), exportCell{text: passedText(i.Passed)})
		sheet.rows = append(sheet.rows, row)
	}
	return sheet
}
func reportSheets(report attemptReport) []exportSheet {
	summary := summarySheet([]resultRecord{report.resultRecord})
	summary.rows = append(summary.rows, cells(""), cells("Правильных вопросов", strconv.Itoa(report.Correct)), cells("Вопросов с ответом", strconv.Itoa(report.Answered)), cells("Всего вопросов", strconv.Itoa(len(report.Questions))), cells("Проходной процент", report.PassPercent))
	categorySource := "Зафиксированы при старте"
	if !report.CategoriesFrozen {
		categorySource = "Для старых заданий категории взяты из текущего банка"
	}
	summary.rows = append(summary.rows, cells("Категории", categorySource))
	categories := exportSheet{name: "Категории", rows: [][]exportCell{cells("Категория", "Вопросов", "С ответом", "Верных", "Баллы", "Максимум", "Процент")}}
	for _, c := range report.Categories {
		categories.rows = append(categories.rows, []exportCell{{text: c.Name}, number(strconv.Itoa(c.Questions)), number(strconv.Itoa(c.Answered)), number(strconv.Itoa(c.Correct)), number(c.Earned), number(c.Maximum), number(c.Percent)})
	}
	questions := exportSheet{name: "Ответы", rows: [][]exportCell{cells("ID попытки", "Кандидат", "Тест", "Версия теста", "Статус", "Итоговые баллы", "Итоговый процент", "№", "Категория", "Источник категории", "Тип", "Версия вопроса", "Текст вопроса", "Вводный текст", "Ответ кандидата", "Правильный ответ", "Верно", "Баллы вопроса", "Вес", "Пояснение")}}
	for _, q := range report.Questions {
		selected, correct := []string{}, []string{}
		for _, o := range q.Options {
			if o.Selected {
				selected = append(selected, o.Label)
			}
			if o.Correct {
				correct = append(correct, o.Label)
			}
		}
		verdict := "Нет"
		if q.Correct {
			verdict = "Да"
		}
		source := "При старте"
		if !q.CategoryFrozen {
			source = "Текущий банк"
		}
		row := cells(report.ID, report.CandidateName, report.TestTitle, strconv.Itoa(report.TestVersion), resultStatusNames[report.Status], textPointer(report.ScorePoints), textPointer(report.Percent), strconv.Itoa(q.Position), q.CategoryName, source, q.Type, strconv.Itoa(q.Version), q.Body, q.Passage, strings.Join(selected, "\n"), strings.Join(correct, "\n"), verdict)
		row[5] = pointerCell(report.ScorePoints)
		row[6] = pointerCell(report.Percent)
		row = append(row, number(q.Earned), number(q.Maximum), exportCell{text: q.Explanation})
		questions.rows = append(questions.rows, row)
	}
	return []exportSheet{summary, categories, questions}
}
func exportFormat(w http.ResponseWriter, r *http.Request) (string, bool) {
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "xlsx" {
		problem(w, 400, "invalid_format", "Выберите CSV или XLSX")
		return "", false
	}
	return format, true
}
func makeExport(format string, sheets []exportSheet, detail bool) ([]byte, error) {
	if format == "xlsx" {
		return xlsxFile(sheets)
	}
	n := 0
	if detail {
		n = len(sheets) - 1
	}
	return csvFile(sheets[n])
}
func (a *App) auditExport(ctx context.Context, tx pgx.Tx, p principal, r *http.Request, format, id string, count int) error {
	var object any
	if id != "" {
		object = id
	}
	details, _ := json.Marshal(map[string]any{"format": format, "row_count": count, "date_from": r.URL.Query().Get("date_from"), "date_to": r.URL.Query().Get("date_to"), "status": r.URL.Query().Get("status")})
	_, err := tx.Exec(ctx, `INSERT INTO audit_events(organization_id,actor_user_id,action,object_type,object_id,details) VALUES($1,$2,'result.export','result',$3,$4)`, p.OrganizationID, p.UserID, object, details)
	return err
}
func exportResponse(w http.ResponseWriter, data []byte, format, name string) {
	contentType := "text/csv; charset=utf-8"
	if format == "xlsx" {
		contentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+"."+format+`"`)
	w.WriteHeader(200)
	w.Write(data)
}
func (a *App) exportResults(w http.ResponseWriter, r *http.Request, p principal) {
	format, ok := exportFormat(w, r)
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
	items, err := queryResults(r.Context(), tx, args, maxExportRows+1, 0)
	if err != nil {
		a.failure(w, err)
		return
	}
	if len(items) > maxExportRows {
		problem(w, 422, "export_limit", "Слишком много результатов. Сузьте диапазон: максимум 10 000 строк")
		return
	}
	data, err := makeExport(format, []exportSheet{summarySheet(items)}, false)
	if err == nil {
		err = a.auditExport(r.Context(), tx, p, r, format, "", len(items))
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	exportResponse(w, data, format, "results")
}
func (a *App) exportResult(w http.ResponseWriter, r *http.Request, p principal) {
	id := r.PathValue("id")
	if !uuid(id) {
		problem(w, 404, "not_found", "Результат не найден")
		return
	}
	format, ok := exportFormat(w, r)
	if !ok {
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
	data, err := makeExport(format, reportSheets(report), true)
	if err == nil {
		err = a.auditExport(r.Context(), tx, p, r, format, id, len(report.Questions))
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	exportResponse(w, data, format, "attempt-"+report.ID)
}
