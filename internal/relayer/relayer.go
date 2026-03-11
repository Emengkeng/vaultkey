package relayer

import (
	"context"
	"encoding/hex"
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
	Value   string `json:"value"`    // hex wei, usually "0x0" for token transfers
	Data    string `json:"data"`     // hex encoded calldata
	ChainID int64  `json:"chain_id"`
	// GasLimit and GasPrice are optional - we estimate if not provided
	GasLimit uint64 `json:"gas_limit,omitempty"`
	GasPrice string `json:"gas_price,omitempty"`
}

// SolanaRelayPayload is what the dev sends for a gasless Solana transaction.
// VaultKey holds both the user wallet key and the relayer key, so it builds
// the transaction server-side, applies both signatures, and broadcasts.
// The dev only describes intent - who to send to, what token, how much.
type SolanaRelayPayload struct {
	// For SOL transfers
	To     string `json:"to"`              // recipient base58 address
	Amount uint64 `json:"amount"`          // lamports for SOL, base units for tokens

	// For SPL token transfers (leave empty for native SOL)
	TokenMint          string `json:"token_mint,omitempty"`           // e.g. USDC mint address
	SourceTokenAccount string `json:"source_token_account,omitempty"` // sender's associated token account
	DestTokenAccount   string `json:"dest_token_account,omitempty"`   // recipient's associated token account
}

// RelayResult is returned after successful relay.
type RelayResult struct {
	SignedTx  string `json:"signed_tx,omitempty"`  // EVM: hex encoded ready to broadcast
	Signature string `json:"signature,omitempty"`  // Solana: transaction signature after broadcast
	TxHash    string `json:"tx_hash,omitempty"`    // EVM: after broadcast
}

// RelaySolana builds a Solana transaction server-side, signs with both the
// user wallet (authorizes transfer) and relayer wallet (pays fee), then broadcasts.
// Neither key ever leaves VaultKey.
func (s *Service) RelaySolana(ctx context.Context, projectID string, userWlt *storage.Wallet, payload SolanaRelayPayload) (*RelayResult, error) {
	// Fetch relayer wallet
	relayerWlt, err := s.store.GetRelayerWallet(ctx, projectID, "solana", "")
	if err != nil {
		return nil, fmt.Errorf("fetch solana relayer wallet: %w", err)
	}
	if relayerWlt == nil {
		return nil, fmt.Errorf("no solana relayer wallet configured - register one at POST /projects/relayer")
	}

	// Check relayer SOL balance - must cover transaction fee
	balance, err := s.rpcMgr.SolanaBalance(ctx, relayerWlt.Address)
	if err != nil {
		return nil, fmt.Errorf("check solana relayer balance: %w", err)
	}
	if balance < MinBalanceLamports {
		return nil, fmt.Errorf("solana relayer wallet %s balance too low (%d lamports) - top up before using gasless transactions", relayerWlt.Address, balance)
	}

	// Fetch a recent blockhash - required for every Solana transaction
	recentBlockhash, err := s.rpcMgr.SolanaRecentBlockhash(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch recent blockhash: %w", err)
	}

	// Build the transaction message.
	// Structure: [fee_payer_key, user_key, recipient_key, ...program_keys]
	// Fee payer is always first - Solana protocol requirement.
	//
	// For a SOL transfer the instruction is SystemProgram.Transfer:
	//   from: user wallet  (signer, debited)
	//   to:   recipient
	//   fee payer: relayer wallet  (signer, pays lamport fee - not debited for transfer)
	//
	// For SPL token transfers the instruction is TokenProgram.Transfer:
	//   source token account: user's ATA
	//   destination token account: recipient's ATA
	//   authority: user wallet  (signer)
	//   fee payer: relayer wallet  (signer, pays fee only)
	txMessage, err := buildSolanaMessage(
		relayerWlt.Address, // fee payer - first account
		userWlt.Address,    // user wallet - transfer authority
		payload,
		recentBlockhash,
	)
	if err != nil {
		return nil, fmt.Errorf("build solana transaction: %w", err)
	}

	// Fetch key wallets from storage
	relayerKeyWlt, err := s.store.GetWalletByID(ctx, relayerWlt.WalletID, projectID)
	if err != nil || relayerKeyWlt == nil {
		return nil, fmt.Errorf("fetch relayer key: %w", err)
	}
	userKeyWlt, err := s.store.GetWalletByID(ctx, userWlt.ID, projectID)
	if err != nil || userKeyWlt == nil {
		return nil, fmt.Errorf("fetch user key: %w", err)
	}

	// Sign with user wallet - authorizes the transfer
	userSig, err := s.walletSvc.SignSolanaTransaction(ctx, userKeyWlt.EncryptedKey, userKeyWlt.EncryptedDEK, txMessage)
	if err != nil {
		return nil, fmt.Errorf("user wallet sign: %w", err)
	}

	// Sign with relayer wallet - authorizes fee payment
	relayerSig, err := s.walletSvc.SignSolanaTransaction(ctx, relayerKeyWlt.EncryptedKey, relayerKeyWlt.EncryptedDEK, txMessage)
	if err != nil {
		return nil, fmt.Errorf("relayer wallet sign: %w", err)
	}

	// Assemble the fully signed transaction
	// Solana wire format: [num_signatures][sig_0...sig_n][message]
	// Signatures must be in the same order as accounts in the message header.
	// Fee payer (relayer) is account[0], user is account[1].
	signedTx := assembleSolanaTransaction(txMessage, relayerSig, userSig)

	// Broadcast
	txSig, err := s.rpcMgr.SolanaBroadcastRaw(ctx, signedTx)
	if err != nil {
		return nil, fmt.Errorf("broadcast solana transaction: %w", err)
	}

	return &RelayResult{
		Signature: txSig,
	}, nil
}

