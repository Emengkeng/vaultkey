package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// StablecoinToken is one row in the stablecoin_tokens table.
type StablecoinToken struct {
	ID        string
	Token     string // normalised lowercase: "usdc", "usdt"
	Symbol    string // display: "USDC", "USDT"
	ChainType string // "evm" or "solana"
	ChainID   string // EVM chain ID; empty string for Solana
	Contract  string // EVM contract address or Solana mint address
	Decimals  int
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// GetStablecoinToken returns the token entry for a given token+chain.
// Returns nil if not found or disabled.
func (s *Store) GetStablecoinToken(ctx context.Context, token, chainType, chainID string) (*StablecoinToken, error) {
	t := &StablecoinToken{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id, token, symbol, chain_type, COALESCE(chain_id,''), contract, decimals, enabled, created_at, updated_at
		 FROM stablecoin_tokens
		 WHERE token = $1
		   AND chain_type = $2
		   AND (chain_id = $3 OR (chain_id IS NULL AND $3 = ''))
		   AND enabled = true
		 LIMIT 1`,
		strings.ToLower(token), chainType, chainID,
	).Scan(
		&t.ID, &t.Token, &t.Symbol, &t.ChainType, &t.ChainID,
		&t.Contract, &t.Decimals, &t.Enabled, &t.CreatedAt, &t.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get stablecoin token: %w", err)
	}
	return t, nil
}

// ListStablecoinTokens returns all enabled token entries, optionally filtered
// by chain_type. Pass empty string for chainType to return all.
func (s *Store) ListStablecoinTokens(ctx context.Context, chainType string) ([]*StablecoinToken, error) {
	query := `SELECT id, token, symbol, chain_type, COALESCE(chain_id,''), contract, decimals, enabled, created_at, updated_at
	          FROM stablecoin_tokens
	          WHERE enabled = true`
	args := []any{}

	if chainType != "" {
		query += " AND chain_type = $1"
		args = append(args, chainType)
	}

	query += " ORDER BY token, chain_type, chain_id"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list stablecoin tokens: %w", err)
	}
	defer rows.Close()

	var tokens []*StablecoinToken
	for rows.Next() {
		t := &StablecoinToken{}
		if err := rows.Scan(
			&t.ID, &t.Token, &t.Symbol, &t.ChainType, &t.ChainID,
			&t.Contract, &t.Decimals, &t.Enabled, &t.CreatedAt, &t.UpdatedAt,
		); err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

// UpsertStablecoinToken inserts or updates a token entry.
// On conflict (token + chain_type + chain_id) it updates contract, decimals,
// symbol, and re-enables the row if it was disabled.
func (s *Store) UpsertStablecoinToken(ctx context.Context, t *StablecoinToken) (*StablecoinToken, error) {
	result := &StablecoinToken{}

	// Normalise: token always lowercase, chain_id empty string → NULL in DB.
	token := strings.ToLower(strings.TrimSpace(t.Token))
	chainID := strings.TrimSpace(t.ChainID)

	err := s.db.QueryRowContext(ctx,
		`INSERT INTO stablecoin_tokens (token, symbol, chain_type, chain_id, contract, decimals, enabled)
		 VALUES ($1, $2, $3, NULLIF($4,''), $5, $6, true)
		 ON CONFLICT (token, chain_type, chain_id) DO UPDATE
		   SET symbol    = EXCLUDED.symbol,
		       contract  = EXCLUDED.contract,
		       decimals  = EXCLUDED.decimals,
		       enabled   = true,
		       updated_at = now()
		 RETURNING id, token, symbol, chain_type, COALESCE(chain_id,''), contract, decimals, enabled, created_at, updated_at`,
		token, t.Symbol, t.ChainType, chainID, t.Contract, t.Decimals,
	).Scan(
		&result.ID, &result.Token, &result.Symbol, &result.ChainType, &result.ChainID,
		&result.Contract, &result.Decimals, &result.Enabled, &result.CreatedAt, &result.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("upsert stablecoin token: %w", err)
	}
	return result, nil
}

// DisableStablecoinToken soft-deletes a token entry by setting enabled=false.
// The row is kept for audit purposes. Returns error if not found.
func (s *Store) DisableStablecoinToken(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE stablecoin_tokens SET enabled = false, updated_at = now() WHERE id = $1`,
		id,
	)
	if err != nil {
		return fmt.Errorf("disable stablecoin token: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("stablecoin token %s not found", id)
	}
	return nil
}