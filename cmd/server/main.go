package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/clerk/clerk-sdk-go/v2"
	"github.com/redis/go-redis/v9"
	"github.com/vaultkey/vaultkey/config"
	"github.com/vaultkey/vaultkey/internal/api/handlers"
	"github.com/vaultkey/vaultkey/internal/api/middleware"
	internalkms "github.com/vaultkey/vaultkey/internal/kms"
	"github.com/vaultkey/vaultkey/internal/nonce"
	"github.com/vaultkey/vaultkey/internal/queue"
	"github.com/vaultkey/vaultkey/internal/ratelimit"
	"github.com/vaultkey/vaultkey/internal/relayer"
	"github.com/vaultkey/vaultkey/internal/rpc"
	"github.com/vaultkey/vaultkey/internal/stablecoin"
	"github.com/vaultkey/vaultkey/internal/storage"
	"github.com/vaultkey/vaultkey/internal/sweep"
	"github.com/vaultkey/vaultkey/internal/wallet"
	"github.com/vaultkey/vaultkey/internal/webhook"
	"github.com/vaultkey/vaultkey/internal/worker"

	awscfg "github.com/aws/aws-sdk-go-v2/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ── Clerk SDK init (cloud mode only) ─────────────────────────────────────
	if cfg.Cloud.EnableCloudFeatures {
		clerk.SetKey(cfg.Cloud.ClerkSecretKey)
		log.Println("clerk: initialized (cloud mode enabled)")
	}

	// ── KMS ──────────────────────────────────────────────────────────────────
	kmsBackend, err := buildKMS(ctx, cfg)
	if err != nil {
		log.Fatalf("init kms: %v", err)
	}
	if err := kmsBackend.Health(ctx); err != nil {
		log.Fatalf("kms health check failed: %v", err)
	}
	log.Printf("kms: connected (provider=%s)", cfg.KMS.Provider)

	// ── Storage ───────────────────────────────────────────────────────────────
	store, err := storage.New(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer store.Close()

	// ── Redis ─────────────────────────────────────────────────────────────────
	q, err := queue.New(cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB)
	if err != nil {
		log.Fatalf("connect redis: %v", err)
	}
	defer q.Close()
	log.Println("redis: connected")

	// Shared Redis client — rate limiter, nonce manager, and registry cache.
	redisClient := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})
	limiter := ratelimit.New(redisClient)
	nonceMgr := nonce.New(redisClient)

	// ── Services ──────────────────────────────────────────────────────────────
	walletSvc := wallet.NewService(kmsBackend)
	rpcMgr := rpc.NewManager(cfg.RPC.EVMEndpoints, cfg.RPC.SolanaEndpoint)
	webhookSvc := webhook.New()
	relayerSvc := relayer.New(store, walletSvc, rpcMgr, nonceMgr)
	sweepSvc := sweep.New(store, walletSvc, rpcMgr, q)

	registry := stablecoin.NewRegistry(store, redisClient)
	stablecoinSvc := stablecoin.NewService(store, rpcMgr, q, registry)

	// ── Worker Pool ───────────────────────────────────────────────────────────
	w := worker.New(
		store, q, walletSvc, relayerSvc, webhookSvc, nonceMgr, rpcMgr,
		cfg.Worker.Concurrency,
		cfg.Worker.PollTimeoutSec,
	)
	go func() {
		log.Printf("worker: starting %d workers", cfg.Worker.Concurrency)
		w.Start(ctx)
	}()

	// ── HTTP Handlers ─────────────────────────────────────────────────────────
	h := handlers.New(store, walletSvc, q, rpcMgr)
	relayerH := handlers.NewRelayerHandler(store, walletSvc, relayerSvc)
	sweepH := handlers.NewSweepHandler(store, sweepSvc)
	stablecoinH := handlers.NewStablecoinHandler(stablecoinSvc)
	adminH := handlers.NewAdminHandler(registry)

	authed := middleware.Auth(store, limiter)
	admin := middleware.AdminAuth(cfg.AdminToken)

	mux := http.NewServeMux()

	// ── Public ────────────────────────────────────────────────────────────────
	mux.HandleFunc("POST /projects", h.CreateProject)
	mux.HandleFunc("GET /health", healthHandler(kmsBackend, q))

	// ── Project-authed (API key) ───────────────────────────────────────────────
	mux.Handle("PATCH /project/webhook", authed(http.HandlerFunc(h.UpdateWebhook)))

	mux.Handle("POST /projects/relayer", authed(http.HandlerFunc(relayerH.RegisterRelayer)))
	mux.Handle("GET /projects/relayer", authed(http.HandlerFunc(relayerH.GetRelayerInfo)))
	mux.Handle("GET /projects/relayers", authed(http.HandlerFunc(relayerH.ListRelayers)))
	mux.Handle("DELETE /projects/relayer/{relayerId}", authed(http.HandlerFunc(relayerH.DeactivateRelayer)))

	mux.Handle("POST /projects/master-wallet", authed(http.HandlerFunc(sweepH.ProvisionMasterWallet)))
	mux.Handle("GET /projects/master-wallet", authed(http.HandlerFunc(sweepH.GetMasterWallet)))
	mux.Handle("GET /projects/master-wallets", authed(http.HandlerFunc(sweepH.ListMasterWallets)))
	mux.Handle("PATCH /projects/master-wallet/{configId}", authed(http.HandlerFunc(sweepH.UpdateSweepConfig)))

	mux.Handle("POST /wallets", authed(http.HandlerFunc(h.CreateWallet)))
	mux.Handle("GET /wallets/{walletId}", authed(http.HandlerFunc(h.GetWallet)))
	mux.Handle("GET /users/{userId}/wallets", authed(http.HandlerFunc(h.ListUserWallets)))

	mux.Handle("POST /wallets/{walletId}/sign/transaction/evm", authed(http.HandlerFunc(h.SubmitSignEVMTransaction)))
	mux.Handle("POST /wallets/{walletId}/sign/message/evm", authed(http.HandlerFunc(h.SubmitSignEVMMessage)))
	mux.Handle("POST /wallets/{walletId}/sign/transaction/solana", authed(http.HandlerFunc(h.SubmitSignSolanaTransaction)))
	mux.Handle("POST /wallets/{walletId}/sign/message/solana", authed(http.HandlerFunc(h.SubmitSignSolanaMessage)))

	mux.Handle("POST /wallets/{walletId}/stablecoin/transfer/{chainType}", authed(http.HandlerFunc(stablecoinH.Transfer)))
	mux.Handle("GET /wallets/{walletId}/stablecoin/balance/{chainType}", authed(http.HandlerFunc(stablecoinH.Balance)))

	mux.Handle("POST /wallets/{walletId}/sweep", authed(http.HandlerFunc(sweepH.TriggerSweep)))
	mux.Handle("GET /jobs/{jobId}", authed(http.HandlerFunc(h.GetJob)))
	mux.Handle("GET /wallets/{walletId}/balance", authed(http.HandlerFunc(h.GetBalance)))
	mux.Handle("POST /wallets/{walletId}/broadcast", authed(http.HandlerFunc(h.Broadcast)))

	// ── Admin (X-Admin-Token) ──────────────────────────────────────────────────
	mux.Handle("GET /admin/stablecoins", admin(http.HandlerFunc(adminH.ListTokens)))
	mux.Handle("POST /admin/stablecoins", admin(http.HandlerFunc(adminH.UpsertToken)))
	mux.Handle("DELETE /admin/stablecoins/{tokenId}", admin(http.HandlerFunc(adminH.DisableToken)))

	// ── Cloud routes (Clerk JWT auth) — only registered when cloud is enabled ──
	if cfg.Cloud.EnableCloudFeatures {
		registerCloudRoutes(mux, store, cfg)
	}

	// ── HTTP Server ───────────────────────────────────────────────────────────
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%s", cfg.Port),
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("api: listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	<-quit
	log.Println("shutting down...")
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("shutdown error: %v", err)
	}
	log.Println("stopped")
}

