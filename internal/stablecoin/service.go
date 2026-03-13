package stablecoin

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strings"

	"github.com/vaultkey/vaultkey/internal/queue"
	"github.com/vaultkey/vaultkey/internal/rpc"
	"github.com/vaultkey/vaultkey/internal/storage"
)

// Minimum relayer balances — mirrors the constants in the relayer package
// so we can check health before creating any job.
const (
	minEVMRelayerBalanceHex  = "0x0B1A2BC2EC50000" // 0.05 ETH in wei
	minSolanaRelayerLamports = uint64(50_000_000)   // 0.05 SOL
)

// evmAddressRe matches a valid EVM hex address (with or without 0x prefix).
var evmAddressRe = regexp.MustCompile(`(?i)^(0x)?[0-9a-f]{40}$`)

// solanaAddressRe matches a base58 Solana address (32-44 chars, base58 alphabet).
var solanaAddressRe = regexp.MustCompile(`^[1-9A-HJ-NP-Za-km-z]{32,44}$`)

// Service handles stablecoin transfers. It translates human-friendly
// transfer requests into signing jobs using the existing queue and worker
// infrastructure. Nothing below the handler layer changes.
type Service struct {
	store    *storage.Store
	rpcMgr   *rpc.Manager
	queue    *queue.Queue
	registry *Registry
}

func NewService(store *storage.Store, rpcMgr *rpc.Manager, q *queue.Queue, registry *Registry) *Service {
	return &Service{
		store:    store,
		rpcMgr:   rpcMgr,
		queue:    q,
		registry: registry,
	}
}

// TransferRequest is the input for a stablecoin transfer.
type TransferRequest struct {
	ProjectID      string
	WalletID       string
	Token          string  // "usdc", "usdt", or any registered token
	To             string  // recipient address (EVM hex or Solana base58)
	Amount         string  // human-readable, e.g. "50.00"
	ChainID        string  // required for EVM, empty for Solana
	ChainType      string  // "evm" or "solana"
	Gasless        bool    // EVM only — Solana is always gasless
	IdempotencyKey *string
}

// TransferResult is returned after the job is enqueued.
type TransferResult struct {
	JobID  string
	Status string
}

