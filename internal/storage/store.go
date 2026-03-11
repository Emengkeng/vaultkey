package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "github.com/lib/pq"
)

type Store struct {
	db *sql.DB
}

func New(databaseURL string) (*Store, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	db.SetMaxOpenConns(50)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// ── Project ───────────────────────────────────────────────────────────────────

type Project struct {
	ID            string
	Name          string
	APIKey        string
	APISecretHash string
	WebhookURL    *string
	WebhookSecret *string
	RateLimitRPS  int
	MaxRetries    int
	CreatedAt     time.Time
}

func (s *Store) CreateProject(ctx context.Context, name, apiKey, apiSecretHash string, webhookURL, webhookSecret *string, rateLimitRPS, maxRetries int) (*Project, error) {
	p := &Project{}
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO projects (name, api_key, api_secret_hash, webhook_url, webhook_secret, rate_limit_rps, max_retries)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id, name, api_key, api_secret_hash, webhook_url, webhook_secret, rate_limit_rps, max_retries, created_at`,
		name, apiKey, apiSecretHash, webhookURL, webhookSecret, rateLimitRPS, maxRetries,
	).Scan(&p.ID, &p.Name, &p.APIKey, &p.APISecretHash, &p.WebhookURL, &p.WebhookSecret, &p.RateLimitRPS, &p.MaxRetries, &p.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("create project: %w", err)
	}
	return p, nil
}

func (s *Store) GetProjectByAPIKey(ctx context.Context, apiKey string) (*Project, error) {
	p := &Project{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, api_key, api_secret_hash, webhook_url, webhook_secret, rate_limit_rps, max_retries, created_at
		 FROM projects WHERE api_key = $1`,
		apiKey,
	).Scan(&p.ID, &p.Name, &p.APIKey, &p.APISecretHash, &p.WebhookURL, &p.WebhookSecret, &p.RateLimitRPS, &p.MaxRetries, &p.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	return p, nil
}

func (s *Store) GetProjectByID(ctx context.Context, projectID string) (*Project, error) {
	p := &Project{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, api_key, api_secret_hash, webhook_url, webhook_secret, rate_limit_rps, max_retries, created_at
		 FROM projects WHERE id = $1`,
		projectID,
	).Scan(&p.ID, &p.Name, &p.APIKey, &p.APISecretHash, &p.WebhookURL, &p.WebhookSecret, &p.RateLimitRPS, &p.MaxRetries, &p.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get project by id: %w", err)
	}
	return p, nil
}

func (s *Store) UpdateProjectWebhook(ctx context.Context, projectID string, webhookURL, webhookSecret *string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE projects SET webhook_url = $1, webhook_secret = $2 WHERE id = $3`,
		webhookURL, webhookSecret, projectID,
	)
	return err
}

// ── Wallet ────────────────────────────────────────────────────────────────────

type Wallet struct {
	ID           string
	ProjectID    string
	UserID       string
	ChainType    string
	Address      string
	EncryptedKey []byte
	EncryptedDEK []byte
	Label        *string
	CreatedAt    time.Time
}

func (s *Store) CreateWallet(ctx context.Context, w *Wallet) (*Wallet, error) {
	result := &Wallet{}
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO wallets (project_id, user_id, chain_type, address, encrypted_key, encrypted_dek, label)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id, project_id, user_id, chain_type, address, encrypted_key, encrypted_dek, label, created_at`,
		w.ProjectID, w.UserID, w.ChainType, w.Address, w.EncryptedKey, w.EncryptedDEK, w.Label,
	).Scan(&result.ID, &result.ProjectID, &result.UserID, &result.ChainType,
		&result.Address, &result.EncryptedKey, &result.EncryptedDEK, &result.Label, &result.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("create wallet: %w", err)
	}
	return result, nil
}

func (s *Store) GetWalletByID(ctx context.Context, walletID, projectID string) (*Wallet, error) {
	w := &Wallet{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id, project_id, user_id, chain_type, address, encrypted_key, encrypted_dek, label, created_at
		 FROM wallets WHERE id = $1 AND project_id = $2`,
		walletID, projectID,
	).Scan(&w.ID, &w.ProjectID, &w.UserID, &w.ChainType,
		&w.Address, &w.EncryptedKey, &w.EncryptedDEK, &w.Label, &w.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get wallet: %w", err)
	}
	return w, nil
}

