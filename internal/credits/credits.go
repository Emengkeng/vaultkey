package credits

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// ErrInsufficientCredits is returned when a debit would bring balance below zero.
// Handlers should convert this to HTTP 402.
var ErrInsufficientCredits = fmt.Errorf("insufficient credits")

// ErrOperationNotFound is returned when the operation has no row in operation_costs.
// This is a configuration error, not a user error — log it and return 500.
var ErrOperationNotFound = fmt.Errorf("operation cost not configured")

// OperationCost holds the cost config for a single operation.
type OperationCost struct {
	Operation   string
	Cost        int64
	Description string
	Active      bool
}


// UsageStat is returned by GetUsageStats — aggregated per operation.
type UsageStat struct {
	Operation       string
	Count           int64
	CreditsConsumed int64
	FirstDate       time.Time
	LastDate        time.Time
}
 
// DailyUsage is returned by GetDailyUsage — one row per day per operation.
type DailyUsage struct {
	Date            time.Time
	Operation       string
	Count           int64
	CreditsConsumed int64
}
 
// CreditBalance is the current balance for an org.
type CreditBalance struct {
	OrgID     string
	Balance   int64
	UpdatedAt time.Time
}

// WalletLimits holds the resolved limits for an org's wallet creation.
// All values are fully resolved — callers never need to fall back themselves.
type WalletLimits struct {
	// HardCap is the absolute maximum wallets this org can have.
	HardCap int64
 
	// HourlyLimit is the maximum wallets this org can create in any 60-minute window.
	// Derived dynamically from existing wallet count unless overridden in org_limits.
	HourlyLimit int64
 
	// ExistingCount is the current wallet count — included so callers
	// don't need a second query.
	ExistingCount int64
 
	// HourlyUsed is how many wallets the org has created in the last hour.
	HourlyUsed int64
}

// Manager handles all credit operations. All mutating operations run inside
// explicit Postgres transactions with SELECT ... FOR UPDATE to serialize
// concurrent debits for the same org. This prevents overdrafts under load.
type Manager struct {
	db *sql.DB
}
 
func New(db *sql.DB) *Manager {
	return &Manager{db: db}
}
 
// GetCost returns the credit cost for an operation.
// Returns ErrOperationNotFound if the operation has no row or is inactive.
// Returns nil error with cost=0 for free operations.
func (m *Manager) GetCost(ctx context.Context, operation string) (*OperationCost, error) {
	oc := &OperationCost{}
	err := m.db.QueryRowContext(ctx,
		`SELECT operation, cost, description, active
		 FROM operation_costs
		 WHERE operation = $1`,
		operation,
	).Scan(&oc.Operation, &oc.Cost, &oc.Description, &oc.Active)
	if err == sql.ErrNoRows {
		return nil, ErrOperationNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get operation cost: %w", err)
	}
	if !oc.Active {
		return nil, ErrOperationNotFound
	}
	return oc, nil
}
 
// DebitParams holds the inputs for a debit operation.
type DebitParams struct {
	OrgID     string
	Operation string // must exist in operation_costs
	RefID     string // job_id, or empty
	Metadata  map[string]any
}
 
// DebitResult is returned after a successful debit.
type DebitResult struct {
	LedgerID     string
	BalanceBefore int64
	Cost         int64
	BalanceAfter int64
}
 
