package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ── Sweep Config ──────────────────────────────────────────────────────────────

type SweepConfig struct {
	ID             string
	ProjectID      string
	ChainType      string
	ChainID        string
	MasterWalletID string
	DustThreshold  string
	Enabled        bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// CreateMasterWallet stores a newly generated wallet as the master wallet
// for a project+chain and upserts the sweep config.
//
// The wallet must already exist in the wallets table — the sweep service
// generates it via wallet.Service before calling this. This function only
// marks it as master and wires up the config.
//
// If a master wallet already exists for this project+chain, the config is
// updated to point to the new wallet and the old wallet's is_master flag
// is cleared. The old wallet's funds are unaffected — the developer is
// responsible for draining it.
func (s *Store) CreateMasterWallet(ctx context.Context, projectID, walletID, chainType, chainID, dustThreshold string) (*SweepConfig, error) {
	threshold := dustThreshold
	if threshold == "" {
		threshold = "0"
	}

	// If there is an existing master wallet for this chain, unmark it
	// so is_master stays accurate and only one wallet holds the flag per chain.
	var oldMasterWalletID string
	_ = s.db.QueryRowContext(ctx,
		`SELECT master_wallet_id FROM project_sweep_config
		 WHERE project_id = $1 AND chain_type = $2
		   AND (chain_id = $3 OR (chain_id IS NULL AND $3 = ''))`,
		projectID, chainType, chainID,
	).Scan(&oldMasterWalletID)

	if oldMasterWalletID != "" && oldMasterWalletID != walletID {
		_, _ = s.db.ExecContext(ctx,
			`UPDATE wallets SET is_master = false WHERE id = $1`,
			oldMasterWalletID,
		)
	}

	// Mark the new wallet as master
	_, err := s.db.ExecContext(ctx,
		`UPDATE wallets SET is_master = true WHERE id = $1`,
		walletID,
	)
	if err != nil {
		return nil, fmt.Errorf("mark wallet as master: %w", err)
	}

	cfg := &SweepConfig{}
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO project_sweep_config
			(project_id, chain_type, chain_id, master_wallet_id, dust_threshold)
		 VALUES ($1, $2, NULLIF($3, ''), $4, $5)
		 ON CONFLICT (project_id, chain_type, chain_id) DO UPDATE
		   SET master_wallet_id = EXCLUDED.master_wallet_id,
		       dust_threshold   = EXCLUDED.dust_threshold,
		       enabled          = true,
		       updated_at       = now()
		 RETURNING id, project_id, chain_type, COALESCE(chain_id,''), master_wallet_id,
		           dust_threshold, enabled, created_at, updated_at`,
		projectID, chainType, chainID, walletID, threshold,
	).Scan(
		&cfg.ID, &cfg.ProjectID, &cfg.ChainType, &cfg.ChainID,
		&cfg.MasterWalletID, &cfg.DustThreshold, &cfg.Enabled,
		&cfg.CreatedAt, &cfg.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("upsert sweep config: %w", err)
	}

	return cfg, nil
}

// GetSweepConfig returns the sweep config for a project+chain.
// Returns nil if not configured.
func (s *Store) GetSweepConfig(ctx context.Context, projectID, chainType, chainID string) (*SweepConfig, error) {
	cfg := &SweepConfig{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id, project_id, chain_type, COALESCE(chain_id,''), master_wallet_id,
		        dust_threshold, enabled, created_at, updated_at
		 FROM project_sweep_config
		 WHERE project_id = $1
		   AND chain_type = $2
		   AND (chain_id = $3 OR (chain_id IS NULL AND $3 = ''))
		 LIMIT 1`,
		projectID, chainType, chainID,
	).Scan(
		&cfg.ID, &cfg.ProjectID, &cfg.ChainType, &cfg.ChainID,
		&cfg.MasterWalletID, &cfg.DustThreshold, &cfg.Enabled,
		&cfg.CreatedAt, &cfg.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get sweep config: %w", err)
	}
	return cfg, nil
}

// GetSweepConfigByID returns a sweep config by its primary key.
func (s *Store) GetSweepConfigByID(ctx context.Context, configID string) (*SweepConfig, error) {
	cfg := &SweepConfig{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id, project_id, chain_type, COALESCE(chain_id,''), master_wallet_id,
		        dust_threshold, enabled, created_at, updated_at
		 FROM project_sweep_config
		 WHERE id = $1`,
		configID,
	).Scan(
		&cfg.ID, &cfg.ProjectID, &cfg.ChainType, &cfg.ChainID,
		&cfg.MasterWalletID, &cfg.DustThreshold, &cfg.Enabled,
		&cfg.CreatedAt, &cfg.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get sweep config by id: %w", err)
	}
	return cfg, nil
}

// ListSweepConfigs returns all sweep configs for a project.
func (s *Store) ListSweepConfigs(ctx context.Context, projectID string) ([]*SweepConfig, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, project_id, chain_type, COALESCE(chain_id,''), master_wallet_id,
		        dust_threshold, enabled, created_at, updated_at
		 FROM project_sweep_config
		 WHERE project_id = $1
		 ORDER BY chain_type, chain_id`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("list sweep configs: %w", err)
	}
	defer rows.Close()

	var configs []*SweepConfig
	for rows.Next() {
		cfg := &SweepConfig{}
		if err := rows.Scan(
			&cfg.ID, &cfg.ProjectID, &cfg.ChainType, &cfg.ChainID,
			&cfg.MasterWalletID, &cfg.DustThreshold, &cfg.Enabled,
			&cfg.CreatedAt, &cfg.UpdatedAt,
		); err != nil {
			return nil, err
		}
		configs = append(configs, cfg)
	}
	return configs, rows.Err()
}

// UpdateSweepConfig updates dust threshold and enabled flag.
func (s *Store) UpdateSweepConfig(ctx context.Context, projectID, configID string, dustThreshold string, enabled bool) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE project_sweep_config
		 SET dust_threshold = $1, enabled = $2, updated_at = now()
		 WHERE id = $3 AND project_id = $4`,
		dustThreshold, enabled, configID, projectID,
	)
	if err != nil {
		return fmt.Errorf("update sweep config: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("sweep config not found")
	}
	return nil
}

// MarkWalletSwept updates the swept_at timestamp on a wallet after a successful sweep.
func (s *Store) MarkWalletSwept(ctx context.Context, walletID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE wallets SET swept_at = now() WHERE id = $1`,
		walletID,
	)
	return err
}

// GetMasterWalletForChain returns the master wallet for a project+chain.
// Convenience wrapper used by the sweep handler and worker.
func (s *Store) GetMasterWalletForChain(ctx context.Context, projectID, chainType, chainID string) (*Wallet, error) {
	cfg, err := s.GetSweepConfig(ctx, projectID, chainType, chainID)
	if err != nil {
		return nil, err
	}
	if cfg == nil || !cfg.Enabled {
		return nil, nil
	}
	return s.GetWalletByID(ctx, cfg.MasterWalletID, projectID)
}