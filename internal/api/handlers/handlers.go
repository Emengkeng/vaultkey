package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/vaultkey/vaultkey/internal/api/middleware"
	"github.com/vaultkey/vaultkey/internal/queue"
	"github.com/vaultkey/vaultkey/internal/rpc"
	"github.com/vaultkey/vaultkey/internal/storage"
	"github.com/vaultkey/vaultkey/internal/wallet"
	"golang.org/x/crypto/bcrypt"
)

type Handler struct {
	store     *storage.Store
	walletSvc *wallet.Service
	queue     *queue.Queue
	rpcMgr    *rpc.Manager
}

func New(store *storage.Store, walletSvc *wallet.Service, q *queue.Queue, rpcMgr *rpc.Manager) *Handler {
	return &Handler{store: store, walletSvc: walletSvc, queue: q, rpcMgr: rpcMgr}
}

// ── Project ───────────────────────────────────────────────────────────────────

type createProjectRequest struct {
	Name         string `json:"name"`
	WebhookURL   string `json:"webhook_url"`
	RateLimitRPS int    `json:"rate_limit_rps"`
	MaxRetries   int    `json:"max_retries"`
}

type createProjectResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	APIKey    string `json:"api_key"`
	APISecret string `json:"api_secret"` // shown once, never stored in plaintext
}

func (h *Handler) CreateProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	rateLimitRPS := 100
	if req.RateLimitRPS > 0 {
		rateLimitRPS = req.RateLimitRPS
	}
	maxRetries := 3
	if req.MaxRetries > 0 {
		maxRetries = req.MaxRetries
	}

	apiKey, err := generateToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	apiSecret, err := generateToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Hash the secret - plaintext is returned once and never stored
	hash, err := bcrypt.GenerateFromPassword([]byte(apiSecret), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	var webhookURL *string
	if req.WebhookURL != "" {
		webhookURL = &req.WebhookURL
	}

	// Generate a webhook signing secret
	webhookSecret, err := generateToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	project, err := h.store.CreateProject(r.Context(), req.Name, apiKey, string(hash), webhookURL, &webhookSecret, rateLimitRPS, maxRetries)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create project")
		return
	}

	writeJSON(w, http.StatusCreated, createProjectResponse{
		ID:        project.ID,
		Name:      project.Name,
		APIKey:    project.APIKey,
		APISecret: apiSecret, // plaintext - shown once
	})
}

type updateWebhookRequest struct {
	WebhookURL string `json:"webhook_url"`
}

