package nonce

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Manager handles nonce assignment for relayer wallets under concurrent load.
//
// Design:
// - Redis INCR gives us atomic nonce increments across all worker goroutines.
// - On startup (or after a crash), we sync the counter from the chain.
// - If a transaction fails on-chain, the nonce is already consumed. We detect
//   this by syncing from chain periodically and whenever submission errors occur.
//
// Key format: vaultkey:nonce:{chainId}:{address}
type Manager struct {
	client *redis.Client
}

func New(client *redis.Client) *Manager {
	return &Manager{client: client}
}

// Next returns the next nonce to use for the given address and chain.
// It atomically increments the counter in Redis.
// Caller must call SyncFromChain first if the counter may be stale.
func (m *Manager) Next(ctx context.Context, chainID, address string) (uint64, error) {
	key := m.key(chainID, address)

	// INCR atomically increments and returns new value.
	// If key doesn't exist, Redis initialises it to 0 then increments to 1.
	// This means we must call SyncFromChain before the first use.
	val, err := m.client.Incr(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("nonce incr: %w", err)
	}

	// Subtract 1: we want the value before increment as the nonce.
	// e.g. if chain nonce is 5, we seed Redis to 5, first INCR returns 6,
	// we return 5. Next call returns 6, etc.
	if val <= 0 {
		return 0, fmt.Errorf("nonce counter in invalid state: %d", val)
	}

	return uint64(val - 1), nil
}

// SyncFromChain sets the Redis counter to the current pending nonce from the chain.
// Call this on startup and after detecting a nonce gap.
func (m *Manager) SyncFromChain(ctx context.Context, chainID, address string, pendingNonce uint64) error {
	key := m.key(chainID, address)

	// SET with NX would skip if exists. We want to force-set on explicit sync.
	// We add 1 because Next() does val-1, so seeding with pendingNonce+1
	// means first Next() call returns pendingNonce.
	err := m.client.Set(ctx, key, pendingNonce+1, 24*time.Hour).Err()
	if err != nil {
		return fmt.Errorf("nonce sync: %w", err)
	}
	return nil
}

// Peek returns the current counter value without incrementing.
// Useful for health checks and monitoring.
func (m *Manager) Peek(ctx context.Context, chainID, address string) (uint64, error) {
	key := m.key(chainID, address)
	val, err := m.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("nonce peek: %w", err)
	}
	n, err := strconv.ParseUint(val, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("nonce parse: %w", err)
	}
	// Counter is always 1 ahead of last used nonce
	if n == 0 {
		return 0, nil
	}
	return n - 1, nil
}

// Reset forces the counter to a specific value. Use after detecting nonce gaps.
func (m *Manager) Reset(ctx context.Context, chainID, address string, nonce uint64) error {
	return m.SyncFromChain(ctx, chainID, address, nonce)
}

func (m *Manager) key(chainID, address string) string {
	return fmt.Sprintf("vaultkey:nonce:%s:%s", chainID, address)
}
