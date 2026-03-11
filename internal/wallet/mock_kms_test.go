package wallet_test

import (
	"context"

	"github.com/vaultkey/vaultkey/internal/wallet"
)

// mockKMS is a pass-through KMS used in unit tests. It XORs the plaintext
// with a fixed byte so Encrypt/Decrypt round-trip correctly without needing Vault.
type mockKMS struct{}

func (m *mockKMS) Encrypt(_ context.Context, plaintext []byte) ([]byte, error) {
	out := make([]byte, len(plaintext))
	for i, b := range plaintext {
		out[i] = b ^ 0xAB
	}
	return out, nil
}

func (m *mockKMS) Decrypt(_ context.Context, ciphertext []byte) ([]byte, error) {
	out := make([]byte, len(ciphertext))
	for i, b := range ciphertext {
		out[i] = b ^ 0xAB
	}
	return out, nil
}

func (m *mockKMS) Health(_ context.Context) error { return nil }

// Ensure mockKMS satisfies the kms.KMS interface at compile time.
// wallet.NewService accepts a kms.KMS so we verify assignability via the
// constructor rather than a direct interface assertion.
var _ = wallet.NewService(&mockKMS{})