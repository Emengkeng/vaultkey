package storage_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/vaultkey/vaultkey/internal/storage"
)

// startPostgres spins up a real Postgres container and runs the schema
// migrations. Returns a ready-to-use *storage.Store.
func startPostgres(t *testing.T) *storage.Store {
	t.Helper()

	ctx := context.Background()

	container, err := postgres.RunContainer(ctx,
		testcontainers.WithImage("postgres:16-alpine"),
		postgres.WithDatabase("vaultkey_test"),
		postgres.WithUsername("vaultkey"),
		postgres.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() { container.Terminate(ctx) }) //nolint:errcheck

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	store, err := storage.New(connStr)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if err := runMigrations(store); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	return store
}

// runMigrations creates the schema in the test database.
// In production this lives in migrations/001_init.sql.
// We inline it here so the test package has no file-system dependency.
func runMigrations(store *storage.Store) error {
	// Access the underlying sql.DB through an exported helper.
	// If your Store doesn't expose DB(), add:
	//   func (s *Store) DB() *sql.DB { return s.db }
	db := store.DB()

	schema := `
CREATE TABLE IF NOT EXISTS projects (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                TEXT NOT NULL,
    api_key             TEXT NOT NULL UNIQUE,
    api_secret_hash     TEXT NOT NULL,
    webhook_url         TEXT,
    webhook_secret      TEXT,
    rate_limit_rps      INT NOT NULL DEFAULT 100,
    max_retries         INT NOT NULL DEFAULT 3,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS wallets (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id         TEXT NOT NULL,
    chain_type      TEXT NOT NULL CHECK (chain_type IN ('evm', 'solana')),
    address         TEXT NOT NULL,
    encrypted_key   BYTEA NOT NULL,
    encrypted_dek   BYTEA NOT NULL,
    label           TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, user_id, chain_type, label)
);

CREATE TABLE IF NOT EXISTS signing_jobs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id),
    wallet_id       UUID NOT NULL REFERENCES wallets(id),
    idempotency_key TEXT,
    operation       TEXT NOT NULL CHECK (operation IN ('sign_tx_evm','sign_msg_evm','sign_tx_solana','sign_msg_solana')),
    payload         JSONB NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','completed','failed','dead')),
    result          JSONB,
    error           TEXT,
    attempts        INT NOT NULL DEFAULT 0,
    gasless         BOOLEAN NOT NULL DEFAULT false,
    webhook_status  TEXT NOT NULL DEFAULT 'pending' CHECK (webhook_status IN ('pending','delivered','failed','skipped')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, idempotency_key)
);

CREATE TABLE IF NOT EXISTS audit_log (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID NOT NULL REFERENCES projects(id),
    wallet_id   UUID REFERENCES wallets(id),
    job_id      UUID REFERENCES signing_jobs(id),
    operation   TEXT NOT NULL,
    actor       TEXT NOT NULL,
    metadata    JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS relayer_wallets (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id          UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    wallet_id           UUID NOT NULL REFERENCES wallets(id),
    chain_type          TEXT NOT NULL CHECK (chain_type IN ('evm', 'solana')),
    chain_id            TEXT,
    min_balance_alert   TEXT NOT NULL DEFAULT '0.1',
    active              BOOLEAN NOT NULL DEFAULT true,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, chain_type, chain_id)
);
`
	_, err := db.Exec(schema)
	return err
}

// seedProject creates a minimal project and wallet for use in tests.
func seedProject(t *testing.T, store *storage.Store) (*storage.Project, *storage.Wallet) {
	t.Helper()

	ctx := context.Background()
	apiKey := fmt.Sprintf("test-key-%d", time.Now().UnixNano())

	proj, err := store.CreateProject(ctx, "Test Project", apiKey, "hash", nil, nil, 100, 3)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	wlt, err := store.CreateWallet(ctx, &storage.Wallet{
		ProjectID:    proj.ID,
		UserID:       "user-1",
		ChainType:    "evm",
		Address:      "0xTestAddress",
		EncryptedKey: []byte("encrypted-key"),
		EncryptedDEK: []byte("encrypted-dek"),
	})
	if err != nil {
		t.Fatalf("create wallet: %v", err)
	}

	return proj, wlt
}

