package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/paymentintent"
	"github.com/stripe/stripe-go/v76/webhook"
	"github.com/vaultkey/vaultkey/config"
	"github.com/vaultkey/vaultkey/internal/api/middleware"
	"github.com/vaultkey/vaultkey/internal/credits"
	"github.com/vaultkey/vaultkey/internal/storage"
)

// peekBody reads r.Body, decodes into v, and returns a replacement ReadCloser
// with the same bytes so the downstream handler can read the body again.
// Returns (nil, false) if the body cannot be read or decoded.
func peekBody(r *http.Request, v any) (io.ReadCloser, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, false
	}
	if err := json.Unmarshal(body, v); err != nil {
		return nil, false
	}
	return io.NopCloser(bytes.NewReader(body)), true
}

// ── Payment Handler ───────────────────────────────────────────────────────────

// PaymentHandler handles Stripe webhooks and the purchase initiation endpoint.
type PaymentHandler struct {
	store          *storage.Store
	creditsMgr     *credits.Manager
	cfg            *config.Config
}

func NewPaymentHandler(store *storage.Store, creditsMgr *credits.Manager, cfg *config.Config) *PaymentHandler {
	return &PaymentHandler{store: store, creditsMgr: creditsMgr, cfg: cfg}
}

// creditPackages maps purchase amount in cents to (base, bonus) credits.
// $1 = 1000 base credits + 50% bonus.
// All packages give 50% bonus. Custom amounts >= $10 also qualify.
var creditPackages = map[int64][2]int64{
	1000:  {10_000, 5_000},   // $10
	5000:  {50_000, 25_000},  // $50
	10000: {100_000, 50_000}, // $100
	50000: {500_000, 250_000}, // $500
}

// creditsForAmount returns (base, bonus) for any amount >= $10.
// Standard packages get exact amounts. Custom amounts use the formula.
func creditsForAmount(amountCents int64) (base, bonus int64) {
	if pkg, ok := creditPackages[amountCents]; ok {
		return pkg[0], pkg[1]
	}
	// Custom amount: $1 = 1000 credits, 50% bonus
	base = (amountCents / 100) * 1000
	bonus = base / 2
	return base, bonus
}