// buildSolanaMessage constructs and serializes a Solana transaction message using
// the solana-go library. This handles all the complexity of account ordering,
// message header construction, and binary serialization.
//
// The transaction has two signers:
//   - Fee payer (relayer): pays the transaction fee
//   - User: authorizes the transfer (owns the source account)
//
// For native SOL transfers, the user's wallet is debited and the recipient is credited.
// For SPL token transfers, the user's token account is debited.
func buildSolanaMessage(feePayerAddr, userAddr string, payload SolanaRelayPayload, recentBlockhash string) ([]byte, error) {
	if payload.To == "" {
		return nil, fmt.Errorf("to address is required")
	}
	if payload.Amount == 0 {
		return nil, fmt.Errorf("amount must be greater than zero")
	}

	// Parse addresses - MustPublicKeyFromBase58 panics on invalid input,
	// so validate addresses before calling if accepting untrusted input
	feePayer := solana.MustPublicKeyFromBase58(feePayerAddr)
	user := solana.MustPublicKeyFromBase58(userAddr)
	recipient := solana.MustPublicKeyFromBase58(payload.To)

	var instruction solana.Instruction

	if payload.TokenMint == "" {
		// Native SOL transfer: user sends to recipient, relayer pays transaction fee
		// The system program handles native SOL transfers
		instruction = system.NewTransferInstruction(
			payload.Amount, // lamports to transfer
			user,           // from (must sign to authorize debit)
			recipient,      // to (receives SOL)
		).Build()
	} else {
		// SPL token transfer
		if payload.SourceTokenAccount == "" || payload.DestTokenAccount == "" {
			return nil, fmt.Errorf("source_token_account and dest_token_account are required for SPL token transfers")
		}

		sourceATA := solana.MustPublicKeyFromBase58(payload.SourceTokenAccount)
		destATA := solana.MustPublicKeyFromBase58(payload.DestTokenAccount)

		// Token transfer: user is the authority who owns the source token account
		// The token program handles SPL token transfers
		instruction = token.NewTransferInstruction(
			payload.Amount,       // token amount (in base units, respects token decimals)
			sourceATA,            // source token account (user's ATA)
			destATA,              // destination token account (recipient's ATA)
			user,                 // authority (owner of source account, must sign)
			[]solana.PublicKey{}, // no multisig signers
		).Build()
	}

	// Parse the recent blockhash - required for replay protection
	hash := solana.MustHashFromBase58(recentBlockhash)

	// Create the transaction with relayer as fee payer
	// The solana-go library automatically handles:
	// - Account ordering (fee payer first, then other signers, then non-signers)
	// - Message header construction (num required signatures, readonly counts)
	// - Instruction account index resolution
	tx, err := solana.NewTransaction(
		[]solana.Instruction{instruction},
		hash,
		solana.TransactionPayer(feePayer), // relayer pays the transaction fee
	)
	if err != nil {
		return nil, fmt.Errorf("create transaction: %w", err)
	}

	// Serialize the message (this is what gets signed)
	// The message includes the header, account keys, blockhash, and instructions
	messageBytes, err := tx.Message.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("serialize message: %w", err)
	}

	return messageBytes, nil
}

// assembleSolanaTransaction combines the message with its signatures into
// the Solana wire transaction format.
//
// Wire format: [compact_u16: num_sigs][sig_0 64 bytes][sig_1 64 bytes][message bytes]
//
// Signatures must be in the same order as signing accounts appear in the message.
// The fee payer (relayer) is always account[0], and for our transfers the user is account[1].
func assembleSolanaTransaction(message, feePayerSig, userSig []byte) []byte {
	// Compact-u16 encoding: for values < 128, it's just a single byte
	// We have 2 signatures, so we write 0x02
	tx := []byte{0x02}

	// Append signatures in order: fee payer first, then user
	// Each signature is exactly 64 bytes (ed25519)
	tx = append(tx, feePayerSig...)
	tx = append(tx, userSig...)

	// Append the serialized message
	tx = append(tx, message...)

	return tx
}

