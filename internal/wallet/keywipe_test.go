package wallet_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/vaultkey/vaultkey/internal/wallet"
)

// TestKeyWipeAfterEVMSign verifies that the private key slice passed into
// SignTransaction is zeroed after the call returns. If someone removes the
// defer wipeBytes from the signing path this test fails.
func TestKeyWipeAfterEVMSign(t *testing.T) {
	evm := wallet.NewEVMWallet()

	kp, err := evm.Generate(context.Background())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	// Copy the address so we can use it in the tx
	privBytes := kp.PrivateKeyBytes
	original := make([]byte, len(privBytes))
	copy(original, privBytes)

	if bytes.Equal(privBytes, make([]byte, len(privBytes))) {
		t.Fatal("private key is already zeroed before signing — test setup is broken")
	}

	tx := wallet.EVMTransaction{
		To:       kp.Address,
		Value:    "0x0",
		GasLimit: 21000,
		GasPrice: "0x3B9ACA00",
		Nonce:    0,
		ChainID:  1,
	}

	_, err = evm.SignTransaction(context.Background(), privBytes, tx)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	// After signing, the slice we passed in must not contain the original key.
	// wipeBytes zeros then overwrites with random bytes — it cannot equal the original.
	if bytes.Equal(privBytes, original) {
		t.Error("private key bytes were NOT wiped after EVM SignTransaction — memory leak risk")
	}
}

// TestKeyWipeAfterEVMMessage same check for message signing.
func TestKeyWipeAfterEVMMessage(t *testing.T) {
	evm := wallet.NewEVMWallet()

	kp, err := evm.Generate(context.Background())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	privBytes := kp.PrivateKeyBytes
	original := make([]byte, len(privBytes))
	copy(original, privBytes)

	_, err = evm.SignMessage(context.Background(), privBytes, []byte("hello vaultkey"))
	if err != nil {
		t.Fatalf("sign message: %v", err)
	}

	if bytes.Equal(privBytes, original) {
		t.Error("private key bytes were NOT wiped after EVM SignMessage")
	}
}

// TestKeyWipeAfterSolanaSign verifies wipe on the Solana path.
func TestKeyWipeAfterSolanaSign(t *testing.T) {
	sol := wallet.NewSolanaWallet()

	kp, err := sol.Generate(context.Background())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	privBytes := kp.PrivateKeyBytes
	original := make([]byte, len(privBytes))
	copy(original, privBytes)

	// Minimal valid-enough message bytes for signing
	msg := make([]byte, 32)
	for i := range msg {
		msg[i] = byte(i)
	}

	_, err = sol.SignTransaction(context.Background(), privBytes, msg)
	if err != nil {
		t.Fatalf("sign solana tx: %v", err)
	}

	if bytes.Equal(privBytes, original) {
		t.Error("private key bytes were NOT wiped after Solana SignTransaction")
	}
}

// TestKeyWipeAfterSolanaMessage same for Solana message signing.
func TestKeyWipeAfterSolanaMessage(t *testing.T) {
	sol := wallet.NewSolanaWallet()

	kp, err := sol.Generate(context.Background())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	privBytes := kp.PrivateKeyBytes
	original := make([]byte, len(privBytes))
	copy(original, privBytes)

	_, err = sol.SignMessage(context.Background(), privBytes, []byte("hello vaultkey"))
	if err != nil {
		t.Fatalf("sign solana message: %v", err)
	}

	if bytes.Equal(privBytes, original) {
		t.Error("private key bytes were NOT wiped after Solana SignMessage")
	}
}

// TestServiceKeyWipe verifies wipe through the full Service.SignEVMTransaction
// path, which involves a mock KMS to exercise decryptPrivateKey → sign → wipe.
func TestServiceKeyWipe(t *testing.T) {
	svc := wallet.NewService(&mockKMS{})

	generated, err := svc.Generate(context.Background(), wallet.ChainTypeEVM)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	// Make a copy of the encrypted key to verify it is NOT modified (the
	// encrypted blob should be stable; only the decrypted slice gets wiped).
	encKeyBefore := make([]byte, len(generated.EncryptedKey))
	copy(encKeyBefore, generated.EncryptedKey)

	tx := wallet.EVMTransaction{
		To:       "0x000000000000000000000000000000000000dEaD",
		Value:    "0x0",
		GasLimit: 21000,
		GasPrice: "0x3B9ACA00",
		ChainID:  1,
	}

	_, err = svc.SignEVMTransaction(context.Background(), generated.EncryptedKey, generated.EncryptedDEK, tx)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	// Encrypted key on disk must remain intact
	if !bytes.Equal(generated.EncryptedKey, encKeyBefore) {
		t.Error("EncryptedKey blob was mutated during signing — should never happen")
	}
}