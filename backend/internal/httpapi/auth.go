package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/PlatvaBestHorse/hr-testing/backend/internal/identity"
	"github.com/jackc/pgx/v5"
)

func (a *App) originAllowed(r *http.Request) bool {
	return a.Origins[r.Header.Get("Origin")] && r.Header.Get("Sec-Fetch-Site") != "cross-site"
}

func (a *App) protected(roles []string, next func(http.ResponseWriter, *http.Request, principal)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("hr_session")
		if err != nil || len(cookie.Value) != 43 {
			problem(w, 401, "unauthorized", "Войдите в систему")
			return
		}
		var p principal
		err = a.Pool.QueryRow(r.Context(), `SELECT u.id,s.organization_id,o.name,u.display_name,u.email,m.role,s.csrf_token
			FROM auth_sessions s JOIN users u ON u.id=s.user_id
			JOIN memberships m ON m.user_id=s.user_id AND m.organization_id=s.organization_id
			JOIN organizations o ON o.id=s.organization_id
			WHERE s.token_hash=$1 AND s.expires_at>now() AND u.status='active' AND m.status='active' AND o.status='active'`, digest(cookie.Value)).Scan(&p.UserID, &p.OrganizationID, &p.OrganizationName, &p.DisplayName, &p.Email, &p.Role, &p.CSRF)
		if err == pgx.ErrNoRows {
			problem(w, 401, "unauthorized", "Сессия завершена. Войдите снова")
			return
		}
		if err != nil {
			a.failure(w, err)
			return
		}
		if len(roles) > 0 {
			allowed := false
			for _, role := range roles {
				if role == p.Role {
					allowed = true
				}
			}
			if !allowed {
				problem(w, 403, "forbidden", "Недостаточно прав для этого действия")
				return
			}
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			if !a.originAllowed(r) || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(p.CSRF)) != 1 {
				problem(w, 403, "csrf_failed", "Запрос не подтверждён. Обновите страницу")
				return
			}
		}
		next(w, r, p)
	}
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if !a.originAllowed(r) {
		problem(w, 403, "origin_rejected", "Источник запроса не разрешён")
		return
	}
	var in struct {
		Email            string `json:"email"`
		Password         string `json:"password"`
		OrganizationSlug string `json:"organization_slug"`
		SecondFactor     string `json:"second_factor"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	if !identity.ValidEmail(in.Email) || len(in.Password) > 1024 || len(in.OrganizationSlug) > 80 || len(in.SecondFactor) > 64 {
		problem(w, 400, "invalid_login", "Проверьте адрес почты и пароль")
		return
	}
	select {
	case a.passwordSlots <- struct{}{}:
		defer func() { <-a.passwordSlots }()
	default:
		problem(w, 429, "rate_limited", "Слишком много попыток входа. Повторите позже")
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if err := a.limitLogin(r.Context(), in.Email, ip); err != nil {
		if err == errLimited {
			w.Header().Set("Retry-After", "900")
			problem(w, 429, "rate_limited", "Слишком много попыток входа. Повторите через 15 минут")
		} else {
			a.failure(w, err)
		}
		return
	}
	var p principal
	var hash string
	err := a.Pool.QueryRow(r.Context(), `SELECT u.id,m.organization_id,o.name,u.display_name,u.email,m.role,u.password_hash
		FROM users u JOIN memberships m ON m.user_id=u.id JOIN organizations o ON o.id=m.organization_id
		WHERE lower(u.email)=$1 AND u.status='active' AND m.status='active' AND o.status='active'
		AND ($2='' OR o.slug=$2) ORDER BY m.created_at,m.organization_id LIMIT 1`, in.Email, in.OrganizationSlug).Scan(&p.UserID, &p.OrganizationID, &p.OrganizationName, &p.DisplayName, &p.Email, &p.Role, &hash)
	if err != nil && err != pgx.ErrNoRows {
		a.failure(w, err)
		return
	}
	if err == pgx.ErrNoRows {
		hash = a.dummyHash
	}
	verified := identity.VerifyPassword(in.Password, hash)
	if err == pgx.ErrNoRows || !verified {
		problem(w, 401, "invalid_credentials", "Неверная почта или пароль")
		return
	}
	tokenBytes := make([]byte, 32)
	csrfBytes := make([]byte, 32)
	rand.Read(tokenBytes)
	rand.Read(csrfBytes)
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	p.CSRF = hex.EncodeToString(csrfBytes)
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	// Serialize with password resets: a password verified before a reset must not
	// create a new session after that reset has revoked the existing sessions.
	var currentHash, userStatus string
	var mfaEnabled bool
	err = tx.QueryRow(r.Context(), `SELECT password_hash,status,mfa_enabled FROM users WHERE id=$1 FOR UPDATE`, p.UserID).Scan(&currentHash, &userStatus, &mfaEnabled)
	if err != nil {
		a.failure(w, err)
		return
	}
	if currentHash != hash || userStatus != "active" {
		problem(w, 401, "invalid_credentials", "Неверная почта или пароль")
		return
	}
	// Recheck access under a membership lock; role changes revoke sessions under the same lock.
	var activeRole string
	err = tx.QueryRow(r.Context(), `SELECT role FROM memberships WHERE organization_id=$1 AND user_id=$2 AND status='active' FOR UPDATE`, p.OrganizationID, p.UserID).Scan(&activeRole)
	if err == pgx.ErrNoRows {
		problem(w, 401, "invalid_credentials", "Неверная почта или пароль")
		return
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	p.Role = activeRole
	if mfaEnabled {
		if in.SecondFactor == "" {
			problem(w, 401, "mfa_required", "Введите код приложения или резервный код")
			return
		}
		if err = a.consumeFactor(r.Context(), tx, p.UserID, in.SecondFactor); err != nil {
			a.accountFailure(w, err)
			return
		}
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM auth_sessions WHERE expires_at <= now()`); err != nil {
		a.failure(w, err)
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO auth_sessions(token_hash,organization_id,user_id,csrf_token,expires_at) VALUES ($1,$2,$3,$4,now()+interval '8 hours')`, digest(token), p.OrganizationID, p.UserID, p.CSRF); err != nil {
		a.failure(w, err)
		return
	}
	if err = a.audit(r.Context(), tx, p, "auth.login", "user", p.UserID); err != nil {
		a.failure(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		a.failure(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "hr_session", Value: token, Path: "/", HttpOnly: true, Secure: a.SecureCookie, SameSite: http.SameSiteLaxMode, MaxAge: 8 * 60 * 60, Expires: time.Now().Add(8 * time.Hour)})
	write(w, 200, p)
}

type limitError struct{}

func (limitError) Error() string { return "login rate limited" }

var errLimited error = limitError{}

func (a *App) limitLogin(ctx context.Context, email, ip string) error {
	// The buckets contain hashes, never raw passwords or email addresses.
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	limited := false
	for _, bucket := range []struct {
		value string
		max   int
	}{{"email:" + hex.EncodeToString(digest(email)), 10}, {"ip:" + hex.EncodeToString(digest(ip)), 60}} {
		var count int
		err = tx.QueryRow(ctx, `INSERT INTO auth_rate_limits(bucket) VALUES($1) ON CONFLICT(bucket) DO UPDATE SET
		 attempts=CASE WHEN auth_rate_limits.window_started_at < now()-interval '15 minutes' THEN 1 ELSE auth_rate_limits.attempts+1 END,
		 window_started_at=CASE WHEN auth_rate_limits.window_started_at < now()-interval '15 minutes' THEN now() ELSE auth_rate_limits.window_started_at END RETURNING attempts`, bucket.value).Scan(&count)
		if err != nil {
			return err
		}
		if count > bucket.max {
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

func (a *App) logout(w http.ResponseWriter, r *http.Request, p principal) {
	cookie, _ := r.Cookie("hr_session")
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(r.Context(), `DELETE FROM auth_sessions WHERE token_hash=$1 AND organization_id=$2`, digest(cookie.Value), p.OrganizationID); err != nil {
		a.failure(w, err)
		return
	}
	if err = a.audit(r.Context(), tx, p, "auth.logout", "user", p.UserID); err != nil {
		a.failure(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		a.failure(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "hr_session", Value: "", Path: "/", HttpOnly: true, Secure: a.SecureCookie, SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(1, 0)})
	w.WriteHeader(204)
}

func (a *App) audit(ctx context.Context, tx pgx.Tx, p principal, action, kind, id string) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit_events(organization_id,actor_user_id,action,object_type,object_id) VALUES ($1,$2,$3,$4,$5)`, p.OrganizationID, p.UserID, action, kind, id)
	return err
}
