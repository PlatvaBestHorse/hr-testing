-- The app role must already exist (see infrastructure/postgres/init.sh).
-- Never run the API as the migration owner. RLS is a future layer, not enabled here.
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO hr_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO hr_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO hr_app;
REVOKE ALL ON schema_migrations FROM hr_app;
GRANT SELECT ON schema_migrations TO hr_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO hr_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO hr_app;
