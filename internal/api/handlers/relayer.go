package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/vaultkey/vaultkey/internal/api/middleware"
	"github.com/vaultkey/vaultkey/internal/relayer"
	"github.com/vaultkey/vaultkey/internal/storage"
	"github.com/vaultkey/vaultkey/internal/wallet"
)

type RelayerHandler struct {
	store      *storage.Store
	walletSvc  *wallet.Service
	relayerSvc *relayer.Service
}

func NewRelayerHandler(store *storage.Store, walletSvc *wallet.Service, relayerSvc *relayer.Service) *RelayerHandler {
	return &RelayerHandler{store: store, walletSvc: walletSvc, relayerSvc: relayerSvc}
}

// RegisterRelayer creates a new wallet designated as the gas relayer for a chain.
// The relayer wallet is a standard wallet under the hood - same key storage,
// same encryption. It just has a role: pay gas for other wallets.
//
// POST /projects/relayer
// {
//   "chain_type": "evm",
//   "chain_id": "1",           // required for EVM, omit for Solana
//   "min_balance_alert": "0.1" // optional, ETH or SOL threshold
// }
type registerRelayerRequest struct {
	ChainType        string `json:"chain_type"`
	ChainID          string `json:"chain_id"`           // EVM only
	MinBalanceAlert  string `json:"min_balance_alert"`  // optional
}

type relayerResponse struct {
	ID              string `json:"id"`
	WalletID        string `json:"wallet_id"`
	Address         string `json:"address"`
	ChainType       string `json:"chain_type"`
	ChainID         string `json:"chain_id,omitempty"`
	MinBalanceAlert string `json:"min_balance_alert"`
	Active          bool   `json:"active"`
}

func (h *RelayerHandler) RegisterRelayer(w http.ResponseWriter, r *http.Request) {
	project := middleware.ProjectFromContext(r.Context())

	var req registerRelayerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ChainType != "evm" && req.ChainType != "solana" {
		writeError(w, http.StatusBadRequest, "chain_type must be 'evm' or 'solana'")
		return
	}
	if req.ChainType == "evm" && req.ChainID == "" {
		writeError(w, http.StatusBadRequest, "chain_id is required for EVM relayer wallets")
		return
	}

	minBalance := req.MinBalanceAlert
	if minBalance == "" {
		minBalance = "0.1"
	}

	// Generate a fresh wallet for the relayer - same path as regular wallets
	generated, err := h.walletSvc.Generate(r.Context(), wallet.ChainType(req.ChainType))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate relayer wallet")
		return
	}

	// Store the wallet
	stored, err := h.store.CreateWallet(r.Context(), &storage.Wallet{
		ProjectID:    project.ID,
		UserID:       "_relayer", // internal marker, not a real user
		ChainType:    req.ChainType,
		Address:      generated.Address,
		EncryptedKey: generated.EncryptedKey,
		EncryptedDEK: generated.EncryptedDEK,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store relayer wallet")
		return
	}

	// Register as relayer
	rw, err := h.store.CreateRelayerWallet(r.Context(), project.ID, stored.ID, stored.Address, req.ChainType, req.ChainID, minBalance)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to register relayer")
		return
	}

	walletID := stored.ID
	h.store.WriteAuditLog(r.Context(), project.ID, &walletID, nil, "relayer.register", project.ID, map[string]any{ //nolint:errcheck
		"chain_type": req.ChainType,
		"chain_id":   req.ChainID,
		"address":    stored.Address,
	})

	writeJSON(w, http.StatusCreated, relayerResponse{
		ID:              rw.ID,
		WalletID:        stored.ID,
		Address:         stored.Address,
		ChainType:       rw.ChainType,
		ChainID:         rw.ChainID,
		MinBalanceAlert: rw.MinBalanceAlert,
		Active:          rw.Active,
	})
}

// GetRelayerInfo returns balance and health status for a relayer wallet.
//
// GET /projects/relayer?chain_type=evm&chain_id=1
func (h *RelayerHandler) GetRelayerInfo(w http.ResponseWriter, r *http.Request) {
	project := middleware.ProjectFromContext(r.Context())
	chainType := r.URL.Query().Get("chain_type")
	chainID := r.URL.Query().Get("chain_id")

	if chainType == "" {
		writeError(w, http.StatusBadRequest, "chain_type query param required")
		return
	}

	info, err := h.relayerSvc.GetInfo(r.Context(), project.ID, chainType, chainID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, info)
}

// ListRelayers lists all relayer wallets for a project.
//
// GET /projects/relayers
func (h *RelayerHandler) ListRelayers(w http.ResponseWriter, r *http.Request) {
	project := middleware.ProjectFromContext(r.Context())

	relayers, err := h.store.ListRelayerWallets(r.Context(), project.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	resp := make([]relayerResponse, 0, len(relayers))
	for _, rw := range relayers {
		resp = append(resp, relayerResponse{
			ID:              rw.ID,
			WalletID:        rw.WalletID,
			Address:         rw.Address,
			ChainType:       rw.ChainType,
			ChainID:         rw.ChainID,
			MinBalanceAlert: rw.MinBalanceAlert,
			Active:          rw.Active,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"relayers": resp})
}

// DeactivateRelayer removes a relayer wallet from active duty.
//
// DELETE /projects/relayer/{relayerId}
func (h *RelayerHandler) DeactivateRelayer(w http.ResponseWriter, r *http.Request) {
	project := middleware.ProjectFromContext(r.Context())
	relayerID := r.PathValue("relayerId")

	if err := h.store.DeactivateRelayerWallet(r.Context(), project.ID, relayerID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to deactivate relayer")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deactivated"})
}
