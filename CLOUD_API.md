# VaultKey Cloud API Reference

The Cloud API powers the multi-tenant SaaS dashboard. It handles organizations,
members, invites, and API key management. It is **separate from the core wallet
API** and uses a different authentication mechanism.

> **Availability:** All endpoints below require `ENABLE_CLOUD_FEATURES=true` on
> the server. Requests to `/cloud/*` return `404` in self-hosted mode.

---

## Base URL

```
Development: http://localhost:8080
```

---

## Authentication

Cloud endpoints use **Clerk JWT session tokens**, not API keys.

```http
Authorization: Bearer <clerk_session_token>
```

The token is the short-lived JWT issued by Clerk on your frontend. Your Clerk
frontend SDK (`@clerk/nextjs`, `@clerk/react`, etc.) manages token refresh
automatically — you do not need to handle expiry manually.

**Do not use your Clerk Secret Key here.** That key is for server-to-server
Clerk API calls only.

**Session expiry:** If a request returns `401` with `"invalid or expired session
token"`, the frontend should call `getToken()` on the Clerk session object to
refresh and retry once.

---

## RBAC — Role Levels

Every `/cloud/organizations/{org_id}/*` endpoint enforces a minimum role.
Roles are hierarchical — higher roles inherit all lower-role permissions.

| Role | Level | Typical use |
|------|-------|-------------|
| `owner` | 4 | Org creator. Can delete the org. Exactly one per org. |
| `admin` | 3 | Manages members, invites, and API keys. |
| `developer` | 2 | Lists API keys. Uses the project. |
| `viewer` | 1 | Read-only access to org and member info. |

Constraints enforced at the API layer:
- `owner` cannot be assigned via invite or role-update — only the org creator holds it
- The owner's role cannot be changed
- The last owner cannot be removed from the org

---

## Response Format

### Success

```json
{ "field": "value" }
```

### Error

```json
{ "error": "Human-readable description of what went wrong" }
```

**Common HTTP status codes:**

| Code | Meaning |
|------|---------|
| `200 OK` | Request succeeded |
| `201 Created` | Resource created |
| `204 No Content` | Webhook acknowledged |
| `400 Bad Request` | Validation failure |
| `401 Unauthorized` | Missing or invalid JWT |
| `403 Forbidden` | Valid JWT but insufficient role |
| `404 Not Found` | Resource not found or not a member of org |
| `409 Conflict` | Already exists (invite accepted, already a member) |
| `410 Gone` | Invite expired |
| `500 Internal Server Error` | Server error |

---

## Endpoints

### Onboarding

#### Start Onboarding

Creates an organization for the authenticated user and seeds them as `owner`.
A project is automatically provisioned for the org and is used when creating
API keys.

**Idempotent** — calling this again while already onboarded returns the
existing org (`200 OK`) without creating a duplicate.

```http
POST /cloud/onboarding
```

**Authentication:** Clerk JWT (any authenticated user)

**Request Body:**
```json
{
  "org_name": "Acme Corp",
  "billing_email": "billing@acme.com"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `org_name` | string | Yes | Display name for the organization |
| `billing_email` | string | No | Billing contact email |

**Response:** `201 Created` (new org) or `200 OK` (already onboarded)
```json
{
  "org_id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
  "org_name": "Acme Corp",
  "org_slug": "acme-corp-4f2a1b",
  "project_id": "e5f6g7h8-i9j0-1234-klmn-op5678901234",
  "created_at": "2026-03-11T12:00:00Z"
}
```

**Example:**
```bash
curl -X POST http://localhost:8080/cloud/onboarding \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer eyJhbGci..." \
  -d '{
    "org_name": "Acme Corp",
    "billing_email": "billing@acme.com"
  }'
```

---

### Organizations

#### List Organizations

Returns all organizations the authenticated user is an active member of.

```http
GET /cloud/organizations
```

**Authentication:** Clerk JWT (any authenticated user)

**Response:** `200 OK`
```json
{
  "organizations": [
    {
      "id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
      "name": "Acme Corp",
      "slug": "acme-corp-4f2a1b",
      "billing_email": "billing@acme.com",
      "created_at": "2026-03-11T12:00:00Z"
    }
  ]
}
```

**Example:**
```bash
curl http://localhost:8080/cloud/organizations \
  -H "Authorization: Bearer eyJhbGci..."
