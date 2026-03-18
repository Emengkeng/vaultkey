-- =============================================================================
-- VaultKey — Complete Database Schema
-- =============================================================================
-- Single source of truth. Run this on a fresh database to get the full schema.
-- No incremental migrations — apply the whole file on first deploy.
-- =============================================================================


-- =============================================================================
-- Projects
-- Top-level tenant. One per developer/company.
-- =============================================================================

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


-- =============================================================================
-- Wallets
-- One wallet per user per chain. Holds encrypted key material only.
-- is_master: true if this wallet is a sweep destination (set by sweep service).
-- swept_at: last successful sweep timestamp, null if never swept.
-- =============================================================================

CREATE TABLE IF NOT EXISTS wallets (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id         TEXT NOT NULL,
    chain_type      TEXT NOT NULL CHECK (chain_type IN ('evm', 'solana')),
    address         TEXT NOT NULL,
    encrypted_key   BYTEA NOT NULL,
    encrypted_dek   BYTEA NOT NULL,
    label           TEXT,
    is_master       BOOLEAN NOT NULL DEFAULT false,
    swept_at        TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, user_id, chain_type, label)
);

CREATE INDEX IF NOT EXISTS idx_wallets_project_id ON wallets(project_id);
CREATE INDEX IF NOT EXISTS idx_wallets_user_id    ON wallets(project_id, user_id);


-- =============================================================================
-- Signing Jobs
-- Persisted before enqueue. Status machine:
--   pending → processing → completed
--                       ↘ failed (retryable)
--                       ↘ dead   (exhausted retries, moved to DLQ)
--
-- Operations:
--   sign_tx_evm / sign_msg_evm       — EVM transaction and message signing
--   sign_tx_solana / sign_msg_solana — Solana transaction and message signing
--   sweep_evm / sweep_solana         — Gasless full-balance sweep to master wallet
-- =============================================================================

CREATE TABLE IF NOT EXISTS signing_jobs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id),
    wallet_id       UUID NOT NULL REFERENCES wallets(id),
    idempotency_key TEXT,
    operation       TEXT NOT NULL CHECK (operation IN (
                        'sign_tx_evm',
                        'sign_msg_evm',
                        'sign_tx_solana',
                        'sign_msg_solana',
                        'sweep_evm',
                        'sweep_solana'
                    )),
    payload         JSONB NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN (
                        'pending',
                        'processing',
                        'completed',
                        'failed',
                        'dead'
                    )),
    result          JSONB,
    error           TEXT,
    attempts        INT NOT NULL DEFAULT 0,
    gasless         BOOLEAN NOT NULL DEFAULT false,
    relayer_wallet_id UUID,               -- set when a relayer pays gas for this job
    webhook_status  TEXT NOT NULL DEFAULT 'pending' CHECK (webhook_status IN (
                        'pending',
                        'delivered',
                        'failed',
                        'skipped'
                    )),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_jobs_project_id  ON signing_jobs(project_id);
CREATE INDEX IF NOT EXISTS idx_jobs_wallet_id   ON signing_jobs(wallet_id);
CREATE INDEX IF NOT EXISTS idx_jobs_status      ON signing_jobs(status);
CREATE INDEX IF NOT EXISTS idx_jobs_created_at  ON signing_jobs(created_at);

-- Partial index for stablecoin job filtering (payload carries _stablecoin metadata)
CREATE INDEX IF NOT EXISTS idx_jobs_stablecoin
    ON signing_jobs ((payload->'_stablecoin'->>'_sc_token'))
    WHERE payload ? '_stablecoin';


-- =============================================================================
-- Audit Log
-- Append-only. Every wallet creation and signing operation is recorded here.
-- Never update or delete rows.
-- =============================================================================

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

CREATE INDEX IF NOT EXISTS idx_audit_project_id ON audit_log(project_id);
CREATE INDEX IF NOT EXISTS idx_audit_wallet_id  ON audit_log(wallet_id);
CREATE INDEX IF NOT EXISTS idx_audit_created_at ON audit_log(created_at);


