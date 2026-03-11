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
	"github.com/vaultkey/vaultkey/internal/storage"
	"github.com/vaultkey/vaultkey/internal/wallet"
	"github.com/vaultkey/vaultkey/internal/webhook"
	"github.com/vaultkey/vaultkey/internal/worker"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	// ── Storage ──────────────────────────────────────────────
	store, err := storage.New(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer store.Close()

	// ── KMS (Vault) ──────────────────────────────────────────
	vaultKMS := internalkms.NewVault(cfg.Vault.Addr, cfg.Vault.Token, cfg.Vault.MountPath, cfg.Vault.KeyName)
	if err := vaultKMS.Health(context.Background()); err != nil {
		log.Fatalf("vault health check failed: %v\nensure Vault is running and unsealed", err)
	}
	log.Println("vault: connected")

	// ── Redis + Queue ────────────────────────────────────────
	q, err := queue.New(cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB)
	if err != nil {
		log.Fatalf("connect redis: %v", err)
	}
	defer q.Close()
	log.Println("redis: connected")

	// ── Rate Limiter ─────────────────────────────────────────
	redisClient := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})
	limiter := ratelimit.New(redisClient)

	// ── Services ─────────────────────────────────────────────
	walletSvc := wallet.NewService(vaultKMS)
	rpcMgr := rpc.NewManager(cfg.RPC.EVMEndpoints, cfg.RPC.SolanaEndpoint)
	webhookSvc := webhook.New()
	nonceMgr := nonce.New(redisClient)
	relayerSvc := relayer.New(store, walletSvc, rpcMgr, nonceMgr)

	// ── Worker Pool ──────────────────────────────────────────
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := worker.New(store, q, walletSvc, relayerSvc, webhookSvc, cfg.Worker.Concurrency, cfg.Worker.PollTimeoutSec)
	go func() {
		log.Printf("worker: starting %d workers", cfg.Worker.Concurrency)
		w.Start(ctx)
	}()

	// ── HTTP Handlers ────────────────────────────────────────
	h := handlers.New(store, walletSvc, q, rpcMgr)
	relayerH := handlers.NewRelayerHandler(store, walletSvc, relayerSvc)
	authed := middleware.Auth(store, limiter)

	mux := http.NewServeMux()

	// Public
	mux.HandleFunc("POST /projects", h.CreateProject)
	mux.HandleFunc("GET /health", healthHandler(vaultKMS, q))

	// Authenticated
	mux.Handle("PATCH /project/webhook", authed(http.HandlerFunc(h.UpdateWebhook)))

	// Relayer wallet management
	mux.Handle("POST /projects/relayer", authed(http.HandlerFunc(relayerH.RegisterRelayer)))
	mux.Handle("GET /projects/relayer", authed(http.HandlerFunc(relayerH.GetRelayerInfo)))
	mux.Handle("GET /projects/relayers", authed(http.HandlerFunc(relayerH.ListRelayers)))
	mux.Handle("DELETE /projects/relayer/{relayerId}", authed(http.HandlerFunc(relayerH.DeactivateRelayer)))

	mux.Handle("POST /wallets", authed(http.HandlerFunc(h.CreateWallet)))
	mux.Handle("GET /wallets/{walletId}", authed(http.HandlerFunc(h.GetWallet)))
	mux.Handle("GET /users/{userId}/wallets", authed(http.HandlerFunc(h.ListUserWallets)))

	// Async signing - returns job_id immediately
	mux.Handle("POST /wallets/{walletId}/sign/transaction/evm", authed(http.HandlerFunc(h.SubmitSignEVMTransaction)))
	mux.Handle("POST /wallets/{walletId}/sign/message/evm", authed(http.HandlerFunc(h.SubmitSignEVMMessage)))
	mux.Handle("POST /wallets/{walletId}/sign/transaction/solana", authed(http.HandlerFunc(h.SubmitSignSolanaTransaction)))
	mux.Handle("POST /wallets/{walletId}/sign/message/solana", authed(http.HandlerFunc(h.SubmitSignSolanaMessage)))

	// Job status polling (dev can poll while waiting for webhook)
	mux.Handle("GET /jobs/{jobId}", authed(http.HandlerFunc(h.GetJob)))

	// Balance + broadcast
	mux.Handle("GET /wallets/{walletId}/balance", authed(http.HandlerFunc(h.GetBalance)))
	mux.Handle("POST /wallets/{walletId}/broadcast", authed(http.HandlerFunc(h.Broadcast)))

	// ── HTTP Server ──────────────────────────────────────────
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

	cancel() // stop workers

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("shutdown error: %v", err)
	}

	log.Println("stopped")
}

func healthHandler(kms internalkms.KMS, q *queue.Queue) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vaultErr := kms.Health(r.Context())
		redisErr := q.Health(r.Context())

		if vaultErr != nil || redisErr != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusOK)
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"vault":"%v","redis":"%v"}`,
			healthStr(vaultErr), healthStr(redisErr))
	}
}

func healthStr(err error) string {
	if err == nil {
		return "ok"
	}
	return err.Error()
}
