package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/vaultkey/vaultkey/internal/api/middleware"
	"github.com/vaultkey/vaultkey/internal/storage"
	"golang.org/x/crypto/bcrypt"
	"github.com/redis/go-redis/v9"
)

// clerkUserIDRe validates Clerk user ID format.
var clerkUserIDRe = regexp.MustCompile(`^user_[a-zA-Z0-9]+$`)

// CloudHandler handles all /cloud/* endpoints.
type CloudHandler struct {
	store *storage.Store
	redisClient *redis.Client
}

func NewCloudHandler(store *storage.Store, redisClient *redis.Client) *CloudHandler {
	return &CloudHandler{store: store, redisClient: redisClient}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (h *CloudHandler) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func (h *CloudHandler) writeError(w http.ResponseWriter, status int, msg string) {
	h.writeJSON(w, status, map[string]string{"error": msg})
}

// slugify converts a string to a URL-safe slug.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else if unicode.IsSpace(r) || r == '-' || r == '_' {
			b.WriteRune('-')
		}
	}
	// Append random suffix to guarantee uniqueness.
	suffix, _ := generateToken(3)
	return b.String() + "-" + suffix
}

// ── POST /cloud/onboarding ────────────────────────────────────────────────────
//
// Called after a user signs up via Clerk. Creates their organization and
// seeds them as the owner. Idempotent: if the user already has an org,
// returns their existing org.

type onboardingRequest struct {
	OrgName      string `json:"org_name"`
	BillingEmail string `json:"billing_email"`
}

type onboardingResponse struct {
	OrgID     string    `json:"org_id"`
	OrgName   string    `json:"org_name"`
	OrgSlug   string    `json:"org_slug"`
	ProjectID string    `json:"project_id"`
	CreatedAt time.Time `json:"created_at"`
}

func (h *CloudHandler) Onboarding(w http.ResponseWriter, r *http.Request) {
	clerkUserID := middleware.ClerkUserIDFromContext(r.Context())
	if clerkUserID == "" {
		h.writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var req onboardingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.OrgName) == "" {
		h.writeError(w, http.StatusBadRequest, "org_name is required")
		return
	}

	// Idempotency check: return existing org if user already onboarded.
	orgs, err := h.store.GetOrganizationsForUser(r.Context(), clerkUserID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if len(orgs) > 0 {
		// Already onboarded — return existing org.
		org := orgs[0]
		proj, _ := h.store.GetOrganizationProject(r.Context(), org.ID)
		resp := onboardingResponse{OrgID: org.ID, OrgName: org.Name, OrgSlug: org.Slug, CreatedAt: org.CreatedAt}
		if proj != nil {
			resp.ProjectID = proj.ID
		}
		h.writeJSON(w, http.StatusOK, resp)
		return
	}

	billingEmail := strings.TrimSpace(req.BillingEmail)
	slug := slugify(req.OrgName)

	// Create org.
	org, err := h.store.CreateOrganization(r.Context(), strings.TrimSpace(req.OrgName), slug, clerkUserID, billingEmail)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "failed to create organization: "+err.Error())
		return
	}

	// Seed caller as owner.
	if _, err := h.store.AddOrgMember(r.Context(), org.ID, clerkUserID, "owner"); err != nil {
		h.writeError(w, http.StatusInternalServerError, "failed to add owner: "+err.Error())
		return
	}

	// Create the project for the org.
	proj, err := h.store.EnsureProjectForOrg(r.Context(), org.ID, org.Name)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "failed to create project: "+err.Error())
		return
	}

	h.writeJSON(w, http.StatusCreated, onboardingResponse{
		OrgID:     org.ID,
		OrgName:   org.Name,
		OrgSlug:   org.Slug,
		ProjectID: proj.ID,
		CreatedAt: org.CreatedAt,
	})
}

// ── GET /cloud/organizations ──────────────────────────────────────────────────

