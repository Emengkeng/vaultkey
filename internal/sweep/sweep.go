package sweep

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/vaultkey/vaultkey/internal/queue"
	"github.com/vaultkey/vaultkey/internal/rpc"
	"github.com/vaultkey/vaultkey/internal/storage"
	"github.com/vaultkey/vaultkey/internal/wallet"
)

// blockedEVMChains lists chain IDs where sweep is not permitted.
// Ethereum mainnet is blocked because gas costs make sweeping uneconomical.
// All other EVM chains (Polygon, Arbitrum, Base, Optimism, etc.) are allowed.
var blockedEVMChains = map[string]bool{
	"1": true, // Ethereum mainnet
}

// Service handles master wallet provisioning and sweep job creation.
type Service struct {
	store     *storage.Store
	walletSvc *wallet.Service
	rpcMgr    *rpc.Manager
	queue     *queue.Queue
}

func New(store *storage.Store, walletSvc *wallet.Service, rpcMgr *rpc.Manager, q *queue.Queue) *Service {
	return &Service{
		store:     store,
		walletSvc: walletSvc,
		rpcMgr:    rpcMgr,
		queue:     q,
	}
}

// MasterWalletResult is returned after provisioning a master wallet.
type MasterWalletResult struct {
	Config *storage.SweepConfig
	Wallet *storage.Wallet
}

// ProvisionMasterWallet returns the existing master wallet for a chain,
// or generates a fresh one if none exists yet.
//
// Idempotent: calling this multiple times on the same chain always returns
// the same wallet. To replace a master wallet (e.g. key rotation after a
// compromise), use POST /projects/master-wallet/rotate (not yet implemented).
//
// The developer never chooses which wallet becomes master. VaultKey generates
// a dedicated one with user_id="_master" so it never appears in user wallet lists.
func (s *Service) ProvisionMasterWallet(ctx context.Context, projectID, chainType, chainID, dustThreshold string) (*MasterWalletResult, error) {
	if err := validateChain(chainType, chainID); err != nil {
		return nil, err
	}

	// Return existing if already configured
	cfg, err := s.store.GetSweepConfig(ctx, projectID, chainType, chainID)
	if err != nil {
		return nil, fmt.Errorf("check existing sweep config: %w", err)
	}
	if cfg != nil {
		existing, err := s.store.GetWalletByID(ctx, cfg.MasterWalletID, projectID)
		if err != nil || existing == nil {
			return nil, fmt.Errorf("master wallet record missing — contact support")
		}
		return &MasterWalletResult{Config: cfg, Wallet: existing}, nil
	}

	// None exists — generate a fresh dedicated wallet
	generated, err := s.walletSvc.Generate(ctx, wallet.ChainType(chainType))
	if err != nil {
		return nil, fmt.Errorf("generate master wallet: %w", err)
	}

	stored, err := s.store.CreateWallet(ctx, &storage.Wallet{
		ProjectID:    projectID,
		UserID:       "_master",
		ChainType:    chainType,
		Address:      generated.Address,
		EncryptedKey: generated.EncryptedKey,
		EncryptedDEK: generated.EncryptedDEK,
	})
	if err != nil {
		return nil, fmt.Errorf("store master wallet: %w", err)
	}

	cfg, err = s.store.CreateMasterWallet(ctx, projectID, stored.ID, chainType, chainID, dustThreshold)
	if err != nil {
		return nil, fmt.Errorf("configure master wallet: %w", err)
	}

	return &MasterWalletResult{
		Config: cfg,
		Wallet: stored,
	}, nil
}

// SweepRequest is the input to TriggerSweep.
type SweepRequest struct {
	ProjectID      string
	WalletID       string
	ChainType      string
	ChainID        string  // EVM only, empty for Solana
	IdempotencyKey *string
}