```

---

#### Get Organization

Returns full details for a specific org, including the linked project ID.

```http
GET /cloud/organizations/{org_id}
```

**Authentication:** Clerk JWT — minimum role: **viewer**

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| `org_id` | string | Organization ID |

**Response:** `200 OK`
```json
{
  "id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
  "name": "Acme Corp",
  "slug": "acme-corp-4f2a1b",
  "created_by": "user_abc123xyz",
  "billing_email": "billing@acme.com",
  "project_id": "e5f6g7h8-i9j0-1234-klmn-op5678901234",
  "created_at": "2026-03-11T12:00:00Z",
  "updated_at": "2026-03-11T12:00:00Z"
}
```

**Example:**
```bash
curl http://localhost:8080/cloud/organizations/a1b2c3d4-... \
  -H "Authorization: Bearer eyJhbGci..."
```

---

#### Update Organization

Updates the organization's display name and/or billing email.

```http
PATCH /cloud/organizations/{org_id}
```

**Authentication:** Clerk JWT — minimum role: **admin**

**Request Body:**
```json
{
  "name": "Acme Corp (Renamed)",
  "billing_email": "new-billing@acme.com"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | New display name |
| `billing_email` | string | No | New billing contact email |

**Response:** `200 OK` — returns the updated org object (same shape as Get Organization).

**Example:**
```bash
curl -X PATCH http://localhost:8080/cloud/organizations/a1b2c3d4-... \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer eyJhbGci..." \
  -d '{"name": "Acme Corp (Renamed)"}'
```

---

#### Delete Organization

Soft-deletes the organization. All wallets, signing jobs, and audit logs
are retained. Members lose access immediately.

```http
DELETE /cloud/organizations/{org_id}
```

**Authentication:** Clerk JWT — minimum role: **owner**

**Response:** `200 OK`
```json
{
  "status": "deleted",
  "org_id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890"
}
```

**Example:**
```bash
curl -X DELETE http://localhost:8080/cloud/organizations/a1b2c3d4-... \
  -H "Authorization: Bearer eyJhbGci..."
```

---

### Members

#### List Members

Returns all active members of an organization with their roles and Clerk
profile data.

```http
GET /cloud/organizations/{org_id}/members
```

**Authentication:** Clerk JWT — minimum role: **viewer**

**Response:** `200 OK`
```json
{
  "members": [
    {
      "id": "m1n2o3p4-...",
      "clerk_user_id": "user_abc123xyz",
      "role": "owner",
      "email": "alice@acme.com",
      "first_name": "Alice",
      "last_name": "Smith",
      "joined_at": "2026-03-11T12:00:00Z"
    },
    {
      "id": "q5r6s7t8-...",
      "clerk_user_id": "user_def456uvw",
      "role": "developer",
      "email": "bob@acme.com",
      "first_name": "Bob",
      "last_name": "Jones",
      "joined_at": "2026-03-12T09:00:00Z"
    }
  ]
}
```

**Example:**
```bash
curl http://localhost:8080/cloud/organizations/a1b2c3d4-.../members \
  -H "Authorization: Bearer eyJhbGci..."
```

---

#### Update Member Role

Changes a member's role. Cannot assign or modify the `owner` role.

```http
PATCH /cloud/organizations/{org_id}/members/{clerk_user_id}
```

**Authentication:** Clerk JWT — minimum role: **admin**

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| `org_id` | string | Organization ID |
| `clerk_user_id` | string | Target member's Clerk user ID (`user_...`) |

**Request Body:**
```json
{
  "role": "admin"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `role` | string | Yes | `admin`, `developer`, or `viewer` |

**Response:** `200 OK`
```json
{
  "id": "q5r6s7t8-...",
  "clerk_user_id": "user_def456uvw",
  "role": "admin",
  "joined_at": "2026-03-12T09:00:00Z"
}
```

**Constraints:**
- `owner` is not a valid value for `role` — returns `400`
- Attempting to change the owner's role returns `403`

**Example:**
```bash
curl -X PATCH http://localhost:8080/cloud/organizations/a1b2c3d4-.../members/user_def456uvw \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer eyJhbGci..." \
  -d '{"role": "admin"}'
```

---

#### Remove Member

Removes a member from the organization. The member's wallets and signing
history are not affected.

```http
DELETE /cloud/organizations/{org_id}/members/{clerk_user_id}
```

**Authentication:** Clerk JWT — minimum role: **admin**

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| `org_id` | string | Organization ID |
| `clerk_user_id` | string | Target member's Clerk user ID |

**Response:** `200 OK`
```json
{
  "status": "removed",
  "clerk_user_id": "user_def456uvw"
}
```

**Constraints:**
- Cannot remove the last owner — returns `403`

**Example:**
```bash
curl -X DELETE http://localhost:8080/cloud/organizations/a1b2c3d4-.../members/user_def456uvw \
  -H "Authorization: Bearer eyJhbGci..."
```

---

### Invites

Invites are email-based with a **7-day expiry**. The invite token is returned
in the API response — your frontend constructs the accept URL
(e.g. `https://app.yoursite.com/invite/{token}`) and sends it to the invitee.

`owner` cannot be used as an invite role. The org creator is always the owner.

#### Create Invite

```http
POST /cloud/organizations/{org_id}/invites
```

**Authentication:** Clerk JWT — minimum role: **admin**

**Request Body:**
```json
{
  "email": "charlie@acme.com",
  "role": "developer"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `email` | string | Yes | Email address to invite |
| `role` | string | No | `admin`, `developer`, or `viewer` (default: `developer`) |

**Response:** `201 Created`
```json
{
  "id": "i1j2k3l4-...",
  "org_id": "a1b2c3d4-...",
  "email": "charlie@acme.com",
  "token": "a3f8c2e1d4b7...",
  "role": "developer",
  "created_by": "user_abc123xyz",
  "expires_at": "2026-03-18T12:00:00Z",
  "created_at": "2026-03-11T12:00:00Z"
}
```

**Example:**
```bash
curl -X POST http://localhost:8080/cloud/organizations/a1b2c3d4-.../invites \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer eyJhbGci..." \
  -d '{
    "email": "charlie@acme.com",
    "role": "developer"
  }'
