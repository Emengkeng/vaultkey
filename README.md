# VaultKey

Custodial wallet infrastructure built for scale. Generate EVM and Solana wallets, sign transactions asynchronously, relay transaction, and receive results via webhook. Built for exchanges, neobanks, and fintechs handling millions of users.

## Architecture

```
Client → API (returns job_id immediately)
              ↓
           Redis Queue (BRPOPLPUSH, exactly-once)
              ↓
         Worker Pool (signs in-process, key lives milliseconds)
              ↓
         Vault (decrypt DEK) → AES-256-GCM (decrypt key)
              ↓
         Webhook → your endpoint (HMAC-SHA256 signed)
```

Private keys are encrypted at rest (AES-256-GCM, per-wallet DEK, Vault-managed master key). Keys are decrypted in-process only during signing and wiped immediately after. No key ever leaves the service.

## Quick start

```bash
git clone https://github.com/yourorg/vaultkey
cd vaultkey
cp .env.example .env
# edit .env with strong passwords
docker compose up
```

On first run Vault auto-initializes. **Save the unseal keys printed to the console.** Loss of unseal keys = permanent loss of access to all encrypted wallets.

## API

All endpoints except `POST /projects` and `GET /health` require:
- `X-API-Key: your_api_key`
- `X-API-Secret: your_api_secret`

---

### Create a project

```
POST /projects
```
```json
{
  "name": "My Exchange",
  "webhook_url": "https://yourapp.com/webhooks/vaultkey",
  "rate_limit_rps": 500,
  "max_retries": 5
}
```

Response — **save the api_secret, it is shown once**:
```json
{
  "id": "proj_...",
  "api_key": "...",
  "api_secret": "..."
}
```

---

### Update webhook URL

```
PATCH /project/webhook
```
```json
{ "webhook_url": "https://yourapp.com/webhooks/vaultkey" }
```

---

### Create a wallet

```
POST /wallets
```
```json
{
  "user_id": "user_123",
  "chain_type": "evm",
  "label": "main"
}
```

One EVM wallet works across all EVM chains. Pass `chain_id` at signing time.

---

### Sign a transaction (async)

```
POST /wallets/{walletId}/sign/transaction/evm
POST /wallets/{walletId}/sign/transaction/solana
POST /wallets/{walletId}/sign/message/evm
POST /wallets/{walletId}/sign/message/solana
```

```json
{
  "idempotency_key": "optional-dedup-key",
  "payload": {
    "to": "0xRecipient",
    "value": "0xDE0B6B3A7640000",
    "gas_limit": 21000,
    "gas_price": "0x3B9ACA00",
    "nonce": 0,
    "chain_id": 1
  }
}
```

Returns **immediately** with HTTP 202:
```json
{ "job_id": "job_abc123", "status": "pending" }
```

---

### Poll job status

```
GET /jobs/{jobId}
```

Status values: `pending` → `processing` → `completed` | `failed` | `dead`

---

### Webhook delivery

When a job completes, VaultKey POSTs to your webhook URL:

```json
{
  "job_id": "job_abc123",
  "project_id": "proj_...",
  "wallet_id": "wallet_...",
  "operation": "sign_tx_evm",
  "status": "completed",
  "result": { "signed_tx": "0x..." },
  "timestamp": "2026-03-11T12:00:00Z"
}
```

Verify the payload is authentic:
```
X-VaultKey-Signature: sha256=<hmac_sha256_of_body>
X-VaultKey-Timestamp: <rfc3339>
```

Failed deliveries retry with exponential backoff (1s, 2s, 4s...) up to `max_retries`. After exhausting retries the job moves to the dead letter queue (`vaultkey:dlq` in Redis).

---

### Get balance

```
GET /wallets/{walletId}/balance?chain_id=1
```

---

### Broadcast

```
POST /wallets/{walletId}/broadcast
```
```json
{ "signed_tx": "0x...", "chain_id": "1" }
```

---

## Configuration

| Variable | Default | Description |
|---|---|---|
| `DATABASE_URL` | required | Postgres connection string |
| `VAULT_ADDR` | `http://vault:8200` | Vault address |
| `VAULT_TOKEN` / `VAULT_TOKEN_FILE` | required | Vault auth |
| `REDIS_ADDR` | `redis:6379` | Redis address |
| `REDIS_PASSWORD` | — | Redis auth |
| `WORKER_CONCURRENCY` | `10` | Signing worker goroutines |
| `WORKER_POLL_TIMEOUT_SEC` | `5` | BRPOPLPUSH block timeout |
| `EVM_RPC_{CHAIN_ID}` | Public RPCs | Per-chain RPC endpoint |
| `SOLANA_RPC_URL` | Solana mainnet | Solana RPC endpoint |

## Security

- **Keys at rest**: AES-256-GCM with per-wallet DEK. DEK encrypted by Vault transit engine (master key never leaves Vault).
- **Keys in memory**: Decrypted for signing only. Byte slices zeroed immediately after use.
- **API auth**: bcrypt-hashed secret. Constant-time comparison prevents timing attacks.
- **Rate limiting**: Per-project sliding window in Redis. Returns 429 with `Retry-After: 1` header.
- **Webhook auth**: HMAC-SHA256 signature on every payload. Verify with your webhook secret.
- **Network isolation**: Vault, Redis, and Postgres have no host port mappings. Unreachable from outside the Docker network.
- **Audit log**: Every wallet creation and signing operation persisted to `audit_log` table.
- **Idempotency**: Supply `idempotency_key` to prevent duplicate jobs on client retry.
- **Dead letter queue**: Exhausted jobs go to `vaultkey:dlq` in Redis for inspection and replay.

## Scaling

Horizontal scaling: run multiple API + worker instances pointing at the same Postgres and Redis. BRPOPLPUSH guarantees each job is processed by exactly one worker across any number of instances.

For the cloud/managed option: replace Vault with AWS KMS or GCP KMS (same interface, swap the adapter). Use customer-managed KMS keys so the operator, not VaultKey, controls the root of trust.

## License

