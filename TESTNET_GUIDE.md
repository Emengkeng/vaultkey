# Testnet Configuration Guide

## Overview

VaultKey supports both testnet and mainnet deployments. This guide shows how to configure the system for testnet development before moving to mainnet production.

---

## Why Start with Testnet?

✅ **Free test tokens** - No real money needed
✅ **Safe testing** - Mistakes don't cost real funds
✅ **Faster iteration** - Test all features without financial risk
✅ **Identical behavior** - Same code paths as mainnet

---

## Testnet vs Mainnet Configuration

The **only difference** is the RPC endpoints. Everything else (database, Vault, Redis, API) remains the same.

### Network IDs

| Network | Mainnet Chain ID | Testnet Chain ID | Testnet Name |
|---------|-----------------|------------------|--------------|
| Ethereum | 1 | 11155111 | Sepolia |
| Polygon | 137 | 80001 | Mumbai |
| Arbitrum | 42161 | 421614 | Arbitrum Sepolia |
| Base | 8453 | 84532 | Base Sepolia |
| Optimism | 10 | 11155420 | Optimism Sepolia |
| Solana | mainnet-beta | devnet | Devnet |

---

## Testnet Setup

### Step 1: Create Testnet Environment File

Create `.env.testnet`:

```bash
# PostgreSQL (same for testnet and mainnet)
POSTGRES_PASSWORD=your_secure_password_here

# Redis (same for testnet and mainnet)
REDIS_PASSWORD=your_redis_password_here

# Worker config (same for testnet and mainnet)
WORKER_CONCURRENCY=10

# ════════════════════════════════════════════════════════
#  TESTNET RPC ENDPOINTS
# ════════════════════════════════════════════════════════

# Ethereum Sepolia (testnet)
EVM_RPC_11155111=https://eth-sepolia.public.blastapi.io
# Alternative: https://rpc.sepolia.org

# Polygon Mumbai (testnet)
EVM_RPC_80001=https://rpc-mumbai.maticvigil.com
# Alternative: https://polygon-mumbai.g.alchemy.com/v2/YOUR_API_KEY

# Arbitrum Sepolia (testnet)
EVM_RPC_421614=https://sepolia-rollup.arbitrum.io/rpc

# Base Sepolia (testnet)
EVM_RPC_84532=https://sepolia.base.org

# Optimism Sepolia (testnet)
EVM_RPC_11155420=https://sepolia.optimism.io

# Solana Devnet (testnet)
SOLANA_RPC_URL=https://api.devnet.solana.com
# Alternative: https://solana-devnet.g.alchemy.com/v2/YOUR_API_KEY
```

### Step 2: Update docker-compose.yml

Replace the `api` service environment section:

```yaml
  api:
    # ... other config ...
    environment:
      PORT: 8080
      DATABASE_URL: postgres://vaultkey:${POSTGRES_PASSWORD:-changeme}@postgres:5432/vaultkey?sslmode=disable
      VAULT_ADDR: http://vault:8200
      VAULT_TOKEN_FILE: /vault/data/root_token
      VAULT_MOUNT_PATH: transit
      VAULT_KEY_NAME: vaultkey-master
      REDIS_ADDR: redis:6379
      REDIS_PASSWORD: ${REDIS_PASSWORD:-redischangeme}
      WORKER_CONCURRENCY: ${WORKER_CONCURRENCY:-10}
      WORKER_POLL_TIMEOUT_SEC: 5

      # TESTNET RPC endpoints
      EVM_RPC_11155111: ${EVM_RPC_11155111}  # Sepolia
      EVM_RPC_80001: ${EVM_RPC_80001}        # Mumbai
      EVM_RPC_421614: ${EVM_RPC_421614}      # Arbitrum Sepolia
      EVM_RPC_84532: ${EVM_RPC_84532}        # Base Sepolia
      EVM_RPC_11155420: ${EVM_RPC_11155420}  # Optimism Sepolia
      SOLANA_RPC_URL: ${SOLANA_RPC_URL}      # Devnet
```

### Step 3: Start with Testnet Config

```bash
# Load testnet environment
cp .env.testnet .env

# Start VaultKey
docker compose up -d

# Check health
curl http://localhost:8080/health
```

---

## Getting Testnet Tokens

