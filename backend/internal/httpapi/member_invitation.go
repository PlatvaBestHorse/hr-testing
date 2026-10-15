package httpapi

import (
	"context"
	"net/http"
	"time"
)

func (a *App) resendMemberInvitation(w http.ResponseWriter, r *http.Request, p principal) {
	id := r.PathValue("id")
	if !uuid(id) {
		problem(w, 404, "not_found", "Сотрудник не найден")
		return
	}
	if a.SendAuthMail == nil || len(a.AuthKey) != 32 {
		problem(w, 503, "mail_unavailable", "Отправка приглашений не настроена")
		return
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(r.Context(), "SELECT id FROM organizations WHERE id=$1 FOR NO KEY UPDATE", p.OrganizationID); err != nil {
		a.failure(w, err)
		return
	}
	// Only pending or disabled members of this organization are eligible. Lock
	// user before membership, matching redemption/password operations.
	var userID string
	err = tx.QueryRow(r.Context(), "SELECT user_id FROM memberships WHERE organization_id=$1 AND user_id=$2 AND status IN ('invited','disabled')", p.OrganizationID, id).Scan(&userID)
	if err != nil {
		a.failure(w, err)
		return
	}
	var hash, email, status string
	err = tx.QueryRow(r.Context(), "SELECT password_hash,email,status FROM users WHERE id=$1 FOR UPDATE", id).Scan(&hash, &email, &status)
	if err != nil {
		a.failure(w, err)
		return
	}
	if status != "active" {
		problem(w, 409, "user_unavailable", "Аккаунт недоступен")
		return
	}
	var memberStatus string
	err = tx.QueryRow(r.Context(), "SELECT status FROM memberships WHERE organization_id=$1 AND user_id=$2 FOR UPDATE", p.OrganizationID, id).Scan(&memberStatus)
	if err != nil {
		a.failure(w, err)
		return
	}
	if memberStatus == "active" {
		problem(w, 409, "member_active", "Сотрудник уже активен")
		return
	}
	// A disabled existing member must be reactivated by normal role management;
	// an invitation must never change the password of an established account.
	if memberStatus != "invited" {
		problem(w, 409, "member_disabled", "Включите доступ существующему сотруднику через управление правами")
		return
	}
	if !a.confirmRetentionAdmin(w, r, p, tx) {
		return
	}
	if _, err = tx.Exec(r.Context(), "DELETE FROM account_tokens WHERE user_id=$1 AND organization_id=$2 AND purpose='staff_invite'", id, p.OrganizationID); err != nil {
		a.failure(w, err)
		return
	}
	token, err := randomToken()
	if err == nil {
		err = a.queueAccountToken(r.Context(), tx, id, p.OrganizationID, email, hash, token, "staff_invite", 48*time.Hour)
	}
	if err == nil {
		err = a.audit(r.Context(), tx, p, "team.member.reinvite", "user", id)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	w.WriteHeader(204)
}
