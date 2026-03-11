# VaultKey Architecture Documentation

## System Overview

VaultKey is a production-grade custodial wallet infrastructure designed for exchanges, neobanks, and fintechs managing millions of users. It provides:

- **Multi-chain support**: EVM (Ethereum, Polygon, Arbitrum, Base, Optimism) and Solana
- **Async signing**: Non-blocking API returns job IDs immediately, webhook delivers results
- **Gasless transactions**: Optional relayer wallets pay gas on behalf of users
- **Enterprise security**: Keys encrypted at rest (AES-256-GCM + Vault), decrypted only during signing
- **Horizontal scaling**: Stateless API + worker pool architecture

---

## Architecture Diagram

```
┌─────────────────────────────────────────────────────────────┐
│                        Client Application                    │
└────────────────┬────────────────────────────────────────────┘
                 │ HTTPS
                 ▼
┌─────────────────────────────────────────────────────────────┐
│                      VaultKey API (Go)                       │
│  ┌────────────┐  ┌──────────┐  ┌─────────────────────┐     │
│  │ Auth       │  │ Rate     │  │ Handlers            │     │
│  │ Middleware │→ │ Limiter  │→ │ - Wallets           │     │
│  │            │  │ (Redis)  │  │ - Signing Jobs      │     │
│  └────────────┘  └──────────┘  │ - Relayer Mgmt      │     │
│                                 │ - Balance/Broadcast │     │
│                                 └──────────┬──────────┘     │
└─────────────────────────────────────────────┼──────────────┘
                                              │
                 ┌────────────────────────────┼──────────────┐
                 │                            │              │
                 ▼                            ▼              ▼
        ┌────────────────┐          ┌────────────┐  ┌──────────────┐
        │   PostgreSQL   │          │   Redis    │  │  Vault KMS   │
        │   Database     │          │   Queue    │  │  (Transit)   │
        │                │          │            │  │              │
        │ - Projects     │          │ - Jobs     │  │ - Master Key │
        │ - Wallets      │          │ - Nonces   │  │ - Encrypt    │
        │ - Signing Jobs │          │ - Rate     │  │ - Decrypt    │
        │ - Audit Log    │          │   Limits   │  │   DEKs       │
        │ - Relayers     │          └──────┬─────┘  └──────────────┘
        └────────────────┘                 │
                                           │ BRPOPLPUSH
                 ┌─────────────────────────┘
                 ▼
        ┌────────────────────────────────────────┐
        │       Worker Pool (10 goroutines)      │
        │                                         │
        │  ┌──────────────────────────────────┐  │
        │  │  For each job:                   │  │
        │  │  1. Fetch wallet from DB         │  │
        │  │  2. Decrypt DEK via Vault        │  │
        │  │  3. Decrypt private key (AES)    │  │
        │  │  4. Sign transaction/message     │  │
        │  │  5. Wipe key from memory         │  │
        │  │  6. Store result in DB           │  │
        │  │  7. Deliver webhook              │  │
        │  └──────────────────────────────────┘  │
        └────────────────────────────────────────┘
                          │
                          ▼
                ┌───────────────────┐
                │  Developer's      │
                │  Webhook Endpoint │
                │  (HMAC verified)  │
                └───────────────────┘
                          │
                          ▼
                ┌───────────────────┐
                │  Blockchain RPC   │
                │  - Ethereum       │
                │  - Polygon        │
                │  - Solana         │
                │  - etc.           │
                └───────────────────┘
```

---

## Component Breakdown

### 1. API Layer (`cmd/api/main.go`, `internal/api/handlers/`)

**Responsibilities:**
- HTTP request handling
- Authentication (API key + secret)
- Rate limiting (per-project, sliding window)
- Request validation
- Immediate response to clients

