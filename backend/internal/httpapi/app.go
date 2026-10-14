package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/PlatvaBestHorse/hr-testing/backend/internal/identity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type App struct {
	Pool          *pgxpool.Pool
	SecureCookie  bool
	Origins       map[string]bool
	passwordSlots chan struct{}
	dummyHash     string
}

func NewApp(pool *pgxpool.Pool, secure bool, origins []string) *App {
	dummy, _ := identity.HashPassword("unused-dummy-password-for-timing")
	a := &App{Pool: pool, SecureCookie: secure, Origins: map[string]bool{}, passwordSlots: make(chan struct{}, 4), dummyHash: dummy}
	for _, origin := range origins {
		a.Origins[strings.TrimSpace(origin)] = true
	}
	return a
}

type principal struct {
	UserID           string `json:"user_id"`
	OrganizationID   string `json:"organization_id"`
	OrganizationName string `json:"organization_name"`
	DisplayName      string `json:"display_name"`
	Email            string `json:"email"`
	Role             string `json:"role"`
	CSRF             string `json:"csrf_token"`
}

func (a *App) register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/login", a.login)
	mux.HandleFunc("POST /api/v1/auth/logout", a.protected(nil, a.logout))
	mux.HandleFunc("GET /api/v1/me", a.protected(nil, func(w http.ResponseWriter, r *http.Request, p principal) { write(w, 200, p) }))
	edit := []string{"org_admin", "recruiter"}
	mux.HandleFunc("GET /api/v1/categories", a.protected(edit, a.listCategories))
	mux.HandleFunc("POST /api/v1/categories", a.protected(edit, a.createCategory))
	mux.HandleFunc("PATCH /api/v1/categories/{id}", a.protected(edit, a.updateCategory))
	mux.HandleFunc("DELETE /api/v1/categories/{id}", a.protected(edit, a.deleteCategory))
	mux.HandleFunc("GET /api/v1/questions", a.protected(edit, a.listQuestions))
	mux.HandleFunc("POST /api/v1/questions", a.protected(edit, a.createQuestion))
	mux.HandleFunc("GET /api/v1/questions/{id}", a.protected(edit, a.getQuestion))
	mux.HandleFunc("POST /api/v1/questions/{id}/versions", a.protected(edit, a.createVersion))
	mux.HandleFunc("POST /api/v1/questions/{id}/publish", a.protected(edit, a.publishQuestion))
	mux.HandleFunc("POST /api/v1/questions/{id}/archive", a.protected(edit, a.archiveQuestion))
	mux.HandleFunc("GET /api/v1/summary", a.protected(edit, a.summary))
	mux.HandleFunc("GET /api/v1/team", a.protected([]string{"org_admin"}, a.listTeam))
	mux.HandleFunc("POST /api/v1/team", a.protected([]string{"org_admin"}, a.createMember))
	mux.HandleFunc("PATCH /api/v1/team/{id}", a.protected([]string{"org_admin"}, a.updateMember))
	mux.HandleFunc("GET /api/v1/question-versions", a.protected(edit, a.availableQuestionVersions))
	mux.HandleFunc("GET /api/v1/tests", a.protected(edit, a.listTests))
	mux.HandleFunc("POST /api/v1/tests", a.protected(edit, a.createTest))
	mux.HandleFunc("GET /api/v1/tests/{id}", a.protected(edit, a.getTest))
	mux.HandleFunc("POST /api/v1/tests/{id}/versions", a.protected(edit, a.createTestVersion))
	mux.HandleFunc("POST /api/v1/tests/{id}/publish", a.protected(edit, a.publishTest))
	mux.HandleFunc("POST /api/v1/tests/{id}/archive", a.protected(edit, a.archiveTest))
	mux.HandleFunc("GET /api/v1/candidates", a.protected(nil, a.listCandidates))
	mux.HandleFunc("POST /api/v1/candidates", a.protected(edit, a.createCandidate))
	mux.HandleFunc("GET /api/v1/candidates/{id}", a.protected(nil, a.getCandidate))
	mux.HandleFunc("PATCH /api/v1/candidates/{id}", a.protected(edit, a.updateCandidate))
	mux.HandleFunc("GET /api/v1/vacancies", a.protected(nil, a.listVacancies))
	mux.HandleFunc("POST /api/v1/vacancies", a.protected(edit, a.createVacancy))
	mux.HandleFunc("GET /api/v1/vacancies/{id}", a.protected(nil, a.getVacancy))
	mux.HandleFunc("PATCH /api/v1/vacancies/{id}", a.protected(edit, a.updateVacancy))
	mux.HandleFunc("GET /api/v1/assessors", a.protected(edit, a.listAssessors))
	mux.HandleFunc("GET /api/v1/invitations", a.protected(edit, a.listInvitations))
	mux.HandleFunc("POST /api/v1/invitations", a.protected(edit, a.issueInvitation))
	mux.HandleFunc("POST /api/v1/invitations/{id}/revoke", a.protected(edit, a.revokeInvitation))
	mux.HandleFunc("GET /api/v1/invite/{token}", a.publicInvitation)
	mux.HandleFunc("POST /api/v1/invite/{token}/start", a.candidateRoute(a.startAttempt))
	mux.HandleFunc("GET /api/v1/invite/{token}/attempt", a.candidateRoute(a.getAttempt))
	mux.HandleFunc("PUT /api/v1/invite/{token}/attempts/{id}/items/{item}/answer", a.candidateRoute(a.saveAnswer))
	mux.HandleFunc("POST /api/v1/invite/{token}/attempts/{id}/submit", a.candidateRoute(a.submitAttempt))
	mux.HandleFunc("GET /api/v1/results", a.protected(nil, a.listResults))
	mux.HandleFunc("GET /api/v1/results/export", a.protected(nil, a.exportResults))
	mux.HandleFunc("GET /api/v1/results/{id}", a.protected(nil, a.getResult))
	mux.HandleFunc("GET /api/v1/results/{id}/export", a.protected(nil, a.exportResult))
	admin := []string{"org_admin"}
	mux.HandleFunc("GET /api/v1/audit", a.protected(admin, a.listAudit))
	mux.HandleFunc("GET /api/v1/audit/options", a.protected(admin, a.auditOptions))
	mux.HandleFunc("GET /api/v1/retention", a.protected(admin, a.getRetention))
	mux.HandleFunc("PATCH /api/v1/retention", a.protected(admin, a.saveRetention))
	mux.HandleFunc("POST /api/v1/retention/preview", a.protected(admin, a.previewRetention))
	mux.HandleFunc("POST /api/v1/retention/previews/{id}/apply", a.protected(admin, a.applyRetention))
	mux.HandleFunc("GET /api/v1/retention/holds", a.protected(admin, a.retentionHolds))
	mux.HandleFunc("PUT /api/v1/candidates/{id}/retention-hold", a.protected(admin, a.setRetentionHold))
}

