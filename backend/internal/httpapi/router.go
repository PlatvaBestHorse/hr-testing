package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"
)

type Checker interface{ Check(context.Context) error }

func Router(db Checker, apps ...*App) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		write(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	ready := func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.Check(ctx); err != nil {
			write(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "code": "database_unavailable"})
			return
		}
		write(w, http.StatusOK, map[string]string{"status": "ready"})
	}
	mux.HandleFunc("GET /readyz", ready)
	mux.HandleFunc("GET /api/v1/status", ready)
	if len(apps) > 0 {
		apps[0].register(mux)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		write(w, http.StatusNotFound, map[string]string{"code": "not_found", "message": "Маршрут не найден"})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var id [16]byte
		rand.Read(id[:])
		w.Header().Set("X-Request-ID", hex.EncodeToString(id[:]))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})
}

func write(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}
