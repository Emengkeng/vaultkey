package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ── Stripe Payments ───────────────────────────────────────────────────────────

type StripePayment struct {
	ID                    string
	OrgID                 string
	StripePaymentIntentID string
	AmountCents           int64
	Currency              string
	PackageCredits        int64
	BonusCredits          int64
	TotalCredits          int64 // generated column, read-only
	Status                string
	Credited              bool
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

func (s *Store) CreateStripePayment(ctx context.Context, p *StripePayment) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO stripe_payments
		    (org_id, stripe_payment_intent_id, amount_cents, currency,
		     package_credits, bonus_credits, status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (stripe_payment_intent_id) DO NOTHING`,
		p.OrgID, p.StripePaymentIntentID, p.AmountCents, p.Currency,
		p.PackageCredits, p.BonusCredits, p.Status,
	)
	if err != nil {
		return fmt.Errorf("create stripe payment: %w", err)
	}
	return nil
}

// IsPaymentCredited returns true if this PaymentIntent has already been
// credited to the org. Used as the idempotency guard in the webhook handler.
func (s *Store) IsPaymentCredited(ctx context.Context, stripePaymentIntentID string) (bool, error) {
	var credited bool
	err := s.db.QueryRowContext(ctx,
		`SELECT credited FROM stripe_payments
		 WHERE stripe_payment_intent_id = $1`,
		stripePaymentIntentID,
	).Scan(&credited)
	if err == sql.ErrNoRows {
		// No row means the PI wasn't recorded at creation time (rare edge case
		// where CreatePaymentIntent DB insert failed but Stripe succeeded).
		// Treat as not-yet-credited — proceed with crediting.
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check payment credited: %w", err)
	}
	return credited, nil
}

// MarkPaymentCredited updates the stripe_payments row to credited=true and
// status='succeeded'. Called after credits are successfully added.
func (s *Store) MarkPaymentCredited(
	ctx context.Context,
	stripePaymentIntentID string,
	amountCents int64,
	currency string,
	packageCredits, bonusCredits int64,
) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE stripe_payments
		 SET credited      = true,
		     status        = 'succeeded',
		     amount_cents  = $2,
		     currency      = $3,
		     package_credits = $4,
		     bonus_credits   = $5,
		     updated_at    = now()
		 WHERE stripe_payment_intent_id = $1`,
		stripePaymentIntentID, amountCents, currency, packageCredits, bonusCredits,
	)
	if err != nil {
		return fmt.Errorf("mark payment credited: %w", err)
	}

	n, _ := result.RowsAffected()
	if n == 0 {
		// Row doesn't exist — PI wasn't recorded at creation.
		// Insert it now so we have an audit trail.
		_, err = s.db.ExecContext(ctx,
			`INSERT INTO stripe_payments
			    (stripe_payment_intent_id, org_id, amount_cents, currency,
			     package_credits, bonus_credits, status, credited)
			 VALUES ($1, '', $2, $3, $4, $5, 'succeeded', true)
			 ON CONFLICT (stripe_payment_intent_id) DO UPDATE
			   SET credited = true, status = 'succeeded', updated_at = now()`,
			stripePaymentIntentID, amountCents, currency, packageCredits, bonusCredits,
		)
		if err != nil {
			return fmt.Errorf("insert missing stripe payment: %w", err)
		}
	}

	return nil
}

// UpdateStripePaymentStatus updates the status of a payment.
// Used for payment_intent.payment_failed and payment_intent.canceled events.
func (s *Store) UpdateStripePaymentStatus(ctx context.Context, stripePaymentIntentID, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE stripe_payments
		 SET status = $2, updated_at = now()
		 WHERE stripe_payment_intent_id = $1
		   AND credited = false`,  // never update a successfully credited payment's status
		stripePaymentIntentID, status,
	)
	if err != nil {
		return fmt.Errorf("update payment status: %w", err)
	}
	return nil
}

// ListPayments returns recent payments for an org. Used for billing history UI.
func (s *Store) ListPayments(ctx context.Context, orgID string, limit int) ([]*StripePayment, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, org_id, stripe_payment_intent_id, amount_cents, currency,
		        package_credits, bonus_credits, total_credits,
		        status, credited, created_at, updated_at
		 FROM stripe_payments
		 WHERE org_id = $1
		 ORDER BY created_at DESC
		 LIMIT $2`,
		orgID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list payments: %w", err)
	}
	defer rows.Close()

	var payments []*StripePayment
	for rows.Next() {
		p := &StripePayment{}
		if err := rows.Scan(
			&p.ID, &p.OrgID, &p.StripePaymentIntentID, &p.AmountCents, &p.Currency,
			&p.PackageCredits, &p.BonusCredits, &p.TotalCredits,
			&p.Status, &p.Credited, &p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, err
		}
		payments = append(payments, p)
	}
	return payments, rows.Err()
}

// ── Wallet count for free tier cap ───────────────────────────────────────────

// CountWalletsForOrg returns the number of active wallets for a project.
// Used to enforce the free tier 50-wallet cap.
// Excludes master wallets and relayer wallets (user_id starts with '_').
func (s *Store) CountWalletsForOrg(ctx context.Context, projectID string) (int64, error) {
	var count int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*)
		 FROM wallets
		 WHERE project_id = $1
		   AND user_id NOT LIKE '\_%'`,
		projectID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count wallets for org: %w", err)
	}
	return count, nil
}

func (s *Store) GetBillingHistoryForHandler(ctx context.Context, orgID string) ([]*StripePayment, error) {
	return s.ListPayments(ctx, orgID, 20)
}
