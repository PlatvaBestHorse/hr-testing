package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func exerciseGovernance(t *testing.T, ctx context.Context, owner *pgxpool.Pool, app *App, call func(string, string, any, testSession, bool) *httptest.ResponseRecorder, admin, recruiter, assessor, other testSession, org, orgB string) {
	check := func(w *httptest.ResponseRecorder, want int) {
		t.Helper()
		if w.Code != want {
			var b map[string]any
			json.Unmarshal(w.Body.Bytes(), &b)
			t.Fatalf("expected HTTP %d got %d code=%v", want, w.Code, b["code"])
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
	prepare := func(s testSession) retentionPreview {
		w := call("POST", "/api/v1/retention/preview", map[string]any{}, s, true)
		check(w, 200)
		var p retentionPreview
		json.Unmarshal(w.Body.Bytes(), &p)
		return p
	}
	apply := func(id string, s testSession, csrf bool) *httptest.ResponseRecorder {
		return call("POST", "/api/v1/retention/previews/"+id+"/apply", map[string]string{"confirmation": retentionConfirmation}, s, csrf)
	}
	var oldEvent, foreignEvent int64
	if err := owner.QueryRow(ctx, `INSERT INTO audit_events(organization_id,actor_user_id,action,object_type,details,occurred_at)
 VALUES($1,$2,'legacy.example','candidate','{"password":"PRIVATE-SECRET","token":"PRIVATE-TOKEN","email":"PRIVATE-EMAIL","reason":"PRIVATE-REASON","format":"csv","row_count":3}',now()-interval '90 days') RETURNING id`, org, admin.userID).Scan(&oldEvent); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `INSERT INTO audit_events(organization_id,actor_user_id,action,object_type,occurred_at) VALUES($1,$2,'foreign.marker','candidate',now()-interval '90 days') RETURNING id`, orgB, other.userID).Scan(&foreignEvent); err != nil {
		t.Fatal(err)
	}
	t.Run("administrator scope audit filters and sanitized details", func(t *testing.T) {
		for _, path := range []string{"/api/v1/audit", "/api/v1/audit/options", "/api/v1/retention", "/api/v1/retention/holds"} {
			check(call("GET", path, nil, testSession{}, false), 401)
			check(call("GET", path, nil, recruiter, false), 403)
			check(call("GET", path, nil, assessor, false), 403)
		}
		w := call("GET", "/api/v1/audit?action=legacy.example&object_type=candidate&actor_id="+admin.userID, nil, admin, false)
		check(w, 200)
		b := data(w)
		if b["total"].(float64) != 1 {
			t.Fatal("audit filters ignored")
		}
		for _, secret := range []string{"PRIVATE-SECRET", "PRIVATE-TOKEN", "PRIVATE-EMAIL", "PRIVATE-REASON", "foreign.marker"} {
			if bytes.Contains(w.Body.Bytes(), []byte(secret)) {
				t.Fatal("audit leaked private details or tenant")
			}
		}
		item := b["items"].([]any)[0].(map[string]any)
		detail := item["details"].(map[string]any)
		if len(detail) != 2 || detail["format"] != "csv" {
			t.Fatal("safe audit metadata missing")
		}
		check(call("GET", "/api/v1/audit?action=INVALID!", nil, admin, false), 400)
		check(call("GET", "/api/v1/audit?actor_id=invalid", nil, admin, false), 400)
		check(call("GET", "/api/v1/audit?date_from=2026-02-30", nil, admin, false), 400)
		check(call("GET", "/api/v1/audit?page=0", nil, admin, false), 400)
		w = call("GET", "/api/v1/audit/options", nil, admin, false)
		check(w, 200)
		if bytes.Contains(w.Body.Bytes(), []byte("foreign.marker")) {
			t.Fatal("audit options leaked tenant")
		}
		_, err := app.Pool.Exec(ctx, "UPDATE audit_events SET action='tampered' WHERE id=$1", oldEvent)
		var pgerr *pgconn.PgError
		if !errors.As(err, &pgerr) || pgerr.Code != "42501" {
			t.Fatal("app can update audit")
		}
		_, err = app.Pool.Exec(ctx, "DELETE FROM audit_events WHERE id=$1", oldEvent)
		if !errors.As(err, &pgerr) || pgerr.Code != "42501" {
			t.Fatal("app can delete audit outside retention")
		}
	})
	policyBody := func(revision int) map[string]any {
		return map[string]any{"enabled": true, "candidate_days": 30, "audit_days": 60, "purpose": "Fixture assessment purpose", "legal_basis": "Fixture organization policy", "base_revision": revision}
	}
	t.Run("disabled defaults policy validation CSRF and revisions", func(t *testing.T) {
		w := call("GET", "/api/v1/retention", nil, admin, false)
		check(w, 200)
		b := data(w)
		if b["enabled"] != false || b["candidate_days"] != nil || b["audit_days"] != nil || b["revision"].(float64) != 0 {
			t.Fatal("retention must default off")
		}
		check(call("POST", "/api/v1/retention/preview", map[string]any{}, admin, true), 422)
		for _, s := range []testSession{recruiter, assessor} {
			check(call("PATCH", "/api/v1/retention", policyBody(0), s, true), 403)
			check(call("POST", "/api/v1/retention/preview", map[string]any{}, s, true), 403)
		}
		check(call("PATCH", "/api/v1/retention", policyBody(0), admin, false), 403)
		bad := policyBody(0)
		bad["legal_basis"] = ""
		check(call("PATCH", "/api/v1/retention", bad, admin, true), 422)
		bad = policyBody(0)
		bad["candidate_days"] = 0
		check(call("PATCH", "/api/v1/retention", bad, admin, true), 422)
		check(call("PATCH", "/api/v1/retention", policyBody(0), admin, true), 200)
		check(call("PATCH", "/api/v1/retention", policyBody(0), admin, true), 409)
		w = call("GET", "/api/v1/retention", nil, other, false)
		check(w, 200)
		if data(w)["revision"].(float64) != 0 {
			t.Fatal("policy leaked tenant")
		}
	})
	create := func(name string, s testSession, tenant string, aged bool) string {
		w := call("POST", "/api/v1/candidates", map[string]string{"full_name": name, "email": "fixture@fixture.test", "phone": "12345"}, s, true)
		check(w, 201)
		id := data(w)["id"].(string)
		if aged {
			if _, err := owner.Exec(ctx, "UPDATE candidates SET created_at=now()-interval '90 days',updated_at=now()-interval '90 days' WHERE organization_id=$1 AND id=$2", tenant, id); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	due := create("Erase fixture", admin, org, true)
	edited := create("Edit fixture", admin, org, true)
	heldAfter := create("Later hold fixture", admin, org, true)
	held := create("Protected fixture", admin, org, true)
	active := create("Active fixture", admin, org, true)
	live := create("Live invitation fixture", admin, org, true)
	create("Recent fixture", admin, org, false)
	foreign := create("Foreign old fixture", other, orgB, true)
	check(call("PUT", "/api/v1/candidates/"+held+"/retention-hold", map[string]any{"hold": true, "reason": "PRIVATE-REASON"}, admin, true), 204)
	check(call("PUT", "/api/v1/candidates/"+held+"/retention-hold", map[string]any{"hold": false}, recruiter, true), 403)
	check(call("PUT", "/api/v1/candidates/"+foreign+"/retention-hold", map[string]any{"hold": true, "reason": "No"}, admin, true), 404)
	var version string
	if err := owner.QueryRow(ctx, `SELECT id FROM test_versions v WHERE organization_id=$1 AND status='published' AND EXISTS(SELECT 1 FROM test_items ti WHERE ti.organization_id=v.organization_id AND ti.test_version_id=v.id) ORDER BY created_at LIMIT 1`, org).Scan(&version); err != nil {
		t.Fatal(err)
	}
	invite := func(candidate string, future bool) (string, string) {
		var raw [32]byte
		rand.Read(raw[:])
		token := base64.RawURLEncoding.EncodeToString(raw[:])
		var id string
		expiry := time.Now().Add(-24 * time.Hour)
		if future {
			expiry = time.Now().Add(24 * time.Hour)
		}
		if err := owner.QueryRow(ctx, `INSERT INTO invitations(organization_id,candidate_id,test_version_id,token_hash,max_attempts,issued_at,expires_at) VALUES($1,$2,$3,$4,1,now()-interval '90 days',$5) RETURNING id`, org, candidate, version, digest(token), expiry).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id, token
	}
	dueInvite, dueToken := invite(due, false)
	activeInvite, _ := invite(active, false)
	invite(live, true)
	var attempt string
	if err := owner.QueryRow(ctx, `INSERT INTO attempts(organization_id,invitation_id,candidate_id,test_version_id,status,started_at,deadline_at,finished_at,score_points,max_points,score_percent,passed)
 VALUES($1,$2,$3,$4,'submitted',now()-interval '90 days',now()-interval '89 days',now()-interval '90 days',2.50,2.50,100,true) RETURNING id`, org, dueInvite, due, version).Scan(&attempt); err != nil {
		t.Fatal(err)
	}
	var item string
	if err := owner.QueryRow(ctx, `INSERT INTO attempt_items(organization_id,attempt_id,test_item_id,position,option_order)
 SELECT $1,$2,ti.id,1,(SELECT jsonb_agg(o.id ORDER BY o.position) FROM question_options o WHERE o.organization_id=ti.organization_id AND o.question_version_id=ti.question_version_id) FROM test_items ti WHERE ti.organization_id=$1 AND ti.test_version_id=$3 ORDER BY position LIMIT 1 RETURNING id`, org, attempt, version).Scan(&item); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO attempt_answers(organization_id,attempt_item_id,selected_option_ids) SELECT $1,$2,array_agg(o.id ORDER BY o.id) FROM question_options o JOIN test_items ti ON ti.organization_id=o.organization_id AND ti.question_version_id=o.question_version_id JOIN attempt_items ai ON ai.organization_id=ti.organization_id AND ai.test_item_id=ti.id WHERE ai.organization_id=$1 AND ai.id=$2 AND o.is_correct`, org, item); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO attempts(organization_id,invitation_id,candidate_id,test_version_id,status,started_at,deadline_at) VALUES($1,$2,$3,$4,'in_progress',now()-interval '90 days',now()+interval '1 hour')`, org, activeInvite, active, version); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO candidate_consents(organization_id,candidate_id,document_version,purpose,is_granted,recorded_at,evidence) VALUES($1,$2,'fixture-v1','assessment',true,now()-interval '90 days','{"name":"Erase fixture"}')`, org, due); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO outbound_messages(organization_id,invitation_id,recipient,template_code) VALUES($1,$2,'fixture@fixture.test','fixture')`, org, dueInvite); err != nil {
		t.Fatal(err)
	}
	var preview retentionPreview
	t.Run("preview is bounded non-destructive and excludes protected live and foreign", func(t *testing.T) {
		preview = prepare(admin)
		ids := []string{}
		for _, c := range preview.Candidates {
			ids = append(ids, c.ID)
		}
		if len(ids) != 3 || !slices.Contains(ids, due) || !slices.Contains(ids, edited) || !slices.Contains(ids, heldAfter) || preview.AuditCount != 1 || preview.CandidateLimit != 100 || preview.AuditLimit != 1000 {
			t.Fatal("wrong preview selection")
		}
		var name string
		owner.QueryRow(ctx, "SELECT full_name FROM candidates WHERE id=$1", due).Scan(&name)
		if name != "Erase fixture" {
			t.Fatal("preview erased data")
		}
		w := call("GET", "/api/v1/audit?action=candidate.retention_hold", nil, admin, false)
		check(w, 200)
		if bytes.Contains(w.Body.Bytes(), []byte("PRIVATE-REASON")) {
			t.Fatal("hold reason leaked to audit")
		}
	})
	t.Run("stale expired and foreign previews cannot apply", func(t *testing.T) {
		check(apply(preview.ID, other, true), 404)
		check(apply(preview.ID, recruiter, true), 403)
		check(apply(preview.ID, admin, false), 403)
		check(call("PATCH", "/api/v1/retention", policyBody(1), admin, true), 200)
		check(apply(preview.ID, admin, true), 409)
		expired := prepare(admin)
		if _, err := owner.Exec(ctx, "UPDATE retention_previews SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", expired.ID); err != nil {
			t.Fatal(err)
		}
		check(apply(expired.ID, admin, true), 409)
		_, err := app.Pool.Exec(ctx, "SELECT prune_audit_events($1,$2)", expired.ID, []byte("invalid-session-hash"))
		var pgerr *pgconn.PgError
		if !errors.As(err, &pgerr) || pgerr.Code != "42501" {
			t.Fatal("audit prune function accepted unauthorized session")
		}
	})
	preview = prepare(admin)
	t.Run("permissions rechecked after waiting for organization lock", func(t *testing.T) {
		blocker, err := owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback(context.Background())
		if _, err = blocker.Exec(ctx, "SELECT id FROM organizations WHERE id=$1 FOR UPDATE", org); err != nil {
			t.Fatal(err)
		}
		response := make(chan *httptest.ResponseRecorder, 1)
		go func() { response <- apply(preview.ID, admin, true) }()
		waiting := false
		for n := 0; n < 100; n++ {
			if err = owner.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%organizations%' AND query LIKE '%FOR NO KEY UPDATE%')`).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !waiting {
			t.Fatal("apply did not wait for organization lock")
		}
		if _, err = blocker.Exec(ctx, "UPDATE memberships SET role='recruiter' WHERE organization_id=$1 AND user_id=$2", org, admin.userID); err != nil {
			t.Fatal(err)
		}
		if err = blocker.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		check(<-response, 401)
		if _, err = owner.Exec(ctx, "UPDATE memberships SET role='org_admin' WHERE organization_id=$1 AND user_id=$2", org, admin.userID); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("confirmation and eligibility rechecked at apply", func(t *testing.T) {
		check(call("POST", "/api/v1/retention/previews/"+preview.ID+"/apply", map[string]string{"confirmation": "wrong"}, admin, true), 422)
		check(call("PATCH", "/api/v1/candidates/"+edited, map[string]string{"full_name": "Freshly edited fixture", "email": "fixture@fixture.test", "status": "active"}, recruiter, true), 200)
		check(call("PUT", "/api/v1/candidates/"+heldAfter+"/retention-hold", map[string]any{"hold": true, "reason": "Changed after preview"}, admin, true), 204)
	})
	later := create("Created after preview fixture", admin, org, true)
	t.Run("atomic idempotent apply contacts consent messages links and scores", func(t *testing.T) {
		var wg sync.WaitGroup
		responses := make([]*httptest.ResponseRecorder, 2)
		for n := range responses {
			wg.Add(1)
			go func(n int) { defer wg.Done(); responses[n] = apply(preview.ID, admin, true) }(n)
		}
		wg.Wait()
		var result retentionReceipt
		for _, w := range responses {
			check(w, 200)
			json.Unmarshal(w.Body.Bytes(), &result)
			if result.Erased != 1 || result.Skipped != 2 || result.AuditDeleted != 1 {
				t.Fatal("unexpected retention receipt")
			}
		}
		if !bytes.Equal(responses[0].Body.Bytes(), responses[1].Body.Bytes()) {
			t.Fatal("repeat apply changed receipt")
		}
		w := call("GET", "/api/v1/candidates/"+due, nil, admin, false)
		check(w, 200)
		b := data(w)
		if b["full_name"] != "Контактные данные удалены" || b["email"] != "" || b["phone"] != "" || b["erased_at"] == nil || b["status"] != "archived" {
			t.Fatal("contacts were not erased")
		}
		check(call("PATCH", "/api/v1/candidates/"+due, map[string]string{"full_name": "Resurrect", "status": "active"}, admin, true), 404)
		check(call("PUT", "/api/v1/candidates/"+due+"/retention-hold", map[string]any{"hold": false}, admin, true), 404)
		check(call("GET", "/api/v1/invite/"+dueToken+"/attempt", nil, testSession{}, false), 404)
		check(call("POST", "/api/v1/invitations", map[string]any{"candidate_id": due, "test_version_id": version, "expires_at": time.Now().Add(time.Hour)}, admin, true), 422)
		w = call("GET", "/api/v1/results/"+attempt, nil, admin, false)
		check(w, 200)
		if data(w)["score_percent"] != "100.00" || bytes.Contains(w.Body.Bytes(), []byte("Erase fixture")) {
			t.Fatal("erasure changed result or leaked name")
		}
		var consents int
		owner.QueryRow(ctx, "SELECT count(*) FROM candidate_consents WHERE organization_id=$1 AND candidate_id=$2", org, due).Scan(&consents)
		if consents != 0 {
			t.Fatal("consent evidence retained")
		}
		var recipient, status string
		owner.QueryRow(ctx, "SELECT recipient,status FROM outbound_messages WHERE organization_id=$1 AND invitation_id=$2", org, dueInvite).Scan(&recipient, &status)
		if recipient != "redacted" || status != "cancelled" {
			t.Fatal("message retained recipient or remains pending")
		}
		for _, id := range []string{held, heldAfter, active, live, edited, later, foreign} {
			var erased *time.Time
			owner.QueryRow(ctx, "SELECT erased_at FROM candidates WHERE id=$1", id).Scan(&erased)
			if erased != nil {
				t.Fatal("protected/changed/non-preview/foreign candidate was erased")
			}
		}
		var deleted, survived, audits int
		owner.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE id=$1", oldEvent).Scan(&deleted)
		owner.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE id=$1", foreignEvent).Scan(&survived)
		owner.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE object_id=$1 AND action='retention.apply'", preview.ID).Scan(&audits)
		if deleted != 0 || survived != 1 || audits != 1 {
			t.Fatal("audit purge scope/idempotency/action record incorrect")
		}
	})
}