func (h *Handler) UpdateWebhook(w http.ResponseWriter, r *http.Request) {
	project := middleware.ProjectFromContext(r.Context())

	var req updateWebhookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	var webhookURL *string
	if req.WebhookURL != "" {
		webhookURL = &req.WebhookURL
	}

	if err := h.store.UpdateProjectWebhook(r.Context(), project.ID, webhookURL, project.WebhookSecret); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update webhook")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// ── Wallets ───────────────────────────────────────────────────────────────────

type createWalletRequest struct {
	UserID    string `json:"user_id"`
	ChainType string `json:"chain_type"`
	Label     string `json:"label"`
}

type walletResponse struct {
	ID        string  `json:"id"`
	UserID    string  `json:"user_id"`
	ChainType string  `json:"chain_type"`
	Address   string  `json:"address"`
	Label     *string `json:"label,omitempty"`
	CreatedAt string  `json:"created_at"`
}

func (h *Handler) CreateWallet(w http.ResponseWriter, r *http.Request) {
	project := middleware.ProjectFromContext(r.Context())

	var req createWalletRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.UserID == "" {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}
	if req.ChainType != "evm" && req.ChainType != "solana" {
		writeError(w, http.StatusBadRequest, "chain_type must be 'evm' or 'solana'")
		return
	}

	generated, err := h.walletSvc.Generate(r.Context(), wallet.ChainType(req.ChainType))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate wallet")
		return
	}

	var label *string
	if req.Label != "" {
		label = &req.Label
	}

	stored, err := h.store.CreateWallet(r.Context(), &storage.Wallet{
		ProjectID:    project.ID,
		UserID:       req.UserID,
		ChainType:    req.ChainType,
		Address:      generated.Address,
		EncryptedKey: generated.EncryptedKey,
		EncryptedDEK: generated.EncryptedDEK,
		Label:        label,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store wallet")
		return
	}

	walletID := stored.ID
	h.store.WriteAuditLog(r.Context(), project.ID, &walletID, nil, "wallet.create", project.ID, map[string]any{ //nolint:errcheck
		"chain_type": req.ChainType,
		"address":    generated.Address,
	})

	writeJSON(w, http.StatusCreated, walletResponse{
		ID:        stored.ID,
		UserID:    stored.UserID,
		ChainType: stored.ChainType,
		Address:   stored.Address,
		Label:     stored.Label,
		CreatedAt: stored.CreatedAt.String(),
	})
}

func (h *Handler) GetWallet(w http.ResponseWriter, r *http.Request) {
	project := middleware.ProjectFromContext(r.Context())
	wlt, err := h.store.GetWalletByID(r.Context(), r.PathValue("walletId"), project.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if wlt == nil {
		writeError(w, http.StatusNotFound, "wallet not found")
		return
	}
	writeJSON(w, http.StatusOK, walletResponse{
		ID: wlt.ID, UserID: wlt.UserID, ChainType: wlt.ChainType,
		Address: wlt.Address, Label: wlt.Label, CreatedAt: wlt.CreatedAt.String(),
	})
}

func (h *Handler) ListUserWallets(w http.ResponseWriter, r *http.Request) {
	project := middleware.ProjectFromContext(r.Context())
	wallets, err := h.store.ListWalletsByUserID(r.Context(), project.ID, r.PathValue("userId"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	resp := make([]walletResponse, 0, len(wallets))
	for _, wlt := range wallets {
		resp = append(resp, walletResponse{
			ID: wlt.ID, UserID: wlt.UserID, ChainType: wlt.ChainType,
			Address: wlt.Address, Label: wlt.Label, CreatedAt: wlt.CreatedAt.String(),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"wallets": resp})
}

// ── Async Signing ─────────────────────────────────────────────────────────────

type submitJobRequest struct {
	IdempotencyKey string          `json:"idempotency_key"`
	Gasless        bool            `json:"gasless"`
	Payload        json.RawMessage `json:"payload"`
}

type submitJobResponse struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}

func (h *Handler) SubmitSignEVMTransaction(w http.ResponseWriter, r *http.Request) {
	h.submitJob(w, r, "sign_tx_evm")
}

func (h *Handler) SubmitSignEVMMessage(w http.ResponseWriter, r *http.Request) {
	h.submitJob(w, r, "sign_msg_evm")
}

func (h *Handler) SubmitSignSolanaTransaction(w http.ResponseWriter, r *http.Request) {
	h.submitJob(w, r, "sign_tx_solana")
}

func (h *Handler) SubmitSignSolanaMessage(w http.ResponseWriter, r *http.Request) {
	h.submitJob(w, r, "sign_msg_solana")
}

func (h *Handler) submitJob(w http.ResponseWriter, r *http.Request, operation string) {
	project := middleware.ProjectFromContext(r.Context())
	walletID := r.PathValue("walletId")

	wlt, err := h.store.GetWalletByID(r.Context(), walletID, project.ID)
	if err != nil || wlt == nil {
		writeError(w, http.StatusNotFound, "wallet not found")
		return
	}

	var req submitJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Payload) == 0 {
		writeError(w, http.StatusBadRequest, "payload is required")
		return
	}

	var idempotencyKey *string
	if req.IdempotencyKey != "" {
		idempotencyKey = &req.IdempotencyKey
	}

	// Persist the job first - queue is derived from DB, not the other way around
	job, err := h.store.CreateSigningJob(r.Context(), project.ID, wlt.ID, operation, req.Payload, idempotencyKey, req.Gasless)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create job")
		return
	}

	// If job was deduplicated (idempotency key already exists), return existing job
	if job.Status == "completed" || job.Status == "processing" {
		writeJSON(w, http.StatusOK, submitJobResponse{JobID: job.ID, Status: job.Status})
		return
	}

	// Enqueue for async processing
	if err := h.queue.Enqueue(r.Context(), queue.Job{
		ID:        job.ID,
		ProjectID: project.ID,
		WalletID:  wlt.ID,
		Operation: operation,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to enqueue job")
		return
	}

	writeJSON(w, http.StatusAccepted, submitJobResponse{JobID: job.ID, Status: "pending"})
}

// GetJob returns the current status of a signing job.
func (h *Handler) GetJob(w http.ResponseWriter, r *http.Request) {
	project := middleware.ProjectFromContext(r.Context())
	job, err := h.store.GetSigningJob(r.Context(), r.PathValue("jobId"), project.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if job == nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// ── Balance + Broadcast ───────────────────────────────────────────────────────

func (h *Handler) GetBalance(w http.ResponseWriter, r *http.Request) {
	project := middleware.ProjectFromContext(r.Context())
	wlt, err := h.store.GetWalletByID(r.Context(), r.PathValue("walletId"), project.ID)
	if err != nil || wlt == nil {
		writeError(w, http.StatusNotFound, "wallet not found")
		return
	}

	switch wlt.ChainType {
	case "evm":
		chainID := r.URL.Query().Get("chain_id")
		if chainID == "" {
			writeError(w, http.StatusBadRequest, "chain_id query param required for EVM wallets")
			return
		}
		balance, err := h.rpcMgr.EVMBalance(r.Context(), chainID, wlt.Address)
		if err != nil {
			writeError(w, http.StatusBadGateway, "failed to fetch balance: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"address": wlt.Address, "balance": balance, "chain_id": chainID, "unit": "wei"})

	case "solana":
		balance, err := h.rpcMgr.SolanaBalance(r.Context(), wlt.Address)
		if err != nil {
			writeError(w, http.StatusBadGateway, "failed to fetch balance: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"address": wlt.Address, "balance": balance, "unit": "lamports"})
	}
}

type broadcastRequest struct {
	SignedTx string `json:"signed_tx"`
	ChainID  string `json:"chain_id"`
}

func (h *Handler) Broadcast(w http.ResponseWriter, r *http.Request) {
	project := middleware.ProjectFromContext(r.Context())
	wlt, err := h.store.GetWalletByID(r.Context(), r.PathValue("walletId"), project.ID)
	if err != nil || wlt == nil {
		writeError(w, http.StatusNotFound, "wallet not found")
		return
	}

	var req broadcastRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SignedTx == "" {
		writeError(w, http.StatusBadRequest, "signed_tx is required")
		return
	}

	switch wlt.ChainType {
	case "evm":
		if req.ChainID == "" {
			writeError(w, http.StatusBadRequest, "chain_id is required for EVM")
			return
		}
		txHash, err := h.rpcMgr.EVMBroadcast(r.Context(), req.ChainID, req.SignedTx)
		if err != nil {
			writeError(w, http.StatusBadGateway, "broadcast failed: "+err.Error())
			return
		}
		walletID := wlt.ID
		h.store.WriteAuditLog(r.Context(), project.ID, &walletID, nil, "wallet.broadcast", project.ID, map[string]any{"chain_id": req.ChainID, "tx_hash": txHash}) //nolint:errcheck
		writeJSON(w, http.StatusOK, map[string]string{"tx_hash": txHash})

	case "solana":
		sig, err := h.rpcMgr.SolanaBroadcast(r.Context(), req.SignedTx)
		if err != nil {
			writeError(w, http.StatusBadGateway, "broadcast failed: "+err.Error())
			return
		}
		walletID := wlt.ID
		h.store.WriteAuditLog(r.Context(), project.ID, &walletID, nil, "wallet.broadcast", project.ID, map[string]any{"chain": "solana", "signature": sig}) //nolint:errcheck
		writeJSON(w, http.StatusOK, map[string]string{"signature": sig})
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func generateToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
