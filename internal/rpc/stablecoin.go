package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// EVMCall executes a read-only eth_call (no gas, no state change).
// Used for ERC-20 balanceOf calls.
//
// contractAddr: "0x..." token contract
// calldata:     hex-encoded ABI call, e.g. "0x70a08231000...{address}"
//
// Returns the raw hex result from the node, e.g. "0x0000...{uint256}".
func (m *Manager) EVMCall(ctx context.Context, chainID, contractAddr, calldata string) (string, error) {
	endpoint, ok := m.evmEndpoints[chainID]
	if !ok {
		return "", fmt.Errorf("no RPC endpoint configured for chain %s", chainID)
	}

	params := []any{
		map[string]string{
			"to":   contractAddr,
			"data": calldata,
		},
		"latest",
	}

	result, err := m.jsonRPC(ctx, endpoint, "eth_call", params)
	if err != nil {
		return "", fmt.Errorf("eth_call: %w", err)
	}

	var hexResult string
	if err := json.Unmarshal(result, &hexResult); err != nil {
		return "", fmt.Errorf("parse eth_call result: %w", err)
	}

	return hexResult, nil
}

// SolanaTokenBalance returns the SPL token balance for a wallet address
// and a given mint. Returns the raw amount in base units (no decimals applied).
//
// It uses getTokenAccountsByOwner to find the associated token account,
// then reads the balance. Returns 0 if no token account exists (wallet
// has never received this token).
func (m *Manager) SolanaTokenBalance(ctx context.Context, walletAddress, mintAddress string) (uint64, error) {
	// getTokenAccountsByOwner returns all token accounts for this wallet+mint.
	result, err := m.jsonRPC(ctx, m.solanaEndpoint, "getTokenAccountsByOwner", []any{
		walletAddress,
		map[string]string{"mint": mintAddress},
		map[string]string{"encoding": "jsonParsed"},
	})
	if err != nil {
		return 0, fmt.Errorf("getTokenAccountsByOwner: %w", err)
	}

	var resp struct {
		Value []struct {
			Account struct {
				Data struct {
					Parsed struct {
						Info struct {
							TokenAmount struct {
								Amount string `json:"amount"`
							} `json:"tokenAmount"`
						} `json:"info"`
					} `json:"parsed"`
				} `json:"data"`
			} `json:"account"`
		} `json:"value"`
	}

	if err := json.Unmarshal(result, &resp); err != nil {
		return 0, fmt.Errorf("parse token accounts: %w", err)
	}

	// No token account = zero balance. This is normal for wallets that have
	// never received this token. Not an error.
	if len(resp.Value) == 0 {
		return 0, nil
	}

	// Sum across all token accounts for this mint (rare to have multiple, but possible).
	var total uint64
	for _, acct := range resp.Value {
		amtStr := strings.TrimSpace(acct.Account.Data.Parsed.Info.TokenAmount.Amount)
		if amtStr == "" || amtStr == "0" {
			continue
		}
		var amt uint64
		if _, err := fmt.Sscanf(amtStr, "%d", &amt); err != nil {
			continue // skip malformed entries
		}
		total += amt
	}

	return total, nil
}