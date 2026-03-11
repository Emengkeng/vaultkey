-- Projects: top-level tenant, one per dev/company
CREATE TABLE IF NOT EXISTS projects (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                TEXT NOT NULL,
    api_key             TEXT NOT NULL UNIQUE,
    api_secret_hash     TEXT NOT NULL,
    webhook_url         TEXT,
    webhook_secret      TEXT,
    rate_limit_rps      INT NOT NULL DEFAULT 100,
    max_retries         INT NOT NULL DEFAULT 3,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Wallets
CREATE TABLE IF NOT EXISTS wallets (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id         TEXT NOT NULL,
    chain_type      TEXT NOT NULL CHECK (chain_type IN ('evm', 'solana')),
    address         TEXT NOT NULL,
    encrypted_key   BYTEA NOT NULL,
    encrypted_dek   BYTEA NOT NULL,
    label           TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, user_id, chain_type, label)
);

CREATE INDEX idx_wallets_project_id ON wallets(project_id);
CREATE INDEX idx_wallets_user_id ON wallets(project_id, user_id);

-- Signing jobs
CREATE TABLE IF NOT EXISTS signing_jobs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id),
    wallet_id       UUID NOT NULL REFERENCES wallets(id),
    idempotency_key TEXT,
    operation       TEXT NOT NULL CHECK (operation IN ('sign_tx_evm','sign_msg_evm','sign_tx_solana','sign_msg_solana')),
    payload         JSONB NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','completed','failed','dead')),
    result          JSONB,
    error           TEXT,
    attempts        INT NOT NULL DEFAULT 0,
    webhook_status  TEXT NOT NULL DEFAULT 'pending' CHECK (webhook_status IN ('pending','delivered','failed','skipped')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, idempotency_key)
);

CREATE INDEX idx_jobs_project_id ON signing_jobs(project_id);
CREATE INDEX idx_jobs_wallet_id ON signing_jobs(wallet_id);
CREATE INDEX idx_jobs_status ON signing_jobs(status);
CREATE INDEX idx_jobs_created_at ON signing_jobs(created_at);

-- Audit log
CREATE TABLE IF NOT EXISTS audit_log (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID NOT NULL REFERENCES projects(id),
    wallet_id   UUID REFERENCES wallets(id),
    job_id      UUID REFERENCES signing_jobs(id),
    operation   TEXT NOT NULL,
    actor       TEXT NOT NULL,
    metadata    JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_project_id ON audit_log(project_id);
CREATE INDEX idx_audit_wallet_id ON audit_log(wallet_id);
CREATE INDEX idx_audit_created_at ON audit_log(created_at);

-- Relayer wallets: one per project per chain, pays gas on behalf of user wallets
CREATE TABLE IF NOT EXISTS relayer_wallets (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id          UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    wallet_id           UUID NOT NULL REFERENCES wallets(id),  -- reuses wallet key storage
    chain_type          TEXT NOT NULL CHECK (chain_type IN ('evm', 'solana')),
    chain_id            TEXT,           -- specific EVM chain, null = all EVM chains for project
    min_balance_alert   TEXT NOT NULL DEFAULT '0.1',  -- alert threshold (ETH or SOL)
    active              BOOLEAN NOT NULL DEFAULT true,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, chain_type, chain_id)  -- one relayer per chain per project
);

CREATE INDEX idx_relayer_project ON relayer_wallets(project_id, chain_type);

-- Add gasless flag to signing jobs
ALTER TABLE signing_jobs ADD COLUMN IF NOT EXISTS gasless BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE signing_jobs ADD COLUMN IF NOT EXISTS relayer_wallet_id UUID REFERENCES relayer_wallets(id);
