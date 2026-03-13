package middleware

import (
	"crypto/subtle"
	"net/http"
)

// AdminAuth returns middleware that checks the X-Admin-Token header against
// the configured admin token. Uses constant-time comparison to prevent
// timing attacks.
//
// The admin token is set via the ADMIN_TOKEN environment variable.
// If the env var is empty the server refuses to start (enforced in config.Load).
// In production, admin routes should additionally be firewalled to internal
// network only — the token is a second layer, not the primary control.
func AdminAuth(adminToken string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provided := r.Header.Get("X-Admin-Token")
			if provided == "" {
				http.Error(w, `{"error":"X-Admin-Token header required"}`, http.StatusUnauthorized)
				return
			}

			// Constant-time comparison prevents timing attacks.
			if subtle.ConstantTimeCompare([]byte(provided), []byte(adminToken)) != 1 {
				http.Error(w, `{"error":"invalid admin token"}`, http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}