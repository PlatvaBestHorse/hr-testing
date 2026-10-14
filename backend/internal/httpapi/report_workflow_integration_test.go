package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func exerciseReports(t *testing.T, ctx context.Context, owner *pgxpool.Pool, call func(string, string, any, testSession, bool) *httptest.ResponseRecorder, admin, assessor, other testSession, org, candidate, vacancy, version, attempt, category string) {
	check := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			var b map[string]any
			json.Unmarshal(w.Body.Bytes(), &b)
			t.Fatalf("expected HTTP %d got %d code=%v", status, w.Code, b["code"])
		}
	}
	report := func(s testSession) attemptReport {
		w := call("GET", "/api/v1/results/"+attempt, nil, s, false)
		check(w, 200)
		var out attemptReport
		json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	t.Run("detail scoring categories snapshot and actor audit", func(t *testing.T) {
		check(call("PATCH", "/api/v1/categories/"+category, map[string]string{"name": "Renamed report category"}, admin, true), 200)
		for _, s := range []testSession{admin, assessor} {
			out := report(s)
			if len(out.Questions) != 3 || len(out.Categories) != 2 || out.Correct != 1 || out.Answered != 2 || !out.CategoriesFrozen {
				t.Fatal("wrong detailed counts")
			}
			for _, c := range out.Categories {
				if c.ID == category && (c.Name != "Report category A" || c.Earned != "2.50" || c.Maximum != "2.75" || c.Percent != "90.91") {
					t.Fatal("category changed or weighted category scoring incorrect")
				}
			}
			for _, q := range out.Questions {
				if q.Type == "multiple_choice" && (q.Correct || q.Earned != "0.00") {
					t.Fatal("extra multiple option earned points")
				}
				if q.Type == "verbal_tfu" && q.Answered {
					t.Fatal("skipped question marked answered")
				}
				if q.Explanation != "PRIVATE ANSWER EXPLANATION" {
					t.Fatal("HR explanation missing")
				}
			}
		}
		var count int
		if err := owner.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE organization_id=$1 AND object_id=$2 AND action='result.view' AND actor_user_id IS NOT NULL", org, attempt).Scan(&count); err != nil || count != 2 {
			t.Fatal("report view not audited")
		}
	})
	t.Run("same scope for detail and all export endpoints", func(t *testing.T) {
		for _, path := range []string{"/api/v1/results/" + attempt, "/api/v1/results/" + attempt + "/export?format=csv", "/api/v1/results/" + attempt + "/export?format=xlsx"} {
			check(call("GET", path, nil, other, false), 404)
			check(call("GET", path, nil, testSession{}, false), 401)
		}
		w := call("GET", "/api/v1/results/export?candidate_id="+candidate, nil, other, false)
		check(w, 200)
		if bytes.Contains(w.Body.Bytes(), []byte(attempt)) {
			t.Fatal("cross tenant bulk export leaked result")
		}
		check(call("GET", "/api/v1/results/export", nil, testSession{}, false), 401)
		check(call("GET", "/api/v1/results/invalid", nil, admin, false), 404)
	})
	t.Run("CSV XLSX contents formula safety and export audit", func(t *testing.T) {
		if _, err := owner.Exec(ctx, "UPDATE candidates SET full_name=$2 WHERE id=$1", candidate, "=HYPERLINK(\"https://example.test\")"); err != nil {
			t.Fatal(err)
		}
		for _, s := range []testSession{admin, assessor} {
			w := call("GET", "/api/v1/results/export?format=csv&candidate_id="+candidate+"&status=submitted", nil, s, false)
			check(w, 200)
			r := csv.NewReader(strings.NewReader(strings.TrimPrefix(w.Body.String(), "\uFEFF")))
			r.Comma = ';'
			rows, err := r.ReadAll()
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 2 || rows[1][0] != attempt || !strings.HasPrefix(rows[1][1], "'=") || rows[1][11] != "62,50" {
				t.Fatal("CSV content/scope/formula safety incorrect")
			}
			if !strings.Contains(w.Header().Get("Content-Disposition"), "results.csv") || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("unsafe export response")
			}
		}
		w := call("GET", "/api/v1/results/"+attempt+"/export?format=xlsx", nil, admin, false)
		check(w, 200)
		z, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
		if err != nil {
			t.Fatal("invalid XLSX archive")
		}
		worksheets := 0
		for _, file := range z.File {
			if !strings.HasPrefix(file.Name, "xl/worksheets/") {
				continue
			}
			worksheets++
			r, _ := file.Open()
			b, _ := io.ReadAll(r)
			r.Close()
			if bytes.Contains(b, []byte("<f>")) {
				t.Fatal("formula in XLSX")
			}
			if file.Name == "xl/worksheets/sheet3.xml" && !bytes.Contains(b, []byte("PRIVATE ANSWER EXPLANATION")) {
				t.Fatal("XLSX detail missing")
			}
		}
		if worksheets != 3 {
			t.Fatal("detail workbook must have summary, categories and answers")
		}
		w = call("GET", "/api/v1/results/"+attempt+"/export?format=csv", nil, admin, false)
		check(w, 200)
		if !bytes.Contains(w.Body.Bytes(), []byte("Правильный ответ")) {
			t.Fatal("detail CSV missing answers")
		}
		var count int
		if err = owner.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE organization_id=$1 AND action='result.export' AND actor_user_id=$2", org, admin.userID).Scan(&count); err != nil || count < 3 {
			t.Fatal("export not audited")
		}
	})
	t.Run("filter validation literal search and Moscow date boundaries", func(t *testing.T) {
		for _, query := range []string{"status=unknown", "passed=yes", "date_from=2026-02-30", "date_from=2026-10-15&date_to=2026-10-14", "candidate_id=invalid", "page=-1"} {
			check(call("GET", "/api/v1/results?"+query, nil, admin, false), 400)
		}
		check(call("GET", "/api/v1/results/export?format=pdf", nil, admin, false), 400)
		w := call("GET", "/api/v1/results?search=%25", nil, admin, false)
		check(w, 200)
		if bytes.Contains(w.Body.Bytes(), []byte(attempt)) {
			t.Fatal("search wildcard should be literal")
		}
		today := time.Now().In(moscow).Format("2006-01-02")
		midnight, _ := time.ParseInLocation("2006-01-02", today, moscow)
		_, err := owner.Exec(ctx, "UPDATE attempts SET started_at=$2 WHERE id=$1", attempt, midnight)
		if err != nil {
			t.Fatal(err)
		}
		path := "/api/v1/results/export?candidate_id=" + candidate + "&date_from=" + today + "&date_to=" + today
		w = call("GET", path, nil, admin, false)
		check(w, 200)
		if !bytes.Contains(w.Body.Bytes(), []byte(attempt)) {
			t.Fatal("Moscow midnight excluded")
		}
		_, err = owner.Exec(ctx, "UPDATE attempts SET started_at=$2 WHERE id=$1", attempt, midnight.Add(-time.Second))
		if err != nil {
			t.Fatal(err)
		}
		w = call("GET", path, nil, admin, false)
		check(w, 200)
		if bytes.Contains(w.Body.Bytes(), []byte(attempt)) {
			t.Fatal("previous Moscow day included")
		}
		w = call("GET", "/api/v1/results?candidate_id="+candidate+"&passed=false", nil, admin, false)
		check(w, 200)
		if bytes.Contains(w.Body.Bytes(), []byte(attempt)) {
			t.Fatal("passed filter ignored")
		}
	})
	t.Run("legacy category fallback explicitly identified", func(t *testing.T) {
		if _, err := owner.Exec(ctx, "UPDATE attempt_items SET category_snapshot=NULL WHERE organization_id=$1 AND attempt_id=$2", org, attempt); err != nil {
			t.Fatal(err)
		}
		out := report(admin)
		if out.CategoriesFrozen || textPointer(out.Percent) != "62.50" {
			t.Fatal("legacy categories must be identified without altering score")
		}
	})
	t.Run("bulk export all pages exact limit and no truncation", func(t *testing.T) {
		var bulkCandidate string
		if err := owner.QueryRow(ctx, "INSERT INTO candidates(organization_id,full_name) VALUES($1,'Export volume fixture') RETURNING id", org).Scan(&bulkCandidate); err != nil {
			t.Fatal(err)
		}
		var invites []string
		err := owner.QueryRow(ctx, `WITH inserted AS (INSERT INTO invitations(organization_id,candidate_id,test_version_id,vacancy_id,token_hash,max_attempts,expires_at)
  SELECT $1,$2,$3,$4,convert_to(gen_random_uuid()::text,'UTF8'),20,now()+interval '1 day' FROM generate_series(1,501) RETURNING id)
  SELECT array_agg(id) FROM inserted`, org, bulkCandidate, version, vacancy).Scan(&invites)
		if err != nil {
			t.Fatal(err)
		}
		_, err = owner.Exec(ctx, `INSERT INTO attempts(organization_id,invitation_id,candidate_id,test_version_id,attempt_no,status,deadline_at,finished_at,score_points,max_points,score_percent,passed)
  SELECT $1,i,$2,$3,n,'submitted',now()+interval '1 minute',now(),0,4,0,false FROM unnest($4::uuid[]) i CROSS JOIN generate_series(1,20) n`, org, bulkCandidate, version, invites)
		if err != nil {
			t.Fatal(err)
		}
		path := "/api/v1/results/export?candidate_id=" + bulkCandidate
		check(call("GET", path, nil, admin, false), 422)
		if _, err = owner.Exec(ctx, "DELETE FROM attempts WHERE organization_id=$1 AND invitation_id=$2", org, invites[0]); err != nil {
			t.Fatal(err)
		}
		w := call("GET", path, nil, admin, false)
		check(w, 200)
		r := csv.NewReader(strings.NewReader(strings.TrimPrefix(w.Body.String(), "\uFEFF")))
		r.Comma = ';'
		rows, err := r.ReadAll()
		if err != nil || len(rows) != 10001 {
			t.Fatal("export should contain all 10000 results, not one page")
		}
		if _, err = owner.Exec(ctx, "DELETE FROM attempts WHERE organization_id=$1 AND candidate_id=$2", org, bulkCandidate); err != nil {
			t.Fatal(err)
		}
		if _, err = owner.Exec(ctx, "DELETE FROM invitations WHERE organization_id=$1 AND candidate_id=$2", org, bulkCandidate); err != nil {
			t.Fatal(err)
		}
	})
}
