// Local operator CLI. It never restores over an existing database and never
// prints database errors, SQL, credentials, contacts or invitation tokens.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/PlatvaBestHorse/hr-testing/backend/internal/backup"
	"github.com/PlatvaBestHorse/hr-testing/backend/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type table struct {
	Name   string `json:"name"`
	Rows   int64  `json:"rows"`
	SHA256 string `json:"sha256"`
}
type manifest struct {
	Version int       `json:"version"`
	Created time.Time `json:"created_at"`
	Tables  []table   `json:"tables"`
}
type operator struct {
	root, dir, keyPath string
	key                []byte
	cfg                *pgx.ConnConfig
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return errors.New("expected create, verify, restore, list or prune")
	}
	mode := os.Args[1]
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	file := flags.String("file", "", "encrypted archive (defaults to latest for verify)")
	target := flags.String("target", "", "fresh hr_recovery_* database; restore only")
	days := flags.Int("days", 0, "prune archives older than this number of days")
	confirm := flags.String("confirm", "", "DELETE_OLD_BACKUPS to apply prune")
	if err := flags.Parse(os.Args[2:]); err != nil || flags.NArg() != 0 {
		return errors.New("invalid backup arguments")
	}
	o := operator{root: os.Getenv("HR_PROJECT_ROOT")}
	if o.root == "" {
		return errors.New("use scripts/run_backend.py backup from the project")
	}
	o.dir = filepath.Join(o.root, ".backups")
	o.keyPath = os.Getenv("HR_BACKUP_KEY_FILE")
	if o.keyPath == "" {
		o.keyPath = filepath.Join(o.root, ".env.backup.key")
	}
	if err := os.MkdirAll(o.dir, 0700); err != nil {
		return errors.New("cannot create backup directory")
	}
	if st, e := os.Lstat(o.dir); e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return errors.New("backup directory must not be a symlink")
	}
	if err := os.Chmod(o.dir, 0700); err != nil {
		return errors.New("cannot protect backup directory")
	}
	var err error
	o.key, err = o.loadKey(mode == "create")
	if err != nil {
		return err
	}
	if mode == "list" || mode == "prune" {
		return o.inventory(mode, *days, *confirm)
	}
	if mode != "create" && mode != "verify" && mode != "restore" {
		return errors.New("unknown backup command")
	}
	o.cfg, err = pgx.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		return errors.New("invalid database configuration")
	}
	// Docker's PG socket authentication is scoped to this local Compose DB.
	if o.cfg.Host != "127.0.0.1" && o.cfg.Host != "localhost" {
		return errors.New("backup CLI supports the local Compose database only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, o.cfg)
	if err != nil {
		return errors.New("cannot connect to local database")
	}
	defer conn.Close(context.Background())
	var currentUser string
	if err = conn.QueryRow(ctx, "SELECT current_user").Scan(&currentUser); err != nil || currentUser == "hr_app" {
		return errors.New("backup requires the local migration owner")
	}
	if mode == "create" {
		return o.create(ctx, conn)
	}
	if *file == "" {
		files, e := o.files()
		if e != nil || len(files) == 0 {
			return errors.New("no backup archives found")
		}
		*file = files[len(files)-1]
	}
	if mode == "restore" && !regexp.MustCompile(`^hr_recovery_[a-z0-9_]{1,32}$`).MatchString(*target) {
		return errors.New("restore requires a fresh --target hr_recovery_<name>")
	}
	return o.restore(ctx, conn, *file, *target, mode == "restore")
}

func privateFile(path string) (*os.File, error) {
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return nil, errors.New("file must be regular, non-symlink and private (0600)")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot read protected file")
	}
	actual, err := f.Stat()
	if err != nil || !os.SameFile(st, actual) {
		f.Close()
		return nil, errors.New("file changed while opening")
	}
	return f, nil
}