### Ethereum Sepolia

**Faucets:**
- https://sepoliafaucet.com/
- https://www.alchemy.com/faucets/ethereum-sepolia
- https://faucet.quicknode.com/ethereum/sepolia

**Minimum for testing:** 0.1 ETH

### Polygon Mumbai

**Faucets:**
- https://faucet.polygon.technology/
- https://mumbaifaucet.com/

**Minimum for testing:** 1 MATIC

### Arbitrum Sepolia

**Faucets:**
- https://faucet.quicknode.com/arbitrum/sepolia
- Bridge Sepolia ETH from Ethereum Sepolia

**Minimum for testing:** 0.1 ETH

### Base Sepolia

**Faucets:**
- https://www.alchemy.com/faucets/base-sepolia
- Bridge Sepolia ETH from Ethereum Sepolia

**Minimum for testing:** 0.01 ETH

### Optimism Sepolia

**Faucets:**
- https://www.alchemy.com/faucets/optimism-sepolia
- Bridge Sepolia ETH from Ethereum Sepolia

**Minimum for testing:** 0.05 ETH

### Solana Devnet

**Faucet (built-in):**
```bash
# Install Solana CLI
curl -sSfL https://release.solana.com/stable/install | sh

# Airdrop devnet SOL to your wallet address
solana airdrop 2 <YOUR_WALLET_ADDRESS> --url devnet
```

**Minimum for testing:** 2 SOL (free, unlimited)

---

## Testing Workflow

### 1. Create a Project

```bash
curl -X POST http://localhost:8080/projects \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Testnet Development",
    "webhook_url": "https://webhook.site/YOUR_UNIQUE_URL",
    "rate_limit_rps": 100,
    "max_retries": 3
  }'
```

Save the `api_key` and `api_secret`.

### 2. Create a Wallet

```bash
curl -X POST http://localhost:8080/wallets \
  -H "Content-Type: application/json" \
  -H "X-API-Key: your_api_key" \
  -H "X-API-Secret: your_api_secret" \
  -d '{
    "user_id": "test_user_1",
    "chain_type": "evm",
    "label": "testnet_wallet"
  }'
```

### 3. Fund the Wallet

Use a testnet faucet to send tokens to the wallet address.

### 4. Check Balance

```bash
# Ethereum Sepolia (chain_id=11155111)
curl "http://localhost:8080/wallets/{wallet_id}/balance?chain_id=11155111" \
  -H "X-API-Key: your_api_key" \
  -H "X-API-Secret: your_api_secret"
```

### 5. Sign a Transaction

```bash
curl -X POST "http://localhost:8080/wallets/{wallet_id}/sign/transaction/evm" \
  -H "Content-Type: application/json" \
  -H "X-API-Key: your_api_key" \
  -H "X-API-Secret: your_api_secret" \
  -d '{
    "payload": {
      "to": "0x742d35Cc6634C0532925a3b844Bc9e7595f0bEb",
      "value": "0x16345785D8A0000",
      "gas_limit": 21000,
      "gas_price": "0x3B9ACA00",
      "nonce": 0,
      "chain_id": 11155111
    }
  }'
```

### 6. Poll Job Status

```bash
curl "http://localhost:8080/jobs/{job_id}" \
  -H "X-API-Key: your_api_key" \
  -H "X-API-Secret: your_api_secret"
```

### 7. Broadcast Transaction

```bash
curl -X POST "http://localhost:8080/wallets/{wallet_id}/broadcast" \
  -H "Content-Type: application/json" \
  -H "X-API-Key: your_api_key" \
  -H "X-API-Secret: your_api_secret" \
  -d '{
    "signed_tx": "0x...",
    "chain_id": "11155111"
  }'
```

### 8. Verify on Block Explorer

**Sepolia:** https://sepolia.etherscan.io/tx/{tx_hash}
**Mumbai:** https://mumbai.polygonscan.com/tx/{tx_hash}
**Solana Devnet:** https://explorer.solana.com/tx/{signature}?cluster=devnet

---

## Testing Gasless Transactions

### 1. Register a Relayer Wallet

```bash
curl -X POST http://localhost:8080/projects/relayer \
  -H "Content-Type: application/json" \
  -H "X-API-Key: your_api_key" \
  -H "X-API-Secret: your_api_secret" \
  -d '{
    "chain_type": "evm",
    "chain_id": "11155111",
    "min_balance_alert": "0.05"
  }'
```

