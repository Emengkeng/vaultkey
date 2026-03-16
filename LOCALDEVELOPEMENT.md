# VaultKey Local Development Guide

## Prerequisites

Before you start, make sure you have:

- **Docker** (version 20.10+)
- **Docker Compose** (version 2.0+)
- **Git**
- **Code editor** (VS Code, GoLand, etc.)
- **Go 1.21+** (optional, for running without Docker)
- **curl** or **Postman** (for testing API)

## Quick Start (5 minutes)

### 1. Clone the Repository

```bash
git clone https://github.com/Emengkeng/vaultkey
cd vaultkey
```

### 2. Create Environment File

```bash
cp .env.example .env
```

Edit `.env`:
```bash
# Simple defaults for local dev - DO NOT use in production
POSTGRES_PASSWORD=dev_password_123
REDIS_PASSWORD=dev_redis_456
WORKER_CONCURRENCY=5  # Lower for local dev

# Testnet RPCs (free, no API key needed)
EVM_RPC_11155111=https://eth-sepolia.public.blastapi.io
EVM_RPC_80001=https://rpc-mumbai.maticvigil.com
SOLANA_RPC_URL=https://api.devnet.solana.com
```

### 3. Start Everything

```bash
docker compose up -d
```

**What happens:**
1. PostgreSQL starts (database)
2. Redis starts (queue)
3. Vault starts (key management)
4. Vault auto-initializes and unseals
5. API + Workers start

**First run takes ~2 minutes** (downloading images)

## Troubleshooting
 
### Vault permission errors
 
If you see `permission denied` errors from Vault:
 
```bash
# Fix vault volume permissions manually
sudo chown -R 100:100 /var/lib/docker/volumes/vaultkey_vault_data/_data
 
# Restart
docker compose down
docker compose up -d
```

### 4. Check Vault Keys (IMPORTANT!)

```bash
docker compose logs vault-init
```

You'll see:
```
==========================================================
  VAULT INITIALIZED - SAVE THESE KEYS SECURELY
==========================================================
  Unseal Key 1: abc123...
  Unseal Key 2: def456...
  Unseal Key 3: ghi789...
  Root Token:   xyz...
==========================================================
```

**Save these to a text file locally** - You'll need them if Vault restarts.

### 5. Verify Everything Works

```bash
# Health check
curl http://localhost:8080/health
# Should return: {"vault":"ok","redis":"ok"}

# Create a project
curl -X POST http://localhost:8080/projects \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Local Dev",
    "webhook_url": "https://webhook.site/YOUR_URL",
    "rate_limit_rps": 100
  }'
```

**Save the `api_key` and `api_secret` from the response!**

### 6. Create a Test Wallet

```bash
# Replace YOUR_API_KEY and YOUR_API_SECRET
curl -X POST http://localhost:8080/wallets \
  -H "Content-Type: application/json" \
  -H "X-API-Key: YOUR_API_KEY" \
  -H "X-API-Secret: YOUR_API_SECRET" \
  -d '{
    "user_id": "dev_user_1",
    "chain_type": "evm",
    "label": "test_wallet"
  }'
```

✅ **You're ready to develop!**

---

## Development Workflow

### Making Code Changes

The Docker setup **does NOT have hot reloading by default**. When you change Go code, you need to rebuild.

#### Option A: Rebuild and Restart (Recommended)

```bash
# After making code changes
docker compose build api
docker compose restart api
```

**Rebuilds only the API container** (~20 seconds)

#### Option B: Full Restart (If database schema changed)

```bash
docker compose down
docker compose up -d
```

**⚠️ Warning:** This **deletes all data** (wallets, jobs, etc.)

#### Option C: Rebuild Everything

```bash
docker compose down
docker compose build
docker compose up -d
```

### File Structure

```
vaultkey/
├── cmd/
│   └── api/
│       └── main.go          # Entry point
├── internal/
│   ├── api/
│   │   ├── handlers/        # HTTP handlers
│   │   └── middleware/      # Auth, rate limiting
│   ├── kms/                 # Vault integration
│   ├── nonce/               # Nonce management
│   ├── queue/               # Redis queue
│   ├── ratelimit/           # Rate limiter
│   ├── relayer/             # Gasless transactions
│   ├── rpc/                 # Blockchain RPC
│   ├── storage/             # Database
│   ├── wallet/              # Wallet generation & signing
│   ├── webhook/             # Webhook delivery
│   └── worker/              # Job processing
├── config/
│   └── config.go            # Configuration loading
├── migrations/
│   └── schema.sql           # Database schema
├── scripts/
│   └── vault-init.sh        # Vault initialization
├── docker-compose.yml       # Docker services
├── Dockerfile               # API container
├── .env                     # Environment variables
├── .env.example             # Template
└── README.md
```

