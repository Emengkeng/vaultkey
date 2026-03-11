package wallet

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"

	"github.com/mr-tron/base58"
)

type SolanaWallet struct{}

type SolanaKeyPair struct {
	PrivateKeyBytes []byte
	Address         string // base58 encoded public key
}

func NewSolanaWallet() *SolanaWallet {
	return &SolanaWallet{}
}

// Generate creates a new Solana keypair. Private key bytes must be
// encrypted immediately by the caller.
func (s *SolanaWallet) Generate(_ context.Context) (*SolanaKeyPair, error) {
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate solana key: %w", err)
	}

	// Solana private key is 64 bytes: first 32 are private scalar, last 32 are public key
	privBytes := make([]byte, 64)
	copy(privBytes[:32], privKey[:32])
	copy(privBytes[32:], pubKey)

	address := base58.Encode(pubKey)

	// zero the ed25519 key objects
	wipeBytes(privKey)

	return &SolanaKeyPair{
		PrivateKeyBytes: privBytes,
		Address:         address,
	}, nil
}

// SignTransaction signs a Solana transaction message.
// The caller is responsible for constructing the serialized transaction message.
func (s *SolanaWallet) SignTransaction(_ context.Context, privKeyBytes []byte, txMessage []byte) ([]byte, error) {
	defer wipeBytes(privKeyBytes)

	privKey, err := solanaPrivKey(privKeyBytes)
	if err != nil {
		return nil, err
	}
	defer wipeBytes(privKey)

	sig := ed25519.Sign(privKey, txMessage)
	return sig, nil
}

// SignMessage signs arbitrary bytes with the Solana private key.
func (s *SolanaWallet) SignMessage(_ context.Context, privKeyBytes []byte, message []byte) ([]byte, error) {
	defer wipeBytes(privKeyBytes)

	privKey, err := solanaPrivKey(privKeyBytes)
	if err != nil {
		return nil, err
	}
	defer wipeBytes(privKey)

	sig := ed25519.Sign(privKey, message)
	return sig, nil
}

// solanaPrivKey reconstructs the ed25519.PrivateKey from our stored 64-byte format.
func solanaPrivKey(privKeyBytes []byte) (ed25519.PrivateKey, error) {
	if len(privKeyBytes) != 64 {
		return nil, fmt.Errorf("invalid solana private key length: expected 64, got %d", len(privKeyBytes))
	}
	key := make(ed25519.PrivateKey, 64)
	copy(key, privKeyBytes)
	return key, nil
}
