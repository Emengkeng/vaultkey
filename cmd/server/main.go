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
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
	"github.com/vaultkey/vaultkey/config"
	"github.com/vaultkey/vaultkey/internal/api/handlers"
	"github.com/vaultkey/vaultkey/internal/api/middleware"
	"github.com/vaultkey/vaultkey/internal/credits"
	"github.com/vaultkey/vaultkey/internal/cron"
	internalkms "github.com/vaultkey/vaultkey/internal/kms"
	"github.com/vaultkey/vaultkey/internal/nonce"
	"github.com/vaultkey/vaultkey/internal/queue"
	"github.com/vaultkey/vaultkey/internal/ratelimit"
	"github.com/vaultkey/vaultkey/internal/relayer"
	"github.com/vaultkey/vaultkey/internal/rpc"
	"github.com/vaultkey/vaultkey/internal/stablecoin"
	"github.com/vaultkey/vaultkey/internal/storage"
	"github.com/vaultkey/vaultkey/internal/sweep"
	"github.com/vaultkey/vaultkey/internal/testclient"
	"github.com/vaultkey/vaultkey/internal/wallet"
	"github.com/vaultkey/vaultkey/internal/webhook"
	"github.com/vaultkey/vaultkey/internal/worker"

	awscfg "github.com/aws/aws-sdk-go-v2/config"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using environment variables from system")
	}

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
	creditsMgr := credits.New(store.DB())

	registry := stablecoin.NewRegistry(store, redisClient)
	stablecoinSvc := stablecoin.NewService(store, rpcMgr, q, registry)

		// ── Cron (free tier grant) ────────────────────────────────────────────────
	cronRunner := cron.New(creditsMgr, cfg.Cloud.FreeTier.MonthlyCredits)
	go cronRunner.Start(ctx)

	// Run immediately on startup to catch any missed grants (e.g. after downtime).
	// Uses a short-lived context so it doesn't block startup.
	go func() {
		startupCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		cronRunner.RunFreeTierGrantNow(startupCtx)
	}()

	// ── Worker Pool ───────────────────────────────────────────────────────────
	w := worker.New(
		store, q, walletSvc, relayerSvc, webhookSvc, nonceMgr, rpcMgr,
		creditsMgr,
		cfg.Worker.Concurrency,
		cfg.Worker.PollTimeoutSec,
	)
	go func() {
		log.Printf("worker: starting %d workers", cfg.Worker.Concurrency)
		w.Start(ctx)
	}()

	// ── HTTP Handlers ─────────────────────────────────────────────────────────
	h := handlers.New(store, walletSvc, q, rpcMgr, cfg)
	relayerH := handlers.NewRelayerHandler(store, walletSvc, relayerSvc)
	sweepH := handlers.NewSweepHandler(store, sweepSvc)
	stablecoinH := handlers.NewStablecoinHandler(stablecoinSvc)
	adminH := handlers.NewAdminHandler(registry)
	sdkH := handlers.NewSDKHandler(
		cfg, creditsMgr, store, walletSvc, q, rpcMgr, sweepSvc, stablecoinSvc,
	)
	paymentH := handlers.NewPaymentHandler(store, creditsMgr, cfg)
	usageH := handlers.NewUsageHandler(creditsMgr)

	authed := middleware.Auth(store, limiter, redisClient)
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
		registerCloudRoutes(mux, store, cfg, redisClient, paymentH, usageH)
		registerSDKRoutes(mux, store, cfg, redisClient, sdkH, limiter)

		// ── Stripe Webhooks (public — verified via Stripe signature) ───────────
		mux.Handle("POST /webhooks/stripe", http.HandlerFunc(paymentH.StripeWebhook))
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
func registerCloudRoutes(
	mux *http.ServeMux, 
	store *storage.Store, 
	cfg *config.Config, 
	redisClient *redis.Client,
	paymentH *handlers.PaymentHandler,
	usageH *handlers.UsageHandler,
	) {
	cloudH := handlers.NewCloudHandler(store, redisClient, cfg)

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

	registerBillingRoutes(mux, store, cfg, paymentH, usageH, clerkAuth, orgViewer, orgDeveloper)

	// ── Clerk Webhooks (public — verified via Svix signature) ───────────
	webhookH, err := handlers.NewWebhookHandler(store, cfg.Cloud.ClerkWebhookSecret)
	if err != nil {
		log.Fatalf("init clerk webhook handler: %v", err)
	}
	mux.Handle("POST /webhooks/clerk", webhookH)

	log.Println("cloud routes: registered")
}

