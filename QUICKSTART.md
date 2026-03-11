# VaultKey Quick Start & Deployment Checklist

## 🚀 Quick Start (5 minutes)

### Prerequisites

- Docker & Docker Compose installed
- 4GB RAM minimum
- 20GB disk space

### Step 1: Clone & Setup

```bash
git clone https://github.com/Emengkeng/vaultkey
cd vaultkey

# Copy environment template
cp .env.example .env

# Edit .env and set strong passwords
nano .env
```

### Step 2: Start VaultKey

```bash
docker compose up -d
```

**IMPORTANT:** On first run, Vault will print unseal keys and root token to the console.

```bash
# View the initialization output
docker compose logs vault-init
```

**⚠️ SAVE THESE KEYS IMMEDIATELY** - Loss of unseal keys = permanent loss of all wallets!

```
Unseal Key 1: abc123...
Unseal Key 2: def456...
Unseal Key 3: ghi789...
Root Token:   xyz...
```

### Step 3: Verify Health

```bash
curl http://localhost:8080/health
```

Expected response:
```json
{"vault":"ok","redis":"ok"}
```

### Step 4: Create Your First Project

```bash
curl -X POST http://localhost:8080/projects \
  -H "Content-Type: application/json" \
  -d '{
    "name": "My App",
    "webhook_url": "https://yourapp.com/webhooks/vaultkey",
    "rate_limit_rps": 100,
    "max_retries": 3
  }'
```

**⚠️ SAVE THE RESPONSE** - `api_secret` is shown only once!

```json
{
  "id": "proj_abc123",
  "name": "My App",
  "api_key": "vk_live_...",
  "api_secret": "vk_secret_..."
}
```

### Step 5: Create a Wallet

```bash
curl -X POST http://localhost:8080/wallets \
  -H "Content-Type: application/json" \
  -H "X-API-Key: vk_live_..." \
  -H "X-API-Secret: vk_secret_..." \
  -d '{
    "user_id": "user_123",
    "chain_type": "evm",
    "label": "main"
  }'
```

Response:
```json
{
  "id": "wallet_xyz",
  "user_id": "user_123",
  "chain_type": "evm",
  "address": "0x742d35Cc6634C0532925a3b844Bc9e7595f0bEb",
  "created_at": "2026-03-11T12:00:00Z"
}
```

✅ **Done!** You now have a custodial wallet infrastructure running.

---

## 📋 Pre-Production Checklist

Before deploying to production, verify:

### Security

- [ ] Strong passwords set in `.env` (20+ characters, random)
- [ ] Vault unseal keys backed up to password manager
- [ ] Vault root token backed up separately
- [ ] API secrets never committed to git
- [ ] Network isolation configured (internal Docker network)
- [ ] HTTPS configured for API (reverse proxy)
- [ ] Webhook signature validation implemented
- [ ] Rate limits appropriate for expected load

### Infrastructure

- [ ] PostgreSQL backed up daily
- [ ] Vault data backed up daily (`/vault/data`)
- [ ] Redis persistence enabled (AOF)
- [ ] Monitoring set up (Prometheus/Datadog)
- [ ] Log aggregation configured
- [ ] Alert rules configured (queue depth, errors)
- [ ] Health check endpoint monitored
- [ ] Auto-restart policies set (Docker/Kubernetes)

### RPC Configuration

- [ ] Using paid RPC provider (not public endpoints)
- [ ] RPC API keys secured
- [ ] Backup RPC endpoints configured
- [ ] Rate limits understood
- [ ] Chain IDs verified for target networks

### Testing

- [ ] Testnet deployment tested end-to-end
- [ ] Wallet creation works
- [ ] Transaction signing works
- [ ] Webhooks delivered successfully
- [ ] Gasless transactions tested (if using)
- [ ] Error cases handled correctly
- [ ] Load testing completed (100+ concurrent requests)
- [ ] Recovery from crashes tested

### Documentation

