package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/PlatvaBestHorse/hr-testing/backend/internal/privacy"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func exercisePrivacy(t *testing.T, ctx context.Context, owner *pgxpool.Pool, app *App, call func(string, string, any, testSession, bool) *httptest.ResponseRecorder, admin, recruiter, assessor, other testSession, org, orgB string) {
	check := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			var b map[string]any
			json.Unmarshal(w.Body.Bytes(), &b)
			t.Fatalf("privacy status want %d got %d code=%v", status, w.Code, b["code"])
		}
	}
	data := func(w *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var b map[string]any
		if json.Unmarshal(w.Body.Bytes(), &b) != nil {
			t.Fatal("bad privacy response")
		}
		return b
	}
	candidate := func() string {
		w := call("POST", "/api/v1/candidates", map[string]string{"full_name": "Synthetic privacy fixture", "email": "privacy@fixture.test"}, admin, true)
		check(w, 201)
		return data(w)["id"].(string)
	}
	prepare := func(id string) map[string]any {
		w := call("POST", "/api/v1/candidates/"+id+"/erasure/preview", map[string]any{}, admin, true)
		check(w, 200)
		return data(w)
	}
	apply := func(id string, s testSession, csrf bool) *httptest.ResponseRecorder {
		return call("POST", "/api/v1/erasure/previews/"+id+"/apply", map[string]string{"confirmation": erasureConfirmation}, s, csrf)
	}
	q := questionInput{Body: "Synthetic privacy question", Type: "single_choice", Difficulty: 1, Options: []questionOption{{Label: "A", Correct: true}, {Label: "B"}}}
	w := call("POST", "/api/v1/questions", q, admin, true)
	check(w, 201)
	qid := data(w)["id"].(string)
	check(call("POST", "/api/v1/questions/"+qid+"/publish", map[string]int{"base_version_no": 1}, admin, true), 200)
	w = call("GET", "/api/v1/questions/"+qid, nil, admin, false)
	check(w, 200)
	qversion := data(w)["version_id"].(string)
	w = call("POST", "/api/v1/tests", testInput{Title: "Synthetic privacy test", Duration: 120, PassPercent: "70.00", Items: []testItemInput{{QuestionVersionID: qversion, Points: "1.00"}}}, admin, true)
	check(w, 201)
	testID := data(w)["id"].(string)
	check(call("POST", "/api/v1/tests/"+testID+"/publish", map[string]int{"base_version_no": 1}, admin, true), 200)
	w = call("GET", "/api/v1/tests/"+testID, nil, admin, false)
	check(w, 200)
	sharedVersion := data(w)["version_id"].(string)
	t.Run("erasure_roles_tenant_confirmation_stale_hold_and_replay", func(t *testing.T) {
		id := candidate()
		path := "/api/v1/candidates/" + id + "/erasure/preview"
		check(call("POST", path, map[string]any{}, testSession{}, false), 401)
		for _, s := range []testSession{recruiter, assessor} {
			check(call("POST", path, map[string]any{}, s, true), 403)
		}
		check(call("POST", path, map[string]any{}, other, true), 404)
		check(call("POST", path, map[string]any{}, admin, false), 403)
		first := prepare(id)["id"].(string)
		check(call("POST", "/api/v1/erasure/previews/"+first+"/apply", map[string]string{"confirmation": "no"}, admin, true), 422)
		check(apply(first, other, true), 404)
		check(apply(first, recruiter, true), 403)
		check(apply(first, admin, false), 403)
		check(call("PATCH", "/api/v1/candidates/"+id, map[string]string{"full_name": "Changed synthetic fixture"}, admin, true), 200)
		check(apply(first, admin, true), 409)
		hold := map[string]any{"hold": true, "reason": "Synthetic mandatory retention"}
		check(call("PUT", "/api/v1/candidates/"+id+"/retention-hold", hold, admin, true), 204)
		held := prepare(id)
		if held["retention_hold"] != true {
			t.Fatal("hold missing in preview")
		}
		check(apply(held["id"].(string), admin, true), 409)
		hold["hold"] = false
		check(call("PUT", "/api/v1/candidates/"+id+"/retention-hold", hold, admin, true), 204)
		expired := prepare(id)["id"].(string)
		if _, e := owner.Exec(ctx, "UPDATE erasure_previews SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", expired); e != nil {
			t.Fatal("cannot expire fixture")
		}
		check(apply(expired, admin, true), 409)
		next := prepare(id)["id"].(string)
		w := apply(next, admin, true)
		check(w, 200)
		again := apply(next, admin, true)
		check(again, 200)
		if w.Body.String() != again.Body.String() {
			t.Fatal("erasure receipt is not idempotent")
		}
		check(call("GET", "/api/v1/candidates/"+id, nil, admin, false), 404)
		var count int
		owner.QueryRow(ctx, "SELECT count(*) FROM candidate_erasures WHERE organization_id=$1 AND candidate_id=$2 AND scope='all'", org, id).Scan(&count)
		if count != 1 {
			t.Fatal("erasure journal missing/duplicated")
		}
		if _, e := app.Pool.Exec(ctx, "DELETE FROM candidate_erasures WHERE false"); e == nil {
			t.Fatal("app can delete erasure journal")
		}
	})
	t.Run("full_graph_active_passage_comments_and_shared_bank", func(t *testing.T) {
		id := candidate()
		version := sharedVersion
		w := call("POST", "/api/v1/invitations", map[string]any{"candidate_id": id, "test_version_id": version, "max_attempts": 2, "expires_at": time.Now().Add(time.Hour)}, admin, true)
		check(w, 201)
		link := data(w)["path"].(string)
		prefix := "/api/v1/invite/" + strings.TrimPrefix(link, "/i/")
		start := call("POST", prefix+"/start", map[string]any{"consent": true, "request_id": fixtureUUID()}, testSession{}, false)
		check(start, 200)
		aID := data(start)["id"].(string)
		items := data(start)["items"].([]any)
		item := items[0].(map[string]any)
		options := item["options"].([]any)
		answer := map[string]any{"request_id": fixtureUUID(), "expected_revision": 0, "selected_option_ids": []string{options[0].(map[string]any)["id"].(string)}}
		check(call("PUT", prefix+"/attempts/"+aID+"/items/"+item["id"].(string)+"/answer", answer, testSession{}, false), 200)
		// Synthetic fixture comment on active attempt also proves no free text survives.
		if _, e := owner.Exec(ctx, `INSERT INTO result_comments(organization_id,attempt_id,author_user_id,body) VALUES($1,$2,$3,'PRIVATE-CANDIDATE-FREE-TEXT')`, org, aID, admin.userID); e != nil {
			t.Fatal("cannot seed comment")
		}
		preview := prepare(id)
		if preview["active_attempts"].(float64) != 1 || preview["answers"].(float64) != 1 || preview["comments"].(float64) != 1 {
			t.Fatal("incorrect erasure counts")
		}
		// Any new comment invalidates the exact preview, even without a card edit.
		if _, e := owner.Exec(ctx, "UPDATE result_comments SET body='CHANGED-TEXT',updated_at=clock_timestamp() WHERE attempt_id=$1", aID); e != nil {
			t.Fatal("cannot edit fixture comment")
		}
		check(apply(preview["id"].(string), admin, true), 409)
		preview = prepare(id)
		check(apply(preview["id"].(string), admin, true), 200)
		check(call("GET", prefix+"/attempt", nil, testSession{}, false), 404)
		check(call("POST", prefix+"/start", map[string]any{"consent": true, "request_id": fixtureUUID()}, testSession{}, false), 404)
		check(call("GET", "/api/v1/results/"+aID, nil, admin, false), 404)
		for _, table := range []string{"result_comments", "attempts", "attempt_items", "attempt_answers", "answer_receipts", "start_receipts", "invitations", "candidate_consents", "outbound_messages"} {
			// IDs from this graph are gone; tables shared by other fixtures remain intact.
			var count int
			query := "SELECT count(*) FROM " + table + " WHERE organization_id=$1 AND "
			switch table {
			case "attempts", "invitations", "candidate_consents":
				query += "candidate_id=$2"
			case "result_comments", "attempt_items", "start_receipts":
				query += "attempt_id=$2"
			default:
				continue
			}
			key := id
			if table == "result_comments" || table == "attempt_items" || table == "start_receipts" {
				key = aID
			}
			if e := owner.QueryRow(ctx, query, org, key).Scan(&count); e != nil || count != 0 {
				t.Fatal("erasure graph retained rows in " + table)
			}
		}
		var intact bool
		owner.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM test_versions WHERE id=$1 AND status='published')", version).Scan(&intact)
		if !intact {
			t.Fatal("shared test removed")
		}
	})
	t.Run("erasure_rechecks_administrator_after_lock_wait", func(t *testing.T) {
		id := candidate()
		preview := prepare(id)["id"].(string)
		blocker, e := owner.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer blocker.Rollback(context.Background())
		defer owner.Exec(context.Background(), "UPDATE memberships SET role='org_admin' WHERE organization_id=$1 AND user_id=$2", org, admin.userID)
		if _, e = blocker.Exec(ctx, "SELECT id FROM organizations WHERE id=$1 FOR NO KEY UPDATE", org); e != nil {
			t.Fatal("cannot lock fixture organization")
		}
		result := make(chan *httptest.ResponseRecorder, 1)
		go func() { result <- apply(preview, admin, true) }()
		waiting := false
		for n := 0; n < 150; n++ {
			owner.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%organizations%' AND query LIKE '%FOR NO KEY UPDATE%')`).Scan(&waiting)
			if waiting {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !waiting {
			t.Fatal("erasure did not reach organization lock")
		}
		if _, e = blocker.Exec(ctx, "UPDATE memberships SET role='recruiter' WHERE organization_id=$1 AND user_id=$2", org, admin.userID); e != nil {
			t.Fatal("cannot change fixture role")
		}
		if e = blocker.Commit(ctx); e != nil {
			t.Fatal("cannot release role fixture")
		}
		select {
		case response := <-result:
			if response.Code != 401 {
				t.Fatal("erasure accepted revoked administrator")
			}
		case <-ctx.Done():
			t.Fatal("erasure blocked after role change")
		}
	})
	t.Run("concurrent_issuance_and_erasure_are_serialized", func(t *testing.T) {
		for _, issueFirst := range []bool{true, false} {
			id := candidate()
			preview := prepare(id)["id"].(string)
			blocker, e := owner.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			if e = privacy.LockCandidate(ctx, blocker, org, id); e != nil {
				blocker.Rollback(ctx)
				t.Fatal("cannot lock candidate fixture")
			}
			issued := make(chan *httptest.ResponseRecorder, 1)
			erased := make(chan *httptest.ResponseRecorder, 1)
			issue := func() {
				issued <- call("POST", "/api/v1/invitations", map[string]any{"candidate_id": id, "test_version_id": sharedVersion, "expires_at": time.Now().Add(time.Hour)}, admin, true)
			}
			erase := func() { erased <- apply(preview, admin, true) }
			wait := func(want int) {
				for n := 0; n < 150; n++ {
					var count int
					owner.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%candidate-privacy:%'`).Scan(&count)
					if count >= want {
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
				blocker.Rollback(ctx)
				t.Fatal("candidate operation did not wait for advisory lock")
			}
			if issueFirst {
				go issue()
			} else {
				go erase()
			}
			wait(1)
			if issueFirst {
				go erase()
			} else {
				go issue()
			}
			wait(2)
			if e = blocker.Commit(ctx); e != nil {
				t.Fatal("cannot release candidate fixture")
			}
			var i, d *httptest.ResponseRecorder
			select {
			case i = <-issued:
			case <-ctx.Done():
				t.Fatal("issue deadlock")
			}
			select {
			case d = <-erased:
			case <-ctx.Done():
				t.Fatal("erasure deadlock")
			}
			if issueFirst {
				if i.Code != 201 || d.Code != 409 {
					t.Fatal("new invitation was ignored by erasure preview")
				}
				check(apply(prepare(id)["id"].(string), admin, true), 200)
			} else {
				if d.Code != 200 || i.Code != 404 {
					t.Fatal("invitation created after full erasure")
				}
			}
		}
	})
	t.Run("security_and_recovery_ip_budgets_do_not_interfere", func(t *testing.T) {
		ip := "203.0.113.53"
		for n := 0; n < 25; n++ {
			if e := app.limitAccount(ctx, "security:synthetic", ip, 30, 90); e != nil {
				t.Fatal("security budget unexpectedly exhausted")
			}
		}
		for n := 0; n < 3; n++ {
			if e := app.limitAccount(ctx, "recovery:synthetic", ip, 3, 20); e != nil {
				t.Fatal("security calls consumed recovery IP budget")
			}
		}
		if e := app.limitAccount(ctx, "recovery:synthetic", ip, 3, 20); !errors.Is(e, errLimited) {
			t.Fatal("per-account recovery budget disappeared")
		}
		for n := 4; n < 20; n++ {
			if e := app.limitAccount(ctx, "recovery:"+fixtureUUID(), ip, 3, 20); e != nil {
				t.Fatal("recovery IP budget ended too soon")
			}
		}
		if e := app.limitAccount(ctx, "recovery:another", ip, 3, 20); !errors.Is(e, errLimited) {
			t.Fatal("recovery IP budget not enforced")
		}
		if e := app.limitAccount(ctx, "reset:synthetic", ip, 8, 40); e != nil {
			t.Fatal("reset budget shares recovery counter")
		}
	})
	t.Run("https_requires_approved_documents_before_new_passage", func(t *testing.T) {
		id := candidate()
		w := call("POST", "/api/v1/invitations", map[string]any{"candidate_id": id, "test_version_id": sharedVersion, "expires_at": time.Now().Add(time.Hour)}, admin, true)
		check(w, 201)
		prefix := "/api/v1/invite/" + strings.TrimPrefix(data(w)["path"].(string), "/i/")
		app.SecureCookie = true
		defer func() { app.SecureCookie = false }()
		check(call("POST", prefix+"/start", map[string]any{"request_id": fixtureUUID(), "consent": true, "consent_revision": 0}, testSession{}, false), 503)
	})
	t.Run("consent_revision_immutable_evidence_and_scoping", func(t *testing.T) {
		check(call("GET", "/api/v1/privacy", nil, recruiter, false), 403)
		doc := map[string]any{"operator_name": "Synthetic operator", "contact": "privacy@fixture.test", "consent_text": "Synthetic consent v1", "policy_text": "Synthetic policy v1", "approved": true, "base_revision": 0}
		check(call("PATCH", "/api/v1/privacy", doc, admin, false), 403)
		check(call("PATCH", "/api/v1/privacy", doc, other, true), 200)
		check(call("PATCH", "/api/v1/privacy", doc, admin, true), 200)
		check(call("PATCH", "/api/v1/privacy", doc, admin, true), 409)
		if _, e := app.Pool.Exec(ctx, "UPDATE consent_documents SET approved=false WHERE false"); e == nil {
			t.Fatal("published consent mutable")
		}
		id := candidate()
		version := sharedVersion
		w := call("POST", "/api/v1/invitations", map[string]any{"candidate_id": id, "test_version_id": version, "expires_at": time.Now().Add(time.Hour)}, admin, true)
		check(w, 201)
		prefix := "/api/v1/invite/" + strings.TrimPrefix(data(w)["path"].(string), "/i/")
		w = call("GET", prefix, nil, testSession{}, false)
		check(w, 200)
		if data(w)["consent_document"].(map[string]any)["consent_text"] != "Synthetic consent v1" {
			t.Fatal("wrong public consent")
		}
		body := map[string]any{"request_id": fixtureUUID(), "consent": true, "consent_revision": 0}
		check(call("POST", prefix+"/start", body, testSession{}, false), 409)
		body["consent_revision"] = 1
		w = call("POST", prefix+"/start", body, testSession{}, false)
		check(w, 200)
		var rev int
		if e := owner.QueryRow(ctx, `SELECT d.revision FROM candidate_consents s JOIN consent_documents d ON d.organization_id=s.organization_id AND d.id=s.consent_document_id WHERE s.organization_id=$1 AND s.candidate_id=$2`, org, id).Scan(&rev); e != nil || rev != 1 {
			t.Fatal("consent version evidence missing")
		}
		doc["base_revision"] = 1
		doc["consent_text"] = "Synthetic consent v2"
		check(call("PATCH", "/api/v1/privacy", doc, admin, true), 200)
		// Retrying an acknowledged start keeps its original consent and attempt.
		check(call("POST", prefix+"/start", body, testSession{}, false), 200)
	})
}
