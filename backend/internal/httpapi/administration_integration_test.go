package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/PlatvaBestHorse/hr-testing/backend/internal/maildelivery"
	"github.com/jackc/pgx/v5/pgxpool"
)

func exerciseAdministration(t *testing.T, ctx context.Context, owner *pgxpool.Pool, app *App, call func(string, string, any, testSession, bool) *httptest.ResponseRecorder, admin, recruiter, assessor, other testSession, org, otherOrg, password, email string) {
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
		var out map[string]any
		if json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatal("invalid JSON")
		}
		return out
	}
	otherEmail := data(call("GET", "/api/v1/me", nil, other, false))["email"].(string)
	t.Run("organization_settings_roles_revision_and_confirmation", func(t *testing.T) {
		w := call("GET", "/api/v1/organization", nil, admin, false)
		check(w, 200)
		before := data(w)
		body := map[string]any{"name": "Updated fixture organization", "status": "active", "invitation_default_days": 14, "base_revision": before["revision"]}
		check(call("PATCH", "/api/v1/organization", body, recruiter, true), 403)
		check(call("PATCH", "/api/v1/organization", body, admin, false), 403)
		check(call("PATCH", "/api/v1/organization", body, admin, true), 200)
		check(call("PATCH", "/api/v1/organization", body, admin, true), 409)
		w = call("GET", "/api/v1/organization", nil, assessor, false)
		check(w, 200)
		if data(w)["invitation_default_days"] != float64(14) {
			t.Fatal("settings not saved")
		}
		foreign := data(call("GET", "/api/v1/organization", nil, other, false))
		if foreign["name"] == body["name"] {
			t.Fatal("cross-tenant organization update")
		}
		suspended := map[string]any{"name": foreign["name"], "status": "suspended", "invitation_default_days": 7, "base_revision": foreign["revision"]}
		check(call("PATCH", "/api/v1/organization", suspended, other, true), 422)
		suspended["confirmation"] = "ЗАКРЫТЬ ДОСТУП"
		check(call("PATCH", "/api/v1/organization", suspended, other, true), 200)
		check(call("GET", "/api/v1/me", nil, other, false), 401)
		if _, e := owner.Exec(ctx, "UPDATE organizations SET status='active',settings_revision=settings_revision+1 WHERE id=$1", otherOrg); e != nil {
			t.Fatal("fixture organization recovery failed")
		}
		check(call("GET", "/api/v1/me", nil, other, false), 401)
		fresh := call("POST", "/api/v1/auth/login", map[string]string{"email": otherEmail, "password": password}, testSession{}, false)
		check(fresh, 200)
		other = testSession{cookie: fresh.Result().Cookies()[0], csrf: data(fresh)["csrf_token"].(string), userID: other.userID}
	})
	t.Run("staff_invitation_activation_rotation_and_cancellation", func(t *testing.T) {
		app.AuthKey = bytes.Repeat([]byte{8}, 32)
		app.PublicURL = "http://127.0.0.1:8088"
		messages := []maildelivery.Message{}
		app.SendAuthMail = func(_ context.Context, m maildelivery.Message) error { messages = append(messages, m); return nil }
		token := func() string {
			t.Helper()
			before := len(messages)
			if e := app.DeliverAuthMail(ctx); e != nil {
				t.Fatal("mail failed")
			}
			if len(messages) != before+1 {
				t.Fatal("missing invitation mail")
			}
			for _, line := range strings.Split(messages[len(messages)-1].Body, "\n") {
				if strings.Contains(line, "#token=") {
					u, e := url.Parse(line)
					if e == nil {
						return strings.TrimPrefix(u.Fragment, "token=")
					}
				}
			}
			t.Fatal("missing link")
			return ""
		}
		invite := map[string]string{"email": "invited-" + email, "display_name": "Invited fixture", "role": "assessor"}
		check(call("POST", "/api/v1/team/invitations", invite, recruiter, true), 403)
		check(call("POST", "/api/v1/team/invitations", invite, admin, false), 403)
		w := call("POST", "/api/v1/team/invitations", invite, admin, true)
		check(w, 201)
		id := data(w)["id"].(string)
		if data(w)["status"] != "invited" {
			t.Fatal("invite prematurely activated")
		}
		first := token()
		check(call("PATCH", "/api/v1/team/"+id, map[string]string{"role": "assessor", "status": "active"}, admin, true), 409)
		check(call("POST", "/api/v1/team/"+id+"/invitation", nil, admin, true), 204)
		next := token()
		pass := "Invited-" + fixtureUUID()
		complete := func(tok string) *httptest.ResponseRecorder {
			return call("POST", "/api/v1/auth/recovery/complete", map[string]string{"token": tok, "new_password": pass}, testSession{}, false)
		}
		check(complete(first), 401)
		check(complete(next), 204)
		check(complete(next), 401)
		check(call("POST", "/api/v1/auth/login", map[string]string{"email": invite["email"], "password": pass}, testSession{}, false), 200)
		check(call("POST", "/api/v1/team/"+id+"/invitation", nil, admin, true), 404)
		invite["email"] = "cancelled-" + email
		w = call("POST", "/api/v1/team/invitations", invite, admin, true)
		check(w, 201)
		id = data(w)["id"].(string)
		next = token()
		check(call("PATCH", "/api/v1/team/"+id, map[string]string{"role": "assessor", "status": "disabled"}, admin, true), 200)
		check(complete(next), 401)
	})
	t.Run("comments_follow_result_assignment_and_author_permissions", func(t *testing.T) {
		var attempt, vacancy string
		e := owner.QueryRow(ctx, `SELECT a.id,i.vacancy_id FROM attempts a JOIN invitations i ON i.id=a.invitation_id AND i.organization_id=a.organization_id WHERE a.organization_id=$1 AND a.status IN ('submitted','expired') AND i.vacancy_id IS NOT NULL ORDER BY a.started_at LIMIT 1`, org).Scan(&attempt, &vacancy)
		if e != nil {
			t.Fatal("missing result fixture")
		}
		if _, e = owner.Exec(ctx, "INSERT INTO vacancy_assessors(organization_id,vacancy_id,user_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", org, vacancy, assessor.userID); e != nil {
			t.Fatal("cannot assign fixture assessor")
		}
		base := "/api/v1/results/" + attempt + "/comments"
		check(call("GET", base, nil, testSession{}, false), 401)
		check(call("POST", base, map[string]string{"body": "Scoped comment"}, assessor, false), 403)
		w := call("POST", base, map[string]string{"body": "<script>escaped</script>"}, assessor, true)
		check(w, 201)
		id := data(w)["id"].(string)
		w = call("GET", base, nil, admin, false)
		check(w, 200)
		items := data(w)["items"].([]any)
		if len(items) != 1 || items[0].(map[string]any)["can_edit"] != false {
			t.Fatal("wrong comment permissions")
		}
		updated := items[0].(map[string]any)["updated_at"]
		check(call("PATCH", base+"/"+id, map[string]any{"body": "Wrong actor", "base_updated_at": updated}, recruiter, true), 403)
		check(call("PATCH", base+"/"+id, map[string]any{"body": "Revised", "base_updated_at": updated}, assessor, true), 200)
		check(call("PATCH", base+"/"+id, map[string]any{"body": "Stale", "base_updated_at": updated}, assessor, true), 409)
		check(call("GET", base, nil, other, false), 404)
		check(call("DELETE", base+"/"+id, nil, recruiter, true), 403)
		check(call("DELETE", base+"/"+id, nil, admin, true), 204)
		w = call("POST", base, map[string]string{"body": "Assignment check"}, assessor, true)
		check(w, 201)
		if e = owner.QueryRow(ctx, "SELECT vacancy_id FROM invitations WHERE id=(SELECT invitation_id FROM attempts WHERE id=$1)", attempt).Scan(&vacancy); e != nil {
			t.Fatal(e)
		}
		if _, e = owner.Exec(ctx, "DELETE FROM vacancy_assessors WHERE organization_id=$1 AND vacancy_id=$2 AND user_id=$3", org, vacancy, assessor.userID); e != nil {
			t.Fatal(e)
		}
		check(call("GET", base, nil, assessor, false), 404)
		check(call("POST", base, map[string]string{"body": "No access"}, assessor, true), 404)
		// Restore only the fixture assignment for the rest of the workflow.
		if _, e = owner.Exec(ctx, "INSERT INTO vacancy_assessors(organization_id,vacancy_id,user_id) VALUES($1,$2,$3)", org, vacancy, assessor.userID); e != nil {
			t.Fatal(e)
		}
	})
}
