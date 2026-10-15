package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func exerciseLegacyImport(t *testing.T, ctx context.Context, owner *pgxpool.Pool, call func(string, string, any, testSession, bool) *httptest.ResponseRecorder, admin, recruiter, assessor, other testSession, org, otherOrg string) {
	raw := legacyFixture(t)
	// Optional supplied bank is checked end to end only in temporary organizations.
	if file := os.Getenv("LEGACY_IMPORT_SAMPLE"); file != "" {
		var err error
		raw, err = os.ReadFile(file)
		if err != nil {
			t.Fatal("cannot read supplied import sample")
		}
	}
	doc, expected := parseLegacy(raw)
	if !expected.Valid {
		t.Fatalf("invalid import sample: %+v", expected.Errors)
	}
	payload := func(hash string) map[string]any {
		return map[string]any{"source": json.RawMessage(raw), "preview_sha256": hash, "confirmation": "ИМПОРТИРОВАТЬ"}
	}
	check := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			var e map[string]any
			json.Unmarshal(w.Body.Bytes(), &e)
			t.Fatalf("import expected %d got %d code=%v", status, w.Code, e["code"])
		}
	}
	t.Run("preview_roles_validation_and_no_content_write", func(t *testing.T) {
		for _, s := range []testSession{recruiter, assessor} {
			check(call("POST", "/api/v1/import/questions/preview", payload(""), s, true), 403)
		}
		check(call("POST", "/api/v1/import/questions/preview", payload(""), admin, false), 403)
		w := call("POST", "/api/v1/import/questions/preview", payload(""), admin, true)
		check(w, 200)
		var got importPreview
		json.Unmarshal(w.Body.Bytes(), &got)
		if !got.Valid || got.Hash != expected.Hash || got.Questions != len(doc.Questions) {
			t.Fatal("preview mismatch")
		}
		var n int
		if err := owner.QueryRow(ctx, "SELECT count(*) FROM imported_questions WHERE organization_id=$1", org).Scan(&n); err != nil || n != 0 {
			t.Fatal("preview wrote question mappings")
		}
		bad := json.RawMessage(`{"version":1,"version":2}`)
		w = call("POST", "/api/v1/import/questions/preview", map[string]any{"source": bad}, admin, true)
		check(w, 200)
		json.Unmarshal(w.Body.Bytes(), &got)
		if got.Valid || len(got.Errors) == 0 {
			t.Fatal("duplicate keys accepted")
		}
		check(call("POST", "/api/v1/import/questions/apply", payload(strings.Repeat("0", 64)), admin, true), 409)
	})
	var saved importReceipt
	t.Run("concurrent_apply_preserves_all_source_fields", func(t *testing.T) {
		var replies [2]*httptest.ResponseRecorder
		var wg sync.WaitGroup
		for i := range replies {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				replies[i] = call("POST", "/api/v1/import/questions/apply", payload(expected.Hash), admin, true)
			}(i)
		}
		wg.Wait()
		statuses := map[int]int{}
		for _, w := range replies {
			statuses[w.Code]++
			var receipt importReceipt
			if json.Unmarshal(w.Body.Bytes(), &receipt) != nil || receipt.Hash != expected.Hash {
				t.Fatal("invalid receipt")
			}
			if saved.ID != "" && !reflect.DeepEqual(saved, receipt) {
				t.Fatal("repeat changed receipt")
			}
			saved = receipt
		}
		if statuses[201] != 1 || statuses[200] != 1 || len(saved.Questions) != len(doc.Questions) || len(saved.Tests) != len(doc.Tests) {
			t.Fatal("non-idempotent apply")
		}
		for i, q := range doc.Questions {
			w := call("GET", "/api/v1/questions/"+saved.Questions[i].ID, nil, admin, false)
			check(w, 200)
			var got question
			json.Unmarshal(w.Body.Bytes(), &got)
			if got.Body != strings.TrimSpace(q.Text) || got.CategoryName != strings.TrimSpace(q.Category) || got.Difficulty != legacyDifficulty[q.Difficulty] || got.Passage != strings.TrimSpace(q.Passage) || got.Explanation != strings.TrimSpace(q.Explanation) || got.Type != "single_choice" || got.VersionStatus != "published" || len(got.Options) != len(q.Choices) {
				t.Fatal("question content changed")
			}
			for j, c := range q.Choices {
				if got.Options[j].Label != strings.TrimSpace(c.Text) || got.Options[j].Correct != (c.ID == q.Correct) {
					t.Fatal("answer key changed")
				}
			}
			check(call("GET", "/api/v1/questions/"+got.ID, nil, other, false), 404)
		}
		for i, test := range doc.Tests {
			w := call("GET", "/api/v1/tests/"+saved.Tests[i].ID, nil, admin, false)
			check(w, 200)
			var got testRecord
			json.Unmarshal(w.Body.Bytes(), &got)
			if got.Title != test.Title || got.Duration != test.Minutes*60 || got.ShowScore != *test.ShowScore || got.ShuffleQuestions != *test.Shuffle || got.ShuffleOptions || len(got.Items) != len(test.QuestionIDs) || (got.VersionStatus == "published") != *test.Published {
				t.Fatal("test settings changed")
			}
			cents, _ := decimalCents(got.PassPercent, 0, 10000)
			want, _ := decimalCents(strconv.FormatFloat(*test.Passing, 'f', 2, 64), 0, 10000)
			if cents != want {
				t.Fatal("passing threshold changed")
			}
			for j, item := range got.Items {
				if item.Points != "1.00" || savedID(saved.Questions, test.QuestionIDs[j]) != item.QuestionID {
					t.Fatal("test order or weight changed")
				}
			}
		}
	})
	t.Run("duplicate_preview_and_changed_source_conflict", func(t *testing.T) {
		w := call("POST", "/api/v1/import/questions/preview", payload(""), admin, true)
		check(w, 200)
		var p importPreview
		json.Unmarshal(w.Body.Bytes(), &p)
		if !p.AlreadyApplied {
			t.Fatal("repeat not detected")
		}
		changed := doc
		changed.Questions = append([]legacyQuestion{}, doc.Questions...)
		changed.Questions[0].Text += " changed"
		b, _ := json.Marshal(changed)
		_, p = parseLegacy(b)
		in := map[string]any{"source": json.RawMessage(b), "preview_sha256": p.Hash, "confirmation": "ИМПОРТИРОВАТЬ"}
		check(call("POST", "/api/v1/import/questions/apply", in, admin, true), 409)
		w = call("POST", "/api/v1/import/questions/apply", payload(expected.Hash), other, true)
		check(w, 201)
		var foreign importReceipt
		json.Unmarshal(w.Body.Bytes(), &foreign)
		if foreign.ID == saved.ID || foreign.Questions[0].ID == saved.Questions[0].ID {
			t.Fatal("cross-tenant mapping reused")
		}
		var n int
		if err := owner.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE organization_id=$1 AND action='import.apply'", org).Scan(&n); err != nil || n != 1 {
			t.Fatal("repeat duplicated audit")
		}
	})
}
func savedID(xs []importObject, id string) string {
	for _, x := range xs {
		if x.SourceID == id {
			return x.ID
		}
	}
	return ""
}
