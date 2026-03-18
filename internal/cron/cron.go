package cron

import (
	"context"
	"log"
	"time"

	"github.com/vaultkey/vaultkey/internal/credits"
)

// FreeTierGrantAmount is the number of credits granted monthly to all orgs.
// Override with FREE_TIER_MONTHLY_CREDITS env var (use lower value on testnet).
const DefaultFreeTierGrantAmount = int64(1000)

// Runner runs background cron jobs.
type Runner struct {
	creditsMgr  *credits.Manager
	grantAmount int64
}

func New(creditsMgr *credits.Manager, grantAmount int64) *Runner {
	return &Runner{
		creditsMgr:  creditsMgr,
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

// runFreeTierGrantNow executes the free tier grant immediately.
// Exported so it can be called on startup to backfill any missed months,
// and from tests.
func (cr *Runner) RunFreeTierGrantNow(ctx context.Context) {
	cr.runFreeTierGrantNow(ctx)
}

func (cr *Runner) runFreeTierGrantNow(ctx context.Context) {
	period := time.Now().UTC().Format("2006-01")
	log.Printf("cron: starting free tier grant for period %s", period)

	orgIDs, err := cr.creditsMgr.ListOrgsForFreeGrant(ctx, period)
	if err != nil {
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
			log.Printf("cron: free tier grant interrupted after %d/%d orgs", succeeded, len(orgIDs))
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
	// MarkFreeGrantGiven uses INSERT ... ON CONFLICT DO NOTHING.
	// If the row already exists (concurrent run or restart), this is a no-op.
	// We check first to avoid crediting before marking, which would leave
	// an orphaned credit if MarkFreeGrantGiven then fails.
	already, err := cr.creditsMgr.HasReceivedFreeGrantThisMonth(ctx, orgID, period)
	if err != nil {
		return err
	}
	if already {
		return nil
	}

	// Mark first, then credit.
	// If credit fails after mark: org misses this month's grant.
	// This is safer than the reverse (double credit risk).
	// A monitoring alert on cron failures catches this edge case.
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

// nextMonthlyRun returns the next midnight UTC on the 1st of the month.
func nextMonthlyRun() time.Time {
	now := time.Now().UTC()
	// First of next month at 00:00:00 UTC
	next := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	return next
}