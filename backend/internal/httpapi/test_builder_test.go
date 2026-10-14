package httpapi

import "testing"

func TestDecimalWeights(t *testing.T) {
	for _, tc := range []struct {
		value    string
		min, max int64
		valid    bool
	}{{"0.01", 1, 99999999, true}, {"1.50", 1, 99999999, true}, {"999999.99", 1, 99999999, true}, {"0", 1, 99999999, false}, {"1.001", 1, 99999999, false}, {"NaN", 1, 99999999, false}, {"1e2", 1, 99999999, false}, {"100", 0, 10000, true}, {"100.01", 0, 10000, false}} {
		t.Run(tc.value, func(t *testing.T) {
			_, ok := decimalCents(tc.value, tc.min, tc.max)
			if ok != tc.valid {
				t.Fatal("decimal validation mismatch")
			}
		})
	}
	in := testInput{Title: "Test", Duration: 60, PassPercent: "70", Items: []testItemInput{}}
	if errors := validateTest(&in); len(errors) != 0 {
		t.Fatal("valid empty draft rejected")
	}
	in.Items = []testItemInput{{QuestionVersionID: "00000000-0000-0000-0000-000000000001", Points: "1"}, {QuestionVersionID: "00000000-0000-0000-0000-000000000001", Points: "1"}}
	if errors := validateTest(&in); len(errors) == 0 {
		t.Fatal("duplicate question accepted")
	}
}
