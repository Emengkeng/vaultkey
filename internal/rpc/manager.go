package rpc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Manager struct {
	evmEndpoints   map[string]string
	solanaEndpoint string
	client         *http.Client
}

func NewManager(evmEndpoints map[string]string, solanaEndpoint string) *Manager {
	return &Manager{
		evmEndpoints:   evmEndpoints,
		solanaEndpoint: solanaEndpoint,
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// EVMBalance returns the balance of an address on a given chain in wei (hex string).
func (m *Manager) EVMBalance(ctx context.Context, chainID, address string) (string, error) {
	endpoint, ok := m.evmEndpoints[chainID]
	if !ok {
		return "", fmt.Errorf("no RPC endpoint configured for chain %s", chainID)
	}

	result, err := m.jsonRPC(ctx, endpoint, "eth_getBalance", []any{address, "latest"})
	if err != nil {
		return "", fmt.Errorf("eth_getBalance: %w", err)
	}

	var balance string
	if err := json.Unmarshal(result, &balance); err != nil {
		return "", fmt.Errorf("parse balance: %w", err)
	}

	return balance, nil
}

// EVMBroadcast broadcasts a signed raw transaction to the given chain.
func (m *Manager) EVMBroadcast(ctx context.Context, chainID, signedTxHex string) (string, error) {
	endpoint, ok := m.evmEndpoints[chainID]
	if !ok {
		return "", fmt.Errorf("no RPC endpoint configured for chain %s", chainID)
	}

	result, err := m.jsonRPC(ctx, endpoint, "eth_sendRawTransaction", []any{signedTxHex})
	if err != nil {
		return "", fmt.Errorf("eth_sendRawTransaction: %w", err)
	}

	var txHash string
	if err := json.Unmarshal(result, &txHash); err != nil {
		return "", fmt.Errorf("parse tx hash: %w", err)
	}

	return txHash, nil
}

// SolanaBalance returns the SOL balance of an address in lamports.
func (m *Manager) SolanaBalance(ctx context.Context, address string) (uint64, error) {
	result, err := m.jsonRPC(ctx, m.solanaEndpoint, "getBalance", []any{address})
	if err != nil {
		return 0, fmt.Errorf("getBalance: %w", err)
	}

	var resp struct {
		Value uint64 `json:"value"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return 0, fmt.Errorf("parse solana balance: %w", err)
	}

	return resp.Value, nil
}

// SolanaBroadcast sends a signed Solana transaction.
func (m *Manager) SolanaBroadcast(ctx context.Context, signedTxBase64 string) (string, error) {
	result, err := m.jsonRPC(ctx, m.solanaEndpoint, "sendTransaction", []any{
		signedTxBase64,
		map[string]string{"encoding": "base64"},
	})
	if err != nil {
		return "", fmt.Errorf("sendTransaction: %w", err)
	}

	var signature string
	if err := json.Unmarshal(result, &signature); err != nil {
		return "", fmt.Errorf("parse solana signature: %w", err)
	}

	return signature, nil
}

func (m *Manager) jsonRPC(ctx context.Context, endpoint, method string, params []any) (json.RawMessage, error) {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rpc request to %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var rpcResp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, fmt.Errorf("parse rpc response: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	return rpcResp.Result, nil
}

// EVMPendingNonce returns the next nonce to use for the given address.
func (m *Manager) EVMPendingNonce(ctx context.Context, chainID, address string) (uint64, error) {
	endpoint, ok := m.evmEndpoints[chainID]
	if !ok {
		return 0, fmt.Errorf("no RPC endpoint configured for chain %s", chainID)
	}

	result, err := m.jsonRPC(ctx, endpoint, "eth_getTransactionCount", []any{address, "pending"})
	if err != nil {
		return 0, fmt.Errorf("eth_getTransactionCount: %w", err)
	}

	var hexNonce string
	if err := json.Unmarshal(result, &hexNonce); err != nil {
		return 0, fmt.Errorf("parse nonce: %w", err)
	}

	var nonce uint64
	fmt.Sscanf(strings.TrimPrefix(hexNonce, "0x"), "%x", &nonce)
	return nonce, nil
}

// EVMGasPrice returns the current gas price as a hex string.
func (m *Manager) EVMGasPrice(ctx context.Context, chainID string) (string, error) {
	endpoint, ok := m.evmEndpoints[chainID]
	if !ok {
		return "", fmt.Errorf("no RPC endpoint configured for chain %s", chainID)
	}

	result, err := m.jsonRPC(ctx, endpoint, "eth_gasPrice", []any{})
	if err != nil {
		return "", fmt.Errorf("eth_gasPrice: %w", err)
	}

	var gasPrice string
	if err := json.Unmarshal(result, &gasPrice); err != nil {
		return "", fmt.Errorf("parse gas price: %w", err)
	}

	return gasPrice, nil
}

// SolanaRecentBlockhash fetches a recent blockhash required for transaction building.
func (m *Manager) SolanaRecentBlockhash(ctx context.Context) (string, error) {
	result, err := m.jsonRPC(ctx, m.solanaEndpoint, "getLatestBlockhash", []any{
		map[string]string{"commitment": "finalized"},
	})
	if err != nil {
		return "", fmt.Errorf("getLatestBlockhash: %w", err)
	}

	var resp struct {
		Value struct {
			Blockhash string `json:"blockhash"`
		} `json:"value"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return "", fmt.Errorf("parse blockhash: %w", err)
	}
	if resp.Value.Blockhash == "" {
		return "", fmt.Errorf("empty blockhash returned")
	}
	return resp.Value.Blockhash, nil
}

// SolanaBroadcastRaw sends a fully-signed binary Solana transaction.
// It base64-encodes the bytes and passes them to the existing SolanaBroadcast method.
func (m *Manager) SolanaBroadcastRaw(ctx context.Context, signedTx []byte) (string, error) {
	encoded := base64.StdEncoding.EncodeToString(signedTx)
	return m.SolanaBroadcast(ctx, encoded)
}
