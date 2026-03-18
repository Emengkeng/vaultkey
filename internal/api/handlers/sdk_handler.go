package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/vaultkey/vaultkey/internal/api/middleware"
	"github.com/vaultkey/vaultkey/internal/credits"
	"github.com/vaultkey/vaultkey/internal/queue"
	"github.com/vaultkey/vaultkey/internal/rpc"
	"github.com/vaultkey/vaultkey/internal/stablecoin"
	"github.com/vaultkey/vaultkey/internal/storage"
	"github.com/vaultkey/vaultkey/internal/sweep"
	"github.com/vaultkey/vaultkey/internal/wallet"
)

// SDKHandler wraps existing handlers with credit deduction.
// Every billable operation debits before forwarding to the underlying logic.
// Free operations (balance, broadcast, get wallet, list wallets, get job)
// forward directly with no credit check.
//
// The underlying Handler, RelayerHandler, SweepHandler, StablecoinHandler
// are reused unchanged — SDKHandler composes them rather than duplicating.
type SDKHandler struct {
	credits     *credits.Manager
	handler     *Handler
	sweepH      *SweepHandler
	stablecoinH *StablecoinHandler
	store       *storage.Store
	walletSvc   *wallet.Service
	queue       *queue.Queue
	rpcMgr      *rpc.Manager
}

func NewSDKHandler(
	creditsMgr *credits.Manager,
	store *storage.Store,
	walletSvc *wallet.Service,
	q *queue.Queue,
	rpcMgr *rpc.Manager,
	sweepSvc *sweep.Service,
	stablecoinSvc *stablecoin.Service,
) *SDKHandler {
	return &SDKHandler{
		credits:     creditsMgr,
		handler:     New(store, walletSvc, q, rpcMgr),
		sweepH:      NewSweepHandler(store, sweepSvc),
		stablecoinH: NewStablecoinHandler(stablecoinSvc),
		store:       store,
		walletSvc:   walletSvc,
		queue:       q,
		rpcMgr:      rpcMgr,
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// debit debits credits for an operation before proceeding.
// Returns false and writes the appropriate HTTP error if debit fails.
// On ErrInsufficientCredits → 402 with balance info.
// On ErrOperationNotFound → 500 (config error, not user error).
func (h *SDKHandler) debit(w http.ResponseWriter, r *http.Request, operation string) bool {
	orgID := middleware.OrgIDFromContext(r.Context())
	project := middleware.ProjectFromContext(r.Context())

	if orgID == "" {
		writeError(w, http.StatusInternalServerError, "org context missing")
		return false
	}

	result, err := h.credits.Debit(r.Context(), credits.DebitParams{
		OrgID:     orgID,
		Operation: operation,
		Metadata: map[string]any{
			"project_id": project.ID,
			"wallet_id":  r.PathValue("walletId"),
			"path":       r.URL.Path,
		},
	})
	if err == credits.ErrInsufficientCredits {
		balance, _ := h.credits.GetBalance(r.Context(), orgID)
		cost, _ := h.credits.GetCost(r.Context(), operation)
		var costVal int64
		if cost != nil {
			costVal = cost.Cost
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"error":           "insufficient credits",
			"current_balance": balance,
			"required":        costVal,
			"purchase_url":    "/cloud/billing/purchase",
		})
		return false
	}
	if err == credits.ErrOperationNotFound {
		writeError(w, http.StatusInternalServerError,
			"operation cost not configured — contact support")
		return false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "credit deduction failed")
		return false
	}

	// Record usage for rollup — non-blocking, best effort.
	// Worker also calls RecordUsage after job completion for async ops,
	// but synchronous ops (create_wallet) are recorded here.
	if result != nil {
		go h.credits.RecordUsage(r.Context(), orgID, operation, result.Cost) //nolint:errcheck
	}

	return true
}

// ── Wallet Operations ─────────────────────────────────────────────────────────

