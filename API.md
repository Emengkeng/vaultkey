# VaultKey API Reference

## Base URL

```
Development: http://localhost:8080
```

## Authentication

All endpoints except `POST /projects` and `GET /health` require authentication via headers:

```http
X-API-Key: vk_live_abc123...
X-API-Secret: vk_secret_xyz789...
```

**Security:**
- API secrets are bcrypt-hashed server-side (cost 10)
- Use constant-time comparison to prevent timing attacks
- Secrets are shown only once during project creation
- Store securely (environment variables, secrets manager)

---

## Rate Limiting

**Per-project rate limiting** using sliding window algorithm:

- Default: 100 requests/second per project
- Configurable via `rate_limit_rps` during project creation
- Returns `429 Too Many Requests` when exceeded

**Response Headers:**
```http
X-RateLimit-Limit: 100
X-RateLimit-Remaining: 45
X-RateLimit-Reset: 1678896000
Retry-After: 1
```

---

## Response Format

### Success Response

```json
{
  "field1": "value1",
  "field2": "value2"
}
```

### Error Response

```json
{
  "error": "Error message describing what went wrong"
}
```

**Common HTTP Status Codes:**
- `200 OK` - Request succeeded
- `201 Created` - Resource created successfully
- `202 Accepted` - Job accepted for async processing
- `400 Bad Request` - Invalid input
- `401 Unauthorized` - Missing or invalid credentials
- `404 Not Found` - Resource not found
- `429 Too Many Requests` - Rate limit exceeded
- `500 Internal Server Error` - Server error
- `502 Bad Gateway` - RPC endpoint error
- `503 Service Unavailable` - Vault or Redis unavailable

---

## Endpoints

### Projects

#### Create Project

Creates a new project and returns API credentials.

**⚠️ The `api_secret` is shown only once. Save it immediately.**

```http
POST /projects
```

**Request Body:**
```json
{
  "name": "My Exchange",
  "webhook_url": "https://yourapp.com/webhooks/vaultkey",
  "rate_limit_rps": 500,
  "max_retries": 5
}
```

**Parameters:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Project name for identification |
| `webhook_url` | string | No | HTTPS endpoint for webhook delivery |
| `rate_limit_rps` | integer | No | Requests per second limit (default: 100) |
| `max_retries` | integer | No | Max webhook delivery retries (default: 3) |

**Response:** `201 Created`
```json
{
  "id": "proj_abc123def456",
  "name": "My Exchange",
  "api_key": "vk_live_xyz789...",
  "api_secret": "vk_secret_abc123..."
}
```

**Example:**
```bash
curl -X POST http://localhost:8080/projects \
  -H "Content-Type: application/json" \
  -d '{
    "name": "My Exchange",
    "webhook_url": "https://api.myexchange.com/webhooks/vaultkey",
    "rate_limit_rps": 500,
    "max_retries": 5
  }'
```

---

#### Update Webhook URL

Updates the webhook URL for an existing project.

```http
PATCH /project/webhook
```

**Authentication:** Required

**Request Body:**
```json
{
  "webhook_url": "https://newdomain.com/webhooks/vaultkey"
}
```

**Parameters:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `webhook_url` | string | Yes | New HTTPS webhook endpoint (empty string to disable) |

**Response:** `200 OK`
```json
{
  "status": "updated"
}
```

**Example:**
```bash
curl -X PATCH http://localhost:8080/project/webhook \
  -H "Content-Type: application/json" \
  -H "X-API-Key: vk_live_..." \
  -H "X-API-Secret: vk_secret_..." \
  -d '{
    "webhook_url": "https://api.myapp.com/webhooks"
  }'
```

---

### Wallets

#### Create Wallet

Creates a new custodial wallet for a user.

```http
POST /wallets
```

**Authentication:** Required

**Request Body:**
```json
{
  "user_id": "user_12345",
  "chain_type": "evm",
  "label": "main_wallet"
}
```

**Parameters:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `user_id` | string | Yes | Your internal user identifier |
| `chain_type` | string | Yes | `evm` or `solana` |
| `label` | string | No | Optional label for wallet identification |

**Response:** `201 Created`
```json
{
  "id": "wallet_xyz789",
  "user_id": "user_12345",
  "chain_type": "evm",
  "address": "0x742d35Cc6634C0532925a3b844Bc9e7595f0bEb",
  "label": "main_wallet",
  "created_at": "2026-03-11T12:00:00Z"
}
```

**Notes:**
- EVM wallets work on all EVM chains (Ethereum, Polygon, Arbitrum, Base, Optimism)
- Specify chain via `chain_id` parameter during signing/balance queries
- Private keys are encrypted immediately after generation
- Addresses are deterministically derived from private keys

**Example:**
```bash
curl -X POST http://localhost:8080/wallets \
  -H "Content-Type: application/json" \
  -H "X-API-Key: vk_live_..." \
  -H "X-API-Secret: vk_secret_..." \
  -d '{
    "user_id": "user_12345",
    "chain_type": "evm",
    "label": "trading_wallet"
  }'
```

---

#### Get Wallet

Retrieves details of a specific wallet.

```http
GET /wallets/{walletId}
```

**Authentication:** Required

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| `walletId` | string | Wallet ID from creation response |

**Response:** `200 OK`
```json
{
  "id": "wallet_xyz789",
  "user_id": "user_12345",
  "chain_type": "evm",
  "address": "0x742d35Cc6634C0532925a3b844Bc9e7595f0bEb",
  "label": "main_wallet",
  "created_at": "2026-03-11T12:00:00Z"
}
```

**Example:**
```bash
curl -X GET http://localhost:8080/wallets/wallet_xyz789 \
  -H "X-API-Key: vk_live_..." \
  -H "X-API-Secret: vk_secret_..."
```

---

#### List User Wallets

Lists all wallets for a specific user.

```http
GET /users/{userId}/wallets
```

**Authentication:** Required

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| `userId` | string | Your internal user identifier |