// RelayEVM handles gasless EVM transaction relay.
// It fetches the relayer wallet, manages the nonce, and signs as the submitter.
// For custodial wallets, the relayer simply submits on behalf of the user.
func (s *Service) RelayEVM(ctx context.Context, projectID string, userWlt *storage.Wallet, payload EVMRelayPayload) (*RelayResult, error) {
	chainIDStr := fmt.Sprintf("%d", payload.ChainID)

	// Fetch relayer wallet for this project + chain
	relayerWlt, err := s.store.GetRelayerWallet(ctx, projectID, "evm", chainIDStr)
	if err != nil {
		return nil, fmt.Errorf("fetch relayer wallet: %w", err)
	}
	if relayerWlt == nil {
		return nil, fmt.Errorf("no relayer wallet configured for chain %s - register one at POST /projects/relayer", chainIDStr)
	}

	// Check relayer balance
	balance, err := s.rpcMgr.EVMBalance(ctx, chainIDStr, relayerWlt.Address)
	if err != nil {
		return nil, fmt.Errorf("check relayer balance: %w", err)
	}
	if !sufficientEVMBalance(balance, MinBalanceWei) {
		return nil, fmt.Errorf("relayer wallet %s balance too low (%s wei) - top up before using gasless transactions", relayerWlt.Address, balance)
	}

	// Sync nonce from chain if not already initialised
	if err := s.ensureNonce(ctx, chainIDStr, relayerWlt.Address); err != nil {
		return nil, fmt.Errorf("nonce init: %w", err)
	}

	// Get next nonce atomically
	txNonce, err := s.nonceMgr.Next(ctx, chainIDStr, relayerWlt.Address)
	if err != nil {
		return nil, fmt.Errorf("get nonce: %w", err)
	}

	// Estimate gas if not provided
	gasLimit := payload.GasLimit
	if gasLimit == 0 {
		gasLimit = 200_000 // safe default; production should estimate
	}

	gasPrice := payload.GasPrice
	if gasPrice == "" {
		// Fetch current gas price from chain
		gp, err := s.rpcMgr.EVMGasPrice(ctx, chainIDStr)
		if err != nil {
			// Fall back to a reasonable default rather than failing
			gasPrice = "0x3B9ACA00" // 1 gwei
		} else {
			gasPrice = gp
		}
	}

	// For custodial wallets: the relayer signs and submits the full transaction.
	// The user wallet's intent is encoded in the calldata (already signed upstream
	// or embedded as a permit signature in the data field).
	tx := wallet.EVMTransaction{
		To:       payload.To,
		Value:    payload.Value,
		Data:     payload.Data,
		GasLimit: gasLimit,
		GasPrice: gasPrice,
		Nonce:    txNonce,
		ChainID:  payload.ChainID,
	}

	// Sign with relayer wallet (relayer pays gas and submits)
	relayerKeyWlt, err := s.store.GetWalletByID(ctx, relayerWlt.WalletID, projectID)
	if err != nil || relayerKeyWlt == nil {
		// Nonce was consumed but tx failed - resync on next attempt
		s.resyncNonce(ctx, chainIDStr, relayerWlt.Address) //nolint:errcheck
		return nil, fmt.Errorf("fetch relayer key wallet: %w", err)
	}

	signed, err := s.walletSvc.SignEVMTransaction(ctx, relayerKeyWlt.EncryptedKey, relayerKeyWlt.EncryptedDEK, tx)
	if err != nil {
		s.resyncNonce(ctx, chainIDStr, relayerWlt.Address) //nolint:errcheck
		return nil, fmt.Errorf("sign relayer transaction: %w", err)
	}

	return &RelayResult{
		SignedTx: "0x" + hex.EncodeToString(signed),
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
	Healthy   bool   `json:"healthy"` // false when balance below threshold
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

// ensureNonce initialises the Redis nonce counter from chain if not already set.
func (s *Service) ensureNonce(ctx context.Context, chainID, address string) error {
	current, err := s.nonceMgr.Peek(ctx, chainID, address)
	if err != nil {
		return err
	}
	if current > 0 {
		return nil // already initialised
	}
	return s.resyncNonce(ctx, chainID, address)
}

// resyncNonce fetches the pending nonce from chain and resets the Redis counter.
func (s *Service) resyncNonce(ctx context.Context, chainID, address string) error {
	pendingNonce, err := s.rpcMgr.EVMPendingNonce(ctx, chainID, address)
	if err != nil {
		return fmt.Errorf("fetch pending nonce from chain: %w", err)
	}
	return s.nonceMgr.SyncFromChain(ctx, chainID, address, pendingNonce)
}

// sufficientEVMBalance compares two hex wei strings.
func sufficientEVMBalance(balance, threshold string) bool {
	b := new(big.Int)
	t := new(big.Int)
	b.SetString(strings.TrimPrefix(balance, "0x"), 16)
	t.SetString(strings.TrimPrefix(threshold, "0x"), 16)
	return b.Cmp(t) >= 0
}