**Key Endpoints:**
```
POST   /projects                          → Create project (returns API credentials)
PATCH  /project/webhook                   → Update webhook URL

POST   /projects/relayer                  → Register relayer wallet (gasless tx support)
GET    /projects/relayer                  → Get relayer balance/health
GET    /projects/relayers                 → List all relayers
DELETE /projects/relayer/{id}             → Deactivate relayer

POST   /wallets                           → Create user wallet
GET    /wallets/{id}                      → Get wallet details
GET    /users/{id}/wallets                → List user's wallets

POST   /wallets/{id}/sign/transaction/evm     → Async sign EVM tx (returns job_id)
POST   /wallets/{id}/sign/message/evm         → Async sign EVM message
POST   /wallets/{id}/sign/transaction/solana  → Async sign Solana tx
POST   /wallets/{id}/sign/message/solana      → Async sign Solana message

GET    /jobs/{id}                         → Poll job status
GET    /wallets/{id}/balance              → Get wallet balance
POST   /wallets/{id}/broadcast            → Broadcast signed transaction
GET    /health                            → Health check
```

### 2. Queue System (`internal/queue/`)

**Technology:** Redis (BRPOPLPUSH)

**Why BRPOPLPUSH:**
- Atomic operation: pop from main queue, push to processing queue
- Guarantees each job processed exactly once
- If worker crashes, job remains in processing queue for recovery
- No job is lost

**Queues:**
- `vaultkey:jobs` - Main job queue (FIFO)
- `vaultkey:jobs:processing` - In-flight jobs
- `vaultkey:dlq` - Dead letter queue (exhausted retries)

**Operations:**
```go
Enqueue(job)      // Push to main queue
Dequeue(timeout)  // Block until job available (BRPOPLPUSH)
Acknowledge(job)  // Remove from processing queue (success)
Requeue(job)      // Move back to main queue (retry)
MoveToDLQ(job)    // Exhausted retries
RecoverStalled()  // Requeue jobs stuck in processing (crash recovery)
```

### 3. Worker Pool (`internal/worker/`)

**Configuration:**
- Default: 10 concurrent workers (configurable via `WORKER_CONCURRENCY`)
- Each worker is a goroutine running an infinite loop

**Job Processing Flow:**
```
1. BRPOPLPUSH from main queue to processing queue
2. Mark job as "processing" in DB (increment attempts)
3. Fetch wallet from DB
4. Route to signing function:
   - Regular signing: Direct wallet signature
   - Gasless signing: Relay through relayer wallet
5. On success:
   - Store result in DB (status="completed")
   - Acknowledge job (remove from processing queue)
   - Deliver webhook
6. On failure:
   - If attempts < max_retries: Requeue
   - If attempts >= max_retries: Move to DLQ
```

### 4. Wallet Service (`internal/wallet/`)

**Key Management:**
- Each wallet has a unique Data Encryption Key (DEK)
- DEK is encrypted by Vault's master key
- Private key is encrypted by DEK using AES-256-GCM

**Encryption Flow:**
```
1. Generate new private key (EVM or Solana)
2. Generate random 32-byte DEK
3. Encrypt private key with DEK (AES-256-GCM)
4. Encrypt DEK with Vault master key
5. Store: {encrypted_key, encrypted_dek}
6. Wipe DEK and private key from memory
```

**Signing Flow:**
```
1. Fetch {encrypted_key, encrypted_dek} from DB
2. Decrypt DEK via Vault API
3. Decrypt private key using DEK
4. Sign transaction/message
5. Wipe DEK and private key from memory immediately
```

**Key Wiping:**
```go
// Zero bytes
for i := range b { b[i] = 0 }
// Overwrite with random
rand.Read(b)
```

### 5. Relayer Service (`internal/relayer/`)

**Purpose:** Pay gas on behalf of user wallets (gasless transactions)

**Architecture:**
- One relayer wallet per project per chain
- Relayer is a standard wallet (same encryption, same key storage)
- Relayer signs transactions as the submitter

