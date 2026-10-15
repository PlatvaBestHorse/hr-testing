package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PlatvaBestHorse/hr-testing/backend/internal/backup"
	"github.com/PlatvaBestHorse/hr-testing/backend/internal/database"
	"github.com/PlatvaBestHorse/hr-testing/backend/internal/identity"
	"github.com/PlatvaBestHorse/hr-testing/backend/internal/privacy"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func fixtureOperator(t *testing.T) *operator {
	t.Helper()
	dir := t.TempDir()
	return &operator{dir: dir, keyPath: filepath.Join(dir, "key"), key: bytes.Repeat([]byte{9}, 32)}
}

func writeArchive(t *testing.T, o *operator, name string, created time.Time) string {
	t.Helper()
	m := manifest{Version: 1, Created: created, Tables: []table{{Name: "example", Rows: 0, SHA256: "test"}}}
	b, _ := json.Marshal(m)
	p := filepath.Join(o.dir, name)
	f, e := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	e = backup.Seal(f, bytes.NewReader(append(append(b, '\n'), []byte("test dump")...)), o.key)
	f.Close()
	if e != nil {
		t.Fatal(e)
	}
	return p
}

func TestKeyAndRetentionSafety(t *testing.T) {
	o := fixtureOperator(t)
	key, e := o.loadKey(true)
	if e != nil || len(key) != 32 {
		t.Fatalf("key generation: %v", e)
	}
	o.key = key
	loaded, e := o.loadKey(true)
	if e != nil || !bytes.Equal(loaded, key) {
		t.Fatal("existing key replaced")
	}
	old := writeArchive(t, o, "hr-old.hrbak", time.Now().Add(-90*24*time.Hour))
	newest := writeArchive(t, o, "hr-newest.hrbak", time.Now().Add(-60*24*time.Hour))
	if e = o.inventory("prune", 30, ""); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(old); e != nil {
		t.Fatal("preview deleted old archive")
	}
	if e = o.inventory("prune", 30, "WRONG"); e == nil {
		t.Fatal("accepted wrong deletion phrase")
	}
	if e = o.inventory("prune", 30, "DELETE_OLD_BACKUPS"); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(old); !os.IsNotExist(e) {
		t.Fatal("old archive retained")
	}
	if _, e = os.Stat(newest); e != nil {
		t.Fatal("last archive deleted")
	}
	os.Remove(o.keyPath)
	if _, e = o.loadKey(true); e == nil {
		t.Fatal("silently replaced lost key despite archives")
	}
	if _, e = os.Stat(o.keyPath); !os.IsNotExist(e) {
		t.Fatal("replacement key created")
	}
}

func TestInvalidArchivesBlockDeletionAndPrivateFiles(t *testing.T) {
	o := fixtureOperator(t)
	old := writeArchive(t, o, "hr-old.hrbak", time.Now().Add(-90*24*time.Hour))
	_ = writeArchive(t, o, "hr-new.hrbak", time.Now().Add(-60*24*time.Hour))
	bad := filepath.Join(o.dir, "hr-bad.hrbak")
	os.WriteFile(bad, []byte("broken"), 0600)
	if e := o.inventory("prune", 30, "DELETE_OLD_BACKUPS"); e == nil {
		t.Fatal("accepted corrupt archive")
	}
	if _, e := os.Stat(old); e != nil {
		t.Fatal("partially deleted before validation")
	}
	os.Chmod(old, 0644)
	if f, e := privateFile(old); e == nil {
		f.Close()
		t.Fatal("accepted public backup")
	}
	os.Chmod(old, 0600)
	link := filepath.Join(o.dir, "symlink")
	if e := os.Symlink(old, link); e != nil {
		t.Fatal(e)
	}
	if f, e := privateFile(link); e == nil {
		f.Close()
		t.Fatal("accepted symlink")
	}
	// An authenticated but oversized JSON header must fail without allocation growth.
	var sealed bytes.Buffer
	if e := backup.Seal(&sealed, io.MultiReader(bytes.NewReader(bytes.Repeat([]byte("x"), 2*1024*1024)), bytes.NewReader([]byte("\n"))), o.key); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(bad, sealed.Bytes(), 0600)
	f, e := privateFile(bad)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	if _, e = o.inspect(f); e == nil {
		t.Fatal("accepted oversized manifest")
	}
}

func TestBackupRestoreIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("requires local Docker Compose owner TEST_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cfg, e := pgx.ParseConfig(url)
	if e != nil {
		t.Fatal("invalid test config")
	}
	owner, e := pgx.ConnectConfig(ctx, cfg)
	if e != nil {
		t.Fatal("cannot connect test owner")
	}
	defer owner.Close(context.Background())
	id := make([]byte, 8)
	rand.Read(id)
	suffix := hex.EncodeToString(id)
	source := "hr_backup_test_" + suffix
	target := "hr_recovery_test_" + suffix
	if _, e = owner.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{source}.Sanitize()+" TEMPLATE template0"); e != nil {
		t.Fatal("cannot create isolated fixture database")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, name := range []string{source, target, target + "_contacts", target + "_all"} {
			if _, e := owner.Exec(cleanup, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); e != nil {
				t.Errorf("cannot clean isolated fixture database %s", name)
			}
		}
	}()
	cfg = cfg.Copy()
	cfg.Database = source
	poolCfg, e := pgxpool.ParseConfig(url)
	if e != nil {
		t.Fatal("cannot parse fixture pool config")
	}
	poolCfg.ConnConfig = cfg
	pool, e := pgxpool.NewWithConfig(ctx, poolCfg)
	if e != nil {
		t.Fatal("cannot open fixture pool")
	}
	defer pool.Close()
	if e = database.Migrate(ctx, pool); e != nil {
		t.Fatal("fixture migration failed")
	}
	_, e = pool.Exec(ctx, `INSERT INTO organizations(id,slug,name) VALUES('00000000-0000-4000-8000-000000000001','backup-fixture','Проверка восстановления');
 INSERT INTO candidates(organization_id,full_name,email) VALUES('00000000-0000-4000-8000-000000000001','Тестовый кандидат','backup@example.test');
 INSERT INTO users(id,email,password_hash,display_name) VALUES('00000000-0000-4000-8000-000000000002','operator@example.test','fixture-only','Тестовый оператор');
 INSERT INTO memberships(organization_id,user_id,role) VALUES('00000000-0000-4000-8000-000000000001','00000000-0000-4000-8000-000000000002','org_admin');
 INSERT INTO auth_sessions(token_hash,organization_id,user_id,csrf_token,expires_at) VALUES(decode(repeat('ab',32),'hex'),'00000000-0000-4000-8000-000000000001','00000000-0000-4000-8000-000000000002',repeat('x',43),now()+interval '1 hour');
 INSERT INTO audit_events(organization_id,action,object_type) VALUES('00000000-0000-4000-8000-000000000001','backup.fixture','candidate');
 INSERT INTO outbound_messages(organization_id,recipient,template_code) VALUES('00000000-0000-4000-8000-000000000001','backup@example.test','fixture');`)
	if e != nil {
		t.Fatal("cannot seed isolated backup fixture")
	}
	userID := "00000000-0000-4000-8000-000000000002"
	seed, e := identity.NewTOTPSecret()
	if e != nil {
		t.Fatal(e)
	}
	secret, e := identity.Encrypt(bytes.Repeat([]byte{6}, 32), "totp:"+userID, []byte(seed))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, "UPDATE users SET mfa_enabled=true WHERE id=$1", userID); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, "INSERT INTO user_security(user_id,secret_ciphertext,pending_ciphertext,pending_until,last_step) VALUES($1,$2,$2,clock_timestamp()+interval '10 minutes',0)", userID, secret); e != nil {
		t.Fatal(e)
	}
	recoveryHash := sha256.Sum256([]byte("fixture-recovery-code"))
	tokenHash := sha256.Sum256([]byte("fixture-password-link"))
	binding := sha256.Sum256([]byte("fixture-only"))
	if _, e = pool.Exec(ctx, "INSERT INTO mfa_recovery_codes(user_id,code_hash) VALUES($1,$2)", userID, recoveryHash[:]); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, "INSERT INTO account_tokens(token_hash,user_id,organization_id,purpose,password_binding,expires_at) VALUES($1,$2,'00000000-0000-4000-8000-000000000001','reset',$3,clock_timestamp()+interval '15 minutes')", tokenHash[:], userID, binding[:]); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, "INSERT INTO account_mail(token_hash,payload) VALUES($1,$2)", tokenHash[:], secret); e != nil {
		t.Fatal(e)
	}
	var raster bytes.Buffer
	if e = png.Encode(&raster, image.NewNRGBA(image.Rect(0, 0, 8, 6))); e != nil {
		t.Fatal(e)
	}
	imageBytes := raster.Bytes()
	sum := sha256.Sum256(imageBytes)
	var imageID string
	if e = pool.QueryRow(ctx, `INSERT INTO attachments(organization_id,storage_key,mime_type,byte_size,sha256,data,width,height)
 VALUES('00000000-0000-4000-8000-000000000001','pg:fixture','image/png',$1,$2,$3,8,6) RETURNING id`, len(imageBytes), hex.EncodeToString(sum[:]), imageBytes).Scan(&imageID); e != nil {
		t.Fatal("cannot seed backup image")
	}
	var questionID string
	if e = pool.QueryRow(ctx, `INSERT INTO questions(organization_id) VALUES('00000000-0000-4000-8000-000000000001') RETURNING id`).Scan(&questionID); e != nil {
		t.Fatal("cannot seed image question")
	}
	if _, e = pool.Exec(ctx, `INSERT INTO question_versions(organization_id,question_id,version_no,type,body,image_id,status)
 VALUES('00000000-0000-4000-8000-000000000001',$1,1,'single_choice','Image backup fixture',$2,'published')`, questionID, imageID); e != nil {
		t.Fatal("cannot seed image version")
	}
	conn, e := pgx.ConnectConfig(ctx, cfg)
	if e != nil {
		t.Fatal("cannot connect fixture")
	}
	defer conn.Close(context.Background())
	o := fixtureOperator(t)
	o.cfg = cfg
	o.root, e = filepath.Abs("../../..")
	if e != nil {
		t.Fatal(e)
	}
	if e = o.create(ctx, conn); e != nil {
		t.Fatal(e)
	}
	files, e := o.files()
	if e != nil || len(files) != 1 {
		t.Fatal("archive not created")
	}
	if e = o.restore(ctx, conn, files[0], "", false); e != nil {
		t.Fatal(e)
	}
	if e = o.restore(ctx, conn, files[0], target, true); e == nil {
		t.Fatal("retained restore accepted missing independent journal")
	}
	o.ledgerPath, e = o.exportErasures(ctx, conn)
	if e != nil {
		t.Fatal(e)
	}
	if e = o.restore(ctx, conn, files[0], target, true); e != nil {
		t.Fatal(e)
	}
	if e = o.restore(ctx, conn, files[0], target, true); e == nil {
		t.Fatal("overwrote existing recovery database")
	}
	recoveredCfg := cfg.Copy()
	recoveredCfg.Database = target
	recovered, e := pgx.ConnectConfig(ctx, recoveredCfg)
	if e != nil {
		t.Fatal("cannot inspect recovery DB")
	}
	defer recovered.Close(context.Background())
	var sessions, pending, candidates int
	if e = recovered.QueryRow(ctx, "SELECT (SELECT count(*) FROM auth_sessions),(SELECT count(*) FROM outbound_messages WHERE status='pending'),(SELECT count(*) FROM candidates)").Scan(&sessions, &pending, &candidates); e != nil || sessions != 0 || pending != 0 || candidates != 1 {
		t.Fatal("recovery safety or data preservation failed")
	}
	var restoredBytes []byte
	if e = recovered.QueryRow(ctx, "SELECT data FROM attachments WHERE id=$1", imageID).Scan(&restoredBytes); e != nil || !bytes.Equal(restoredBytes, imageBytes) {
		t.Fatal("image bytes lost during restore")
	}
	var restoredSecret []byte
	var tokens, codeCount, mailCount int
	var pendingSetup bool
	if e = recovered.QueryRow(ctx, "SELECT secret_ciphertext,pending_ciphertext IS NOT NULL,(SELECT count(*) FROM account_tokens),(SELECT count(*) FROM mfa_recovery_codes),(SELECT count(*) FROM account_mail) FROM user_security WHERE user_id=$1", userID).Scan(&restoredSecret, &pendingSetup, &tokens, &codeCount, &mailCount); e != nil || pendingSetup || tokens != 0 || codeCount != 0 || mailCount != 0 {
		t.Fatal("restore reactivated one-time account access")
	}
	plain, e := identity.Decrypt(bytes.Repeat([]byte{6}, 32), "totp:"+userID, restoredSecret)
	if e != nil || string(plain) != seed {
		t.Fatal("restored MFA seed cannot be decrypted with preserved key")
	}
	var candidateID string
	if e = conn.QueryRow(ctx, "SELECT id FROM candidates LIMIT 1").Scan(&candidateID); e != nil {
		t.Fatal("missing source fixture candidate")
	}
	erasureTx, e := conn.Begin(ctx)
	if e != nil {
		t.Fatal("cannot begin erasure fixture")
	}
	if e = privacy.Redact(ctx, erasureTx, "00000000-0000-4000-8000-000000000001", candidateID); e == nil {
		e = privacy.Record(ctx, erasureTx, "00000000-0000-4000-8000-000000000001", candidateID, "contacts")
	}
	if e != nil {
		erasureTx.Rollback(ctx)
		t.Fatal("cannot seed contact erasure")
	}
	if e = erasureTx.Commit(ctx); e != nil {
		t.Fatal("cannot commit contact erasure")
	}
	if e = o.restore(ctx, conn, files[0], target+"_contacts", true); e == nil {
		t.Fatal("accepted export missing newer source deletion")
	}
	o.ledgerPath, e = o.exportErasures(ctx, conn)
	if e != nil {
		t.Fatal(e)
	}
	if e = o.restore(ctx, conn, files[0], target+"_contacts", true); e != nil {
		t.Fatal(e)
	}
	inspectCfg := cfg.Copy()
	inspectCfg.Database = target + "_contacts"
	inspect, e := pgx.ConnectConfig(ctx, inspectCfg)
	if e != nil {
		t.Fatal("cannot inspect contact recovery")
	}
	var redacted bool
	e = inspect.QueryRow(ctx, "SELECT email IS NULL AND phone IS NULL AND erased_at IS NOT NULL FROM candidates WHERE id=$1", candidateID).Scan(&redacted)
	if e != nil || !redacted {
		inspect.Close(ctx)
		t.Fatal("recovery resurrected erased contacts")
	}
	// A later hold on an already-redacted card must survive idempotent replay.
	if _, e = inspect.Exec(ctx, "UPDATE candidates SET retention_hold=true,retention_hold_reason='Fixture later hold' WHERE id=$1", candidateID); e != nil {
		inspect.Close(ctx)
		t.Fatal("cannot set later hold fixture")
	}
	ledger, ledgerErr := o.loadErasureLedger(time.Time{})
	if ledgerErr != nil {
		inspect.Close(ctx)
		t.Fatal(ledgerErr)
	}
	if e = reconcileErasures(ctx, conn, inspect, ledger); e != nil {
		inspect.Close(ctx)
		t.Fatal(e)
	}
	var held bool
	e = inspect.QueryRow(ctx, "SELECT retention_hold FROM candidates WHERE id=$1", candidateID).Scan(&held)
	inspect.Close(ctx)
	if e != nil || !held {
		t.Fatal("erasure replay removed a later retention hold")
	}
	if e != nil || !redacted {
		t.Fatal("recovery resurrected erased contacts")
	}
	erasureTx, e = conn.Begin(ctx)
	if e != nil {
		t.Fatal("cannot begin full erasure fixture")
	}
	if e = privacy.Purge(ctx, erasureTx, "00000000-0000-4000-8000-000000000001", candidateID); e == nil {
		e = privacy.Record(ctx, erasureTx, "00000000-0000-4000-8000-000000000001", candidateID, "all")
	}
	if e != nil {
		erasureTx.Rollback(ctx)
		t.Fatal("cannot seed full erasure")
	}
	if e = erasureTx.Commit(ctx); e != nil {
		t.Fatal("cannot commit full erasure")
	}
	o.ledgerPath, e = o.exportErasures(ctx, conn)
	if e != nil {
		t.Fatal(e)
	}
	if e = o.restore(ctx, conn, files[0], target+"_all", true); e != nil {
		t.Fatal(e)
	}
	inspectCfg.Database = target + "_all"
	inspect, e = pgx.ConnectConfig(ctx, inspectCfg)
	if e != nil {
		t.Fatal("cannot inspect full recovery")
	}
	var remaining int
	e = inspect.QueryRow(ctx, "SELECT count(*) FROM candidates").Scan(&remaining)
	inspect.Close(ctx)
	if e != nil || remaining != 0 {
		t.Fatal("recovery resurrected fully erased candidate")
	}
	f, e := os.ReadFile(files[0])
	if e != nil {
		t.Fatal(e)
	}
	f[len(f)-1] ^= 1
	corrupt := filepath.Join(o.dir, "hr-corrupt.hrbak")
	os.WriteFile(corrupt, f, 0600)
	if e = o.restore(ctx, conn, corrupt, "", false); e == nil {
		t.Fatal("restored corrupted archive")
	}
	var liveCandidates int
	if e = conn.QueryRow(ctx, "SELECT count(*) FROM candidates").Scan(&liveCandidates); e != nil || liveCandidates != 0 {
		t.Fatal("backup changed source database")
	}
}
