package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Limiter implements a per-project sliding window rate limiter backed by Redis.
// Uses a sorted set: key = vaultkey:rl:{projectID}, score = timestamp, member = unique request ID.
type Limiter struct {
	client *redis.Client
}

func New(client *redis.Client) *Limiter {
	return &Limiter{client: client}
}

// Allow checks if the project is within its rate limit.
// Returns true if the request is allowed, false if rate limited.
func (l *Limiter) Allow(ctx context.Context, projectID string, limitRPS int) (bool, error) {
	now := time.Now()
	windowStart := now.Add(-time.Second).UnixMicro()
	key := fmt.Sprintf("vaultkey:rl:%s", projectID)

	pipe := l.client.Pipeline()

	// Remove entries outside the 1-second window
	pipe.ZRemRangeByScore(ctx, key, "-inf", fmt.Sprintf("%d", windowStart))

	// Count entries in current window
	countCmd := pipe.ZCard(ctx, key)

	// Add current request with timestamp as score, unique member
	member := fmt.Sprintf("%d", now.UnixNano())
	pipe.ZAdd(ctx, key, redis.Z{Score: float64(now.UnixMicro()), Member: member})

	// Expire the key after 2 seconds to avoid memory leaks
	pipe.Expire(ctx, key, 2*time.Second)

	if _, err := pipe.Exec(ctx); err != nil {
		// On Redis failure, fail open (allow request) to avoid blocking the API
		// Log this in production
		return true, fmt.Errorf("rate limit redis error (failing open): %w", err)
	}

	count := countCmd.Val()
	return count < int64(limitRPS), nil
}