func (o *operator) loadKey(create bool) ([]byte, error) {
	f, err := privateFile(o.keyPath)
	_, statErr := os.Lstat(o.keyPath)
	if err != nil && os.IsNotExist(statErr) && create {
		files, e := o.files()
		if e != nil || len(files) > 0 {
			return nil, errors.New("backup key is missing; recover the original key, do not replace it")
		}
		key := make([]byte, 32)
		if _, e = rand.Read(key); e != nil {
			return nil, errors.New("cannot generate backup key")
		}
		out, e := os.OpenFile(o.keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return nil, errors.New("cannot create backup key")
		}
		_, e = fmt.Fprintln(out, hex.EncodeToString(key))
		if e == nil {
			e = out.Sync()
		}
		closeErr := out.Close()
		if e != nil || closeErr != nil {
			os.Remove(o.keyPath)
			return nil, errors.New("cannot save backup key")
		}
		return key, nil
	}
	if err != nil {
		return nil, errors.New("backup key unavailable; keep the original key private and separate from copies")
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 100))
	if e != nil {
		return nil, errors.New("cannot read backup key")
	}
	key, e := hex.DecodeString(strings.TrimSpace(string(b)))
	if e != nil || len(key) != 32 {
		return nil, errors.New("invalid backup key")
	}
	return key, nil
}

func (o *operator) files() ([]string, error) {
	entries, err := os.ReadDir(o.dir)
	if err != nil {
		return nil, errors.New("cannot list backup directory")
	}
	var files []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "hr-") && strings.HasSuffix(entry.Name(), ".hrbak") {
			files = append(files, filepath.Join(o.dir, entry.Name()))
		}
	}
	sort.Strings(files)
	return files, nil
}

func (o *operator) pg(ctx context.Context, tool, db string, args ...string) *exec.Cmd {
	a := []string{"compose", "exec", "-T", "db", tool, "--username", o.cfg.User, "--dbname", db}
	a = append(a, args...)
	c := exec.CommandContext(ctx, "docker", a...)
	c.Dir = o.root
	c.Stderr = io.Discard
	return c
}

