package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/vaultkey/vaultkey/internal/api/middleware"
	"github.com/vaultkey/vaultkey/internal/storage"
	"github.com/vaultkey/vaultkey/internal/sweep"
)

type SweepHandler struct {
	store    *storage.Store
	sweepSvc *sweep.Service
}

func NewSweepHandler(store *storage.Store, sweepSvc *sweep.Service) *SweepHandler {
	return &SweepHandler{store: store, sweepSvc: sweepSvc}
}

// ── Master Wallet ─────────────────────────────────────────────────────────────

// provisionMasterWalletRequest does NOT include wallet_id.
// The developer specifies the chain. VaultKey generates the wallet.
type provisionMasterWalletRequest struct {
	ChainType     string `json:"chain_type"`
	ChainID       string `json:"chain_id"`       // required for EVM, omit for Solana
	DustThreshold string `json:"dust_threshold"` // optional, default "0"
}

type sweepConfigResponse struct {
	ID             string `json:"id"`
	ChainType      string `json:"chain_type"`
	ChainID        string `json:"chain_id,omitempty"`
	MasterWalletID string `json:"master_wallet_id"`
	MasterAddress  string `json:"master_address"`
	DustThreshold  string `json:"dust_threshold"`
	Enabled        bool   `json:"enabled"`
}

