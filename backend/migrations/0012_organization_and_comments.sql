ALTER TABLE organizations ADD COLUMN settings_revision integer NOT NULL DEFAULT 1 CHECK(settings_revision>0);
CREATE TABLE result_comments (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 organization_id uuid NOT NULL,
 attempt_id uuid NOT NULL,
 author_user_id uuid NOT NULL REFERENCES users(id),
 body text NOT NULL CHECK(length(btrim(body)) BETWEEN 1 AND 5000),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organization_id,attempt_id) REFERENCES attempts(organization_id,id),
 UNIQUE(organization_id,id)
);
CREATE INDEX ix_result_comments_attempt ON result_comments(organization_id,attempt_id,created_at,id);
