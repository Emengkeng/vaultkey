package testclient

// SDKRouteSpecs returns the full list of SDK route specs.
// Called from registerSDKRoutes in main.go.
// When you add a new SDK route, add its spec here.
func SDKRouteSpecs() []RouteSpec {
	return []RouteSpec{

		// ── Wallets ───────────────────────────────────────────────────────────

		{
			Method:      "POST",
			Path:        "/sdk/wallets",
			Group:       "Wallets",
			Name:        "Create Wallet",
			Description: "Creates a new custodial wallet for a user. Deducts 10 credits.",
			Credits:     10,
			Body: []FieldSpec{
				{Name: "user_id", Type: "string", Required: true, Description: "Your internal user identifier"},
				{Name: "chain_type", Type: "string", Required: true, Description: "evm or solana", Enum: []any{"evm", "solana"}},
				{Name: "label", Type: "string", Required: false, Description: "Optional label for the wallet"},
			},
			ResultFields: []string{"id", "address", "chain_type", "user_id"},
		},
		{
			Method:      "GET",
			Path:        "/sdk/wallets/{walletId}",
			Group:       "Wallets",
			Name:        "Get Wallet",
			Description: "Fetches a wallet by ID. Free.",
			Credits:     0,
			PathParams: []FieldSpec{
				{Name: "walletId", Type: "string", Required: true, Description: "Wallet ID"},
			},
			ResultFields: []string{"id", "address", "chain_type", "user_id", "label"},
		},
		{
			Method:      "GET",
			Path:        "/sdk/users/{userId}/wallets",
			Group:       "Wallets",
			Name:        "List User Wallets",
			Description: "Lists all wallets for a user. Free.",
			Credits:     0,
			PathParams: []FieldSpec{
				{Name: "userId", Type: "string", Required: true, Description: "Your internal user identifier"},
			},
			ResultFields: []string{"wallets"},
		},

		// ── Signing ───────────────────────────────────────────────────────────

		// {
		// 	Method:      "POST",
		// 	Path:        "/sdk/wallets/{walletId}/sign/transaction/evm",
		// 	Group:       "Signing",
		// 	Name:        "Sign EVM Transaction",
		// 	Description: "Signs an EVM transaction asynchronously. Deducts 10 credits.",
		// 	Credits:     10,
		// 	Async:       true,
		// 	PathParams: []FieldSpec{
		// 		{Name: "walletId", Type: "string", Required: true, Description: "Wallet ID"},
		// 	},
		// 	Body: []FieldSpec{
		// 		{Name: "idempotency_key", Type: "string", Required: false, Description: "Deduplication key — safe to retry"},
		// 		{Name: "gasless", Type: "boolean", Required: false, Description: "Use relayer to pay gas", Default: false},
		// 		{Name: "payload.to", Type: "string", Required: true, Description: "Recipient address (0x...)"},
		// 		{Name: "payload.value", Type: "string", Required: true, Description: "Amount in wei (hex), e.g. 0x0", Default: "0x0"},
		// 		{Name: "payload.data", Type: "string", Required: false, Description: "Contract calldata (hex)", Default: "0x"},
		// 		{Name: "payload.gas_limit", Type: "number", Required: false, Description: "Gas limit (omit for gasless)", Default: 21000},
		// 		{Name: "payload.gas_price", Type: "string", Required: false, Description: "Gas price hex (omit for gasless)", Default: "0x3B9ACA00"},
		// 		{Name: "payload.chain_id", Type: "number", Required: true, Description: "Chain ID: 137=Polygon, 42161=Arbitrum, 8453=Base, 10=Optimism", Default: 137},
		// 	},
		// 	ResultFields: []string{"job_id", "status", "result"},
		// },
		{
			Method:      "POST",
			Path:        "/sdk/wallets/{walletId}/sign/message/evm",
			Group:       "Signing",
			Name:        "Sign EVM Message",
			Description: "Signs an EIP-191 message. Deducts 5 credits.",
			Credits:     5,
			Async:       true,
			PathParams: []FieldSpec{
				{Name: "walletId", Type: "string", Required: true, Description: "Wallet ID"},
			},
			Body: []FieldSpec{
				{Name: "idempotency_key", Type: "string", Required: false, Description: "Deduplication key"},
				{Name: "payload.message", Type: "string", Required: true, Description: "Message to sign", Default: "Hello from VaultKey"},
			},
			ResultFields: []string{"job_id", "status", "result"},
		},
		// {
		// 	Method:      "POST",
		// 	Path:        "/sdk/wallets/{walletId}/sign/transaction/solana",
		// 	Group:       "Signing",
		// 	Name:        "Sign Solana Transaction",
		// 	Description: "Signs a Solana transaction asynchronously. Deducts 10 credits.",
		// 	Credits:     10,
		// 	Async:       true,
		// 	PathParams: []FieldSpec{
		// 		{Name: "walletId", Type: "string", Required: true, Description: "Wallet ID"},
		// 	},
		// 	Body: []FieldSpec{
		// 		{Name: "idempotency_key", Type: "string", Required: false, Description: "Deduplication key"},
		// 		{Name: "gasless", Type: "boolean", Required: false, Description: "Use relayer as fee payer", Default: true},
		// 		{Name: "payload.to", Type: "string", Required: true, Description: "Recipient base58 address"},
		// 		{Name: "payload.amount", Type: "number", Required: true, Description: "Amount in lamports", Default: 1000000},
		// 		{Name: "payload.token_mint", Type: "string", Required: false, Description: "SPL token mint (omit for native SOL)"},
		// 		{Name: "payload.source_token_account", Type: "string", Required: false, Description: "Source ATA (SPL only)"},
		// 		{Name: "payload.dest_token_account", Type: "string", Required: false, Description: "Destination ATA (SPL only)"},
		// 	},
		// 	ResultFields: []string{"job_id", "status", "result"},
		// },
		{
			Method:      "POST",
			Path:        "/sdk/wallets/{walletId}/sign/message/solana",
			Group:       "Signing",
			Name:        "Sign Solana Message",
			Description: "Signs an arbitrary message with a Solana wallet. Deducts 5 credits.",
			Credits:     5,
			Async:       true,
			PathParams: []FieldSpec{
				{Name: "walletId", Type: "string", Required: true, Description: "Wallet ID"},
			},
			Body: []FieldSpec{
				{Name: "idempotency_key", Type: "string", Required: false, Description: "Deduplication key"},
				{Name: "payload.message", Type: "string", Required: true, Description: "Message to sign", Default: "Hello from VaultKey"},
			},
			ResultFields: []string{"job_id", "status", "result"},
		},

		// ── Stablecoin ────────────────────────────────────────────────────────

		{
			Method:      "POST",
			Path:        "/sdk/wallets/{walletId}/stablecoin/transfer/{chainType}",
			Group:       "Stablecoin",
			Name:        "Transfer Stablecoin",
			Description: "Transfers USDC or USDT. Deducts 10 credits.",
			Credits:     10,
			Async:       true,
			PathParams: []FieldSpec{
				{Name: "walletId", Type: "string", Required: true, Description: "Wallet ID"},
				{Name: "chainType", Type: "string", Required: true, Description: "evm or solana", Enum: []any{"evm", "solana"}},
			},
			Body: []FieldSpec{
				{Name: "token", Type: "string", Required: true, Description: "usdc or usdt", Enum: []any{"usdc", "usdt"}, Default: "usdc"},
				{Name: "to", Type: "string", Required: true, Description: "Recipient address"},
				{Name: "amount", Type: "string", Required: true, Description: "Human-readable amount e.g. 50.00", Default: "1.00"},
				{Name: "chain_id", Type: "string", Required: false, Description: "EVM chain ID (omit for Solana)", Default: "137"},
				{Name: "gasless", Type: "boolean", Required: false, Description: "Relayer pays gas (EVM only)", Default: true},
				{Name: "idempotency_key", Type: "string", Required: false, Description: "Deduplication key"},
			},
			ResultFields: []string{"job_id", "status"},
		},
		{
			Method:      "GET",
			Path:        "/sdk/wallets/{walletId}/stablecoin/balance/{chainType}",
			Group:       "Stablecoin",
			Name:        "Get Stablecoin Balance",
			Description: "Returns token balance for a wallet. Free.",
			Credits:     0,
			PathParams: []FieldSpec{
				{Name: "walletId", Type: "string", Required: true, Description: "Wallet ID"},
				{Name: "chainType", Type: "string", Required: true, Description: "evm or solana", Enum: []any{"evm", "solana"}},
			},
			QueryParams: []FieldSpec{
				{Name: "token", Type: "string", Required: true, Description: "usdc or usdt", Enum: []any{"usdc", "usdt"}, Default: "usdc"},
				{Name: "chain_id", Type: "string", Required: false, Description: "EVM chain ID (omit for Solana)", Default: "137"},
			},
			ResultFields: []string{"address", "token", "symbol", "balance", "raw_balance"},
		},

		// ── Sweep ─────────────────────────────────────────────────────────────

		{
			Method:      "POST",
			Path:        "/sdk/wallets/{walletId}/sweep",
			Group:       "Sweep",
			Name:        "Trigger Sweep",
			Description: "Sweeps wallet balance to master wallet. Deducts 10 credits.",
			Credits:     10,
			Async:       true,
			PathParams: []FieldSpec{
				{Name: "walletId", Type: "string", Required: true, Description: "Wallet ID to sweep from"},
			},
			Body: []FieldSpec{
				{Name: "chain_type", Type: "string", Required: true, Description: "evm or solana", Enum: []any{"evm", "solana"}, Default: "evm"},
				{Name: "chain_id", Type: "string", Required: false, Description: "EVM chain ID (omit for Solana)", Default: "137"},
				{Name: "idempotency_key", Type: "string", Required: false, Description: "Deduplication key"},
			},
			ResultFields: []string{"job_id", "status"},
		},

		// ── Jobs ──────────────────────────────────────────────────────────────

		{
			Method:      "GET",
			Path:        "/sdk/jobs/{jobId}",
			Group:       "Jobs",
			Name:        "Get Job Status",
			Description: "Polls a signing job for its current status. Free.",
			Credits:     0,
			PathParams: []FieldSpec{
				{Name: "jobId", Type: "string", Required: true, Description: "Job ID from a signing request"},
			},
			ResultFields: []string{"id", "status", "result", "error", "attempts"},
		},

		// ── Balance & Broadcast ───────────────────────────────────────────────

		{
			Method:      "GET",
			Path:        "/sdk/wallets/{walletId}/balance",
			Group:       "Balance",
			Name:        "Get Native Balance",
			Description: "Returns native token balance (ETH, MATIC, SOL). Free.",
			Credits:     0,
			PathParams: []FieldSpec{
				{Name: "walletId", Type: "string", Required: true, Description: "Wallet ID"},
			},
			QueryParams: []FieldSpec{
				{Name: "chain_id", Type: "string", Required: false, Description: "EVM chain ID (omit for Solana)", Default: "137"},
			},
			ResultFields: []string{"address", "balance", "unit"},
		},
		{
			Method:      "POST",
			Path:        "/sdk/wallets/{walletId}/broadcast",
			Group:       "Balance",
			Name:        "Broadcast Transaction",
			Description: "Broadcasts a signed transaction to the network. Free.",
			Credits:     0,
			PathParams: []FieldSpec{
				{Name: "walletId", Type: "string", Required: true, Description: "Wallet ID"},
			},
			Body: []FieldSpec{
				{Name: "signed_tx", Type: "string", Required: true, Description: "Signed transaction hex (EVM) or base64 (Solana)"},
				{Name: "chain_id", Type: "string", Required: false, Description: "EVM chain ID (omit for Solana)", Default: "137"},
			},
			ResultFields: []string{"tx_hash", "signature"},
		},
	}
}