CREATE TABLE auth_sessions (
    token_hash bytea PRIMARY KEY,
    organization_id uuid NOT NULL,
    user_id uuid NOT NULL,
    csrf_token varchar(64) NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (organization_id,user_id) REFERENCES memberships(organization_id,user_id)
);
CREATE INDEX ix_auth_sessions_user ON auth_sessions(user_id,organization_id);
CREATE INDEX ix_auth_sessions_expiry ON auth_sessions(expires_at);

CREATE TABLE auth_rate_limits (
    bucket text PRIMARY KEY,
    window_started_at timestamptz NOT NULL DEFAULT now(),
    attempts integer NOT NULL DEFAULT 1
);

-- Table privileges follow the owner defaults established in migration 0002.