**Response:** `200 OK`
```json
{
  "wallets": [
    {
      "id": "wallet_xyz789",
      "user_id": "user_12345",
      "chain_type": "evm",
      "address": "0x742d35Cc6634C0532925a3b844Bc9e7595f0bEb",
      "label": "main_wallet",
      "created_at": "2026-03-11T12:00:00Z"
    },
    {
      "id": "wallet_abc456",
      "user_id": "user_12345",
      "chain_type": "solana",
      "address": "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU",
      "label": "sol_wallet",
      "created_at": "2026-03-11T13:00:00Z"
    }
  ]
}
```

**Example:**
```bash
curl -X GET http://localhost:8080/users/user_12345/wallets \
  -H "X-API-Key: vk_live_..." \
  -H "X-API-Secret: vk_secret_..."
```

---

### Signing (Async)

All signing operations are **asynchronous**. The API returns a `job_id` immediately, and the result is delivered via webhook when complete.

#### Sign EVM Transaction

Submits an EVM transaction for signing.

```http
POST /wallets/{walletId}/sign/transaction/evm
```

**Authentication:** Required

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| `walletId` | string | Wallet ID |

**Request Body:**
```json
{
  "idempotency_key": "tx_user123_20260311_001",
  "gasless": false,
  "payload": {
    "to": "0x742d35Cc6634C0532925a3b844Bc9e7595f0bEb",
    "value": "0x16345785D8A0000",
    "data": "0x",
    "gas_limit": 21000,
    "gas_price": "0x3B9ACA00",
    "chain_id": 1
  }
}
```

**Parameters:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `idempotency_key` | string | No | Deduplication key (prevents duplicate submissions) |
| `gasless` | boolean | No | Use relayer to pay gas (default: false) |
| `payload.to` | string | Yes | Recipient address (0x...) |
| `payload.value` | string | Yes | Amount in wei (hex string, e.g., "0x0" for token transfers) |
| `payload.data` | string | No | Contract call data (hex string) |
| `payload.gas_limit` | integer | Yes* | Gas limit (*Not required for gasless) |
| `payload.gas_price` | string | Yes* | Gas price in wei (hex string, *Not required for gasless) |
| `payload.chain_id` | integer | Yes | Chain ID (1=Ethereum, 137=Polygon, etc.) |

**Chain IDs:**
| Network | Mainnet | Testnet | Testnet Name |
|---------|---------|---------|--------------|
| Ethereum | 1 | 11155111 | Sepolia |
| Polygon | 137 | 80001 | Mumbai |
| Arbitrum | 42161 | 421614 | Arbitrum Sepolia |
| Base | 8453 | 84532 | Base Sepolia |
| Optimism | 10 | 11155420 | Optimism Sepolia |

**Response:** `202 Accepted`
```json
{
  "job_id": "job_abc123",
  "status": "pending"
}
```

**Example:**
```bash
curl -X POST http://localhost:8080/wallets/wallet_xyz789/sign/transaction/evm \
  -H "Content-Type: application/json" \
  -H "X-API-Key: vk_live_..." \
  -H "X-API-Secret: vk_secret_..." \
  -d '{
    "idempotency_key": "tx_001",
    "payload": {
      "to": "0x742d35Cc6634C0532925a3b844Bc9e7595f0bEb",
      "value": "0x16345785D8A0000",
      "data": "0x",
      "gas_limit": 21000,
      "gas_price": "0x3B9ACA00",
      "chain_id": 1
    }
  }'
```

---

#### Sign EVM Message

Signs an arbitrary message (EIP-191 personal_sign).

```http
POST /wallets/{walletId}/sign/message/evm
```

**Authentication:** Required

**Request Body:**
```json
{
  "idempotency_key": "msg_user123_001",
  "payload": {
    "message": "Sign in to MyApp"
  }
}
```

**Parameters:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `idempotency_key` | string | No | Deduplication key |
| `payload.message` | string | Yes | Message to sign |

**Response:** `202 Accepted`
```json
{
  "job_id": "job_def456",
  "status": "pending"
}
```

**Example:**
```bash
curl -X POST http://localhost:8080/wallets/wallet_xyz789/sign/message/evm \
  -H "Content-Type: application/json" \
  -H "X-API-Key: vk_live_..." \
  -H "X-API-Secret: vk_secret_..." \
  -d '{
    "payload": {
      "message": "Welcome to MyApp! Sign this message to prove you own this wallet."
    }
  }'
```

---

#### Sign Solana Transaction

Submits a Solana transaction for signing.

```http
POST /wallets/{walletId}/sign/transaction/solana
```

**Authentication:** Required

**Request Body (Regular):**
```json
{
  "idempotency_key": "tx_sol_001",
  "gasless": false,
  "payload": {
    "message": "01000103c8d842..."
  }
}
```

**Request Body (Gasless):**
```json
{
  "idempotency_key": "tx_sol_002",
  "gasless": true,
  "payload": {
    "to": "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU",
    "amount": 1000000,
    "token_mint": "",
    "source_token_account": "",
    "dest_token_account": ""
  }
}
```

**Parameters (Regular):**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `payload.message` | string | Yes | Serialized transaction message (hex string) |

**Parameters (Gasless - Native SOL):**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `payload.to` | string | Yes | Recipient address (base58) |
| `payload.amount` | integer | Yes | Amount in lamports |

**Parameters (Gasless - SPL Token):**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `payload.to` | string | Yes | Recipient address |
| `payload.amount` | integer | Yes | Amount in base units |
| `payload.token_mint` | string | Yes | Token mint address |
| `payload.source_token_account` | string | Yes | Sender's token account |
| `payload.dest_token_account` | string | Yes | Recipient's token account |

**Response:** `202 Accepted`
```json
{
  "job_id": "job_ghi789",
  "status": "pending"
}
```

**Example (Gasless SOL Transfer):**
```bash
curl -X POST http://localhost:8080/wallets/wallet_sol123/sign/transaction/solana \
  -H "Content-Type: application/json" \
  -H "X-API-Key: vk_live_..." \
  -H "X-API-Secret: vk_secret_..." \
  -d '{
    "gasless": true,
    "payload": {
      "to": "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU",
      "amount": 1000000
    }
  }'
```

---

#### Sign Solana Message

Signs an arbitrary message with a Solana wallet.