// ── registerSDKRoutes ─────────────────────────────────────────────────────────
// Registers routes under /sdk/*, protected by SDK auth (project API keys or Clerk JWTs).
func registerSDKRoutes(
	mux *http.ServeMux,
	store *storage.Store,
	cfg *config.Config,
	redisClient *redis.Client,
	sdkH *handlers.SDKHandler,
	limiter *ratelimit.Limiter,
) {
	sdkAuth := middleware.SDKAuth(store, limiter, redisClient)
	sdkAuthed := func(fn http.HandlerFunc) http.Handler {
		return sdkAuth(http.HandlerFunc(fn))
	}
 
	// Build registry — spec drives both the test client and route registration.
	reg := testclient.NewRegistry()
	specs := testclient.SDKRouteSpecs()
 
	// Helper: find spec by method+path, register route, return handler.
	// The spec list in specs.go must stay in sync with the mux.Handle calls below.
	// If a route has no spec, it still works — it just won't appear in the test client.
	find := func(method, path string) testclient.RouteSpec {
		for _, s := range specs {
			if s.Method == method && s.Path == path {
				return s
			}
		}
		return testclient.RouteSpec{Method: method, Path: path, Name: path}
	}
 
	// ── Wallet operations ─────────────────────────────────────────────────────
	mux.Handle("POST /sdk/wallets",
		reg.Register(find("POST", "/sdk/wallets"), sdkAuthed(sdkH.CreateWallet)))
 
	mux.Handle("GET /sdk/wallets/{walletId}",
		reg.Register(find("GET", "/sdk/wallets/{walletId}"), sdkAuthed(sdkH.GetWallet)))
 
	mux.Handle("GET /sdk/users/{userId}/wallets",
		reg.Register(find("GET", "/sdk/users/{userId}/wallets"), sdkAuthed(sdkH.ListUserWallets)))
 
	// ── Signing operations ────────────────────────────────────────────────────
	mux.Handle("POST /sdk/wallets/{walletId}/sign/transaction/evm",
		reg.Register(find("POST", "/sdk/wallets/{walletId}/sign/transaction/evm"),
			sdkAuthed(sdkH.SignEVMTransaction)))
 
	mux.Handle("POST /sdk/wallets/{walletId}/sign/message/evm",
		reg.Register(find("POST", "/sdk/wallets/{walletId}/sign/message/evm"),
			sdkAuthed(sdkH.SignEVMMessage)))
 
	mux.Handle("POST /sdk/wallets/{walletId}/sign/transaction/solana",
		reg.Register(find("POST", "/sdk/wallets/{walletId}/sign/transaction/solana"),
			sdkAuthed(sdkH.SignSolanaTransaction)))
 
	mux.Handle("POST /sdk/wallets/{walletId}/sign/message/solana",
		reg.Register(find("POST", "/sdk/wallets/{walletId}/sign/message/solana"),
			sdkAuthed(sdkH.SignSolanaMessage)))
 
	// ── Free operations ───────────────────────────────────────────────────────
	mux.Handle("GET /sdk/jobs/{jobId}",
		reg.Register(find("GET", "/sdk/jobs/{jobId}"), sdkAuthed(sdkH.GetJob)))
 
	mux.Handle("GET /sdk/wallets/{walletId}/balance",
		reg.Register(find("GET", "/sdk/wallets/{walletId}/balance"), sdkAuthed(sdkH.GetBalance)))
 
	mux.Handle("POST /sdk/wallets/{walletId}/broadcast",
		reg.Register(find("POST", "/sdk/wallets/{walletId}/broadcast"), sdkAuthed(sdkH.Broadcast)))
 
	// ── Sweep ─────────────────────────────────────────────────────────────────
	mux.Handle("POST /sdk/wallets/{walletId}/sweep",
		reg.Register(find("POST", "/sdk/wallets/{walletId}/sweep"), sdkAuthed(sdkH.TriggerSweep)))
 
	// ── Stablecoin ────────────────────────────────────────────────────────────
	mux.Handle("POST /sdk/wallets/{walletId}/stablecoin/transfer/{chainType}",
		reg.Register(find("POST", "/sdk/wallets/{walletId}/stablecoin/transfer/{chainType}"),
			sdkAuthed(sdkH.StablecoinTransfer)))
 
	mux.Handle("GET /sdk/wallets/{walletId}/stablecoin/balance/{chainType}",
		reg.Register(find("GET", "/sdk/wallets/{walletId}/stablecoin/balance/{chainType}"),
			sdkAuthed(sdkH.StablecoinBalance)))
 
	// ── Mount test client (testnet / dev only) ────────────────────────────────
	if testclient.Enabled() {
		testclient.Mount(mux, reg)
		log.Println("test client: mounted at /testclient (ENABLE_TEST_UI=true)")
	}
 
	log.Println("sdk routes: registered")
}

// ── registerBillingRoutes ─────────────────────────────────────────────────────
// Registers billing-related routes under /billing/*, protected by API key auth.
func registerBillingRoutes(
	mux *http.ServeMux,
	store *storage.Store,
	cfg *config.Config,
	paymentH *handlers.PaymentHandler,
	usageH *handlers.UsageHandler,
	clerkAuth func(http.Handler) http.Handler,
	orgViewer func(http.HandlerFunc) http.Handler,
	orgDeveloper func(http.HandlerFunc) http.Handler,
) {
	// Purchase — any org member can initiate
	mux.Handle("POST /cloud/billing/purchase",  orgViewer(paymentH.CreatePaymentIntent))
 
	// Billing history — viewer and above
	mux.Handle("GET /cloud/billing/history",    orgViewer(paymentH.GetBillingHistory))
 
	// Usage stats — developer and above
	mux.Handle("GET /cloud/organizations/{org_id}/usage",    orgDeveloper(usageH.GetUsage))
	mux.Handle("GET /cloud/organizations/{org_id}/credits",  orgViewer(usageH.GetCreditBalance))
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