// Transfer validates every precondition, then enqueues a signing job.
//
// Validation order (cheap checks first, network calls last):
//  1. Token format valid
//  2. Chain type valid
//  3. Gasless flag consistent with chain (Solana always requires gasless)
//  4. Recipient address format matches chain type
//  5. Wallet exists, belongs to project, chain type matches
//  6. Wallet is not a master wallet
//  7. Token registered and enabled for this chain (DB/cache)
//  8. Amount parseable and > 0
//  9. Relayer configured (if gasless or Solana)
// 10. Relayer balance healthy (above minimum threshold)
// 11. Sender token balance > 0
// 12. Sender token balance >= requested amount
func (s *Service) Transfer(ctx context.Context, req TransferRequest) (*TransferResult, error) {
	// ── 1. Token format ───────────────────────────────────────
	token, err := ParseToken(req.Token)
	if err != nil {
		return nil, err
	}

	// ── 2. Chain type ─────────────────────────────────────────
	if req.ChainType != "evm" && req.ChainType != "solana" {
		return nil, fmt.Errorf("unsupported chain_type %q — must be 'evm' or 'solana'", req.ChainType)
	}

	// ── 3. Gasless flag ───────────────────────────────────────
	// Solana SPL transfers always require a fee payer with SOL. The user wallet
	// holds stablecoins not SOL, so gasless is the only viable mode.
	if req.ChainType == "solana" && !req.Gasless {
		return nil, fmt.Errorf("Solana stablecoin transfers require gasless=true — " +
			"the user wallet holds no SOL for fees, the relayer must pay")
	}

	// ── 4. Recipient address format ───────────────────────────
	if err := validateRecipientAddress(req.To, req.ChainType); err != nil {
		return nil, err
	}

	// ── 5. Wallet ─────────────────────────────────────────────
	wlt, err := s.store.GetWalletByID(ctx, req.WalletID, req.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("fetch wallet: %w", err)
	}
	if wlt == nil {
		return nil, fmt.Errorf("wallet not found")
	}
	if wlt.ChainType != req.ChainType {
		return nil, fmt.Errorf("wallet is a %s wallet but request specifies chain_type=%q — "+
			"use a %s wallet for this transfer", wlt.ChainType, req.ChainType, req.ChainType)
	}

	// ── 6. Master wallet guard ────────────────────────────────
	if wlt.IsMaster {
		return nil, fmt.Errorf("master wallets cannot initiate stablecoin transfers — " +
			"they are sweep destinations only")
	}

	// ── 7. Token registered for this chain ────────────────────
	// Registry hits Redis first, then Postgres. Returns a clear error if the
	// token hasn't been seeded for this environment/chain.
	var decimals int
	switch req.ChainType {
	case "evm":
		entry, err := s.registry.ResolveEVM(ctx, token, req.ChainID)
		if err != nil {
			return nil, err
		}
		decimals = entry.Decimals
	case "solana":
		entry, err := s.registry.ResolveSolana(ctx, token)
		if err != nil {
			return nil, err
		}
		decimals = entry.Decimals
	}

	// ── 8. Amount ─────────────────────────────────────────────
	requestedAmount, err := ParseAmount(req.Amount, decimals)
	if err != nil {
		return nil, fmt.Errorf("invalid amount: %w", err)
	}

	// ── 9. Relayer configured ─────────────────────────────────
	// EVM: only required when gasless=true.
	// Solana: always required (step 3 already enforces gasless=true for Solana).
	needsRelayer := req.Gasless || req.ChainType == "solana"
	if needsRelayer {
		if err := s.assertRelayerConfigured(ctx, req.ProjectID, req.ChainType, req.ChainID); err != nil {
			return nil, err
		}

		// ── 10. Relayer balance healthy ───────────────────────
		if err := s.assertRelayerHealthy(ctx, req.ProjectID, req.ChainType, req.ChainID); err != nil {
			return nil, err
		}
	}

	// ── 11 & 12. Sender token balance ─────────────────────────
	// Always check — no point enqueuing a job that will fail on-chain.
	// Fetch once, validate twice (non-zero, then sufficient).
	senderBalance, err := s.fetchTokenBalance(ctx, wlt, token, req.ChainType, req.ChainID)
	if err != nil {
		return nil, fmt.Errorf("fetch sender token balance: %w", err)
	}
	if senderBalance.Sign() == 0 {
		return nil, fmt.Errorf("wallet has zero %s balance — top it up before transferring",
			strings.ToUpper(string(token)))
	}
	if senderBalance.Cmp(requestedAmount) < 0 {
		return nil, fmt.Errorf("insufficient %s balance: wallet has %s but transfer requires %s",
			strings.ToUpper(string(token)),
			FormatAmount(senderBalance, decimals),
			FormatAmount(requestedAmount, decimals))
	}

	// ── All checks passed — build payload and enqueue ─────────
	var (
		operation string
		payload   json.RawMessage
	)
	switch req.ChainType {
	case "evm":
		operation, payload, err = s.buildEVMPayload(ctx, token, req)
	case "solana":
		operation, payload, err = s.buildSolanaPayload(ctx, token, req)
	}
	if err != nil {
		return nil, err
	}

	job, err := s.store.CreateSigningJob(ctx, req.ProjectID, req.WalletID, operation, payload, req.IdempotencyKey, req.Gasless)
	if err != nil {
		return nil, fmt.Errorf("create signing job: %w", err)
	}

	// Idempotency hit — job already in flight or done.
	if job.Status == "completed" || job.Status == "processing" {
		return &TransferResult{JobID: job.ID, Status: job.Status}, nil
	}

	if err := s.queue.Enqueue(ctx, queue.Job{
		ID:        job.ID,
		ProjectID: req.ProjectID,
		WalletID:  req.WalletID,
		Operation: operation,
	}); err != nil {
		return nil, fmt.Errorf("enqueue job: %w", err)
	}

	return &TransferResult{JobID: job.ID, Status: "pending"}, nil
}