// TestIdempotencyDeduplication submits two jobs with the same idempotency key.
// The second insert must not create a new row; both calls must return the same job ID.
func TestIdempotencyDeduplication(t *testing.T) {
	store := startPostgres(t)
	proj, wlt := seedProject(t, store)
	ctx := context.Background()

	idKey := "idem-key-abc"
	payload := json.RawMessage(`{"to":"0xDead","value":"0x0","gas_limit":21000,"gas_price":"0x3B9ACA00","chain_id":1}`)

	// First submit
	job1, err := store.CreateSigningJob(ctx, proj.ID, wlt.ID, "sign_tx_evm", payload, &idKey, false)
	if err != nil {
		t.Fatalf("first CreateSigningJob: %v", err)
	}

	// Second submit — same idempotency key
	job2, err := store.CreateSigningJob(ctx, proj.ID, wlt.ID, "sign_tx_evm", payload, &idKey, false)
	if err != nil {
		t.Fatalf("second CreateSigningJob: %v", err)
	}

	// Must return the same job
	if job1.ID != job2.ID {
		t.Errorf("idempotency failed: first job %s, second job %s — duplicate created", job1.ID, job2.ID)
	}

	// Status must still be pending (not reset)
	if job2.Status != "pending" {
		t.Errorf("unexpected status after dedup: %s", job2.Status)
	}

	// Exactly one row must exist for this idempotency key
	db := store.DB()
	var count int
	row := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM signing_jobs WHERE project_id = $1 AND idempotency_key = $2`,
		proj.ID, idKey,
	)
	if err := row.Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row for idempotency key, got %d", count)
	}
}

// TestIdempotencyDifferentKeysCreateSeparateJobs verifies that distinct keys
// produce distinct jobs (not over-aggressive deduplication).
func TestIdempotencyDifferentKeysCreateSeparateJobs(t *testing.T) {
	store := startPostgres(t)
	proj, wlt := seedProject(t, store)
	ctx := context.Background()

	payload := json.RawMessage(`{"to":"0xDead","value":"0x0","gas_limit":21000,"gas_price":"0x3B9ACA00","chain_id":1}`)
	key1 := "key-1"
	key2 := "key-2"

	job1, err := store.CreateSigningJob(ctx, proj.ID, wlt.ID, "sign_tx_evm", payload, &key1, false)
	if err != nil {
		t.Fatalf("job1: %v", err)
	}

	job2, err := store.CreateSigningJob(ctx, proj.ID, wlt.ID, "sign_tx_evm", payload, &key2, false)
	if err != nil {
		t.Fatalf("job2: %v", err)
	}

	if job1.ID == job2.ID {
		t.Error("different idempotency keys should create different jobs, got the same ID")
	}
}

// TestIdempotencyNilKeyAlwaysCreatesNewJob verifies that nil idempotency keys
// are not deduplicated against each other.
func TestIdempotencyNilKeyAlwaysCreatesNewJob(t *testing.T) {
	store := startPostgres(t)
	proj, wlt := seedProject(t, store)
	ctx := context.Background()

	payload := json.RawMessage(`{"to":"0xDead","value":"0x0","gas_limit":21000,"gas_price":"0x3B9ACA00","chain_id":1}`)

	job1, err := store.CreateSigningJob(ctx, proj.ID, wlt.ID, "sign_tx_evm", payload, nil, false)
	if err != nil {
		t.Fatalf("job1: %v", err)
	}

	job2, err := store.CreateSigningJob(ctx, proj.ID, wlt.ID, "sign_tx_evm", payload, nil, false)
	if err != nil {
		t.Fatalf("job2: %v", err)
	}

	if job1.ID == job2.ID {
		t.Error("nil idempotency keys should always create separate jobs")
	}
}