```http
POST /wallets/{walletId}/sign/message/solana
```

**Authentication:** Required

**Request Body:**
```json
{
  "idempotency_key": "msg_sol_001",
  "payload": {
    "message": "Sign this message to authenticate"
  }
}
```

**Response:** `202 Accepted`
```json
{
  "job_id": "job_jkl012",
  "status": "pending"
}
```

---

### Jobs

#### Get Job Status

Retrieves the current status of a signing job.

```http
GET /jobs/{jobId}
```

**Authentication:** Required

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| `jobId` | string | Job ID from signing request |

**Response:** `200 OK`

**Pending Job:**
```json
{
  "id": "job_abc123",
  "project_id": "proj_xyz",
  "wallet_id": "wallet_123",
  "operation": "sign_tx_evm",
  "status": "pending",
  "attempts": 0,
  "gasless": false,
  "webhook_status": "pending",
  "created_at": "2026-03-11T12:00:00Z",
  "updated_at": "2026-03-11T12:00:00Z"
}
```

**Completed Job:**
```json
{
  "id": "job_abc123",
  "project_id": "proj_xyz",
  "wallet_id": "wallet_123",
  "operation": "sign_tx_evm",
  "status": "completed",
  "result": {
    "signed_tx": "0xf86c808504a817c800825208..."
  },
  "attempts": 1,
  "gasless": false,
  "webhook_status": "delivered",
  "created_at": "2026-03-11T12:00:00Z",
  "updated_at": "2026-03-11T12:00:05Z"
}
```

**Failed Job:**
```json
{
  "id": "job_abc123",
  "project_id": "proj_xyz",
  "wallet_id": "wallet_123",
  "operation": "sign_tx_evm",
  "status": "failed",
  "error": "insufficient funds for gas",
  "attempts": 3,
  "gasless": false,
  "webhook_status": "pending",
  "created_at": "2026-03-11T12:00:00Z",
  "updated_at": "2026-03-11T12:00:15Z"
}
```

**Status Values:**
- `pending` - Waiting in queue
- `processing` - Currently being processed
- `completed` - Successfully signed
- `failed` - Failed but will retry
- `dead` - Failed after max retries (moved to DLQ)

**Example:**
```bash
curl -X GET http://localhost:8080/jobs/job_abc123 \
  -H "X-API-Key: vk_live_..." \
  -H "X-API-Secret: vk_secret_..."
```

---

### Balance & Broadcast

#### Get Wallet Balance

Queries the on-chain balance of a wallet.

```http
GET /wallets/{walletId}/balance
```

**Authentication:** Required

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| `walletId` | string | Wallet ID |

**Query Parameters (EVM only):**
| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `chain_id` | string | Yes | Chain ID (e.g., "1" for Ethereum) |

**Response (EVM):** `200 OK`
```json
{
  "address": "0x742d35Cc6634C0532925a3b844Bc9e7595f0bEb",
  "balance": "0x16345785D8A0000",
  "chain_id": "1",
  "unit": "wei"
}
```

**Response (Solana):** `200 OK`
```json
{
  "address": "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU",
  "balance": 5000000,
  "unit": "lamports"
}
```

**Example (EVM):**
```bash
curl -X GET "http://localhost:8080/wallets/wallet_xyz789/balance?chain_id=1" \
  -H "X-API-Key: vk_live_..." \
  -H "X-API-Secret: vk_secret_..."
```

**Example (Solana):**
```bash
curl -X GET "http://localhost:8080/wallets/wallet_sol123/balance" \
  -H "X-API-Key: vk_live_..." \
  -H "X-API-Secret: vk_secret_..."
```

---

#### Broadcast Transaction

Broadcasts a signed transaction to the blockchain.

```http
POST /wallets/{walletId}/broadcast
```

**Authentication:** Required

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| `walletId` | string | Wallet ID |

**Request Body (EVM):**
```json
{
  "signed_tx": "0xf86c808504a817c800825208...",
  "chain_id": "1"
}
```

**Request Body (Solana):**
```json
{
  "signed_tx": "AQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAACAAQABAgMEBQYH..."
}
```

**Parameters:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `signed_tx` | string | Yes | Signed transaction (hex for EVM, base64 for Solana) |
| `chain_id` | string | Yes* | Chain ID (*EVM only) |

**Response (EVM):** `200 OK`
```json
{
  "tx_hash": "0x1234567890abcdef..."
}
```

**Response (Solana):** `200 OK`
```json
{
  "signature": "5VERv8NMvzbJMEkV8xnrLkEaWRtSz9CosKDYjCJjBRnbJLgp8uirBgmQpjKhoR4tjF3ZpRzrFmBV6UjKdiSZkQtg"
}
```

**Example:**
```bash
curl -X POST http://localhost:8080/wallets/wallet_xyz789/broadcast \
  -H "Content-Type: application/json" \
  -H "X-API-Key: vk_live_..." \
  -H "X-API-Secret: vk_secret_..." \
  -d '{
    "signed_tx": "0xf86c808504a817c800825208...",
    "chain_id": "1"
  }'
```

---

### Relayer Management

#### Register Relayer Wallet

Creates a relayer wallet for gasless transactions.

```http
POST /projects/relayer
```

**Authentication:** Required

**Request Body:**
```json
{
  "chain_type": "evm",
  "chain_id": "1",
  "min_balance_alert": "0.1"
}
```

**Parameters:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `chain_type` | string | Yes | `evm` or `solana` |
| `chain_id` | string | Yes* | Chain ID (*Required for EVM, omit for Solana) |
| `min_balance_alert` | string | No | Alert threshold in ETH/SOL (default: "0.1") |

**Response:** `201 Created`
```json
{
  "id": "relayer_abc123",
  "wallet_id": "wallet_relay_xyz",
  "address": "0x9876543210abcdef...",
  "chain_type": "evm",
  "chain_id": "1",
  "min_balance_alert": "0.1",
  "active": true
}
```

**⚠️ Important:** Fund this address with ETH/SOL immediately. Without funds, gasless transactions will fail.

