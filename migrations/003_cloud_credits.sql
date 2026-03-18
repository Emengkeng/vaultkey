-- =============================================================================
-- VaultKey — Migration 003: Cloud Credits & Billing
-- Run AFTER 001_initial.sql and 002_cloud_features.sql
-- Safe to re-run: all statements use IF NOT EXISTS / ON CONFLICT DO NOTHING
-- =============================================================================


-- =============================================================================
-- Organizations: add billing state
-- has_ever_purchased: flips to true on first confirmed Stripe payment.
--   Unlocks: wallet cap removed, rate limit raised to 1000 RPS.
-- credits_expire_months: how long purchased credits last (default 24).
--   Kept as a column so enterprise deals can negotiate different terms.
-- =============================================================================

ALTER TABLE organizations
    ADD COLUMN IF NOT EXISTS has_ever_purchased   BOOLEAN     NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS credits_expire_months INT        NOT NULL DEFAULT 24;


-- =============================================================================
-- Projects: cloud_managed flag
-- Set to true only when a project is created via EnsureProjectForOrg (cloud path).
-- Self-hosted projects created via POST /projects never set this.
-- Controls worker behavior: cloud_managed=true routes through settleAndDeliver.
-- =============================================================================

ALTER TABLE projects
    ADD COLUMN IF NOT EXISTS cloud_managed BOOLEAN NOT NULL DEFAULT false;


-- =============================================================================
-- Signing Jobs: drop operation enum constraint
-- The CHECK constraint fights every new operation type.
-- Switch to plain TEXT with an index. Existing rows unaffected.
-- New constraint: NOT NULL only (application layer validates values).
-- =============================================================================

ALTER TABLE signing_jobs
    DROP CONSTRAINT IF EXISTS signing_jobs_operation_check;

CREATE INDEX IF NOT EXISTS idx_jobs_operation ON signing_jobs(operation);


-- =============================================================================
-- Operation Costs
-- Config table for credit costs per operation. Updatable without deploy.
-- All SDK-facing billable operations must have a row here.
-- cost = 0 means free (balance, broadcast, get wallet, list wallets).
-- gas_* rows seeded but unused until platform-managed relayer is built.
-- =============================================================================

