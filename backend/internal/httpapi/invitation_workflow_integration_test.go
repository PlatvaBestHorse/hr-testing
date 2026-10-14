package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func exerciseTestInvitations(t *testing.T, ctx context.Context, owner *pgxpool.Pool, call func(string, string, any, testSession, bool) *httptest.ResponseRecorder, admin, recruiter, assessor, other testSession, orgA, orgB, assessorID string) {
	check := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			var body map[string]any
			json.Unmarshal(w.Body.Bytes(), &body)
			t.Fatalf("expected HTTP %d, got %d (code=%v)", status, w.Code, body["code"])
		}
	}
	data := func(w *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal("invalid JSON response")
		}
		return out
	}
	q := questionInput{Body: "Builder fixture question", Difficulty: 2, Type: "single_choice", Options: []questionOption{{Label: "Correct", Correct: true}, {Label: "Wrong"}}}
	w := call("POST", "/api/v1/questions", q, recruiter, true)
	check(w, 201)
	qid := data(w)["id"].(string)
	w = call("GET", "/api/v1/questions/"+qid, nil, admin, false)
	check(w, 200)
	qvid := data(w)["version_id"].(string)
	t.Run("draft source and empty publication rejected", func(t *testing.T) {
		in := testInput{Title: "Draft source", Duration: 120, PassPercent: "70", Items: []testItemInput{{QuestionVersionID: qvid, Points: "1"}}}
		check(call("POST", "/api/v1/tests", in, admin, true), 422)
		in.Items = nil
		w := call("POST", "/api/v1/tests", in, admin, true)
		check(w, 201)
		id := data(w)["id"].(string)
		check(call("POST", "/api/v1/tests/"+id+"/publish", map[string]int{"base_version_no": 1}, admin, true), 422)
	})
	check(call("POST", "/api/v1/questions/"+qid+"/publish", map[string]int{"base_version_no": 1}, admin, true), 200)
	in := testInput{Title: "Invitation fixture test", Description: "Public description", Duration: 120, PassPercent: "70.00", Items: []testItemInput{{QuestionVersionID: qvid, Points: "2.50"}}, ShuffleQuestions: true}
	w = call("POST", "/api/v1/tests", in, recruiter, true)
	check(w, 201)
	testID := data(w)["id"].(string)
	w = call("GET", "/api/v1/tests/"+testID, nil, admin, false)
	check(w, 200)
	versionID := data(w)["version_id"].(string)
	var candidateID, vacancyID string
	t.Run("candidate and vacancy scoping", func(t *testing.T) {
		w := call("POST", "/api/v1/candidates", map[string]string{"full_name": "Fixture Candidate", "email": "candidate@fixture.test", "phone": "test"}, recruiter, true)
		check(w, 201)
		candidateID = data(w)["id"].(string)
		w = call("POST", "/api/v1/vacancies", map[string]any{"title": "Fixture Vacancy", "department": "Testing", "assessor_ids": []string{assessorID}}, admin, true)
		check(w, 201)
		vacancyID = data(w)["id"].(string)
		check(call("GET", "/api/v1/vacancies/"+vacancyID, nil, assessor, false), 200)
		check(call("GET", "/api/v1/candidates/"+candidateID, nil, assessor, false), 404)
		check(call("GET", "/api/v1/candidates/"+candidateID, nil, other, false), 404)
		check(call("GET", "/api/v1/vacancies/"+vacancyID, nil, other, false), 404)
		check(call("GET", "/api/v1/tests/"+testID, nil, other, false), 404)
		check(call("GET", "/api/v1/tests", nil, assessor, false), 403)
		check(call("POST", "/api/v1/candidates", map[string]string{"full_name": "No"}, assessor, true), 403)
		check(call("POST", "/api/v1/vacancies", map[string]any{"title": "No", "assessor_ids": []string{other.userID}}, admin, true), 422)
	})
	inv := map[string]any{"candidate_id": candidateID, "test_version_id": versionID, "vacancy_id": vacancyID, "max_attempts": 1, "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)}
	t.Run("invitation requires published own resources", func(t *testing.T) {
		check(call("POST", "/api/v1/invitations", inv, admin, true), 422)
		check(call("POST", "/api/v1/tests/"+testID+"/publish", map[string]int{"base_version_no": 1}, admin, true), 200)
		check(call("POST", "/api/v1/invitations", inv, other, true), 404)
		check(call("POST", "/api/v1/invitations", inv, assessor, true), 403)
		bad := map[string]any{}
		for k, v := range inv {
			bad[k] = v
		}
		bad["expires_at"] = time.Now().Add(-time.Minute).Format(time.RFC3339)
		check(call("POST", "/api/v1/invitations", bad, admin, true), 422)
		bad["expires_at"] = time.Now().Add(time.Hour).Format(time.RFC3339)
		bad["max_attempts"] = 21
		check(call("POST", "/api/v1/invitations", bad, admin, true), 422)
	})
	w = call("POST", "/api/v1/invitations", inv, recruiter, true)
	check(w, 201)
	issued := data(w)
	path := issued["path"].(string)
	invitationID := issued["invitation"].(map[string]any)["id"].(string)
	token := strings.TrimPrefix(path, "/i/")
	publicURL := "/api/v1/invite/" + token
	t.Run("public DTO and token storage", func(t *testing.T) {
		w := call("GET", publicURL, nil, testSession{}, false)
		check(w, 200)
		body := data(w)
		if body["title"] != in.Title || body["question_count"].(float64) != 1 {
			t.Fatal("wrong public invitation")
		}
		for _, field := range []string{"is_correct", "explanation", "pass_percent", "points", "candidate_id", "email", "phone", "token_hash", "options"} {
			if _, exists := body[field]; exists {
				t.Fatal("public invitation exposes private field")
			}
		}
		if bytes.Contains(w.Body.Bytes(), []byte("candidate@fixture.test")) || bytes.Contains(w.Body.Bytes(), []byte("Correct")) {
			t.Fatal("public response leaked contacts or answer keys")
		}
		var hash []byte
		var storedVersion string
		if err := owner.QueryRow(ctx, `SELECT token_hash,test_version_id FROM invitations WHERE organization_id=$1 AND id=$2`, orgA, invitationID).Scan(&hash, &storedVersion); err != nil {
			t.Fatal("cannot inspect invitation metadata")
		}
		if !bytes.Equal(hash, digest(token)) || storedVersion != versionID {
			t.Fatal("token must be hashed and bound to version")
		}
		w = call("GET", "/api/v1/invitations", nil, admin, false)
		check(w, 200)
		if bytes.Contains(w.Body.Bytes(), []byte(token)) || bytes.Contains(w.Body.Bytes(), []byte("token_hash")) {
			t.Fatal("history leaked invitation credential")
		}
		check(call("GET", "/api/v1/candidates/"+candidateID, nil, assessor, false), 200)
		check(call("GET", "/api/v1/invitations", nil, assessor, false), 403)
	})
	t.Run("published test configuration cannot change", func(t *testing.T) {
		_, err := owner.Exec(ctx, `UPDATE test_versions SET duration_seconds=60 WHERE organization_id=$1 AND id=$2`, orgA, versionID)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55000" {
			t.Fatal("published duration was mutable")
		}
		_, err = owner.Exec(ctx, `UPDATE test_items SET points=100 WHERE organization_id=$1 AND test_version_id=$2`, orgA, versionID)
		if !errors.As(err, &pgErr) || pgErr.Code != "55000" {
			t.Fatal("published points were mutable")
		}
		in.BaseVersion = 1
		in.Title = "New draft title"
		in.Duration = 180
		check(call("POST", "/api/v1/tests/"+testID+"/versions", in, admin, true), 201)
		w := call("GET", publicURL, nil, testSession{}, false)
		check(w, 200)
		if data(w)["title"] != "Invitation fixture test" || data(w)["duration_seconds"].(float64) != 120 {
			t.Fatal("invitation switched to newer version")
		}
		check(call("POST", "/api/v1/tests/"+testID+"/versions", in, admin, true), 409)
		for _, filter := range []string{"published_only=true", "status=published"} {
			w = call("GET", "/api/v1/tests?"+filter, nil, admin, false)
			check(w, 200)
			rows := data(w)["items"].([]any)
			if len(rows) != 1 {
				t.Fatal("published test disappeared after creating a draft")
			}
			row := rows[0].(map[string]any)
			if row["version_no"].(float64) != 1 || row["latest_version_no"].(float64) != 2 {
				t.Fatal("published and latest version metadata are incorrect")
			}
		}
	})
	t.Run("parallel test saves serialize", func(t *testing.T) {
		in.BaseVersion = 2
		codes := make(chan int, 2)
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				codes <- call("POST", "/api/v1/tests/"+testID+"/versions", in, admin, true).Code
			}()
		}
		wg.Wait()
		close(codes)
		counts := map[int]int{}
		for code := range codes {
			counts[code]++
		}
		if counts[201] != 1 || counts[409] != 1 {
			t.Fatal("parallel saves did not reject stale revision")
		}
	})
	t.Run("expiry and attempt limit", func(t *testing.T) {
		if _, err := owner.Exec(ctx, `UPDATE invitations SET expires_at=now()-interval '1 second' WHERE organization_id=$1 AND id=$2`, orgA, invitationID); err != nil {
			t.Fatal(err)
		}
		check(call("GET", publicURL, nil, testSession{}, false), 404)
		if _, err := owner.Exec(ctx, `UPDATE invitations SET expires_at=now()+interval '1 hour' WHERE organization_id=$1 AND id=$2`, orgA, invitationID); err != nil {
			t.Fatal(err)
		}
		var attemptID string
		if err := owner.QueryRow(ctx, `INSERT INTO attempts(organization_id,invitation_id,candidate_id,test_version_id,status,deadline_at) VALUES ($1,$2,$3,$4,'submitted',now()+interval '1 minute') RETURNING id`, orgA, invitationID, candidateID, versionID).Scan(&attemptID); err != nil {
			t.Fatal(err)
		}
		check(call("GET", publicURL, nil, testSession{}, false), 404)
		if _, err := owner.Exec(ctx, `UPDATE attempts SET status='in_progress' WHERE organization_id=$1 AND id=$2`, orgA, attemptID); err != nil {
			t.Fatal(err)
		}
		check(call("GET", publicURL, nil, testSession{}, false), 200)
		if _, err := owner.Exec(ctx, `DELETE FROM attempts WHERE organization_id=$1 AND id=$2`, orgA, attemptID); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("archive and explicit revocation", func(t *testing.T) {
		check(call("POST", "/api/v1/tests/"+testID+"/archive", nil, admin, true), 204)
		check(call("POST", "/api/v1/invitations", inv, admin, true), 422)
		check(call("GET", publicURL, nil, testSession{}, false), 200)
		check(call("POST", "/api/v1/invitations/"+invitationID+"/revoke", nil, other, true), 404)
		check(call("POST", "/api/v1/invitations/"+invitationID+"/revoke", nil, admin, true), 204)
		check(call("POST", "/api/v1/invitations/"+invitationID+"/revoke", nil, admin, true), 204)
		check(call("GET", publicURL, nil, testSession{}, false), 404)
		check(call("GET", "/api/v1/invite/invalid", nil, testSession{}, false), 404)
	})
	t.Run("unassigned assessor loses access", func(t *testing.T) {
		check(call("PATCH", "/api/v1/vacancies/"+vacancyID, map[string]any{"title": "Fixture Vacancy", "department": "Testing", "status": "open", "assessor_ids": []string{}}, admin, true), 200)
		check(call("GET", "/api/v1/vacancies/"+vacancyID, nil, assessor, false), 404)
		check(call("GET", "/api/v1/candidates/"+candidateID, nil, assessor, false), 404)
	})
}