**Example:**
```bash
curl -X POST http://localhost:8080/projects/relayer \
  -H "Content-Type: application/json" \
  -H "X-API-Key: vk_live_..." \
  -H "X-API-Secret: vk_secret_..." \
  -d '{
    "chain_type": "evm",
    "chain_id": "1",
    "min_balance_alert": "0.05"
  }'
```

---

#### Get Relayer Info

Retrieves balance and health status of a relayer wallet.

```http
GET /projects/relayer
```

**Authentication:** Required

**Query Parameters:**
| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `chain_type` | string | Yes | `evm` or `solana` |
| `chain_id` | string | Yes* | Chain ID (*EVM only) |

**Response:** `200 OK`
```json
{
  "wallet_id": "wallet_relay_xyz",
  "address": "0x9876543210abcdef...",
  "chain_type": "evm",
  "chain_id": "1",
  "balance": "0x16345785D8A0000",
  "unit": "wei",
  "healthy": true
}
```

**Health Status:**
- `healthy: true` - Balance above minimum threshold (0.05 ETH or 50M lamports)
- `healthy: false` - Balance too low, gasless transactions will be rejected

**Example:**
```bash
curl -X GET "http://localhost:8080/projects/relayer?chain_type=evm&chain_id=1" \
  -H "X-API-Key: vk_live_..." \
  -H "X-API-Secret: vk_secret_..."
```

---

#### List Relayer Wallets

Lists all relayer wallets for the project.

```http
GET /projects/relayers
```

**Authentication:** Required

**Response:** `200 OK`
```json
{
  "relayers": [
    {
      "id": "relayer_abc123",
      "wallet_id": "wallet_relay_xyz",
      "address": "0x9876543210abcdef...",
      "chain_type": "evm",
      "chain_id": "1",
      "min_balance_alert": "0.1",
      "active": true
    },
    {
      "id": "relayer_def456",
      "wallet_id": "wallet_relay_sol",
      "address": "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU",
      "chain_type": "solana",
      "chain_id": "",
      "min_balance_alert": "0.1",
      "active": true
    }
  ]
}
```

**Example:**
```bash
curl -X GET http://localhost:8080/projects/relayers \
  -H "X-API-Key: vk_live_..." \
  -H "X-API-Secret: vk_secret_..."
```

---

#### Deactivate Relayer

Deactivates a relayer wallet (stops accepting gasless transactions).

```http
DELETE /projects/relayer/{relayerId}
```

**Authentication:** Required

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| `relayerId` | string | Relayer ID from list/create response |

**Response:** `200 OK`
```json
{
  "status": "deactivated"
}
```

**Note:** This does NOT delete the wallet or its funds. It only marks it as inactive.

**Example:**
```bash
curl -X DELETE http://localhost:8080/projects/relayer/relayer_abc123 \
  -H "X-API-Key: vk_live_..." \
  -H "X-API-Secret: vk_secret_..."
```

---

### Sweep

Sweep lets you automatically consolidate funds from user wallets into a single master wallet per chain. Useful for neobanks and exchange backends that need to pool deposits.

**Prerequisites for sweep to work:**
- A master wallet must be provisioned for the target chain
- A relayer wallet must be configured and funded on the same chain
- Sweep is supported on EVM L2s and Solana (not Ethereum mainnet)

#### Provision Master Wallet

Creates a new dedicated wallet and designates it as the sweep destination for a given chain. VaultKey always generates the wallet — you do not supply one.

Calling this again for an already-configured chain is idempotent: it returns the existing config without creating a new wallet.

```http
POST /projects/master-wallet
```

**Authentication:** Required

**Request Body:**
```json
{
  "chain_type": "evm",
  "chain_id": "137",
  "dust_threshold": "1000000000000000"
}
```

**Parameters:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `chain_type` | string | Yes | `evm` or `solana` |
| `chain_id` | string | Yes* | Chain ID (*Required for EVM, must be omitted for Solana) |
| `dust_threshold` | string | No | Minimum balance to sweep in native units (default: "0") |

**Response:** `201 Created`
```json
{
  "id": "sweepconfig_abc123",
  "chain_type": "evm",
  "chain_id": "137",
  "master_wallet_id": "wallet_master_xyz",
  "master_address": "0x9876543210abcdef...",
  "dust_threshold": "1000000000000000",
  "enabled": true
}
```

**Example:**
```bash
curl -X POST http://localhost:8080/projects/master-wallet \
  -H "Content-Type: application/json" \
  -H "X-API-Key: vk_live_..." \
  -H "X-API-Secret: vk_secret_..." \
  -d '{
    "chain_type": "evm",
    "chain_id": "137",
    "dust_threshold": "1000000000000000"
  }'
```

---

#### Get Master Wallet

Returns the sweep config and master wallet address for a specific chain.

```http
GET /projects/master-wallet
```

**Authentication:** Required

**Query Parameters:**
| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `chain_type` | string | Yes | `evm` or `solana` |
| `chain_id` | string | Yes* | Chain ID (*EVM only) |

**Response:** `200 OK`
```json
{
  "id": "sweepconfig_abc123",
  "chain_type": "evm",
  "chain_id": "137",
  "master_wallet_id": "wallet_master_xyz",
  "master_address": "0x9876543210abcdef...",
  "dust_threshold": "1000000000000000",
  "enabled": true
}
```

**Example (EVM):**
```bash
curl -X GET "http://localhost:8080/projects/master-wallet?chain_type=evm&chain_id=137" \
  -H "X-API-Key: vk_live_..." \
  -H "X-API-Secret: vk_secret_..."
```

**Example (Solana):**
```bash
curl -X GET "http://localhost:8080/projects/master-wallet?chain_type=solana" \
  -H "X-API-Key: vk_live_..." \
  -H "X-API-Secret: vk_secret_..."
```

---

#### List Master Wallets

Lists all sweep configs across all chains for the project.

```http
GET /projects/master-wallets
```

**Authentication:** Required

