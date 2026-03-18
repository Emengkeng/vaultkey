package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type StripeConfig struct {
	SecretKey     string // STRIPE_SECRET_KEY (sk_live_xxx or sk_test_xxx)
	WebhookSecret string // STRIPE_WEBHOOK_SECRET (whsec_xxx)
	PublishableKey string // STRIPE_PUBLISHABLE_KEY (pk_live_xxx) — returned to frontend
}

type FreeTierConfig struct {
	MonthlyCredits int64 // FREE_TIER_MONTHLY_CREDITS, default 1000 (testnet: 500)
}

type Config struct {
	Environment string // "testnet" | "mainnet"
	Port        string
	DatabaseURL string
	AdminToken  string
	SupportEmail string // for user-facing error messages with contact info, e.g. "support@vaultkey.io"
	KMS         KMSConfig
	Vault       VaultConfig
	GCP         GCPConfig
	AWS         AWSConfig
	Redis       RedisConfig
	Worker      WorkerConfig
	RPC         RPCConfig
	Cloud       CloudConfig
}

type KMSConfig struct {
	Provider string
}

type VaultConfig struct {
	Addr      string
	Token     string
	MountPath string
	KeyName   string
}

// GCPConfig holds configuration for Google Cloud KMS.
// Credential resolution order:
//  1. CredentialsJSON (GOOGLE_APPLICATION_CREDENTIALS_JSON) — inline JSON
//  2. CredentialsFile (GOOGLE_APPLICATION_CREDENTIALS) — path to key file
//  3. ADC — automatic on GCP infrastructure (Cloud Run, GKE)
type GCPConfig struct {
	KeyName         string
	CredentialsJSON string
	CredentialsFile string
}

// AWSConfig holds configuration for AWS KMS.
// KeyID accepts key ID, key ARN, alias name, or alias ARN.
type AWSConfig struct {
	KeyID  string
	Region string
}

type RedisConfig struct {
	Addr     string
	Password string
	DB       int
}

type WorkerConfig struct {
	Concurrency    int
	PollTimeoutSec int
}

type RPCConfig struct {
	EVMEndpoints   map[string]string
	SolanaEndpoint string
}

// CloudConfig holds all Clerk / SaaS multi-tenant configuration.
// Only required when EnableCloudFeatures = true.
type CloudConfig struct {
	EnableCloudFeatures bool

	// Clerk credentials — all required when cloud is enabled.
	ClerkSecretKey      string
	ClerkPublishableKey string
	ClerkWebhookSecret  string

	Stripe      StripeConfig
	FreeTier   FreeTierConfig

	// Testnet-specific limits (only used when Environment="testnet")
    MaxProjectsPerOrg  int // e.g., 2
    MaxMembersPerOrg   int // e.g., 3
    MaxAPIKeysPerProject int // e.g., 3
}

