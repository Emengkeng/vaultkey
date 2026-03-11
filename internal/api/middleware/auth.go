package middleware

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/vaultkey/vaultkey/internal/ratelimit"
	"github.com/vaultkey/vaultkey/internal/storage"
	"golang.org/x/crypto/bcrypt"
)

type contextKey string

const ProjectContextKey contextKey = "project"

// Auth validates X-API-Key and X-API-Secret, then checks per-project rate limit.
func Auth(store *storage.Store, limiter *ratelimit.Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiKey := r.Header.Get("X-API-Key")
			apiSecret := r.Header.Get("X-API-Secret")

			if apiKey == "" || apiSecret == "" {
				writeError(w, http.StatusUnauthorized, "missing X-API-Key or X-API-Secret")
				return
			}

			project, err := store.GetProjectByAPIKey(r.Context(), apiKey)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			if project == nil {
				writeError(w, http.StatusUnauthorized, "invalid credentials")
				return
			}

			// Constant-time bcrypt compare - prevents timing attacks
			if err := bcrypt.CompareHashAndPassword([]byte(project.APISecretHash), []byte(apiSecret)); err != nil {
				writeError(w, http.StatusUnauthorized, "invalid credentials")
				return
			}

			// Per-project rate limiting
			allowed, err := limiter.Allow(r.Context(), project.ID, project.RateLimitRPS)
			if err != nil {
				// Log but don't block on Redis errors
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

func ProjectFromContext(ctx context.Context) *storage.Project {
	p, _ := ctx.Value(ProjectContextKey).(*storage.Project)
	return p
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg}) //nolint:errcheck
}
