package kms

import "context"

// KMS is the interface every key management backend must implement.
// The application only talks to this interface - never to Vault or AWS directly.
type KMS interface {
	// Encrypt encrypts plaintext using the master key.
	// Returns opaque ciphertext safe to store in the database.
	Encrypt(ctx context.Context, plaintext []byte) (ciphertext []byte, err error)

	// Decrypt decrypts ciphertext previously produced by Encrypt.
	Decrypt(ctx context.Context, ciphertext []byte) (plaintext []byte, err error)

	// Health checks connectivity and auth to the KMS backend.
	Health(ctx context.Context) error
}
