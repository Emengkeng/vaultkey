package relayer

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/programs/system"
	"github.com/gagliardetto/solana-go/programs/token"
	"github.com/vaultkey/vaultkey/internal/nonce"
	"github.com/vaultkey/vaultkey/internal/rpc"
	"github.com/vaultkey/vaultkey/internal/storage"
	"github.com/vaultkey/vaultkey/internal/wallet"
	"encoding/hex"
)

// MinBalanceWei is 0.05 ETH - below this we reject gasless jobs and log a warning.
const MinBalanceWei = "0x0B1A2BC2EC50000" // 0.05 ETH in wei hex

// MinBalanceLamports is 0.05 SOL.
const MinBalanceLamports = uint64(50_000_000)

// Service orchestrates gasless transaction relay for EVM and Solana.
type Service struct {
	store     *storage.Store
	walletSvc *wallet.Service
	rpcMgr    *rpc.Manager
	nonceMgr  *nonce.Manager
}

func New(store *storage.Store, walletSvc *wallet.Service, rpcMgr *rpc.Manager, nonceMgr *nonce.Manager) *Service {
	return &Service{
		store:     store,
		walletSvc: walletSvc,
		rpcMgr:    rpcMgr,
		nonceMgr:  nonceMgr,
	}
}

// EVMRelayPayload is the signing job payload for a gasless EVM transaction.
type EVMRelayPayload struct {
	To      string `json:"to"`
	Value   string `json:"value"`   // hex wei, usually "0x0" for token transfers
	Data    string `json:"data"`    // hex encoded calldata
	ChainID int64  `json:"chain_id"`
	// GasLimit and GasPrice are optional - we estimate if not provided
	GasLimit uint64 `json:"gas_limit,omitempty"`
	GasPrice string `json:"gas_price,omitempty"`
}

// SolanaRelayPayload is what the dev sends for a gasless Solana transaction.
type SolanaRelayPayload struct {
	To     string `json:"to"`     // recipient base58 address
	Amount uint64 `json:"amount"` // lamports for SOL, base units for tokens

	// For SPL token transfers (leave empty for native SOL)
	TokenMint          string `json:"token_mint,omitempty"`
	SourceTokenAccount string `json:"source_token_account,omitempty"`
	DestTokenAccount   string `json:"dest_token_account,omitempty"`
}

// RelayResult is returned after successful relay.
type RelayResult struct {
	Signature string `json:"signature,omitempty"` // Solana: transaction signature after broadcast
	TxHash    string `json:"tx_hash,omitempty"`   // EVM: tx hash after broadcast
}

// RelaySolana builds a Solana transaction server-side, signs with both the
// user wallet and relayer wallet, then broadcasts.
func (s *Service) RelaySolana(ctx context.Context, projectID string, userWlt *storage.Wallet, payload SolanaRelayPayload) (*RelayResult, error) {
	relayerWlt, err := s.store.GetRelayerWallet(ctx, projectID, "solana", "")
	if err != nil {
		return nil, fmt.Errorf("fetch solana relayer wallet: %w", err)
	}
	if relayerWlt == nil {
		return nil, fmt.Errorf("no solana relayer wallet configured - register one at POST /projects/relayer")
	}

	balance, err := s.rpcMgr.SolanaBalance(ctx, relayerWlt.Address)
	if err != nil {
		return nil, fmt.Errorf("check solana relayer balance: %w", err)
	}
	if balance < MinBalanceLamports {
		return nil, fmt.Errorf("solana relayer wallet %s balance too low (%d lamports) - top up before using gasless transactions", relayerWlt.Address, balance)
	}

	recentBlockhash, err := s.rpcMgr.SolanaRecentBlockhash(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch recent blockhash: %w", err)
	}

	txMessage, err := buildSolanaMessage(
		relayerWlt.Address,
		userWlt.Address,
		payload,
		recentBlockhash,
	)
	if err != nil {
		return nil, fmt.Errorf("build solana transaction: %w", err)
	}

	relayerKeyWlt, err := s.store.GetWalletByID(ctx, relayerWlt.WalletID, projectID)
	if err != nil || relayerKeyWlt == nil {
		return nil, fmt.Errorf("fetch relayer key: %w", err)
	}
	userKeyWlt, err := s.store.GetWalletByID(ctx, userWlt.ID, projectID)
	if err != nil || userKeyWlt == nil {
		return nil, fmt.Errorf("fetch user key: %w", err)
	}

	userSig, err := s.walletSvc.SignSolanaTransaction(ctx, userKeyWlt.EncryptedKey, userKeyWlt.EncryptedDEK, txMessage)
	if err != nil {
		return nil, fmt.Errorf("user wallet sign: %w", err)
	}

	relayerSig, err := s.walletSvc.SignSolanaTransaction(ctx, relayerKeyWlt.EncryptedKey, relayerKeyWlt.EncryptedDEK, txMessage)
	if err != nil {
		return nil, fmt.Errorf("relayer wallet sign: %w", err)
	}

	signedTx := assembleSolanaTransaction(txMessage, relayerSig, userSig)

	txSig, err := s.rpcMgr.SolanaBroadcastRaw(ctx, signedTx)
	if err != nil {
		return nil, fmt.Errorf("broadcast solana transaction: %w", err)
	}

	return &RelayResult{
		Signature: txSig,
	}, nil
}