**Response:** `200 OK`
```json
{
  "master_wallets": [
    {
      "id": "sweepconfig_abc123",
      "chain_type": "evm",
      "chain_id": "137",
      "master_wallet_id": "wallet_master_xyz",
      "master_address": "0x9876543210abcdef...",
      "dust_threshold": "1000000000000000",
      "enabled": true
    },
    {
      "id": "sweepconfig_def456",
      "chain_type": "solana",
      "chain_id": "",
      "master_wallet_id": "wallet_master_sol",
      "master_address": "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU",
      "dust_threshold": "0",
      "enabled": true
    }
  ]
}
```

**Example:**
```bash
curl -X GET http://localhost:8080/projects/master-wallets \
  -H "X-API-Key: vk_live_..." \
  -H "X-API-Secret: vk_secret_..."
```

---

#### Update Sweep Config

Updates the `dust_threshold` and/or `enabled` flag for a sweep config.

```http
PATCH /projects/master-wallet/{configId}
```

**Authentication:** Required

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| `configId` | string | Sweep config ID from provision/list response |

**Request Body:**
```json
{
  "dust_threshold": "5000000000000000",
  "enabled": false
}
```

**Parameters:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `dust_threshold` | string | No | New minimum balance to sweep in native units |
| `enabled` | boolean | No | Enable or disable sweeping for this chain |

Omitted fields retain their current values.

**Response:** `200 OK`
```json
{
  "status": "updated"
}
```

**Example:**
```bash
curl -X PATCH http://localhost:8080/projects/master-wallet/sweepconfig_abc123 \
  -H "Content-Type: application/json" \
  -H "X-API-Key: vk_live_..." \
  -H "X-API-Secret: vk_secret_..." \
  -d '{
    "dust_threshold": "5000000000000000",
    "enabled": true
  }'
```

---

#### Trigger Sweep

Enqueues a sweep job for a wallet. The wallet's entire balance (above `dust_threshold`) is transferred to the master wallet. Gas is covered by the configured relayer.

```http
POST /wallets/{walletId}/sweep
```

**Authentication:** Required

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| `walletId` | string | Wallet ID to sweep from |

**Request Body:**
```json
{
  "chain_type": "evm",
  "chain_id": "137",
  "idempotency_key": "sweep_user123_20260311"
}
```

**Parameters:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `chain_type` | string | Yes | `evm` or `solana` |
| `chain_id` | string | Yes* | Chain ID (*Required for EVM, must be omitted for Solana) |
| `idempotency_key` | string | No | Deduplication key — reuses existing job if already submitted |

**Response:** `202 Accepted` (new job)
```json
{
  "job_id": "job_sweep_abc123",
  "status": "pending"
}
```

**Response:** `200 OK` (job already in progress or completed)
```json
{
  "job_id": "job_sweep_abc123",
  "status": "processing"
}
```

**Notes:**
- Requires a funded relayer on the target chain
- Requires a master wallet provisioned for the target chain
- Ethereum mainnet (`chain_id: 1`) is not supported for sweep
- Use `GET /jobs/{jobId}` or webhooks to track the result

**Example:**
```bash
curl -X POST http://localhost:8080/wallets/wallet_xyz789/sweep \
  -H "Content-Type: application/json" \
  -H "X-API-Key: vk_live_..." \
  -H "X-API-Secret: vk_secret_..." \
  -d '{
    "chain_type": "evm",
    "chain_id": "137",
    "idempotency_key": "sweep_user123_20260311"
  }'
```

---

---

### Stablecoins

Transfer and query balances for USDC and USDT across EVM chains and Solana. All transfers are asynchronous — the API returns a `job_id` immediately and delivers the result via webhook.

**Supported tokens:** `usdc`, `usdt`

**Supported chains (EVM):**
| Network | Chain ID |
|---------|----------|
| Polygon | 137 |
| Arbitrum | 42161 |
| Base | 8453 |
| Optimism | 10 |
| BSC | 56 |

**Supported chains (Solana):** Mainnet only — omit `chain_id`.

> **Note:** BSC USDC/USDT use 18 decimals. All other chains use 6.

---

#### Transfer Stablecoin

Transfers a stablecoin from a user wallet to a recipient address. The amount is specified in human-readable form (e.g. `"50.00"`).

**EVM:**
```http
POST /wallets/{walletId}/stablecoin/transfer/evm
```

**Solana:**
```http
POST /wallets/{walletId}/stablecoin/transfer/solana
```

**Authentication:** Required

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| `walletId` | string | Wallet ID — must match the chain type in the URL |

**Request Body (EVM):**
```json
{
  "token": "usdc",
  "to": "0x742d35Cc6634C0532925a3b844Bc9e7595f0bEb",
  "amount": "50.00",
  "chain_id": "137",
  "gasless": true,
  "idempotency_key": "transfer_user123_20260311_001"
}
```

**Request Body (Solana):**
```json
{
  "token": "usdc",
  "to": "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU",
  "amount": "50.00",
  "idempotency_key": "transfer_user123_sol_001"
}
```

**Parameters:**
| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `token` | string | Yes | `usdc` or `usdt` |
| `to` | string | Yes | Recipient address (`0x...` for EVM, base58 for Solana) |
| `amount` | string | Yes | Human-readable amount, e.g. `"50.00"` |
| `chain_id` | string | EVM only | Chain ID — required for EVM, must be omitted for Solana |
| `gasless` | boolean | EVM only | `true` = relayer pays gas. Solana transfers always require a relayer. |
| `idempotency_key` | string | No | Deduplication key — safe to retry |

**Response:** `202 Accepted` (new job)
```json
{
  "job_id": "job_abc123",
  "status": "pending"
}
```

**Response:** `200 OK` (idempotency hit — job already in progress or complete)
```json
{
  "job_id": "job_abc123",
  "status": "completed"
}
```

**Webhook result on completion:**
```json
{
  "job_id": "job_abc123",
  "operation": "sign_tx_evm",
  "status": "completed",
  "result": {
    "tx_hash": "0x1234..."
  }
}
```

**Validation errors returned as `400 Bad Request`:**
- Token not registered for this chain
- Wallet chain type doesn't match URL chain type
- Master wallet attempted as sender
- Zero or insufficient token balance
- No relayer configured (gasless or Solana)
- Relayer balance below minimum (0.05 ETH / 0.05 SOL)
- Invalid recipient address format