func Load() (*Config, error) {
	provider := getEnv("KMS_PROVIDER", "vault")

	// ADMIN_TOKEN is required. Without it the /admin/* routes would be unprotected.
	adminToken := os.Getenv("ADMIN_TOKEN")
	if adminToken == "" {
		return nil, fmt.Errorf("ADMIN_TOKEN env var is required — set a strong random value to protect admin endpoints")
	}

	env := getEnv("ENVIRONMENT", "mainnet")
	enableCloud := getEnvBool("ENABLE_CLOUD_FEATURES", false)

	cloudCfg := CloudConfig{
		EnableCloudFeatures: enableCloud,
	}

	if env == "testnet" && enableCloud {
        cloudCfg.MaxProjectsPerOrg = getEnvInt("TESTNET_MAX_PROJECTS", 2)
        cloudCfg.MaxMembersPerOrg = getEnvInt("TESTNET_MAX_MEMBERS", 3)
        cloudCfg.MaxAPIKeysPerProject = getEnvInt("TESTNET_MAX_API_KEYS", 3)
    }

	if enableCloud {
		clerkSecret := os.Getenv("CLERK_SECRET_KEY")
		clerkPublishable := os.Getenv("CLERK_PUBLISHABLE_KEY")
		clerkWebhook := os.Getenv("CLERK_WEBHOOK_SECRET")

		stripeSecret := os.Getenv("STRIPE_SECRET_KEY")
		stripeWebhook := os.Getenv("STRIPE_WEBHOOK_SECRET")
		stripePublishable := os.Getenv("STRIPE_PUBLISHABLE_KEY")

		if clerkSecret == "" {
			return nil, fmt.Errorf("CLERK_SECRET_KEY is required when ENABLE_CLOUD_FEATURES=true")
		}
		if clerkPublishable == "" {
			return nil, fmt.Errorf("CLERK_PUBLISHABLE_KEY is required when ENABLE_CLOUD_FEATURES=true")
		}
		if clerkWebhook == "" {
			return nil, fmt.Errorf("CLERK_WEBHOOK_SECRET is required when ENABLE_CLOUD_FEATURES=true")
		}

		if stripeSecret == "" {
			return nil, fmt.Errorf("STRIPE_SECRET_KEY is required when ENABLE_CLOUD_FEATURES=true")
		}
		if stripeWebhook == "" {
			return nil, fmt.Errorf("STRIPE_WEBHOOK_SECRET is required when ENABLE_CLOUD_FEATURES=true")
		}

		cloudCfg.ClerkSecretKey = clerkSecret
		cloudCfg.ClerkPublishableKey = clerkPublishable
		cloudCfg.ClerkWebhookSecret = clerkWebhook

		cloudCfg.Stripe = StripeConfig{
			SecretKey:      stripeSecret,
			WebhookSecret:  stripeWebhook,
			PublishableKey: stripePublishable,
		}

		// Free tier grant amount (lower on testnet):
		defaultGrant := int64(1000)
		if env == "testnet" {
			defaultGrant = 500
		}
		cloudCfg.FreeTier = FreeTierConfig{
			MonthlyCredits: int64(getEnvInt("FREE_TIER_MONTHLY_CREDITS", int(defaultGrant))),
		}
	}

	cfg := &Config{
		Port:        getEnv("PORT", "8080"),
		DatabaseURL: requireEnv("DATABASE_URL"),
		AdminToken:  adminToken,
		SupportEmail: getEnv("PLARTFORM_SUPPORT_EMAIL", "support@vaultkeyio.com"),
		KMS: KMSConfig{
			Provider: provider,
		},
		Redis: RedisConfig{
			Addr:     getEnv("REDIS_ADDR", "redis:6379"),
			Password: getEnv("REDIS_PASSWORD", ""),
			DB:       getEnvInt("REDIS_DB", 0),
		},
		Worker: WorkerConfig{
			Concurrency:    getEnvInt("WORKER_CONCURRENCY", 10),
			PollTimeoutSec: getEnvInt("WORKER_POLL_TIMEOUT_SEC", 5),
		},
		RPC: RPCConfig{
			EVMEndpoints:   loadEVMEndpoints(),
			SolanaEndpoint: getEnv("SOLANA_RPC_URL", "https://api.mainnet-beta.solana.com"),
		},
		Environment: env,
		Cloud: cloudCfg,
	}

	switch provider {
	case "vault":
		vaultToken := os.Getenv("VAULT_TOKEN")
		if vaultTokenFile := os.Getenv("VAULT_TOKEN_FILE"); vaultTokenFile != "" {
			tokenBytes, err := os.ReadFile(vaultTokenFile)
			if err != nil {
				return nil, fmt.Errorf("read vault token file %s: %w", vaultTokenFile, err)
			}
			vaultToken = strings.TrimSpace(string(tokenBytes))
		}
		if vaultToken == "" {
			return nil, fmt.Errorf("KMS_PROVIDER=vault requires VAULT_TOKEN or VAULT_TOKEN_FILE")
		}
		cfg.Vault = VaultConfig{
			Addr:      getEnv("VAULT_ADDR", "http://vault:8200"),
			Token:     vaultToken,
			MountPath: getEnv("VAULT_MOUNT_PATH", "transit"),
			KeyName:   getEnv("VAULT_KEY_NAME", "vaultkey-master"),
		}

	case "gcp":
		keyName := os.Getenv("GCP_KMS_KEY_NAME")
		if keyName == "" {
			return nil, fmt.Errorf("KMS_PROVIDER=gcp requires GCP_KMS_KEY_NAME")
		}
		cfg.GCP = GCPConfig{
			KeyName:         keyName,
			CredentialsJSON: getEnv("GOOGLE_APPLICATION_CREDENTIALS_JSON", ""),
			CredentialsFile: getEnv("GOOGLE_APPLICATION_CREDENTIALS", ""),
		}

	case "aws":
		keyID := os.Getenv("AWS_KMS_KEY_ID")
		if keyID == "" {
			return nil, fmt.Errorf("KMS_PROVIDER=aws requires AWS_KMS_KEY_ID")
		}
		cfg.AWS = AWSConfig{
			KeyID:  keyID,
			Region: getEnv("AWS_REGION", "us-east-1"),
		}

	default:
		return nil, fmt.Errorf("unknown KMS_PROVIDER %q — valid values: vault, gcp, aws", provider)
	}

	return cfg, nil
}

func loadEVMEndpoints() map[string]string {
	endpoints := make(map[string]string)

	for _, env := range os.Environ() {
		if strings.HasPrefix(env, "EVM_RPC_") {
			parts := strings.SplitN(env, "=", 2)
			if len(parts) != 2 {
				continue
			}
			chainID := strings.TrimPrefix(parts[0], "EVM_RPC_")
			if chainID != "" && parts[1] != "" {
				endpoints[chainID] = parts[1]
			}
		}
	}

	if len(endpoints) == 0 {
		endpoints = map[string]string{
			"1":     "https://cloudflare-eth.com",
			"137":   "https://polygon-rpc.com",
			"42161": "https://arb1.arbitrum.io/rpc",
			"8453":  "https://mainnet.base.org",
			"10":    "https://mainnet.optimism.io",
		}
	}

	return endpoints
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("required env var %s is not set", key))
	}
	return v
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func getEnvBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}