func fingerprints(ctx context.Context, tx pgx.Tx) ([]table, error) {
	if _, err := tx.Exec(ctx, "SET LOCAL TIME ZONE 'UTC'; SET LOCAL DateStyle='ISO, YMD'"); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename")
	if err != nil {
		return nil, err
	}
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		names = append(names, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var result []table
	for _, name := range names {
		// Order canonical row JSON by C collation; stream it into SHA256. This
		// detects changed values, not merely matching row counts.
		r, err := tx.Query(ctx, "SELECT to_jsonb(t)::text FROM "+pgx.Identifier{"public", name}.Sanitize()+" t ORDER BY to_jsonb(t)::text COLLATE \"C\"")
		if err != nil {
			return nil, err
		}
		h := sha256.New()
		count := int64(0)
		for r.Next() {
			var value string
			if err = r.Scan(&value); err != nil {
				r.Close()
				return nil, err
			}
			fmt.Fprintln(h, value)
			count++
		}
		err = r.Err()
		r.Close()
		if err != nil {
			return nil, err
		}
		result = append(result, table{Name: name, Rows: count, SHA256: hex.EncodeToString(h.Sum(nil))})
	}
	return result, nil
}

func (o *operator) create(ctx context.Context, conn *pgx.Conn) error {
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return errors.New("cannot open backup snapshot")
	}
	defer tx.Rollback(context.Background())
	var snapshot string
	var created time.Time
	if err = tx.QueryRow(ctx, "SELECT pg_export_snapshot(), transaction_timestamp()").Scan(&snapshot, &created); err != nil {
		return errors.New("cannot export database snapshot")
	}
	tables, err := fingerprints(ctx, tx)
	if err != nil || len(tables) == 0 {
		return errors.New("cannot fingerprint database snapshot")
	}
	m := manifest{Version: 1, Created: created.UTC(), Tables: tables}
	id := make([]byte, 8)
	if _, err = rand.Read(id); err != nil {
		return errors.New("cannot generate archive ID")
	}
	name := "hr-" + m.Created.Format("20060102T150405Z") + "-" + hex.EncodeToString(id) + ".hrbak"
	f, err := os.CreateTemp(o.dir, ".incomplete-")
	if err != nil {
		return errors.New("cannot create archive")
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	defer f.Close()
	c := o.pg(ctx, "pg_dump", o.cfg.Database, "--format=custom", "--snapshot="+snapshot)
	r, err := c.StdoutPipe()
	if err != nil {
		return errors.New("cannot prepare pg_dump")
	}
	if err = c.Start(); err != nil {
		return errors.New("cannot start pg_dump")
	}
	meta, _ := json.Marshal(m)
	meta = append(meta, '\n')
	err = backup.Seal(f, io.MultiReader(strings.NewReader(string(meta)), r), o.key)
	if err != nil {
		c.Process.Kill()
	}
	waitErr := c.Wait()
	if err != nil || waitErr != nil {
		return errors.New("backup failed; no completed archive saved")
	}
	if err = f.Sync(); err != nil {
		return errors.New("cannot sync archive")
	}
	if err = f.Close(); err != nil {
		return errors.New("cannot close archive")
	}
	// A completed backup is published only after a real restore succeeds.
	// Release the exported snapshot before CREATE DATABASE on this connection.
	if err = tx.Rollback(ctx); err != nil {
		return errors.New("cannot release backup snapshot")
	}
	if err = o.restore(ctx, conn, tmp, "", false); err != nil {
		return fmt.Errorf("backup restore verification failed; no completed archive saved: %w", err)
	}
	if err = os.Rename(tmp, filepath.Join(o.dir, name)); err != nil {
		return errors.New("cannot publish completed archive")
	}
	if err = syncDir(o.dir); err != nil {
		return err
	}
	fmt.Printf("Created %s (%d tables). Key: %s; keep an independent protected copy of the key.\n", filepath.Join(o.dir, name), len(tables), o.keyPath)
	return nil
}

func syncDir(path string) error {
	d, e := os.Open(path)
	if e != nil {
		return errors.New("cannot open backup directory")
	}
	defer d.Close()
	if e = d.Sync(); e != nil {
		return errors.New("cannot sync backup directory")
	}
	return nil
}

// Decode always authenticates to EOF, even when the consumer rejects a manifest.
func (o *operator) decode(f *os.File, consume func(manifest, io.Reader) error) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return errors.New("cannot seek archive")
	}
	r, w := io.Pipe()
	done := make(chan error, 1)
	go func() { err := backup.Open(w, f, o.key); w.CloseWithError(err); done <- err }()
	b := bufio.NewReaderSize(r, 1024*1024+1)
	line, err := b.ReadSlice('\n')
	var m manifest
	if err == nil {
		err = json.Unmarshal(line, &m)
	}
	if err == nil && (m.Version != 1 || m.Created.IsZero() || m.Created.After(time.Now().Add(time.Minute)) || len(m.Tables) == 0) {
		err = errors.New("invalid backup manifest")
	}
	if err == nil {
		err = consume(m, b)
	}
	if err != nil {
		r.CloseWithError(err)
	} else {
		_, err = io.Copy(io.Discard, b)
		r.Close()
	}
	decryptErr := <-done
	if err != nil || decryptErr != nil {
		return errors.New("backup is unreadable, incomplete, damaged or has the wrong key")
	}
	return nil
}

func (o *operator) inspect(f *os.File) (manifest, error) {
	var m manifest
	err := o.decode(f, func(value manifest, r io.Reader) error { m = value; _, e := io.Copy(io.Discard, r); return e })
	return m, err
}

