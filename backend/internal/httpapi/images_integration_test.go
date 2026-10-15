package httpapi

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/PlatvaBestHorse/hr-testing/backend/internal/database"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func exerciseImages(t *testing.T, ctx context.Context, owner *pgxpool.Pool, app *App, call func(string, string, any, testSession, bool) *httptest.ResponseRecorder, admin, recruiter, assessor, other testSession, org, foreignOrg, assessorID string) {
	handler := Router(database.Readiness{Pool: app.Pool}, app)
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
	upload := func(raw []byte, kind string, s testSession, csrf, extra bool) *httptest.ResponseRecorder {
		var b bytes.Buffer
		writer := multipart.NewWriter(&b)
		head := textproto.MIMEHeader{}
		head.Set("Content-Disposition", `form-data; name="file"; filename="private-original-name.png"`)
		head.Set("Content-Type", kind)
		part, _ := writer.CreatePart(head)
		part.Write(raw)
		if extra {
			writer.WriteField("organization_id", foreignOrg)
		}
		writer.Close()
		r := httptest.NewRequest("POST", "/api/v1/images", &b)
		r.Header.Set("Content-Type", writer.FormDataContentType())
		r.Header.Set("Origin", "http://127.0.0.1:8088")
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
	var imageID, optionID, unusedID, foreignID string
	t.Run("safe_upload_roles_CSRF_signatures_limits_and_private_delivery", func(t *testing.T) {
		check(upload(fixturePNG(), "image/png", testSession{}, false, false), 401)
		check(upload(fixturePNG(), "image/png", assessor, true, false), 403)
		check(upload(fixturePNG(), "image/png", admin, false, false), 403)
		check(call("POST", "/api/v1/images", map[string]string{"file": "fake"}, admin, true), 415)
		check(upload([]byte("svg"), "image/svg+xml", admin, true, false), 415)
		check(upload([]byte("invalid"), "image/png", admin, true, false), 422)
		check(upload(fixturePNG(), "image/jpeg", admin, true, false), 422)
		check(upload(make([]byte, maxImageBytes+1), "image/png", admin, true, false), 413)
		check(upload(fixturePNG(), "image/png", admin, true, true), 400)
		app.imageSlots <- struct{}{}
		app.imageSlots <- struct{}{}
		check(upload(fixturePNG(), "image/png", admin, true, false), 503)
		<-app.imageSlots
		<-app.imageSlots
		for n, s := range []testSession{admin, recruiter, admin, other} {
			w := upload(append(fixturePNG(), []byte("TRAILING-PRIVATE-METADATA")...), "image/png", s, true, false)
			check(w, 201)
			b := data(w)
			if b["width"] != float64(8) || b["height"] != float64(6) || strings.Contains(w.Body.String(), "private-original-name") {
				t.Fatal("metadata mismatch or filename disclosed")
			}
			id := b["id"].(string)
			switch n {
			case 0:
				imageID = id
			case 1:
				optionID = id
			case 2:
				unusedID = id
			case 3:
				foreignID = id
			}
		}
		w := call("GET", "/api/v1/images/"+imageID, nil, recruiter, false)
		check(w, 200)
		if w.Header().Get("Content-Type") != "image/png" || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Referrer-Policy") != "no-referrer" || w.Header().Get("Cross-Origin-Resource-Policy") != "same-origin" {
			t.Fatal("unsafe image response")
		}
		if bytes.Contains(w.Body.Bytes(), []byte("TRAILING-PRIVATE-METADATA")) {
			t.Fatal("metadata retained")
		}
		var body []byte
		var hash string
		if e := owner.QueryRow(ctx, "SELECT data,sha256 FROM attachments WHERE organization_id=$1 AND id=$2", org, imageID).Scan(&body, &hash); e != nil || !bytes.Equal(body, w.Body.Bytes()) || hash != hex.EncodeToString(digest(string(body))) {
			t.Fatal("stored raster mismatch")
		}
		check(call("GET", "/api/v1/images/"+imageID, nil, other, false), 404)
		check(call("GET", "/api/v1/images/"+imageID, nil, assessor, false), 403)
		check(call("GET", "/api/v1/images/"+imageID, nil, testSession{}, false), 401)
		check(call("GET", "/api/v1/images", nil, assessor, false), 403)
		check(call("GET", "/api/v1/images?page=0", nil, admin, false), 400)
	})
	q := questionInput{Body: "Image fixture question", Explanation: "PRIVATE IMAGE EXPLANATION", Difficulty: 2, Type: "single_choice", ImageID: imageID, Options: []questionOption{{Label: "A", Correct: true, ImageID: optionID}, {Label: "B"}}}
	var questionID, versionID string
	t.Run("question_links_are_tenant_checked_and_immutable", func(t *testing.T) {
		bad := q
		bad.ImageID = foreignID
		check(call("POST", "/api/v1/questions", bad, admin, true), 422)
		bad.ImageID = fixtureUUID()
		check(call("POST", "/api/v1/questions", bad, admin, true), 422)
		bad = q
		bad.Options = append([]questionOption{}, q.Options...)
		bad.Options[0].ImageID = foreignID
		check(call("POST", "/api/v1/questions", bad, admin, true), 422)
		w := call("POST", "/api/v1/questions", q, admin, true)
		check(w, 201)
		questionID = data(w)["id"].(string)
		w = call("GET", "/api/v1/questions/"+questionID, nil, admin, false)
		check(w, 200)
		b := data(w)
		versionID = b["version_id"].(string)
		if b["image_id"] != imageID || b["options"].([]any)[0].(map[string]any)["image_id"] != optionID {
			t.Fatal("image references not persisted")
		}
		check(call("POST", "/api/v1/questions/"+questionID+"/publish", map[string]int{"base_version_no": 1}, admin, true), 200)
		check(call("DELETE", "/api/v1/images/"+imageID, nil, admin, true), 409)
		check(call("DELETE", "/api/v1/images/"+optionID, nil, admin, true), 409)
		if _, e := app.Pool.Exec(ctx, "UPDATE attachments SET data=data WHERE id=$1", imageID); e == nil {
			t.Fatal("app changed immutable attachment")
		}
		if _, e := owner.Exec(ctx, "UPDATE attachments SET width=width WHERE id=$1", imageID); e == nil {
			t.Fatal("attachment trigger allows changes")
		}
		if _, e := app.Pool.Exec(ctx, "UPDATE question_versions SET image_id=$2 WHERE id=$1", versionID, unusedID); e == nil {
			t.Fatal("published image reference changed")
		}
		bad = q
		bad.BaseVersion = 1
		bad.ImageID = unusedID
		bad.Options = append([]questionOption{}, q.Options...)
		bad.Options[0].ImageID = ""
		check(call("POST", "/api/v1/questions/"+questionID+"/versions", bad, admin, true), 201)
		w = call("GET", "/api/v1/questions/"+questionID+"?version_no=1", nil, admin, false)
		check(w, 200)
		if data(w)["image_id"] != imageID {
			t.Fatal("old version changed")
		}
		var draft string
		if e := owner.QueryRow(ctx, "SELECT id FROM question_versions WHERE organization_id=$1 AND question_id=$2 AND version_no=2", org, questionID).Scan(&draft); e != nil {
			t.Fatal("draft missing")
		}
		_, e := owner.Exec(ctx, "INSERT INTO question_options(organization_id,question_version_id,option_key,label,is_correct,position,image_id) VALUES($1,$2,'C','FK test',false,3,$3)", org, draft, foreignID)
		var pe *pgconn.PgError
		if !errors.As(e, &pe) || pe.Code != "23503" {
			t.Fatal("cross-tenant attachment FK not enforced")
		}
	})
	var url, url2, attemptID, attemptID2, invitation string
	t.Run("candidate_images_only_in_the_active_attempt_snapshot", func(t *testing.T) {
		in := testInput{Title: "Image test", Duration: 120, PassPercent: "50", Items: []testItemInput{{QuestionVersionID: versionID, Points: "1"}}}
		w := call("POST", "/api/v1/tests", in, admin, true)
		check(w, 201)
		tid := data(w)["id"].(string)
		check(call("POST", "/api/v1/tests/"+tid+"/publish", map[string]int{"base_version_no": 1}, admin, true), 200)
		testVersion := data(call("GET", "/api/v1/tests/"+tid, nil, admin, false))["version_id"].(string)
		w = call("POST", "/api/v1/candidates", map[string]string{"full_name": "Image Candidate"}, admin, true)
		check(w, 201)
		candidate := data(w)["id"].(string)
		w = call("POST", "/api/v1/vacancies", map[string]any{"title": "Image Vacancy", "assessor_ids": []string{assessorID}}, admin, true)
		check(w, 201)
		vacancy := data(w)["id"].(string)
		for n := 0; n < 2; n++ {
			w = call("POST", "/api/v1/invitations", map[string]any{"candidate_id": candidate, "vacancy_id": vacancy, "test_version_id": testVersion, "max_attempts": 1, "expires_at": time.Now().Add(time.Hour)}, admin, true)
			check(w, 201)
			b := data(w)
			prefix := "/api/v1/invite/" + strings.TrimPrefix(b["path"].(string), "/i/")
			check(call("GET", prefix+"/attempts/"+fixtureUUID()+"/images/"+imageID, nil, testSession{}, false), 404)
			w = call("POST", prefix+"/start", map[string]any{"consent": true, "request_id": fixtureUUID()}, testSession{}, false)
			check(w, 200)
			attempt := data(w)["id"].(string)
			if n == 0 {
				url = prefix
				attemptID = attempt
				invitation = b["invitation"].(map[string]any)["id"].(string)
				item := data(w)["items"].([]any)[0].(map[string]any)
				if item["image_id"] != imageID {
					t.Fatal("draft image entered candidate snapshot")
				}
				if strings.Contains(w.Body.String(), "is_correct") || strings.Contains(w.Body.String(), "explanation") || strings.Contains(w.Body.String(), "sha256") || strings.Contains(w.Body.String(), "uploaded_by") {
					t.Fatal("image DTO leaks private fields")
				}
			} else {
				url2 = prefix
				attemptID2 = attempt
			}
		}
		for _, img := range []string{imageID, optionID} {
			check(call("GET", url+"/attempts/"+attemptID+"/images/"+img, nil, testSession{}, false), 200)
		}
		for _, img := range []string{unusedID, foreignID} {
			check(call("GET", url+"/attempts/"+attemptID+"/images/"+img, nil, testSession{}, false), 404)
		}
		check(call("GET", url+"/attempts/"+attemptID2+"/images/"+imageID, nil, testSession{}, false), 404)
		check(call("GET", url2+"/attempts/"+attemptID+"/images/"+imageID, nil, testSession{}, false), 404)
		check(call("GET", "/api/v1/results/"+attemptID+"/images/"+imageID, nil, admin, false), 409)
	})
	t.Run("deadline_revocation_and_scoped_report_images", func(t *testing.T) {
		if _, e := owner.Exec(ctx, "UPDATE attempts SET deadline_at=clock_timestamp()-interval '1 second' WHERE organization_id=$1 AND id=$2", org, attemptID2); e != nil {
			t.Fatal("cannot expire image fixture")
		}
		check(call("GET", url2+"/attempts/"+attemptID2+"/images/"+imageID, nil, testSession{}, false), 404)
		check(call("POST", url+"/attempts/"+attemptID+"/submit", nil, testSession{}, false), 200)
		check(call("GET", url+"/attempts/"+attemptID+"/images/"+imageID, nil, testSession{}, false), 404)
		for _, s := range []testSession{admin, recruiter, assessor} {
			check(call("GET", "/api/v1/results/"+attemptID+"/images/"+imageID, nil, s, false), 200)
			check(call("GET", "/api/v1/results/"+attemptID+"/images/"+optionID, nil, s, false), 200)
		}
		check(call("GET", "/api/v1/results/"+attemptID+"/images/"+imageID, nil, other, false), 404)
		check(call("GET", "/api/v1/results/"+attemptID+"/images/"+unusedID, nil, admin, false), 404)
		w := call("GET", "/api/v1/results/"+attemptID, nil, assessor, false)
		check(w, 200)
		if data(w)["questions"].([]any)[0].(map[string]any)["image_id"] != imageID {
			t.Fatal("report image changed")
		}
		if _, e := owner.Exec(ctx, "DELETE FROM vacancy_assessors WHERE organization_id=$1 AND vacancy_id=(SELECT vacancy_id FROM invitations WHERE id=$2)", org, invitation); e != nil {
			t.Fatal("cannot unassign assessor")
		}
		check(call("GET", "/api/v1/results/"+attemptID+"/images/"+imageID, nil, assessor, false), 404)
		check(call("POST", "/api/v1/invitations/"+invitation+"/revoke", nil, admin, true), 204)
		check(call("GET", url+"/attempts/"+attemptID+"/images/"+imageID, nil, testSession{}, false), 404)
	})
	t.Run("unused_cleanup_quota_and_audit", func(t *testing.T) {
		w := upload(fixturePNG(), "image/png", admin, true, false)
		check(w, 201)
		id := data(w)["id"].(string)
		w = call("GET", "/api/v1/images", nil, admin, false)
		check(w, 200)
		if !strings.Contains(w.Body.String(), id) || strings.Contains(w.Body.String(), imageID) {
			t.Fatal("unused image list incorrect")
		}
		check(call("DELETE", "/api/v1/images/"+id, nil, other, true), 404)
		check(call("DELETE", "/api/v1/images/"+id, nil, assessor, true), 403)
		check(call("DELETE", "/api/v1/images/"+id, nil, admin, false), 403)
		check(call("DELETE", "/api/v1/images/"+id, nil, admin, true), 204)
		check(call("GET", "/api/v1/images/"+id, nil, admin, false), 404)
		var count int
		if e := owner.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE organization_id=$1 AND object_id=$2 AND action IN ('image.upload','image.delete')", org, id).Scan(&count); e != nil || count != 2 {
			t.Fatal("image operations not audited")
		}
		if _, e := owner.Exec(ctx, "INSERT INTO attachments(organization_id,storage_key,mime_type,byte_size,sha256) SELECT $1,'quota:'||g,'image/png',20000000,repeat('0',64) FROM generate_series(1,6)g", foreignOrg); e != nil {
			t.Fatal("cannot seed quota fixture")
		}
		check(upload(fixturePNG(), "image/png", other, true, false), 422)
	})
}