- [ ] API credentials documented (secure location)
- [ ] Vault unseal procedure documented
- [ ] Disaster recovery plan written
- [ ] Runbook for common issues created
- [ ] On-call rotation defined

### Compliance

- [ ] Audit logging enabled
- [ ] Data retention policy defined
- [ ] User data privacy reviewed
- [ ] Terms of service accepted for RPC providers
- [ ] Incident response plan created

---

## 🏗️ Production Deployment

### Option A: Single Server (Small Scale)

**Specs:**
- 4 vCPU
- 8GB RAM
- 100GB SSD

**Max throughput:**
- ~500 requests/sec
- ~50 signing jobs/sec

**Setup:**

```bash
# Install Docker
curl -fsSL https://get.docker.com | sh

# Clone repo
git clone https://github.com/Emengkeng/vaultkey
cd vaultkey

# Configure production environment
cp .env.mainnet .env
nano .env

# Start
docker compose up -d

# Enable auto-restart
docker update --restart unless-stopped $(docker ps -aq)
```

### Option B: Kubernetes (High Scale)

**Specs:**
- 3+ nodes
- 8GB RAM per node
- Managed PostgreSQL (RDS/Cloud SQL)
- Managed Redis (ElastiCache/Memorystore)
- Managed KMS (AWS KMS/GCP KMS)

**Max throughput:**
- 5,000+ requests/sec
- 500+ signing jobs/sec

**Setup:**

See `k8s/` directory for Kubernetes manifests.

```bash
# Deploy database
kubectl apply -f k8s/postgres.yaml

# Deploy Redis
kubectl apply -f k8s/redis.yaml

# Deploy VaultKey API (3 replicas)
kubectl apply -f k8s/api-deployment.yaml

# Deploy Workers (2 replicas)
kubectl apply -f k8s/worker-deployment.yaml

# Expose API
kubectl apply -f k8s/ingress.yaml
```

---

## 🔒 Vault Management

### Daily Operations

**Check Vault Status:**
```bash
docker exec vaultkey-vault-1 vault status -address=http://127.0.0.1:8200
```

**Unseal Vault (after restart):**
```bash
docker exec vaultkey-vault-init-1 sh /scripts/vault-init.sh
```

### Backup Vault

**Manual Backup:**
```bash
# Backup Vault data
docker cp vaultkey-vault-1:/vault/data ./vault-backup-$(date +%Y%m%d)
```

**Automated Backup (cron):**
```bash
# Add to /etc/cron.daily/vault-backup
#!/bin/bash
docker cp vaultkey-vault-1:/vault/data /backups/vault-$(date +%Y%m%d)
find /backups/vault-* -mtime +30 -delete  # Keep 30 days
```

### Rotate Vault Token

**Do NOT rotate the root token without a plan.** It's embedded in `VAULT_TOKEN_FILE`.

Instead, create a dedicated token with limited permissions:

```bash
vault token create -policy=vaultkey-policy -ttl=8760h  # 1 year
```

Update `.env` with new token and restart:
```bash
docker compose restart api
```

---

## 📊 Monitoring

### Key Metrics

**API Health:**
- Request rate (requests/sec)
- Error rate (%)
- p50/p95/p99 latency

**Worker Health:**
- Queue depth (jobs pending)
- Job completion rate (jobs/sec)
- Job failure rate (%)
- Webhook delivery rate (%)

**Infrastructure:**
- PostgreSQL connections
- Redis memory usage
- Vault response time
- Disk usage

### Health Check Endpoint

```bash
curl http://localhost:8080/health
```

Integrate with:
- **Uptime monitoring:** UptimeRobot, Pingdom
- **APM:** Datadog, New Relic
- **Custom:** Prometheus + Grafana

### Log Aggregation

**ELK Stack:**
```yaml
# docker-compose.yml addition
  elasticsearch:
    image: elasticsearch:8.11.0
    # ... config ...

  logstash:
    image: logstash:8.11.0
    # ... config ...

  kibana:
    image: kibana:8.11.0
    # ... config ...
```

