package middleware

import (
	"context"
	"net/http"
	"os"
	"strings"

	"github.com/redis/go-redis/v9"
	"github.com/vaultkey/vaultkey/internal/ratelimit"
	"github.com/vaultkey/vaultkey/internal/storage"
	"golang.org/x/crypto/bcrypt"
)

// OrgIDContextKey is the context key for the org ID injected by SDKAuth.
const OrgIDContextKey contextKey = "org_id"

// SDKAuth is the middleware for SDK-facing endpoints (/sdk/*).
// It extends the existing API key validation with:
//   - org_id injection into context (required for credit deduction)
//   - cloud_managed enforcement: only cloud-provisioned keys work here
//   - testnet/mainnet key prefix enforcement (same as Auth)
//
// Self-hosted projects (org_id IS NULL on the project) are rejected — they
// use the existing /wallets, /projects routes directly, not /sdk routes.
//
// SDKAuth does NOT replace Auth — it wraps the same key lookup logic
// but adds the org context layer on top.
func SDKAuth(store *storage.Store, limiter *ratelimit.Limiter, redisClient *redis.Client) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiKey := r.Header.Get("X-API-Key")
			apiSecret := r.Header.Get("X-API-Secret")

			if apiKey == "" || apiSecret == "" {
				writeError(w, http.StatusUnauthorized, "missing X-API-Key or X-API-Secret")
				return
			}

			// Enforce testnet/mainnet key prefix isolation.
			// Testnet servers only accept testnet_ keys.
			// Mainnet servers only accept vk_live_ keys.
			env := os.Getenv("ENVIRONMENT")
			if strings.HasPrefix(apiKey, "testnet_") && env == "mainnet" {
				writeError(w, http.StatusUnauthorized,
					"testnet API key cannot be used on mainnet — switch to testnet environment")
				return
			}
			if strings.HasPrefix(apiKey, "vk_live_") && env == "testnet" {
				writeError(w, http.StatusUnauthorized,
					"mainnet API key cannot be used on testnet — switch to mainnet environment")
				return
			}

			// Look up API key — same cached path as Auth middleware.
			ak, project, err := store.GetAPIKeyByKeyCached(r.Context(), redisClient, apiKey)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			if ak == nil || project == nil {
				writeError(w, http.StatusUnauthorized, "invalid credentials")
				return
			}

			// Constant-time bcrypt compare — prevents timing attacks.
			if err := bcrypt.CompareHashAndPassword([]byte(ak.SecretHash), []byte(apiSecret)); err != nil {
				writeError(w, http.StatusUnauthorized, "invalid credentials")
				return
			}

			// SDK routes only serve cloud-managed projects.
			// Self-hosted projects use the original /wallets routes directly.
			if !project.CloudManaged {
				writeError(w, http.StatusForbidden,
					"this API key is not associated with a cloud-managed project — "+
						"use the direct API endpoints (/wallets, /projects, etc.) for self-hosted deployments")
				return
			}

			// Resolve org_id — must exist for cloud-managed projects.
			// If missing, the project was created in an inconsistent state.
			if project.OrgID == nil || *project.OrgID == "" {
				writeError(w, http.StatusInternalServerError,
					"cloud-managed project has no associated organization — contact support")
				return
			}

			// Per-project rate limiting — same as Auth middleware.
			// RPS is set to 10 (free) or 1000 (pro) at key creation time
			// and updated when the org purchases credits.
			allowed, err := limiter.Allow(r.Context(), project.ID, project.RateLimitRPS)
			if err != nil {
				// Fail open on Redis errors — don't block requests.
				// Log in production.
			}
			if !allowed {
				w.Header().Set("Retry-After", "1")
				writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}

			// Inject both project and org_id into context.
			ctx := context.WithValue(r.Context(), ProjectContextKey, project)
			ctx = context.WithValue(ctx, OrgIDContextKey, *project.OrgID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// OrgIDFromContext retrieves the org ID injected by SDKAuth.
// Returns empty string if not set (non-SDK routes).
func OrgIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(OrgIDContextKey).(string)
	return id
}