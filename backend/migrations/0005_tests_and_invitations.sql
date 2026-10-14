ALTER TABLE tests ADD COLUMN status varchar(20) NOT NULL DEFAULT 'active'
    CHECK (status IN ('active','archived'));

CREATE TABLE vacancy_assessors (
    organization_id uuid NOT NULL,
    vacancy_id uuid NOT NULL,
    user_id uuid NOT NULL,
    PRIMARY KEY (organization_id,vacancy_id,user_id),
    FOREIGN KEY (organization_id,vacancy_id) REFERENCES vacancies(organization_id,id),
    FOREIGN KEY (organization_id,user_id) REFERENCES memberships(organization_id,user_id)
);
CREATE INDEX ix_vacancy_assessors_user ON vacancy_assessors(organization_id,user_id,vacancy_id);

CREATE FUNCTION protect_published_test_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status IN ('published','archived') OR EXISTS (
        SELECT 1 FROM invitations WHERE organization_id=OLD.organization_id AND test_version_id=OLD.id
    ) THEN
        RAISE EXCEPTION 'Published test versions are immutable' USING ERRCODE='55000';
    END IF;
    IF TG_OP='DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER test_version_immutable BEFORE UPDATE OR DELETE ON test_versions
    FOR EACH ROW EXECUTE FUNCTION protect_published_test_version();

CREATE FUNCTION protect_published_test_items() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE version_status text;
BEGIN
    IF TG_OP IN ('UPDATE','DELETE') THEN
        SELECT status INTO version_status FROM test_versions
            WHERE organization_id=OLD.organization_id AND id=OLD.test_version_id FOR UPDATE;
        IF version_status IN ('published','archived') THEN
            RAISE EXCEPTION 'Published test items are immutable' USING ERRCODE='55000';
        END IF;
    END IF;
    IF TG_OP IN ('INSERT','UPDATE') THEN
        SELECT status INTO version_status FROM test_versions
            WHERE organization_id=NEW.organization_id AND id=NEW.test_version_id FOR UPDATE;
        IF version_status IN ('published','archived') THEN
            RAISE EXCEPTION 'Published test items are immutable' USING ERRCODE='55000';
        END IF;
        RETURN NEW;
    END IF;
    RETURN OLD;
END;
$$;
CREATE TRIGGER test_items_immutable BEFORE INSERT OR UPDATE OR DELETE ON test_items
    FOR EACH ROW EXECUTE FUNCTION protect_published_test_items();
