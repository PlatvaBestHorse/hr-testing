package httpapi

import "testing"

func TestAuditDetailsAllowlist(t *testing.T) {
	safe := safeAuditDetails([]byte(`{"token":"secret","password":"secret","reason":"secret","format":"xlsx","row_count":5,"hold":true,"date_from":"2026-10-14","date_to":"not-a-date","candidates_erased":2,"audit_events_deleted":-1}`))
	if len(safe) != 5 || safe["format"] != "xlsx" || safe["row_count"] != int64(5) {
		t.Fatal("audit details allowlist failed")
	}
	for _, key := range []string{"token", "password", "reason", "date_to", "audit_events_deleted"} {
		if _, ok := safe[key]; ok {
			t.Fatal("unsafe audit detail exposed")
		}
	}
}