**Prerequisites:**
- For `gasless: true` (EVM) or any Solana transfer: a relayer wallet must be registered and funded via `POST /projects/relayer`
- Token must be registered for the target chain (mainnet defaults are pre-seeded; testnets require manual setup via `POST /admin/stablecoins`)

**Example (EVM, gasless):**
```bash
curl -X POST http://localhost:8080/wallets/wallet_xyz789/stablecoin/transfer/evm \
  -H "Content-Type: application/json" \
  -H "X-API-Key: vk_live_..." \
  -H "X-API-Secret: vk_secret_..." \
  -d '{
    "token": "usdc",
    "to": "0x742d35Cc6634C0532925a3b844Bc9e7595f0bEb",
    "amount": "50.00",
    "chain_id": "137",
    "gasless": true,
    "idempotency_key": "transfer_user123_20260311_001"
  }'
```

**Example (Solana):**
```bash
curl -X POST http://localhost:8080/wallets/wallet_sol123/stablecoin/transfer/solana \
  -H "Content-Type: application/json" \
  -H "X-API-Key: vk_live_..." \
  -H "X-API-Secret: vk_secret_..." \
  -d '{
    "token": "usdc",
    "to": "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU",
    "amount": "50.00",
    "idempotency_key": "transfer_user123_sol_001"
  }'
```

---

#### Get Stablecoin Balance

Returns the token balance for a wallet on a specific chain.

**EVM:**
```http
GET /wallets/{walletId}/stablecoin/balance/evm
```

**Solana:**
```http
GET /wallets/{walletId}/stablecoin/balance/solana
```

**Authentication:** Required

**Query Parameters:**
| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `token` | string | Yes | `usdc` or `usdt` |
| `chain_id` | string | EVM only | Chain ID — required for EVM, omit for Solana |

**Response:** `200 OK`
```json
{
  "address": "0x742d35Cc6634C0532925a3b844Bc9e7595f0bEb",
  "token": "usdc",
  "symbol": "USDC",
  "balance": "50.000000",
  "raw_balance": "50000000",
  "chain_id": "137"
}
```

**Fields:**
| Field | Type | Description |
|-------|------|-------------|
| `address` | string | Wallet address |
| `token` | string | Token identifier |
| `symbol` | string | Display symbol (e.g. `USDC`) |
| `balance` | string | Human-readable balance |
| `raw_balance` | string | Balance in base units (for precision-sensitive use) |
| `chain_id` | string | EVM only — omitted for Solana |

**Example (EVM):**
```bash
curl -X GET "http://localhost:8080/wallets/wallet_xyz789/stablecoin/balance/evm?token=usdc&chain_id=137" \
  -H "X-API-Key: vk_live_..." \
  -H "X-API-Secret: vk_secret_..."
```

**Example (Solana):**
```bash
curl -X GET "http://localhost:8080/wallets/wallet_sol123/stablecoin/balance/solana?token=usdc" \
  -H "X-API-Key: vk_live_..." \
  -H "X-API-Secret: vk_secret_..."
```

### Health Check

#### System Health

Checks connectivity to Vault and Redis.

```http
GET /health
```

**Authentication:** Not required

**Response:** `200 OK` (healthy) or `503 Service Unavailable` (unhealthy)
```json
{
  "vault": "ok",
  "redis": "ok"
}
```

**Example:**
```bash
curl http://localhost:8080/health
```

---

## Webhooks

VaultKey delivers job results to your webhook endpoint via HTTP POST.

### Webhook Payload

**Format:**
```json
{
  "job_id": "job_abc123",
  "project_id": "proj_xyz",
  "wallet_id": "wallet_123",
  "operation": "sign_tx_evm",
  "status": "completed",
  "result": {
    "signed_tx": "0xf86c808504a817c800825208..."
  },
  "error": "",
  "timestamp": "2026-03-11T12:00:05Z"
}
```

**Fields:**
| Field | Type | Description |
|-------|------|-------------|
| `job_id` | string | Job ID |
| `project_id` | string | Your project ID |
| `wallet_id` | string | Wallet ID |
| `operation` | string | Operation type (`sign_tx_evm`, `sign_msg_evm`, `sweep`, etc.) |
| `status` | string | `completed`, `failed`, or `dead` |
| `result` | object | Signing result (only present if `status=completed`) |
| `error` | string | Error message (only present if `status=failed` or `dead`) |
| `timestamp` | string | ISO 8601 timestamp |

### Webhook Headers

```http
POST /your-webhook-endpoint
Content-Type: application/json
User-Agent: VaultKey-Webhook/1.0
X-VaultKey-Signature: sha256=abc123def456...
X-VaultKey-Timestamp: 2026-03-11T12:00:05Z
```

### Signature Verification

**Algorithm:** HMAC-SHA256

**Pseudocode:**
```javascript
const crypto = require('crypto');

function verifyWebhook(req) {
  const signature = req.headers['x-vaultkey-signature'];
  const timestamp = req.headers['x-vaultkey-timestamp'];
  const body = req.body; // Raw JSON string
  
  // Check timestamp (prevent replay attacks)
  const requestTime = new Date(timestamp);
  const now = new Date();
  if (Math.abs(now - requestTime) > 300000) { // 5 minutes
    return false;
  }
  
  // Compute HMAC
  const hmac = crypto.createHmac('sha256', WEBHOOK_SECRET);
  hmac.update(body);
  const computed = 'sha256=' + hmac.digest('hex');
  
  // Constant-time comparison
  return crypto.timingSafeEqual(
    Buffer.from(signature),
    Buffer.from(computed)
  );
}
```

**Node.js Example:**
```javascript
const express = require('express');
const crypto = require('crypto');

const app = express();
const WEBHOOK_SECRET = process.env.WEBHOOK_SECRET;

app.post('/webhooks/vaultkey', express.json(), (req, res) => {
  const signature = req.headers['x-vaultkey-signature'];
  const body = JSON.stringify(req.body);
  
  const hmac = crypto.createHmac('sha256', WEBHOOK_SECRET);
  hmac.update(body);
  const computed = 'sha256=' + hmac.digest('hex');
  
  if (!crypto.timingSafeEqual(Buffer.from(signature), Buffer.from(computed))) {
    return res.status(401).send('Invalid signature');
  }
  
  const { job_id, status, result, error } = req.body;
  
  if (status === 'completed') {
    console.log('Job completed:', job_id, result);
    // Update your database, notify user, etc.
  } else {
    console.error('Job failed:', job_id, error);
    // Handle failure
  }
  
  res.status(200).send('OK');
});
```