// registerCloudRoutes wires all /cloud/* and /webhooks/* routes.
// Only called when ENABLE_CLOUD_FEATURES=true.
func registerCloudRoutes(mux *http.ServeMux, store *storage.Store, cfg *config.Config) {
	cloudH := handlers.NewCloudHandler(store)

	// Clerk JWT middleware (applied individually or composed per route).
	clerkAuth := middleware.ClerkAuth()

	// Convenience: clerk-authed handler wrapper.
	clerkAuthed := func(fn http.HandlerFunc) http.Handler {
		return clerkAuth(http.HandlerFunc(fn))
	}

	// OrgAuthz requires ClerkAuth to have run first (extracts clerk_user_id).
	// These helpers compose both middlewares.
	orgViewer := func(fn http.HandlerFunc) http.Handler {
		return clerkAuth(middleware.OrgAuthz(store, "viewer")(http.HandlerFunc(fn)))
	}
	orgDeveloper := func(fn http.HandlerFunc) http.Handler {
		return clerkAuth(middleware.OrgAuthz(store, "developer")(http.HandlerFunc(fn)))
	}
	orgAdmin := func(fn http.HandlerFunc) http.Handler {
		return clerkAuth(middleware.OrgAuthz(store, "admin")(http.HandlerFunc(fn)))
	}
	orgOwner := func(fn http.HandlerFunc) http.Handler {
		return clerkAuth(middleware.OrgAuthz(store, "owner")(http.HandlerFunc(fn)))
	}

	// ── Onboarding ─────────────────────────────────────────────────────
	mux.Handle("POST /cloud/onboarding", clerkAuthed(cloudH.Onboarding))

	// ── Organizations ──────────────────────────────────────────────────
	mux.Handle("GET /cloud/organizations", clerkAuthed(cloudH.ListOrganizations))
	mux.Handle("GET /cloud/organizations/{org_id}", orgViewer(cloudH.GetOrganization))
	mux.Handle("PATCH /cloud/organizations/{org_id}", orgAdmin(cloudH.UpdateOrganization))
	mux.Handle("DELETE /cloud/organizations/{org_id}", orgOwner(cloudH.DeleteOrganization))

	// ── Members ────────────────────────────────────────────────────────
	mux.Handle("GET /cloud/organizations/{org_id}/members", orgViewer(cloudH.ListMembers))
	mux.Handle("PATCH /cloud/organizations/{org_id}/members/{clerk_user_id}", orgAdmin(cloudH.UpdateMember))
	mux.Handle("DELETE /cloud/organizations/{org_id}/members/{clerk_user_id}", orgAdmin(cloudH.RemoveMember))

	// ── Invites ────────────────────────────────────────────────────────
	mux.Handle("POST /cloud/organizations/{org_id}/invites", orgAdmin(cloudH.CreateInvite))
	mux.Handle("GET /cloud/organizations/{org_id}/invites", orgViewer(cloudH.ListInvites))
	mux.Handle("DELETE /cloud/organizations/{org_id}/invites/{token}", orgAdmin(cloudH.RevokeInvite))

	// Accept invite — requires Clerk auth but not org membership (they're joining).
	mux.Handle("POST /cloud/invites/{token}/accept", clerkAuthed(cloudH.AcceptInvite))

	// ── API Keys ───────────────────────────────────────────────────────
	// Admin can create/revoke; developer/viewer can list (read-only).
	mux.Handle("POST /cloud/organizations/{org_id}/api-keys", orgAdmin(cloudH.CreateAPIKey))
	mux.Handle("GET /cloud/organizations/{org_id}/api-keys", orgDeveloper(cloudH.ListAPIKeys))
	mux.Handle("DELETE /cloud/organizations/{org_id}/api-keys/{key_id}", orgAdmin(cloudH.RevokeAPIKey))

	// ── Clerk Webhooks (public — verified via Svix signature) ───────────
	webhookH, err := handlers.NewWebhookHandler(store, cfg.Cloud.ClerkWebhookSecret)
	if err != nil {
		log.Fatalf("init clerk webhook handler: %v", err)
	}
	mux.Handle("POST /webhooks/clerk", webhookH)

	log.Println("cloud routes: registered")
}

func buildKMS(ctx context.Context, cfg *config.Config) (internalkms.KMS, error) {
	switch cfg.KMS.Provider {
	case "vault":
		return internalkms.NewVault(cfg.Vault.Addr, cfg.Vault.Token, cfg.Vault.MountPath, cfg.Vault.KeyName), nil
	case "gcp":
		return internalkms.NewGCP(ctx, cfg.GCP.KeyName, internalkms.GCPOptions{
			CredentialsJSON: cfg.GCP.CredentialsJSON,
			CredentialsFile: cfg.GCP.CredentialsFile,
		})
	case "aws":
		return internalkms.NewAWS(ctx, cfg.AWS.KeyID, awscfg.WithRegion(cfg.AWS.Region))
	default:
		return nil, fmt.Errorf("unknown KMS provider: %s", cfg.KMS.Provider)
	}
}

func healthHandler(kms internalkms.KMS, q *queue.Queue) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kmsErr := kms.Health(r.Context())
		redisErr := q.Health(r.Context())
		if kmsErr != nil || redisErr != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusOK)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"kms":"%v","redis":"%v"}`, healthStr(kmsErr), healthStr(redisErr))
	}
}

func healthStr(err error) string {
	if err == nil {
		return "ok"
	}
	return err.Error()
}