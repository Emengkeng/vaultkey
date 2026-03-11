package kms

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type VaultKMS struct {
	addr      string
	token     string
	mountPath string
	keyName   string
	client    *http.Client
}

func NewVault(addr, token, mountPath, keyName string) *VaultKMS {
	return &VaultKMS{
		addr:      addr,
		token:     token,
		mountPath: mountPath,
		keyName:   keyName,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (v *VaultKMS) Encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	encoded := base64.StdEncoding.EncodeToString(plaintext)

	body, _ := json.Marshal(map[string]string{
		"plaintext": encoded,
	})

	url := fmt.Sprintf("%s/v1/%s/encrypt/%s", v.addr, v.mountPath, v.keyName)
	resp, err := v.do(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, fmt.Errorf("vault encrypt request: %w", err)
	}

	var result struct {
		Data struct {
			Ciphertext string `json:"ciphertext"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("vault encrypt parse response: %w", err)
	}

	if result.Data.Ciphertext == "" {
		return nil, fmt.Errorf("vault returned empty ciphertext")
	}

	return []byte(result.Data.Ciphertext), nil
}

func (v *VaultKMS) Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	body, _ := json.Marshal(map[string]string{
		"ciphertext": string(ciphertext),
	})

	url := fmt.Sprintf("%s/v1/%s/decrypt/%s", v.addr, v.mountPath, v.keyName)
	resp, err := v.do(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, fmt.Errorf("vault decrypt request: %w", err)
	}

	var result struct {
		Data struct {
			Plaintext string `json:"plaintext"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("vault decrypt parse response: %w", err)
	}

	decoded, err := base64.StdEncoding.DecodeString(result.Data.Plaintext)
	if err != nil {
		return nil, fmt.Errorf("vault decrypt base64 decode: %w", err)
	}

	return decoded, nil
}

func (v *VaultKMS) Health(ctx context.Context) error {
	url := fmt.Sprintf("%s/v1/sys/health", v.addr)
	_, err := v.do(ctx, http.MethodGet, url, nil)
	return err
}

func (v *VaultKMS) do(ctx context.Context, method, url string, body []byte) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, err
	}

	req.Header.Set("X-Vault-Token", v.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := v.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vault http: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("vault read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("vault returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}