### Retry Behavior

- Failed deliveries retry with exponential backoff: 1s, 2s, 4s, 8s, 16s...
- Max retries configured per project (default: 3)
- After exhausting retries, job moves to dead letter queue
- Webhook endpoint must return `2xx` status code for success

---

## Code Examples

### Stablecoin Transfer (Node.js)
```javascript
const axios = require('axios');

const client = axios.create({
  baseURL: 'http://localhost:8080',
  headers: { 'X-API-Key': 'vk_live_...', 'X-API-Secret': 'vk_secret_...' }
});

async function transferUSDC(walletId, recipient, amount) {
  // 1. Check balance before submitting
  const { data: bal } = await client.get(
    `/wallets/${walletId}/stablecoin/balance/evm`,
    { params: { token: 'usdc', chain_id: '137' } }
  );
  console.log(`Current balance: ${bal.balance} USDC`);

  // 2. Submit transfer
  const { data: job } = await client.post(
    `/wallets/${walletId}/stablecoin/transfer/evm`,
    {
      token: 'usdc',
      to: recipient,
      amount,
      chain_id: '137',
      gasless: true,
      idempotency_key: `transfer_${walletId}_${Date.now()}`
    }
  );
  console.log('Job submitted:', job.job_id);

  // 3. Poll for result (prefer webhooks in production)
  while (true) {
    const { data: status } = await client.get(`/jobs/${job.job_id}`);
    if (status.status === 'completed') {
      console.log('Transfer complete. Tx hash:', status.result.tx_hash);
      return status.result;
    }
    if (status.status === 'failed' || status.status === 'dead') {
      throw new Error(`Transfer failed: ${status.error}`);
    }
    await new Promise(r => setTimeout(r, 1000));
  }
}

transferUSDC('wallet_xyz789', '0x742d35Cc6634C0532925a3b844Bc9e7595f0bEb', '50.00');
```

### Complete Workflow (Node.js)

```javascript
const axios = require('axios');

const API_BASE = 'http://localhost:8080';
const API_KEY = 'vk_live_...';
const API_SECRET = 'vk_secret_...';

const client = axios.create({
  baseURL: API_BASE,
  headers: {
    'X-API-Key': API_KEY,
    'X-API-Secret': API_SECRET
  }
});

async function main() {
  // 1. Create a wallet
  const wallet = await client.post('/wallets', {
    user_id: 'user_12345',
    chain_type: 'evm',
    label: 'main'
  });
  console.log('Wallet created:', wallet.data.address);
  
  // 2. Check balance
  const balance = await client.get(`/wallets/${wallet.data.id}/balance?chain_id=1`);
  console.log('Balance:', balance.data.balance, 'wei');
  
  // 3. Submit signing job
  const job = await client.post(`/wallets/${wallet.data.id}/sign/transaction/evm`, {
    idempotency_key: 'tx_001',
    payload: {
      to: '0x742d35Cc6634C0532925a3b844Bc9e7595f0bEb',
      value: '0x16345785D8A0000',
      data: '0x',
      gas_limit: 21000,
      gas_price: '0x3B9ACA00',
      chain_id: 1
    }
  });
  console.log('Job submitted:', job.data.job_id);
  
  // 4. Poll for result (or wait for webhook)
  let result;
  while (true) {
    const status = await client.get(`/jobs/${job.data.job_id}`);
    if (status.data.status === 'completed') {
      result = status.data.result;
      break;
    } else if (status.data.status === 'failed' || status.data.status === 'dead') {
      throw new Error(`Job failed: ${status.data.error}`);
    }
    await new Promise(r => setTimeout(r, 1000)); // Wait 1 second
  }
  console.log('Signed transaction:', result.signed_tx);
  
  // 5. Broadcast
  const broadcast = await client.post(`/wallets/${wallet.data.id}/broadcast`, {
    signed_tx: result.signed_tx,
    chain_id: '1'
  });
  console.log('Transaction hash:', broadcast.data.tx_hash);
}

main();
```

### Gasless Transaction (Python)

```python
import requests
import time

API_BASE = 'http://localhost:8080'
API_KEY = 'vk_live_...'
API_SECRET = 'vk_secret_...'

headers = {
    'X-API-Key': API_KEY,
    'X-API-Secret': API_SECRET
}

# 1. Register relayer (one-time setup)
relayer = requests.post(f'{API_BASE}/projects/relayer', 
    headers=headers,
    json={
        'chain_type': 'evm',
        'chain_id': '1',
        'min_balance_alert': '0.05'
    }
).json()
print(f'Relayer address: {relayer["address"]}')
print('Fund this address with ETH before proceeding!')

# 2. Create user wallet
wallet = requests.post(f'{API_BASE}/wallets',
    headers=headers,
    json={
        'user_id': 'user_12345',
        'chain_type': 'evm'
    }
).json()

# 3. Submit gasless transaction
job = requests.post(f'{API_BASE}/wallets/{wallet["id"]}/sign/transaction/evm',
    headers=headers,
    json={
        'gasless': True,  # Relayer pays gas!
        'payload': {
            'to': '0x742d35Cc6634C0532925a3b844Bc9e7595f0bEb',
            'value': '0x0',
            'data': '0xa9059cbb...',  # Token transfer calldata
            'chain_id': 1
        }
    }
).json()

# 4. Wait for webhook or poll
while True:
    status = requests.get(f'{API_BASE}/jobs/{job["job_id"]}', headers=headers).json()
    if status['status'] == 'completed':
        print(f'Signed: {status["result"]["signed_tx"]}')
        break
    time.sleep(1)
```

### Sweep Workflow (Python)