**EVM Gasless Flow:**
```
1. User submits gasless transaction request
2. Fetch relayer wallet for chain
3. Check relayer balance (must be > 0.05 ETH)
4. Get next nonce from Redis (atomic INCR)
5. Build transaction with relayer as sender
6. Sign with relayer's private key
7. Return signed transaction for broadcast
```

**Solana Gasless Flow:**
```
1. User submits gasless transaction request
2. Fetch relayer wallet
3. Check relayer balance (must be > 0.05 SOL)
4. Fetch recent blockhash from chain
5. Build transaction with 2 signers:
   - Fee payer: relayer (pays transaction fee)
   - Authority: user (authorizes transfer)
6. Sign with both keys server-side
7. Broadcast immediately
8. Return transaction signature
```

**Nonce Management (EVM only):**
- Redis counter per relayer per chain
- Atomic INCR for each transaction
- Sync from chain on startup and errors
- Prevents nonce gaps under concurrent load

### 6. Storage Layer (`internal/storage/`)

**Tables:**

**projects**
- Top-level tenant (one per developer/company)
- API credentials (bcrypt-hashed secret)
- Webhook configuration
- Rate limits and retry policies

**wallets**
- Project-scoped user wallets
- Encrypted key material
- Chain type (evm or solana)
- Label (optional, for UI)

**signing_jobs**
- Async signing requests
- Status: pending → processing → completed/failed/dead
- Idempotency key support (deduplication)
- Gasless flag
- Webhook delivery status

**relayer_wallets**
- One per project per chain
- References underlying wallet for key storage
- Balance threshold alerts
- Active/inactive flag

**audit_log**
- All wallet operations
- All signing operations
- Relayer activity
- Immutable append-only log

### 7. RPC Manager (`internal/rpc/`)

**Responsibilities:**
- Blockchain RPC communication
- Balance queries
- Transaction broadcasting
- Gas price estimation
- Nonce fetching
- Blockhash retrieval (Solana)

**Endpoints Configuration:**
```env
EVM_RPC_1=https://cloudflare-eth.com        # Ethereum
EVM_RPC_137=https://polygon-rpc.com         # Polygon
EVM_RPC_42161=https://arb1.arbitrum.io/rpc  # Arbitrum
EVM_RPC_8453=https://mainnet.base.org       # Base
EVM_RPC_10=https://mainnet.optimism.io      # Optimism
SOLANA_RPC_URL=https://api.mainnet-beta.solana.com
```

### 8. KMS Layer (`internal/kms/`)

**Interface:**
```go
type KMS interface {
    Encrypt(ctx, plaintext) (ciphertext, error)
    Decrypt(ctx, ciphertext) (plaintext, error)
    Health(ctx) error
}
```

**Implementations:**
- `VaultKMS` - HashiCorp Vault (default)
- Extensible to AWS KMS, GCP KMS, Azure Key Vault

**Vault Configuration:**
- Transit secrets engine
- AES-256-GCM encryption
- Key named `vaultkey-master` (configurable)
- Non-exportable key (never leaves Vault)

---

## Security Model

### Layers of Protection

**1. At Rest:**
- Private keys: AES-256-GCM encrypted with DEK
- DEK: Encrypted by Vault master key
- API secrets: bcrypt hashed (cost 10)
- Webhook secrets: Stored plaintext (used for HMAC signing)

**2. In Transit:**
- HTTPS for all external communication
- Internal Docker network isolation
- Vault, Redis, Postgres unreachable from host

**3. In Memory:**
- Private keys decrypted only during signing
- Byte slices zeroed immediately after use
- DEKs never persisted to disk unencrypted

**4. Access Control:**
- API key + secret required for all operations
- Per-project isolation (can't access other projects' wallets)
- Rate limiting (default 100 RPS per project)

**5. Auditability:**
- All operations logged to `audit_log` table
- Immutable append-only log
- Includes: operation type, actor, timestamp, metadata

