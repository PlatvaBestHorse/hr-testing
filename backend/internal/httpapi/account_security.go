package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PlatvaBestHorse/hr-testing/backend/internal/identity"
	"github.com/PlatvaBestHorse/hr-testing/backend/internal/maildelivery"
	"github.com/jackc/pgx/v5"
)

var errSecondFactor = errors.New("second factor rejected")
var errAuthKey = errors.New("account encryption unavailable")

func (a *App) authOptions(w http.ResponseWriter, r *http.Request) {
	write(w, 200, map[string]any{"recovery_available": a.SendAuthMail != nil && len(a.AuthKey) == 32, "local_mail": a.MailMode == "file"})
}
func (a *App) getSecurity(w http.ResponseWriter, r *http.Request, p principal) {
	var enabled bool
	var count int
	err := a.Pool.QueryRow(r.Context(), `SELECT u.mfa_enabled,(SELECT count(*) FROM mfa_recovery_codes WHERE user_id=u.id) FROM users u WHERE u.id=$1`, p.UserID).Scan(&enabled, &count)
	if err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, map[string]any{"mfa_enabled": enabled, "recovery_codes_remaining": count, "mfa_available": len(a.AuthKey) == 32})
}

func (a *App) accountSlot(w http.ResponseWriter, r *http.Request, p principal) bool {
	select {
	case a.passwordSlots <- struct{}{}:
	default:
		problem(w, 429, "rate_limited", "Повторите позже")
		return false
	}
	ip := a.clientIP(r)
	if err := a.limitAccount(r.Context(), "security:"+p.UserID, ip, 15, 90); err != nil {
		<-a.passwordSlots
		if errors.Is(err, errLimited) {
			problem(w, 429, "rate_limited", "Повторите через 15 минут")
		} else {
			a.failure(w, err)
		}
		return false
	}
	return true
}