// ── Precondition helpers ──────────────────────────────────────────────────────

func validateRecipientAddress(addr, chainType string) error {
	if addr == "" {
		return fmt.Errorf("recipient address (to) is required")
	}
	switch chainType {
	case "evm":
		if !evmAddressRe.MatchString(addr) {
			return fmt.Errorf("invalid EVM address %q — expected 0x-prefixed 40 hex characters", addr)
		}
	case "solana":
		if !solanaAddressRe.MatchString(addr) {
			return fmt.Errorf("invalid Solana address %q — expected base58-encoded public key (32-44 characters)", addr)
		}
	}
	return nil
}

func (s *Service) assertRelayerConfigured(ctx context.Context, projectID, chainType, chainID string) error {
	rw, err := s.store.GetRelayerWallet(ctx, projectID, chainType, chainID)
	if err != nil {
		return fmt.Errorf("check relayer: %w", err)
	}
	if rw == nil {
		switch chainType {
		case "solana":
			return fmt.Errorf("no Solana relayer configured — Solana stablecoin transfers always require " +
				"a relayer to pay fees, register one at POST /projects/relayer")
		default:
			return fmt.Errorf("no relayer configured for EVM chain %s — gasless transfers require "+
				"a relayer to pay gas, register one at POST /projects/relayer", chainID)
		}
	}
	return nil
}

func (s *Service) assertRelayerHealthy(ctx context.Context, projectID, chainType, chainID string) error {
	rw, err := s.store.GetRelayerWallet(ctx, projectID, chainType, chainID)
	if err != nil || rw == nil {
		// assertRelayerConfigured already ran — this is a guard only.
		return fmt.Errorf("relayer wallet unexpectedly missing during health check")
	}

	switch chainType {
	case "evm":
		balance, err := s.rpcMgr.EVMBalance(ctx, chainID, rw.Address)
		if err != nil {
			return fmt.Errorf("fetch relayer balance: %w", err)
		}
		bal := hexToBigInt(balance)
		min := hexToBigInt(minEVMRelayerBalanceHex)
		if bal.Cmp(min) < 0 {
			return fmt.Errorf("relayer wallet %s has insufficient gas balance — "+
				"has %s wei, needs at least 0.05 ETH equivalent, top it up before sending gasless transactions",
				rw.Address, bal.String())
		}

	case "solana":
		balance, err := s.rpcMgr.SolanaBalance(ctx, rw.Address)
		if err != nil {
			return fmt.Errorf("fetch relayer balance: %w", err)
		}
		if balance < minSolanaRelayerLamports {
			return fmt.Errorf("relayer wallet %s has insufficient SOL balance — "+
				"has %d lamports, needs at least 0.05 SOL, top it up before sending stablecoin transfers",
				rw.Address, balance)
		}
	}

	return nil
}

func (s *Service) fetchTokenBalance(ctx context.Context, wlt *storage.Wallet, token Token, chainType, chainID string) (*big.Int, error) {
	switch chainType {
	case "evm":
		entry, err := s.registry.ResolveEVM(ctx, token, chainID)
		if err != nil {
			return nil, err
		}
		addrHex := strings.TrimPrefix(strings.ToLower(wlt.Address), "0x")
		calldata := "0x70a08231" + strings.Repeat("0", 24) + addrHex
		rawBalance, err := s.rpcMgr.EVMCall(ctx, chainID, entry.ContractAddress, calldata)
		if err != nil {
			return nil, err
		}
		rawHex := strings.TrimPrefix(rawBalance, "0x")
		if rawHex == "" {
			return big.NewInt(0), nil
		}
		bal := new(big.Int)
		bal.SetString(rawHex, 16)
		return bal, nil

	case "solana":
		entry, err := s.registry.ResolveSolana(ctx, token)
		if err != nil {
			return nil, err
		}
		rawBalance, err := s.rpcMgr.SolanaTokenBalance(ctx, wlt.Address, entry.MintAddress)
		if err != nil {
			return nil, err
		}
		return new(big.Int).SetUint64(rawBalance), nil
	}

	return nil, fmt.Errorf("unsupported chain_type %q", chainType)
}