**Cloud Options:**
- AWS CloudWatch Logs
- GCP Cloud Logging
- Datadog Logs

---

## 🚨 Disaster Recovery

### Scenario 1: Database Corruption

**Recovery Steps:**

1. Stop API and workers:
```bash
docker compose stop api
```

2. Restore from backup:
```bash
docker compose exec postgres psql -U vaultkey -d vaultkey < backup.sql
```

3. Restart:
```bash
docker compose start api
```

**RTO:** 15 minutes
**RPO:** Last backup (ideally < 1 hour)

### Scenario 2: Vault Data Lost

**⚠️ CRITICAL:** Without Vault data and unseal keys, all wallets are permanently inaccessible.

**Prevention:**
- Daily backups of `/vault/data`
- Store unseal keys in separate secure location (password manager)
- Test recovery procedure monthly

**Recovery Steps:**

1. Stop Vault:
```bash
docker compose stop vault
```

2. Restore Vault data:
```bash
docker cp ./vault-backup-20260311/data vaultkey-vault-1:/vault/
```

3. Restart and unseal:
```bash
docker compose start vault
docker exec vaultkey-vault-init-1 sh /scripts/vault-init.sh
```

**RTO:** 30 minutes
**RPO:** Last backup

### Scenario 3: Redis Failure

**Impact:** In-flight jobs lost, nonces may desync

**Recovery Steps:**

1. Restart Redis:
```bash
docker compose restart redis
```

2. Recover stalled jobs (automatic):
```bash
docker compose restart api
```

3. Verify queue depth:
```bash
docker exec vaultkey-redis-1 redis-cli -a <password> LLEN vaultkey:jobs
```

**RTO:** 5 minutes
**RPO:** In-flight jobs (< 1 minute of work)

---

## 🐛 Troubleshooting

### API Returns 500 Errors

**Check logs:**
```bash
docker compose logs api --tail 100
```

**Common causes:**
- Vault sealed
- Database connection lost
- Redis connection lost

**Fix:**
```bash
# Unseal Vault
docker exec vaultkey-vault-init-1 sh /scripts/vault-init.sh

# Restart services
docker compose restart api
```

### Jobs Stuck in "pending"

**Check worker logs:**
```bash
docker compose logs api --tail 100 | grep worker
```

**Check queue depth:**
```bash
docker exec vaultkey-redis-1 redis-cli -a <password> LLEN vaultkey:jobs
```

**Fix:**
- Increase worker concurrency: `WORKER_CONCURRENCY=20` in `.env`
- Scale workers: `docker compose up -d --scale api=2`

### Webhooks Not Delivered

**Check webhook status in database:**
```sql
SELECT COUNT(*), webhook_status FROM signing_jobs GROUP BY webhook_status;
```

**Common causes:**
- Developer's endpoint down
- Signature validation failing
- Timeout (>10 seconds)

**Debug:**
- Check webhook endpoint is reachable
- Verify HMAC signature calculation
- Increase timeout if needed

### Nonce Too Low (EVM)

**Symptom:** Relayer transactions failing with "nonce too low"

**Cause:** Redis nonce counter out of sync with chain

**Fix:**
```bash
# Automatic resync on next job
docker compose restart api

# Manual resync (optional)
docker exec vaultkey-redis-1 redis-cli -a <password> DEL "vaultkey:nonce:1:0x..."
```

---

## 📚 Additional Resources

- **Architecture Documentation:** [ARCHITECTURE.md](./ARCHITECTURE.md)
- **Testnet Guide:** [TESTNET_GUIDE.md](./TESTNET_GUIDE.md)
- **API Reference:** [API.md](./API.md)

---

## 🎯 Next Steps

After deployment:

1. **Set up monitoring** - Integrate with your APM
2. **Configure alerts** - High queue depth, webhook failures
3. **Test disaster recovery** - Simulate failures
4. **Document runbook** - Team-specific procedures
5. **Schedule backups** - Automate Vault + DB backups
6. **Review security** - Penetration testing
7. **Scale as needed** - Add workers/API instances

Happy building!