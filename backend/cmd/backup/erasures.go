package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/PlatvaBestHorse/hr-testing/backend/internal/backup"
	"github.com/PlatvaBestHorse/hr-testing/backend/internal/privacy"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type erasureEntry struct {
	ID           string    `json:"id"`
	Organization string    `json:"organization_id"`
	Candidate    string    `json:"candidate_id"`
	Scope        string    `json:"scope"`
	At           time.Time `json:"erased_at"`
}
type erasureLedger struct {
	Version  int            `json:"version"`
	Identity string         `json:"database_identity"`
	Exported time.Time      `json:"exported_at"`
	Entries  []erasureEntry `json:"entries"`
}

func ledgerUUID(s string) bool {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return false
	}
	b, e := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	return e == nil && len(b) == 16
}
func readLedgerRows(ctx context.Context, tx pgx.Tx) (erasureLedger, error) {
	out := erasureLedger{Version: 1, Entries: []erasureEntry{}}
	err := tx.QueryRow(ctx, `SELECT identity,clock_timestamp() FROM privacy_ledger_state WHERE singleton`).Scan(&out.Identity, &out.Exported)
	if err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `SELECT id,organization_id,candidate_id,scope,erased_at FROM candidate_erasures ORDER BY erased_at,id LIMIT 100001`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var e erasureEntry
		if err = rows.Scan(&e.ID, &e.Organization, &e.Candidate, &e.Scope, &e.At); err != nil {
			return out, err
		}
		out.Entries = append(out.Entries, e)
	}
	if len(out.Entries) > 100000 {
		return out, errors.New("erasure ledger exceeds supported export limit")
	}
	return out, rows.Err()
}
func (o *operator) exportErasures(ctx context.Context, conn *pgx.Conn) (string, error) {
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return "", errors.New("cannot open erasure journal")
	}
	defer tx.Rollback(context.Background())
	ledger, err := readLedgerRows(ctx, tx)
	if err != nil {
		return "", errors.New("cannot read erasure journal; check current migrations")
	}
	body, err := json.Marshal(ledger)
	if err != nil {
		return "", errors.New("cannot encode erasure journal")
	}
	if len(body) > 30*1024*1024 {
		return "", errors.New("erasure journal exceeds supported size")
	}
	out, err := os.CreateTemp(o.dir, ".erasure-incomplete-")
	if err != nil {
		return "", errors.New("cannot create protected erasure export")
	}
	temporary := out.Name()
	defer os.Remove(temporary)
	defer out.Close()
	if err = backup.Seal(out, bytes.NewReader(body), o.key); err == nil {
		err = out.Sync()
	}
	if err == nil {
		err = out.Close()
	}
	if err != nil {
		return "", errors.New("cannot encrypt/sync erasure export")
	}
	path := filepath.Join(o.dir, "erasure-"+ledger.Exported.UTC().Format("20060102T150405Z")+"-"+filepath.Base(temporary)[len(".erasure-incomplete-"):]+".hrledger")
	if err = os.Rename(temporary, path); err == nil {
		err = syncDir(o.dir)
	}
	if err != nil {
		return "", errors.New("cannot publish erasure export")
	}
	fmt.Printf("Encrypted erasure journal saved: %s (%d entries). Copy independently of DB snapshots; refresh after each erasure.\n", path, len(ledger.Entries))
	return path, nil
}
func (o *operator) loadErasureLedger(snapshot time.Time) (erasureLedger, error) {
	var out erasureLedger
	if o.ledgerPath == "" {
		return out, errors.New("retained restore requires a current independent --ledger export; no database retained")
	}
	f, err := privateFile(o.ledgerPath)
	if err != nil {
		return out, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() > 32*1024*1024 {
		return out, errors.New("invalid erasure export size")
	}
	var plain bytes.Buffer
	if err = backup.Open(&plain, f, o.key); err != nil {
		return out, errors.New("erasure export damaged or wrong key")
	}
	decoder := json.NewDecoder(&plain)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&out) != nil || decoder.Decode(new(any)) != io.EOF || out.Version != 1 || !ledgerUUID(out.Identity) || out.Exported.Before(snapshot) || out.Exported.After(time.Now().Add(time.Minute)) || len(out.Entries) > 100000 {
		return out, errors.New("invalid or older-than-snapshot erasure export")
	}
	seen := map[string]bool{}
	scopes := map[string]bool{}
	for _, e := range out.Entries {
		key := e.Organization + e.Candidate + e.Scope
		if !ledgerUUID(e.ID) || !ledgerUUID(e.Organization) || !ledgerUUID(e.Candidate) || (e.Scope != "contacts" && e.Scope != "all") || e.At.IsZero() || e.At.After(out.Exported) || seen[e.ID] || scopes[key] {
			return out, errors.New("invalid erasure export entry")
		}
		seen[e.ID] = true
		scopes[key] = true
	}
	return out, nil
}
func reconcileErasures(ctx context.Context, source, recovered *pgx.Conn, ledger erasureLedger) error {
	var lineage string
	if err := recovered.QueryRow(ctx, `SELECT identity FROM privacy_ledger_state WHERE singleton`).Scan(&lineage); err != nil || lineage != ledger.Identity {
		return errors.New("erasure journal belongs to another database lineage")
	}
	// When the original DB still exists, detect exports missing later deletions.
	var sourceLineage string
	sourceErr := source.QueryRow(ctx, `SELECT identity FROM privacy_ledger_state WHERE singleton`).Scan(&sourceLineage)
	if sourceErr != nil {
		var pgerr *pgconn.PgError
		if !errors.As(sourceErr, &pgerr) || pgerr.Code != "42P01" {
			return errors.New("cannot verify original erasure journal")
		}
	}
	if sourceErr == nil && sourceLineage == ledger.Identity {
		tx, err := source.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			return errors.New("cannot check source erasure journal")
		}
		live, e := readLedgerRows(ctx, tx)
		tx.Rollback(context.Background())
		a, _ := json.Marshal(live.Entries)
		b, _ := json.Marshal(ledger.Entries)
		if e != nil || !bytes.Equal(a, b) {
			return errors.New("erasure export is stale; refresh from the source database")
		}
	}
	tx, err := recovered.Begin(ctx)
	if err != nil {
		return errors.New("cannot reconcile erasures")
	}
	defer tx.Rollback(context.Background())
	for _, e := range ledger.Entries {
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organizations WHERE id=$1)`, e.Organization).Scan(&exists); err != nil {
			return errors.New("cannot inspect recovered organization")
		}
		if !exists {
			continue
		}
		if e.Scope == "all" {
			err = privacy.Purge(ctx, tx, e.Organization, e.Candidate)
		} else {
			err = privacy.Redact(ctx, tx, e.Organization, e.Candidate)
		}
		if err != nil {
			return errors.New("cannot reapply candidate erasure")
		}
		if _, err = tx.Exec(ctx, `INSERT INTO candidate_erasures(id,organization_id,candidate_id,scope,erased_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(organization_id,candidate_id,scope) DO NOTHING`, e.ID, e.Organization, e.Candidate, e.Scope, e.At); err != nil {
			return errors.New("cannot preserve recovered erasure journal")
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return errors.New("cannot commit erasure reconciliation")
	}
	return nil
}
