package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PlatvaBestHorse/hr-testing/backend/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

func fixtureUUID() string {
	var b [16]byte
	rand.Read(b[:])
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func exerciseAttempts(t *testing.T, ctx context.Context, owner *pgxpool.Pool, app *App, call func(string, string, any, testSession, bool) *httptest.ResponseRecorder, admin, assessor, other testSession, org, assessorID string) {
	check := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			var b map[string]any
			json.Unmarshal(w.Body.Bytes(), &b)
			t.Fatalf("expected HTTP %d got %d code=%v", status, w.Code, b["code"])
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
	parse := func(w *httptest.ResponseRecorder) candidateAttempt {
		t.Helper()
		check(w, 200)
		var out candidateAttempt
		if json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatal("invalid attempt")
		}
		return out
	}
	sources := []testItemInput{}
	correct := map[string][]string{}
	categoryIDs := []string{}
	for _, name := range []string{"Report category A", "Report category B"} {
		w := call("POST", "/api/v1/categories", map[string]string{"name": name}, admin, true)
		check(w, 201)
		categoryIDs = append(categoryIDs, data(w)["id"].(string))
	}
	for n, kind := range []string{"single_choice", "verbal_tfu", "multiple_choice"} {
		q := questionInput{Body: "Passage " + kind, Type: kind, Difficulty: 2, Explanation: "PRIVATE ANSWER EXPLANATION", Options: []questionOption{{Label: "Yes", Correct: true}, {Label: "No"}}}
		q.CategoryID = categoryIDs[n%2]
		if kind == "verbal_tfu" {
			q.Passage = "Текст для оценки утверждения"
			q.Options = []questionOption{{Label: "Верно", Correct: true}, {Label: "Неверно"}, {Label: "Недостаточно информации"}}
		}
		if kind == "multiple_choice" {
			q.Options = []questionOption{{Label: "First", Correct: true}, {Label: "Second", Correct: true}, {Label: "Extra"}}
		}
		w := call("POST", "/api/v1/questions", q, admin, true)
		check(w, 201)
		qid := data(w)["id"].(string)
		w = call("GET", "/api/v1/questions/"+qid, nil, admin, false)
		check(w, 200)
		var record question
		json.Unmarshal(w.Body.Bytes(), &record)
		for _, o := range record.Options {
			if o.Correct {
				correct[kind] = append(correct[kind], o.ID)
			}
		}
		check(call("POST", "/api/v1/questions/"+qid+"/publish", map[string]int{"base_version_no": 1}, admin, true), 200)
		sources = append(sources, testItemInput{QuestionVersionID: record.VersionID, Points: []string{"2.50", "1.25", "0.25"}[n]})
	}
	createTest := func(show bool) string {
		in := testInput{Title: "Passage scoring fixture", Duration: 120, PassPercent: "62.50", ShowScore: show, ShuffleQuestions: true, ShuffleOptions: true, Items: sources}
		w := call("POST", "/api/v1/tests", in, admin, true)
		check(w, 201)
		id := data(w)["id"].(string)
		check(call("POST", "/api/v1/tests/"+id+"/publish", map[string]int{"base_version_no": 1}, admin, true), 200)
		return data(call("GET", "/api/v1/tests/"+id, nil, admin, false))["version_id"].(string)
	}
	version := createTest(true)
	w := call("POST", "/api/v1/candidates", map[string]string{"full_name": "Passage Candidate"}, admin, true)
	check(w, 201)
	candidate := data(w)["id"].(string)
	w = call("POST", "/api/v1/vacancies", map[string]any{"title": "Passage Vacancy", "assessor_ids": []string{assessorID}}, admin, true)
	check(w, 201)
	vacancy := data(w)["id"].(string)
	issue := func(version string, max int) (string, string) {
		w := call("POST", "/api/v1/invitations", map[string]any{"candidate_id": candidate, "test_version_id": version, "vacancy_id": vacancy, "max_attempts": max, "expires_at": time.Now().Add(time.Hour)}, admin, true)
		check(w, 201)
		b := data(w)
		return "/api/v1/invite/" + strings.TrimPrefix(b["path"].(string), "/i/"), b["invitation"].(map[string]any)["id"].(string)
	}
	url, invite := issue(version, 3)
	startBody := map[string]any{"request_id": fixtureUUID(), "consent": true}
	resumeBody := map[string]any{"request_id": fixtureUUID(), "consent": true, "new_attempt": true}
	var first candidateAttempt
	t.Run("consent origin concurrent start and immutable snapshot", func(t *testing.T) {
		check(call("POST", url+"/start", map[string]any{"request_id": fixtureUUID(), "consent": false}, testSession{}, false), 422)
		r := httptest.NewRequest("POST", url+"/start", strings.NewReader("{}"))
		r.Header.Set("Origin", "https://untrusted.invalid")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		Router(database.Readiness{Pool: app.Pool}, app).ServeHTTP(w, r)
		check(w, 403)
		var wg sync.WaitGroup
		responses := make([]*httptest.ResponseRecorder, 2)
		for n := range responses {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				responses[n] = call("POST", url+"/start", startBody, testSession{}, false)
			}(n)
		}
		wg.Wait()
		first = parse(responses[0])
		second := parse(responses[1])
		if first.ID != second.ID || !first.Deadline.Equal(second.Deadline) || len(first.Items) != 3 {
			t.Fatal("start race changed snapshot")
		}
		b1, _ := json.Marshal(first.Items)
		b2, _ := json.Marshal(second.Items)
		if !bytes.Equal(b1, b2) {
			t.Fatal("question or option order changed")
		}
		for _, secret := range []string{"is_correct", "explanation", "points", "pass_percent", "PRIVATE ANSWER"} {
			if bytes.Contains(responses[0].Body.Bytes(), []byte(secret)) {
				t.Fatal("candidate keys leaked")
			}
		}
		if first.Score != nil || first.Remaining != 2 {
			t.Fatal("premature score or wrong attempt limit")
		}
		resumed := parse(call("POST", url+"/start", resumeBody, testSession{}, false))
		if resumed.ID != first.ID {
			t.Fatal("concurrent resume created an attempt")
		}
		for _, item := range first.Items {
			if item.Type == "verbal_tfu" && (item.Options[0].Label != "Верно" || item.Options[2].Label != "Недостаточно информации") {
				t.Fatal("TFU order shuffled")
			}
		}
		var count int
		owner.QueryRow(ctx, "SELECT count(*) FROM candidate_consents WHERE organization_id=$1 AND candidate_id=$2", org, candidate).Scan(&count)
		if count != 1 {
			t.Fatal("consent must be recorded once")
		}
	})
	t.Run("active attempt report and export disclose no keys", func(t *testing.T) {
		check(call("GET", "/api/v1/results/"+first.ID, nil, admin, false), 409)
		check(call("GET", "/api/v1/results/"+first.ID+"/export", nil, admin, false), 409)
	})
	answerPath := func(a candidateAttempt, item candidateItem) string {
		return url + "/attempts/" + a.ID + "/items/" + item.ID + "/answer"
	}
	var receipt answerInput
	var single candidateItem
	for _, item := range first.Items {
		if item.Type == "single_choice" {
			single = item
		}
	}
	t.Run("answer validation receipts conflict and restart recovery", func(t *testing.T) {
		check(call("PUT", answerPath(first, single), answerInput{RequestID: fixtureUUID(), Selected: []string{correct["verbal_tfu"][0]}}, testSession{}, false), 422)
		check(call("PUT", answerPath(first, single), answerInput{RequestID: fixtureUUID(), Selected: []string{single.Options[0].ID, single.Options[1].ID}}, testSession{}, false), 422)
		check(call("PUT", answerPath(first, single), answerInput{RequestID: fixtureUUID(), Selected: []string{single.Options[0].ID, single.Options[0].ID}}, testSession{}, false), 422)
		receipt = answerInput{RequestID: fixtureUUID(), Selected: correct["single_choice"]}
		w := call("PUT", answerPath(first, single), receipt, testSession{}, false)
		check(w, 200)
		check(call("PUT", answerPath(first, single), receipt, testSession{}, false), 200)
		var revision int
		owner.QueryRow(ctx, "SELECT revision FROM attempt_answers WHERE organization_id=$1 AND attempt_item_id=$2", org, single.ID).Scan(&revision)
		if revision != 1 {
			t.Fatal("duplicate answer increments revision")
		}
		changed := receipt
		changed.Selected = []string{}
		check(call("PUT", answerPath(first, single), changed, testSession{}, false), 409)
		stale := receipt
		stale.RequestID = fixtureUUID()
		check(call("PUT", answerPath(first, single), stale, testSession{}, false), 409)
		// A fresh HTTP application instance restores from PostgreSQL, without client state.
		fresh := NewApp(app.Pool, false, []string{"http://127.0.0.1:8088"})
		r := httptest.NewRequest("GET", url+"/attempt", nil)
		w = httptest.NewRecorder()
		Router(database.Readiness{Pool: app.Pool}, fresh).ServeHTTP(w, r)
		restored := parse(w)
		if restored.ID != first.ID || !restored.Deadline.Equal(first.Deadline) {
			t.Fatal("restart reset attempt")
		}
		for n, item := range restored.Items {
			if item.ID != first.Items[n].ID {
				t.Fatal("question order changed")
			}
			if item.Type == "single_choice" && (item.Revision != 1 || !slices.Equal(item.Selected, correct["single_choice"])) {
				t.Fatal("confirmed answer not restored")
			}
		}
		foreign, _ := issue(version, 1)
		check(call("PUT", foreign+"/attempts/"+first.ID+"/items/"+single.ID+"/answer", receipt, testSession{}, false), 404)
		check(call("POST", foreign+"/attempts/"+first.ID+"/submit", map[string]any{}, testSession{}, false), 404)
	})
	t.Run("exact sets weighted scoring threshold and idempotent submit", func(t *testing.T) {
		for _, item := range first.Items {
			if item.Type == "multiple_choice" {
				partial := answerInput{RequestID: fixtureUUID(), Selected: correct[item.Type][:1]}
				check(call("PUT", answerPath(first, item), partial, testSession{}, false), 200)
				all := []string{}
				for _, o := range item.Options {
					all = append(all, o.ID)
				}
				check(call("PUT", answerPath(first, item), answerInput{RequestID: fixtureUUID(), Expected: 1, Selected: all}, testSession{}, false), 200)
			}
		}
		done := parse(call("POST", url+"/attempts/"+first.ID+"/submit", map[string]any{}, testSession{}, false))
		if done.Status != "submitted" || done.Score == nil || done.Score.Percent != "62.50" || !done.Score.Passed || len(done.Items) != 0 {
			t.Fatal("weighted score/threshold/final DTO incorrect")
		}
		again := parse(call("POST", url+"/attempts/"+first.ID+"/submit", map[string]any{}, testSession{}, false))
		if !again.Finished.Equal(*done.Finished) {
			t.Fatal("submit changed final time")
		}
		retriedResume := parse(call("POST", url+"/start", resumeBody, testSession{}, false))
		if retriedResume.ID != first.ID || retriedResume.Remaining != 2 {
			t.Fatal("retry of acknowledged resume consumed a new attempt")
		}
		check(call("PUT", answerPath(first, single), receipt, testSession{}, false), 200) // lost acknowledgement
		check(call("PUT", answerPath(first, single), answerInput{RequestID: fixtureUUID(), Expected: 1, Selected: []string{}}, testSession{}, false), 409)
		resumed := parse(call("POST", url+"/start", map[string]any{"request_id": fixtureUUID(), "consent": true}, testSession{}, false))
		if resumed.ID != first.ID {
			t.Fatal("reload silently starts another attempt")
		}
		var score, max string
		owner.QueryRow(ctx, "SELECT score_points::text,max_points::text FROM attempts WHERE id=$1", first.ID).Scan(&score, &max)
		if score != "2.50" || max != "4.00" {
			t.Fatal("stored weights incorrect")
		}
	})
	t.Run("reports filters exports and audit", func(t *testing.T) {
		exerciseReports(t, ctx, owner, call, admin, assessor, other, org, candidate, vacancy, version, first.ID, categoryIDs[0])
	})
	t.Run("partial multiple set gets zero and TFU earns its weight", func(t *testing.T) {
		partialURL, _ := issue(version, 1)
		a := parse(call("POST", partialURL+"/start", map[string]any{"request_id": fixtureUUID(), "consent": true}, testSession{}, false))
		for _, item := range a.Items {
			selected := correct[item.Type]
			if item.Type == "multiple_choice" {
				selected = selected[:1]
			}
			if item.Type == "single_choice" {
				for _, option := range item.Options {
					if option.ID != selected[0] {
						selected = []string{option.ID}
						break
					}
				}
			}
			check(call("PUT", partialURL+"/attempts/"+a.ID+"/items/"+item.ID+"/answer", answerInput{RequestID: fixtureUUID(), Selected: selected}, testSession{}, false), 200)
		}
		done := parse(call("POST", partialURL+"/attempts/"+a.ID+"/submit", map[string]any{}, testSession{}, false))
		if done.Score.Percent != "31.25" || done.Score.Passed {
			t.Fatal("partial multiple choice must earn zero; TFU must earn 1.25/4")
		}
	})
	t.Run("explicit retry complete correct sets and result authorization", func(t *testing.T) {
		body := map[string]any{"request_id": fixtureUUID(), "consent": true, "new_attempt": true}
		next := parse(call("POST", url+"/start", body, testSession{}, false))
		if next.ID == first.ID || next.Remaining != 1 {
			t.Fatal("explicit retry did not consume one attempt")
		}
		repeated := parse(call("POST", url+"/start", body, testSession{}, false))
		if repeated.ID != next.ID {
			t.Fatal("retry request not idempotent")
		}
		for _, item := range next.Items {
			check(call("PUT", answerPath(next, item), answerInput{RequestID: fixtureUUID(), Selected: correct[item.Type]}, testSession{}, false), 200)
		}
		done := parse(call("POST", url+"/attempts/"+next.ID+"/submit", map[string]any{}, testSession{}, false))
		if done.Score.Percent != "100.00" || !done.Score.Passed {
			t.Fatal("exact correct set must score full points")
		}
		for _, s := range []testSession{admin, assessor} {
			w := call("GET", "/api/v1/results", nil, s, false)
			check(w, 200)
			if !bytes.Contains(w.Body.Bytes(), []byte(next.ID)) {
				t.Fatal("authorized HR cannot see result")
			}
		}
		w := call("GET", "/api/v1/results", nil, other, false)
		check(w, 200)
		if bytes.Contains(w.Body.Bytes(), []byte(next.ID)) {
			t.Fatal("cross tenant result leaked")
		}
		check(call("GET", "/api/v1/results", nil, testSession{}, false), 401)
		// Removing vacancy assignment removes assessor access immediately.
		check(call("PATCH", "/api/v1/vacancies/"+vacancy, map[string]any{"title": "Passage Vacancy", "status": "open", "department": "", "assessor_ids": []string{}}, admin, true), 200)
		w = call("GET", "/api/v1/results", nil, assessor, false)
		if bytes.Contains(w.Body.Bytes(), []byte(next.ID)) {
			t.Fatal("unassigned assessor retains result access")
		}
		check(call("GET", "/api/v1/results/"+first.ID, nil, assessor, false), 404)
		check(call("GET", "/api/v1/results/"+first.ID+"/export?format=xlsx", nil, assessor, false), 404)
		w = call("GET", "/api/v1/results/export?format=csv&candidate_id="+candidate, nil, assessor, false)
		check(w, 200)
		if bytes.Contains(w.Body.Bytes(), []byte(first.ID)) {
			t.Fatal("unassigned assessor exported result")
		}
	})
	t.Run("deadline worker no browser frozen score and exhausted attempts", func(t *testing.T) {
		third := parse(call("POST", url+"/start", map[string]any{"request_id": fixtureUUID(), "consent": true, "new_attempt": true}, testSession{}, false))
		if _, err := owner.Exec(ctx, "UPDATE attempts SET started_at=clock_timestamp()-interval '121 seconds',deadline_at=clock_timestamp()-interval '1 second' WHERE id=$1", third.ID); err != nil {
			t.Fatal("deadline fixture failed")
		}
		if err := app.ExpireDue(ctx); err != nil {
			t.Fatal("expiry worker failed")
		}
		if err := app.ExpireDue(ctx); err != nil {
			t.Fatal("repeat expiry failed")
		}
		done := parse(call("GET", url+"/attempt", nil, testSession{}, false))
		if done.Status != "expired" || done.Score.Percent != "0.00" || done.Score.Passed {
			t.Fatal("expired unanswered test incorrect")
		}
		check(call("PUT", answerPath(third, third.Items[0]), answerInput{RequestID: fixtureUUID(), Selected: correct[third.Items[0].Type]}, testSession{}, false), 409)
		check(call("POST", url+"/start", map[string]any{"request_id": fixtureUUID(), "consent": true, "new_attempt": true}, testSession{}, false), 409)
		var count int
		owner.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE object_id=$1 AND action='attempt.expired'", third.ID).Scan(&count)
		if count != 1 {
			t.Fatal("expiry audit repeated")
		}
	})
	t.Run("late answer rejected after waiting for invitation lock", func(t *testing.T) {
		lateURL, lateInvite := issue(version, 1)
		a := parse(call("POST", lateURL+"/start", map[string]any{"request_id": fixtureUUID(), "consent": true}, testSession{}, false))
		blocker, err := owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback(context.Background())
		if _, err = blocker.Exec(ctx, "SELECT id FROM invitations WHERE id=$1 FOR UPDATE", lateInvite); err != nil {
			t.Fatal(err)
		}
		if _, err = blocker.Exec(ctx, "UPDATE attempts SET started_at=clock_timestamp()-interval '121 seconds',deadline_at=clock_timestamp()+interval '150 milliseconds' WHERE id=$1", a.ID); err != nil {
			t.Fatal(err)
		}
		response := make(chan *httptest.ResponseRecorder, 1)
		item := a.Items[0]
		go func() {
			response <- call("PUT", lateURL+"/attempts/"+a.ID+"/items/"+item.ID+"/answer", answerInput{RequestID: fixtureUUID(), Selected: correct[item.Type]}, testSession{}, false)
		}()
		time.Sleep(200 * time.Millisecond)
		if err = blocker.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		check(<-response, 409)
		done := parse(call("GET", lateURL+"/attempt", nil, testSession{}, false))
		if done.Status != "expired" || done.Score.Percent != "0.00" {
			t.Fatal("late answer changed score")
		}
	})
	t.Run("hidden score expiry access and revocation", func(t *testing.T) {
		hiddenURL, hiddenInvite := issue(createTest(false), 1)
		a := parse(call("POST", hiddenURL+"/start", map[string]any{"request_id": fixtureUUID(), "consent": true}, testSession{}, false))
		for _, item := range a.Items {
			check(call("PUT", hiddenURL+"/attempts/"+a.ID+"/items/"+item.ID+"/answer", answerInput{RequestID: fixtureUUID(), Selected: correct[item.Type]}, testSession{}, false), 200)
		}
		done := parse(call("POST", hiddenURL+"/attempts/"+a.ID+"/submit", map[string]any{}, testSession{}, false))
		if done.Score != nil {
			t.Fatal("hidden score leaked")
		}
		owner.Exec(ctx, "UPDATE invitations SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", hiddenInvite)
		restored := parse(call("GET", hiddenURL+"/attempt", nil, testSession{}, false))
		if restored.Score != nil || restored.Status != "submitted" {
			t.Fatal("finished result cannot restore after invitation expiry")
		}
		check(call("POST", "/api/v1/invitations/"+invite+"/revoke", nil, admin, true), 204)
		check(call("GET", url+"/attempt", nil, testSession{}, false), 404)
		revokedURL, revokedID := issue(version, 1)
		live := parse(call("POST", revokedURL+"/start", map[string]any{"request_id": fixtureUUID(), "consent": true}, testSession{}, false))
		check(call("POST", "/api/v1/invitations/"+revokedID+"/revoke", nil, admin, true), 204)
		var status string
		owner.QueryRow(ctx, "SELECT status FROM attempts WHERE id=$1", live.ID).Scan(&status)
		if status != "cancelled" {
			t.Fatal("revocation leaves active attempt")
		}
	})
}
