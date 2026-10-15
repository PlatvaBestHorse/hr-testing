-- Full erasure is distinct from contact redaction. No personal text in receipts.
CREATE TABLE erasure_previews (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 organization_id uuid NOT NULL REFERENCES organizations(id),
 actor_user_id uuid NOT NULL REFERENCES users(id),
 candidate_id uuid NOT NULL,
 fingerprint text NOT NULL,
 expires_at timestamptz NOT NULL,
 applied_at timestamptz,
 result jsonb,
 UNIQUE(organization_id,id)
);
CREATE TABLE candidate_erasures (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 organization_id uuid NOT NULL REFERENCES organizations(id),
 candidate_id uuid NOT NULL,
 scope text NOT NULL CHECK(scope IN ('contacts','all')),
 erased_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(organization_id,candidate_id,scope)
);
REVOKE UPDATE,DELETE ON candidate_erasures FROM hr_app;
CREATE INDEX ix_candidate_erasures_time ON candidate_erasures(erased_at,id);
-- Binds an independent erasure export to this database lineage, including a
-- restored snapshot; two independently initialized databases cannot be mixed.
CREATE TABLE privacy_ledger_state (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 identity uuid NOT NULL DEFAULT gen_random_uuid()
);
INSERT INTO privacy_ledger_state(singleton) VALUES(true);
REVOKE INSERT,UPDATE,DELETE ON privacy_ledger_state FROM hr_app;

CREATE TABLE consent_documents (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 organization_id uuid NOT NULL REFERENCES organizations(id),
 revision integer NOT NULL CHECK(revision>0),
 operator_name text NOT NULL CHECK(length(operator_name)<=250),
 contact text NOT NULL CHECK(length(contact)<=320),
 consent_text text NOT NULL CHECK(length(consent_text) BETWEEN 1 AND 20000),
 policy_text text NOT NULL CHECK(length(policy_text) BETWEEN 1 AND 20000),
 approved boolean NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(organization_id,revision),
 UNIQUE(organization_id,id)
);
REVOKE UPDATE,DELETE ON consent_documents FROM hr_app;
ALTER TABLE candidate_consents ADD COLUMN consent_document_id uuid;
ALTER TABLE candidate_consents ADD FOREIGN KEY(organization_id,consent_document_id) REFERENCES consent_documents(organization_id,id);
