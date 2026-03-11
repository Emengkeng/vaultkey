package wallet

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

type EVMWallet struct{}

type EVMKeyPair struct {
	PrivateKeyBytes []byte
	Address         string
}

type EVMTransaction struct {
	To       string   `json:"to"`
	Value    string   `json:"value"`     // hex string, wei
	Data     string   `json:"data"`      // hex string
	GasLimit uint64   `json:"gas_limit"`
	GasPrice string   `json:"gas_price"` // hex string, wei
	Nonce    uint64   `json:"nonce"`
	ChainID  int64    `json:"chain_id"`
}

func NewEVMWallet() *EVMWallet {
	return &EVMWallet{}
}

// Generate creates a new EVM keypair. Private key bytes are returned
// and must be encrypted immediately by the caller.
func (e *EVMWallet) Generate(_ context.Context) (*EVMKeyPair, error) {
	privateKey, err := crypto.GenerateKey()
	if err != nil {
		return nil, fmt.Errorf("generate evm key: %w", err)
	}

	privBytes := crypto.FromECDSA(privateKey)
	address := crypto.PubkeyToAddress(privateKey.PublicKey).Hex()

	// zero the in-memory private key object immediately after extracting bytes
	zeroECDSA(privateKey)

	return &EVMKeyPair{
		PrivateKeyBytes: privBytes,
		Address:         address,
	}, nil
}

// SignTransaction signs a transaction. Private key bytes are wiped after signing.
func (e *EVMWallet) SignTransaction(_ context.Context, privKeyBytes []byte, tx EVMTransaction) ([]byte, error) {
	defer wipeBytes(privKeyBytes)

	privateKey, err := crypto.ToECDSA(privKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("parse evm private key: %w", err)
	}
	defer zeroECDSA(privateKey)

	toAddr := common.HexToAddress(tx.To)

	value := new(big.Int)
	if tx.Value != "" {
		value.SetString(stripHexPrefix(tx.Value), 16)
	}

	gasPrice := new(big.Int)
	if tx.GasPrice != "" {
		gasPrice.SetString(stripHexPrefix(tx.GasPrice), 16)
	}

	var data []byte
	if tx.Data != "" {
		data, err = hex.DecodeString(stripHexPrefix(tx.Data))
		if err != nil {
			return nil, fmt.Errorf("decode tx data: %w", err)
		}
	}

	rawTx := types.NewTransaction(
		tx.Nonce,
		toAddr,
		value,
		tx.GasLimit,
		gasPrice,
		data,
	)

	signer := types.NewEIP155Signer(big.NewInt(tx.ChainID))
	signed, err := types.SignTx(rawTx, signer, privateKey)
	if err != nil {
		return nil, fmt.Errorf("sign evm transaction: %w", err)
	}

	encoded, err := signed.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("encode signed transaction: %w", err)
	}

	return encoded, nil
}

// SignMessage signs an arbitrary message (EIP-191 personal_sign prefix).
func (e *EVMWallet) SignMessage(_ context.Context, privKeyBytes []byte, message []byte) ([]byte, error) {
	defer wipeBytes(privKeyBytes)

	privateKey, err := crypto.ToECDSA(privKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("parse evm private key: %w", err)
	}
	defer zeroECDSA(privateKey)

	prefixed := fmt.Sprintf("\x19Ethereum Signed Message:\n%d%s", len(message), message)
	hash := crypto.Keccak256Hash([]byte(prefixed))

	sig, err := crypto.Sign(hash.Bytes(), privateKey)
	if err != nil {
		return nil, fmt.Errorf("sign evm message: %w", err)
	}

	// adjust recovery id to match eth_sign convention
	sig[64] += 27

	return sig, nil
}

// wipeBytes zeros a byte slice to minimize private key exposure in memory.
// Note: Go GC may have already copied the slice; this is best-effort.
func wipeBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
	// overwrite with random bytes as second pass
	rand.Read(b) //nolint:errcheck
}

func zeroECDSA(key *ecdsa.PrivateKey) {
	if key == nil {
		return
	}
	key.D.SetInt64(0)
}

func stripHexPrefix(s string) string {
	if len(s) >= 2 && s[:2] == "0x" {
		return s[2:]
	}
	return s
}