### Attack Scenarios & Mitigations

**Scenario 1: Database Compromised**
- Attacker gets encrypted keys + encrypted DEKs
- Cannot decrypt without Vault access
- Mitigation: Vault is network-isolated, requires separate credential

**Scenario 2: Vault Compromised**
- Attacker can decrypt DEKs
- Still needs encrypted keys from database
- Mitigation: Defense in depth, monitor Vault audit logs

**Scenario 3: Memory Dump**
- Attacker dumps worker process memory mid-signing
- Private key may be in memory for ~100ms
- Mitigation: Minimize key lifetime, wipe after use

**Scenario 4: API Credential Leak**
- Attacker gets API key + secret
- Can create wallets, submit signing jobs
- Mitigation: Rate limiting, webhook delivery to developer's endpoint (they detect anomalies), audit logs

**Scenario 5: Insider Threat**
- Operator with Vault access attempts to export keys
- Vault master key is marked non-exportable
- Mitigation: Use customer-managed KMS (AWS/GCP), operator never has root of trust

---

## Operational Procedures

### Initial Setup

1. **Start infrastructure:**
```bash
docker compose up -d
```

2. **Save Vault keys:**
```bash
# Printed to console on first run
# Save to password manager immediately
Unseal Key 1: ...
Unseal Key 2: ...
Unseal Key 3: ...
Root Token: ...
```

3. **Create first project:**
```bash
curl -X POST http://localhost:8080/projects \
  -H "Content-Type: application/json" \
  -d '{
    "name": "My Exchange",
    "webhook_url": "https://myapp.com/webhooks/vaultkey",
    "rate_limit_rps": 500,
    "max_retries": 5
  }'
```

4. **Save API credentials** (shown once):
```json
{
  "api_key": "...",
  "api_secret": "..."
}
```

### Vault Unsealing

If Vault becomes sealed (after restart):
```bash
docker exec -it vaultkey-vault-1 vault operator unseal <key1>
docker exec -it vaultkey-vault-1 vault operator unseal <key2>
```

Or run the init script again:
```bash
docker exec vaultkey-vault-init-1 sh /scripts/vault-init.sh
```

### Monitoring

**Health Check:**
```bash
curl http://localhost:8080/health
```

**Queue Depth:**
```bash
docker exec vaultkey-redis-1 redis-cli -a <password> LLEN vaultkey:jobs
```

**Dead Letter Queue:**
```bash
docker exec vaultkey-redis-1 redis-cli -a <password> LLEN vaultkey:dlq
```

**Database Metrics:**
```sql
-- Pending jobs
SELECT COUNT(*) FROM signing_jobs WHERE status = 'pending';

-- Failed jobs
SELECT COUNT(*) FROM signing_jobs WHERE status = 'failed';

-- Webhook failures
SELECT COUNT(*) FROM signing_jobs WHERE webhook_status = 'failed';
```

### Disaster Recovery

**Scenario: Vault data lost**
- All wallets are permanently inaccessible
- No recovery possible without Vault unseal keys + data
- **Prevention:** Daily backup of `/vault/data/` directory

**Scenario: Database lost**
- Wallet addresses and job history lost
- Private keys still encrypted in database backups
- **Recovery:** Restore from PostgreSQL backup

**Scenario: Redis lost**
- In-flight jobs lost
- Nonce counters reset
- **Recovery:** Restart workers, they resync nonces from chain

---

## Performance Characteristics

### Throughput

**Single Instance:**
- API: 1,000+ requests/sec (limited by bcrypt auth)
- Workers: ~100 signatures/sec (10 workers × 10 sig/sec each)
- Database: 5,000+ writes/sec (PostgreSQL with SSDs)

**Bottlenecks:**
1. **Vault API:** ~500 decrypt/sec (network round trip)
2. **bcrypt auth:** ~100 auth/sec (CPU-bound)
3. **Blockchain RPC:** Variable (external dependency)

