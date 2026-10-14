-- Кадровый тестер Web v2.0 — базовая PostgreSQL-схема (MVP)
-- Требует PostgreSQL 15+; применять миграциями на пустой базе.
-- UUID генерируются функцией gen_random_uuid().
-- ВАЖНО: запускайте миграции отдельной ролью-владельцем; веб-приложение
-- должно пользоваться ролью без прав OWNER/BYPASSRLS.
BEGIN;

CREATE TABLE organizations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug varchar(80) NOT NULL UNIQUE,
    name varchar(250) NOT NULL,
    status varchar(20) NOT NULL DEFAULT 'active'
        CHECK (status IN ('active','suspended','archived')),
    settings jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email varchar(320) NOT NULL,
    password_hash text NOT NULL,
    display_name varchar(250) NOT NULL,
    status varchar(20) NOT NULL DEFAULT 'active'
        CHECK (status IN ('active','blocked')),
    mfa_enabled boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX ux_users_email_lower ON users (lower(email));

CREATE TABLE memberships (
    organization_id uuid NOT NULL REFERENCES organizations(id),
    user_id uuid NOT NULL REFERENCES users(id),
    role varchar(30) NOT NULL CHECK (role IN ('org_admin','recruiter','assessor')),
    status varchar(20) NOT NULL DEFAULT 'active'
        CHECK (status IN ('active','invited','disabled')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id,user_id)
);
CREATE INDEX ix_memberships_user ON memberships(user_id);

CREATE TABLE categories (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organizations(id),
    name varchar(150) NOT NULL,
    description text,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id,id),
    UNIQUE (organization_id,name)
);

CREATE TABLE questions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organizations(id),
    category_id uuid,
    difficulty smallint NOT NULL DEFAULT 2 CHECK (difficulty BETWEEN 1 AND 5),
    status varchar(20) NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft','active','archived')),
    created_by uuid REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id,id),
    FOREIGN KEY (organization_id,category_id) REFERENCES categories(organization_id,id)
);
CREATE INDEX ix_questions_org_category ON questions(organization_id,category_id,status);

-- Опубликованные версии вопросов неизменяемы на уровне бизнес-логики.
CREATE TABLE question_versions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    question_id uuid NOT NULL,
    version_no integer NOT NULL CHECK (version_no >= 1),
    type varchar(30) NOT NULL CHECK (type IN ('single_choice','verbal_tfu','multiple_choice')),
    passage text,
    body text NOT NULL,
    explanation text,
    media jsonb NOT NULL DEFAULT '[]'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id,id),
    UNIQUE (organization_id,question_id,version_no),
    FOREIGN KEY (organization_id,question_id) REFERENCES questions(organization_id,id)
);

-- is_correct доступен исключительно серверной части и уполномоченным HR.
CREATE TABLE question_options (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    question_version_id uuid NOT NULL,
    option_key varchar(30) NOT NULL,
    label text NOT NULL,
    is_correct boolean NOT NULL DEFAULT false,
    position smallint NOT NULL CHECK (position >= 1),
    UNIQUE (organization_id,id),
    UNIQUE (organization_id,question_version_id,option_key),
    UNIQUE (organization_id,question_version_id,position),
    FOREIGN KEY (organization_id,question_version_id)
        REFERENCES question_versions(organization_id,id)
);

CREATE TABLE tests (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organizations(id),
    code varchar(80),
    created_by uuid REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id,id),
    UNIQUE (organization_id,code)
);

CREATE TABLE test_versions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    test_id uuid NOT NULL,
    version_no integer NOT NULL CHECK (version_no >= 1),
    title varchar(250) NOT NULL,
    description text,
    duration_seconds integer NOT NULL CHECK (duration_seconds BETWEEN 60 AND 28800),
    pass_percent numeric(5,2) NOT NULL CHECK (pass_percent BETWEEN 0 AND 100),
    status varchar(20) NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft','published','archived')),
    shuffle_questions boolean NOT NULL DEFAULT true,
    shuffle_options boolean NOT NULL DEFAULT false,
    show_candidate_score boolean NOT NULL DEFAULT false,
    published_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id,id),
    UNIQUE (organization_id,test_id,version_no),
    FOREIGN KEY (organization_id,test_id) REFERENCES tests(organization_id,id)
);
CREATE INDEX ix_test_versions_published ON test_versions(organization_id,status,published_at);

