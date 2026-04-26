package rpc

import (
	"encoding/base64"
	"fmt"
	"math/big"
)

// Raydium AMM V4 account layout (LIQUIDITY_STATE_LAYOUT_V4):
//
//	32 × u64   = 256 bytes  (status through orderBookToInitTime)
//	1 × u128   = 16 bytes   (swapCoinInAmount)
//	1 × u128   = 16 bytes   (swapPcOutAmount)
//	1 × u64    =  8 bytes   (swapCoin2PcFee)
//	1 × u128   = 16 bytes   (swapPcInAmount)
//	1 × u128   = 16 bytes   (swapCoinOutAmount)
//	1 × u64    =  8 bytes   (swapPc2CoinFee)
//	             ----------
//	total so far = 336 bytes
//
//	pubkey poolCoinTokenAccount  32 bytes at offset 336  (coin/token vault)
//	pubkey poolPcTokenAccount    32 bytes at offset 368  (pc/WSOL vault ← what we need)
//	... 11 more pubkeys follow ...
//	total layout size: 752 bytes
const (
	raydiumAMMV4MinSize  = 400 // conservatively require at least up through pcVault
	raydiumAMMV4DataSize = 752 // canonical full layout size

	coinVaultOffset = 336 // poolCoinTokenAccount
	pcVaultOffset   = 368 // poolPcTokenAccount (WSOL/SOL reserve)
)

// PCVaultFromAMMData extracts the Raydium AMM V4 pc_vault (WSOL reserve) pubkey
// from raw account data. Returns the pubkey as a base58-encoded string.
//
// The pc_vault is the SPL token account holding the pool's SOL/WSOL reserve.
// Its token balance (via getTokenAccountBalance) is the executable pool depth.
//
// Returns an error when data is shorter than raydiumAMMV4MinSize or when the
// extracted 32 bytes are all zeros (uninitialized account).
func PCVaultFromAMMData(data []byte) (string, error) {
	if len(data) < raydiumAMMV4MinSize {
		return "", fmt.Errorf("raydium amm data too short: got %d bytes, need at least %d",
			len(data), raydiumAMMV4MinSize)
	}
	keyBytes := data[pcVaultOffset : pcVaultOffset+32]
	if isAllZero(keyBytes) {
		return "", fmt.Errorf("raydium amm pc_vault pubkey is all-zero (uninitialized)")
	}
	return base58Encode(keyBytes), nil
}

// CoinVaultFromAMMData extracts the coin vault (token-side reserve) pubkey.
// Not used for depth measurement but exposed for diagnostics.
func CoinVaultFromAMMData(data []byte) (string, error) {
	if len(data) < coinVaultOffset+32 {
		return "", fmt.Errorf("raydium amm data too short for coin vault: %d bytes", len(data))
	}
	keyBytes := data[coinVaultOffset : coinVaultOffset+32]
	if isAllZero(keyBytes) {
		return "", fmt.Errorf("raydium amm coin_vault pubkey is all-zero (uninitialized)")
	}
	return base58Encode(keyBytes), nil
}

func isAllZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

// base58Alphabet is the standard Bitcoin/Solana base58 character set.
var base58Alphabet = []byte("123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz")

// base58Encode encodes raw bytes into a base58 string.
// Leading zero bytes in input map to leading '1' characters in output.
func base58Encode(input []byte) string {
	leadingZeros := 0
	for _, b := range input {
		if b != 0 {
			break
		}
		leadingZeros++
	}

	n := new(big.Int).SetBytes(input)
	base := big.NewInt(58)
	mod := new(big.Int)

	var encoded []byte
	for n.Sign() > 0 {
		n.DivMod(n, base, mod)
		encoded = append(encoded, base58Alphabet[mod.Int64()])
	}
	for i := 0; i < leadingZeros; i++ {
		encoded = append(encoded, base58Alphabet[0])
	}

	// Reverse in place.
	for i, j := 0, len(encoded)-1; i < j; i, j = i+1, j-1 {
		encoded[i], encoded[j] = encoded[j], encoded[i]
	}
	return string(encoded)
}

// decodeBase64 wraps base64.StdEncoding to decode RPC-returned data strings.
func decodeBase64(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}
