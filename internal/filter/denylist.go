// Package filter provides shared token-mint filtering utilities.
package filter

// denylist contains Solana token mints that are never actionable memecoin signals.
// Only infrastructure tokens and stablecoins belong here — not memecoins.
var denylist = map[string]bool{
	"So11111111111111111111111111111111111111112":  true, // wrapped SOL (wSOL)
	"EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v": true, // USDC
	"Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB": true, // USDT (SPL)
}

// IsDenylisted reports whether mint is a known non-memecoin infrastructure token.
func IsDenylisted(mint string) bool {
	return denylist[mint]
}
