package main

import (
	"bytes"
	"encoding/json"
	"github.com/PlatvaBestHorse/hr-testing/backend/internal/backup"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestErasureExportValidation(t *testing.T) {
	o := fixtureOperator(t)
	now := time.Now().UTC()
	snapshot := now.Add(-time.Hour)
	uuid := "00000000-0000-4000-8000-000000000001"
	valid := erasureLedger{Version: 1, Identity: uuid, Exported: now, Entries: []erasureEntry{{ID: uuid, Organization: uuid, Candidate: uuid, Scope: "all", At: now.Add(-time.Minute)}}}
	write := func(ledger erasureLedger) {
		t.Helper()
		body, _ := json.Marshal(ledger)
		var encrypted bytes.Buffer
		if e := backup.Seal(&encrypted, bytes.NewReader(body), o.key); e != nil {
			t.Fatal(e)
		}
		o.ledgerPath = filepath.Join(o.dir, "independent.hrledger")
		if e := os.WriteFile(o.ledgerPath, encrypted.Bytes(), 0600); e != nil {
			t.Fatal(e)
		}
	}
	write(valid)
	if _, e := o.loadErasureLedger(snapshot); e != nil {
		t.Fatal("valid independent journal rejected")
	}
	tests := []struct {
		name   string
		change func(*erasureLedger)
	}{
		{"old", func(l *erasureLedger) { l.Exported = snapshot.Add(-time.Second) }},
		{"future", func(l *erasureLedger) { l.Exported = now.Add(time.Hour) }},
		{"unknown_scope", func(l *erasureLedger) { l.Entries[0].Scope = "anything" }},
		{"invalid_identity", func(l *erasureLedger) { l.Identity = "wrong" }},
		{"duplicate", func(l *erasureLedger) { l.Entries = append(l.Entries, l.Entries[0]) }},
		{"future_entry", func(l *erasureLedger) { l.Entries[0].At = now.Add(time.Hour) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			copy := valid
			copy.Entries = append([]erasureEntry{}, valid.Entries...)
			test.change(&copy)
			write(copy)
			if _, e := o.loadErasureLedger(snapshot); e == nil {
				t.Fatal("unsafe erasure journal accepted")
			}
		})
	}
	write(valid)
	b, _ := os.ReadFile(o.ledgerPath)
	b[len(b)-1] ^= 1
	os.WriteFile(o.ledgerPath, b, 0600)
	if _, e := o.loadErasureLedger(snapshot); e == nil {
		t.Fatal("tampered journal accepted")
	}
	write(valid)
	os.Chmod(o.ledgerPath, 0644)
	if _, e := o.loadErasureLedger(snapshot); e == nil {
		t.Fatal("public journal accepted")
	}
	o.ledgerPath = ""
	if _, e := o.loadErasureLedger(snapshot); e == nil {
		t.Fatal("missing journal accepted")
	}
}

func TestLostKeyWithOnlyErasureExport(t *testing.T) {
	o := fixtureOperator(t)
	var encrypted bytes.Buffer
	if err := backup.Seal(&encrypted, bytes.NewReader([]byte("fixture journal")), o.key); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(o.dir, "only.hrledger"), encrypted.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := o.loadKey(true); err == nil {
		t.Fatal("replaced a missing key despite existing independent erasure export")
	}
	if _, err := os.Stat(o.keyPath); !os.IsNotExist(err) {
		t.Fatal("replacement key created")
	}
}