```python
import requests
import time

API_BASE = 'http://localhost:8080'
headers = {
    'X-API-Key': 'vk_live_...',
    'X-API-Secret': 'vk_secret_...'
}

# 1. One-time setup: provision master wallet for Polygon
master = requests.post(f'{API_BASE}/projects/master-wallet',
    headers=headers,
    json={
        'chain_type': 'evm',
        'chain_id': '137',
        'dust_threshold': '1000000000000000'  # 0.001 MATIC
    }
).json()
print(f'Master wallet: {master["master_address"]}')

# 2. One-time setup: register and fund a relayer on Polygon
relayer = requests.post(f'{API_BASE}/projects/relayer',
    headers=headers,
    json={'chain_type': 'evm', 'chain_id': '137'}
).json()
print(f'Fund relayer at: {relayer["address"]}')

# 3. Trigger sweep for a user wallet
job = requests.post(f'{API_BASE}/wallets/wallet_xyz789/sweep',
    headers=headers,
    json={
        'chain_type': 'evm',
        'chain_id': '137',
        'idempotency_key': 'sweep_user123_20260311'
    }
).json()
print(f'Sweep job: {job["job_id"]}')

# 4. Poll for result
while True:
    status = requests.get(f'{API_BASE}/jobs/{job["job_id"]}', headers=headers).json()
    if status['status'] == 'completed':
        print('Sweep complete')
        break
    elif status['status'] in ('failed', 'dead'):
        print(f'Sweep failed: {status["error"]}')
        break
    time.sleep(1)
```

---

## Error Handling

### Common Errors

**401 Unauthorized**
```json
{"error": "invalid credentials"}
```
- Invalid API key or secret
- Missing authentication headers

**400 Bad Request**
```json
{"error": "chain_id is required for EVM wallets"}
```
- Missing required field
- Invalid input format

**404 Not Found**
```json
{"error": "wallet not found"}
```
- Wallet ID doesn't exist
- Job ID doesn't exist
- Resource belongs to different project

**429 Too Many Requests**
```json
{"error": "rate limit exceeded"}
```
- Exceeded per-project rate limit
- Check `Retry-After` header (seconds until reset)

**502 Bad Gateway**
```json
{"error": "failed to fetch balance: connection refused"}
```
- RPC endpoint unreachable
- Blockchain node down
- Network issue

**503 Service Unavailable**
```json
{"error": "vault sealed"}
```
- Vault needs unsealing
- Redis connection lost
- Database unavailable

**400 Bad Request — insufficient balance**
```json
{"error": "insufficient usdc balance: wallet has 10.00 but transfer requires 50.00"}
```
- Wallet token balance is below the requested transfer amount

**400 Bad Request — token not registered**
```json
{"error": "usdc not registered for evm chain 80001 — seed it via POST /admin/stablecoins"}
```
- Token hasn't been configured for this chain (common on testnets)

### Retry Logic

**Idempotent operations** (safe to retry):
- `GET /wallets/{id}`
- `GET /jobs/{id}`
- `GET /wallets/{id}/balance`
- `POST /wallets/{id}/sign/*` (with `idempotency_key`)
- `POST /wallets/{id}/sweep` (with `idempotency_key`)

**Non-idempotent** (do NOT auto-retry without idempotency key):
- `POST /wallets` (creates new wallet)
- `POST /wallets/{id}/sign/*` (without `idempotency_key`)
- `POST /wallets/{id}/sweep` (without `idempotency_key`)
- `POST /projects/relayer`
- `POST /projects/master-wallet`

**Recommended retry strategy:**
```javascript
async function retryRequest(fn, maxRetries = 3) {
  for (let i = 0; i < maxRetries; i++) {
    try {
      return await fn();
    } catch (err) {
      if (err.response?.status >= 500) {
        // Server error - retry
        await new Promise(r => setTimeout(r, Math.pow(2, i) * 1000));
        continue;
      } else {
        // Client error - don't retry
        throw err;
      }
    }
  }
  throw new Error('Max retries exceeded');
}
```

---

## Best Practices

### Security

1. **Store API credentials securely**
   - Environment variables
   - Secrets manager (AWS Secrets Manager, GCP Secret Manager)
   - Never commit to version control

2. **Verify webhook signatures**
   - Always validate HMAC signature
   - Check timestamp (prevent replay attacks)

3. **Use HTTPS in production**
   - Never send API credentials over HTTP
   - Configure reverse proxy (nginx, Caddy) with SSL

4. **Rotate credentials periodically**
   - Create new project every 90 days
   - Migrate users gradually
   - Deactivate old project

5. **Implement rate limiting client-side**
   - Respect `X-RateLimit-*` headers
   - Implement exponential backoff

### Performance

1. **Use idempotency keys**
   - Prevents duplicate jobs on retry
   - Safe to retry failed requests
   - Format: `{operation}_{user_id}_{timestamp}`

2. **Don't poll aggressively**
   - Wait 1-2 seconds between polls
   - Better: rely on webhooks
   - Poll as fallback only

3. **Batch wallet creation**
   - Create wallets asynchronously in background
   - Don't create on-demand during user signup

4. **Cache balance queries**
   - Balance changes slowly
   - Cache for 30-60 seconds
   - Invalidate on successful transaction

### Sweep

1. **Set a realistic dust threshold**
   - Avoids sweeping wallets with negligible balances
   - Account for gas cost of the sweep transaction itself

2. **Use idempotency keys on sweep triggers**
   - Safe to retry if your service crashes mid-request
   - Format: `sweep_{user_id}_{date}`

3. **Monitor relayer balance**
   - Use `GET /projects/relayer` to check health before triggering sweeps
   - Set `min_balance_alert` conservatively and top up proactively

4. **Avoid Ethereum mainnet**
   - Sweep is only supported on EVM L2s and Solana
   - Gas costs on mainnet make sweeping uneconomical

### Error Handling

1. **Handle all error cases**
   - Network errors
   - Server errors (500-599)
   - Client errors (400-499)
   - Timeout errors

2. **Provide user feedback**
   - Show meaningful error messages
   - Don't expose API keys in error logs
   - Log errors for debugging

3. **Implement fallback UI**
   - "Transaction pending" states
   - "Try again" buttons
   - Support contact information

---