-- =============================================================================
-- Relayer Wallets
-- One active relayer per project per chain. Pays gas on behalf of user wallets
-- for gasless transactions and sweeps.
--
-- chain_id is NULL for Solana (single network) and required for EVM.
-- Deactivating a relayer sets active=false; the wallet record is kept.
--
-- Constraint: a master wallet must not also be registered as a relayer.
-- This is enforced at the application layer (cross-table CHECK not supported
-- in Postgres).
-- =============================================================================

CREATE TABLE IF NOT EXISTS relayer_wallets (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id          UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    wallet_id           UUID NOT NULL REFERENCES wallets(id),
    chain_type          TEXT NOT NULL CHECK (chain_type IN ('evm', 'solana')),
    chain_id            TEXT,
    min_balance_alert   TEXT NOT NULL DEFAULT '0.1',
    active              BOOLEAN NOT NULL DEFAULT true,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, chain_type, chain_id)
);

CREATE INDEX IF NOT EXISTS idx_relayer_project ON relayer_wallets(project_id, chain_type);

-- Wire the FK that signing_jobs references
ALTER TABLE signing_jobs
    ADD CONSTRAINT fk_signing_jobs_relayer
    FOREIGN KEY (relayer_wallet_id) REFERENCES relayer_wallets(id)
    NOT VALID;   -- NOT VALID: skip retroactive check, new rows are still enforced


-- =============================================================================
-- Sweep Config (project_sweep_config)
-- One config per project per chain. Points to the master wallet that receives
-- swept funds. Sweep jobs are only accepted when this is configured and enabled.
--
-- dust_threshold: minimum balance (in wei or lamports) worth sweeping.
--   "0" means always sweep regardless of balance.
-- =============================================================================

CREATE TABLE IF NOT EXISTS project_sweep_config (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id          UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    chain_type          TEXT NOT NULL CHECK (chain_type IN ('evm', 'solana')),
    chain_id            TEXT,
    master_wallet_id    UUID NOT NULL REFERENCES wallets(id),
    dust_threshold      TEXT NOT NULL DEFAULT '0',
    enabled             BOOLEAN NOT NULL DEFAULT true,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, chain_type, chain_id)
);

CREATE INDEX IF NOT EXISTS idx_sweep_config_project ON project_sweep_config(project_id);


-- =============================================================================
-- Stablecoin Token Registry
-- Operator-managed. No contract addresses are hardcoded in application code.
-- Populated via:
--   - Seed data below (mainnet defaults)
--   - Admin API: POST /admin/stablecoins
--   - Direct INSERT for testnet / custom deployments
--
-- token:    canonical lowercase identifier — "usdc", "usdt"
-- symbol:   display symbol — "USDC", "USDT"
-- chain_type: "evm" or "solana"
-- chain_id: EVM chain ID string ("137", "80001"), NULL for Solana
-- contract: EVM contract address or Solana mint address
-- decimals: token precision (6 for most stablecoins, 18 for BSC USDC/USDT)
-- enabled:  soft-disable a token without deleting it
-- =============================================================================

CREATE TABLE IF NOT EXISTS stablecoin_tokens (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    token       TEXT NOT NULL,
    symbol      TEXT NOT NULL,
    chain_type  TEXT NOT NULL CHECK (chain_type IN ('evm', 'solana')),
    chain_id    TEXT,
    contract    TEXT NOT NULL,
    decimals    INT NOT NULL DEFAULT 6,
    enabled     BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (token, chain_type, chain_id)
);

CREATE INDEX IF NOT EXISTS idx_stablecoin_token      ON stablecoin_tokens(token, chain_type);
CREATE INDEX IF NOT EXISTS idx_stablecoin_chain      ON stablecoin_tokens(chain_type, chain_id);
CREATE INDEX IF NOT EXISTS idx_stablecoin_enabled    ON stablecoin_tokens(enabled) WHERE enabled = true;


-- =============================================================================
-- Stablecoin Seed Data — Mainnet
-- These are the defaults for a mainnet deployment. For testnet, either:
--   a) Don't run this block and insert your testnet addresses manually, or
--   b) Run this then UPDATE/INSERT your testnet rows after.
--
-- Use ON CONFLICT DO NOTHING so re-running this file on an existing DB is safe.
-- =============================================================================

