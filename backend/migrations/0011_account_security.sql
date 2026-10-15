CREATE TABLE user_security (
 user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 secret_ciphertext bytea,
 pending_ciphertext bytea,
 pending_until timestamptz,
 last_step bigint NOT NULL DEFAULT -1
);
CREATE TABLE mfa_recovery_codes (
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 code_hash bytea NOT NULL CHECK(octet_length(code_hash)=32),
 PRIMARY KEY(user_id,code_hash)
);
CREATE TABLE account_tokens (
 token_hash bytea PRIMARY KEY CHECK(octet_length(token_hash)=32),
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 organization_id uuid NOT NULL REFERENCES organizations(id),
 purpose text NOT NULL CHECK(purpose IN ('reset','staff_invite')),
 password_binding bytea NOT NULL CHECK(octet_length(password_binding)=32),
 expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ix_account_tokens_user ON account_tokens(user_id,purpose);
CREATE TABLE account_mail (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 token_hash bytea NOT NULL REFERENCES account_tokens(token_hash) ON DELETE CASCADE,
 payload bytea,
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','sent','failed','cancelled')),
 attempts integer NOT NULL DEFAULT 0,
 available_at timestamptz NOT NULL DEFAULT now(),
 created_at timestamptz NOT NULL DEFAULT now(),
 CHECK(status IN ('sent','cancelled') OR payload IS NOT NULL)
);
CREATE INDEX ix_account_mail_pending ON account_mail(available_at) WHERE status IN ('pending','failed');
