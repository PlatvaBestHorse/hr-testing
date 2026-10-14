ALTER TABLE candidates ADD COLUMN erased_at timestamptz;
ALTER TABLE candidates ADD COLUMN retention_hold boolean NOT NULL DEFAULT false;
ALTER TABLE candidates ADD COLUMN retention_hold_reason text NOT NULL DEFAULT '';
ALTER TABLE candidates ADD CONSTRAINT ck_candidate_hold_reason CHECK (NOT retention_hold OR length(btrim(retention_hold_reason)) BETWEEN 1 AND 2000);

CREATE TABLE retention_policies (
 organization_id uuid PRIMARY KEY REFERENCES organizations(id),
 enabled boolean NOT NULL DEFAULT false,
 candidate_days integer CHECK(candidate_days BETWEEN 1 AND 36500),
 audit_days integer CHECK(audit_days BETWEEN 1 AND 36500),
 purpose text NOT NULL DEFAULT '',
 legal_basis text NOT NULL DEFAULT '',
 revision integer NOT NULL DEFAULT 1 CHECK(revision>=1),
 updated_at timestamptz NOT NULL DEFAULT now(),
 updated_by uuid NOT NULL REFERENCES users(id),
 CHECK(NOT enabled OR (length(btrim(purpose)) BETWEEN 1 AND 2000 AND length(btrim(legal_basis)) BETWEEN 1 AND 2000 AND (candidate_days IS NOT NULL OR audit_days IS NOT NULL)))
);
CREATE TABLE retention_previews (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 organization_id uuid NOT NULL REFERENCES organizations(id),
 actor_user_id uuid NOT NULL REFERENCES users(id),
 policy_revision integer NOT NULL,
 as_of timestamptz NOT NULL,
 expires_at timestamptz NOT NULL,
 candidate_ids uuid[] NOT NULL DEFAULT '{}',
 audit_ids bigint[] NOT NULL DEFAULT '{}',
 audit_cutoff timestamptz,
 applied_at timestamptz,
 result jsonb,
 CHECK(cardinality(candidate_ids)<=100 AND cardinality(audit_ids)<=1000),
 UNIQUE(organization_id,id)
);
CREATE INDEX ix_retention_previews_org_time ON retention_previews(organization_id,as_of DESC);

-- Ordinary runtime access is append-only. Retention deletes go through this narrow function.
REVOKE UPDATE, DELETE ON audit_events FROM hr_app;
CREATE FUNCTION prune_audit_events(preview_id uuid, session_hash bytea) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE selected public.retention_previews%ROWTYPE; days integer; removed bigint;
BEGIN
 SELECT rp.* INTO selected FROM public.retention_previews rp
 JOIN public.retention_policies policy ON policy.organization_id=rp.organization_id AND policy.revision=rp.policy_revision
 JOIN public.auth_sessions s ON s.organization_id=rp.organization_id AND s.user_id=rp.actor_user_id
 JOIN public.memberships m ON m.organization_id=s.organization_id AND m.user_id=s.user_id
 JOIN public.users u ON u.id=s.user_id JOIN public.organizations o ON o.id=s.organization_id
 WHERE rp.id=preview_id AND rp.applied_at IS NULL AND rp.expires_at>clock_timestamp()
 AND policy.enabled AND policy.audit_days IS NOT NULL AND s.token_hash=session_hash AND s.expires_at>clock_timestamp()
 AND m.role='org_admin' AND m.status='active' AND u.status='active' AND o.status='active'
 FOR UPDATE OF rp;
 IF NOT FOUND THEN RAISE EXCEPTION 'retention operation not authorized' USING ERRCODE='42501'; END IF;
 SELECT audit_days INTO days FROM public.retention_policies WHERE organization_id=selected.organization_id;
 IF selected.audit_cutoff IS NULL OR selected.audit_cutoff>clock_timestamp()-make_interval(days=>days) THEN
  RAISE EXCEPTION 'invalid audit cutoff' USING ERRCODE='42501';
 END IF;
 DELETE FROM public.audit_events WHERE organization_id=selected.organization_id
 AND id=ANY(selected.audit_ids) AND occurred_at<selected.audit_cutoff;
 GET DIAGNOSTICS removed=ROW_COUNT;
 RETURN removed;
END $$;
REVOKE ALL ON FUNCTION prune_audit_events(uuid,bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION prune_audit_events(uuid,bytea) TO hr_app;
