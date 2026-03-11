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
	Vault       VaultConfig
	Redis       RedisConfig
	Worker      WorkerConfig
	RPC         RPCConfig
}

type VaultConfig struct {
	Addr      string
	Token     string
	MountPath string
	KeyName   string
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
	// Vault token loading: prefer file over direct env var
	vaultToken := os.Getenv("VAULT_TOKEN")
	if vaultTokenFile := os.Getenv("VAULT_TOKEN_FILE"); vaultTokenFile != "" {
		tokenBytes, err := os.ReadFile(vaultTokenFile)
		if err != nil {
			return nil, fmt.Errorf("read vault token file %s: %w", vaultTokenFile, err)
		}
		vaultToken = strings.TrimSpace(string(tokenBytes))
	}
	if vaultToken == "" {
		return nil, fmt.Errorf("VAULT_TOKEN or VAULT_TOKEN_FILE must be set")
	}

	cfg := &Config{
		Port:        getEnv("PORT", "8080"),
		DatabaseURL: requireEnv("DATABASE_URL"),
		Vault: VaultConfig{
			Addr:      getEnv("VAULT_ADDR", "http://vault:8200"),
			Token:     vaultToken,
			MountPath: getEnv("VAULT_MOUNT_PATH", "transit"),
			KeyName:   getEnv("VAULT_KEY_NAME", "vaultkey-master"),
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

	return cfg, nil
}

// loadEVMEndpoints dynamically loads all EVM_RPC_{CHAIN_ID} environment variables
// This supports both mainnet and testnet configurations without code changes
func loadEVMEndpoints() map[string]string {
	endpoints := make(map[string]string)
	
	// Load from environment - any variable matching EVM_RPC_* pattern
	for _, env := range os.Environ() {
		if strings.HasPrefix(env, "EVM_RPC_") {
			parts := strings.SplitN(env, "=", 2)
			if len(parts) != 2 {
				continue
			}
			key := parts[0]
			value := parts[1]
			
			// Extract chain ID from EVM_RPC_{CHAIN_ID}
			chainID := strings.TrimPrefix(key, "EVM_RPC_")
			if chainID != "" && value != "" {
				endpoints[chainID] = value
			}
		}
	}
	
	// Fallback defaults for mainnet if nothing configured
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