func (a *App) limitAccount(ctx context.Context, subject, ip string, perAccount, perIP int) error {
	// Different operations have different budgets. MFA/password settings must
	// not consume the smaller password-recovery IP budget for an entire office.
	family, _, _ := strings.Cut(subject, ":")
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	limited := false
	for _, b := range []struct {
		key string
		max int
	}{{"account:" + hex.EncodeToString(digest(subject)), perAccount}, {"account-ip:" + family + ":" + hex.EncodeToString(digest(ip)), perIP}} {
		var n int
		err = tx.QueryRow(ctx, `INSERT INTO auth_rate_limits(bucket) VALUES($1) ON CONFLICT(bucket) DO UPDATE SET
  attempts=CASE WHEN auth_rate_limits.window_started_at<clock_timestamp()-interval '15 minutes' THEN 1 ELSE auth_rate_limits.attempts+1 END,
  window_started_at=CASE WHEN auth_rate_limits.window_started_at<clock_timestamp()-interval '15 minutes' THEN clock_timestamp() ELSE auth_rate_limits.window_started_at END RETURNING attempts`, b.key).Scan(&n)
		if err != nil {
			return err
		}
		if n > b.max {
			limited = true
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if limited {
		return errLimited
	}
	return nil
}

// Lock user before sessions/membership and recheck the session after waiting.
// Password reset, MFA, login and CLI password changes serialize on this row.
func (a *App) reauthenticate(ctx context.Context, tx pgx.Tx, r *http.Request, p principal, password string) (bool, error) {
	var hash, status string
	var enabled bool
	err := tx.QueryRow(ctx, "SELECT password_hash,status,mfa_enabled FROM users WHERE id=$1 FOR UPDATE", p.UserID).Scan(&hash, &status, &enabled)
	if err != nil {
		return false, err
	}
	if status != "active" || !identity.VerifyPassword(password, hash) {
		return false, errSecondFactor
	}
	cookie, err := r.Cookie("hr_session")
	if err != nil {
		return false, errSecondFactor
	}
	var role string
	err = tx.QueryRow(ctx, `SELECT m.role FROM auth_sessions s JOIN memberships m ON m.user_id=s.user_id AND m.organization_id=s.organization_id JOIN organizations o ON o.id=m.organization_id
 WHERE s.token_hash=$1 AND s.user_id=$2 AND s.organization_id=$3 AND s.expires_at>clock_timestamp() AND m.status='active' AND o.status='active' FOR SHARE OF m`, digest(cookie.Value), p.UserID, p.OrganizationID).Scan(&role)
	if err != nil {
		return false, errSecondFactor
	}
	return enabled, nil
}

// Caller holds users FOR UPDATE. Every accepted TOTP counter is single-use
// across login, password changes and recovery; backup codes are DELETE RETURNING.
func (a *App) consumeFactor(ctx context.Context, tx pgx.Tx, userID, code string) error {
	code = strings.TrimSpace(code)
	if len(code) == 6 {
		var encrypted []byte
		var last int64
		var now time.Time
		if err := tx.QueryRow(ctx, "SELECT secret_ciphertext,last_step,clock_timestamp() FROM user_security WHERE user_id=$1", userID).Scan(&encrypted, &last, &now); err != nil {
			return errSecondFactor
		}
		secret, err := identity.Decrypt(a.AuthKey, "totp:"+userID, encrypted)
		if err != nil {
			return errAuthKey
		}
		step, ok := identity.MatchTOTP(string(secret), code, now, last)
		if !ok {
			return errSecondFactor
		}
		_, err = tx.Exec(ctx, "UPDATE user_security SET last_step=$2 WHERE user_id=$1", userID, step)
		return err
	}
	normalized := identity.NormalizeRecovery(code)
	if len(normalized) != 16 {
		return errSecondFactor
	}
	tag, err := tx.Exec(ctx, "DELETE FROM mfa_recovery_codes WHERE user_id=$1 AND code_hash=$2", userID, digest(normalized))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errSecondFactor
	}
	return nil
}
func (a *App) accountFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, errSecondFactor) || errors.Is(err, pgx.ErrNoRows) {
		problem(w, 401, "verification_failed", "Пароль, код или сессия недействительны")
		return
	}
	if errors.Is(err, errAuthKey) {
		problem(w, 503, "security_unavailable", "Защита аккаунта временно недоступна. Обратитесь к оператору")
		return
	}
	a.failure(w, err)
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func (a *App) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: "hr_session", Value: "", Path: "/", HttpOnly: true, Secure: a.SecureCookie, SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(1, 0)})
}

func (a *App) changePassword(w http.ResponseWriter, r *http.Request, p principal) {
	var in struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
		Code    string `json:"second_factor"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !a.accountSlot(w, r, p) {
		return
	}
	defer func() { <-a.passwordSlots }()
	hash, err := identity.HashPassword(in.New)
	if err != nil || in.New == in.Current {
		problem(w, 422, "invalid_password", "Нужен новый пароль от 12 символов и до 1024 байт")
		return
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	enabled, err := a.reauthenticate(r.Context(), tx, r, p, in.Current)
	if err == nil && enabled {
		err = a.consumeFactor(r.Context(), tx, p.UserID, in.Code)
	}
	if err != nil {
		a.accountFailure(w, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "UPDATE users SET password_hash=$2,updated_at=clock_timestamp() WHERE id=$1", p.UserID, hash); err == nil {
		err = clearAccountAccess(r.Context(), tx, p.UserID)
	}
	if err == nil {
		err = a.audit(r.Context(), tx, p, "auth.password.change", "user", p.UserID)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	a.clearSession(w)
	w.WriteHeader(204)
}
func clearAccountAccess(ctx context.Context, tx pgx.Tx, id string) error {
	if _, err := tx.Exec(ctx, "DELETE FROM auth_sessions WHERE user_id=$1", id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "DELETE FROM account_tokens WHERE user_id=$1", id); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, "UPDATE user_security SET pending_ciphertext=NULL,pending_until=NULL WHERE user_id=$1", id)
	return err
}

func (a *App) setupMFA(w http.ResponseWriter, r *http.Request, p principal) {
	var in struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if len(a.AuthKey) != 32 {
		a.accountFailure(w, errAuthKey)
		return
	}
	if !a.accountSlot(w, r, p) {
		return
	}
	defer func() { <-a.passwordSlots }()
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	enabled, err := a.reauthenticate(r.Context(), tx, r, p, in.Password)
	if err != nil {
		a.accountFailure(w, err)
		return
	}
	if enabled {
		problem(w, 409, "mfa_enabled", "Двухфакторная защита уже включена")
		return
	}
	secret, err := identity.NewTOTPSecret()
	if err != nil {
		a.failure(w, err)
		return
	}
	cipher, err := identity.Encrypt(a.AuthKey, "pending:"+p.UserID, []byte(secret))
	if err != nil {
		a.accountFailure(w, errAuthKey)
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO user_security(user_id,pending_ciphertext,pending_until) VALUES($1,$2,clock_timestamp()+interval '10 minutes') ON CONFLICT(user_id) DO UPDATE SET pending_ciphertext=excluded.pending_ciphertext,pending_until=excluded.pending_until`, p.UserID, cipher)
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	label := "Кадровый тестер:" + p.Email
	uri := "otpauth://totp/" + url.PathEscape(label) + "?" + url.Values{"secret": {secret}, "issuer": {"Кадровый тестер"}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}.Encode()
	write(w, 200, map[string]any{"secret": secret, "otpauth_uri": uri, "expires_in": 600})
}

