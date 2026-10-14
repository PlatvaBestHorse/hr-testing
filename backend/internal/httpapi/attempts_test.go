package httpapi

import "testing"

func TestScorePercentageRounding(t *testing.T) {
	for _, tc := range []struct{ earned, max, want int64 }{{0, 0, 0}, {1, 3, 3333}, {2, 3, 6667}, {1, 32, 313}, {9999999999, 9999999999, 10000}, {250, 400, 6250}} {
		if got := scorePercent(tc.earned, tc.max); got != tc.want {
			t.Errorf("percentage got %d want %d", got, tc.want)
		}
	}
}