func problem(w http.ResponseWriter, status int, code, message string) {
	write(w, status, map[string]string{"code": code, "message": message})
}
func readJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	contentType, _, parseErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if parseErr != nil || contentType != "application/json" {
		problem(w, 415, "unsupported_media_type", "Ожидается JSON")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 512*1024)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		problem(w, 400, "invalid_json", "Проверьте формат запроса")
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		problem(w, 400, "invalid_json", "Ожидается один JSON-объект")
		return false
	}
	return true
}

func (a *App) failure(w http.ResponseWriter, err error) {
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) {
		switch pgerr.Code {
		case "23505":
			problem(w, 409, "conflict", "Такая запись уже существует")
			return
		case "23503":
			problem(w, 409, "resource_in_use", "Запись используется или связанный объект недоступен")
			return
		case "22P02":
			problem(w, 400, "invalid_id", "Некорректный идентификатор")
			return
		case "55000":
			problem(w, 409, "immutable_version", "Опубликованную версию нельзя изменить")
			return
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, 404, "not_found", "Запись не найдена")
		return
	}
	slog.Error("database request failed")
	problem(w, 500, "internal_error", "Не удалось выполнить запрос. Попробуйте ещё раз")
}

func validText(s string, max int) bool {
	return strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= max
}
func uuid(s string) bool {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return false
	}
	_, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	return err == nil
}
func digest(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }
func roleAllowed(role string) bool {
	return role == "org_admin" || role == "recruiter" || role == "assessor"
}