type orgListItem struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Slug         string    `json:"slug"`
	BillingEmail string    `json:"billing_email"`
	CreatedAt    time.Time `json:"created_at"`
}

func (h *CloudHandler) ListOrganizations(w http.ResponseWriter, r *http.Request) {
	clerkUserID := middleware.ClerkUserIDFromContext(r.Context())
	orgs, err := h.store.GetOrganizationsForUser(r.Context(), clerkUserID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	items := make([]orgListItem, 0, len(orgs))
	for _, o := range orgs {
		items = append(items, orgListItem{
			ID: o.ID, Name: o.Name, Slug: o.Slug,
			BillingEmail: o.BillingEmail, CreatedAt: o.CreatedAt,
		})
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"organizations": items})
}

// ── GET /cloud/organizations/{org_id} ─────────────────────────────────────────

type orgDetail struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Slug         string    `json:"slug"`
	CreatedBy    string    `json:"created_by"`
	BillingEmail string    `json:"billing_email"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	ProjectID    string    `json:"project_id,omitempty"`
}

func (h *CloudHandler) GetOrganization(w http.ResponseWriter, r *http.Request) {
	member := middleware.OrgMemberFromContext(r.Context())
	org, err := h.store.GetOrganizationByID(r.Context(), member.OrgID)
	if err != nil || org == nil {
		h.writeError(w, http.StatusNotFound, "organization not found")
		return
	}
	proj, _ := h.store.GetOrganizationProject(r.Context(), org.ID)
	detail := orgDetail{
		ID: org.ID, Name: org.Name, Slug: org.Slug, CreatedBy: org.CreatedBy,
		BillingEmail: org.BillingEmail, CreatedAt: org.CreatedAt, UpdatedAt: org.UpdatedAt,
	}
	if proj != nil {
		detail.ProjectID = proj.ID
	}
	h.writeJSON(w, http.StatusOK, detail)
}

// ── PATCH /cloud/organizations/{org_id} ──────────────────────────────────────

type updateOrgRequest struct {
	Name         string `json:"name"`
	BillingEmail string `json:"billing_email"`
}

func (h *CloudHandler) UpdateOrganization(w http.ResponseWriter, r *http.Request) {
	member := middleware.OrgMemberFromContext(r.Context())

	var req updateOrgRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		h.writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	org, err := h.store.UpdateOrganization(r.Context(), member.OrgID,
		strings.TrimSpace(req.Name), strings.TrimSpace(req.BillingEmail))
	if err != nil || org == nil {
		h.writeError(w, http.StatusInternalServerError, "failed to update organization")
		return
	}

	h.writeJSON(w, http.StatusOK, orgDetail{
		ID: org.ID, Name: org.Name, Slug: org.Slug, CreatedBy: org.CreatedBy,
		BillingEmail: org.BillingEmail, CreatedAt: org.CreatedAt, UpdatedAt: org.UpdatedAt,
	})
}

// ── DELETE /cloud/organizations/{org_id} ─────────────────────────────────────

func (h *CloudHandler) DeleteOrganization(w http.ResponseWriter, r *http.Request) {
	member := middleware.OrgMemberFromContext(r.Context())

	if err := h.store.SoftDeleteOrganization(r.Context(), member.OrgID); err != nil {
		h.writeError(w, http.StatusInternalServerError, "failed to delete organization: "+err.Error())
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "org_id": member.OrgID})
}

// ── GET /cloud/organizations/{org_id}/members ─────────────────────────────────

type memberResponse struct {
	ID          string    `json:"id"`
	ClerkUserID string    `json:"clerk_user_id"`
	Role        string    `json:"role"`
	Email       string    `json:"email"`
	FirstName   string    `json:"first_name"`
	LastName    string    `json:"last_name"`
	JoinedAt    time.Time `json:"joined_at"`
}

func (h *CloudHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
	member := middleware.OrgMemberFromContext(r.Context())
	members, err := h.store.ListOrgMembers(r.Context(), member.OrgID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	resp := make([]memberResponse, 0, len(members))
	for _, m := range members {
		resp = append(resp, memberResponse{
			ID: m.ID, ClerkUserID: m.ClerkUserID, Role: m.Role,
			Email: m.Email, FirstName: m.FirstName, LastName: m.LastName,
			JoinedAt: m.JoinedAt,
		})
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"members": resp})
}

// ── PATCH /cloud/organizations/{org_id}/members/{clerk_user_id} ──────────────

type updateMemberRequest struct {
	Role string `json:"role"`
}

func (h *CloudHandler) UpdateMember(w http.ResponseWriter, r *http.Request) {
	caller := middleware.OrgMemberFromContext(r.Context())
	targetUserID := r.PathValue("clerk_user_id")

	if !clerkUserIDRe.MatchString(targetUserID) {
		h.writeError(w, http.StatusBadRequest, "invalid clerk_user_id format")
		return
	}

	var req updateMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate role — cannot assign owner via this endpoint.
	validRoles := map[string]bool{"admin": true, "developer": true, "viewer": true}
	if !validRoles[req.Role] {
		h.writeError(w, http.StatusBadRequest, "role must be one of: admin, developer, viewer")
		return
	}

	// Cannot change the owner's role.
	target, err := h.store.GetOrgMember(r.Context(), caller.OrgID, targetUserID)
	if err != nil || target == nil {
		h.writeError(w, http.StatusNotFound, "member not found")
		return
	}
	if target.Role == "owner" {
		h.writeError(w, http.StatusForbidden, "cannot change the owner's role")
		return
	}

	updated, err := h.store.UpdateOrgMemberRole(r.Context(), caller.OrgID, targetUserID, req.Role)
	if err != nil || updated == nil {
		h.writeError(w, http.StatusInternalServerError, "failed to update member role")
		return
	}

	h.writeJSON(w, http.StatusOK, memberResponse{
		ID: updated.ID, ClerkUserID: updated.ClerkUserID, Role: updated.Role,
		JoinedAt: updated.JoinedAt,
	})
}

// ── DELETE /cloud/organizations/{org_id}/members/{clerk_user_id} ─────────────

func (h *CloudHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	caller := middleware.OrgMemberFromContext(r.Context())
	targetUserID := r.PathValue("clerk_user_id")

	if !clerkUserIDRe.MatchString(targetUserID) {
		h.writeError(w, http.StatusBadRequest, "invalid clerk_user_id format")
		return
	}

	target, err := h.store.GetOrgMember(r.Context(), caller.OrgID, targetUserID)
	if err != nil || target == nil {
		h.writeError(w, http.StatusNotFound, "member not found")
		return
	}

	// Prevent removing the owner if they are the last one.
	if target.Role == "owner" {
		ownerCount, err := h.store.CountActiveOwners(r.Context(), caller.OrgID)
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if ownerCount <= 1 {
			h.writeError(w, http.StatusForbidden,
				"cannot remove the last owner — transfer ownership first")
			return
		}
	}

	if err := h.store.RemoveOrgMember(r.Context(), caller.OrgID, targetUserID); err != nil {
		h.writeError(w, http.StatusInternalServerError, "failed to remove member: "+err.Error())
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]string{"status": "removed", "clerk_user_id": targetUserID})
}

// ── POST /cloud/organizations/{org_id}/invites ────────────────────────────────

type createInviteRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

type inviteResponse struct {
	ID        string     `json:"id"`
	OrgID     string     `json:"org_id"`
	Email     string     `json:"email"`
	Token     string     `json:"token"`
	Role      string     `json:"role"`
	CreatedBy string     `json:"created_by"`
	ExpiresAt time.Time  `json:"expires_at"`
	CreatedAt time.Time  `json:"created_at"`
	AcceptedAt *time.Time `json:"accepted_at,omitempty"`
}

func (h *CloudHandler) CreateInvite(w http.ResponseWriter, r *http.Request) {
	caller := middleware.OrgMemberFromContext(r.Context())

	var req createInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if req.Email == "" || !strings.Contains(req.Email, "@") {
		h.writeError(w, http.StatusBadRequest, "valid email is required")
		return
	}

	// Cannot invite as owner.
	validRoles := map[string]bool{"admin": true, "developer": true, "viewer": true}
	if req.Role == "" {
		req.Role = "developer"
	}
	if !validRoles[req.Role] {
		h.writeError(w, http.StatusBadRequest, "role must be one of: admin, developer, viewer")
		return
	}

	// Generate a secure invite token.
	token, err := generateToken(32)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	inv, err := h.store.CreateInvite(r.Context(), caller.OrgID, req.Email, token, req.Role, caller.ClerkUserID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "failed to create invite: "+err.Error())
		return
	}

	h.writeJSON(w, http.StatusCreated, inviteResponse{
		ID: inv.ID, OrgID: inv.OrgID, Email: inv.Email, Token: inv.Token,
		Role: inv.Role, CreatedBy: inv.CreatedBy, ExpiresAt: inv.ExpiresAt,
		CreatedAt: inv.CreatedAt,
	})
}

// ── GET /cloud/organizations/{org_id}/invites ─────────────────────────────────

func (h *CloudHandler) ListInvites(w http.ResponseWriter, r *http.Request) {
	member := middleware.OrgMemberFromContext(r.Context())
	invites, err := h.store.ListOrgInvites(r.Context(), member.OrgID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	resp := make([]inviteResponse, 0, len(invites))
	for _, inv := range invites {
		resp = append(resp, inviteResponse{
			ID: inv.ID, OrgID: inv.OrgID, Email: inv.Email, Token: inv.Token,
			Role: inv.Role, CreatedBy: inv.CreatedBy, ExpiresAt: inv.ExpiresAt,
			CreatedAt: inv.CreatedAt, AcceptedAt: inv.AcceptedAt,
		})
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"invites": resp})
}

// ── POST /cloud/invites/{token}/accept ────────────────────────────────────────

func (h *CloudHandler) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	clerkUserID := middleware.ClerkUserIDFromContext(r.Context())
	token := r.PathValue("token")
	if token == "" {
		h.writeError(w, http.StatusBadRequest, "token path param required")
		return
	}

	inv, err := h.store.GetInviteByToken(r.Context(), token)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if inv == nil {
		h.writeError(w, http.StatusNotFound, "invite not found or already revoked")
		return
	}
	if inv.AcceptedAt != nil {
		h.writeError(w, http.StatusConflict, "invite has already been accepted")
		return
	}
	if time.Now().After(inv.ExpiresAt) {
		h.writeError(w, http.StatusGone, "invite has expired")
		return
	}

	// Check if the user is already a member.
	existing, err := h.store.GetOrgMember(r.Context(), inv.OrgID, clerkUserID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if existing != nil {
		h.writeError(w, http.StatusConflict, "you are already a member of this organization")
		return
	}

	// Mark invite accepted.
	accepted, err := h.store.AcceptInvite(r.Context(), token, clerkUserID)
	if err != nil || accepted == nil {
		h.writeError(w, http.StatusInternalServerError, "failed to accept invite")
		return
	}

	// Add the user as an org member.
	if _, err := h.store.AddOrgMember(r.Context(), inv.OrgID, clerkUserID, inv.Role); err != nil {
		h.writeError(w, http.StatusInternalServerError, "failed to add member: "+err.Error())
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]string{
		"status": "accepted",
		"org_id": inv.OrgID,
		"role":   inv.Role,
	})
}

// ── DELETE /cloud/organizations/{org_id}/invites/{token} ─────────────────────

func (h *CloudHandler) RevokeInvite(w http.ResponseWriter, r *http.Request) {
	member := middleware.OrgMemberFromContext(r.Context())
	token := r.PathValue("token")
	if token == "" {
		h.writeError(w, http.StatusBadRequest, "token path param required")
		return
	}

	if err := h.store.RevokeInvite(r.Context(), member.OrgID, token); err != nil {
		h.writeError(w, http.StatusNotFound, "invite not found: "+err.Error())
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// ── POST /cloud/organizations/{org_id}/api-keys ───────────────────────────────

type createAPIKeyRequest struct {
	Name string `json:"name"`
}

type createAPIKeyResponse struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Key       string     `json:"key"`
	Secret    string     `json:"secret"` // shown once only
	CreatedAt time.Time  `json:"created_at"`
}

type listAPIKeyItem struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Key        string     `json:"key"`
	Active     bool       `json:"active"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