CREATE TABLE IF NOT EXISTS operation_costs (
    operation   TEXT PRIMARY KEY,
    cost        BIGINT NOT NULL DEFAULT 0 CHECK (cost >= 0),
    description TEXT NOT NULL DEFAULT '',
    active      BOOLEAN NOT NULL DEFAULT true,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO operation_costs (operation, cost, description) VALUES
    -- Wallet operations
    ('create_wallet',               10,  'Create a new wallet (EVM or Solana)'),
    ('get_wallet',                   0,  'Fetch wallet by ID — free'),
    ('list_wallets',                 0,  'List wallets for a user — free'),

    -- Signing operations
    ('sign_tx_evm',                 10,  'Sign an EVM transaction'),
    ('sign_msg_evm',                 5,  'Sign an EVM message'),
    ('sign_tx_solana',              10,  'Sign a Solana transaction'),
    ('sign_msg_solana',              5,  'Sign a Solana message'),

    -- Sweep operations
    ('sweep_evm',                   10,  'Sweep EVM wallet to master wallet'),
    ('sweep_solana',                10,  'Sweep Solana wallet to master wallet'),

    -- Stablecoin operations
    ('stablecoin_transfer_evm',     10,  'Transfer stablecoin on EVM chain'),
    ('stablecoin_transfer_solana',  10,  'Transfer stablecoin on Solana'),

    -- Balance and broadcast — always free
    ('get_balance',                  0,  'Get native token balance — free'),
    ('broadcast',                    0,  'Broadcast a signed transaction — free'),
    ('get_job',                      0,  'Poll job status — free'),

    -- Gas costs — reserved for future platform-managed relayer
    -- Seeded now so the column exists when we build that feature.
    -- Nothing reads these yet.
    ('gas_evm_polygon',              2,  'Gas cost for Polygon (chain 137) — reserved'),
    ('gas_evm_base',                 2,  'Gas cost for Base (chain 8453) — reserved'),
    ('gas_evm_arbitrum',             3,  'Gas cost for Arbitrum (chain 42161) — reserved'),
    ('gas_evm_optimism',             3,  'Gas cost for Optimism (chain 10) — reserved'),
    ('gas_solana',                   1,  'Gas cost for Solana — reserved')

ON CONFLICT (operation) DO NOTHING;


-- =============================================================================
-- Org Credit Balance
-- One row per org. Updated atomically alongside credit_ledger inserts.
-- This is the authoritative balance. Never compute balance by summing
-- credit_ledger — always read this table.
--
-- balance is in credits (integer). Never goes below 0 — enforced by
-- application-layer FOR UPDATE check before any debit.
--
-- Row is created by application when org is first created (or on first
-- credit/debit if missing — handled by ON CONFLICT DO UPDATE in DebitCredits).
-- =============================================================================

CREATE TABLE IF NOT EXISTS org_credit_balance (
    org_id      UUID PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
    balance     BIGINT NOT NULL DEFAULT 0 CHECK (balance >= 0),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_credit_balance_org ON org_credit_balance(org_id);


-- =============================================================================
-- Credit Ledger
-- Append-only. Never update or delete rows.
-- Every credit change (purchase, operation debit, refund, free grant, expiry)
-- gets a row here. This is the audit trail.
--
-- amount: positive = credits added, negative = credits consumed
-- balance_after: snapshot of org_credit_balance.balance after this row.
--   Computed inside the same transaction as the balance update so it is
--   always accurate. Useful for debugging and auditing without joins.
--
-- reason values:
--   'purchase'    — Stripe payment confirmed
--   'operation'   — billable SDK operation debited
--   'refund'      — job failed before execution, credits returned
--   'free_grant'  — monthly free tier grant
--   'expiry'      — credits expired (future — not implemented yet)
--
-- ref_id: job_id for operations/refunds, payment_id for purchases,
--         period string ('2026-03') for free_grant, null otherwise.
--
-- expires_at: set for 'purchase' rows only (now + credits_expire_months).
--   Expiry enforcement is deferred — seeded for future use.
-- =============================================================================

CREATE TABLE IF NOT EXISTS credit_ledger (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    amount          BIGINT NOT NULL,  -- positive or negative, never zero
    balance_after   BIGINT NOT NULL CHECK (balance_after >= 0),
    reason          TEXT NOT NULL CHECK (reason IN (
                        'purchase',
                        'operation',
                        'refund',
                        'free_grant',
                        'expiry'
                    )),
    ref_id          TEXT,             -- job_id, payment_id, period, etc.
    metadata        JSONB,            -- operation name, chain, amount, etc.
    expires_at      TIMESTAMPTZ,      -- set for 'purchase' rows only
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT credit_ledger_amount_nonzero CHECK (amount != 0)
);

CREATE INDEX IF NOT EXISTS idx_ledger_org_id     ON credit_ledger(org_id);
CREATE INDEX IF NOT EXISTS idx_ledger_created_at ON credit_ledger(org_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_ledger_reason     ON credit_ledger(org_id, reason);
CREATE INDEX IF NOT EXISTS idx_ledger_ref_id     ON credit_ledger(ref_id) WHERE ref_id IS NOT NULL;


-- =============================================================================
-- Stripe Payments
-- Tracks every Stripe Payment Intent lifecycle.
-- One row per payment attempt. status updated by webhook events.
--
-- stripe_payment_intent_id: the pi_xxx ID from Stripe.
-- credited: whether we have already credited the org for this payment.
--   Used as idempotency guard — webhook can fire multiple times.
-- package_credits: the base credits for this package (before bonus).
-- bonus_credits: the 50% bonus credits.
-- total_credits: package_credits + bonus_credits (what org actually receives).
-- currency: lowercase ISO code ('usd', 'kes', 'ngn', etc.)
-- amount_cents: amount charged in smallest currency unit.
-- =============================================================================

CREATE TABLE IF NOT EXISTS stripe_payments (
    id                          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                      UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    stripe_payment_intent_id    TEXT NOT NULL UNIQUE,
    amount_cents                BIGINT NOT NULL CHECK (amount_cents > 0),
    currency                    TEXT NOT NULL DEFAULT 'usd',
    package_credits             BIGINT NOT NULL CHECK (package_credits > 0),
    bonus_credits               BIGINT NOT NULL DEFAULT 0 CHECK (bonus_credits >= 0),
    total_credits               BIGINT NOT NULL GENERATED ALWAYS AS (package_credits + bonus_credits) STORED,
    status                      TEXT NOT NULL DEFAULT 'pending' CHECK (status IN (
                                    'pending',
                                    'succeeded',
                                    'failed',
                                    'canceled',
                                    'refunded'
                                )),
    credited                    BOOLEAN NOT NULL DEFAULT false,
    stripe_metadata             JSONB,   -- raw Stripe event metadata for debugging
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_stripe_payments_org      ON stripe_payments(org_id);
CREATE INDEX IF NOT EXISTS idx_stripe_payments_intent   ON stripe_payments(stripe_payment_intent_id);
CREATE INDEX IF NOT EXISTS idx_stripe_payments_status   ON stripe_payments(status) WHERE status = 'pending';


-- =============================================================================
-- Free Tier Grants
-- Records each monthly free credit grant per org.
-- Primary key (org_id, period) is the idempotency guard.
-- Cron uses INSERT ... ON CONFLICT DO NOTHING — safe to run multiple times.
-- period format: 'YYYY-MM' (e.g. '2026-03')
-- =============================================================================

CREATE TABLE IF NOT EXISTS free_tier_grants (
    org_id      UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    period      TEXT NOT NULL CHECK (period ~ '^\d{4}-\d{2}$'),
    granted_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, period)
);

CREATE INDEX IF NOT EXISTS idx_free_grants_period ON free_tier_grants(period);


-- =============================================================================
-- Usage Daily Rollup
-- Pre-aggregated usage stats per org per day per operation.
-- Updated by worker after each successful operation via ON CONFLICT DO UPDATE.
-- Queried by GET /cloud/organizations/{org_id}/usage.
--
-- This avoids full credit_ledger scans for usage stats at scale.
-- date is stored as DATE (no time component) in UTC.
-- count: number of times this operation was executed on this date.
-- credits_consumed: total credits debited for this operation on this date.
--   Will be 0 for free operations (balance, broadcast, etc.).
-- =============================================================================

CREATE TABLE IF NOT EXISTS usage_daily_rollup (
    org_id              UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    date                DATE NOT NULL,
    operation           TEXT NOT NULL,
    count               BIGINT NOT NULL DEFAULT 0 CHECK (count >= 0),
    credits_consumed    BIGINT NOT NULL DEFAULT 0 CHECK (credits_consumed >= 0),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, date, operation)
);

CREATE INDEX IF NOT EXISTS idx_usage_rollup_org_date
    ON usage_daily_rollup(org_id, date DESC);

CREATE INDEX IF NOT EXISTS idx_usage_rollup_operation
    ON usage_daily_rollup(org_id, operation, date DESC);


-- =============================================================================
-- Ensure org_credit_balance row exists for all current orgs.
-- New orgs get their row created in application code (CreateOrganization).
-- This backfills existing orgs so the constraint is consistent.
-- =============================================================================

INSERT INTO org_credit_balance (org_id, balance)
SELECT id, 0
FROM organizations
WHERE deleted_at IS NULL
ON CONFLICT (org_id) DO NOTHING;


-- =============================================================================
-- Indexes on organizations for billing queries
-- =============================================================================

CREATE INDEX IF NOT EXISTS idx_orgs_has_ever_purchased
    ON organizations(has_ever_purchased)
    WHERE has_ever_purchased = false AND deleted_at IS NULL;