// ProvisionMasterWallet generates a fresh dedicated wallet and designates it
// as the sweep destination for the given chain. The developer does not choose
// the wallet — VaultKey always creates a new one.
//
// Calling this again on the same chain returns the existing wallet and
//
// POST /projects/master-wallet
func (h *SweepHandler) ProvisionMasterWallet(w http.ResponseWriter, r *http.Request) {
	project := middleware.ProjectFromContext(r.Context())

	var req provisionMasterWalletRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ChainType != "evm" && req.ChainType != "solana" {
		writeError(w, http.StatusBadRequest, "chain_type must be 'evm' or 'solana'")
		return
	}
	if req.ChainType == "evm" && req.ChainID == "" {
		writeError(w, http.StatusBadRequest, "chain_id is required for EVM")
		return
	}
	if req.ChainType == "solana" && req.ChainID != "" {
		writeError(w, http.StatusBadRequest, "chain_id must not be set for Solana")
		return
	}

	result, err := h.sweepSvc.ProvisionMasterWallet(
		r.Context(),
		project.ID,
		req.ChainType,
		req.ChainID,
		req.DustThreshold,
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	walletID := result.Wallet.ID
	h.store.WriteAuditLog(r.Context(), project.ID, &walletID, nil, //nolint:errcheck
		"sweep.master_wallet_provisioned", project.ID, map[string]any{
			"chain_type": req.ChainType,
			"chain_id":   req.ChainID,
			"address":    result.Wallet.Address,
		})

	writeJSON(w, http.StatusCreated, sweepConfigResponse{
		ID:             result.Config.ID,
		ChainType:      result.Config.ChainType,
		ChainID:        result.Config.ChainID,
		MasterWalletID: result.Wallet.ID,
		MasterAddress:  result.Wallet.Address,
		DustThreshold:  result.Config.DustThreshold,
		Enabled:        result.Config.Enabled,
	})
}

// GetMasterWallet returns the sweep config and master wallet address for a chain.
//
// GET /projects/master-wallet?chain_type=evm&chain_id=137
// GET /projects/master-wallet?chain_type=solana
func (h *SweepHandler) GetMasterWallet(w http.ResponseWriter, r *http.Request) {
	project := middleware.ProjectFromContext(r.Context())
	chainType := r.URL.Query().Get("chain_type")
	chainID := r.URL.Query().Get("chain_id") // empty for Solana

	if chainType == "" {
		writeError(w, http.StatusBadRequest, "chain_type query param required")
		return
	}
	if chainType != "evm" && chainType != "solana" {
		writeError(w, http.StatusBadRequest, "chain_type must be 'evm' or 'solana'")
		return
	}
	if chainType == "evm" && chainID == "" {
		writeError(w, http.StatusBadRequest, "chain_id query param required for EVM")
		return
	}

	cfg, err := h.store.GetSweepConfig(r.Context(), project.ID, chainType, chainID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if cfg == nil {
		writeError(w, http.StatusNotFound, "no master wallet configured for this chain")
		return
	}

	masterWlt, err := h.store.GetWalletByID(r.Context(), cfg.MasterWalletID, project.ID)
	if err != nil || masterWlt == nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, sweepConfigResponse{
		ID:             cfg.ID,
		ChainType:      cfg.ChainType,
		ChainID:        cfg.ChainID,
		MasterWalletID: cfg.MasterWalletID,
		MasterAddress:  masterWlt.Address,
		DustThreshold:  cfg.DustThreshold,
		Enabled:        cfg.Enabled,
	})
}

// ListMasterWallets returns all sweep configs for the project.
//
// GET /projects/master-wallets
func (h *SweepHandler) ListMasterWallets(w http.ResponseWriter, r *http.Request) {
	project := middleware.ProjectFromContext(r.Context())

	configs, err := h.store.ListSweepConfigs(r.Context(), project.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	resp := make([]sweepConfigResponse, 0, len(configs))
	for _, cfg := range configs {
		masterWlt, err := h.store.GetWalletByID(r.Context(), cfg.MasterWalletID, project.ID)
		if err != nil || masterWlt == nil {
			continue
		}
		resp = append(resp, sweepConfigResponse{
			ID:             cfg.ID,
			ChainType:      cfg.ChainType,
			ChainID:        cfg.ChainID,
			MasterWalletID: cfg.MasterWalletID,
			MasterAddress:  masterWlt.Address,
			DustThreshold:  cfg.DustThreshold,
			Enabled:        cfg.Enabled,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"master_wallets": resp})
}

// UpdateSweepConfig updates dust threshold and enabled flag.
//
// PATCH /projects/master-wallet/{configId}
type updateSweepConfigRequest struct {
	DustThreshold string `json:"dust_threshold"`
	Enabled       *bool  `json:"enabled"`
}

func (h *SweepHandler) UpdateSweepConfig(w http.ResponseWriter, r *http.Request) {
	project := middleware.ProjectFromContext(r.Context())
	configID := r.PathValue("configId")

	var req updateSweepConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	cfg, err := h.store.GetSweepConfigByID(r.Context(), configID)
	if err != nil || cfg == nil || cfg.ProjectID != project.ID {
		writeError(w, http.StatusNotFound, "sweep config not found")
		return
	}

	threshold := cfg.DustThreshold
	if req.DustThreshold != "" {
		threshold = req.DustThreshold
	}

	enabled := cfg.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	if err := h.store.UpdateSweepConfig(r.Context(), project.ID, configID, threshold, enabled); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update sweep config")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// ── Sweep Trigger ─────────────────────────────────────────────────────────────

type triggerSweepRequest struct {
	ChainType      string  `json:"chain_type"`
	ChainID        string  `json:"chain_id"`        // required for EVM, omit for Solana
	IdempotencyKey *string `json:"idempotency_key"` // optional
}

type triggerSweepResponse struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}

// TriggerSweep enqueues a sweep job for a wallet.
// Supported chains: EVM L2s (not Ethereum mainnet), Solana.
// Requires: relayer wallet configured and funded, master wallet configured.
//
// POST /wallets/{walletId}/sweep
func (h *SweepHandler) TriggerSweep(w http.ResponseWriter, r *http.Request) {
	project := middleware.ProjectFromContext(r.Context())
	walletID := r.PathValue("walletId")

	var req triggerSweepRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ChainType != "evm" && req.ChainType != "solana" {
		writeError(w, http.StatusBadRequest, "chain_type must be 'evm' or 'solana'")
		return
	}
	if req.ChainType == "evm" && req.ChainID == "" {
		writeError(w, http.StatusBadRequest, "chain_id is required for EVM sweep")
		return
	}
	if req.ChainType == "solana" && req.ChainID != "" {
		writeError(w, http.StatusBadRequest, "chain_id must not be set for Solana")
		return
	}

	job, err := h.sweepSvc.TriggerSweep(r.Context(), sweep.SweepRequest{
		ProjectID:      project.ID,
		WalletID:       walletID,
		ChainType:      req.ChainType,
		ChainID:        req.ChainID,
		IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if job.Status == "completed" || job.Status == "processing" {
		writeJSON(w, http.StatusOK, triggerSweepResponse{JobID: job.ID, Status: job.Status})
		return
	}

	writeJSON(w, http.StatusAccepted, triggerSweepResponse{JobID: job.ID, Status: "pending"})
}