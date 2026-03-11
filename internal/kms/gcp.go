package kms

import (
	"context"
	"encoding/base64"
	"fmt"

	kmsapi "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"google.golang.org/api/option"
)

// GCPKMS implements KMS using Google Cloud KMS.
//
// keyName must be the full resource name:
// projects/{project}/locations/{location}/keyRings/{ring}/cryptoKeys/{key}/cryptoKeyVersions/{version}
//
// Authentication is handled by Application Default Credentials (ADC).
// In production: attach a service account to the GKE node or Cloud Run instance.
// Locally: set GOOGLE_APPLICATION_CREDENTIALS to a service account key file,
// or run: gcloud auth application-default login
type GCPKMS struct {
	client  *kmsapi.KeyManagementClient
	keyName string
}

func NewGCP(ctx context.Context, keyName string, opts ...option.ClientOption) (*GCPKMS, error) {
	if keyName == "" {
		return nil, fmt.Errorf("gcp kms: keyName is required")
	}

	client, err := kmsapi.NewKeyManagementClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("gcp kms: create client: %w", err)
	}

	return &GCPKMS{
		client:  client,
		keyName: keyName,
	}, nil
}

func (g *GCPKMS) Encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	resp, err := g.client.Encrypt(ctx, &kmspb.EncryptRequest{
		Name:      g.keyName,
		Plaintext: plaintext,
	})
	if err != nil {
		return nil, fmt.Errorf("gcp kms encrypt: %w", err)
	}

	// Base64-encode so ciphertext is safe to store as TEXT in Postgres,
	// consistent with how Vault returns its ciphertext blob.
	encoded := base64.StdEncoding.EncodeToString(resp.Ciphertext)
	return []byte(encoded), nil
}

func (g *GCPKMS) Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(string(ciphertext))
	if err != nil {
		return nil, fmt.Errorf("gcp kms decrypt: base64 decode: %w", err)
	}

	resp, err := g.client.Decrypt(ctx, &kmspb.DecryptRequest{
		Name:       g.keyName,
		Ciphertext: raw,
	})
	if err != nil {
		return nil, fmt.Errorf("gcp kms decrypt: %w", err)
	}

	return resp.Plaintext, nil
}

// Health checks that the key is accessible and enabled.
func (g *GCPKMS) Health(ctx context.Context) error {
	_, err := g.client.GetCryptoKey(ctx, &kmspb.GetCryptoKeyRequest{
		Name: g.keyName,
	})
	if err != nil {
		return fmt.Errorf("gcp kms health: %w", err)
	}
	return nil
}