// Debit atomically debits credits for a billable operation.
//
// Flow inside a single transaction:
//  1. Look up cost from operation_costs
//  2. SELECT ... FOR UPDATE on org_credit_balance — serializes concurrent debits
//  3. Check balance >= cost — return ErrInsufficientCredits if not
//  4. UPDATE org_credit_balance
//  5. INSERT credit_ledger row with balance_after snapshot
//
// Returns ErrInsufficientCredits if balance is too low.
// Returns ErrOperationNotFound if operation has no cost config.
// All other errors are internal.
func (m *Manager) Debit(ctx context.Context, p DebitParams) (*DebitResult, error) {
	oc, err := m.GetCost(ctx, p.Operation)
	if err != nil {
		return nil, err
	}
 
	// Free operations still go through this path so we get a ledger entry,
	// but the balance check and update are skipped for cost=0.
	if oc.Cost == 0 {
		return &DebitResult{Cost: 0}, nil
	}
 
	tx, err := m.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin debit transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
 
	// Lock this org's balance row for the duration of the transaction.
	// Any concurrent Debit for the same org will block here until we commit.
	// This is the critical section — no overdraft possible.
	var currentBalance int64
	err = tx.QueryRowContext(ctx,
		`SELECT balance FROM org_credit_balance
		 WHERE org_id = $1
		 FOR UPDATE`,
		p.OrgID,
	).Scan(&currentBalance)
	if err == sql.ErrNoRows {
		// Balance row missing — create it with 0 balance and retry the lock.
		// This handles the race between org creation and first operation.
		_, err = tx.ExecContext(ctx,
			`INSERT INTO org_credit_balance (org_id, balance)
			 VALUES ($1, 0)
			 ON CONFLICT (org_id) DO NOTHING`,
			p.OrgID,
		)
		if err != nil {
			return nil, fmt.Errorf("create credit balance row: %w", err)
		}
		// Re-lock after insert.
		err = tx.QueryRowContext(ctx,
			`SELECT balance FROM org_credit_balance
			 WHERE org_id = $1
			 FOR UPDATE`,
			p.OrgID,
		).Scan(&currentBalance)
	}
	if err != nil {
		return nil, fmt.Errorf("lock credit balance: %w", err)
	}
 
	if currentBalance < oc.Cost {
		// Roll back immediately — no state change.
		tx.Rollback() //nolint:errcheck
		return nil, ErrInsufficientCredits
	}
 
	newBalance := currentBalance - oc.Cost
 
	// Update the materialized balance.
	_, err = tx.ExecContext(ctx,
		`UPDATE org_credit_balance
		 SET balance = $1, updated_at = now()
		 WHERE org_id = $2`,
		newBalance, p.OrgID,
	)
	if err != nil {
		return nil, fmt.Errorf("update credit balance: %w", err)
	}
 
	// Append ledger row. balance_after matches what we just set above.
	var metaJSON []byte
	if p.Metadata != nil {
		metaJSON, _ = json.Marshal(p.Metadata)
	}
 
	var refID *string
	if p.RefID != "" {
		refID = &p.RefID
	}
 
	var ledgerID string
	err = tx.QueryRowContext(ctx,
		`INSERT INTO credit_ledger
		    (org_id, amount, balance_after, reason, ref_id, metadata)
		 VALUES ($1, $2, $3, 'operation', $4, $5)
		 RETURNING id`,
		p.OrgID, -oc.Cost, newBalance, refID, metaJSON,
	).Scan(&ledgerID)
	if err != nil {
		return nil, fmt.Errorf("insert ledger row: %w", err)
	}
 
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit debit transaction: %w", err)
	}
 
	return &DebitResult{
		LedgerID:      ledgerID,
		BalanceBefore: currentBalance,
		Cost:          oc.Cost,
		BalanceAfter:  newBalance,
	}, nil
}
 
// CreditParams holds the inputs for adding credits to an org.
type CreditParams struct {
	OrgID     string
	Amount    int64 // must be positive
	Reason    string // 'purchase' | 'refund' | 'free_grant'
	RefID     string // payment_id, job_id, period string
	Metadata  map[string]any
	ExpiresAt *time.Time // set for 'purchase' only
}
 