// TriggerSweep validates all preconditions and enqueues a sweep job.
// Supported on: EVM L2s (Polygon, Arbitrum, Base, Optimism), Solana.
// Not supported on: Ethereum mainnet.
//
// Preconditions checked in order:
//  1. Chain is supported
//  2. Source wallet exists and belongs to project
//  3. Source wallet is not a master wallet
//  4. Sweep config exists and is enabled
//  5. Relayer exists and is healthy
//  6. Source wallet balance exceeds dust threshold
func (s *Service) TriggerSweep(ctx context.Context, req SweepRequest) (*storage.SigningJob, error) {
	if err := validateChain(req.ChainType, req.ChainID); err != nil {
		return nil, err
	}

	wlt, err := s.store.GetWalletByID(ctx, req.WalletID, req.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("fetch wallet: %w", err)
	}
	if wlt == nil {
		return nil, fmt.Errorf("wallet not found")
	}

	if wlt.IsMaster {
		return nil, fmt.Errorf("master wallets cannot be swept — they are the sweep destination")
	}

	cfg, err := s.store.GetSweepConfig(ctx, req.ProjectID, req.ChainType, req.ChainID)
	if err != nil {
		return nil, fmt.Errorf("fetch sweep config: %w", err)
	}
	if cfg == nil {
		return nil, fmt.Errorf("sweep not configured for this chain — call POST /projects/master-wallet first")
	}
	if !cfg.Enabled {
		return nil, fmt.Errorf("sweep is disabled for this chain")
	}

	relayerWlt, err := s.store.GetRelayerWallet(ctx, req.ProjectID, req.ChainType, req.ChainID)
	if err != nil {
		return nil, fmt.Errorf("fetch relayer: %w", err)
	}
	if relayerWlt == nil {
		return nil, fmt.Errorf("no relayer configured for this chain — sweep requires a relayer to pay fees, register one at POST /projects/relayer")
	}
	if err := s.assertRelayerHealthy(ctx, relayerWlt, req.ChainType, req.ChainID); err != nil {
		return nil, err
	}

	masterWlt, err := s.store.GetWalletByID(ctx, cfg.MasterWalletID, req.ProjectID)
	if err != nil || masterWlt == nil {
		return nil, fmt.Errorf("master wallet missing or inaccessible")
	}

	balance, err := s.fetchBalance(ctx, wlt.Address, req.ChainType, req.ChainID)
	if err != nil {
		return nil, fmt.Errorf("fetch balance: %w", err)
	}
	if err := s.assertAboveDustThreshold(balance, cfg.DustThreshold, req.ChainType); err != nil {
		return nil, err
	}

	operation := "sweep_" + req.ChainType
	payload := buildSweepPayload(masterWlt.Address, balance, req.ChainType, req.ChainID)

	// Sweep is always gasless — relayer pays, full balance moves
	job, err := s.store.CreateSigningJob(ctx, req.ProjectID, req.WalletID, operation, payload, req.IdempotencyKey, true)
	if err != nil {
		return nil, fmt.Errorf("create sweep job: %w", err)
	}

	if job.Status == "completed" || job.Status == "processing" {
		return job, nil
	}

	if err := s.queue.Enqueue(ctx, queue.Job{
		ID:        job.ID,
		ProjectID: req.ProjectID,
		WalletID:  req.WalletID,
		Operation: operation,
	}); err != nil {
		return nil, fmt.Errorf("enqueue sweep job: %w", err)
	}

	return job, nil
}

func validateChain(chainType, chainID string) error {
	switch chainType {
	case "evm":
		if chainID == "" {
			return fmt.Errorf("chain_id is required for EVM sweep")
		}
		if blockedEVMChains[chainID] {
			return fmt.Errorf("sweep is not available on Ethereum mainnet (chain_id=1) — gas costs are too high, use an L2 (Polygon, Arbitrum, Base, Optimism)")
		}
	case "solana":
		// Solana has no chain_id and no blocked networks
	default:
		return fmt.Errorf("unsupported chain_type %q — must be 'evm' or 'solana'", chainType)
	}
	return nil
}

func (s *Service) assertRelayerHealthy(ctx context.Context, rw *storage.RelayerWallet, chainType, chainID string) error {
	const minEVMBalanceHex = "0x0B1A2BC2EC50000" // 0.05 ETH equivalent in wei
	const minSolanaLamports = uint64(50_000_000)  // 0.05 SOL

	switch chainType {
	case "evm":
		bal, err := s.rpcMgr.EVMBalance(ctx, chainID, rw.Address)
		if err != nil {
			return fmt.Errorf("check relayer balance: %w", err)
		}
		if hexToBigInt(bal).Cmp(hexToBigInt(minEVMBalanceHex)) < 0 {
			return fmt.Errorf("relayer wallet %s balance too low to pay sweep gas — top it up before sweeping", rw.Address)
		}
	case "solana":
		bal, err := s.rpcMgr.SolanaBalance(ctx, rw.Address)
		if err != nil {
			return fmt.Errorf("check relayer balance: %w", err)
		}
		if bal < minSolanaLamports {
			return fmt.Errorf("relayer wallet %s balance too low to pay sweep fee — top it up before sweeping", rw.Address)
		}
	}
	return nil
}

func (s *Service) fetchBalance(ctx context.Context, address, chainType, chainID string) (string, error) {
	switch chainType {
	case "evm":
		return s.rpcMgr.EVMBalance(ctx, chainID, address)
	case "solana":
		bal, err := s.rpcMgr.SolanaBalance(ctx, address)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d", bal), nil
	}
	return "", fmt.Errorf("unsupported chain type: %s", chainType)
}

func (s *Service) assertAboveDustThreshold(balance, threshold, chainType string) error {
	if threshold == "0" || threshold == "" {
		return nil
	}

	var bal, thr *big.Int

	switch chainType {
	case "evm":
		bal = hexToBigInt(balance)
		thr = hexToBigInt(threshold)
	case "solana":
		bal = new(big.Int)
		bal.SetString(balance, 10)
		thr = new(big.Int)
		thr.SetString(threshold, 10)
	default:
		return nil
	}

	if bal.Cmp(thr) < 0 {
		return fmt.Errorf("wallet balance (%s) is below dust threshold (%s) — nothing worth sweeping", balance, threshold)
	}
	return nil
}

func hexToBigInt(h string) *big.Int {
	n := new(big.Int)
	n.SetString(strings.TrimPrefix(h, "0x"), 16)
	return n
}

func buildSweepPayload(masterAddress, balance, chainType, chainID string) []byte {
	switch chainType {
	case "evm":
		return []byte(fmt.Sprintf(
			`{"to":%q,"value":%q,"data":"0x","chain_id":%q,"sweep":true}`,
			masterAddress, balance, chainID,
		))
	case "solana":
		return []byte(fmt.Sprintf(
			`{"to":%q,"amount":%s,"sweep":true}`,
			masterAddress, balance,
		))
	}
	return nil
}