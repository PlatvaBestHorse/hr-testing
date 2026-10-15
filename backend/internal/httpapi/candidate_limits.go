package httpapi

import (
	"context"
	"encoding/hex"
	"net/http"
)

// Shared PostgreSQL counters also apply when more than one API process runs.
// Only IP hashes are stored; the invitation URL is never a bucket or log field.
func (a *App) limitCandidate(w http.ResponseWriter, r *http.Request) bool {
	limit := a.CandidateIPLimit
	if limit < 1 {
		limit = 600
	}
	bucket := "candidate-ip:" + hex.EncodeToString(digest(a.clientIP(r)))
	var attempts int
	err := a.Pool.QueryRow(r.Context(), `INSERT INTO auth_rate_limits(bucket) VALUES($1)
 ON CONFLICT(bucket) DO UPDATE SET
 attempts=CASE WHEN auth_rate_limits.window_started_at<=clock_timestamp()-interval '1 minute' THEN 1 ELSE auth_rate_limits.attempts+1 END,
 window_started_at=CASE WHEN auth_rate_limits.window_started_at<=clock_timestamp()-interval '1 minute' THEN clock_timestamp() ELSE auth_rate_limits.window_started_at END
 RETURNING attempts`, bucket).Scan(&attempts)
	if err != nil {
		a.failure(w, err)
		return false
	}
	if attempts > limit {
		w.Header().Set("Retry-After", "60")
		problem(w, 429, "rate_limited", "Слишком много запросов. Повторите через минуту")
		return false
	}
	return true
}

// Bounded cleanup of expired technical counters, unrelated to personal-data
// retention policy. Every counter window is at most 15 minutes.
func (a *App) CleanupRateLimits(ctx context.Context) error {
	_, err := a.Pool.Exec(ctx, `DELETE FROM auth_rate_limits WHERE window_started_at<clock_timestamp()-interval '1 day' AND bucket IN
 (SELECT bucket FROM auth_rate_limits WHERE window_started_at<clock_timestamp()-interval '1 day'
 ORDER BY window_started_at LIMIT 1000)`)
	return err
}