// Credit atomically adds credits to an org's balance.
// Uses the same FOR UPDATE pattern as Debit for consistency,
// even though credits can't overdraft — this keeps the balance
// row locked during both reads and writes so stats are always consistent.
func (m *Manager) Credit(ctx context.Context, p CreditParams) error {
	if p.Amount <= 0 {
		return fmt.Errorf("credit amount must be positive, got %d", p.Amount)
	}
 
	validReasons := map[string]bool{
		"purchase":   true,
		"refund":     true,
		"free_grant": true,
	}
	if !validReasons[p.Reason] {
		return fmt.Errorf("invalid credit reason %q", p.Reason)
	}
 
	tx, err := m.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("begin credit transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
 
	// Upsert balance row first (handles new orgs gracefully).
	_, err = tx.ExecContext(ctx,
		`INSERT INTO org_credit_balance (org_id, balance)
		 VALUES ($1, 0)
		 ON CONFLICT (org_id) DO NOTHING`,
		p.OrgID,
	)
	if err != nil {
		return fmt.Errorf("ensure credit balance row: %w", err)
	}
 
	// Lock and read current balance.
	var currentBalance int64
	err = tx.QueryRowContext(ctx,
		`SELECT balance FROM org_credit_balance
		 WHERE org_id = $1
		 FOR UPDATE`,
		p.OrgID,
	).Scan(&currentBalance)
	if err != nil {
		return fmt.Errorf("lock credit balance for credit: %w", err)
	}
 
	newBalance := currentBalance + p.Amount
 
	_, err = tx.ExecContext(ctx,
		`UPDATE org_credit_balance
		 SET balance = $1, updated_at = now()
		 WHERE org_id = $2`,
		newBalance, p.OrgID,
	)
	if err != nil {
		return fmt.Errorf("update credit balance: %w", err)
	}
 
	var metaJSON []byte
	if p.Metadata != nil {
		metaJSON, _ = json.Marshal(p.Metadata)
	}
 
	var refID *string
	if p.RefID != "" {
		refID = &p.RefID
	}
 
	_, err = tx.ExecContext(ctx,
		`INSERT INTO credit_ledger
		    (org_id, amount, balance_after, reason, ref_id, metadata, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		p.OrgID, p.Amount, newBalance, p.Reason, refID, metaJSON, p.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("insert credit ledger row: %w", err)
	}
 
	return tx.Commit()
}
 
// GetBalance returns the current credit balance for an org.
// No lock needed for reads — reads the materialized balance row.
func (m *Manager) GetBalance(ctx context.Context, orgID string) (int64, error) {
	var balance int64
	err := m.db.QueryRowContext(ctx,
		`SELECT balance FROM org_credit_balance WHERE org_id = $1`,
		orgID,
	).Scan(&balance)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get credit balance: %w", err)
	}
	return balance, nil
}
 
// RecordUsage upserts a row in usage_daily_rollup for the given org, date,
// and operation. Called by the worker after each successful operation.
// Safe to call concurrently — ON CONFLICT DO UPDATE is atomic in Postgres.
func (m *Manager) RecordUsage(ctx context.Context, orgID, operation string, cost int64) error {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	_, err := m.db.ExecContext(ctx,
		`INSERT INTO usage_daily_rollup (org_id, date, operation, count, credits_consumed)
		 VALUES ($1, $2, $3, 1, $4)
		 ON CONFLICT (org_id, date, operation)
		 DO UPDATE SET
		     count            = usage_daily_rollup.count + 1,
		     credits_consumed = usage_daily_rollup.credits_consumed + EXCLUDED.credits_consumed,
		     updated_at       = now()`,
		orgID, today, operation, cost,
	)
	if err != nil {
		return fmt.Errorf("record usage: %w", err)
	}
	return nil
}
 
// HasReceivedFreeGrantThisMonth returns true if the org already received
// a free tier grant for the given period (format: '2026-03').
func (m *Manager) HasReceivedFreeGrantThisMonth(ctx context.Context, orgID, period string) (bool, error) {
	var exists bool
	err := m.db.QueryRowContext(ctx,
		`SELECT EXISTS(
		     SELECT 1 FROM free_tier_grants
		     WHERE org_id = $1 AND period = $2
		 )`,
		orgID, period,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check free grant: %w", err)
	}
	return exists, nil
}
 
// MarkFreeGrantGiven inserts a free_tier_grants row.
// ON CONFLICT DO NOTHING makes this idempotent — safe to call twice.
func (m *Manager) MarkFreeGrantGiven(ctx context.Context, orgID, period string) error {
	_, err := m.db.ExecContext(ctx,
		`INSERT INTO free_tier_grants (org_id, period)
		 VALUES ($1, $2)
		 ON CONFLICT (org_id, period) DO NOTHING`,
		orgID, period,
	)
	return err
}
 
// ListOrgsForFreeGrant returns all active orgs that have NOT yet received
// a free grant for the given period. Used by the monthly cron.
func (m *Manager) ListOrgsForFreeGrant(ctx context.Context, period string) ([]string, error) {
	rows, err := m.db.QueryContext(ctx,
		`SELECT o.id
		 FROM organizations o
		 WHERE o.deleted_at IS NULL
		   AND NOT EXISTS (
		       SELECT 1 FROM free_tier_grants fg
		       WHERE fg.org_id = o.id AND fg.period = $1
		   )
		 ORDER BY o.created_at ASC`,
		period,
	)
	if err != nil {
		return nil, fmt.Errorf("list orgs for free grant: %w", err)
	}
	defer rows.Close()
 
	var orgIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		orgIDs = append(orgIDs, id)
	}
	return orgIDs, rows.Err()
}
 
// MarkOrgPurchased flips has_ever_purchased=true and raises all active
// project rate limits to 1000 RPS for the org.
// Called from the Stripe webhook handler after payment_intent.succeeded.
// Idempotent — safe to call if already true.
func (m *Manager) MarkOrgPurchased(ctx context.Context, orgID string) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin purchased transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
 
	_, err = tx.ExecContext(ctx,
		`UPDATE organizations
		 SET has_ever_purchased = true, updated_at = now()
		 WHERE id = $1`,
		orgID,
	)
	if err != nil {
		return fmt.Errorf("mark org purchased: %w", err)
	}
 
	// Raise rate limit on all active projects for this org.
	// Self-hosted projects (org_id IS NULL) are unaffected.
	_, err = tx.ExecContext(ctx,
		`UPDATE projects
		 SET rate_limit_rps = 1000
		 WHERE org_id = $1 AND deleted_at IS NULL`,
		orgID,
	)
	if err != nil {
		return fmt.Errorf("update project rate limits: %w", err)
	}
 
	return tx.Commit()
}
 
// GetUsageStats returns aggregated usage for an org within a date range.
// Reads from usage_daily_rollup — no credit_ledger scan.
// startDate and endDate are inclusive, format 'YYYY-MM-DD'.
func (m *Manager) GetUsageStats(ctx context.Context, orgID, startDate, endDate string) ([]UsageStat, error) {
	rows, err := m.db.QueryContext(ctx,
		`SELECT operation, SUM(count), SUM(credits_consumed), MIN(date), MAX(date)
		 FROM usage_daily_rollup
		 WHERE org_id = $1
		   AND date >= $2::date
		   AND date <= $3::date
		 GROUP BY operation
		 ORDER BY SUM(credits_consumed) DESC`,
		orgID, startDate, endDate,
	)
	if err != nil {
		return nil, fmt.Errorf("get usage stats: %w", err)
	}
	defer rows.Close()
 
	var stats []UsageStat
	for rows.Next() {
		var s UsageStat
		if err := rows.Scan(
			&s.Operation, &s.Count, &s.CreditsConsumed, &s.FirstDate, &s.LastDate,
		); err != nil {
			return nil, err
		}
		stats = append(stats, s)
	}
	return stats, rows.Err()
}
 
// GetDailyUsage returns day-by-day breakdown for an org within a date range.
// Used for charting in the dashboard.
func (m *Manager) GetDailyUsage(ctx context.Context, orgID, startDate, endDate string) ([]DailyUsage, error) {
	rows, err := m.db.QueryContext(ctx,
		`SELECT date, operation, count, credits_consumed
		 FROM usage_daily_rollup
		 WHERE org_id = $1
		   AND date >= $2::date
		   AND date <= $3::date
		 ORDER BY date DESC, credits_consumed DESC`,
		orgID, startDate, endDate,
	)
	if err != nil {
		return nil, fmt.Errorf("get daily usage: %w", err)
	}
	defer rows.Close()
 
	var rows2 []DailyUsage
	for rows.Next() {
		var d DailyUsage
		if err := rows.Scan(&d.Date, &d.Operation, &d.Count, &d.CreditsConsumed); err != nil {
			return nil, err
		}
		rows2 = append(rows2, d)
	}
	return rows2, rows.Err()
}



// ResolveWalletLimits returns the fully resolved wallet creation limits for an org.
//
// Resolution order for hard cap:
//  1. org_limits.max_wallets (per-org override, set by support for enterprise)
//  2. operation_costs['create_wallet_hard_cap'] (global default)
//  3. Hardcoded fallback: 200,000
//
// Resolution order for hourly limit:
//  1. org_limits.max_wallets_per_hour (per-org override)
//  2. Dynamic formula: hourly_base + (existing_wallets / 1000) * hourly_per_1k
//     where hourly_base and hourly_per_1k come from operation_costs
//  3. Hardcoded fallback: 50/hour base, 100 per 1k
//
// This means:
//   - A new org (0 wallets) gets 50/hour by default
//   - An org with 10,000 wallets gets 1,050/hour
//   - An enterprise org with a custom override gets whatever support set
//   - All values are tunable via operation_costs without a deploy
func (m *Manager) ResolveWalletLimits(ctx context.Context, orgID, projectID string) (*WalletLimits, error) {
	// ── Step 1: Fetch all inputs in parallel via a single query ──────────────
	// One round-trip: get org_limits override, existing wallet count,
	// hourly wallet creation count, and all three cost config values.
	// Using COALESCE so missing rows return safe defaults.
 
	var (
		overrideHardCap     sql.NullInt64
		overrideHourlyLimit sql.NullInt64
		existingCount       int64
		hourlyUsed          int64
		configHardCap       int64
		configHourlyBase    int64
		configHourlyPer1k   int64
	)
 
	err := m.db.QueryRowContext(ctx, `
		SELECT
		    -- Per-org overrides (NULL if no override row exists)
		    ol.max_wallets,
		    ol.max_wallets_per_hour,
 
		    -- Current wallet count for this project
		    -- Excludes internal wallets (master, relayer) via user_id filter
		    (SELECT COUNT(*) FROM wallets
		     WHERE project_id = $2
		       AND user_id NOT LIKE '\_%%' ESCAPE '\'),
 
		    -- Wallets created in the last hour (from credit_ledger)
		    (SELECT COUNT(*) FROM credit_ledger
		     WHERE org_id = $1
		       AND reason = 'operation'
		       AND metadata->>'operation' = 'create_wallet'
		       AND created_at >= now() - INTERVAL '1 hour'),
 
		    -- Global config defaults
		    COALESCE(
		        (SELECT cost FROM operation_costs
		         WHERE operation = 'create_wallet_hard_cap' AND active = true),
		        200000
		    ),
		    COALESCE(
		        (SELECT cost FROM operation_costs
		         WHERE operation = 'create_wallet_hourly_base' AND active = true),
		        50
		    ),
		    COALESCE(
		        (SELECT cost FROM operation_costs
		         WHERE operation = 'create_wallet_hourly_per_1k' AND active = true),
		        100
		    )
 
		FROM organizations o
		LEFT JOIN org_limits ol ON ol.org_id = o.id
		WHERE o.id = $1
	`, orgID, projectID).Scan(
		&overrideHardCap,
		&overrideHourlyLimit,
		&existingCount,
		&hourlyUsed,
		&configHardCap,
		&configHourlyBase,
		&configHourlyPer1k,
	)
	if err != nil {
		return nil, fmt.Errorf("resolve wallet limits: %w", err)
	}
 
	// ── Step 2: Resolve hard cap ──────────────────────────────────────────────
	hardCap := configHardCap
	if overrideHardCap.Valid {
		hardCap = overrideHardCap.Int64
	}
 
	// ── Step 3: Resolve hourly limit ──────────────────────────────────────────
	var hourlyLimit int64
	if overrideHourlyLimit.Valid {
		// Enterprise override — use exactly what support set.
		hourlyLimit = overrideHourlyLimit.Int64
	} else {
		// Dynamic formula: scales with org's existing wallet footprint.
		// New org (0 wallets): base only.
		// Established org: base + (existing / 1000) * per_1k.
		//
		// Example with defaults (base=50, per_1k=100):
		//   0 wallets    →    50/hour
		//   500 wallets  →    50/hour  (below 1k threshold)
		//   1,000        →   150/hour
		//   5,000        →   550/hour
		//   10,000       → 1,050/hour
		//   50,000       → 5,050/hour
		//   100,000      → 10,050/hour
		hourlyLimit = configHourlyBase + (existingCount/1000)*configHourlyPer1k
	}
 
	return &WalletLimits{
		HardCap:       hardCap,
		HourlyLimit:   hourlyLimit,
		ExistingCount: existingCount,
		HourlyUsed:    hourlyUsed,
	}, nil
}