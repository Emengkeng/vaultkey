package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const apiKeyCacheTTL = 5 * time.Minute

type cachedAPIKeyEntry struct {
	APIKey  APIKey  `json:"api_key"`
	Project Project `json:"project"`
}

func apiKeyCacheKey(key string) string {
	return fmt.Sprintf("vk:apikey:%s", key)
}

// GetAPIKeyByKeyCached is a Redis-backed wrapper around GetAPIKeyByKey.
// Cache miss falls through to DB. Returns the same signature.
func (s *Store) GetAPIKeyByKeyCached(ctx context.Context, redisClient *redis.Client, key string) (*APIKey, *Project, error) {
	cacheKey := apiKeyCacheKey(key)

	raw, err := redisClient.Get(ctx, cacheKey).Bytes()
	if err == nil {
		var entry cachedAPIKeyEntry
		if json.Unmarshal(raw, &entry) == nil {
			return &entry.APIKey, &entry.Project, nil
		}
		// Corrupt entry — fall through to DB.
	}

	ak, proj, err := s.GetAPIKeyByKey(ctx, key)
	if err != nil {
		return nil, nil, err
	}
	if ak == nil || proj == nil {
		return nil, nil, nil
	}

	// Populate cache.
	if data, merr := json.Marshal(cachedAPIKeyEntry{APIKey: *ak, Project: *proj}); merr == nil {
		redisClient.Set(ctx, cacheKey, data, apiKeyCacheTTL) //nolint:errcheck
	}

	return ak, proj, nil
}

// InvalidateAPIKeyCache deletes the cache entry for a given raw key string.
func InvalidateAPIKeyCache(ctx context.Context, redisClient *redis.Client, key string) {
	redisClient.Del(ctx, apiKeyCacheKey(key)) //nolint:errcheck
}