-- USDC — EVM mainnet
INSERT INTO stablecoin_tokens (token, symbol, chain_type, chain_id, contract, decimals) VALUES
    ('usdc', 'USDC', 'evm', '137',   '0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174', 6),  -- Polygon
    ('usdc', 'USDC', 'evm', '42161', '0xaf88d065e77c8cC2239327C5EDb3A432268e5831', 6),  -- Arbitrum
    ('usdc', 'USDC', 'evm', '8453',  '0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913', 6),  -- Base
    ('usdc', 'USDC', 'evm', '10',    '0x0b2C639c533813f4Aa9D7837CAf62653d097Ff85', 6),  -- Optimism
    ('usdc', 'USDC', 'evm', '56',    '0x8AC76a51cc950d9822D68b83fE1Ad97B32Cd580d', 18) -- BSC (18 decimals)
ON CONFLICT (token, chain_type, chain_id) DO NOTHING;

-- USDT — EVM mainnet
INSERT INTO stablecoin_tokens (token, symbol, chain_type, chain_id, contract, decimals) VALUES
    ('usdt', 'USDT', 'evm', '137',   '0xc2132D05D31c914a87C6611C10748AEb04B58e8F', 6),  -- Polygon
    ('usdt', 'USDT', 'evm', '42161', '0xFd086bC7CD5C481DCC9C85ebE478A1C0b69FCbb9', 6),  -- Arbitrum
    ('usdt', 'USDT', 'evm', '8453',  '0xfde4C96c8593536E31F229EA8f37b2ADa2699bb2', 6),  -- Base
    ('usdt', 'USDT', 'evm', '10',    '0x94b008aA00579c1307B0EF2c499aD98a8ce58e58', 6),  -- Optimism
    ('usdt', 'USDT', 'evm', '56',    '0x55d398326f99059fF775485246999027B3197955', 18) -- BSC (18 decimals)
ON CONFLICT (token, chain_type, chain_id) DO NOTHING;

-- USDC — Solana mainnet (chain_id is NULL for Solana)
INSERT INTO stablecoin_tokens (token, symbol, chain_type, chain_id, contract, decimals) VALUES
    ('usdc', 'USDC', 'solana', NULL, 'EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v', 6)
ON CONFLICT (token, chain_type, chain_id) DO NOTHING;

-- USDT — Solana mainnet
INSERT INTO stablecoin_tokens (token, symbol, chain_type, chain_id, contract, decimals) VALUES
    ('usdt', 'USDT', 'solana', NULL, 'Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB', 6)
ON CONFLICT (token, chain_type, chain_id) DO NOTHING;


-- =============================================================================
-- Testnet addresses (commented out — uncomment for testnet deployments)
-- =============================================================================

-- Polygon Mumbai (chain_id=80001)
-- INSERT INTO stablecoin_tokens (token, symbol, chain_type, chain_id, contract, decimals) VALUES
--     ('usdc', 'USDC', 'evm', '80001', '0x0FA8781a83E46826621b3BC094Ea2A0212e71B23', 6),
--     ('usdt', 'USDT', 'evm', '80001', '0xA02f6adc7926efeBBd59Fd43A84f4E0c0c91e832', 6)
-- ON CONFLICT (token, chain_type, chain_id) DO NOTHING;

-- Arbitrum Sepolia (chain_id=421614)
-- INSERT INTO stablecoin_tokens (token, symbol, chain_type, chain_id, contract, decimals) VALUES
--     ('usdc', 'USDC', 'evm', '421614', '0x75faf114eafb1BDbe2F0316DF893fd58CE46AA4d', 6)
-- ON CONFLICT (token, chain_type, chain_id) DO NOTHING;

-- Base Sepolia (chain_id=84532)
-- INSERT INTO stablecoin_tokens (token, symbol, chain_type, chain_id, contract, decimals) VALUES
--     ('usdc', 'USDC', 'evm', '84532', '0x036CbD53842c5426634e7929541eC2318f3dCF7e', 6)
-- ON CONFLICT (token, chain_type, chain_id) DO NOTHING;

-- Solana Devnet
-- INSERT INTO stablecoin_tokens (token, symbol, chain_type, chain_id, contract, decimals) VALUES
--     ('usdc', 'USDC', 'solana', NULL, '4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU', 6)
-- ON CONFLICT (token, chain_type, chain_id) DO NOTHING;