func (s *Store) ListWalletsByUserID(ctx context.Context, projectID, userID string) ([]*Wallet, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, project_id, user_id, chain_type, address, label, created_at
		 FROM wallets WHERE project_id = $1 AND user_id = $2 ORDER BY created_at ASC`,
		projectID, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list wallets: %w", err)
	}
	defer rows.Close()
	var wallets []*Wallet
	for rows.Next() {
		w := &Wallet{}
		if err := rows.Scan(&w.ID, &w.ProjectID, &w.UserID, &w.ChainType, &w.Address, &w.Label, &w.CreatedAt); err != nil {
			return nil, err
		}
		wallets = append(wallets, w)
	}
	return wallets, rows.Err()
}

// ── Signing Jobs ──────────────────────────────────────────────────────────────

type SigningJob struct {
	ID             string
	ProjectID      string
	WalletID       string
	IdempotencyKey *string
	Operation      string
	Payload        json.RawMessage
	Status         string
	Result         json.RawMessage
	Error          *string
	Attempts       int
	Gasless        bool
	WebhookStatus  string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (s *Store) CreateSigningJob(ctx context.Context, projectID, walletID, operation string, payload json.RawMessage, idempotencyKey *string, gasless bool) (*SigningJob, error) {
	job := &SigningJob{}
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO signing_jobs (project_id, wallet_id, operation, payload, idempotency_key, gasless)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (project_id, idempotency_key) DO UPDATE SET updated_at = now()
		 RETURNING id, project_id, wallet_id, idempotency_key, operation, payload, status, result, error, attempts, gasless, webhook_status, created_at, updated_at`,
		projectID, walletID, operation, payload, idempotencyKey, gasless,
	).Scan(&job.ID, &job.ProjectID, &job.WalletID, &job.IdempotencyKey,
		&job.Operation, &job.Payload, &job.Status, &job.Result,
		&job.Error, &job.Attempts, &job.Gasless, &job.WebhookStatus, &job.CreatedAt, &job.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("create signing job: %w", err)
	}
	return job, nil
}

func (s *Store) GetSigningJob(ctx context.Context, jobID, projectID string) (*SigningJob, error) {
	job := &SigningJob{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id, project_id, wallet_id, idempotency_key, operation, payload, status, result, error, attempts, gasless, webhook_status, created_at, updated_at
		 FROM signing_jobs WHERE id = $1 AND project_id = $2`,
		jobID, projectID,
	).Scan(&job.ID, &job.ProjectID, &job.WalletID, &job.IdempotencyKey,
		&job.Operation, &job.Payload, &job.Status, &job.Result,
		&job.Error, &job.Attempts, &job.Gasless, &job.WebhookStatus, &job.CreatedAt, &job.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get signing job: %w", err)
	}
	return job, nil
}

func (s *Store) MarkJobProcessing(ctx context.Context, jobID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE signing_jobs SET status = 'processing', attempts = attempts + 1, updated_at = now() WHERE id = $1`, jobID)
	return err
}

func (s *Store) MarkJobCompleted(ctx context.Context, jobID string, result json.RawMessage) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE signing_jobs SET status = 'completed', result = $1, updated_at = now() WHERE id = $2`, result, jobID)
	return err
}

func (s *Store) MarkJobFailed(ctx context.Context, jobID, errMsg string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE signing_jobs SET status = 'failed', error = $1, updated_at = now() WHERE id = $2`, errMsg, jobID)
	return err
}

func (s *Store) MarkJobDead(ctx context.Context, jobID, errMsg string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE signing_jobs SET status = 'dead', error = $1, updated_at = now() WHERE id = $2`, errMsg, jobID)
	return err
}

func (s *Store) MarkWebhookDelivered(ctx context.Context, jobID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE signing_jobs SET webhook_status = 'delivered', updated_at = now() WHERE id = $1`, jobID)
	return err
}

