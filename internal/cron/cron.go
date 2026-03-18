package cron

import (
	"context"
	"database/sql"
	"log"
	"strings"
	"time"

	"github.com/vaultkey/vaultkey/internal/credits"
)

// DefaultFreeTierGrantAmount is the number of credits granted monthly to all orgs.
// Override with FREE_TIER_MONTHLY_CREDITS env var (use lower value on testnet).
const DefaultFreeTierGrantAmount = int64(1000)

// Runner runs background cron jobs.
type Runner struct {
	creditsMgr  *credits.Manager
	db          *sql.DB
	grantAmount int64
}

// New creates a new cron runner.
// db is used only for migration readiness checks — credit operations
// go through creditsMgr.
func New(creditsMgr *credits.Manager, db *sql.DB, grantAmount int64) *Runner {
	return &Runner{
		creditsMgr:  creditsMgr,
		db:          db,
		grantAmount: grantAmount,
	}
}

// Start launches all cron goroutines. Call from main.go.
// Blocks until ctx is cancelled.
func (cr *Runner) Start(ctx context.Context) {
	go cr.runFreeTierGrant(ctx)
	<-ctx.Done()
}

// runFreeTierGrant runs at midnight UTC on the 1st of each month.
// Grants free credits to all orgs that haven't received them yet this month.
// Idempotent: re-runs on the same day are safe (INSERT ON CONFLICT DO NOTHING).
func (cr *Runner) runFreeTierGrant(ctx context.Context) {
	for {
		next := nextMonthlyRun()
		log.Printf("cron: free tier grant scheduled for %s", next.Format(time.RFC3339))

		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
		}

		cr.runFreeTierGrantNow(ctx)
	}
}

// RunFreeTierGrantNow executes the free tier grant immediately.
// Safe to call on startup — checks migration readiness before doing
// anything and silently skips if tables are not yet present.
// Exported for use in main.go startup and tests.
func (cr *Runner) RunFreeTierGrantNow(ctx context.Context) {
	if !cr.tablesReady(ctx) {
		log.Println("cron: skipping free tier grant — credit tables not yet migrated")
		return
	}
	cr.runFreeTierGrantNow(ctx)
}

func (cr *Runner) runFreeTierGrantNow(ctx context.Context) {
	period := time.Now().UTC().Format("2006-01")
	log.Printf("cron: starting free tier grant for period %s", period)

	orgIDs, err := cr.creditsMgr.ListOrgsForFreeGrant(ctx, period)
	if err != nil {
		if isMissingTableError(err) {
			log.Printf("cron: skipping free tier grant — migration 003 not applied yet: %v", err)
			return
		}
		log.Printf("cron: list orgs for free grant failed: %v", err)
		return
	}

	if len(orgIDs) == 0 {
		log.Printf("cron: no orgs need free grant for period %s", period)
		return
	}

	log.Printf("cron: granting %d credits to %d orgs for period %s",
		cr.grantAmount, len(orgIDs), period)

	succeeded, failed := 0, 0
	for _, orgID := range orgIDs {
		select {
		case <-ctx.Done():
			log.Printf("cron: free tier grant interrupted after %d/%d orgs",
				succeeded, len(orgIDs))
			return
		default:
		}

		if err := cr.grantToOrg(ctx, orgID, period); err != nil {
			log.Printf("cron: free tier grant failed for org %s: %v", orgID, err)
			failed++
			continue
		}
		succeeded++
	}

	log.Printf("cron: free tier grant complete — succeeded=%d failed=%d period=%s",
		succeeded, failed, period)
}

func (cr *Runner) grantToOrg(ctx context.Context, orgID, period string) error {
	already, err := cr.creditsMgr.HasReceivedFreeGrantThisMonth(ctx, orgID, period)
	if err != nil {
		return err
	}
	if already {
		return nil
	}

	if err := cr.creditsMgr.MarkFreeGrantGiven(ctx, orgID, period); err != nil {
		return err
	}

	return cr.creditsMgr.Credit(ctx, credits.CreditParams{
		OrgID:  orgID,
		Amount: cr.grantAmount,
		Reason: "free_grant",
		RefID:  period,
		Metadata: map[string]any{
			"period":       period,
			"grant_amount": cr.grantAmount,
		},
	})
}

// tablesReady checks whether migration 003 has been applied by probing
// the three tables it creates. Returns false if any are missing.
func (cr *Runner) tablesReady(ctx context.Context) bool {
	tables := []string{
		"free_tier_grants",
		"org_credit_balance",
		"credit_ledger",
	}
	for _, table := range tables {
		var exists bool
		err := cr.db.QueryRowContext(ctx,
			`SELECT EXISTS (
			     SELECT 1 FROM information_schema.tables
			     WHERE table_schema = 'public'
			       AND table_name   = $1
			 )`,
			table,
		).Scan(&exists)
		if err != nil || !exists {
			return false
		}
	}
	return true
}

// isMissingTableError returns true if the error is a Postgres
// "relation does not exist" error (SQLSTATE 42P01).
func isMissingTableError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "42P01") ||
		strings.Contains(s, "does not exist")
}

// nextMonthlyRun returns the next midnight UTC on the 1st of the month.
func nextMonthlyRun() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
}