package stablecoin

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"golang.org/x/crypto/sha3"
)

// EncodeERC20Transfer ABI-encodes a transfer(address,uint256) call.
//
// ERC-20 transfer calldata layout (68 bytes total):
//   [0:4]   function selector  — keccak256("transfer(address,uint256)")[:4]
//   [4:36]  address argument   — zero-padded to 32 bytes
//   [36:68] amount argument    — big-endian uint256, zero-padded to 32 bytes
func EncodeERC20Transfer(to string, amount *big.Int) (string, error) {
	if to == "" {
		return "", fmt.Errorf("recipient address is required")
	}
	if amount == nil || amount.Sign() <= 0 {
		return "", fmt.Errorf("amount must be greater than zero")
	}

	selector := erc20TransferSelector()

	// Pad address to 32 bytes. Strip 0x prefix, left-pad with zeros.
	addrHex := strings.TrimPrefix(strings.ToLower(to), "0x")
	if len(addrHex) != 40 {
		return "", fmt.Errorf("invalid EVM address: %s", to)
	}
	paddedAddr := strings.Repeat("0", 24) + addrHex // 12 zero bytes + 20 addr bytes = 32 bytes

	// Pad amount to 32 bytes big-endian.
	amountBytes := amount.Bytes()
	if len(amountBytes) > 32 {
		return "", fmt.Errorf("amount too large for uint256")
	}
	paddedAmount := strings.Repeat("0", (32-len(amountBytes))*2) + hex.EncodeToString(amountBytes)

	calldata := "0x" + hex.EncodeToString(selector) + paddedAddr + paddedAmount
	return calldata, nil
}

// erc20TransferSelector returns the first 4 bytes of keccak256("transfer(address,uint256)").
func erc20TransferSelector() []byte {
	h := sha3.NewLegacyKeccak256()
	h.Write([]byte("transfer(address,uint256)"))
	full := h.Sum(nil)
	return full[:4]
}

// ParseAmount converts a human-readable amount string to the token's base units.
//
// Examples (decimals=6):
//   "50"      → 50_000_000
//   "50.5"    → 50_500_000
//   "0.000001" → 1
//
// We do this in integer arithmetic to avoid floating point precision issues.
func ParseAmount(amount string, decimals int) (*big.Int, error) {
	amount = strings.TrimSpace(amount)
	if amount == "" {
		return nil, fmt.Errorf("amount is required")
	}

	// Split on decimal point.
	parts := strings.SplitN(amount, ".", 2)
	intPart := parts[0]
	fracPart := ""
	if len(parts) == 2 {
		fracPart = parts[1]
	}

	// Validate digits only.
	for _, c := range intPart {
		if c < '0' || c > '9' {
			return nil, fmt.Errorf("invalid amount %q: non-numeric characters", amount)
		}
	}
	for _, c := range fracPart {
		if c < '0' || c > '9' {
			return nil, fmt.Errorf("invalid amount %q: non-numeric characters in fractional part", amount)
		}
	}

	// Truncate or pad fractional part to exactly `decimals` digits.
	if len(fracPart) > decimals {
		// Truncate — we don't round, we floor. Prevents sending more than intended.
		fracPart = fracPart[:decimals]
	} else {
		fracPart = fracPart + strings.Repeat("0", decimals-len(fracPart))
	}

	// Combine into a single integer string.
	combined := intPart + fracPart

	// Strip leading zeros so big.Int doesn't misinterpret as octal.
	combined = strings.TrimLeft(combined, "0")
	if combined == "" {
		combined = "0"
	}

	result := new(big.Int)
	if _, ok := result.SetString(combined, 10); !ok {
		return nil, fmt.Errorf("invalid amount %q: could not parse as integer", amount)
	}

	if result.Sign() <= 0 {
		return nil, fmt.Errorf("amount must be greater than zero")
	}

	return result, nil
}

// FormatAmount converts base units back to a human-readable string.
// Used in balance responses so devs see "50.00" not "50000000".
func FormatAmount(raw *big.Int, decimals int) string {
	if raw == nil || raw.Sign() == 0 {
		return "0"
	}

	divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	intPart := new(big.Int).Div(raw, divisor)
	fracPart := new(big.Int).Mod(raw, divisor)

	if fracPart.Sign() == 0 {
		return intPart.String()
	}

	// Zero-pad fractional part to `decimals` digits, then trim trailing zeros.
	fracStr := fmt.Sprintf("%0*s", decimals, fracPart.String())
	fracStr = strings.TrimRight(fracStr, "0")

	return intPart.String() + "." + fracStr
}

// GasLimitERC20Transfer is a safe upper bound for ERC-20 transfer gas.
// 21k covers native ETH. ERC-20 transfers require ~50-70k.
// We use 100k to leave room for tokens with custom transfer hooks (e.g. USDT on some chains).
const GasLimitERC20Transfer = uint64(100_000)