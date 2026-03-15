-- =============================================================================
-- VaultKey — Cloud Features Migration
-- Adds Clerk-based multi-tenant SaaS support while maintaining backward
-- compatibility with self-hosted API key auth.
--
-- Run AFTER the initial schema (001_initial.sql / schema.sql).
-- Safe to re-run: all statements use IF NOT EXISTS / ON CONFLICT DO NOTHING.
-- =============================================================================


-- =============================================================================
-- Clerk Users Cache
-- Synced via Clerk webhooks (user.created, user.updated, user.deleted).
-- Never stores passwords — Clerk owns auth state.
-- =============================================================================

CREATE TABLE IF NOT EXISTS clerk_users (
    clerk_user_id   TEXT PRIMARY KEY CHECK (clerk_user_id ~ '^user_[a-zA-Z0-9]+$'),
    email           TEXT NOT NULL,
    first_name      TEXT NOT NULL DEFAULT '',
    last_name       TEXT NOT NULL DEFAULT '',
    image_url       TEXT NOT NULL DEFAULT '',
    synced_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_clerk_users_email ON clerk_users(email);


-- =============================================================================
-- Organizations
-- Top-level multi-tenant unit. One org per Clerk user on signup (auto-created).
-- Supports soft-delete: deleted_at IS NULL means active.
-- =============================================================================

CREATE TABLE IF NOT EXISTS organizations (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL,
    slug            TEXT NOT NULL UNIQUE,
    created_by      TEXT NOT NULL CHECK (created_by ~ '^user_[a-zA-Z0-9]+$'),
    billing_email   TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_orgs_created_by  ON organizations(created_by) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_orgs_slug        ON organizations(slug)       WHERE deleted_at IS NULL;


-- =============================================================================
-- Org Members
-- RBAC roles: owner | admin | developer | viewer
-- Only one owner per org (the creator). Owner cannot be changed or removed
-- if they are the last member. Enforced in application layer.
-- soft-delete: deleted_at IS NOT NULL = removed from org.
-- =============================================================================

CREATE TABLE IF NOT EXISTS org_members (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    clerk_user_id   TEXT NOT NULL CHECK (clerk_user_id ~ '^user_[a-zA-Z0-9]+$'),
    role            TEXT NOT NULL DEFAULT 'developer'
                        CHECK (role IN ('owner', 'admin', 'developer', 'viewer')),
    joined_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ,
    UNIQUE (org_id, clerk_user_id)
);

CREATE INDEX IF NOT EXISTS idx_org_members_user     ON org_members(clerk_user_id)  WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_org_members_org      ON org_members(org_id)         WHERE deleted_at IS NULL;


-- =============================================================================
-- Invites
-- Email-based invite flow. Token is a random hex string (32 bytes).
-- expires_at defaults to 7 days. accepted_at set on acceptance.
-- accepted_by is the clerk_user_id of the user who accepted.
-- soft-delete used for revocation (deleted_at IS NOT NULL).
-- =============================================================================

CREATE TABLE IF NOT EXISTS invites (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    email           TEXT NOT NULL,
    token           TEXT NOT NULL UNIQUE,
    role            TEXT NOT NULL DEFAULT 'developer'
                        CHECK (role IN ('admin', 'developer', 'viewer')),
    created_by      TEXT NOT NULL CHECK (created_by ~ '^user_[a-zA-Z0-9]+$'),
    expires_at      TIMESTAMPTZ NOT NULL DEFAULT (now() + INTERVAL '7 days'),
    accepted_at     TIMESTAMPTZ,
    accepted_by     TEXT CHECK (accepted_by IS NULL OR accepted_by ~ '^user_[a-zA-Z0-9]+$'),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_invites_token    ON invites(token)   WHERE deleted_at IS NULL AND accepted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_invites_email    ON invites(email)   WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_invites_org      ON invites(org_id)  WHERE deleted_at IS NULL;


-- =============================================================================
-- API Keys
-- Replaces embedded api_key/api_secret_hash columns in projects table.
-- Multiple keys per project for rotation. Soft-delete for revocation.
-- key is the public identifier (sent in X-API-Key header).
-- secret_hash is bcrypt of the raw secret (shown once on creation).
-- =============================================================================

CREATE TABLE IF NOT EXISTS api_keys (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name            TEXT NOT NULL DEFAULT 'Default',
    key             TEXT NOT NULL UNIQUE,
    secret_hash     TEXT NOT NULL,
    created_by      TEXT CHECK (created_by IS NULL OR created_by ~ '^user_[a-zA-Z0-9]+$'),
    active          BOOLEAN NOT NULL DEFAULT true,
    last_used_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_api_keys_project     ON api_keys(project_id)  WHERE deleted_at IS NULL AND active = true;
CREATE INDEX IF NOT EXISTS idx_api_keys_key         ON api_keys(key)         WHERE deleted_at IS NULL AND active = true;


-- =============================================================================
-- Modify projects table
-- Add org_id (nullable for backward compat) and deleted_at.
-- Existing rows keep org_id = NULL and continue to use the old api_key column.
-- New cloud-mode projects set org_id and use the api_keys table.
-- =============================================================================

ALTER TABLE projects
    ADD COLUMN IF NOT EXISTS org_id     UUID REFERENCES organizations(id),
    ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;

-- One active project per org
CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_org_id
    ON projects(org_id)
    WHERE deleted_at IS NULL AND org_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_projects_org ON projects(org_id) WHERE deleted_at IS NULL;


-- =============================================================================
-- Migrate existing API keys into the api_keys table
-- Existing projects that have api_key / api_secret_hash columns get a row
-- in api_keys so the new auth middleware can find them.
-- This is idempotent: ON CONFLICT DO NOTHING.
-- After all clients migrate you can drop api_key/api_secret_hash from projects.
-- =============================================================================

-- INSERT INTO api_keys (project_id, name, key, secret_hash, created_by, active)
-- SELECT
--     p.id,
--     'Legacy Key',
--     p.api_key,
--     p.api_secret_hash,
--     NULL,
--     true
-- FROM projects p
-- WHERE p.api_key IS NOT NULL
--   AND p.api_secret_hash IS NOT NULL
--   AND NOT EXISTS (
--       SELECT 1 FROM api_keys ak WHERE ak.project_id = p.id AND ak.key = p.api_key
--   );