ALTER TABLE question_versions ADD COLUMN status varchar(20) NOT NULL DEFAULT 'draft'
    CHECK (status IN ('draft','published'));
ALTER TABLE question_versions ADD COLUMN published_at timestamptz;

CREATE FUNCTION protect_published_question_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status = 'published' OR EXISTS (
        SELECT 1 FROM test_items WHERE organization_id = OLD.organization_id AND question_version_id = OLD.id
    ) THEN
        RAISE EXCEPTION 'Published question versions are immutable' USING ERRCODE = '55000';
    END IF;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER question_version_immutable BEFORE UPDATE OR DELETE ON question_versions
    FOR EACH ROW EXECUTE FUNCTION protect_published_question_version();

CREATE FUNCTION protect_published_question_options() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    target_id uuid;
    target_org uuid;
    version_status text;
BEGIN
    IF TG_OP IN ('UPDATE','DELETE') THEN
        SELECT status INTO version_status FROM question_versions
            WHERE organization_id = OLD.organization_id AND id = OLD.question_version_id FOR UPDATE;
        IF version_status = 'published' THEN
            RAISE EXCEPTION 'Published question options are immutable' USING ERRCODE = '55000';
        END IF;
    END IF;
    IF TG_OP IN ('INSERT','UPDATE') THEN
        target_id := NEW.question_version_id;
        target_org := NEW.organization_id;
        SELECT status INTO version_status FROM question_versions
            WHERE organization_id = target_org AND id = target_id FOR UPDATE;
        IF version_status = 'published' THEN
            RAISE EXCEPTION 'Published question options are immutable' USING ERRCODE = '55000';
        END IF;
        RETURN NEW;
    END IF;
    RETURN OLD;
END;
$$;
CREATE TRIGGER question_options_immutable BEFORE INSERT OR UPDATE OR DELETE ON question_options
    FOR EACH ROW EXECUTE FUNCTION protect_published_question_options();
