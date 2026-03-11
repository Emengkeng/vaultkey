package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Port        string
	DatabaseURL string
	KMS         KMSConfig
	Vault       VaultConfig
	GCP         GCPConfig
	AWS         AWSConfig
	Redis       RedisConfig
	Worker      WorkerConfig
	RPC         RPCConfig
}

// KMSConfig controls which KMS backend is active.
// Provider values: "vault" (default), "gcp", "aws"
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
// KeyName is the full resource name:
// projects/{project}/locations/{location}/keyRings/{ring}/cryptoKeys/{key}/cryptoKeyVersions/{version}
type GCPConfig struct {
	KeyName                    string
	CredentialsFile            string // path to service account JSON, empty = ADC
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

func Load() (*Config, error) {
	provider := getEnv("KMS_PROVIDER", "vault")

	cfg := &Config{
		Port:        getEnv("PORT", "8080"),
		DatabaseURL: requireEnv("DATABASE_URL"),
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