// ── Payload builders ──────────────────────────────────────────────────────────

func (s *Service) buildEVMPayload(ctx context.Context, token Token, req TransferRequest) (string, json.RawMessage, error) {
	entry, err := s.registry.ResolveEVM(ctx, token, req.ChainID)
	if err != nil {
		return "", nil, err
	}

	amount, err := ParseAmount(req.Amount, entry.Decimals)
	if err != nil {
		return "", nil, fmt.Errorf("parse amount: %w", err)
	}

	calldata, err := EncodeERC20Transfer(req.To, amount)
	if err != nil {
		return "", nil, fmt.Errorf("encode transfer: %w", err)
	}

	chainIDInt, err := parseChainIDInt(req.ChainID)
	if err != nil {
		return "", nil, err
	}

	// ERC-20 transfers: value is always 0x0, data carries the calldata.
	// The destination is the token contract, not the recipient —
	// the recipient is encoded inside the calldata by EncodeERC20Transfer.
	txPayload := evmTxPayload{
		To:       entry.ContractAddress,
		Value:    "0x0",
		Data:     calldata,
		ChainID:  chainIDInt,
		GasLimit: GasLimitERC20Transfer,
		StablecoinMeta: &stablecoinMeta{
			Token:   string(token),
			Symbol:  entry.Symbol,
			To:      req.To,
			Amount:  req.Amount,
			ChainID: req.ChainID,
		},
	}

	raw, err := json.Marshal(txPayload)
	if err != nil {
		return "", nil, err
	}
	return "sign_tx_evm", raw, nil
}

func (s *Service) buildSolanaPayload(ctx context.Context, token Token, req TransferRequest) (string, json.RawMessage, error) {
	entry, err := s.registry.ResolveSolana(ctx, token)
	if err != nil {
		return "", nil, err
	}

	amount, err := ParseAmount(req.Amount, entry.Decimals)
	if err != nil {
		return "", nil, fmt.Errorf("parse amount: %w", err)
	}

	solPayload := solanaTokenPayload{
		To:        req.To,
		Amount:    amount.Uint64(),
		TokenMint: entry.MintAddress,
		Gasless:   true, // always — enforced at step 3 above
		StablecoinMeta: &stablecoinMeta{
			Token:  string(token),
			Symbol: entry.Symbol,
			To:     req.To,
			Amount: req.Amount,
		},
	}

	raw, err := json.Marshal(solPayload)
	if err != nil {
		return "", nil, err
	}
	return "sign_tx_solana", raw, nil
}

// ── Balance ───────────────────────────────────────────────────────────────────

// BalanceRequest is the input for a stablecoin balance check.
type BalanceRequest struct {
	ProjectID string
	WalletID  string
	Token     string
	ChainID   string // empty for Solana
	ChainType string
}

// BalanceResult holds the token balance response.
type BalanceResult struct {
	Address    string `json:"address"`
	Token      string `json:"token"`
	Symbol     string `json:"symbol"`
	Balance    string `json:"balance"`     // human-readable, e.g. "50.00"
	RawBalance string `json:"raw_balance"` // base units, for precision-sensitive callers
	ChainID    string `json:"chain_id,omitempty"`
}