**Save the relayer wallet address** from the response.

### 2. Fund the Relayer Wallet

Send testnet ETH to the relayer wallet address using a faucet.

Minimum: 0.1 ETH (to pay gas for multiple test transactions)

### 3. Check Relayer Balance

```bash
curl "http://localhost:8080/projects/relayer?chain_type=evm&chain_id=11155111" \
  -H "X-API-Key: your_api_key" \
  -H "X-API-Secret: your_api_secret"
```

Response:
```json
{
  "wallet_id": "...",
  "address": "0x...",
  "chain_type": "evm",
  "chain_id": "11155111",
  "balance": "0x16345785D8A0000",
  "unit": "wei",
  "healthy": true
}
```

### 4. Submit Gasless Transaction

```bash
curl -X POST "http://localhost:8080/wallets/{wallet_id}/sign/transaction/evm" \
  -H "Content-Type: application/json" \
  -H "X-API-Key: your_api_key" \
  -H "X-API-Secret: your_api_secret" \
  -d '{
    "gasless": true,
    "payload": {
      "to": "0x742d35Cc6634C0532925a3b844Bc9e7595f0bEb",
      "value": "0x0",
      "data": "0xa9059cbb...",
      "chain_id": 11155111
    }
  }'
```

**Note:** For gasless transactions, you don't provide `gas_limit`, `gas_price`, or `nonce`. The relayer handles these.

---

## Migrating to Mainnet

### Step 1: Verify Testnet Works

Before migrating to mainnet, ensure:
- ✅ Wallet creation works
- ✅ Transaction signing works
- ✅ Webhooks are delivered
- ✅ Gasless transactions work (if using)
- ✅ Error handling is correct
- ✅ Rate limits are appropriate

### Step 2: Create Mainnet Environment File

Create `.env.mainnet`:

```bash
# PostgreSQL
POSTGRES_PASSWORD=your_production_password_here

# Redis
REDIS_PASSWORD=your_production_redis_password

# Worker config
WORKER_CONCURRENCY=20  # Higher for production

# ════════════════════════════════════════════════════════
#  MAINNET RPC ENDPOINTS
# ════════════════════════════════════════════════════════

# Use your own RPC provider for production (Alchemy, Infura, QuickNode)
# Public RPCs are rate-limited and unreliable

# Ethereum Mainnet
EVM_RPC_1=https://eth-mainnet.g.alchemy.com/v2/YOUR_API_KEY

# Polygon Mainnet
EVM_RPC_137=https://polygon-mainnet.g.alchemy.com/v2/YOUR_API_KEY

# Arbitrum Mainnet
EVM_RPC_42161=https://arb-mainnet.g.alchemy.com/v2/YOUR_API_KEY

# Base Mainnet
EVM_RPC_8453=https://base-mainnet.g.alchemy.com/v2/YOUR_API_KEY

# Optimism Mainnet
EVM_RPC_10=https://opt-mainnet.g.alchemy.com/v2/YOUR_API_KEY

# Solana Mainnet
SOLANA_RPC_URL=https://solana-mainnet.g.alchemy.com/v2/YOUR_API_KEY
```

### Step 3: Recommended RPC Providers

**For Production:**
- **Alchemy** - https://www.alchemy.com/ (recommended)
- **Infura** - https://www.infura.io/
- **QuickNode** - https://www.quicknode.com/
- **Ankr** - https://www.ankr.com/

**Pricing:**
- Most offer 300M+ requests/month on free tier
- Paid plans: $49-199/month for higher throughput
- Dedicated nodes: $500-2000/month

### Step 4: Deploy to Production

```bash
# Backup testnet data (optional)
docker compose exec postgres pg_dump -U vaultkey vaultkey > testnet_backup.sql

# Switch to mainnet config
cp .env.mainnet .env

# Restart with mainnet endpoints
docker compose down
docker compose up -d

# Verify health
curl http://localhost:8080/health
```

### Step 5: Create New Production Projects

**Do NOT reuse testnet API keys in production.**

