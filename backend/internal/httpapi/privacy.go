package httpapi

import (
	"context"
	"errors"
	"github.com/PlatvaBestHorse/hr-testing/backend/internal/identity"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"unicode/utf8"
)

type consentDocument struct {
	ID       string `json:"id"`
	Revision int    `json:"revision"`
	Operator string `json:"operator_name"`
	Contact  string `json:"contact"`
	Consent  string `json:"consent_text"`
	Policy   string `json:"policy_text"`
	Approved bool   `json:"approved"`
}

func readConsent(ctx context.Context, tx pgx.Tx, org string) (consentDocument, error) {
	out := consentDocument{Consent: "Тестовый контур: ответы и результаты используются для проверки системы оценки кандидатов. Не вводите реальные персональные данные.", Policy: "Рабочая политика обработки персональных данных ещё не утверждена организацией."}
	err := tx.QueryRow(ctx, `SELECT id,revision,operator_name,contact,consent_text,policy_text,approved FROM consent_documents WHERE organization_id=$1 ORDER BY revision DESC LIMIT 1`, org).Scan(&out.ID, &out.Revision, &out.Operator, &out.Contact, &out.Consent, &out.Policy, &out.Approved)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return out, err
}

// Shared start lock and exclusive publication lock also cover the initial
// absence of a consent row; an old checkbox cannot accept a newly published text.
func lockConsent(ctx context.Context, tx pgx.Tx, org string, shared bool) error {
	fn := "pg_advisory_xact_lock"
	if shared {
		fn += "_shared"
	}
	_, err := tx.Exec(ctx, `SELECT `+fn+`(hashtextextended('consent-document:'||$1,0))`, org)
	return err
}
func (a *App) getPrivacy(w http.ResponseWriter, r *http.Request, p principal) {
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	out, err := readConsent(r.Context(), tx, p.OrganizationID)
	if err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, out)
}
func (a *App) savePrivacy(w http.ResponseWriter, r *http.Request, p principal) {
	var in struct {
		Operator string `json:"operator_name"`
		Contact  string `json:"contact"`
		Consent  string `json:"consent_text"`
		Policy   string `json:"policy_text"`
		Approved bool   `json:"approved"`
		Revision int    `json:"base_revision"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Operator = strings.TrimSpace(in.Operator)
	in.Contact = strings.TrimSpace(in.Contact)
	in.Consent = strings.TrimSpace(in.Consent)
	in.Policy = strings.TrimSpace(in.Policy)
	if !validText(in.Consent, 20000) || !validText(in.Policy, 20000) || utf8.RuneCountInString(in.Operator) > 250 || utf8.RuneCountInString(in.Contact) > 320 || in.Revision < 0 || (in.Approved && (!validText(in.Operator, 250) || !identity.ValidEmail(in.Contact))) {
		problem(w, 422, "invalid_privacy_document", "Укажите тексты до 20000 символов; для утверждения нужны наименование оператора и рабочая почта")
		return
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	if err = lockConsent(r.Context(), tx, p.OrganizationID, false); err == nil {
		err = lockRetentionOrg(r.Context(), tx, p.OrganizationID)
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	if !a.confirmRetentionAdmin(w, r, p, tx) {
		return
	}
	previous, err := readConsent(r.Context(), tx, p.OrganizationID)
	if err != nil {
		a.failure(w, err)
		return
	}
	if previous.Revision != in.Revision {
		problem(w, 409, "revision_conflict", "Документы изменились. Обновите страницу")
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO consent_documents(organization_id,revision,operator_name,contact,consent_text,policy_text,approved) VALUES($1,$2,$3,$4,$5,$6,$7)`, p.OrganizationID, previous.Revision+1, in.Operator, in.Contact, in.Consent, in.Policy, in.Approved)
	if err == nil {
		err = a.audit(r.Context(), tx, p, "privacy.document.publish", "organization", p.OrganizationID)
	}
	out, readErr := readConsent(r.Context(), tx, p.OrganizationID)
	if err == nil {
		err = readErr
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, out)
}
