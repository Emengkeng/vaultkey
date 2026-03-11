// package handlers

// import (
// 	"encoding/json"
// 	"net/http"

// 	"github.com/vaultkey/vaultkey/internal/rpc"
// 	"github.com/vaultkey/vaultkey/internal/storage"
// )

// type RPCHandler struct {
// 	store   *storage.Store
// 	rpcMgr  *rpc.Manager
// }

// func NewRPCHandler(store *storage.Store, rpcMgr *rpc.Manager) *RPCHandler {
// 	return &RPCHandler{store: store, rpcMgr: rpcMgr}
// }

// func (h *RPCHandler) GetBalance(w http.ResponseWriter, r *http.Request) {
// 	project := ProjectFromContext(r)
// 	walletID := r.PathValue("walletId")
// 	chainID := r.URL.Query().Get("chain_id")

// 	wlt, err := h.store.GetWalletByID(r.Context(), walletID, project.ID)
// 	if err != nil || wlt == nil {
// 		writeError(w, http.StatusNotFound, "wallet not found")
// 		return
// 	}

// 	switch wlt.ChainType {
// 	case "evm":
// 		if chainID == "" {
// 			writeError(w, http.StatusBadRequest, "chain_id query param required for EVM wallets")
// 			return
// 		}
// 		balance, err := h.rpcMgr.EVMBalance(r.Context(), chainID, wlt.Address)
// 		if err != nil {
// 			writeError(w, http.StatusBadGateway, "failed to fetch balance: "+err.Error())
// 			return
// 		}
// 		writeJSON(w, http.StatusOK, map[string]string{
// 			"address":  wlt.Address,
// 			"balance":  balance,
// 			"chain_id": chainID,
// 			"unit":     "wei",
// 		})

// 	case "solana":
// 		balance, err := h.rpcMgr.SolanaBalance(r.Context(), wlt.Address)
// 		if err != nil {
// 			writeError(w, http.StatusBadGateway, "failed to fetch balance: "+err.Error())
// 			return
// 		}
// 		writeJSON(w, http.StatusOK, map[string]any{
// 			"address": wlt.Address,
// 			"balance": balance,
// 			"unit":    "lamports",
// 		})
// 	}
// }

// type broadcastRequest struct {
// 	SignedTx string `json:"signed_tx"` // hex for EVM, base64 for Solana
// 	ChainID  string `json:"chain_id"`  // required for EVM
// }

// func (h *RPCHandler) Broadcast(w http.ResponseWriter, r *http.Request) {
// 	project := ProjectFromContext(r)
// 	walletID := r.PathValue("walletId")

// 	wlt, err := h.store.GetWalletByID(r.Context(), walletID, project.ID)
// 	if err != nil || wlt == nil {
// 		writeError(w, http.StatusNotFound, "wallet not found")
// 		return
// 	}

// 	var req broadcastRequest
// 	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SignedTx == "" {
// 		writeError(w, http.StatusBadRequest, "signed_tx is required")
// 		return
// 	}

// 	switch wlt.ChainType {
// 	case "evm":
// 		if req.ChainID == "" {
// 			writeError(w, http.StatusBadRequest, "chain_id is required for EVM broadcast")
// 			return
// 		}
// 		txHash, err := h.rpcMgr.EVMBroadcast(r.Context(), req.ChainID, req.SignedTx)
// 		if err != nil {
// 			writeError(w, http.StatusBadGateway, "broadcast failed: "+err.Error())
// 			return
// 		}
// 		h.store.WriteAuditLog(r.Context(), project.ID, wlt.ID, "wallet.broadcast", project.ID, map[string]any{ //nolint:errcheck
// 			"chain_id": req.ChainID,
// 			"tx_hash":  txHash,
// 		})
// 		writeJSON(w, http.StatusOK, map[string]string{"tx_hash": txHash})

// 	case "solana":
// 		sig, err := h.rpcMgr.SolanaBroadcast(r.Context(), req.SignedTx)
// 		if err != nil {
// 			writeError(w, http.StatusBadGateway, "broadcast failed: "+err.Error())
// 			return
// 		}
// 		h.store.WriteAuditLog(r.Context(), project.ID, wlt.ID, "wallet.broadcast", project.ID, map[string]any{ //nolint:errcheck
// 			"chain":     "solana",
// 			"signature": sig,
// 		})
// 		writeJSON(w, http.StatusOK, map[string]string{"signature": sig})
// 	}
// }

// func ProjectFromContext(r *http.Request) *storage.Project {
// 	p, _ := r.Context().Value("project").(*storage.Project)
// 	return p
// }