// RelayEVM handles gasless EVM transaction relay.
// It signs the transaction with the relayer wallet and broadcasts it.
// Nonce management and resync on failure are handled here.
func (s *Service) RelayEVM(ctx context.Context, projectID string, userWlt *storage.Wallet, payload EVMRelayPayload) (*RelayResult, error) {
	chainIDStr := fmt.Sprintf("%d", payload.ChainID)

	relayerWlt, err := s.store.GetRelayerWallet(ctx, projectID, "evm", chainIDStr)
	if err != nil {
		return nil, fmt.Errorf("fetch relayer wallet: %w", err)
	}
	if relayerWlt == nil {
		return nil, fmt.Errorf("no relayer wallet configured for chain %s - register one at POST /projects/relayer", chainIDStr)
	}

	balance, err := s.rpcMgr.EVMBalance(ctx, chainIDStr, relayerWlt.Address)
	if err != nil {
		return nil, fmt.Errorf("check relayer balance: %w", err)
	}
	if !sufficientEVMBalance(balance, MinBalanceWei) {
		return nil, fmt.Errorf("relayer wallet %s balance too low (%s wei) - top up before using gasless transactions", relayerWlt.Address, balance)
	}

	if err := s.ensureNonce(ctx, chainIDStr, relayerWlt.Address); err != nil {
		return nil, fmt.Errorf("nonce init: %w", err)
	}

	txNonce, err := s.nonceMgr.Next(ctx, chainIDStr, relayerWlt.Address)
	if err != nil {
		return nil, fmt.Errorf("get nonce: %w", err)
	}

	gasLimit := payload.GasLimit
	if gasLimit == 0 {
		gasLimit = 200_000
	}

	gasPrice := payload.GasPrice
	if gasPrice == "" {
		gp, err := s.rpcMgr.EVMGasPrice(ctx, chainIDStr)
		if err != nil {
			gasPrice = "0x3B9ACA00" // 1 gwei fallback
		} else {
			gasPrice = gp
		}
	}

	tx := wallet.EVMTransaction{
		To:       payload.To,
		Value:    payload.Value,
		Data:     payload.Data,
		GasLimit: gasLimit,
		GasPrice: gasPrice,
		Nonce:    txNonce,
		ChainID:  payload.ChainID,
	}

	relayerKeyWlt, err := s.store.GetWalletByID(ctx, relayerWlt.WalletID, projectID)
	if err != nil || relayerKeyWlt == nil {
		s.resyncNonce(ctx, chainIDStr, relayerWlt.Address) //nolint:errcheck
		return nil, fmt.Errorf("fetch relayer key wallet: %w", err)
	}

	signed, err := s.walletSvc.SignEVMTransaction(ctx, relayerKeyWlt.EncryptedKey, relayerKeyWlt.EncryptedDEK, tx)
	if err != nil {
		s.resyncNonce(ctx, chainIDStr, relayerWlt.Address) //nolint:errcheck
		return nil, fmt.Errorf("sign relayer transaction: %w", err)
	}

	// Broadcast here - not in the worker. This keeps resync logic in one place.
	// If broadcast fails, the nonce was consumed in Redis but not on-chain,
	// so we must resync before the next transaction can succeed.
	txHash, err := s.rpcMgr.EVMBroadcast(ctx, chainIDStr, "0x"+hex.EncodeToString(signed))
	if err != nil {
		s.resyncNonce(ctx, chainIDStr, relayerWlt.Address) //nolint:errcheck
		return nil, fmt.Errorf("broadcast relayer transaction: %w", err)
	}

	return &RelayResult{
		TxHash: txHash,
	}, nil
}

