-- Add master wallet flag and sweep tracking to wallets
ALTER TABLE wallets ADD COLUMN IF NOT EXISTS is_master BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE wallets ADD COLUMN IF NOT EXISTS swept_at TIMESTAMPTZ;

-- Extend signing_jobs operation constraint to include sweep operations
ALTER TABLE signing_jobs DROP CONSTRAINT IF EXISTS signing_jobs_operation_check;
ALTER TABLE signing_jobs ADD CONSTRAINT signing_jobs_operation_check
    CHECK (operation IN (
        'sign_tx_evm',
        'sign_msg_evm',
        'sign_tx_solana',
        'sign_msg_solana',
        'sweep_evm',
        'sweep_solana'
    ));

-- Sweep config per project per chain
-- A project must have this configured before sweep jobs are accepted
CREATE TABLE IF NOT EXISTS project_sweep_config (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id          UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    chain_type          TEXT NOT NULL CHECK (chain_type IN ('evm', 'solana')),
    chain_id            TEXT,                          -- EVM chain id, null for Solana
    master_wallet_id    UUID NOT NULL REFERENCES wallets(id),
    dust_threshold      TEXT NOT NULL DEFAULT '0',     -- minimum balance to sweep (wei for EVM, lamports for Solana)
    enabled             BOOLEAN NOT NULL DEFAULT true,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, chain_type, chain_id)
);

CREATE INDEX idx_sweep_config_project ON project_sweep_config(project_id);

-- Enforce: master wallet cannot also be a relayer wallet
-- This is a check done at the application layer, not DB level,
-- because cross-table CHECK constraints are not supported in Postgres.
-- The storage layer enforces this before INSERT.