// CreatePaymentIntent handles POST /cloud/billing/purchase
// Creates a Stripe PaymentIntent and returns the client_secret for
// the frontend to complete payment with Stripe.js.
//
// Request body:
//
//	{ "amount_cents": 1000, "currency": "usd" }
//
// Response:
//
//	{ "client_secret": "pi_xxx_secret_xxx", "payment_intent_id": "pi_xxx",
//	  "base_credits": 10000, "bonus_credits": 5000, "total_credits": 15000 }
func (h *PaymentHandler) CreatePaymentIntent(w http.ResponseWriter, r *http.Request) {
	member := middleware.OrgMemberFromContext(r.Context())

	var req struct {
		AmountCents int64  `json:"amount_cents"`
		Currency    string `json:"currency"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Minimum purchase: $10 (1000 cents)
	if req.AmountCents < 1000 {
		writeError(w, http.StatusBadRequest,
			"minimum purchase is $10 (1000 cents)")
		return
	}

	currency := strings.ToLower(req.Currency)
	if currency == "" {
		currency = "usd"
	}

	base, bonus := creditsForAmount(req.AmountCents)
	total := base + bonus

	// Create Stripe PaymentIntent
	stripe.Key = h.cfg.Cloud.Stripe.SecretKey

	params := &stripe.PaymentIntentParams{
		Amount:   stripe.Int64(req.AmountCents),
		Currency: stripe.String(currency),
		Metadata: map[string]string{
			"org_id":        member.OrgID,
			"base_credits":  formatInt64(base),
			"bonus_credits": formatInt64(bonus),
			"total_credits": formatInt64(total),
			"environment":   h.cfg.Environment,
		},
	}

	pi, err := paymentintent.New(params)
	if err != nil {
		log.Printf("stripe: create payment intent failed for org %s: %v", member.OrgID, err)
		writeError(w, http.StatusInternalServerError, "failed to create payment intent")
		return
	}

	// Record payment in DB immediately with 'pending' status.
	// Webhook will update this to 'succeeded' or 'failed'.
	if err := h.store.CreateStripePayment(r.Context(), &storage.StripePayment{
		OrgID:                   member.OrgID,
		StripePaymentIntentID:   pi.ID,
		AmountCents:             req.AmountCents,
		Currency:                currency,
		PackageCredits:          base,
		BonusCredits:            bonus,
		Status:                  "pending",
	}); err != nil {
		log.Printf("stripe: record payment failed for org %s pi %s: %v", member.OrgID, pi.ID, err)
		// Don't fail the request — Stripe has the PI, webhook will catch it.
		// But log loudly so we can reconcile manually if needed.
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"client_secret":     pi.ClientSecret,
		"payment_intent_id": pi.ID,
		"base_credits":      base,
		"bonus_credits":     bonus,
		"total_credits":     total,
		"amount_cents":      req.AmountCents,
		"currency":          currency,
	})
}

// StripeWebhook handles POST /webhooks/stripe
// Verifies Stripe signature, processes payment_intent.succeeded and
// payment_intent.payment_failed events.
//
// This endpoint is public — no API key auth. Security comes from
// Stripe webhook signature verification (Stripe-Signature header).
func (h *PaymentHandler) StripeWebhook(w http.ResponseWriter, r *http.Request) {
	// Read body — Stripe needs full bytes for signature verification.
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	// Verify Stripe signature. This prevents replay attacks and fake events.
	sig := r.Header.Get("Stripe-Signature")
	event, err := webhook.ConstructEvent(body, sig, h.cfg.Cloud.Stripe.WebhookSecret)
	if err != nil {
		log.Printf("stripe webhook: signature verification failed: %v", err)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	switch event.Type {
	case "payment_intent.succeeded":
		h.handlePaymentSucceeded(w, r, event)
	case "payment_intent.payment_failed":
		h.handlePaymentFailed(w, r, event)
	case "payment_intent.canceled":
		h.handlePaymentCanceled(w, r, event)
	default:
		// Acknowledge unknown events — don't let Stripe retry indefinitely.
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *PaymentHandler) handlePaymentSucceeded(w http.ResponseWriter, r *http.Request, event stripe.Event) {
	var pi stripe.PaymentIntent
	if err := json.Unmarshal(event.Data.Raw, &pi); err != nil {
		log.Printf("stripe webhook: unmarshal payment_intent failed: %v", err)
		http.Error(w, "invalid event data", http.StatusBadRequest)
		return
	}

	// Extract org_id from metadata — set when we created the PaymentIntent.
	orgID := pi.Metadata["org_id"]
	if orgID == "" {
		log.Printf("stripe webhook: payment_intent %s missing org_id metadata", pi.ID)
		// Acknowledge so Stripe doesn't retry — this needs manual investigation.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Idempotency check: has this PI already been credited?
	// Uses the 'credited' column on stripe_payments.
	alreadyCredited, err := h.store.IsPaymentCredited(r.Context(), pi.ID)
	if err != nil {
		log.Printf("stripe webhook: check credited failed for pi %s: %v", pi.ID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if alreadyCredited {
		// Already processed — Stripe is retrying. Acknowledge and stop.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Parse credit amounts from metadata.
	// Metadata was set at PaymentIntent creation so it matches exactly what
	// the customer was shown. We don't recompute from amount_cents here to
	// avoid any discrepancy if the formula changes between creation and webhook.
	base := parseMetadataInt64(pi.Metadata["base_credits"])
	bonus := parseMetadataInt64(pi.Metadata["bonus_credits"])
	total := base + bonus

	if total <= 0 {
		log.Printf("stripe webhook: pi %s has invalid credit metadata: base=%d bonus=%d",
			pi.ID, base, bonus)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Credit the org. Credits expire 24 months from now.
	expiresAt := time.Now().UTC().Add(24 * 30 * 24 * time.Hour) // ~24 months
	org, _ := h.store.GetOrganizationByID(r.Context(), orgID)
	if org != nil {
		expiresAt = time.Now().UTC().AddDate(int(org.CreditsExpireMonths), 0, 0)
	}

	if err := h.creditsMgr.Credit(r.Context(), credits.CreditParams{
		OrgID:     orgID,
		Amount:    total,
		Reason:    "purchase",
		RefID:     pi.ID,
		ExpiresAt: &expiresAt,
		Metadata: map[string]any{
			"stripe_pi_id":   pi.ID,
			"amount_cents":   pi.Amount,
			"currency":       string(pi.Currency),
			"base_credits":   base,
			"bonus_credits":  bonus,
		},
	}); err != nil {
		log.Printf("stripe webhook: credit org %s failed for pi %s: %v", orgID, pi.ID, err)
		// Return 500 — Stripe will retry. This is safe because we check
		// alreadyCredited at the top. Retries are idempotent.
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Mark payment as credited — idempotency guard for future retries.
	if err := h.store.MarkPaymentCredited(r.Context(), pi.ID, pi.Amount, string(pi.Currency), base, bonus); err != nil {
		log.Printf("stripe webhook: mark credited failed for pi %s: %v", pi.ID, err)
		// Credits were given. Marking failed. If Stripe retries:
		// - alreadyCredited check will return false (marking failed)
		// - Credit will be called again
		// - Credit itself is NOT idempotent by design (double credit is worse than retry)
		// Fix: we use ref_id = pi.ID in credit_ledger. Add a unique constraint
		// on (org_id, ref_id, reason='purchase') to prevent double crediting.
		// See migration: UNIQUE (org_id, ref_id) WHERE reason = 'purchase'
	}

	// Flip org to pro (has_ever_purchased) if this is their first purchase.
	if err := h.creditsMgr.MarkOrgPurchased(r.Context(), orgID); err != nil {
		log.Printf("stripe webhook: mark org purchased failed for org %s: %v", orgID, err)
		// Non-fatal — credits were given. Rate limit upgrade can be retried.
	}

	log.Printf("stripe webhook: credited %d credits to org %s (pi=%s)", total, orgID, pi.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (h *PaymentHandler) handlePaymentFailed(w http.ResponseWriter, r *http.Request, event stripe.Event) {
	var pi stripe.PaymentIntent
	if err := json.Unmarshal(event.Data.Raw, &pi); err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if err := h.store.UpdateStripePaymentStatus(r.Context(), pi.ID, "failed"); err != nil {
		log.Printf("stripe webhook: update payment failed status for pi %s: %v", pi.ID, err)
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *PaymentHandler) handlePaymentCanceled(w http.ResponseWriter, r *http.Request, event stripe.Event) {
	var pi stripe.PaymentIntent
	if err := json.Unmarshal(event.Data.Raw, &pi); err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if err := h.store.UpdateStripePaymentStatus(r.Context(), pi.ID, "canceled"); err != nil {
		log.Printf("stripe webhook: update payment canceled status for pi %s: %v", pi.ID, err)
	}

	w.WriteHeader(http.StatusNoContent)
}

// GetBillingHistory handles GET /cloud/organizations/{org_id}/billing
// Returns recent payment history for the org.
func (h *PaymentHandler) GetBillingHistory(w http.ResponseWriter, r *http.Request) {
	member := middleware.OrgMemberFromContext(r.Context())

	info, err := h.store.GetBillingHistoryForHandler(r.Context(), member.OrgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fetch billing info")
		return
	}

	writeJSON(w, http.StatusOK, info)
}

// ── Usage Handler ─────────────────────────────────────────────────────────────

// UsageHandler handles GET /cloud/organizations/{org_id}/usage
type UsageHandler struct {
	creditsMgr *credits.Manager
}

func NewUsageHandler(creditsMgr *credits.Manager) *UsageHandler {
	return &UsageHandler{creditsMgr: creditsMgr}
}

// GetUsage returns usage statistics for an org.
//
// Query params:
//   - start: start date (YYYY-MM-DD), defaults to 30 days ago
//   - end: end date (YYYY-MM-DD), defaults to today
//   - breakdown: "daily" returns day-by-day breakdown, omit for summary
//
// Response (summary):
//
//	{
//	  "org_id": "...",
//	  "period": {"start": "...", "end": "..."},
//	  "total_credits_consumed": 4100,
//	  "total_operations": 410,
//	  "current_balance": 58900,
//	  "by_operation": [
//	    { "operation": "sign_tx_evm", "count": 300, "credits_consumed": 3000 },
//	    ...
//	  ]
//	}
func (h *UsageHandler) GetUsage(w http.ResponseWriter, r *http.Request) {
	member := middleware.OrgMemberFromContext(r.Context())

	// Parse date range. Default: last 30 days.
	end := time.Now().UTC().Format("2006-01-02")
	start := time.Now().UTC().AddDate(0, 0, -30).Format("2006-01-02")

	if q := r.URL.Query().Get("start"); q != "" {
		if _, err := time.Parse("2006-01-02", q); err != nil {
			writeError(w, http.StatusBadRequest, "invalid start date — use YYYY-MM-DD")
			return
		}
		start = q
	}
	if q := r.URL.Query().Get("end"); q != "" {
		if _, err := time.Parse("2006-01-02", q); err != nil {
			writeError(w, http.StatusBadRequest, "invalid end date — use YYYY-MM-DD")
			return
		}
		end = q
	}

	stats, err := h.creditsMgr.GetUsageStats(r.Context(), member.OrgID, start, end)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fetch usage stats")
		return
	}

	balance, err := h.creditsMgr.GetBalance(r.Context(), member.OrgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fetch balance")
		return
	}

	var totalCredits, totalOps int64
	for _, s := range stats {
		totalCredits += s.CreditsConsumed
		totalOps += s.Count
	}

	resp := map[string]any{
		"org_id": member.OrgID,
		"period": map[string]string{
			"start": start,
			"end":   end,
		},
		"total_credits_consumed": totalCredits,
		"total_operations":       totalOps,
		"current_balance":        balance,
		"by_operation":           stats,
	}

	// Include daily breakdown if requested.
	if r.URL.Query().Get("breakdown") == "daily" {
		daily, err := h.creditsMgr.GetDailyUsage(r.Context(), member.OrgID, start, end)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to fetch daily usage")
			return
		}
		resp["daily"] = daily
	}

	writeJSON(w, http.StatusOK, resp)
}

// GetCreditBalance handles GET /cloud/organizations/{org_id}/credits
func (h *UsageHandler) GetCreditBalance(w http.ResponseWriter, r *http.Request) {
	member := middleware.OrgMemberFromContext(r.Context())

	balance, err := h.creditsMgr.GetBalance(r.Context(), member.OrgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"org_id":  member.OrgID,
		"balance": balance,
	})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func formatInt64(n int64) string {
	return fmt.Sprintf("%d", n)
}

func parseMetadataInt64(s string) int64 {
	var n int64
	fmt.Sscanf(s, "%d", &n)
	return n
}