package httpapi

import "testing"

func TestQuestionValidation(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		options    []questionOption
		valid      bool
	}{
		{"single valid", "single_choice", []questionOption{{Label: "A", Correct: true}, {Label: "B"}}, true},
		{"single too many keys", "single_choice", []questionOption{{Label: "A", Correct: true}, {Label: "B", Correct: true}}, false},
		{"multiple valid", "multiple_choice", []questionOption{{Label: "A", Correct: true}, {Label: "B", Correct: true}}, true},
		{"multiple only one key", "multiple_choice", []questionOption{{Label: "A", Correct: true}, {Label: "B"}}, false},
		{"verbal valid", "verbal_tfu", []questionOption{{Label: "Верно", Correct: true}, {Label: "Неверно"}, {Label: "Недостаточно информации"}}, true},
		{"verbal wrong labels", "verbal_tfu", []questionOption{{Label: "A", Correct: true}, {Label: "B"}, {Label: "C"}}, false},
		{"duplicate options", "single_choice", []questionOption{{Label: "A", Correct: true}, {Label: " a "}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := questionInput{Type: tc.kind, Body: "Question", Difficulty: 2, Options: tc.options}
			message := validateQuestion(&in)
			if (message == "") != tc.valid {
				t.Fatalf("valid=%t, message=%s", tc.valid, message)
			}
		})
	}
}