func (o *operator) restore(ctx context.Context, owner *pgx.Conn, path, target string, keep bool) (result error) {
	f, err := privateFile(path)
	if err != nil {
		return err
	}
	defer f.Close()
	m, err := o.inspect(f)
	if err != nil {
		return err
	} // authenticate before CREATE DATABASE
	if target == "" {
		id := make([]byte, 8)
		if _, err = rand.Read(id); err != nil {
			return errors.New("cannot generate check database name")
		}
		target = "hr_restorecheck_" + hex.EncodeToString(id)
	}
	// No --clean, no DROP existing DB, no choice of the live database.
	if _, err = owner.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{target}.Sanitize()+" TEMPLATE template0"); err != nil {
		return errors.New("cannot create fresh restore database (name may already exist)")
	}
	retained := false
	defer func() {
		if !retained {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, e := owner.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{target}.Sanitize()+" WITH (FORCE)")
			if e != nil {
				result = fmt.Errorf("restore cleanup failed; operator must remove isolated database %s", target)
			}
		}
	}()
	err = o.decode(f, func(_ manifest, r io.Reader) error {
		c := o.pg(ctx, "pg_restore", target, "--exit-on-error", "--single-transaction", "--no-owner")
		c.Stdin = r
		return c.Run()
	})
	if err != nil {
		return errors.New("restore failed; isolated database is being removed")
	}
	cfg := o.cfg.Copy()
	cfg.Database = target
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return errors.New("cannot inspect restored database")
	}
	defer conn.Close(context.Background())
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return errors.New("cannot verify restored snapshot")
	}
	actual, err := fingerprints(ctx, tx)
	tx.Rollback(context.Background())
	want, _ := json.Marshal(m.Tables)
	got, _ := json.Marshal(actual)
	if err != nil || string(want) != string(got) {
		return errors.New("restored table contents differ from the backup snapshot")
	}
	if err = checkMigrations(ctx, conn); err != nil {
		return errors.New("backup schema does not match current migrations")
	}
	if err = checkPrivileges(ctx, conn); err != nil {
		return err
	}
	if keep {
		// Offline recovery safety: old cookies, invitations and outbound messages
		// must not reactivate when an operator later switches the API.
		if _, err = conn.Exec(ctx, "BEGIN; DELETE FROM auth_sessions; DELETE FROM retention_previews; UPDATE invitations SET status='revoked'; UPDATE outbound_messages SET status='cancelled' WHERE status IN ('pending','failed'); COMMIT"); err != nil {
			return errors.New("cannot disable restored access; isolated database is being removed")
		}
		retained = true
		fmt.Printf("Restored and checked %s (%d tables). Sessions/links revoked; API remains connected to its original database. Reconcile deletions newer than %s before activation.\n", target, len(actual), m.Created.Format(time.RFC3339))
	} else {
		fmt.Printf("Verified restore (%d tables, exact contents, migrations and app privileges); temporary database will be removed. Snapshot: %s\n", len(actual), m.Created.Format(time.RFC3339))
	}
	return nil
}