```

---

#### List Invites

Returns all pending, accepted, and revoked invites for the organization.

```http
GET /cloud/organizations/{org_id}/invites
```

**Authentication:** Clerk JWT — minimum role: **viewer**

**Response:** `200 OK`
```json
{
  "invites": [
    {
      "id": "i1j2k3l4-...",
      "org_id": "a1b2c3d4-...",
      "email": "charlie@acme.com",
      "token": "a3f8c2e1d4b7...",
      "role": "developer",
      "created_by": "user_abc123xyz",
      "expires_at": "2026-03-18T12:00:00Z",
      "created_at": "2026-03-11T12:00:00Z",
      "accepted_at": null
    }
  ]
}
```

**Example:**
```bash
curl http://localhost:8080/cloud/organizations/a1b2c3d4-.../invites \
  -H "Authorization: Bearer eyJhbGci..."
```

---

#### Accept Invite

Accepts an invite and adds the authenticated user to the organization.
The caller must be logged in via Clerk — they do not need to be an existing
member of the org.

```http
POST /cloud/invites/{token}/accept
```

**Authentication:** Clerk JWT (any authenticated user)

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| `token` | string | Invite token from the invite creation response |

**Response:** `200 OK`
```json
{
  "status": "accepted",
  "org_id": "a1b2c3d4-...",
  "role": "developer"
}
```

**Error cases:**

| Status | Error | Cause |
|--------|-------|-------|
| `404` | `invite not found or already revoked` | Token doesn't exist or was deleted |
| `409` | `invite has already been accepted` | Token was already used |
| `410` | `invite has expired` | More than 7 days since creation |
| `409` | `you are already a member of this organization` | User already belongs to the org |

**Example:**
```bash
curl -X POST http://localhost:8080/cloud/invites/a3f8c2e1d4b7.../accept \
  -H "Authorization: Bearer eyJhbGci..."
```

---

#### Revoke Invite

Soft-deletes a pending invite. Has no effect on already-accepted invites.

```http
DELETE /cloud/organizations/{org_id}/invites/{token}
```

**Authentication:** Clerk JWT — minimum role: **admin**

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| `org_id` | string | Organization ID |
| `token` | string | Invite token |

**Response:** `200 OK`
```json
{
  "status": "revoked"
}
```

**Example:**
```bash
curl -X DELETE http://localhost:8080/cloud/organizations/a1b2c3d4-.../invites/a3f8c2e1d4b7... \
  -H "Authorization: Bearer eyJhbGci..."
