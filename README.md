# VaultKey

[![License: AGPL v3](https://img.shields.io/badge/License-AGPL%20v3-blue.svg)](https://www.gnu.org/licenses/agpl-3.0)
[![Status](https://img.shields.io/badge/status-active%20development-green)](https://github.com/Emengkeng/vaultkey)

**Open source custodial wallet infrastructure built for scale.** Generate EVM and Solana wallets, sign transactions asynchronously, relay transactions, and receive results via webhook. Built for exchanges, neobanks, and fintechs handling millions of users.

> 🚧 **Active Development** - This project is under active development. APIs may change. Check [CHANGELOG.md](CHANGELOG.md) for breaking changes and [Issues](https://github.com/Emengkeng/vaultkey/issues) for the roadmap.

## Why VaultKey?

- **Open Source & Auditable** - Full transparency for security-critical wallet infrastructure
- **Async by Design** - Non-blocking API with webhook delivery for high throughput
- **Battle-tested Crypto** - AES-256-GCM + Vault transit engine, keys in memory for milliseconds only
- **Horizontally Scalable** - Redis queue guarantees exactly-once processing across unlimited workers
- **Multi-chain** - EVM (Ethereum, Polygon, BSC, Arbitrum, etc.) and Solana support

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

## Quick Start

```bash
git clone https://github.com/Emengkeng/vaultkey
cd vaultkey
cp .env.example .env
# edit .env with strong passwords
docker compose up
```

On first run Vault auto-initializes. **Save the unseal keys printed to the console.** Loss of unseal keys = permanent loss of access to all encrypted wallets.

## API Reference

All endpoints except `POST /projects` and `GET /health` require:
- `X-API-Key: your_api_key`
- `X-API-Secret: your_api_secret`

---

### Create a Project

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

### Update Webhook URL

```
PATCH /project/webhook
```
```json
{ "webhook_url": "https://yourapp.com/webhooks/vaultkey" }
```

---

### Create a Wallet

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

### Sign a Transaction (Async)

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

### Poll Job Status

```
GET /jobs/{jobId}
```

Status values: `pending` → `processing` → `completed` | `failed` | `dead`

---

### Webhook Delivery

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

### Get Balance

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

**Horizontal scaling**: Run multiple API + worker instances pointing at the same Postgres and Redis. BRPOPLPUSH guarantees each job is processed by exactly one worker across any number of instances.

**Cloud/managed option**: Replace Vault with AWS KMS or GCP KMS (same interface, swap the adapter). Use customer-managed KMS keys so the operator, not VaultKey, controls the root of trust.

---

## Development

### Prerequisites
- Go 1.21+
- Docker & Docker Compose
- PostgreSQL 15+
- Redis 7+
- HashiCorp Vault

### Running Locally

```bash
# Clone and setup
git clone https://github.com/Emengkeng/vaultkey
cd vaultkey

# Install dependencies
go mod download

# Start infrastructure
docker compose up -d postgres redis vault

# Run migrations
make migrate-up

# Start API server
go run cmd/api/main.go

# Start workers (in another terminal)
go run cmd/worker/main.go
```

### Running Tests

```bash
# Unit tests
make test

# Integration tests (requires Docker)
make test-integration

# Load tests
make test-load
```

## Contributing

VaultKey is open source and contributions are welcome! Since we're in active development, please check existing [issues](https://github.com/Emengkeng/vaultkey/issues) and [discussions](https://github.com/Emengkeng/vaultkey/discussions) before starting work.

### How to Contribute

1. **Fork the repository**
2. **Create a feature branch** (`git checkout -b feature/amazing-feature`)
3. **Make your changes** with tests
4. **Run tests** (`make test`)
5. **Commit** (`git commit -m 'Add amazing feature'`)
6. **Push** (`git push origin feature/amazing-feature`)
7. **Open a Pull Request**

### Reporting Issues

Found a bug or have a feature request? [Open an issue](https://github.com/Emengkeng/vaultkey/issues/new) with:
- Clear description
- Steps to reproduce (for bugs)
- Expected vs actual behavior
- Environment details (OS, Go version, etc.)

**Security vulnerabilities**: Please report privately to [hello@juslen.site](mailto:hello@juslen.site) instead of opening a public issue.

## Roadmap

- [x] EVM wallet support
- [x] Solana wallet support
- [x] Async signing with webhooks
- [x] Horizontal scaling
- [ ] Bitcoin support
- [ ] Multi-sig wallets
- [ ] Hardware security module (HSM) integration
- [ ] GraphQL API
- [ ] WebSocket real-time updates
- [ ] Built-in transaction simulation
- [ ] Gas price oracle integration

See the [full roadmap](https://github.com/Emengkeng/vaultkey/issues) in our issue tracker.

## Self-Hosted vs Cloud

**Self-Hosted (This Repository)**
- ✅ Free and open source (AGPL v3)
- ✅ Full control over infrastructure
- ✅ Customer-managed encryption keys
- ✅ Deploy anywhere (AWS, GCP, bare metal)
- ⚠️ You manage: uptime, security, compliance, backups

**VaultKey Cloud** *(Coming Soon)*
- ✅ Managed infrastructure with 99.99% SLA
- ✅ SOC2 Type II compliant
- ✅ 24/7 enterprise support
- ✅ Automatic updates and security patches
- ✅ Multi-region redundancy
- ✅ Built-in monitoring and alerting

> Both versions use the **same open source codebase**. Cloud customers get the convenience of managed infrastructure, not proprietary features.


## License

VaultKey is licensed under the **GNU Affero General Public License v3.0 (AGPL-3.0)**.

See [LICENSE](LICENSE) for the full text.