### Latency

**Wallet Creation:**
- p50: 150ms
- p99: 300ms
- (1 Vault encrypt + 1 DB write)

**Job Submission:**
- p50: 50ms
- p99: 100ms
- (Returns immediately, no signing yet)

**Signing (worker):**
- p50: 200ms
- p99: 500ms
- (1 DB read + 1 Vault decrypt + signing + 1 DB write)

**Webhook Delivery:**
- p50: 100ms
- p99: 2s
- (Depends on developer's endpoint)

### Scaling

**Horizontal Scaling:**
```yaml
# Add more API instances
docker compose up -d --scale api=5

# Add more worker instances
docker compose up -d --scale worker=3
```

All instances share:
- Single PostgreSQL database
- Single Redis queue
- Single Vault instance

**Limits:**
- PostgreSQL: 50 connections per instance (default)
- Redis: 20 connections per instance (default)
- Vault: No connection limit (HTTP)

**Production Recommendations:**
- Run 3-5 API instances behind load balancer
- Run 2-3 worker instances (auto-restart on failure)
- PostgreSQL: Use managed service (RDS, Cloud SQL)
- Redis: Use managed service (ElastiCache, Cloud Memorystore)
- Vault: Use managed KMS (AWS KMS, GCP KMS) instead

---

## Troubleshooting

### Common Issues

**1. Vault health check failing**
```
Error: vault health check failed: connection refused
```
Solution: Ensure Vault is unsealed
```bash
docker compose restart vault-init
```

**2. Jobs stuck in "processing"**
```
Problem: Jobs never complete
```
Solution: Recover stalled jobs
```bash
# Workers do this automatically on startup
# Manual recovery:
docker compose restart api
```

**3. Webhook delivery failing**
```
Problem: webhook_status = 'failed'
```
Debug:
- Check developer's endpoint is reachable
- Verify HMAC signature validation
- Check webhook endpoint returns 2xx status

**4. Nonce too low (EVM)**
```
Error: nonce too low
```
Solution: Resync nonce from chain
- Happens automatically on error
- Manual: Redis DEL vaultkey:nonce:{chainId}:{address}

**5. Rate limit exceeded**
```
Error: 429 Too Many Requests
```
Solution: Increase project rate limit
```sql
UPDATE projects SET rate_limit_rps = 1000 WHERE id = '...';
```

---

## Future Enhancements

### Planned Features

1. **MPC (Multi-Party Computation)**
   - Threshold signatures (2-of-3, 3-of-5)
   - No single point of key compromise

2. **Hardware Security Modules (HSM)**
   - FIPS 140-2 Level 3 compliance
   - Physical key protection

3. **Policy Engine**
   - Transaction amount limits
   - Whitelist/blacklist addresses
   - Time-based restrictions
   - Multi-approval workflows

4. **Advanced Monitoring**
   - Prometheus metrics
   - Grafana dashboards
   - Alert manager integration

5. **SDK Libraries**
   - Python SDK
   - Node.js SDK
   - Go SDK

6. **Additional Chains**
   - Bitcoin
   - Cosmos
   - Near
   - Aptos

---

## Compliance & Certifications

### Current Status

- **SOC 2:** Not yet audited
- **PCI DSS:** Not applicable (no credit card data)
- **GDPR:** Compliant (user_id is opaque identifier)
- **ISO 27001:** Not yet certified

### Audit Log Requirements

All operations are logged to `audit_log` table:
- Wallet creation
- Signing operations
- Relayer activity
- API authentication failures

Retention: Indefinite (customer responsibility to archive)

---

## License

MIT License - See LICENSE file for details

---

## Support

- **Documentation:** https://docs.vaultkey.io (TODO)
- **GitHub Issues:** https://github.com/yourorg/vaultkey/issues
- **Email:** support@vaultkey.io
- **Discord:** https://discord.gg/vaultkey (TODO)