package stablecoin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/vaultkey/vaultkey/internal/storage"
)

const (
	// cacheTTL controls how long a registry lookup lives in Redis.
	// Token contracts are immutable in practice. 10 minutes is conservative.
	// Cache is invalidated immediately on any write via the admin API.
	cacheTTL = 10 * time.Minute

	cacheKeyPrefix = "vaultkey:stablecoin:"
)

// Token is a stablecoin identifier (normalised lowercase).
type Token string

const (
	USDC Token = "usdc"
	USDT Token = "usdt"
)

// ChainEntry holds contract details for a token on a specific EVM chain.
type ChainEntry struct {
	ID              string
	ContractAddress string
	Decimals        int
	Symbol          string
}

// SolanaEntry holds SPL mint details for a token on Solana.
type SolanaEntry struct {
	ID          string
	MintAddress string
	Decimals    int
	Symbol      string
}

// TokenInfo is the public shape returned by the discovery endpoint.
type TokenInfo struct {
	ID       string `json:"id"`
	Token    string `json:"token"`
	Symbol   string `json:"symbol"`
	Chain    string `json:"chain"`
	ChainID  string `json:"chain_id,omitempty"`
	Contract string `json:"contract"`
	Decimals int    `json:"decimals"`
}

// Registry is a DB-backed, Redis-cached token registry.
//
// Read path:  Redis → Postgres. Cache miss populates Redis with cacheTTL.
// Write path: Postgres → invalidate Redis. Next read re-populates.
//
// Pass a nil redis.Client to disable caching (useful in tests).
type Registry struct {
	store *storage.Store
	cache *redis.Client
}

func NewRegistry(store *storage.Store, cache *redis.Client) *Registry {
	return &Registry{store: store, cache: cache}
}

// ResolveEVM returns the chain entry for a token on a given EVM chain.
// Returns a descriptive error if the token is not registered or disabled.
func (r *Registry) ResolveEVM(ctx context.Context, token Token, chainID string) (ChainEntry, error) {
	cacheKey := r.key(string(token), "evm", chainID)

	if r.cache != nil {
		if raw, err := r.cache.Get(ctx, cacheKey).Bytes(); err == nil {
			var entry ChainEntry
			if json.Unmarshal(raw, &entry) == nil {
				return entry, nil
			}
		}
	}

	row, err := r.store.GetStablecoinToken(ctx, string(token), "evm", chainID)
	if err != nil {
		return ChainEntry{}, fmt.Errorf("registry lookup: %w", err)
	}
	if row == nil {
		return ChainEntry{}, fmt.Errorf(
			"token %s is not registered for EVM chain %s — "+
				"add it via POST /admin/stablecoins or run the environment seed migration",
			strings.ToUpper(string(token)), chainID,
		)
	}

	entry := ChainEntry{
		ID:              row.ID,
		ContractAddress: row.Contract,
		Decimals:        row.Decimals,
		Symbol:          row.Symbol,
	}
	r.setCache(ctx, cacheKey, entry)
	return entry, nil
}

// ResolveSolana returns the SPL mint entry for a token.
// Returns a descriptive error if the token is not registered or disabled.
func (r *Registry) ResolveSolana(ctx context.Context, token Token) (SolanaEntry, error) {
	cacheKey := r.key(string(token), "solana", "")

	if r.cache != nil {
		if raw, err := r.cache.Get(ctx, cacheKey).Bytes(); err == nil {
			var entry SolanaEntry
			if json.Unmarshal(raw, &entry) == nil {
				return entry, nil
			}
		}
	}

	row, err := r.store.GetStablecoinToken(ctx, string(token), "solana", "")
	if err != nil {
		return SolanaEntry{}, fmt.Errorf("registry lookup: %w", err)
	}
	if row == nil {
		return SolanaEntry{}, fmt.Errorf(
			"token %s is not registered for Solana — "+
				"add it via POST /admin/stablecoins or run the environment seed migration",
			strings.ToUpper(string(token)),
		)
	}

	entry := SolanaEntry{
		ID:          row.ID,
		MintAddress: row.Contract,
		Decimals:    row.Decimals,
		Symbol:      row.Symbol,
	}
	r.setCache(ctx, cacheKey, entry)
	return entry, nil
}