CREATE TABLE test_items (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    test_version_id uuid NOT NULL,
    question_version_id uuid NOT NULL,
    position integer NOT NULL CHECK (position >= 1),
    points numeric(8,2) NOT NULL DEFAULT 1 CHECK (points > 0),
    UNIQUE (organization_id,id),
    UNIQUE (organization_id,test_version_id,position),
    UNIQUE (organization_id,test_version_id,question_version_id),
    FOREIGN KEY (organization_id,test_version_id)
        REFERENCES test_versions(organization_id,id),
    FOREIGN KEY (organization_id,question_version_id)
        REFERENCES question_versions(organization_id,id)
);

CREATE TABLE candidates (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organizations(id),
    full_name varchar(250) NOT NULL,
    email varchar(320),
    phone varchar(40),
    status varchar(20) NOT NULL DEFAULT 'active'
        CHECK (status IN ('active','archived','erasure_pending')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id,id)
);
CREATE INDEX ix_candidates_org_name ON candidates(organization_id,full_name);
CREATE INDEX ix_candidates_org_email ON candidates(organization_id,lower(email));

CREATE TABLE vacancies (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organizations(id),
    title varchar(250) NOT NULL,
    department varchar(200),
    status varchar(20) NOT NULL DEFAULT 'open'
        CHECK (status IN ('open','closed','archived')),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id,id)
);

CREATE TABLE invitations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organizations(id),
    candidate_id uuid NOT NULL,
    test_version_id uuid NOT NULL,
    vacancy_id uuid,
    token_hash bytea NOT NULL UNIQUE,
    status varchar(20) NOT NULL DEFAULT 'issued'
        CHECK (status IN ('issued','revoked','expired','completed')),
    max_attempts integer NOT NULL DEFAULT 1 CHECK (max_attempts BETWEEN 1 AND 20),
    expires_at timestamptz NOT NULL,
    issued_by uuid REFERENCES users(id),
    issued_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id,id),
    FOREIGN KEY (organization_id,candidate_id) REFERENCES candidates(organization_id,id),
    FOREIGN KEY (organization_id,test_version_id) REFERENCES test_versions(organization_id,id),
    FOREIGN KEY (organization_id,vacancy_id) REFERENCES vacancies(organization_id,id)
);
CREATE INDEX ix_invitations_org_candidate ON invitations(organization_id,candidate_id,issued_at DESC);

CREATE TABLE attempts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organizations(id),
    invitation_id uuid NOT NULL,
    candidate_id uuid NOT NULL,
    test_version_id uuid NOT NULL,
    attempt_no integer NOT NULL DEFAULT 1 CHECK (attempt_no >= 1),
    status varchar(20) NOT NULL DEFAULT 'in_progress'
        CHECK (status IN ('in_progress','submitted','expired','cancelled')),
    started_at timestamptz NOT NULL DEFAULT now(),
    deadline_at timestamptz NOT NULL,
    finished_at timestamptz,
    score_points numeric(10,2),
    max_points numeric(10,2),
    score_percent numeric(5,2) CHECK (score_percent BETWEEN 0 AND 100),
    passed boolean,
    revision integer NOT NULL DEFAULT 0,
    UNIQUE (organization_id,id),
    UNIQUE (organization_id,invitation_id,attempt_no),
    FOREIGN KEY (organization_id,invitation_id) REFERENCES invitations(organization_id,id),
    FOREIGN KEY (organization_id,candidate_id) REFERENCES candidates(organization_id,id),
    FOREIGN KEY (organization_id,test_version_id) REFERENCES test_versions(organization_id,id),
    CHECK (finished_at IS NULL OR finished_at >= started_at)
);
CREATE INDEX ix_attempts_org_created ON attempts(organization_id,started_at DESC);
CREATE INDEX ix_attempts_org_candidate ON attempts(organization_id,candidate_id,started_at DESC);