---

## Common Development Tasks

### Viewing Logs

**All services:**
```bash
docker compose logs -f
```

**Specific service:**
```bash
docker compose logs -f api        # API + Workers
docker compose logs -f postgres   # Database
docker compose logs -f redis      # Queue
docker compose logs -f vault      # KMS
```

**Filter by time:**
```bash
docker compose logs --since 5m api
docker compose logs --tail 100 api
```

### Accessing the Database

```bash
# PostgreSQL shell
docker compose exec postgres psql -U vaultkey -d vaultkey

# Useful queries
SELECT COUNT(*) FROM wallets;
SELECT COUNT(*), status FROM signing_jobs GROUP BY status;
SELECT * FROM audit_log ORDER BY created_at DESC LIMIT 10;
```

**GUI Tool (Optional):**
- Download TablePlus, DBeaver, or pgAdmin
- Connect to: `localhost:5432`
- Database: `vaultkey`
- Username: `vaultkey`
- Password: (from `.env`)

### Accessing Redis

```bash
# Redis CLI
docker compose exec redis redis-cli -a dev_redis_456

# Useful commands
> LLEN vaultkey:jobs           # Queue depth
> LRANGE vaultkey:jobs 0 -1    # View all jobs
> KEYS vaultkey:*              # All VaultKey keys
> DEL vaultkey:nonce:1:0x...   # Reset nonce (if stuck)
```

### Accessing Vault

```bash
# Vault status
docker compose exec vault vault status -address=http://127.0.0.1:8200

# List encryption keys
docker compose exec vault vault list -address=http://127.0.0.1:8200 transit/keys

# Unseal manually (if needed)
docker compose exec vault vault operator unseal -address=http://127.0.0.1:8200
```

---

## Running Without Docker (Advanced)

If you prefer to run Go directly (faster iteration):

### 1. Start Infrastructure Only

```bash
# Run only Postgres, Redis, Vault
docker compose up -d postgres redis vault vault-init
```

### 2. Set Environment Variables

```bash
export DATABASE_URL="postgres://vaultkey:dev_password_123@localhost:5432/vaultkey?sslmode=disable"
export VAULT_ADDR="http://localhost:8200"
export VAULT_TOKEN="$(cat ./vault-data/root_token)"  # Or use the token from logs
export REDIS_ADDR="localhost:6379"
export REDIS_PASSWORD="dev_redis_456"
export WORKER_CONCURRENCY=5
export EVM_RPC_11155111="https://eth-sepolia.public.blastapi.io"
export SOLANA_RPC_URL="https://api.devnet.solana.com"
```

### 3. Run the Application

```bash
# Install dependencies
go mod download

# Run
go run cmd/api/main.go
```

**Advantages:**
- ✅ Instant restarts (no rebuild)
- ✅ Native debugger support
- ✅ Faster compilation

**Disadvantages:**
- ❌ Need Go installed locally
- ❌ Need to manage environment variables
- ❌ Ports might conflict (8080, 5432, 6379, 8200)

---

## Debugging

### VS Code Debugging (With Docker)

**Install:** `Remote - Containers` extension

**.vscode/launch.json:**
```json
{
  "version": "0.2.0",
  "configurations": [
    {
      "name": "Attach to Docker",
      "type": "go",
      "request": "attach",
      "mode": "remote",
      "remotePath": "/app",
      "port": 2345,
      "host": "localhost"
    }
  ]
}
```

**Dockerfile (add Delve):**
```dockerfile
RUN go install github.com/go-delve/delve/cmd/dlv@latest
CMD ["dlv", "debug", "--headless", "--listen=:2345", "--api-version=2", "--accept-multiclient", "./cmd/api"]
```

**Expose port in docker-compose.yml:**
```yaml
api:
  ports:
    - "8080:8080"
    - "2345:2345"  # Debugger
```

### GoLand Debugging

1. **Run** → **Edit Configurations**
2. **Add** → **Go Remote**
3. Host: `localhost`, Port: `2345`
4. Set breakpoints in code
5. Click **Debug**

### Printf Debugging (Simple)

```go
log.Printf("[DEBUG] Wallet ID: %s, Chain: %s", walletID, chainID)
```

View logs:
```bash
docker compose logs -f api | grep DEBUG
```

---

## Testing

### Manual API Testing

**Postman Collection:**
Create a collection with your API requests. Import/export for team sharing.

