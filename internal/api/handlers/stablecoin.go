package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/vaultkey/vaultkey/internal/api/middleware"
	"github.com/vaultkey/vaultkey/internal/stablecoin"
)

// StablecoinHandler exposes the stablecoin transfer and balance endpoints.
// It delegates all business logic to stablecoin.Service — handlers only
// parse HTTP and write responses.
type StablecoinHandler struct {
	svc *stablecoin.Service
}

func NewStablecoinHandler(svc *stablecoin.Service) *StablecoinHandler {
	return &StablecoinHandler{svc: svc}
}

// ── Transfer ──────────────────────────────────────────────────────────────────

type stablecoinTransferRequest struct {
	Token          string  `json:"token"`           // "usdc" or "usdt"
	To             string  `json:"to"`              // recipient address
	Amount         string  `json:"amount"`          // human-readable, e.g. "50.00"
	ChainID        string  `json:"chain_id"`        // required for EVM, omit for Solana
	Gasless        bool    `json:"gasless"`         // true = relayer pays gas
	IdempotencyKey *string `json:"idempotency_key"` // optional dedup key
}

type stablecoinTransferResponse struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}

// Transfer handles POST /wallets/{walletId}/stablecoin/transfer
//
// EVM example:
//
//	{
//	  "token": "usdc",
//	  "to": "0xRecipient",
//	  "amount": "50.00",
//	  "chain_id": "137",
//	  "gasless": true
//	}
//
// Solana example:
//
//	{
//	  "token": "usdc",
//	  "to": "RecipientBase58",
//	  "amount": "50.00"
//	}
//
// Returns HTTP 202 immediately. Poll GET /jobs/{jobId} or wait for webhook.
func (h *StablecoinHandler) Transfer(w http.ResponseWriter, r *http.Request) {
	project := middleware.ProjectFromContext(r.Context())
	walletID := r.PathValue("walletId")

	// Determine chain type from the URL.
	// EVM: POST /wallets/{walletId}/stablecoin/transfer/evm
	// Solana: POST /wallets/{walletId}/stablecoin/transfer/solana
	chainType := r.PathValue("chainType")
	if chainType != "evm" && chainType != "solana" {
		writeError(w, http.StatusBadRequest, "chain type must be 'evm' or 'solana'")
		return
	}

	var req stablecoinTransferRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Token == "" {
		writeError(w, http.StatusBadRequest, "token is required (usdc or usdt)")
		return
	}
	if req.To == "" {
		writeError(w, http.StatusBadRequest, "to address is required")
		return
	}
	if req.Amount == "" {
		writeError(w, http.StatusBadRequest, "amount is required")
		return
	}
	if chainType == "evm" && req.ChainID == "" {
		writeError(w, http.StatusBadRequest, "chain_id is required for EVM transfers")
		return
	}
	if chainType == "solana" && req.ChainID != "" {
		writeError(w, http.StatusBadRequest, "chain_id must not be set for Solana transfers")
		return
	}

	result, err := h.svc.Transfer(r.Context(), stablecoin.TransferRequest{
		ProjectID:      project.ID,
		WalletID:       walletID,
		Token:          req.Token,
		To:             req.To,
		Amount:         req.Amount,
		ChainID:        req.ChainID,
		ChainType:      chainType,
		Gasless:        req.Gasless,
		IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		// Distinguish validation errors (400) from internal errors (500).
		// Service returns descriptive errors for token/chain mismatches — surface them.
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Idempotency hit — job already in progress or done.
	if result.Status == "completed" || result.Status == "processing" {
		writeJSON(w, http.StatusOK, stablecoinTransferResponse{
			JobID:  result.JobID,
			Status: result.Status,
		})
		return
	}

	writeJSON(w, http.StatusAccepted, stablecoinTransferResponse{
		JobID:  result.JobID,
		Status: result.Status,
	})
}

// ── Balance ───────────────────────────────────────────────────────────────────

// Balance handles GET /wallets/{walletId}/stablecoin/balance
//
// Query params:
//   - token: "usdc" or "usdt" (required)
//   - chain_id: EVM chain ID (required for EVM wallets)
//
// Response:
//
//	{
//	  "address": "0x...",
//	  "token": "usdc",
//	  "symbol": "USDC",
//	  "balance": "50.00",
//	  "raw_balance": "50000000",
//	  "chain_id": "137"
//	}
func (h *StablecoinHandler) Balance(w http.ResponseWriter, r *http.Request) {
	project := middleware.ProjectFromContext(r.Context())
	walletID := r.PathValue("walletId")
	chainType := r.PathValue("chainType")

	if chainType != "evm" && chainType != "solana" {
		writeError(w, http.StatusBadRequest, "chain type must be 'evm' or 'solana'")
		return
	}

	token := r.URL.Query().Get("token")
	chainID := r.URL.Query().Get("chain_id")

	if token == "" {
		writeError(w, http.StatusBadRequest, "token query param required (usdc or usdt)")
		return
	}
	if chainType == "evm" && chainID == "" {
		writeError(w, http.StatusBadRequest, "chain_id query param required for EVM wallets")
		return
	}

	result, err := h.svc.Balance(r.Context(), stablecoin.BalanceRequest{
		ProjectID: project.ID,
		WalletID:  walletID,
		Token:     token,
		ChainID:   chainID,
		ChainType: chainType,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// ── Discovery ─────────────────────────────────────────────────────────────────

// SupportedTokens handles GET /stablecoins
//
// Returns all supported token+chain combinations so devs can discover
// what's available without reading docs.
//
// Response:
//
//	{
//	  "tokens": [
//	    { "token": "usdc", "chain": "evm", "chain_id": "137", "contract": "0x...", "decimals": 6, "symbol": "USDC" },
//	    { "token": "usdc", "chain": "solana", "contract": "EPjF...", "decimals": 6, "symbol": "USDC" },
//	    ...
//	  ]
//	}
// func (h *StablecoinHandler) SupportedTokens(w http.ResponseWriter, r *http.Request) {
// 	tokens := stablecoin.SupportedTokens()
// 	writeJSON(w, http.StatusOK, map[string]any{"tokens": tokens})
// }