// ListAll returns all enabled tokens. Used by the discovery endpoint.
// Cached as a single aggregate key.
func (r *Registry) ListAll(ctx context.Context) ([]TokenInfo, error) {
	listKey := cacheKeyPrefix + "list"

	if r.cache != nil {
		if raw, err := r.cache.Get(ctx, listKey).Bytes(); err == nil {
			var tokens []TokenInfo
			if json.Unmarshal(raw, &tokens) == nil {
				return tokens, nil
			}
		}
	}

	rows, err := r.store.ListStablecoinTokens(ctx, "")
	if err != nil {
		return nil, err
	}

	tokens := make([]TokenInfo, 0, len(rows))
	for _, row := range rows {
		tokens = append(tokens, TokenInfo{
			ID:       row.ID,
			Token:    row.Token,
			Symbol:   row.Symbol,
			Chain:    row.ChainType,
			ChainID:  row.ChainID,
			Contract: row.Contract,
			Decimals: row.Decimals,
		})
	}

	r.setCache(ctx, listKey, tokens)
	return tokens, nil
}

// Upsert writes a token entry to the DB and invalidates the relevant cache keys.
func (r *Registry) Upsert(ctx context.Context, t *storage.StablecoinToken) (*storage.StablecoinToken, error) {
	result, err := r.store.UpsertStablecoinToken(ctx, t)
	if err != nil {
		return nil, err
	}
	r.invalidate(ctx, result.Token, result.ChainType, result.ChainID)
	return result, nil
}

// Disable soft-deletes a token entry and invalidates the cache.
func (r *Registry) Disable(ctx context.Context, id string) error {
	// Fetch all rows first so we can build the correct cache invalidation key.
	// This is a small table — the scan is negligible.
	rows, err := r.store.ListStablecoinTokens(ctx, "")
	if err != nil {
		return err
	}

	var target *storage.StablecoinToken
	for _, row := range rows {
		if row.ID == id {
			target = row
			break
		}
	}

	if err := r.store.DisableStablecoinToken(ctx, id); err != nil {
		return err
	}

	if target != nil {
		r.invalidate(ctx, target.Token, target.ChainType, target.ChainID)
	}
	return nil
}

// ── Cache internals ───────────────────────────────────────────────────────────

func (r *Registry) key(token, chainType, chainID string) string {
	return fmt.Sprintf("%s%s:%s:%s", cacheKeyPrefix, token, chainType, chainID)
}

func (r *Registry) setCache(ctx context.Context, key string, v any) {
	if r.cache == nil {
		return
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	r.cache.Set(ctx, key, raw, cacheTTL) //nolint:errcheck
}

// invalidate removes the specific entry key and the aggregate list key.
func (r *Registry) invalidate(ctx context.Context, token, chainType, chainID string) {
	if r.cache == nil {
		return
	}
	r.cache.Del(ctx,
		r.key(token, chainType, chainID),
		cacheKeyPrefix+"list",
	) //nolint:errcheck
}

// ── Token parsing ─────────────────────────────────────────────────────────────

// ParseToken normalises a token string. Accepts any casing ("USDC", "usdc").
// We don't restrict to a fixed enum — the DB is the source of truth for what's
// supported. This just validates the format is sane before hitting the DB.
func ParseToken(s string) (Token, error) {
	t := Token(strings.ToLower(strings.TrimSpace(s)))
	if t == "" {
		return "", fmt.Errorf("token is required")
	}
	for _, c := range string(t) {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
			return "", fmt.Errorf("invalid token %q — must be lowercase alphanumeric (e.g. 'usdc', 'usdt')", s)
		}
	}
	if len(t) < 2 || len(t) > 10 {
		return "", fmt.Errorf("invalid token %q — expected 2-10 alphanumeric characters", s)
	}
	return t, nil
}