// RelayerInfo holds balance and status info for a relayer wallet.
type RelayerInfo struct {
	WalletID  string `json:"wallet_id"`
	Address   string `json:"address"`
	ChainType string `json:"chain_type"`
	ChainID   string `json:"chain_id,omitempty"`
	Balance   string `json:"balance"`
	Unit      string `json:"unit"`
	Healthy   bool   `json:"healthy"`
}

// GetInfo returns balance and health status of a relayer wallet.
func (s *Service) GetInfo(ctx context.Context, projectID, chainType, chainID string) (*RelayerInfo, error) {
	rw, err := s.store.GetRelayerWallet(ctx, projectID, chainType, chainID)
	if err != nil || rw == nil {
		return nil, fmt.Errorf("relayer wallet not found")
	}

	info := &RelayerInfo{
		WalletID:  rw.WalletID,
		Address:   rw.Address,
		ChainType: chainType,
		ChainID:   chainID,
	}

	switch chainType {
	case "evm":
		balance, err := s.rpcMgr.EVMBalance(ctx, chainID, rw.Address)
		if err != nil {
			return nil, fmt.Errorf("fetch evm balance: %w", err)
		}
		info.Balance = balance
		info.Unit = "wei"
		info.Healthy = sufficientEVMBalance(balance, MinBalanceWei)

	case "solana":
		balance, err := s.rpcMgr.SolanaBalance(ctx, rw.Address)
		if err != nil {
			return nil, fmt.Errorf("fetch solana balance: %w", err)
		}
		info.Balance = fmt.Sprintf("%d", balance)
		info.Unit = "lamports"
		info.Healthy = balance >= MinBalanceLamports
	}

	return info, nil
}

func (s *Service) ensureNonce(ctx context.Context, chainID, address string) error {
	current, err := s.nonceMgr.Peek(ctx, chainID, address)
	if err != nil {
		return err
	}
	if current > 0 {
		return nil
	}
	return s.resyncNonce(ctx, chainID, address)
}

func (s *Service) resyncNonce(ctx context.Context, chainID, address string) error {
	pendingNonce, err := s.rpcMgr.EVMPendingNonce(ctx, chainID, address)
	if err != nil {
		return fmt.Errorf("fetch pending nonce from chain: %w", err)
	}
	return s.nonceMgr.SyncFromChain(ctx, chainID, address, pendingNonce)
}

func sufficientEVMBalance(balance, threshold string) bool {
	b := new(big.Int)
	t := new(big.Int)
	b.SetString(strings.TrimPrefix(balance, "0x"), 16)
	t.SetString(strings.TrimPrefix(threshold, "0x"), 16)
	return b.Cmp(t) >= 0
}

func buildSolanaMessage(feePayerAddr, userAddr string, payload SolanaRelayPayload, recentBlockhash string) ([]byte, error) {
	if payload.To == "" {
		return nil, fmt.Errorf("to address is required")
	}
	if payload.Amount == 0 {
		return nil, fmt.Errorf("amount must be greater than zero")
	}

	feePayer := solana.MustPublicKeyFromBase58(feePayerAddr)
	user := solana.MustPublicKeyFromBase58(userAddr)
	recipient := solana.MustPublicKeyFromBase58(payload.To)

	var instruction solana.Instruction

	if payload.TokenMint == "" {
		instruction = system.NewTransferInstruction(
			payload.Amount,
			user,
			recipient,
		).Build()
	} else {
		if payload.SourceTokenAccount == "" || payload.DestTokenAccount == "" {
			return nil, fmt.Errorf("source_token_account and dest_token_account are required for SPL token transfers")
		}

		sourceATA := solana.MustPublicKeyFromBase58(payload.SourceTokenAccount)
		destATA := solana.MustPublicKeyFromBase58(payload.DestTokenAccount)

		instruction = token.NewTransferInstruction(
			payload.Amount,
			sourceATA,
			destATA,
			user,
			[]solana.PublicKey{},
		).Build()
	}

	hash := solana.MustHashFromBase58(recentBlockhash)

	tx, err := solana.NewTransaction(
		[]solana.Instruction{instruction},
		hash,
		solana.TransactionPayer(feePayer),
	)
	if err != nil {
		return nil, fmt.Errorf("create transaction: %w", err)
	}

	messageBytes, err := tx.Message.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("serialize message: %w", err)
	}

	return messageBytes, nil
}

func assembleSolanaTransaction(message, feePayerSig, userSig []byte) []byte {
	tx := []byte{0x02}
	tx = append(tx, feePayerSig...)
	tx = append(tx, userSig...)
	tx = append(tx, message...)
	return tx
}