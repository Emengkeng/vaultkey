package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/vaultkey/vaultkey/internal/stablecoin"
	"github.com/vaultkey/vaultkey/internal/storage"
)

// AdminHandler exposes internal management endpoints.
// These are NOT authenticated via the project API key — they use a separate
// admin token (ADMIN_TOKEN env var) checked by middleware.AdminAuth.
// In production, these routes should be firewalled to internal network only.
type AdminHandler struct {
	registry *stablecoin.Registry
}

func NewAdminHandler(registry *stablecoin.Registry) *AdminHandler {
	return &AdminHandler{registry: registry}
}

// ── Stablecoin token registry ─────────────────────────────────────────────────

type upsertTokenRequest struct {
	Token     string `json:"token"`      // "usdc", "usdt", or any new token
	Symbol    string `json:"symbol"`     // "USDC" — display name
	ChainType string `json:"chain_type"` // "evm" or "solana"
	ChainID   string `json:"chain_id"`   // EVM chain ID; omit for Solana
	Contract  string `json:"contract"`   // EVM contract address or Solana mint
	Decimals  int    `json:"decimals"`   // typically 6; BSC USDC/USDT is 18
}

type tokenResponse struct {
	ID        string `json:"id"`
	Token     string `json:"token"`
	Symbol    string `json:"symbol"`
	ChainType string `json:"chain_type"`
	ChainID   string `json:"chain_id,omitempty"`
	Contract  string `json:"contract"`
	Decimals  int    `json:"decimals"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// UpsertToken handles POST /admin/stablecoins
//
// Registers or updates a stablecoin token for a specific chain.
// Safe to call multiple times — idempotent via ON CONFLICT DO UPDATE.
// Cache is invalidated automatically on success.
//
// Example — add USDC on Polygon Mumbai testnet:
//
//	{
//	  "token": "usdc",
//	  "symbol": "USDC",
//	  "chain_type": "evm",
//	  "chain_id": "80001",
//	  "contract": "0x0FA8781a83E46826621b3BC094Ea2A0212e71B23",
//	  "decimals": 6
//	}
//
// Example — add USDC on Solana Devnet:
//
//	{
//	  "token": "usdc",
//	  "symbol": "USDC",
//	  "chain_type": "solana",
//	  "contract": "4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU",
//	  "decimals": 6
//	}
func (h *AdminHandler) UpsertToken(w http.ResponseWriter, r *http.Request) {
	var req upsertTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate required fields
	if req.Token == "" {
		writeError(w, http.StatusBadRequest, "token is required (e.g. 'usdc', 'usdt')")
		return
	}
	if req.Symbol == "" {
		writeError(w, http.StatusBadRequest, "symbol is required (e.g. 'USDC')")
		return
	}
	if req.ChainType != "evm" && req.ChainType != "solana" {
		writeError(w, http.StatusBadRequest, "chain_type must be 'evm' or 'solana'")
		return
	}
	if req.ChainType == "evm" && req.ChainID == "" {
		writeError(w, http.StatusBadRequest, "chain_id is required for EVM tokens")
		return
	}
	if req.ChainType == "solana" && req.ChainID != "" {
		writeError(w, http.StatusBadRequest, "chain_id must not be set for Solana tokens")
		return
	}
	if req.Contract == "" {
		writeError(w, http.StatusBadRequest, "contract is required (EVM contract address or Solana mint address)")
		return
	}
	if req.Decimals <= 0 {
		writeError(w, http.StatusBadRequest, "decimals must be greater than 0")
		return
	}
	if req.Decimals > 18 {
		writeError(w, http.StatusBadRequest, "decimals must be 18 or less")
		return
	}

	// Basic contract address format check
	if req.ChainType == "evm" && !strings.HasPrefix(req.Contract, "0x") {
		writeError(w, http.StatusBadRequest, "EVM contract address must start with 0x")
		return
	}

	result, err := h.registry.Upsert(r.Context(), &storage.StablecoinToken{
		Token:     req.Token,
		Symbol:    req.Symbol,
		ChainType: req.ChainType,
		ChainID:   req.ChainID,
		Contract:  req.Contract,
		Decimals:  req.Decimals,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to register token: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, tokenResponse{
		ID:        result.ID,
		Token:     result.Token,
		Symbol:    result.Symbol,
		ChainType: result.ChainType,
		ChainID:   result.ChainID,
		Contract:  result.Contract,
		Decimals:  result.Decimals,
		Enabled:   result.Enabled,
		CreatedAt: result.CreatedAt.String(),
		UpdatedAt: result.UpdatedAt.String(),
	})
}

// DisableToken handles DELETE /admin/stablecoins/{tokenId}
//
// Soft-deletes a token entry. The row is kept for audit purposes.
// Existing jobs referencing this token are unaffected — they already have
// the contract address in their payload. New transfer requests will be
// rejected with a "token not registered" error.
// Cache is invalidated automatically.
func (h *AdminHandler) DisableToken(w http.ResponseWriter, r *http.Request) {
	tokenID := r.PathValue("tokenId")
	if tokenID == "" {
		writeError(w, http.StatusBadRequest, "tokenId path param required")
		return
	}

	if err := h.registry.Disable(r.Context(), tokenID); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "disabled", "id": tokenID})
}

// ListTokens handles GET /admin/stablecoins
//
// Returns all enabled tokens across all chains.
// This is also the public discovery endpoint at GET /stablecoins —
// the admin version is identical but lives behind admin auth.
func (h *AdminHandler) ListTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := h.registry.ListAll(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list tokens")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": tokens})
}