-- -- Projects: top-level tenant, one per dev/company
-- CREATE TABLE IF NOT EXISTS projects (
--     id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
--     name                TEXT NOT NULL,
--     api_key             TEXT NOT NULL UNIQUE,
--     api_secret_hash     TEXT NOT NULL,
--     webhook_url         TEXT,
--     webhook_secret      TEXT,
--     rate_limit_rps      INT NOT NULL DEFAULT 100,
--     max_retries         INT NOT NULL DEFAULT 3,
--     created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
-- );

-- -- Wallets
-- CREATE TABLE IF NOT EXISTS wallets (
--     id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
--     project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
--     user_id         TEXT NOT NULL,
--     chain_type      TEXT NOT NULL CHECK (chain_type IN ('evm', 'solana')),
--     address         TEXT NOT NULL,
--     encrypted_key   BYTEA NOT NULL,
--     encrypted_dek   BYTEA NOT NULL,
--     label           TEXT,
--     created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
--     UNIQUE (project_id, user_id, chain_type, label)
-- );

-- CREATE INDEX idx_wallets_project_id ON wallets(project_id);
-- CREATE INDEX idx_wallets_user_id ON wallets(project_id, user_id);

-- -- Signing jobs
-- CREATE TABLE IF NOT EXISTS signing_jobs (
--     id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
--     project_id      UUID NOT NULL REFERENCES projects(id),
--     wallet_id       UUID NOT NULL REFERENCES wallets(id),
--     idempotency_key TEXT,
--     operation       TEXT NOT NULL CHECK (operation IN ('sign_tx_evm','sign_msg_evm','sign_tx_solana','sign_msg_solana')),
--     payload         JSONB NOT NULL,
--     status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','completed','failed','dead')),
--     result          JSONB,
--     error           TEXT,
--     attempts        INT NOT NULL DEFAULT 0,
--     webhook_status  TEXT NOT NULL DEFAULT 'pending' CHECK (webhook_status IN ('pending','delivered','failed','skipped')),
--     created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
--     updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
--     UNIQUE (project_id, idempotency_key)
-- );

-- CREATE INDEX idx_jobs_project_id ON signing_jobs(project_id);
-- CREATE INDEX idx_jobs_wallet_id ON signing_jobs(wallet_id);
-- CREATE INDEX idx_jobs_status ON signing_jobs(status);
-- CREATE INDEX idx_jobs_created_at ON signing_jobs(created_at);

-- -- Audit log
-- CREATE TABLE IF NOT EXISTS audit_log (
--     id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
--     project_id  UUID NOT NULL REFERENCES projects(id),
--     wallet_id   UUID REFERENCES wallets(id),
--     job_id      UUID REFERENCES signing_jobs(id),
--     operation   TEXT NOT NULL,
--     actor       TEXT NOT NULL,
--     metadata    JSONB,
--     created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
-- );

-- CREATE INDEX idx_audit_project_id ON audit_log(project_id);
-- CREATE INDEX idx_audit_wallet_id ON audit_log(wallet_id);
-- CREATE INDEX idx_audit_created_at ON audit_log(created_at);

-- -- Relayer wallets: one per project per chain, pays gas on behalf of user wallets
-- CREATE TABLE IF NOT EXISTS relayer_wallets (
--     id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
--     project_id          UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
--     wallet_id           UUID NOT NULL REFERENCES wallets(id),  -- reuses wallet key storage
--     chain_type          TEXT NOT NULL CHECK (chain_type IN ('evm', 'solana')),
--     chain_id            TEXT,           -- specific EVM chain, null = all EVM chains for project
--     min_balance_alert   TEXT NOT NULL DEFAULT '0.1',  -- alert threshold (ETH or SOL)
--     active              BOOLEAN NOT NULL DEFAULT true,
--     created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
--     UNIQUE (project_id, chain_type, chain_id)  -- one relayer per chain per project
-- );

-- CREATE INDEX idx_relayer_project ON relayer_wallets(project_id, chain_type);

-- -- Add gasless flag to signing jobs
-- ALTER TABLE signing_jobs ADD COLUMN IF NOT EXISTS gasless BOOLEAN NOT NULL DEFAULT false;
-- ALTER TABLE signing_jobs ADD COLUMN IF NOT EXISTS relayer_wallet_id UUID REFERENCES relayer_wallets(id);
