-- Inline private image storage keeps database snapshots and images consistent.
-- Existing attachment records remain compatible; only new inline images are served.
ALTER TABLE attachments ADD COLUMN data bytea;
ALTER TABLE attachments ADD COLUMN width integer;
ALTER TABLE attachments ADD COLUMN height integer;
ALTER TABLE attachments ADD CONSTRAINT ck_inline_image CHECK (
 data IS NULL OR (mime_type IN ('image/png','image/jpeg') AND
 byte_size=octet_length(data) AND byte_size<=5242880 AND
 width IS NOT NULL AND height IS NOT NULL AND width BETWEEN 1 AND 4096 AND height BETWEEN 1 AND 4096 AND
 width::bigint*height<=8000000 AND length(sha256)=64)
);

ALTER TABLE question_versions ADD COLUMN image_id uuid;
ALTER TABLE question_versions ADD CONSTRAINT fk_question_image
 FOREIGN KEY (organization_id,image_id) REFERENCES attachments(organization_id,id);
ALTER TABLE question_options ADD COLUMN image_id uuid;
ALTER TABLE question_options ADD CONSTRAINT fk_option_image
 FOREIGN KEY (organization_id,image_id) REFERENCES attachments(organization_id,id);
CREATE INDEX ix_question_image ON question_versions(organization_id,image_id) WHERE image_id IS NOT NULL;
CREATE INDEX ix_option_image ON question_options(organization_id,image_id) WHERE image_id IS NOT NULL;

-- The original immutable-version triggers also protect the new image references.
-- Files themselves cannot change. Unreferenced uploads can be reclaimed; FK
-- locks prevent deletion racing with creation of a new question version.
CREATE FUNCTION protect_attachment_data() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' THEN
  RAISE EXCEPTION 'Attachment bytes and metadata are immutable' USING ERRCODE='55000';
 END IF;
 RETURN OLD;
END $$;
CREATE TRIGGER attachment_immutable BEFORE UPDATE ON attachments
 FOR EACH ROW EXECUTE FUNCTION protect_attachment_data();
REVOKE UPDATE ON attachments FROM hr_app;
