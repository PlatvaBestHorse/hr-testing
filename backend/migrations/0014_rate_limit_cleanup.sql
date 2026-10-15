-- Supports bounded expiry of technical rate-limit buckets, not user content.
CREATE INDEX ix_auth_rate_limits_window ON auth_rate_limits(window_started_at);
