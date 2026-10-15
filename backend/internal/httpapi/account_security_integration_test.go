package httpapi

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PlatvaBestHorse/hr-testing/backend/internal/identity"
	"github.com/PlatvaBestHorse/hr-testing/backend/internal/maildelivery"
	"github.com/jackc/pgx/v5/pgxpool"
)

func exerciseAccountSecurity(t *testing.T, ctx context.Context, owner *pgxpool.Pool, app *App, call func(string, string, any, testSession, bool) *httptest.ResponseRecorder, recruiter, other testSession, password, email string) {
	app.AuthKey = bytes.Repeat([]byte{7}, 32)
	app.PublicURL = "http://127.0.0.1:8088"
	app.MailMode = "file"
	messages := []maildelivery.Message{}
	app.SendAuthMail = func(_ context.Context, m maildelivery.Message) error { messages = append(messages, m); return nil }
	check := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			var b map[string]any
			json.Unmarshal(w.Body.Bytes(), &b)
			t.Fatalf("expected %d got %d code=%v", status, w.Code, b["code"])
		}
	}
	data := func(w *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var b map[string]any
		if json.Unmarshal(w.Body.Bytes(), &b) != nil {
			t.Fatal("invalid JSON")
		}
		return b
	}
	login := func(pass, code string) testSession {
		t.Helper()
		w := call("POST", "/api/v1/auth/login", map[string]string{"email": email, "password": pass, "second_factor": code}, testSession{}, false)
		check(w, 200)
		b := data(w)
		return testSession{cookie: w.Result().Cookies()[0], csrf: b["csrf_token"].(string), userID: b["user_id"].(string)}
	}
	clearLimits := func() {
		t.Helper()
		_, e := owner.Exec(ctx, "DELETE FROM auth_rate_limits WHERE bucket=ANY($1::text[])", []string{"email:" + hex.EncodeToString(digest(email)), "account:" + hex.EncodeToString(digest("security:"+recruiter.userID)), "account:" + hex.EncodeToString(digest("recovery:"+email)), "account-ip:security:" + hex.EncodeToString(digest("192.0.2.1")), "account-ip:recovery:" + hex.EncodeToString(digest("192.0.2.1")), "account-ip:reset:" + hex.EncodeToString(digest("192.0.2.1")), "account-ip:import:" + hex.EncodeToString(digest("192.0.2.1"))})
		if e != nil {
			t.Fatal("cannot reset fixture limit")
		}
	}
	defer clearLimits()
	tokenFromMail := func() string {
		t.Helper()
		before := len(messages)
		if e := app.DeliverAuthMail(ctx); e != nil {
			t.Fatal("delivery failed")
		}
		if len(messages) != before+1 {
			t.Fatal("expected one delivered message")
		}
		m := messages[len(messages)-1]
		if m.To != email {
			t.Fatal("wrong recipient")
		}
		for _, line := range strings.Split(m.Body, "\n") {
			if strings.Contains(line, "/reset-password#token=") {
				u, e := url.Parse(line)
				if e != nil {
					t.Fatal("invalid reset URL")
				}
				token := strings.TrimPrefix(u.Fragment, "token=")
				if !validInviteToken(token) {
					t.Fatal("invalid recovery token")
				}
				return token
			}
		}
		t.Fatal("missing recovery link")
		return ""
	}
	request := func() string {
		t.Helper()
		check(call("POST", "/api/v1/auth/recovery/request", map[string]string{"email": email}, testSession{}, false), 202)
		return tokenFromMail()
	}
	complete := func(token, pass, code string) *httptest.ResponseRecorder {
		return call("POST", "/api/v1/auth/recovery/complete", map[string]string{"token": token, "new_password": pass, "second_factor": code}, testSession{}, false)
	}
	newPassword := "Changed-" + fixtureUUID()
	recoveredPassword := "Recovered-" + fixtureUUID()
	t.Run("password_change_reauth_CSRF_and_all_session_revocation", func(t *testing.T) {
		check(call("GET", "/api/v1/security", nil, testSession{}, false), 401)
		second := login(password, "")
		body := map[string]string{"current_password": password, "new_password": newPassword}
		check(call("POST", "/api/v1/security/password", body, recruiter, false), 403)
		check(call("POST", "/api/v1/security/password", map[string]string{"current_password": "wrong", "new_password": newPassword}, recruiter, true), 401)
		body["new_password"] = "short"
		check(call("POST", "/api/v1/security/password", body, recruiter, true), 422)
		body["new_password"] = newPassword
		check(call("POST", "/api/v1/security/password", body, recruiter, true), 204)
		check(call("GET", "/api/v1/me", nil, recruiter, false), 401)
		check(call("GET", "/api/v1/me", nil, second, false), 401)
		check(call("GET", "/api/v1/me", nil, other, false), 200)
		check(call("POST", "/api/v1/auth/login", map[string]string{"email": email, "password": password}, testSession{}, false), 401)
		recruiter = login(newPassword, "")
	})
	t.Run("opaque_reset_delivery_rotation_expiry_and_atomic_single_use", func(t *testing.T) {
		unknown := call("POST", "/api/v1/auth/recovery/request", map[string]string{"email": "absent-" + email}, testSession{}, false)
		check(unknown, 202)
		known := call("POST", "/api/v1/auth/recovery/request", map[string]string{"email": email}, testSession{}, false)
		check(known, 202)
		if !bytes.Equal(unknown.Body.Bytes(), known.Body.Bytes()) {
			t.Fatal("account enumeration")
		}
		if len(messages) != 0 {
			t.Fatal("synchronous delivery")
		}
		var payload, storedHash []byte
		if e := owner.QueryRow(ctx, "SELECT m.payload,t.token_hash FROM account_tokens t JOIN account_mail m ON m.token_hash=t.token_hash WHERE t.user_id=$1", recruiter.userID).Scan(&payload, &storedHash); e != nil {
			t.Fatal("missing encrypted mail")
		}
		first := tokenFromMail()
		if !bytes.Equal(storedHash, digest(first)) || bytes.Contains(payload, []byte(first)) {
			t.Fatal("raw recovery token stored")
		}
		next := request()
		check(complete(first, recoveredPassword, ""), 401)
		if _, e := owner.Exec(ctx, "UPDATE account_tokens SET expires_at=clock_timestamp()-interval '1 second' WHERE token_hash=$1", digest(next)); e != nil {
			t.Fatal(e)
		}
		check(complete(next, recoveredPassword, ""), 401)
		clearLimits()
		token := request()
		check(complete(token, "short", ""), 422)
		var wg sync.WaitGroup
		responses := make([]*httptest.ResponseRecorder, 2)
		for n := range responses {
			wg.Add(1)
			go func(n int) { defer wg.Done(); responses[n] = complete(token, recoveredPassword, "") }(n)
		}
		wg.Wait()
		successes := 0
		for _, w := range responses {
			if w.Code == 204 {
				successes++
			} else {
				check(w, 401)
			}
		}
		if successes != 1 {
			t.Fatal("reset was not one-time")
		}
		check(call("GET", "/api/v1/me", nil, recruiter, false), 401)
		recruiter = login(recoveredPassword, "")
		check(complete(token, newPassword, ""), 401)
	})
	var secret string
	var codes []string
	t.Run("TOTP_enrollment_expiry_confirmation_and_secret_encryption", func(t *testing.T) {
		clearLimits()
		check(call("POST", "/api/v1/security/mfa/setup", map[string]string{"password": recoveredPassword}, recruiter, false), 403)
		check(call("POST", "/api/v1/security/mfa/setup", map[string]string{"password": "wrong"}, recruiter, true), 401)
		w := call("POST", "/api/v1/security/mfa/setup", map[string]string{"password": recoveredPassword}, recruiter, true)
		check(w, 200)
		first := data(w)["secret"].(string)
		check(call("POST", "/api/v1/security/mfa/confirm", map[string]string{"password": recoveredPassword, "code": "wrong"}, recruiter, true), 401)
		if _, e := owner.Exec(ctx, "UPDATE user_security SET pending_until=clock_timestamp()-interval '1 second' WHERE user_id=$1", recruiter.userID); e != nil {
			t.Fatal(e)
		}
		check(call("POST", "/api/v1/security/mfa/confirm", map[string]string{"password": recoveredPassword, "code": "000000"}, recruiter, true), 401)
		w = call("POST", "/api/v1/security/mfa/setup", map[string]string{"password": recoveredPassword}, recruiter, true)
		check(w, 200)
		secret = data(w)["secret"].(string)
		if secret == first {
			t.Fatal("pending secret not rotated")
		}
		var encrypted []byte
		if e := owner.QueryRow(ctx, "SELECT pending_ciphertext FROM user_security WHERE user_id=$1", recruiter.userID).Scan(&encrypted); e != nil || bytes.Contains(encrypted, []byte(secret)) {
			t.Fatal("TOTP seed plaintext")
		}
		otp, _ := identity.TOTP(secret, time.Now().Unix()/30)
		w = call("POST", "/api/v1/security/mfa/confirm", map[string]string{"password": recoveredPassword, "code": otp}, recruiter, true)
		check(w, 200)
		for _, code := range data(w)["recovery_codes"].([]any) {
			codes = append(codes, code.(string))
		}
		if len(codes) != 8 {
			t.Fatal("missing recovery codes")
		}
		check(call("GET", "/api/v1/me", nil, recruiter, false), 401)
		w = call("POST", "/api/v1/auth/login", map[string]string{"email": email, "password": recoveredPassword}, testSession{}, false)
		check(w, 401)
		if data(w)["code"] != "mfa_required" || len(w.Result().Cookies()) != 0 {
			t.Fatal("password-only MFA bypass")
		}
		check(call("POST", "/api/v1/auth/login", map[string]string{"email": email, "password": recoveredPassword, "second_factor": otp}, testSession{}, false), 401)
		recruiter = login(recoveredPassword, codes[0])
		check(call("POST", "/api/v1/auth/login", map[string]string{"email": email, "password": recoveredPassword, "second_factor": codes[0]}, testSession{}, false), 401)
		future, _ := identity.TOTP(secret, time.Now().Unix()/30+1)
		recruiter = login(recoveredPassword, future)
		status := call("GET", "/api/v1/security", nil, recruiter, false)
		check(status, 200)
		if data(status)["recovery_codes_remaining"] != float64(7) {
			t.Fatal("code not consumed")
		}
	})
	t.Run("regeneration_and_password_recovery_cannot_bypass_MFA", func(t *testing.T) {
		clearLimits()
		w := call("POST", "/api/v1/security/mfa/recovery-codes", map[string]string{"password": recoveredPassword, "second_factor": codes[1]}, recruiter, true)
		check(w, 200)
		old := codes
		codes = nil
		for _, code := range data(w)["recovery_codes"].([]any) {
			codes = append(codes, code.(string))
		}
		check(call("POST", "/api/v1/auth/login", map[string]string{"email": email, "password": recoveredPassword, "second_factor": old[2]}, testSession{}, false), 401)
		recruiter = login(recoveredPassword, codes[0])
		token := request()
		w = complete(token, newPassword, "")
		check(w, 401)
		if data(w)["code"] != "mfa_required" {
			t.Fatal("reset did not require MFA")
		}
		check(complete(token, newPassword, "wrong"), 401)
		check(complete(token, newPassword, codes[1]), 204)
		check(call("POST", "/api/v1/auth/login", map[string]string{"email": email, "password": newPassword}, testSession{}, false), 401)
		recruiter = login(newPassword, codes[2])
		check(call("POST", "/api/v1/security/mfa/disable", map[string]string{"password": newPassword, "second_factor": "wrong"}, recruiter, true), 401)
		check(call("POST", "/api/v1/security/mfa/disable", map[string]string{"password": newPassword, "second_factor": codes[3]}, recruiter, true), 200)
		recruiter = login(newPassword, "")
		w = call("GET", "/api/v1/security", nil, recruiter, false)
		check(w, 200)
		if data(w)["mfa_enabled"] != false {
			t.Fatal("MFA not disabled")
		}
	})
	t.Run("CLI_password_change_revokes_recovery_links", func(t *testing.T) {
		clearLimits()
		token := request()
		if e := identity.ResetPassword(ctx, owner, email, recoveredPassword); e != nil {
			t.Fatal("CLI reset failed")
		}
		check(complete(token, newPassword, ""), 401)
		recruiter = login(recoveredPassword, "")
		check(call("GET", "/api/v1/me", nil, other, false), 200)
	})
}