```

---

### API Keys

API keys are scoped to the organization's project and are used to authenticate
requests to the core wallet API (`X-API-Key` / `X-API-Secret` headers).

Multiple keys per org are supported — create separate keys for production,
staging, CI, etc. and rotate them independently.

**The secret is shown exactly once** at creation time. Store it immediately in
your secrets manager. If lost, revoke and create a new key.

#### Create API Key

```http
POST /cloud/organizations/{org_id}/api-keys
```

**Authentication:** Clerk JWT — minimum role: **admin**

**Request Body:**
```json
{
  "name": "Production"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | No | Label for this key (default: `Key YYYY-MM-DD`) |

**Response:** `201 Created`
```json
{
  "id": "k1l2m3n4-...",
  "name": "Production",
  "key": "a7f3c9e2d8b1...",
  "secret": "4e8f2a6c0d9b...",
  "created_at": "2026-03-11T12:00:00Z"
}
```

> ⚠️ **`secret` is shown once and never stored in plaintext. Save it immediately.**

Use `key` as `X-API-Key` and `secret` as `X-API-Secret` in core API requests.

**Example:**
```bash
curl -X POST http://localhost:8080/cloud/organizations/a1b2c3d4-.../api-keys \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer eyJhbGci..." \
  -d '{"name": "Production"}'
```

---

#### List API Keys

Returns all active API keys for the org's project. Secrets are never returned.

```http
GET /cloud/organizations/{org_id}/api-keys
```

**Authentication:** Clerk JWT — minimum role: **developer**

**Response:** `200 OK`
```json
{
  "api_keys": [
    {
      "id": "k1l2m3n4-...",
      "name": "Production",
      "key": "a7f3c9e2d8b1...",
      "active": true,
      "last_used_at": "2026-03-11T14:32:10Z",
      "created_at": "2026-03-11T12:00:00Z"
    },
    {
      "id": "o5p6q7r8-...",
      "name": "Staging",
      "key": "b2g4d1e7c5f0...",
      "active": true,
      "last_used_at": null,
      "created_at": "2026-03-11T13:00:00Z"
    }
  ]
}
```

**Example:**
```bash
curl http://localhost:8080/cloud/organizations/a1b2c3d4-.../api-keys \
  -H "Authorization: Bearer eyJhbGci..."
```

---

#### Revoke API Key

Permanently deactivates an API key. Any requests using this key will
immediately begin returning `401`. This cannot be undone — create a new key
if needed.

```http
DELETE /cloud/organizations/{org_id}/api-keys/{key_id}
```

**Authentication:** Clerk JWT — minimum role: **admin**

**Path Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| `org_id` | string | Organization ID |
| `key_id` | string | API key ID from list/create response |

**Response:** `200 OK`
```json
{
  "status": "revoked",
  "id": "k1l2m3n4-..."
}
```

**Example:**
```bash
curl -X DELETE http://localhost:8080/cloud/organizations/a1b2c3d4-.../api-keys/k1l2m3n4-... \
  -H "Authorization: Bearer eyJhbGci..."
```

---

### Clerk Webhooks

VaultKey listens for Clerk user lifecycle events to keep its internal user
cache in sync. This endpoint is **public** — it does not require a JWT.
Payloads are verified using the Svix HMAC signature before processing.

#### Receive Clerk Event

```http
POST /webhooks/clerk
```

**Authentication:** None (verified via Svix signature headers)

**Required headers (set automatically by Svix):**
```http
svix-id: msg_abc123
svix-timestamp: 1678896000
svix-signature: v1,base64signature...
```

**Handled events:**

| Event | Action |
|-------|--------|
| `user.created` | Upserts a row in `clerk_users` |
| `user.updated` | Updates email, name, and avatar in `clerk_users` |
| `user.deleted` | Soft-deletes all org memberships for that user |

All other event types are acknowledged with `204` and ignored.

**Response:** `204 No Content` on success. `401` if signature verification fails.

**Setup in Clerk Dashboard:**
1. Go to **Webhooks** → **Add Endpoint**
2. URL: `https://yourdomain.com/webhooks/clerk`
3. Select events: `user.created`, `user.updated`, `user.deleted`
4. Copy the **Signing Secret** → set as `CLERK_WEBHOOK_SECRET` env var

> Clerk webhooks are asynchronous and delivered via Svix with automatic retries.
> Do not rely on immediate delivery for synchronous user flows.

---

## Error Reference

| Status | Error message | Cause |
|--------|---------------|-------|
| `401` | `Authorization: Bearer <token> header required` | No auth header |
| `401` | `invalid or expired session token` | JWT invalid or expired — refresh and retry |
| `403` | `not a member of this organization` | Caller is not in this org |
| `403` | `insufficient permissions: admin role required` | Caller's role is below the required minimum |
| `403` | `cannot change the owner's role` | Attempted to update the owner's role |
| `403` | `cannot remove the last owner — transfer ownership first` | Last owner removal blocked |
| `400` | `org_name is required` | Missing field on onboarding |
| `400` | `role must be one of: admin, developer, viewer` | Invalid role value (including `owner`) |
| `400` | `valid email is required` | Malformed email on invite creation |
| `400` | `invalid clerk_user_id format` | Path param doesn't match `user_[a-zA-Z0-9]+` |
| `404` | `invite not found or already revoked` | Token unknown or soft-deleted |
| `404` | `api key not found` | Key ID not in this project or already revoked |
| `409` | `invite has already been accepted` | Token reuse attempt |
| `409` | `you are already a member of this organization` | Accept invite while already a member |
| `410` | `invite has expired` | More than 7 days since invite creation |

---

## Code Examples

### Full Onboarding Flow (TypeScript)

```typescript
const BASE = 'http://localhost:8080';

async function getToken(): Promise<string> {
  // Use your Clerk frontend SDK — e.g. @clerk/nextjs
  // const { getToken } = useAuth();
  // return getToken();
  return 'eyJhbGci...'; // placeholder
}

async function cloudRequest(method: string, path: string, body?: object) {
  const token = await getToken();
  const res = await fetch(`${BASE}${path}`, {
    method,
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`,
    },
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) {
    const err = await res.json();
    throw new Error(err.error);
  }
  return res.json();
}