func (h *CloudHandler) CreateAPIKey(w http.ResponseWriter, r *http.Request) {
	caller := middleware.OrgMemberFromContext(r.Context())

	var req createAPIKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = fmt.Sprintf("Key %s", time.Now().Format("2006-01-02"))
	}

	// Ensure project exists.
	org, err := h.store.GetOrganizationByID(r.Context(), caller.OrgID)
	if err != nil || org == nil {
		h.writeError(w, http.StatusNotFound, "organization not found")
		return
	}
	proj, err := h.store.EnsureProjectForOrg(r.Context(), org.ID, org.Name)
	if err != nil || proj == nil {
		h.writeError(w, http.StatusInternalServerError, "failed to get project")
		return
	}

	// Generate key pair.
	key, err := generateToken(32)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	secret, err := generateToken(32)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.DefaultCost)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	createdBy := caller.ClerkUserID
	ak, err := h.store.CreateAPIKey(r.Context(), proj.ID, name, key, string(hash), &createdBy)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "failed to create API key: "+err.Error())
		return
	}

	h.writeJSON(w, http.StatusCreated, createAPIKeyResponse{
		ID:        ak.ID,
		Name:      ak.Name,
		Key:       ak.Key,
		Secret:    secret, // plaintext — shown once, never stored
		CreatedAt: ak.CreatedAt,
	})
}