func saveRecoveryCodes(ctx context.Context, tx pgx.Tx, id string) ([]string, error) {
	codes, err := identity.NewRecoveryCodes()
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM mfa_recovery_codes WHERE user_id=$1", id); err != nil {
		return nil, err
	}
	for _, code := range codes {
		if _, err = tx.Exec(ctx, "INSERT INTO mfa_recovery_codes(user_id,code_hash) VALUES($1,$2)", id, digest(identity.NormalizeRecovery(code))); err != nil {
			return nil, err
		}
	}
	return codes, nil
}

func (a *App) confirmMFA(w http.ResponseWriter, r *http.Request, p principal) {
	var in struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !a.accountSlot(w, r, p) {
		return
	}
	defer func() { <-a.passwordSlots }()
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	enabled, err := a.reauthenticate(r.Context(), tx, r, p, in.Password)
	if err != nil {
		a.accountFailure(w, err)
		return
	}
	if enabled {
		problem(w, 409, "mfa_enabled", "Защита уже включена")
		return
	}
	var encrypted []byte
	var now time.Time
	err = tx.QueryRow(r.Context(), "SELECT pending_ciphertext,clock_timestamp() FROM user_security WHERE user_id=$1 AND pending_until>clock_timestamp()", p.UserID).Scan(&encrypted, &now)
	if err != nil {
		a.accountFailure(w, errSecondFactor)
		return
	}
	secret, err := identity.Decrypt(a.AuthKey, "pending:"+p.UserID, encrypted)
	if err != nil {
		a.accountFailure(w, errAuthKey)
		return
	}
	step, ok := identity.MatchTOTP(string(secret), in.Code, now, -1)
	if !ok {
		a.accountFailure(w, errSecondFactor)
		return
	}
	active, err := identity.Encrypt(a.AuthKey, "totp:"+p.UserID, secret)
	if err != nil {
		a.accountFailure(w, errAuthKey)
		return
	}
	_, err = tx.Exec(r.Context(), "UPDATE user_security SET secret_ciphertext=$2,last_step=$3,pending_ciphertext=NULL,pending_until=NULL WHERE user_id=$1", p.UserID, active, step)
	if err == nil {
		_, err = tx.Exec(r.Context(), "UPDATE users SET mfa_enabled=true,updated_at=clock_timestamp() WHERE id=$1", p.UserID)
	}
	var codes []string
	if err == nil {
		codes, err = saveRecoveryCodes(r.Context(), tx, p.UserID)
	}
	if err == nil {
		err = clearAccountAccess(r.Context(), tx, p.UserID)
	}
	if err == nil {
		err = a.audit(r.Context(), tx, p, "auth.mfa.enable", "user", p.UserID)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	a.clearSession(w)
	write(w, 200, map[string]any{"recovery_codes": codes, "sign_in_required": true})
}

func (a *App) disableMFA(w http.ResponseWriter, r *http.Request, p principal) {
	a.modifyMFA(w, r, p, false)
}
func (a *App) regenerateCodes(w http.ResponseWriter, r *http.Request, p principal) {
	a.modifyMFA(w, r, p, true)
}
func (a *App) modifyMFA(w http.ResponseWriter, r *http.Request, p principal, regenerate bool) {
	var in struct {
		Password string `json:"password"`
		Code     string `json:"second_factor"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !a.accountSlot(w, r, p) {
		return
	}
	defer func() { <-a.passwordSlots }()
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	enabled, err := a.reauthenticate(r.Context(), tx, r, p, in.Password)
	if err == nil && !enabled {
		err = errSecondFactor
	}
	if err == nil {
		err = a.consumeFactor(r.Context(), tx, p.UserID, in.Code)
	}
	if err != nil {
		a.accountFailure(w, err)
		return
	}
	var codes []string
	action := "auth.mfa.disable"
	if regenerate {
		action = "auth.mfa.recovery_codes"
		codes, err = saveRecoveryCodes(r.Context(), tx, p.UserID)
	} else {
		_, err = tx.Exec(r.Context(), "UPDATE users SET mfa_enabled=false,updated_at=clock_timestamp() WHERE id=$1", p.UserID)
		if err == nil {
			_, err = tx.Exec(r.Context(), "DELETE FROM user_security WHERE user_id=$1", p.UserID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), "DELETE FROM mfa_recovery_codes WHERE user_id=$1", p.UserID)
		}
	}
	if err == nil {
		err = clearAccountAccess(r.Context(), tx, p.UserID)
	}
	if err == nil {
		err = a.audit(r.Context(), tx, p, action, "user", p.UserID)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	a.clearSession(w)
	write(w, 200, map[string]any{"recovery_codes": codes, "sign_in_required": true})
}

func (a *App) requestPasswordRecovery(w http.ResponseWriter, r *http.Request) {
	if !a.originAllowed(r) {
		problem(w, 403, "origin_rejected", "Источник запроса не разрешён")
		return
	}
	var in struct {
		Email string `json:"email"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	if !identity.ValidEmail(in.Email) {
		problem(w, 422, "invalid_email", "Проверьте адрес почты")
		return
	}
	if a.SendAuthMail == nil || len(a.AuthKey) != 32 {
		problem(w, 503, "recovery_unavailable", "Восстановление по почте не настроено")
		return
	}
	ip := a.clientIP(r)
	if err := a.limitAccount(r.Context(), "recovery:"+in.Email, ip, 3, 20); err != nil {
		if errors.Is(err, errLimited) {
			problem(w, 429, "rate_limited", "Повторите через 15 минут")
		} else {
			a.failure(w, err)
		}
		return
	}
	token, err := randomToken()
	if err != nil {
		a.failure(w, err)
		return
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	var id, hash, org string
	err = tx.QueryRow(r.Context(), `SELECT u.id,u.password_hash,m.organization_id FROM users u JOIN memberships m ON m.user_id=u.id JOIN organizations o ON o.id=m.organization_id WHERE lower(u.email)=$1 AND u.status='active' AND m.status='active' AND o.status='active' ORDER BY m.created_at,m.organization_id LIMIT 1 FOR UPDATE OF u`, in.Email).Scan(&id, &hash, &org)
	if errors.Is(err, pgx.ErrNoRows) {
		write(w, 202, map[string]string{"message": "Если аккаунт доступен, письмо со ссылкой будет отправлено"})
		return
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	_, err = tx.Exec(r.Context(), "DELETE FROM account_tokens WHERE user_id=$1 AND purpose='reset'", id)
	if err == nil {
		err = a.queueAccountToken(r.Context(), tx, id, org, in.Email, hash, token, "reset", 15*time.Minute)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	write(w, 202, map[string]string{"message": "Если аккаунт доступен, письмо со ссылкой будет отправлено"})
}

func (a *App) queueAccountToken(ctx context.Context, tx pgx.Tx, id, org, email, hash, token, purpose string, valid time.Duration) error {
	link := a.PublicURL + "/reset-password#token=" + token
	subject := "Восстановление доступа"
	if purpose == "staff_invite" {
		subject = "Приглашение в Кадровый тестер"
	}
	message := maildelivery.Message{To: email, Subject: subject, Body: subject + "\n\nОткройте ссылку и задайте новый пароль:\n" + link + "\n\nСрок ссылки: " + fmt.Sprint(int(valid.Minutes())) + " мин. Если вы не запрашивали письмо, проигнорируйте его."}
	b, err := json.Marshal(message)
	if err != nil {
		return err
	}
	encrypted, err := identity.Encrypt(a.AuthKey, "mail:"+hex.EncodeToString(digest(token)), b)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO account_tokens(token_hash,user_id,organization_id,purpose,password_binding,expires_at) VALUES($1,$2,$3,$4,$5,clock_timestamp()+$6::interval)", digest(token), id, org, purpose, digest(hash), fmt.Sprintf("%f seconds", valid.Seconds())); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO account_mail(token_hash,payload) VALUES($1,$2)", digest(token), encrypted)
	return err
}

func (a *App) completePasswordRecovery(w http.ResponseWriter, r *http.Request) {
	if !a.originAllowed(r) {
		problem(w, 403, "origin_rejected", "Источник запроса не разрешён")
		return
	}
	var in struct {
		Token    string `json:"token"`
		Password string `json:"new_password"`
		Code     string `json:"second_factor"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !validInviteToken(in.Token) {
		problem(w, 400, "invalid_token", "Ссылка недействительна")
		return
	}
	select {
	case a.passwordSlots <- struct{}{}:
		defer func() { <-a.passwordSlots }()
	default:
		problem(w, 429, "rate_limited", "Повторите позже")
		return
	}
	ip := a.clientIP(r)
	if err := a.limitAccount(r.Context(), "reset:"+hex.EncodeToString(digest(in.Token)), ip, 8, 40); err != nil {
		if errors.Is(err, errLimited) {
			problem(w, 429, "rate_limited", "Повторите через 15 минут")
		} else {
			a.failure(w, err)
		}
		return
	}
	hash, err := identity.HashPassword(in.Password)
	if err != nil {
		problem(w, 422, "invalid_password", "Пароль от 12 символов и до 1024 байт")
		return
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	var id, org, purpose string
	var binding []byte
	err = tx.QueryRow(r.Context(), "SELECT user_id FROM account_tokens WHERE token_hash=$1", digest(in.Token)).Scan(&id)
	if err != nil {
		a.accountFailure(w, errSecondFactor)
		return
	}
	var current, status string
	var enabled bool
	err = tx.QueryRow(r.Context(), "SELECT password_hash,status,mfa_enabled FROM users WHERE id=$1 FOR UPDATE", id).Scan(&current, &status, &enabled)
	if err != nil {
		a.failure(w, err)
		return
	}
	err = tx.QueryRow(r.Context(), "SELECT organization_id,purpose,password_binding FROM account_tokens WHERE user_id=$1 AND token_hash=$2 AND expires_at>clock_timestamp() FOR UPDATE", id, digest(in.Token)).Scan(&org, &purpose, &binding)
	if err != nil || status != "active" || !bytesEqual(binding, digest(current)) {
		a.accountFailure(w, errSecondFactor)
		return
	}
	expected := "active"
	if purpose == "staff_invite" {
		expected = "invited"
	}
	var role string
	err = tx.QueryRow(r.Context(), `SELECT m.role FROM memberships m JOIN organizations o ON o.id=m.organization_id WHERE m.user_id=$1 AND m.organization_id=$2 AND m.status=$3 AND o.status='active' FOR UPDATE OF m`, id, org, expected).Scan(&role)
	if err != nil {
		a.accountFailure(w, errSecondFactor)
		return
	}
	if enabled {
		if in.Code == "" {
			problem(w, 401, "mfa_required", "Введите код приложения или резервный код")
			return
		}
		if err = a.consumeFactor(r.Context(), tx, id, in.Code); err != nil {
			a.accountFailure(w, err)
			return
		}
	}
	_, err = tx.Exec(r.Context(), "UPDATE users SET password_hash=$2,updated_at=clock_timestamp() WHERE id=$1", id, hash)
	if err == nil && purpose == "staff_invite" {
		_, err = tx.Exec(r.Context(), "UPDATE memberships SET status='active' WHERE user_id=$1 AND organization_id=$2", id, org)
	}
	if err == nil {
		err = clearAccountAccess(r.Context(), tx, id)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO audit_events(organization_id,action,object_type,object_id) SELECT organization_id,'auth.password.recover','user',user_id FROM memberships WHERE user_id=$1`, id)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	a.clearSession(w)
	w.WriteHeader(204)
}
func bytesEqual(a, b []byte) bool { return len(a) == len(b) && subtle.ConstantTimeCompare(a, b) == 1 }

// Delivery is outside the request path and never takes the user lock. A reset
// racing with delivery may invalidate a link; it cannot revive a consumed token.
func (a *App) DeliverAuthMail(ctx context.Context) error {
	if _, err := a.Pool.Exec(ctx, "DELETE FROM account_tokens WHERE expires_at<clock_timestamp()-interval '1 day'"); err != nil {
		return err
	}
	if _, err := a.Pool.Exec(ctx, "UPDATE user_security SET pending_ciphertext=NULL,pending_until=NULL WHERE pending_until<clock_timestamp()"); err != nil {
		return err
	}
	if a.SendAuthMail == nil {
		return nil
	}
	for n := 0; n < 5; n++ {
		tx, err := a.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		var id string
		var hash, payload []byte
		var attempts int
		err = tx.QueryRow(ctx, `SELECT id,token_hash,payload,attempts FROM account_mail WHERE status IN ('pending','failed') AND available_at<=clock_timestamp() AND attempts<5 ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&id, &hash, &payload, &attempts)
		if errors.Is(err, pgx.ErrNoRows) {
			tx.Rollback(context.Background())
			return nil
		}
		if err != nil {
			tx.Rollback(context.Background())
			return err
		}
		var valid bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM account_tokens t JOIN users u ON u.id=t.user_id JOIN organizations o ON o.id=t.organization_id JOIN memberships m ON m.organization_id=t.organization_id AND m.user_id=t.user_id WHERE t.token_hash=$1 AND t.expires_at>clock_timestamp() AND u.status='active' AND o.status='active' AND m.status=CASE WHEN t.purpose='staff_invite' THEN 'invited' ELSE 'active' END)`, hash).Scan(&valid)
		if err != nil {
			tx.Rollback(context.Background())
			return err
		}
		delivered := false
		if valid {
			plain, e := identity.Decrypt(a.AuthKey, "mail:"+hex.EncodeToString(hash), payload)
			var m maildelivery.Message
			if e == nil && json.Unmarshal(plain, &m) == nil {
				delivered = a.SendAuthMail(ctx, m) == nil
			}
		}
		if !valid {
			_, err = tx.Exec(ctx, "UPDATE account_mail SET status='cancelled',payload=NULL WHERE id=$1", id)
		} else if delivered {
			_, err = tx.Exec(ctx, "UPDATE account_mail SET status='sent',payload=NULL,attempts=attempts+1 WHERE id=$1", id)
		} else {
			_, err = tx.Exec(ctx, "UPDATE account_mail SET status='failed',attempts=attempts+1,available_at=clock_timestamp()+interval '1 minute' WHERE id=$1", id)
		}
		if err == nil {
			err = tx.Commit(ctx)
		}
		tx.Rollback(context.Background())
		if err != nil {
			return err
		}
	}
	return nil
}