-- Снимок порядка вопросов и вариантов для конкретной попытки.
CREATE TABLE attempt_items (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    attempt_id uuid NOT NULL,
    test_item_id uuid NOT NULL,
    position integer NOT NULL CHECK (position >= 1),
    option_order jsonb NOT NULL DEFAULT '[]'::jsonb,
    UNIQUE (organization_id,id),
    UNIQUE (organization_id,attempt_id,position),
    UNIQUE (organization_id,attempt_id,test_item_id),
    FOREIGN KEY (organization_id,attempt_id) REFERENCES attempts(organization_id,id),
    FOREIGN KEY (organization_id,test_item_id) REFERENCES test_items(organization_id,id)
);

-- Одна запись на вопрос: атомарный UPSERT и оптимистическая блокировка.
CREATE TABLE attempt_answers (
    organization_id uuid NOT NULL,
    attempt_item_id uuid NOT NULL,
    selected_option_ids uuid[] NOT NULL DEFAULT '{}'::uuid[],
    revision integer NOT NULL DEFAULT 1 CHECK (revision >= 1),
    saved_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id,attempt_item_id),
    FOREIGN KEY (organization_id,attempt_item_id) REFERENCES attempt_items(organization_id,id)
);

CREATE TABLE candidate_consents (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL,
    candidate_id uuid NOT NULL,
    document_version varchar(80) NOT NULL,
    purpose varchar(120) NOT NULL,
    is_granted boolean NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT now(),
    evidence jsonb NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (organization_id,id),
    FOREIGN KEY (organization_id,candidate_id) REFERENCES candidates(organization_id,id)
);

CREATE TABLE attachments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organizations(id),
    storage_key text NOT NULL,
    mime_type varchar(120) NOT NULL,
    byte_size bigint NOT NULL CHECK (byte_size BETWEEN 1 AND 20000000),
    sha256 varchar(64) NOT NULL,
    uploaded_by uuid REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id,id),
    UNIQUE (organization_id,storage_key)
);

CREATE TABLE audit_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES organizations(id),
    actor_user_id uuid REFERENCES users(id),
    action varchar(120) NOT NULL,
    object_type varchar(90) NOT NULL,
    object_id uuid,
    details jsonb NOT NULL DEFAULT '{}'::jsonb,
    occurred_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ix_audit_org_time ON audit_events(organization_id,occurred_at DESC);

CREATE TABLE outbound_messages (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organizations(id),
    invitation_id uuid,
    recipient varchar(320) NOT NULL,
    template_code varchar(80) NOT NULL,
    status varchar(20) NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','sent','failed','cancelled')),
    attempts_count integer NOT NULL DEFAULT 0 CHECK (attempts_count >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    sent_at timestamptz,
    UNIQUE (organization_id,id),
    FOREIGN KEY (organization_id,invitation_id) REFERENCES invitations(organization_id,id)
);
CREATE INDEX ix_messages_pending ON outbound_messages(status,created_at) WHERE status = 'pending';

COMMIT;

-- Защита tenant-данных: ПРИМЕР применения, не включён автоматически.
-- Прежде чем включать RLS: спроектировать отдельную низкопривилегированную
-- роль приложения, безопасный resolver приглашений и SET LOCAL app.organization_id
-- на каждую транзакцию. Выполнить негативные cross-tenant тесты.
-- ALTER TABLE questions ENABLE ROW LEVEL SECURITY;
-- ALTER TABLE questions FORCE ROW LEVEL SECURITY;
-- CREATE POLICY questions_tenant_policy ON questions
--   USING (organization_id = NULLIF(current_setting('app.organization_id',true),'')::uuid)
--   WITH CHECK (organization_id = NULLIF(current_setting('app.organization_id',true),'')::uuid);
-- Применить аналогичную политику ко ВСЕМ tenant-таблицам, включая историю.
-- Нельзя использовать владельца таблиц/BYPASSRLS в качестве веб-роли.

-- Важно: корректность ответа (1 для single/verbal, 2+ для multiple),
-- соответствие option_ids вопросу, неизменяемость опубликованных версий,
-- проверка срока попытки и согласованность связи attempt/invitation/test_version
-- контролируются транзакционно в серверном приложении и автотестах.
-- Миграции, аккаунты, API и seed-data в этот файл не входят.