// ── GET /cloud/organizations/{org_id}/api-keys ────────────────────────────────

func (h *CloudHandler) ListAPIKeys(w http.ResponseWriter, r *http.Request) {
	caller := middleware.OrgMemberFromContext(r.Context())

	proj, err := h.store.GetOrganizationProject(r.Context(), caller.OrgID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if proj == nil {
		h.writeJSON(w, http.StatusOK, map[string]any{"api_keys": []any{}})
		return
	}

	keys, err := h.store.ListAPIKeys(r.Context(), proj.ID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	items := make([]listAPIKeyItem, 0, len(keys))
	for _, k := range keys {
		items = append(items, listAPIKeyItem{
			ID: k.ID, Name: k.Name, Key: k.Key,
			Active: k.Active, LastUsedAt: k.LastUsedAt, CreatedAt: k.CreatedAt,
		})
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"api_keys": items})
}

// ── DELETE /cloud/organizations/{org_id}/api-keys/{key_id} ───────────────────

func (h *CloudHandler) RevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	caller := middleware.OrgMemberFromContext(r.Context())
	keyID := r.PathValue("key_id")
	if keyID == "" {
		h.writeError(w, http.StatusBadRequest, "key_id path param required")
		return
	}

	proj, err := h.store.GetOrganizationProject(r.Context(), caller.OrgID)
	if err != nil || proj == nil {
		h.writeError(w, http.StatusNotFound, "project not found")
		return
	}

	rawKey, err := h.store.RevokeAPIKey(r.Context(), proj.ID, keyID)
	if err != nil {
		h.writeError(w, http.StatusNotFound, "api key not found: "+err.Error())
		return
	}

	// Invalidate cache immediately so revocation takes effect before TTL.
	storage.InvalidateAPIKeyCache(r.Context(), h.redisClient, rawKey)

	h.writeJSON(w, http.StatusOK, map[string]string{"status": "revoked", "id": keyID})
}