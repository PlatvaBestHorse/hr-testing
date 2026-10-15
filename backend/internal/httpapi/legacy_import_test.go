package httpapi

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func legacyFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/candidate-tester-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestLegacyValidationAndDigest(t *testing.T) {
	raw := legacyFixture(t)
	doc, p := parseLegacy(raw)
	if !p.Valid || p.Questions != 2 || p.Tests != 2 || len(p.Categories) != 1 || !p.TestSettings[0].Published || p.TestSettings[0].ShowScore || p.TestSettings[0].Passing != 70.25 {
		t.Fatalf("invalid fixture conversion: %+v", p.Errors)
	}
	compact, _ := json.Marshal(doc)
	_, same := parseLegacy(compact)
	if p.Hash != same.Hash {
		t.Fatal("formatting changed content digest")
	}
	for _, tc := range []struct {
		name  string
		alter func(*legacyDocument)
	}{
		{"missing answer", func(d *legacyDocument) { d.Questions[0].Correct = "absent" }},
		{"duplicate question ID", func(d *legacyDocument) { d.Questions[1].ID = d.Questions[0].ID }},
		{"duplicate choice ID", func(d *legacyDocument) { d.Questions[0].Choices[1].ID = d.Questions[0].Choices[0].ID }},
		{"duplicate label", func(d *legacyDocument) { d.Questions[0].Choices[1].Text = " 3 " }},
		{"unsupported type", func(d *legacyDocument) { d.Questions[0].Type = "multiple" }},
		{"unknown difficulty", func(d *legacyDocument) { d.Questions[0].Difficulty = "Высокая" }},
		{"broken test reference", func(d *legacyDocument) { d.Tests[0].QuestionIDs[0] = "foreign" }},
		{"repeated test question", func(d *legacyDocument) { d.Tests[0].QuestionIDs[1] = d.Tests[0].QuestionIDs[0] }},
		{"missing score setting", func(d *legacyDocument) { d.Tests[0].ShowScore = nil }},
		{"bad duration", func(d *legacyDocument) { d.Tests[0].Minutes = 481 }},
		{"bad percent", func(d *legacyDocument) { v := 70.251; d.Tests[0].Passing = &v }},
		{"unsupported version", func(d *legacyDocument) { d.Version = 2 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var d legacyDocument
			json.Unmarshal(raw, &d)
			tc.alter(&d)
			b, _ := json.Marshal(d)
			_, p := parseLegacy(b)
			if p.Valid || len(p.Errors) == 0 {
				t.Fatal("invalid source accepted")
			}
		})
	}
	for _, bad := range []string{`{"version":1,"version":2}`, `{"version":1,"questions":[],"tests":[],"unknown":true}`, `{"version":1} {}`, `null`, strings.Repeat("[", 42) + strings.Repeat("]", 42)} {
		if _, p := parseLegacy([]byte(bad)); p.Valid {
			t.Fatal("ambiguous JSON accepted")
		}
	}
}