// 1. Onboard after Clerk sign-up
const org = await cloudRequest('POST', '/cloud/onboarding', {
  org_name: 'Acme Corp',
  billing_email: 'billing@acme.com',
});
console.log('Org ID:', org.org_id);
console.log('Project ID:', org.project_id);

// 2. Create a production API key
const key = await cloudRequest('POST', `/cloud/organizations/${org.org_id}/api-keys`, {
  name: 'Production',
});
console.log('API Key:', key.key);
console.log('API Secret:', key.secret); // save this — shown once only

// 3. Invite a teammate
const invite = await cloudRequest('POST', `/cloud/organizations/${org.org_id}/invites`, {
  email: 'bob@acme.com',
  role: 'developer',
});
// Send invite URL to bob — your frontend constructs this:
console.log('Invite URL:', `https://app.yoursite.com/invite/${invite.token}`);
```

---

### Accept Invite Flow (TypeScript)

```typescript
// On your /invite/[token] page — user is already signed in via Clerk

async function acceptInvite(token: string) {
  try {
    const result = await cloudRequest('POST', `/cloud/invites/${token}/accept`);
    console.log(`Joined org ${result.org_id} as ${result.role}`);
    // Redirect to dashboard
  } catch (err: any) {
    if (err.message.includes('already been accepted')) {
      // Show "this invite has already been used"
    } else if (err.message.includes('expired')) {
      // Show "this invite has expired — ask for a new one"
    } else if (err.message.includes('already a member')) {
      // Redirect to dashboard — they're already in
    } else {
      throw err;
    }
  }
}
```

---

### Key Rotation (TypeScript)

```typescript
async function rotateAPIKey(orgId: string, oldKeyId: string) {
  // 1. Create the new key first
  const newKey = await cloudRequest('POST', `/cloud/organizations/${orgId}/api-keys`, {
    name: `Production ${new Date().toISOString().slice(0, 10)}`,
  });
  console.log('New key created:', newKey.key);
  console.log('New secret (save now):', newKey.secret);

  // 2. Update your secrets manager / environment with newKey.key + newKey.secret

  // 3. Revoke the old key after confirming the new one works
  await cloudRequest('DELETE', `/cloud/organizations/${orgId}/api-keys/${oldKeyId}`);
  console.log('Old key revoked');
}
```