package kms

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
)

// AWSKMS implements KMS using AWS Key Management Service.
//
// keyID can be any of:
//   - Key ID:     "1234abcd-12ab-34cd-56ef-1234567890ab"
//   - Key ARN:    "arn:aws:kms:us-east-1:123456789012:key/..."
//   - Alias name: "alias/vaultkey-master"
//   - Alias ARN:  "arn:aws:kms:us-east-1:123456789012:alias/..."
//
// Authentication uses the standard AWS credential chain:
// environment variables → shared credentials file → IAM role (EC2/ECS/EKS).
// In production: attach an IAM role to your compute instance with
// kms:Encrypt and kms:Decrypt permissions on the key.
type AWSKMS struct {
	client *kms.Client
	keyID  string
}

func NewAWS(ctx context.Context, keyID string, optFns ...func(*config.LoadOptions) error) (*AWSKMS, error) {
	if keyID == "" {
		return nil, fmt.Errorf("aws kms: keyID is required")
	}

	cfg, err := config.LoadDefaultConfig(ctx, optFns...)
	if err != nil {
		return nil, fmt.Errorf("aws kms: load config: %w", err)
	}

	return &AWSKMS{
		client: kms.NewFromConfig(cfg),
		keyID:  keyID,
	}, nil
}

func (a *AWSKMS) Encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	resp, err := a.client.Encrypt(ctx, &kms.EncryptInput{
		KeyId:     aws.String(a.keyID),
		Plaintext: plaintext,
	})
	if err != nil {
		return nil, fmt.Errorf("aws kms encrypt: %w", err)
	}

	// Base64-encode to match the storage contract used by VaultKMS and GCPKMS.
	encoded := base64.StdEncoding.EncodeToString(resp.CiphertextBlob)
	return []byte(encoded), nil
}

func (a *AWSKMS) Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(string(ciphertext))
	if err != nil {
		return nil, fmt.Errorf("aws kms decrypt: base64 decode: %w", err)
	}

	resp, err := a.client.Decrypt(ctx, &kms.DecryptInput{
		KeyId:          aws.String(a.keyID),
		CiphertextBlob: raw,
	})
	if err != nil {
		return nil, fmt.Errorf("aws kms decrypt: %w", err)
	}

	return resp.Plaintext, nil
}

// Health describes the key and checks it is enabled.
func (a *AWSKMS) Health(ctx context.Context) error {
	resp, err := a.client.DescribeKey(ctx, &kms.DescribeKeyInput{
		KeyId: aws.String(a.keyID),
	})
	if err != nil {
		return fmt.Errorf("aws kms health: %w", err)
	}

	if resp.KeyMetadata == nil {
		return fmt.Errorf("aws kms health: no key metadata returned")
	}

	state := string(resp.KeyMetadata.KeyState)
	if state != "Enabled" {
		return fmt.Errorf("aws kms health: key state is %s, expected Enabled", state)
	}

	return nil
}