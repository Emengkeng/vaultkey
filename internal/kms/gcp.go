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
// Authentication is resolved in this order:
//  1. GOOGLE_APPLICATION_CREDENTIALS_JSON — full service account JSON as an env var.
//     Use this when running outside GCP without a file mount.
//  2. GOOGLE_APPLICATION_CREDENTIALS — path to a service account key file.
//     Use this when you have the file available locally.
//  3. Application Default Credentials (ADC) — automatic on GCP infrastructure
//     (Cloud Run, GKE with Workload Identity, GCE). No config needed.
type GCPKMS struct {
	client  *kmsapi.KeyManagementClient
	keyName string
}

// GCPOptions holds authentication options for the GCP KMS adapter.
// All fields are optional — leave them empty to fall through to ADC.
type GCPOptions struct {
	// CredentialsJSON is the full contents of a service account key JSON file.
	// Maps to the GOOGLE_APPLICATION_CREDENTIALS_JSON env var.
	CredentialsJSON string

	// CredentialsFile is a path to a service account key JSON file.
	// Maps to the GOOGLE_APPLICATION_CREDENTIALS env var.
	// Ignored if CredentialsJSON is set.
	CredentialsFile string
}

func NewGCP(ctx context.Context, keyName string, gcpOpts GCPOptions) (*GCPKMS, error) {
	if keyName == "" {
		return nil, fmt.Errorf("gcp kms: keyName is required")
	}

	var clientOpts []option.ClientOption

	switch {
	case gcpOpts.CredentialsJSON != "":
		clientOpts = append(clientOpts, option.WithCredentialsJSON([]byte(gcpOpts.CredentialsJSON)))
	case gcpOpts.CredentialsFile != "":
		clientOpts = append(clientOpts, option.WithCredentialsFile(gcpOpts.CredentialsFile))
	// default: ADC — no option needed, the SDK handles it
	}

	client, err := kmsapi.NewKeyManagementClient(ctx, clientOpts...)
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

func (g *GCPKMS) Health(ctx context.Context) error {
	_, err := g.client.GetCryptoKey(ctx, &kmspb.GetCryptoKeyRequest{
		Name: g.keyName,
	})
	if err != nil {
		return fmt.Errorf("gcp kms health: %w", err)
	}
	return nil
}