func (s *Store) MarkWebhookFailed(ctx context.Context, jobID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE signing_jobs SET webhook_status = 'failed', updated_at = now() WHERE id = $1`, jobID)
	return err
}

// ── Audit Log ─────────────────────────────────────────────────────────────────

func (s *Store) WriteAuditLog(ctx context.Context, projectID string, walletID, jobID *string, operation, actor string, metadata map[string]any) error {
	var metaJSON []byte
	if metadata != nil {
		b, err := json.Marshal(metadata)
		if err != nil {
			return err
		}
		metaJSON = b
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_log (project_id, wallet_id, job_id, operation, actor, metadata) VALUES ($1, $2, $3, $4, $5, $6)`,
		projectID, walletID, jobID, operation, actor, metaJSON,
	)
	return err
}

// ── Relayer Wallets ───────────────────────────────────────────────────────────

type RelayerWallet struct {
	ID                string
	ProjectID         string
	WalletID          string
	Address           string // denormalised from wallets table for convenience
	ChainType         string
	ChainID           string
	MinBalanceAlert   string
	Active            bool
	CreatedAt         time.Time
}

func (s *Store) CreateRelayerWallet(ctx context.Context, projectID, walletID, address, chainType, chainID, minBalanceAlert string) (*RelayerWallet, error) {
	rw := &RelayerWallet{}
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO relayer_wallets (project_id, wallet_id, chain_type, chain_id, min_balance_alert)
		 VALUES ($1, $2, $3, NULLIF($4,''), $5)
		 ON CONFLICT (project_id, chain_type, chain_id) DO UPDATE
		   SET wallet_id = EXCLUDED.wallet_id, active = true, min_balance_alert = EXCLUDED.min_balance_alert
		 RETURNING id, project_id, wallet_id, chain_type, COALESCE(chain_id,''), min_balance_alert, active, created_at`,
		projectID, walletID, chainType, chainID, minBalanceAlert,
	).Scan(&rw.ID, &rw.ProjectID, &rw.WalletID, &rw.ChainType, &rw.ChainID, &rw.MinBalanceAlert, &rw.Active, &rw.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("create relayer wallet: %w", err)
	}
	rw.Address = address
	return rw, nil
}

func (s *Store) GetRelayerWallet(ctx context.Context, projectID, chainType, chainID string) (*RelayerWallet, error) {
	rw := &RelayerWallet{}
	// Match on chain_id or fall back to any relayer for the chain type
	err := s.db.QueryRowContext(ctx,
		`SELECT rw.id, rw.project_id, rw.wallet_id, w.address, rw.chain_type,
		        COALESCE(rw.chain_id,''), rw.min_balance_alert, rw.active, rw.created_at
		 FROM relayer_wallets rw
		 JOIN wallets w ON w.id = rw.wallet_id
		 WHERE rw.project_id = $1
		   AND rw.chain_type = $2
		   AND (rw.chain_id = $3 OR rw.chain_id IS NULL)
		   AND rw.active = true
		 ORDER BY rw.chain_id NULLS LAST
		 LIMIT 1`,
		projectID, chainType, chainID,
	).Scan(&rw.ID, &rw.ProjectID, &rw.WalletID, &rw.Address, &rw.ChainType,
		&rw.ChainID, &rw.MinBalanceAlert, &rw.Active, &rw.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get relayer wallet: %w", err)
	}
	return rw, nil
}

func (s *Store) ListRelayerWallets(ctx context.Context, projectID string) ([]*RelayerWallet, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT rw.id, rw.project_id, rw.wallet_id, w.address, rw.chain_type,
		        COALESCE(rw.chain_id,''), rw.min_balance_alert, rw.active, rw.created_at
		 FROM relayer_wallets rw
		 JOIN wallets w ON w.id = rw.wallet_id
		 WHERE rw.project_id = $1
		 ORDER BY rw.chain_type, rw.chain_id`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("list relayer wallets: %w", err)
	}
	defer rows.Close()
	var relayers []*RelayerWallet
	for rows.Next() {
		rw := &RelayerWallet{}
		if err := rows.Scan(&rw.ID, &rw.ProjectID, &rw.WalletID, &rw.Address,
			&rw.ChainType, &rw.ChainID, &rw.MinBalanceAlert, &rw.Active, &rw.CreatedAt); err != nil {
			return nil, err
		}
		relayers = append(relayers, rw)
	}
	return relayers, rows.Err()
}

func (s *Store) DeactivateRelayerWallet(ctx context.Context, projectID, relayerID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE relayer_wallets SET active = false WHERE id = $1 AND project_id = $2`,
		relayerID, projectID,
	)
	return err
}
