package wallet

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"

	"github.com/vaultkey/vaultkey/internal/kms"
)

// Service orchestrates wallet operations. It is the only layer that
// touches raw private key bytes - and only briefly.
type Service struct {
	kms     kms.KMS
	evm     *EVMWallet
	solana  *SolanaWallet
}

func NewService(k kms.KMS) *Service {
	return &Service{
		kms:    k,
		evm:    NewEVMWallet(),
		solana: NewSolanaWallet(),
	}
}

type ChainType string

const (
	ChainTypeEVM    ChainType = "evm"
	ChainTypeSolana ChainType = "solana"
)

type GeneratedWallet struct {
	Address      string
	ChainType    ChainType
	EncryptedKey []byte // AES-GCM encrypted private key
	EncryptedDEK []byte // Vault-encrypted DEK
}

// Generate creates a new wallet, encrypts the private key, and returns
// the encrypted material ready for storage. Raw private key never leaves this function.
func (s *Service) Generate(ctx context.Context, chainType ChainType) (*GeneratedWallet, error) {
	var (
		privKeyBytes []byte
		address      string
		err          error
	)

	switch chainType {
	case ChainTypeEVM:
		kp, err := s.evm.Generate(ctx)
		if err != nil {
			return nil, fmt.Errorf("generate evm wallet: %w", err)
		}
		privKeyBytes = kp.PrivateKeyBytes
		address = kp.Address
	case ChainTypeSolana:
		kp, err := s.solana.Generate(ctx)
		if err != nil {
			return nil, fmt.Errorf("generate solana wallet: %w", err)
		}
		privKeyBytes = kp.PrivateKeyBytes
		address = kp.Address
	default:
		return nil, fmt.Errorf("unsupported chain type: %s", chainType)
	}

	// generate DEK
	dek := make([]byte, 32)
	if _, err = rand.Read(dek); err != nil {
		wipeBytes(privKeyBytes)
		return nil, fmt.Errorf("generate dek: %w", err)
	}

	// encrypt private key with DEK
	encryptedKey, err := aesEncrypt(dek, privKeyBytes)
	wipeBytes(privKeyBytes) // private key no longer needed
	if err != nil {
		wipeBytes(dek)
		return nil, fmt.Errorf("encrypt private key with dek: %w", err)
	}

	// encrypt DEK with master key via KMS
	encryptedDEK, err := s.kms.Encrypt(ctx, dek)
	wipeBytes(dek) // DEK no longer needed
	if err != nil {
		return nil, fmt.Errorf("encrypt dek with kms: %w", err)
	}

	return &GeneratedWallet{
		Address:      address,
		ChainType:    chainType,
		EncryptedKey: encryptedKey,
		EncryptedDEK: encryptedDEK,
	}, nil
}

// decryptPrivateKey fetches the DEK from KMS and decrypts the private key.
// Caller MUST call wipeBytes on the returned slice after use.
func (s *Service) decryptPrivateKey(ctx context.Context, encryptedKey, encryptedDEK []byte) ([]byte, error) {
	dek, err := s.kms.Decrypt(ctx, encryptedDEK)
	if err != nil {
		return nil, fmt.Errorf("decrypt dek from kms: %w", err)
	}
	defer wipeBytes(dek)

	privKeyBytes, err := aesDecrypt(dek, encryptedKey)
	if err != nil {
		return nil, fmt.Errorf("decrypt private key with dek: %w", err)
	}

	return privKeyBytes, nil
}

// SignEVMTransaction decrypts the key, signs, and wipes.
func (s *Service) SignEVMTransaction(ctx context.Context, encryptedKey, encryptedDEK []byte, tx EVMTransaction) ([]byte, error) {
	privKey, err := s.decryptPrivateKey(ctx, encryptedKey, encryptedDEK)
	if err != nil {
		return nil, err
	}
	// privKey will be wiped inside SignTransaction via defer
	return s.evm.SignTransaction(ctx, privKey, tx)
}

// SignEVMMessage decrypts the key, signs, and wipes.
func (s *Service) SignEVMMessage(ctx context.Context, encryptedKey, encryptedDEK []byte, message []byte) ([]byte, error) {
	privKey, err := s.decryptPrivateKey(ctx, encryptedKey, encryptedDEK)
	if err != nil {
		return nil, err
	}
	return s.evm.SignMessage(ctx, privKey, message)
}

// SignSolanaTransaction decrypts the key, signs, and wipes.
func (s *Service) SignSolanaTransaction(ctx context.Context, encryptedKey, encryptedDEK []byte, txMessage []byte) ([]byte, error) {
	privKey, err := s.decryptPrivateKey(ctx, encryptedKey, encryptedDEK)
	if err != nil {
		return nil, err
	}
	return s.solana.SignTransaction(ctx, privKey, txMessage)
}

// SignSolanaMessage decrypts the key, signs, and wipes.
func (s *Service) SignSolanaMessage(ctx context.Context, encryptedKey, encryptedDEK []byte, message []byte) ([]byte, error) {
	privKey, err := s.decryptPrivateKey(ctx, encryptedKey, encryptedDEK)
	if err != nil {
		return nil, err
	}
	return s.solana.SignMessage(ctx, privKey, message)
}

// aesEncrypt encrypts plaintext with AES-256-GCM using the given key.
func aesEncrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	// nonce is prepended to ciphertext
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return ciphertext, nil
}

// aesDecrypt decrypts AES-256-GCM ciphertext produced by aesEncrypt.
func aesDecrypt(key, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("aes decrypt: %w", err)
	}

	return plaintext, nil
}