**Example request (Postman):**
```
POST http://localhost:8080/wallets
Headers:
  Content-Type: application/json
  X-API-Key: {{api_key}}
  X-API-Secret: {{api_secret}}
Body:
  {
    "user_id": "test_user",
    "chain_type": "evm"
  }
```

**cURL Scripts:**
Create a `test.sh` file:
```bash
#!/bin/bash
API_KEY="vk_live_..."
API_SECRET="vk_secret_..."

# Create wallet
WALLET=$(curl -s -X POST http://localhost:8080/wallets \
  -H "Content-Type: application/json" \
  -H "X-API-Key: $API_KEY" \
  -H "X-API-Secret: $API_SECRET" \
  -d '{
    "user_id": "test_user",
    "chain_type": "evm"
  }')

echo "Wallet created: $WALLET"
WALLET_ID=$(echo $WALLET | jq -r '.id')

# Check balance
curl -s "http://localhost:8080/wallets/$WALLET_ID/balance?chain_id=11155111" \
  -H "X-API-Key: $API_KEY" \
  -H "X-API-Secret: $API_SECRET" | jq
```

Make executable:
```bash
chmod +x test.sh
./test.sh
```

### Unit Testing

```bash
# Run all tests
go test ./...

# Run specific package
go test ./internal/wallet

# With coverage
go test -cover ./...

# Verbose output
go test -v ./internal/wallet
```

### Integration Testing

**Example test:**
```go
// internal/api/handlers/handlers_test.go
package handlers_test

import (
    "testing"
    "net/http/httptest"
)

func TestCreateWallet(t *testing.T) {
    // Setup test database
    // Setup test handlers
    // Make request
    // Assert response
}
```

---

## Common Issues & Solutions

### Issue 1: Port Already in Use

**Error:**
```
Error starting userland proxy: listen tcp4 0.0.0.0:8080: bind: address already in use
```

**Solution:**
```bash
# Find what's using the port
lsof -i :8080
# Or on Linux:
netstat -tulpn | grep 8080

# Kill the process
kill -9 <PID>

# Or change the port in docker-compose.yml
ports:
  - "8081:8080"  # Use 8081 instead
```

### Issue 2: Vault Sealed

**Error:**
```
{"vault":"Vault is sealed","redis":"ok"}
```

**Solution:**
```bash
# Run the init script again
docker compose restart vault-init

# Or manually
docker compose logs vault-init  # Get unseal keys
docker compose exec vault vault operator unseal <key1>
docker compose exec vault vault operator unseal <key2>
```

### Issue 3: Database Schema Not Applied

**Error:**
```
pq: relation "wallets" does not exist
```

**Solution:**
```bash
# Schema is applied on first run only
# To reapply:
docker compose down -v  # Delete volumes (LOSES ALL DATA)
docker compose up -d
```

**Or manually:**
```bash
docker compose exec postgres psql -U vaultkey -d vaultkey -f /docker-entrypoint-initdb.d/schema.sql
```

### Issue 4: Code Changes Not Reflected

**Problem:** Changed code but API still behaves the same

**Solution:**
```bash
# Rebuild the container
docker compose build api
docker compose restart api

# Or run without Docker (see "Running Without Docker" section)
```

### Issue 5: Queue Not Processing

**Symptoms:**
- Jobs stuck in "pending"
- `LLEN vaultkey:jobs` keeps growing

**Debug:**
```bash
# Check worker logs
docker compose logs -f api | grep worker

# Check queue depth
docker compose exec redis redis-cli -a dev_redis_456 LLEN vaultkey:jobs

# Restart workers
docker compose restart api
```

### Issue 6: Out of Disk Space

**Error:**
```
no space left on device
```

**Solution:**
```bash
# Clean up Docker
docker system prune -a --volumes

# Remove old images
docker image prune -a

# Check disk usage
docker system df
```

---

## Tips for Faster Development

### 1. Use Make for Common Tasks

Create `Makefile`:
```makefile
.PHONY: start stop restart logs build test

start:
	docker compose up -d

stop:
	docker compose down

restart:
	docker compose restart api

logs:
	docker compose logs -f api

build:
	docker compose build api
	docker compose restart api

test:
	go test ./...

clean:
	docker compose down -v
	docker system prune -f
```

Usage:
```bash
make start
make logs
make build
make test
```

### 2. Shell Aliases

Add to `~/.bashrc` or `~/.zshrc`:
```bash
alias vk-start='docker compose up -d'
alias vk-stop='docker compose down'
alias vk-logs='docker compose logs -f api'
alias vk-rebuild='docker compose build api && docker compose restart api'
alias vk-db='docker compose exec postgres psql -U vaultkey -d vaultkey'
alias vk-redis='docker compose exec redis redis-cli -a dev_redis_456'
alias vk-health='curl http://localhost:8080/health'
```

