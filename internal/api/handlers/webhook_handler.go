package handlers

import (
	"encoding/json"
	"io"
	"log"
	"net/http"

	svix "github.com/svix/svix-webhooks/go"
	"github.com/vaultkey/vaultkey/internal/storage"
)

// WebhookHandler handles Clerk webhook events delivered via Svix.
// Endpoint: POST /webhooks/clerk
//
// Verified using the Svix webhook signature. Events processed:
//   - user.created  → upsert clerk_users row
//   - user.updated  → upsert clerk_users row
//   - user.deleted  → soft-delete all org memberships
type WebhookHandler struct {
	store         *storage.Store
	webhookSecret string // CLERK_WEBHOOK_SECRET (whsec_...)
	wh            *svix.Webhook
}

func NewWebhookHandler(store *storage.Store, webhookSecret string) (*WebhookHandler, error) {
	wh, err := svix.NewWebhook(webhookSecret)
	if err != nil {
		return nil, err
	}
	return &WebhookHandler{
		store:         store,
		webhookSecret: webhookSecret,
		wh:            wh,
	}, nil
}

// clerkWebhookEnvelope is the outer shape of every Clerk webhook payload.
type clerkWebhookEnvelope struct {
	Type   string          `json:"type"`
	Data   json.RawMessage `json:"data"`
	Object string          `json:"object"`
}

// clerkUserData is the subset of Clerk's User object we care about.
type clerkUserData struct {
	ID             string              `json:"id"`
	EmailAddresses []clerkEmailAddress `json:"email_addresses"`
	FirstName      string              `json:"first_name"`
	LastName       string              `json:"last_name"`
	ImageURL       string              `json:"image_url"`
}

type clerkEmailAddress struct {
	EmailAddress string `json:"email_address"`
	Primary      bool   `json:"primary"`
}

// clerkDeletedData is the payload for user.deleted events (minimal).
type clerkDeletedData struct {
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
}

// ServeHTTP handles POST /webhooks/clerk
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Read body — Svix needs full bytes for signature verification.
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MB limit
	if err != nil {
		http.Error(w, `{"error":"failed to read body"}`, http.StatusBadRequest)
		return
	}

	// Verify Svix signature. This uses the svix-id, svix-timestamp, and
	// svix-signature headers set by Svix on every delivery.
	if err := h.wh.Verify(body, r.Header); err != nil {
		log.Printf("clerk webhook: signature verification failed: %v", err)
		http.Error(w, `{"error":"invalid webhook signature"}`, http.StatusUnauthorized)
		return
	}

	var env clerkWebhookEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		http.Error(w, `{"error":"invalid payload"}`, http.StatusBadRequest)
		return
	}

	switch env.Type {
	case "user.created", "user.updated":
		h.handleUserUpsert(w, r, env.Data)
	case "user.deleted":
		h.handleUserDeleted(w, r, env.Data)
	default:
		// Unknown event type — acknowledge to prevent Svix retries for events
		// we don't handle, without treating it as an error.
		log.Printf("clerk webhook: unhandled event type %q", env.Type)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *WebhookHandler) handleUserUpsert(w http.ResponseWriter, r *http.Request, data json.RawMessage) {
	var user clerkUserData
	if err := json.Unmarshal(data, &user); err != nil {
		http.Error(w, `{"error":"invalid user data"}`, http.StatusBadRequest)
		return
	}

	if user.ID == "" {
		http.Error(w, `{"error":"user id is required"}`, http.StatusBadRequest)
		return
	}

	// Pick the primary email address, fall back to first.
	email := primaryEmail(user.EmailAddresses)

	cu := &storage.ClerkUser{
		ClerkUserID: user.ID,
		Email:       email,
		FirstName:   user.FirstName,
		LastName:    user.LastName,
		ImageURL:    user.ImageURL,
	}

	if err := h.store.UpsertClerkUser(r.Context(), cu); err != nil {
		log.Printf("clerk webhook: upsert user %s failed: %v", user.ID, err)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	log.Printf("clerk webhook: upserted user %s (%s)", user.ID, email)
	w.WriteHeader(http.StatusNoContent)
}

func (h *WebhookHandler) handleUserDeleted(w http.ResponseWriter, r *http.Request, data json.RawMessage) {
	var deleted clerkDeletedData
	if err := json.Unmarshal(data, &deleted); err != nil {
		http.Error(w, `{"error":"invalid deleted payload"}`, http.StatusBadRequest)
		return
	}

	if deleted.ID == "" {
		http.Error(w, `{"error":"user id is required"}`, http.StatusBadRequest)
		return
	}

	// Soft-delete all org memberships — user's wallets/audit_log are NEVER deleted.
	if err := h.store.DeleteClerkUser(r.Context(), deleted.ID); err != nil {
		log.Printf("clerk webhook: soft-delete user %s failed: %v", deleted.ID, err)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	log.Printf("clerk webhook: soft-deleted memberships for user %s", deleted.ID)
	w.WriteHeader(http.StatusNoContent)
}

// primaryEmail returns the primary email from a list of Clerk email addresses.
// Falls back to the first address if none is marked primary.
func primaryEmail(addrs []clerkEmailAddress) string {
	for _, a := range addrs {
		if a.Primary {
			return a.EmailAddress
		}
	}
	if len(addrs) > 0 {
		return addrs[0].EmailAddress
	}
	return ""
}