# Running the Tests

### Unit tests (no Docker required)

```bash
# Key wipe tests
go test -v ./internal/wallet/... -run TestKeyWipe

# Webhook HMAC tests
go test -v ./internal/webhook/...

# Run all unit tests with race detector
go test -race ./internal/wallet/... ./internal/webhook/...
```

### Integration tests (requires Docker)

```bash
# Nonce concurrency + resync (Redis container)
go test -race -v ./internal/nonce/...

# Queue crash recovery (Redis container)
go test -v ./internal/queue/...

# Idempotency deduplication (Postgres container)
go test -v ./internal/storage/...
```

### All tests

```bash
go test -race ./...
```

## Test file locations

Place each file in the corresponding package directory:

| File | Destination |
|------|-------------|
| `wallet/keywipe_test.go` | `internal/wallet/keywipe_test.go` |
| `wallet/mock_kms_test.go` | `internal/wallet/mock_kms_test.go` |
| `nonce/nonce_test.go` | `internal/nonce/nonce_test.go` |
| `webhook/webhook_test.go` | `internal/webhook/webhook_test.go` |
| `queue/queue_test.go` | `internal/queue/queue_test.go` |
| `storage/idempotency_test.go` | `internal/storage/idempotency_test.go` |

## What each test covers

| Test | Type | What it catches |
|------|------|-----------------|
| `TestKeyWipeAfterEVMSign` | Unit | `defer wipeBytes` removed from signing path |
| `TestKeyWipeAfterSolanaSign` | Unit | Same on Solana path |
| `TestServiceKeyWipe` | Unit | Wipe through full `wallet.Service` |
| `TestNonceConcurrentUniqueness` | Integration | Race condition in Redis INCR; run with `-race` |
| `TestNonceResyncCorrectness` | Integration | Stale Redis counter after broadcast failure |
| `TestNoncePeekDoesNotAdvance` | Integration | Peek has no side effects |
| `TestWebhookHMACSignatureCorrect` | Unit | Signature matches receiver's independent computation |
| `TestWebhookHMACTamperedBody` | Unit | Tampered payload detected |
| `TestWebhookRetryOn5xx` | Unit | Retry logic fires on 5xx |
| `TestWebhookExhaustedRetries` | Unit | Error returned after max retries |
| `TestCrashRecovery` | Integration | Stalled job moves back to main queue exactly once |
| `TestEnqueueDequeueOrder` | Integration | FIFO order maintained |
| `TestIdempotencyDeduplication` | Integration | Same key → same job ID, no duplicate row |
| `TestIdempotencyNilKeyAlwaysCreatesNewJob` | Integration | Nil keys not over-deduplicated |