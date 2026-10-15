package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"github.com/PlatvaBestHorse/hr-testing/backend/internal/database"
	"os"
	"testing"
	"time"
)

func TestResumeRequiresOwnerAndRevokesAccess(t *testing.T) {
	ownerURL, appURL := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_APP_DATABASE_URL")
	if ownerURL == "" || appURL == "" {
		t.Skip("run make integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, ownerURL)
	if err != nil {
		t.Fatal("cannot open owner")
	}
	defer pool.Close()
	if err = database.Migrate(ctx, pool); err != nil {
		t.Fatal("fixture migrations failed")
	}
	var id, userID string
	if err = pool.QueryRow(ctx, "INSERT INTO organizations(slug,name,status) VALUES('resume-test-'||gen_random_uuid(),'Resume fixture','suspended') RETURNING id").Scan(&id); err != nil {
		t.Fatal("cannot create isolated organization")
	}
	defer func() {
		pool.Exec(context.Background(), "DELETE FROM auth_sessions WHERE organization_id=$1", id)
		pool.Exec(context.Background(), "DELETE FROM memberships WHERE organization_id=$1", id)
		if _, e := pool.Exec(context.Background(), "DELETE FROM audit_events WHERE organization_id=$1", id); e != nil {
			t.Error("audit fixture cleanup failed")
		}
		if _, e := pool.Exec(context.Background(), "DELETE FROM organizations WHERE id=$1", id); e != nil {
			t.Error("organization fixture cleanup failed")
		}
		if userID != "" {
			if _, e := pool.Exec(context.Background(), "DELETE FROM users WHERE id=$1", userID); e != nil {
				t.Error("user fixture cleanup failed")
			}
		}
	}()
	if err = pool.QueryRow(ctx, "INSERT INTO users(email,password_hash,display_name) VALUES($1,'fixture-only','Resume fixture') RETURNING id", "resume-"+id+"@fixture.test").Scan(&userID); err != nil {
		t.Fatal("cannot seed resume user")
	}
	if _, err = pool.Exec(ctx, "INSERT INTO memberships(organization_id,user_id,role) VALUES($1,$2,'org_admin')", id, userID); err != nil {
		t.Fatal("cannot seed membership")
	}
	tokenHash := sha256.Sum256([]byte("resume-session:" + id))
	if _, err = pool.Exec(ctx, "INSERT INTO auth_sessions(token_hash,organization_id,user_id,csrf_token,expires_at) VALUES($1,$2,$3,repeat('s',64),clock_timestamp()+interval '1 hour')", tokenHash[:], id, userID); err != nil {
		t.Fatal("cannot seed session")
	}
	invoke := func(url string) error {
		t.Helper()
		t.Setenv("DATABASE_URL", url)
		old := os.Stdin
		r, w, e := os.Pipe()
		if e != nil {
			t.Fatal(e)
		}
		json.NewEncoder(w).Encode(map[string]string{"organization_id": id})
		w.Close()
		os.Stdin = r
		defer func() { os.Stdin = old; r.Close() }()
		return run()
	}
	if e := invoke(appURL); e == nil {
		t.Fatal("app role resumed organization")
	}
	if e := invoke(ownerURL); e != nil {
		t.Fatal("owner could not resume organization")
	}
	var status string
	var count int
	if e := pool.QueryRow(ctx, "SELECT status,(SELECT count(*) FROM audit_events WHERE organization_id=$1 AND action='organization.resume_cli') FROM organizations WHERE id=$1", id).Scan(&status, &count); e != nil || status != "active" || count != 1 {
		t.Fatal("resume or audit missing")
	}
	if e := invoke(ownerURL); e == nil {
		t.Fatal("unexpected repeated resume of active organization")
	}
	if e := pool.QueryRow(ctx, "SELECT count(*) FROM auth_sessions WHERE organization_id=$1", id).Scan(&count); e != nil || count != 0 {
		t.Fatal("old sessions survived resume")
	}
}
