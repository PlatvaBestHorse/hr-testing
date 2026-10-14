-- New attempts keep category labels as they were at start.
-- NULL on older attempts is intentional: their historical category cannot be reconstructed.
ALTER TABLE attempt_items ADD COLUMN category_snapshot jsonb;
ALTER TABLE attempt_items ADD CONSTRAINT ck_attempt_category_snapshot CHECK (
 category_snapshot IS NULL OR (jsonb_typeof(category_snapshot)='object'
 AND category_snapshot ? 'id' AND category_snapshot ? 'name'
 AND jsonb_typeof(category_snapshot->'id')='string'
 AND jsonb_typeof(category_snapshot->'name')='string'));