// Balance fetches the stablecoin token balance for a wallet.
func (s *Service) Balance(ctx context.Context, req BalanceRequest) (*BalanceResult, error) {
	token, err := ParseToken(req.Token)
	if err != nil {
		return nil, err
	}

	wlt, err := s.store.GetWalletByID(ctx, req.WalletID, req.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("fetch wallet: %w", err)
	}
	if wlt == nil {
		return nil, fmt.Errorf("wallet not found")
	}
	if wlt.ChainType != req.ChainType {
		return nil, fmt.Errorf("wallet is a %s wallet, cannot check %s balance", wlt.ChainType, req.ChainType)
	}

	switch req.ChainType {
	case "evm":
		return s.evmBalance(ctx, wlt, token, req.ChainID)
	case "solana":
		return s.solanaBalance(ctx, wlt, token)
	default:
		return nil, fmt.Errorf("unsupported chain_type %q", req.ChainType)
	}
}

func (s *Service) evmBalance(ctx context.Context, wlt *storage.Wallet, token Token, chainID string) (*BalanceResult, error) {
	entry, err := s.registry.ResolveEVM(ctx, token, chainID)
	if err != nil {
		return nil, err
	}

	addrHex := strings.TrimPrefix(strings.ToLower(wlt.Address), "0x")
	calldata := "0x70a08231" + strings.Repeat("0", 24) + addrHex

	rawBalance, err := s.rpcMgr.EVMCall(ctx, chainID, entry.ContractAddress, calldata)
	if err != nil {
		return nil, fmt.Errorf("balanceOf call failed: %w", err)
	}

	rawHex := strings.TrimPrefix(rawBalance, "0x")
	if rawHex == "" {
		rawHex = "0"
	}
	balBig := new(big.Int)
	balBig.SetString(rawHex, 16)

	return &BalanceResult{
		Address:    wlt.Address,
		Token:      string(token),
		Symbol:     entry.Symbol,
		Balance:    FormatAmount(balBig, entry.Decimals),
		RawBalance: balBig.String(),
		ChainID:    chainID,
	}, nil
}

func (s *Service) solanaBalance(ctx context.Context, wlt *storage.Wallet, token Token) (*BalanceResult, error) {
	entry, err := s.registry.ResolveSolana(ctx, token)
	if err != nil {
		return nil, err
	}

	rawBalance, err := s.rpcMgr.SolanaTokenBalance(ctx, wlt.Address, entry.MintAddress)
	if err != nil {
		return nil, fmt.Errorf("token balance fetch failed: %w", err)
	}

	balBig := new(big.Int).SetUint64(rawBalance)

	return &BalanceResult{
		Address:    wlt.Address,
		Token:      string(token),
		Symbol:     entry.Symbol,
		Balance:    FormatAmount(balBig, entry.Decimals),
		RawBalance: balBig.String(),
	}, nil
}

// ── Internal payload shapes ───────────────────────────────────────────────────

type stablecoinMeta struct {
	Token   string `json:"_sc_token"`
	Symbol  string `json:"_sc_symbol"`
	To      string `json:"_sc_to"`
	Amount  string `json:"_sc_amount"`
	ChainID string `json:"_sc_chain_id,omitempty"`
}

type evmTxPayload struct {
	To             string          `json:"to"`
	Value          string          `json:"value"`
	Data           string          `json:"data"`
	ChainID        int64           `json:"chain_id"`
	GasLimit       uint64          `json:"gas_limit"`
	GasPrice       string          `json:"gas_price,omitempty"`
	StablecoinMeta *stablecoinMeta `json:"_stablecoin,omitempty"`
}

type solanaTokenPayload struct {
	To             string          `json:"to"`
	Amount         uint64          `json:"amount"`
	TokenMint      string          `json:"token_mint"`
	Gasless        bool            `json:"gasless"`
	StablecoinMeta *stablecoinMeta `json:"_stablecoin,omitempty"`
}

// ── Utilities ─────────────────────────────────────────────────────────────────

func hexToBigInt(h string) *big.Int {
	n := new(big.Int)
	n.SetString(strings.TrimPrefix(h, "0x"), 16)
	return n
}

func parseChainIDInt(s string) (int64, error) {
	var id int64
	_, err := fmt.Sscanf(s, "%d", &id)
	if err != nil {
		return 0, fmt.Errorf("chain_id %q is not a valid integer", s)
	}
	return id, nil
}