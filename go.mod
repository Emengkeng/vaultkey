module github.com/vaultkey/vaultkey

go 1.22

require (
	github.com/ethereum/go-ethereum v1.13.14
	github.com/lib/pq v1.10.9
	github.com/mr-tron/base58 v1.2.0
	github.com/redis/go-redis/v9 v9.5.1
	github.com/gagliardetto/solana-go v1.12.0
	github.com/testcontainers/testcontainers-go/modules/postgres v0.31.0

	// GCP KMS
	cloud.google.com/go/kms v1.15.0
	google.golang.org/api v0.169.0

	// AWS KMS (SDK v2)
	github.com/aws/aws-sdk-go-v2 v1.26.0
	github.com/aws/aws-sdk-go-v2/config v1.27.0
	github.com/aws/aws-sdk-go-v2/service/kms v1.30.0

	golang.org/x/crypto v0.21.0
)