### 3. Watch Mode (Auto-rebuild)

Install `air` for hot reloading:
```bash
# Install air
go install github.com/cosmtrek/air@latest

# Create .air.toml
air init

# Run with air instead of go run
air
```

### 4. Use Docker Compose Profiles

**docker-compose.yml:**
```yaml
services:
  api:
    # ... regular config ...

  api-debug:
    profiles: ["debug"]
    # ... same as api but with delve ...
    
  mailhog:  # Email testing
    profiles: ["mail"]
    image: mailhog/mailhog
    ports:
      - "8025:8025"
```

Usage:
```bash
# Normal mode
docker compose up -d

# Debug mode
docker compose --profile debug up -d

# With email testing
docker compose --profile mail up -d
```

### 5. Sample Data Script

Create `scripts/seed.sh`:
```bash
#!/bin/bash
set -e

API_KEY=${1}
API_SECRET=${2}

if [ -z "$API_KEY" ]; then
  echo "Usage: ./seed.sh <api_key> <api_secret>"
  exit 1
fi

echo "Creating 10 test wallets..."
for i in {1..10}; do
  curl -s -X POST http://localhost:8080/wallets \
    -H "Content-Type: application/json" \
    -H "X-API-Key: $API_KEY" \
    -H "X-API-Secret: $API_SECRET" \
    -d "{
      \"user_id\": \"user_$i\",
      \"chain_type\": \"evm\",
      \"label\": \"wallet_$i\"
    }" | jq -r '.address'
done

echo "Done! Created 10 wallets."
```

Usage:
```bash
chmod +x scripts/seed.sh
./scripts/seed.sh YOUR_API_KEY YOUR_API_SECRET
```

---

## Environment Switching

### Local Development (Testnet)

**.env.local:**
```bash
POSTGRES_PASSWORD=dev_password
REDIS_PASSWORD=dev_redis
WORKER_CONCURRENCY=5

# Testnet RPCs (free)
EVM_RPC_11155111=https://eth-sepolia.public.blastapi.io
SOLANA_RPC_URL=https://api.devnet.solana.com
```

### Local Development (Mainnet RPCs)

**.env.local.mainnet:**
```bash
POSTGRES_PASSWORD=dev_password
REDIS_PASSWORD=dev_redis
WORKER_CONCURRENCY=5

# Mainnet RPCs (use your Alchemy/Infura keys)
EVM_RPC_1=https://eth-mainnet.g.alchemy.com/v2/YOUR_KEY
SOLANA_RPC_URL=https://api.mainnet-beta.solana.com
```

Switch environments:
```bash
cp .env.local .env && docker compose restart api
cp .env.local.mainnet .env && docker compose restart api
```

---

## Performance Monitoring

### Check Resource Usage

```bash
# Container stats (live)
docker stats

# Disk usage
docker system df
```

### Prometheus (Optional)

Add to `docker-compose.yml`:
```yaml
  prometheus:
    image: prom/prometheus
    volumes:
      - ./prometheus.yml:/etc/prometheus/prometheus.yml
    ports:
      - "9090:9090"

  grafana:
    image: grafana/grafana
    ports:
      - "3000:3000"
```

Access Grafana at `http://localhost:3000`

---

## Team Development

### Sharing State

**Problem:** Each developer has their own Vault keys and database

**Solution:**

1. **Shared Vault keys** (optional):
   - Commit `vault_init.json` to **private repo** (never public!)
   - Team members copy file before first run

2. **Shared database snapshots**:
```bash
# Export
docker compose exec postgres pg_dump -U vaultkey vaultkey > snapshot.sql

# Import
cat snapshot.sql | docker compose exec -T postgres psql -U vaultkey -d vaultkey
```

3. **Git ignore patterns**:
```gitignore
# .gitignore
.env
vault-data/
postgres-data/
redis-data/
*.log
```

---

## Summary: Daily Workflow

```bash
# Morning: Start everything
docker compose up -d
curl http://localhost:8080/health  # Verify

# Make code changes
# ... edit files in your IDE ...

# Rebuild and test
docker compose build api
docker compose restart api
curl -X POST http://localhost:8080/wallets ...  # Test

# Check logs if something breaks
docker compose logs -f api

# End of day: Stop everything (optional)
docker compose down
```

**That's it!** Docker handles all the complexity - you just need:
1. `docker compose up -d` to start
2. `docker compose build api && docker compose restart api` after code changes
3. `docker compose logs -f api` to debug

Simple and clean! 🚀