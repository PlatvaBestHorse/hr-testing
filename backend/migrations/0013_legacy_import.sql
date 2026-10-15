CREATE TABLE import_batches (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 organization_id uuid NOT NULL REFERENCES organizations(id),
 actor_user_id uuid NOT NULL REFERENCES users(id),
 source_hash bytea NOT NULL CHECK(octet_length(source_hash)=32),
 result jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(organization_id,id),
 UNIQUE(organization_id,source_hash)
);
CREATE TABLE imported_questions (
 organization_id uuid NOT NULL,
 legacy_id varchar(80) NOT NULL,
 question_id uuid NOT NULL,
 PRIMARY KEY(organization_id,legacy_id),
 FOREIGN KEY(organization_id,question_id) REFERENCES questions(organization_id,id)
);
CREATE TABLE imported_tests (
 organization_id uuid NOT NULL,
 legacy_id varchar(80) NOT NULL,
 test_id uuid NOT NULL,
 PRIMARY KEY(organization_id,legacy_id),
 FOREIGN KEY(organization_id,test_id) REFERENCES tests(organization_id,id)
);
