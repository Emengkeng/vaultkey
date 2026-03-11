package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
)

// Payload is the body sent to the developer's webhook endpoint.
type Payload struct {
	JobID     string          `json:"job_id"`
	ProjectID string          `json:"project_id"`
	WalletID  string          `json:"wallet_id"`
	Operation string          `json:"operation"`
	Status    string          `json:"status"`    // completed | failed | dead
	Result    json.RawMessage `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
	Timestamp string          `json:"timestamp"`
}

type Deliverer struct {
	client *http.Client
}

func New() *Deliverer {
	return &Deliverer{
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Deliver attempts to deliver a webhook payload with exponential backoff.
// maxRetries is configured per project.
// Returns nil if delivered successfully, error after all retries exhausted.
func (d *Deliverer) Deliver(ctx context.Context, url, secret string, payload Payload, maxRetries int) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal webhook payload: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// exponential backoff: 1s, 2s, 4s, 8s...
			backoff := time.Duration(math.Pow(2, float64(attempt-1))) * time.Second
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}

		if err := d.send(ctx, url, secret, body); err != nil {
			lastErr = err
			continue
		}

		return nil // delivered
	}

	return fmt.Errorf("webhook delivery failed after %d attempts: %w", maxRetries+1, lastErr)
}

func (d *Deliverer) send(ctx context.Context, url, secret string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "VaultKey-Webhook/1.0")

	// HMAC-SHA256 signature so the dev can verify the payload is from us
	if secret != "" {
		sig := sign(body, secret)
		req.Header.Set("X-VaultKey-Signature", "sha256="+sig)
	}

	// Timestamp to prevent replay attacks
	req.Header.Set("X-VaultKey-Timestamp", time.Now().UTC().Format(time.RFC3339))

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck

	// Treat 2xx as success, anything else as failure
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook endpoint returned status %d", resp.StatusCode)
	}

	return nil
}

// sign computes HMAC-SHA256 of body using secret.
// Dev verifies: hmac.Equal(computed, received).
func sign(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
