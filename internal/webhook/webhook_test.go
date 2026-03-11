package webhook_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vaultkey/vaultkey/internal/webhook"
)

const testSecret = "super-secret-webhook-key"

// computeExpectedSig mirrors the sign() function inside the webhook package.
func computeExpectedSig(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// TestWebhookHMACSignatureCorrect verifies that the signature on a delivered
// webhook matches what the receiver would compute independently.
func TestWebhookHMACSignatureCorrect(t *testing.T) {
	var capturedSig string
	var capturedBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedSig = r.Header.Get("X-VaultKey-Signature")

		buf := make([]byte, r.ContentLength)
		r.Body.Read(buf) //nolint:errcheck
		capturedBody = buf

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	d := webhook.New()
	payload := webhook.Payload{
		JobID:     "job_abc",
		ProjectID: "proj_xyz",
		WalletID:  "wallet_123",
		Operation: "sign_tx_evm",
		Status:    "completed",
		Timestamp: "2026-01-01T00:00:00Z",
	}

	err := d.Deliver(t.Context(), server.URL, testSecret, payload, 0)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}

	if capturedSig == "" {
		t.Fatal("X-VaultKey-Signature header was not set")
	}

	expected := computeExpectedSig(capturedBody, testSecret)
	if capturedSig != expected {
		t.Errorf("signature mismatch\ngot:  %s\nwant: %s", capturedSig, expected)
	}
}

// TestWebhookHMACTamperedBody verifies that modifying the body after signing
// produces a different signature (tamper detection).
func TestWebhookHMACTamperedBody(t *testing.T) {
	payload := webhook.Payload{
		JobID:  "job_abc",
		Status: "completed",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	originalSig := computeExpectedSig(body, testSecret)

	// Tamper: change "completed" to "failed" in the raw bytes
	tampered := strings.Replace(string(body), "completed", "failed", 1)
	tamperedSig := computeExpectedSig([]byte(tampered), testSecret)

	if originalSig == tamperedSig {
		t.Error("tampered body produced the same HMAC signature — HMAC is broken")
	}
}

// TestWebhookHMACWrongSecret verifies that the wrong secret produces a
// different signature (key isolation).
func TestWebhookHMACWrongSecret(t *testing.T) {
	body := []byte(`{"job_id":"job_abc","status":"completed"}`)

	sig1 := computeExpectedSig(body, testSecret)
	sig2 := computeExpectedSig(body, "different-secret")

	if sig1 == sig2 {
		t.Error("different secrets produced the same HMAC signature")
	}
}

// TestWebhookNoSecretHeaderOmitted verifies that when no secret is configured,
// no X-VaultKey-Signature header is sent (don't send an empty/broken signature).
func TestWebhookNoSecretHeaderOmitted(t *testing.T) {
	var headers http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	d := webhook.New()
	payload := webhook.Payload{
		JobID:  "job_abc",
		Status: "completed",
	}

	err := d.Deliver(t.Context(), server.URL, "" /* no secret */, payload, 0)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}

	if sig := headers.Get("X-VaultKey-Signature"); sig != "" {
		t.Errorf("expected no signature header when secret is empty, got: %s", sig)
	}
}

// TestWebhookRetryOn5xx verifies that a 500 response causes a retry and
// eventual success when the server recovers.
func TestWebhookRetryOn5xx(t *testing.T) {
	attempts := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	d := webhook.New()
	payload := webhook.Payload{JobID: "job_retry", Status: "completed"}

	err := d.Deliver(t.Context(), server.URL, testSecret, payload, 3)
	if err != nil {
		t.Fatalf("deliver with retries: %v", err)
	}

	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

// TestWebhookExhaustedRetries verifies that a permanently failing endpoint
// returns an error after exhausting all retries.
func TestWebhookExhaustedRetries(t *testing.T) {
	attempts := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	d := webhook.New()
	payload := webhook.Payload{JobID: "job_dead", Status: "failed"}

	const maxRetries = 2
	err := d.Deliver(t.Context(), server.URL, testSecret, payload, maxRetries)
	if err == nil {
		t.Fatal("expected error after exhausted retries, got nil")
	}

	// maxRetries+1 total attempts (initial + retries)
	if attempts != maxRetries+1 {
		t.Errorf("expected %d attempts, got %d", maxRetries+1, attempts)
	}
}