Create a new project for mainnet:
```bash
curl -X POST http://localhost:8080/projects \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Production",
    "webhook_url": "https://api.yourapp.com/webhooks/vaultkey",
    "rate_limit_rps": 500,
    "max_retries": 5
  }'
```

---

## Best Practices

### During Testnet Phase

1. **Test all error cases:**
   - Invalid transactions
   - Insufficient gas
   - Nonce gaps
   - Webhook failures

2. **Load testing:**
   - Submit 100+ concurrent jobs
   - Verify queue doesn't back up
   - Check worker pool scales

3. **Monitor metrics:**
   - Job completion time
   - Webhook delivery success rate
   - Queue depth

### For Mainnet

1. **Use managed services:**
   - RDS for PostgreSQL
   - ElastiCache for Redis
   - AWS KMS instead of Vault

2. **Enable monitoring:**
   - CloudWatch / Datadog
   - Alert on high queue depth
   - Alert on webhook failures

3. **Implement safeguards:**
   - Transaction amount limits
   - Address whitelisting
   - Multi-approval for large amounts

4. **Backup strategy:**
   - Daily Vault data backup
   - Continuous PostgreSQL replication
   - Test recovery procedures

---

## Dual Environment Setup (Advanced)

Run both testnet and mainnet simultaneously using Docker Compose profiles:

### docker-compose.yml (modified)

```yaml
version: "3.9"

services:
  # Shared Vault (testnet and mainnet use different keys)
  vault:
    # ... same as before ...

  # Testnet stack
  postgres-testnet:
    profiles: ["testnet"]
    # ... postgres config ...
    environment:
      POSTGRES_DB: vaultkey_testnet

  redis-testnet:
    profiles: ["testnet"]
    # ... redis config ...

  api-testnet:
    profiles: ["testnet"]
    # ... api config ...
    ports:
      - "8080:8080"
    environment:
      DATABASE_URL: postgres://vaultkey:${POSTGRES_PASSWORD}@postgres-testnet:5432/vaultkey_testnet
      REDIS_ADDR: redis-testnet:6379
      EVM_RPC_11155111: ${EVM_RPC_11155111}
      # ... testnet RPC endpoints ...

  # Mainnet stack
  postgres-mainnet:
    profiles: ["mainnet"]
    # ... postgres config ...
    environment:
      POSTGRES_DB: vaultkey_mainnet

  redis-mainnet:
    profiles: ["mainnet"]
    # ... redis config ...

  api-mainnet:
    profiles: ["mainnet"]
    # ... api config ...
    ports:
      - "8081:8080"  # Different port
    environment:
      DATABASE_URL: postgres://vaultkey:${POSTGRES_PASSWORD}@postgres-mainnet:5432/vaultkey_mainnet
      REDIS_ADDR: redis-mainnet:6379
      EVM_RPC_1: ${EVM_RPC_1}
      # ... mainnet RPC endpoints ...
```

### Start both environments

```bash
# Start testnet
docker compose --profile testnet up -d

# Start mainnet
docker compose --profile mainnet up -d

# Testnet available on :8080
# Mainnet available on :8081
```

---

## Troubleshooting Testnet

### Transaction Reverted

**Cause:** Contract call failed or insufficient gas

**Solution:**
- Increase `gas_limit`
- Check contract is deployed on testnet
- Verify contract address is correct

### Nonce Too High

**Cause:** Submitted transaction with nonce=5, but chain expects nonce=3

**Solution:**
- Relayer nonce desync
- Restart API to resync: `docker compose restart api`

### Insufficient Funds (Relayer)

**Error:** `relayer wallet balance too low`

**Solution:**
- Send more testnet tokens to relayer wallet
- Check faucet daily limits
- Use multiple faucets if needed

---

## Summary

| Step | Testnet | Mainnet |
|------|---------|---------|
| 1. Setup | Use `.env.testnet` | Use `.env.mainnet` |
| 2. RPC Endpoints | Public testnets | Paid RPC provider |
| 3. Tokens | Free from faucets | Buy with real money |
| 4. Testing | Unlimited, free | Limited, costs money |
| 5. Monitoring | Optional | Required |
| 6. Backups | Optional | Required |

**Recommendation:** Spend 1-2 weeks on testnet before moving to mainnet. Test every feature thoroughly. Once on mainnet, you can still use testnet for development of new features.