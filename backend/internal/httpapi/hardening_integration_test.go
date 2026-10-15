package httpapi

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/PlatvaBestHorse/hr-testing/backend/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

func exerciseHardening(t *testing.T, ctx context.Context, owner *pgxpool.Pool, app *App, call func(string, string, any, testSession, bool) *httptest.ResponseRecorder, admin, recruiter testSession, org, email, password string) {
	t.Run("team_writes_recheck_actor_after_lock_wait", func(t *testing.T) {
		for _, mode := range []string{"create", "invite", "update"} {
			t.Run(mode, func(t *testing.T) {
				blocker, err := owner.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					blocker.Rollback(context.Background())
					if _, e := owner.Exec(ctx, "UPDATE memberships SET role='org_admin' WHERE organization_id=$1 AND user_id=$2", org, admin.userID); e != nil {
						t.Error("fixture role recovery failed")
					}
				}()
				if _, err = blocker.Exec(ctx, "SELECT id FROM organizations WHERE id=$1 FOR UPDATE", org); err != nil {
					t.Fatal(err)
				}
				method, path := "POST", "/api/v1/team"
				address := mode + "-race-" + email
				body := map[string]string{"email": address, "display_name": "Blocked fixture", "role": "assessor", "password": password}
				if mode == "invite" {
					path = "/api/v1/team/invitations"
					delete(body, "password")
				}
				if mode == "update" {
					method = "PATCH"
					path = "/api/v1/team/" + recruiter.userID
					body = map[string]string{"role": "assessor", "status": "active"}
				}
				response := make(chan *httptest.ResponseRecorder, 1)
				go func() { response <- call(method, path, body, admin, true) }()
				waiting := false
				for n := 0; n < 150; n++ {
					if err = owner.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%organizations%' AND query LIKE '%FOR UPDATE%')`).Scan(&waiting); err != nil {
						t.Fatal(err)
					}
					if waiting {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if !waiting {
					t.Fatal("team write did not reach organization lock")
				}
				if _, err = blocker.Exec(ctx, "UPDATE memberships SET role='recruiter' WHERE organization_id=$1 AND user_id=$2", org, admin.userID); err != nil {
					t.Fatal(err)
				}
				if err = blocker.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				w := <-response
				if w.Code != 401 {
					t.Fatalf("stale administrator accepted: %d", w.Code)
				}
				if mode == "update" {
					var role string
					if err = owner.QueryRow(ctx, "SELECT role FROM memberships WHERE organization_id=$1 AND user_id=$2", org, recruiter.userID).Scan(&role); err != nil || role != "recruiter" {
						t.Fatal("unauthorized role update committed")
					}
				} else {
					var found bool
					if err = owner.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE email=$1)", address).Scan(&found); err != nil || found {
						t.Fatal("unauthorized staff creation committed")
					}
				}
			})
		}
	})
	t.Run("candidate_rate_limits_proxy_isolation_and_expiry", func(t *testing.T) {
		oldLimit, oldProxies := app.CandidateIPLimit, app.TrustedProxies
		app.CandidateIPLimit = 3
		app.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("172.30.254.10/32")}
		ips := []string{"198.51.100.71", "198.51.100.72", "198.51.100.73", "203.0.113.71"}
		buckets := []string{}
		for _, ip := range ips {
			buckets = append(buckets, "candidate-ip:"+hex.EncodeToString(digest(ip)))
		}
		defer func() {
			app.CandidateIPLimit = oldLimit
			app.TrustedProxies = oldProxies
			if _, e := owner.Exec(ctx, "DELETE FROM auth_rate_limits WHERE bucket=ANY($1::text[])", buckets); e != nil {
				t.Error("candidate limiter cleanup failed")
			}
		}()
		handler := Router(database.Readiness{Pool: app.Pool}, app)
		request := func(method, path, peer, forward string) *httptest.ResponseRecorder {
			r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
			r.RemoteAddr = peer
			r.Header.Set("X-Forwarded-For", forward)
			r.Header.Set("Origin", "http://127.0.0.1:8088")
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			return w
		}
		token := strings.Repeat("A", 43)
		path := "/api/v1/invite/" + token
		for n := 0; n < 3; n++ {
			if w := request("GET", path, "172.30.254.10:1234", ips[0]); w.Code != 404 {
				t.Fatalf("unexpected initial status %d", w.Code)
			}
		}
		w := request("GET", path, "172.30.254.10:1234", ips[0])
		if w.Code != 429 || w.Header().Get("Retry-After") != "60" {
			t.Fatal("public invitation not limited")
		}
		if w := request("POST", path+"/start", "172.30.254.10:1234", ips[0]); w.Code != 429 {
			t.Fatal("candidate mutation bypassed limiter")
		}
		if w := request("GET", path, "172.30.254.10:1234", ips[1]); w.Code != 404 {
			t.Fatal("separate clients share a proxy bucket")
		}
		// Rotating a forged forwarded header on a direct connection must not reset limits.
		for n := 0; n < 3; n++ {
			request("GET", path, ips[2]+":1234", ips[n])
		}
		if w := request("GET", path, ips[2]+":1234", ips[3]); w.Code != 429 {
			t.Fatal("direct spoof bypassed limiter")
		}
		if _, err := owner.Exec(ctx, "UPDATE auth_rate_limits SET window_started_at=clock_timestamp()-interval '61 seconds' WHERE bucket=$1", buckets[0]); err != nil {
			t.Fatal(err)
		}
		if w := request("GET", path, "172.30.254.10:1234", ips[0]); w.Code != 404 {
			t.Fatal("expired minute bucket not reset")
		}
		// Technical cleanup must remove old buckets and keep active ones.
		if _, err := owner.Exec(ctx, "INSERT INTO auth_rate_limits(bucket,window_started_at) VALUES($1,clock_timestamp()-interval '2 days')", buckets[3]); err != nil {
			t.Fatal(err)
		}
		if err := app.CleanupRateLimits(ctx); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := owner.QueryRow(ctx, "SELECT count(*) FROM auth_rate_limits WHERE bucket=ANY($1::text[])", buckets).Scan(&n); err != nil || n != 3 {
			t.Fatal("cleanup removed active counters or kept expired counter")
		}
		// A refresh that commits while cleanup waits must keep the active bucket.
		if _, err := owner.Exec(ctx, "INSERT INTO auth_rate_limits(bucket,window_started_at) VALUES($1,clock_timestamp()-interval '2 days')", buckets[3]); err != nil {
			t.Fatal(err)
		}
		blocker, err := owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback(context.Background())
		if _, err = blocker.Exec(ctx, "UPDATE auth_rate_limits SET window_started_at=clock_timestamp() WHERE bucket=$1", buckets[3]); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- app.CleanupRateLimits(ctx) }()
		waiting := false
		for i := 0; i < 150; i++ {
			if err = owner.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%DELETE FROM auth_rate_limits WHERE window_started_at%')`).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !waiting {
			t.Fatal("cleanup did not wait for concurrent bucket refresh")
		}
		if err = blocker.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err = <-done; err != nil {
			t.Fatal(err)
		}
		if err = owner.QueryRow(ctx, "SELECT count(*) FROM auth_rate_limits WHERE bucket=$1", buckets[3]).Scan(&n); err != nil || n != 1 {
			t.Fatal("cleanup erased a concurrently refreshed counter")
		}
		var body map[string]any
		json.Unmarshal(w.Body.Bytes(), &body)
		if body["code"] != "rate_limited" {
			t.Fatal("nonstandard rate error")
		}
	})
}
