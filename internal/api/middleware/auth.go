package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/clerk/clerk-sdk-go/v2"
	clerkhttp "github.com/clerk/clerk-sdk-go/v2/http"
	"github.com/redis/go-redis/v9"
	"github.com/vaultkey/vaultkey/internal/ratelimit"
	"github.com/vaultkey/vaultkey/internal/storage"
	"golang.org/x/crypto/bcrypt"
)

// ── Context keys ──────────────────────────────────────────────────────────────

type contextKey string

const (
	ProjectContextKey     contextKey = "project"
	ClerkUserContextKey   contextKey = "clerk_user_id"
	OrgMemberContextKey   contextKey = "org_member"
)

// ── API Key Auth (existing, backward-compatible) ──────────────────────────────

// Auth validates X-API-Key and X-API-Secret against the api_keys table,
// then checks per-project rate limit.
// Supports both legacy (api_key on projects table, migrated) and new multi-key model.
func Auth(store *storage.Store, limiter *ratelimit.Limiter, redisClient *redis.Client) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiKey := r.Header.Get("X-API-Key")
			apiSecret := r.Header.Get("X-API-Secret")

			if apiKey == "" || apiSecret == "" {
				writeError(w, http.StatusUnauthorized, "missing X-API-Key or X-API-Secret")
				return
			}

			if strings.HasPrefix(apiKey, "testnet_") && os.Getenv("ENVIRONMENT") == "mainnet" {
                writeError(w, http.StatusUnauthorized, 
                    "testnet API key cannot be used on mainnet - switch to testnet")
                return
            }

			if strings.HasPrefix(apiKey, "vk_live_") && os.Getenv("ENVIRONMENT") == "testnet" {
                writeError(w, http.StatusUnauthorized, 
                    "mainnet API key cannot be used on testnet - switch to mainnet")
                return
            }

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

			// Per-project rate limiting.
			allowed, err := limiter.Allow(r.Context(), project.ID, project.RateLimitRPS)
			if err != nil {
				// Log but don't block on Redis errors — fail open.
			}
			if !allowed {
				w.Header().Set("Retry-After", "1")
				writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}

			ctx := context.WithValue(r.Context(), ProjectContextKey, project)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ProjectFromContext retrieves the authenticated project from the request context.
func ProjectFromContext(ctx context.Context) *storage.Project {
	p, _ := ctx.Value(ProjectContextKey).(*storage.Project)
	return p
}

// ── Clerk JWT Auth (cloud mode) ───────────────────────────────────────────────

// ClerkAuth wraps Clerk's WithHeaderAuthorization middleware and extracts the
// clerk_user_id into our own context key for downstream handlers.
// Responds with 401 if the JWT is missing or invalid.
func ClerkAuth() func(http.Handler) http.Handler {
	// RequireHeaderAuthorization returns 403 on missing/invalid token.
	// We use WithHeaderAuthorization so we can return our own 401 JSON error.
	inner := clerkhttp.WithHeaderAuthorization()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Check Authorization header exists before delegating to Clerk.
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
				writeError(w, http.StatusUnauthorized, "Authorization: Bearer <token> header required")
				return
			}

			// Clerk middleware validates the JWT, sets SessionClaims in context.
			var verified bool
			inner(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				claims, ok := clerk.SessionClaimsFromContext(r.Context())
				if !ok || claims == nil {
					return
				}
				verified = true
				ctx := context.WithValue(r.Context(), ClerkUserContextKey, claims.Subject)
				next.ServeHTTP(w, r.WithContext(ctx))
			})).ServeHTTP(w, r)

			if !verified {
				writeError(w, http.StatusUnauthorized, "invalid or expired session token")
			}
		})
	}
}

// ClerkUserIDFromContext retrieves the authenticated Clerk user ID.
func ClerkUserIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(ClerkUserContextKey).(string)
	return id
}

// OrgMemberFromContext retrieves the org member record set by OrgAuthz.
func OrgMemberFromContext(ctx context.Context) *storage.OrgMember {
	m, _ := ctx.Value(OrgMemberContextKey).(*storage.OrgMember)
	return m
}

// ── RBAC / Org Authorization ──────────────────────────────────────────────────

// RoleLevel maps roles to numeric levels for ≥ comparisons.
var roleLevel = map[string]int{
	"owner":     4,
	"admin":     3,
	"developer": 2,
	"viewer":    1,
}

// OrgAuthz extracts {org_id} from the URL path, verifies the caller is an
// active member of that org, and enforces a minimum role level.
// Sets OrgMemberContextKey in context for downstream handlers.
//
// minRole: "viewer" | "developer" | "admin" | "owner"
func OrgAuthz(store *storage.Store, minRole string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			clerkUserID := ClerkUserIDFromContext(r.Context())
			if clerkUserID == "" {
				writeError(w, http.StatusUnauthorized, "authentication required")
				return
			}

			orgID := r.PathValue("org_id")
			if orgID == "" {
				writeError(w, http.StatusBadRequest, "org_id path param required")
				return
			}

			member, err := store.GetOrgMember(r.Context(), orgID, clerkUserID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			if member == nil {
				writeError(w, http.StatusForbidden, "not a member of this organization")
				return
			}

			if roleLevel[member.Role] < roleLevel[minRole] {
				writeError(w, http.StatusForbidden,
					"insufficient permissions: "+minRole+" role required")
				return
			}

			ctx := context.WithValue(r.Context(), OrgMemberContextKey, member)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg}) //nolint:errcheck
}