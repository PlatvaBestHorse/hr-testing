package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PlatvaBestHorse/hr-testing/backend/internal/database"
	"github.com/PlatvaBestHorse/hr-testing/backend/internal/identity"
	"github.com/jackc/pgx/v5/pgconn"
)

type testSession struct {
	cookie *http.Cookie
	csrf   string
	userID string
}

func TestAuthenticatedWorkflow(t *testing.T) {
	ownerURL, appURL := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_APP_DATABASE_URL")
	if ownerURL == "" || appURL == "" {
		t.Skip("run make integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	owner, err := database.Open(ctx, ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err = database.Migrate(ctx, owner); err != nil {
		t.Fatal(database.RedactError(err))
	}
	pool, err := database.Open(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var random [12]byte
	rand.Read(random[:])
	suffix := hex.EncodeToString(random[:])
	password := "fixture-" + suffix
	hash, err := identity.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	orgIDs := []string{}
	userIDs := []string{}
	// These committed fixtures exercise real HTTP transactions; delete only fixture IDs.
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		// Fixture teardown uses only the local test-owner connection and fixture IDs.
		// Application sessions cannot bypass immutable published history.
		cleanupTx, err := owner.Begin(cleanup)
		if err != nil {
			t.Error("fixture cleanup transaction failed")
			return
		}
		defer cleanupTx.Rollback(context.Background())
		if _, err = cleanupTx.Exec(cleanup, `SET LOCAL session_replication_role = replica`); err != nil {
			t.Error("local test-owner cleanup requires maintenance privileges")
			return
		}
		// Replica mode also disables FK cascades: remove new dependent fixture
		// rows explicitly before their parent tokens/users disappear.
		for _, sql := range []string{
			`DELETE FROM account_mail WHERE token_hash IN (SELECT token_hash FROM account_tokens WHERE organization_id=ANY($1::uuid[]))`,
		} {
			if _, err := cleanupTx.Exec(cleanup, sql, orgIDs); err != nil {
				t.Error("account mail fixture cleanup failed")
				return
			}
		}
		for _, table := range []string{"mfa_recovery_codes", "user_security"} {
			if _, err := cleanupTx.Exec(cleanup, `DELETE FROM `+table+` WHERE user_id IN (SELECT id FROM users WHERE id=ANY($1::uuid[]) OR email LIKE $2)`, userIDs, "%"+suffix+"@fixture.test"); err != nil {
				t.Error("account security fixture cleanup failed")
				return
			}
		}
		for _, table := range []string{"erasure_previews", "candidate_erasures", "imported_tests", "imported_questions", "import_batches", "result_comments", "account_tokens", "auth_sessions", "retention_previews", "retention_policies", "outbound_messages", "audit_events", "candidate_consents", "consent_documents", "answer_receipts", "start_receipts", "attempt_answers", "attempt_items", "attempts", "invitations", "vacancy_assessors", "candidates", "vacancies", "test_items", "test_versions", "tests", "question_options", "question_versions", "attachments", "questions", "categories", "memberships", "organizations"} {
			column := "organization_id"
			if table == "organizations" {
				column = "id"
			}
			if _, err := cleanupTx.Exec(cleanup, `DELETE FROM `+table+` WHERE `+column+`=ANY($1::uuid[])`, orgIDs); err != nil {
				t.Errorf("fixture cleanup %s failed", table)
			}
		}
		if _, err := cleanupTx.Exec(cleanup, `DELETE FROM users WHERE id=ANY($1::uuid[]) OR email LIKE $2`, userIDs, "%"+suffix+"@fixture.test"); err != nil {
			t.Error("user fixture cleanup failed")
		}
		if _, err := owner.Exec(cleanup, `DELETE FROM auth_rate_limits WHERE bucket=ANY($1::text[])`, []string{"candidate-ip:" + hex.EncodeToString(digest("192.0.2.1")), "ip:" + hex.EncodeToString(digest("192.0.2.1")), "email:" + hex.EncodeToString(digest("admin-a-"+suffix+"@fixture.test")), "email:" + hex.EncodeToString(digest("admin-b-"+suffix+"@fixture.test")), "email:" + hex.EncodeToString(digest("recruiter-"+suffix+"@fixture.test")), "email:" + hex.EncodeToString(digest("assessor-"+suffix+"@fixture.test")), "email:" + hex.EncodeToString(digest("staff-"+suffix+"@fixture.test"))}); err != nil {
			t.Error("limiter cleanup failed")
		}
		if err = cleanupTx.Commit(cleanup); err != nil {
			t.Error("fixture cleanup commit failed")
		}
		var orphaned int
		if err := owner.QueryRow(cleanup, `SELECT
		 (SELECT count(*) FROM account_mail m WHERE NOT EXISTS(SELECT 1 FROM account_tokens t WHERE t.token_hash=m.token_hash))+
		 (SELECT count(*) FROM user_security s WHERE NOT EXISTS(SELECT 1 FROM users u WHERE u.id=s.user_id))+
		 (SELECT count(*) FROM mfa_recovery_codes c WHERE NOT EXISTS(SELECT 1 FROM users u WHERE u.id=c.user_id))`).Scan(&orphaned); err != nil || orphaned != 0 {
			t.Error("fixture teardown left orphaned account records")
		}
	}()
	for _, label := range []string{"A", "B"} {
		var id string
		if err = owner.QueryRow(ctx, `INSERT INTO organizations(slug,name) VALUES ($1,$2) RETURNING id`, "fixture-"+label+"-"+suffix, label).Scan(&id); err != nil {
			t.Fatal(err)
		}
		orgIDs = append(orgIDs, id)
	}
	emails := map[string]string{}
	ids := map[string]string{}
	for _, u := range []struct {
		name, role string
		org        int
	}{{"admin-a", "org_admin", 0}, {"admin-b", "org_admin", 1}, {"recruiter", "recruiter", 0}, {"assessor", "assessor", 0}} {
		email := u.name + "-" + suffix + "@fixture.test"
		var id string
		if err = owner.QueryRow(ctx, `INSERT INTO users(email,password_hash,display_name) VALUES ($1,$2,$3) RETURNING id`, email, hash, u.name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		userIDs = append(userIDs, id)
		emails[u.name] = email
		ids[u.name] = id
		if _, err = owner.Exec(ctx, `INSERT INTO memberships(organization_id,user_id,role) VALUES ($1,$2,$3)`, orgIDs[u.org], id, u.role); err != nil {
			t.Fatal(err)
		}
	}
	app := NewApp(pool, false, []string{"http://127.0.0.1:8088"})
	handler := Router(database.Readiness{Pool: pool}, app)
	call := func(method, path string, body any, s testSession, csrf bool) *httptest.ResponseRecorder {
		var content []byte
		if body != nil {
			content, _ = json.Marshal(body)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(content))
		r.RemoteAddr = "192.0.2.1:12345"
		r.Header.Set("Origin", "http://127.0.0.1:8088")
		r.Header.Set("Content-Type", "application/json")
		if s.cookie != nil {
			r.AddCookie(s.cookie)
		}
		if csrf {
			r.Header.Set("X-CSRF-Token", s.csrf)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	expect := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("expected %d, got %d: %s", status, w.Code, w.Body.String())
		}
	}
	decode := func(w *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var data map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		return data
	}
	login := func(email string) testSession {
		t.Helper()
		w := call("POST", "/api/v1/auth/login", map[string]string{"email": email, "password": password}, testSession{}, false)
		expect(w, 200)
		data := decode(w)
		cookies := w.Result().Cookies()
		if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
			t.Fatal("unsafe cookie attributes")
		}
		return testSession{cookies[0], data["csrf_token"].(string), data["user_id"].(string)}
	}
	t.Run("anonymous access and origin", func(t *testing.T) {
		expect(call("GET", "/api/v1/questions", nil, testSession{}, false), 401)
		r := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(`{}`))
		r.Header.Set("Origin", "https://evil.example")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		expect(w, 403)
		expect(call("POST", "/api/v1/auth/login", map[string]string{"email": emails["admin-a"], "password": "incorrect"}, testSession{}, false), 401)
	})
	admin, recruiter, assessor, other := login(emails["admin-a"]), login(emails["recruiter"]), login(emails["assessor"]), login(emails["admin-b"])
	t.Run("sessions contain no raw token in database or public JSON", func(t *testing.T) {
		w := call("GET", "/api/v1/me", nil, admin, false)
		expect(w, 200)
		if strings.Contains(w.Body.String(), admin.cookie.Value) || strings.Contains(w.Body.String(), hash) {
			t.Fatal("session response leaked secret")
		}
		var stored []byte
		if err := owner.QueryRow(ctx, `SELECT token_hash FROM auth_sessions WHERE user_id=$1`, admin.userID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(stored, digest(admin.cookie.Value)) {
			t.Fatal("session token not hashed")
		}
	})
	var categoryID string
	t.Run("categories CSRF and roles", func(t *testing.T) {
		expect(call("POST", "/api/v1/categories", map[string]string{"name": "Logic"}, admin, false), 403)
		expect(call("POST", "/api/v1/categories", map[string]string{"name": "Logic", "organization_id": orgIDs[1]}, admin, true), 400)
		w := call("POST", "/api/v1/categories", map[string]string{"name": "Logic"}, admin, true)
		expect(w, 201)
		categoryID = decode(w)["id"].(string)
		expect(call("GET", "/api/v1/team", nil, recruiter, false), 403)
		expect(call("GET", "/api/v1/questions", nil, assessor, false), 403)
		expect(call("POST", "/api/v1/categories", map[string]string{"name": "No"}, assessor, true), 403)
		expect(call("GET", "/api/v1/me", nil, assessor, false), 200)
	})
	in := questionInput{CategoryID: categoryID, Difficulty: 2, Type: "single_choice", Body: "Original question", Options: []questionOption{{Label: "A", Correct: true}, {Label: "B"}}}
	var questionID, versionID string
	t.Run("question validation and persistence", func(t *testing.T) {
		bad := in
		bad.Options = []questionOption{{Label: "A"}, {Label: "B"}}
		expect(call("POST", "/api/v1/questions", bad, recruiter, true), 422)
		w := call("POST", "/api/v1/questions", in, recruiter, true)
		expect(w, 201)
		questionID = decode(w)["id"].(string)
		w = call("GET", "/api/v1/questions/"+questionID, nil, recruiter, false)
		expect(w, 200)
		q := decode(w)
		versionID = q["version_id"].(string)
		if q["body"] != "Original question" || len(q["options"].([]any)) != 2 {
			t.Fatal("question not persisted")
		}
	})
	t.Run("tenant isolation", func(t *testing.T) {
		expect(call("GET", "/api/v1/questions/"+questionID, nil, other, false), 404)
		expect(call("PATCH", "/api/v1/categories/"+categoryID, map[string]string{"name": "Stolen"}, other, true), 404)
		expect(call("DELETE", "/api/v1/categories/"+categoryID, nil, other, true), 404)
		bad := in
		bad.BaseVersion = 1
		bad.CategoryID = ""
		expect(call("POST", "/api/v1/questions/"+questionID+"/versions", bad, other, true), 404)
		expect(call("POST", "/api/v1/questions", in, other, true), 422)
		w := call("GET", "/api/v1/questions?organization_id="+orgIDs[0], nil, other, false)
		expect(w, 200)
		if decode(w)["total"].(float64) != 0 {
			t.Fatal("cross-tenant list leak")
		}
	})
	t.Run("publication and immutable history", func(t *testing.T) {
		expect(call("POST", "/api/v1/questions/"+questionID+"/publish", map[string]int{"base_version_no": 1}, admin, true), 200)
		_, err := owner.Exec(ctx, `UPDATE question_versions SET body='tampered' WHERE organization_id=$1 AND id=$2`, orgIDs[0], versionID)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55000" {
			t.Fatal("published version mutation accepted")
		}
		_, err = owner.Exec(ctx, `UPDATE question_options SET is_correct=NOT is_correct WHERE organization_id=$1 AND question_version_id=$2`, orgIDs[0], versionID)
		if !errors.As(err, &pgErr) || pgErr.Code != "55000" {
			t.Fatal("published answer mutation accepted")
		}
		in.BaseVersion = 1
		in.Body = "Revised question"
		expect(call("POST", "/api/v1/questions/"+questionID+"/versions", in, admin, true), 201)
		w := call("GET", "/api/v1/questions/"+questionID+"?version_no=1", nil, admin, false)
		expect(w, 200)
		q := decode(w)
		if q["body"] != "Original question" || q["version_status"] != "published" {
			t.Fatal("old version changed")
		}
		expect(call("POST", "/api/v1/questions/"+questionID+"/versions", in, admin, true), 409)
	})
	t.Run("parallel saves reject stale revision", func(t *testing.T) {
		in.BaseVersion = 2
		in.Body = "Concurrent revision"
		codes := make(chan int, 2)
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				codes <- call("POST", "/api/v1/questions/"+questionID+"/versions", in, admin, true).Code
			}()
		}
		wg.Wait()
		close(codes)
		counts := map[int]int{}
		for code := range codes {
			counts[code]++
		}
		if counts[201] != 1 || counts[409] != 1 {
			t.Fatalf("parallel results: %v", counts)
		}
	})
	t.Run("last administrator and session revocation", func(t *testing.T) {
		expect(call("PATCH", "/api/v1/team/"+admin.userID, map[string]string{"role": "recruiter", "status": "active"}, admin, true), 409)
		email := "staff-" + suffix + "@fixture.test"
		w := call("POST", "/api/v1/team", map[string]string{"email": email, "display_name": "Staff", "role": "recruiter", "password": password}, admin, true)
		expect(w, 201)
		id := decode(w)["id"].(string)
		staff := login(email)
		expect(call("GET", "/api/v1/questions", nil, staff, false), 200)
		expect(call("PATCH", "/api/v1/team/"+id, map[string]string{"role": "assessor", "status": "active"}, admin, true), 200)
		expect(call("GET", "/api/v1/me", nil, staff, false), 401)
		staff = login(email)
		expect(call("GET", "/api/v1/questions", nil, staff, false), 403)
		expect(call("PATCH", "/api/v1/team/"+id, map[string]string{"role": "assessor", "status": "disabled"}, admin, true), 200)
		expect(call("POST", "/api/v1/auth/login", map[string]string{"email": email, "password": password}, testSession{}, false), 401)
		expect(call("PATCH", "/api/v1/team/"+ids["admin-b"], map[string]string{"role": "recruiter", "status": "active"}, admin, true), 404)
	})
	t.Run("archive and category deletion protection", func(t *testing.T) {
		expect(call("DELETE", "/api/v1/categories/"+categoryID, nil, admin, true), 409)
		expect(call("POST", "/api/v1/questions/"+questionID+"/archive", nil, admin, true), 204)
		in.BaseVersion = 3
		expect(call("POST", "/api/v1/questions/"+questionID+"/versions", in, admin, true), 409)
	})
	t.Run("test builder and invitation workflow", func(t *testing.T) {
		exerciseTestInvitations(t, ctx, owner, call, admin, recruiter, assessor, other, orgIDs[0], orgIDs[1], ids["assessor"])
	})
	t.Run("candidate passage workflow", func(t *testing.T) {
		exerciseAttempts(t, ctx, owner, app, call, admin, assessor, other, orgIDs[0], ids["assessor"])
	})
	t.Run("audit and retention workflow", func(t *testing.T) {
		exerciseGovernance(t, ctx, owner, app, call, admin, recruiter, assessor, other, orgIDs[0], orgIDs[1])
	})
	t.Run("question images workflow", func(t *testing.T) {
		exerciseImages(t, ctx, owner, app, call, admin, recruiter, assessor, other, orgIDs[0], orgIDs[1], ids["assessor"])
	})
	t.Run("administration workflow", func(t *testing.T) {
		exerciseAdministration(t, ctx, owner, app, call, admin, recruiter, assessor, other, orgIDs[0], orgIDs[1], password, emails["recruiter"])
	})
	other = login(emails["admin-b"])
	t.Run("security hardening workflow", func(t *testing.T) {
		exerciseHardening(t, ctx, owner, app, call, admin, recruiter, orgIDs[0], emails["recruiter"], password)
	})
	t.Run("legacy import workflow", func(t *testing.T) {
		exerciseLegacyImport(t, ctx, owner, call, admin, recruiter, assessor, other, orgIDs[0], orgIDs[1])
	})
	t.Run("individual privacy workflow", func(t *testing.T) {
		exercisePrivacy(t, ctx, owner, app, call, admin, recruiter, assessor, other, orgIDs[0], orgIDs[1])
	})
	t.Run("account security workflow", func(t *testing.T) {
		exerciseAccountSecurity(t, ctx, owner, app, call, recruiter, other, password, emails["recruiter"])
	})
	t.Run("login limiter", func(t *testing.T) {
		_, err := owner.Exec(ctx, `UPDATE auth_rate_limits SET attempts=10 WHERE bucket=$1`, "email:"+hex.EncodeToString(digest(emails["admin-a"])))
		if err != nil {
			t.Fatal(err)
		}
		expect(call("POST", "/api/v1/auth/login", map[string]string{"email": emails["admin-a"], "password": password}, testSession{}, false), 429)
	})
	t.Run("logout and expiry", func(t *testing.T) {
		expect(call("POST", "/api/v1/auth/logout", nil, admin, true), 204)
		expect(call("GET", "/api/v1/me", nil, admin, false), 401)
		if _, err := owner.Exec(ctx, `UPDATE auth_sessions SET expires_at=now()-interval '1 second' WHERE token_hash=$1`, digest(other.cookie.Value)); err != nil {
			t.Fatal(err)
		}
		expect(call("GET", "/api/v1/me", nil, other, false), 401)
	})
	t.Run("CLI password reset revokes sessions", func(t *testing.T) {
		newPassword := password + "-new"
		if err := identity.ResetPassword(ctx, pool, emails["recruiter"], newPassword); err != nil {
			t.Fatal("password reset failed")
		}
		expect(call("GET", "/api/v1/me", nil, recruiter, false), 401)
		expect(call("POST", "/api/v1/auth/login", map[string]string{"email": emails["recruiter"], "password": password}, testSession{}, false), 401)
		expect(call("POST", "/api/v1/auth/login", map[string]string{"email": emails["recruiter"], "password": newPassword}, testSession{}, false), 200)
	})
	t.Run("parallel login and password reset do not revive a revoked session", func(t *testing.T) {
		blocker, err := owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback(context.Background())
		if _, err = blocker.Exec(ctx, `SELECT role FROM memberships WHERE organization_id=$1 AND user_id=$2 FOR UPDATE`, orgIDs[1], ids["admin-b"]); err != nil {
			t.Fatal(err)
		}
		loginDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			loginDone <- call("POST", "/api/v1/auth/login", map[string]string{"email": emails["admin-b"], "password": password}, testSession{}, false)
		}()
		waitBlocked := func(pattern string) bool {
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				var count int
				if err := owner.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE $1`, pattern).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count > 0 {
					return true
				}
				time.Sleep(10 * time.Millisecond)
			}
			return false
		}
		if !waitBlocked("SELECT role FROM memberships%") {
			t.Fatal("login did not reach membership lock")
		}
		resetDone := make(chan error, 1)
		go func() { resetDone <- identity.ResetPassword(ctx, pool, emails["admin-b"], password+"-race-reset") }()
		if !waitBlocked("UPDATE users SET password_hash%") {
			t.Fatal("password reset must wait for the in-flight login user lock")
		}
		if err = blocker.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		var loggedIn *httptest.ResponseRecorder
		select {
		case loggedIn = <-loginDone:
		case <-ctx.Done():
			t.Fatal("login did not complete")
		}
		expect(loggedIn, 200)
		select {
		case err = <-resetDone:
			if err != nil {
				t.Fatal("reset did not complete")
			}
		case <-ctx.Done():
			t.Fatal("reset timed out")
		}
		cookie := loggedIn.Result().Cookies()[0]
		expect(call("GET", "/api/v1/me", nil, testSession{cookie: cookie}, false), 401)
	})
	var publishedAudit int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE organization_id=$1 AND action='question.publish' AND object_id=$2`, orgIDs[0], questionID).Scan(&publishedAudit); err != nil || publishedAudit != 1 {
		t.Fatal("publication audit missing")
	}
}