func checkPrivileges(ctx context.Context, conn *pgx.Conn) error {
	var valid bool
	err := conn.QueryRow(ctx, `SELECT
	 has_table_privilege('hr_app','public.audit_events','SELECT')
	 AND has_table_privilege('hr_app','public.audit_events','INSERT')
 AND NOT has_table_privilege('hr_app','public.audit_events','UPDATE')
 AND NOT has_table_privilege('hr_app','public.audit_events','DELETE')
 AND NOT has_table_privilege('hr_app','public.schema_migrations','UPDATE')
 AND has_function_privilege('hr_app','public.prune_audit_events(uuid,bytea)','EXECUTE')
 AND NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='hr_app' AND (rolsuper OR rolbypassrls OR rolcreatedb OR rolcreaterole))
 AND NOT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_roles r ON r.oid=c.relowner WHERE n.nspname='public' AND r.rolname='hr_app')
	 AND (SELECT count(*) FROM pg_trigger WHERE NOT tgisinternal AND tgenabled='O' AND tgname IN ('question_version_immutable','question_options_immutable','test_version_immutable','test_items_immutable'))=4`).Scan(&valid)
	if err != nil || !valid {
		return errors.New("restored application permissions or triggers are unsafe")
	}
	// Exercise the real role, not just stored ACLs.
	if _, err = conn.Exec(ctx, "BEGIN; SET LOCAL ROLE hr_app; SELECT id FROM candidates LIMIT 1; SELECT id FROM attempts LIMIT 1; ROLLBACK"); err != nil {
		return errors.New("restored app role cannot read workflow tables")
	}
	for _, sql := range []string{"UPDATE audit_events SET action=action WHERE false", "DELETE FROM audit_events WHERE false"} {
		tx, e := conn.Begin(ctx)
		if e != nil {
			return errors.New("cannot test audit permissions")
		}
		_, e = tx.Exec(ctx, "SET LOCAL ROLE hr_app")
		if e == nil {
			_, e = tx.Exec(ctx, sql)
		}
		tx.Rollback(context.Background())
		var pgErr *pgconn.PgError
		if !errors.As(e, &pgErr) || pgErr.Code != "42501" {
			return errors.New("restored app role can modify audit history")
		}
	}
	for _, name := range []string{"question_versions", "test_versions"} {
		var exists bool
		if err := conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM "+pgx.Identifier{name}.Sanitize()+" WHERE status='published')").Scan(&exists); err != nil {
			return errors.New("cannot inspect published versions")
		}
		if !exists {
			continue
		}
		tx, e := conn.Begin(ctx)
		if e != nil {
			return errors.New("cannot test immutable versions")
		}
		_, e = tx.Exec(ctx, "SET LOCAL ROLE hr_app")
		if e == nil {
			_, e = tx.Exec(ctx, "UPDATE "+pgx.Identifier{name}.Sanitize()+" SET status=status WHERE id=(SELECT id FROM "+pgx.Identifier{name}.Sanitize()+" WHERE status='published' LIMIT 1)")
		}
		tx.Rollback(context.Background())
		var pgErr *pgconn.PgError
		if !errors.As(e, &pgErr) || pgErr.Code != "55000" {
			return errors.New("restored published versions are not immutable")
		}
	}
	return nil
}

func checkMigrations(ctx context.Context, conn *pgx.Conn) error {
	names, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		return err
	}
	var count int
	if err = conn.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != len(names) {
		return errors.New("migration count differs")
	}
	for _, name := range names {
		body, e := migrations.Files.ReadFile(name)
		if e != nil {
			return e
		}
		sum := sha256.Sum256(body)
		var saved string
		if e = conn.QueryRow(ctx, "SELECT checksum FROM schema_migrations WHERE name=$1", name).Scan(&saved); e != nil || saved != hex.EncodeToString(sum[:]) {
			return errors.New("migration checksum differs")
		}
	}
	return nil
}

func (o *operator) inventory(mode string, days int, confirm string) error {
	if mode == "prune" && (days < 1 || days > 36500 || (confirm != "" && confirm != "DELETE_OLD_BACKUPS")) {
		return errors.New("prune requires --days 1..36500; --confirm DELETE_OLD_BACKUPS applies deletion")
	}
	files, err := o.files()
	if err != nil {
		return err
	}
	type entry struct {
		path    string
		created time.Time
	}
	var entries []entry
	for _, path := range files {
		f, e := privateFile(path)
		if e != nil {
			return e
		}
		m, e := o.inspect(f)
		f.Close()
		if e != nil {
			return errors.New("invalid archive; no retention deletion performed")
		}
		entries = append(entries, entry{path, m.Created})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].created.Before(entries[j].created) })
	cutoff := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	for i, e := range entries {
		old := mode == "prune" && i < len(entries)-1 && e.created.Before(cutoff)
		label := "KEEP"
		if old {
			label = "WOULD_DELETE"
		}
		if old && confirm == "DELETE_OLD_BACKUPS" {
			if err = os.Remove(e.path); err != nil {
				return errors.New("cannot delete archive; earlier listed deletions may have completed")
			}
			label = "DELETED"
		}
		fmt.Printf("%s %s %s\n", label, filepath.Base(e.path), e.created.Format(time.RFC3339))
	}
	if len(entries) == 0 {
		fmt.Println("No archives")
	}
	return syncDir(o.dir)
}