// CreateWallet: debit create_wallet (10 credits), enforce free tier wallet cap.
func (h *SDKHandler) CreateWallet(w http.ResponseWriter, r *http.Request) {
	project := middleware.ProjectFromContext(r.Context())
	orgID := middleware.OrgIDFromContext(r.Context())

	// Free tier wallet cap: 50 wallets per org.
	// Check has_ever_purchased — if false, enforce the cap.
	org, err := h.store.GetOrganizationByID(r.Context(), orgID)
	if err != nil || org == nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if !org.HasEverPurchased {
		count, err := h.store.CountWalletsForOrg(r.Context(), project.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if count >= 50 {
			writeError(w, http.StatusForbidden,
				"free tier wallet limit reached (50 wallets) — purchase credits to unlock unlimited wallets")
			return
		}
	}

	if !h.debit(w, r, "create_wallet") {
		return
	}

	// Forward to existing handler — identical logic, no duplication.
	h.handler.CreateWallet(w, r)
}

// GetWallet: free — forward directly.
func (h *SDKHandler) GetWallet(w http.ResponseWriter, r *http.Request) {
	h.handler.GetWallet(w, r)
}

// ListUserWallets: free — forward directly.
func (h *SDKHandler) ListUserWallets(w http.ResponseWriter, r *http.Request) {
	h.handler.ListUserWallets(w, r)
}

// ── Signing Operations ────────────────────────────────────────────────────────

func (h *SDKHandler) SignEVMTransaction(w http.ResponseWriter, r *http.Request) {
	if !h.debit(w, r, "sign_tx_evm") {
		return
	}
	h.handler.SubmitSignEVMTransaction(w, r)
}

func (h *SDKHandler) SignEVMMessage(w http.ResponseWriter, r *http.Request) {
	if !h.debit(w, r, "sign_msg_evm") {
		return
	}
	h.handler.SubmitSignEVMMessage(w, r)
}

func (h *SDKHandler) SignSolanaTransaction(w http.ResponseWriter, r *http.Request) {
	if !h.debit(w, r, "sign_tx_solana") {
		return
	}
	h.handler.SubmitSignSolanaTransaction(w, r)
}

func (h *SDKHandler) SignSolanaMessage(w http.ResponseWriter, r *http.Request) {
	if !h.debit(w, r, "sign_msg_solana") {
		return
	}
	h.handler.SubmitSignSolanaMessage(w, r)
}

// ── Free Operations ───────────────────────────────────────────────────────────

func (h *SDKHandler) GetJob(w http.ResponseWriter, r *http.Request) {
	h.handler.GetJob(w, r)
}

func (h *SDKHandler) GetBalance(w http.ResponseWriter, r *http.Request) {
	h.handler.GetBalance(w, r)
}

func (h *SDKHandler) Broadcast(w http.ResponseWriter, r *http.Request) {
	h.handler.Broadcast(w, r)
}

// ── Sweep ─────────────────────────────────────────────────────────────────────

func (h *SDKHandler) TriggerSweep(w http.ResponseWriter, r *http.Request) {
	// Determine chain type from request body to pick the right cost.
	// We need to peek at the body without consuming it.
	// Simplest: decode chain_type, then reconstruct context for sweep handler.
	// Use a lightweight peek struct.
	var peek struct {
		ChainType string `json:"chain_type"`
	}

	// We can't consume the body here since SweepHandler reads it again.
	// Solution: read into a buffer, decode, then replace r.Body.
	body, ok := peekBody(r, &peek)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	r.Body = body // restore for downstream handler

	operation := "sweep_evm"
	if peek.ChainType == "solana" {
		operation = "sweep_solana"
	}

	if !h.debit(w, r, operation) {
		return
	}

	h.sweepH.TriggerSweep(w, r)
}

// ── Stablecoin ────────────────────────────────────────────────────────────────

func (h *SDKHandler) StablecoinTransfer(w http.ResponseWriter, r *http.Request) {
	chainType := r.PathValue("chainType")
	operation := "stablecoin_transfer_evm"
	if chainType == "solana" {
		operation = "stablecoin_transfer_solana"
	}

	if !h.debit(w, r, operation) {
		return
	}

	h.stablecoinH.Transfer(w, r)
}

func (h *SDKHandler) StablecoinBalance(w http.ResponseWriter, r *http.Request) {
	// Free — forward directly.
	h.stablecoinH.Balance(w, r)
}