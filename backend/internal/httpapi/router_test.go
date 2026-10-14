package httpapi

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeDB struct{ err error }

func (f fakeDB) Check(context.Context) error { return f.err }

func TestReadiness(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"ready", nil, 200}, {"unavailable", errors.New("password=must-not-leak"), 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, path := range []string{"/readyz", "/api/v1/status"} {
				w := httptest.NewRecorder()
				Router(fakeDB{tc.err}).ServeHTTP(w, httptest.NewRequest("GET", path, nil))
				if w.Code != tc.status {
					t.Fatalf("status: %d", w.Code)
				}
				if strings.Contains(w.Body.String(), "must-not-leak") {
					t.Fatal("leaked database credentials")
				}
				if w.Header().Get("X-Request-ID") == "" {
					t.Fatal("missing request ID")
				}
			}
		})
	}
}

func TestHealthIndependentOfDB(t *testing.T) {
	w := httptest.NewRecorder()
	Router(fakeDB{errors.New("offline")}).ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
}

func TestUnknownRoute(t *testing.T) {
	w := httptest.NewRecorder()
	Router(fakeDB{}).ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/questions", nil))
	if w.Code != 404 {
		t.Fatalf("unimplemented route status %d", w.Code)
	}
}
