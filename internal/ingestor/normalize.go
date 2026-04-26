// Package ingestor normalises raw Helius webhook payloads into SwapEvents.
package ingestor

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"memecoin_scorer/internal/filter"
	"memecoin_scorer/internal/model"
)

// NormalizeHelius parses a raw Helius enhanced-transaction webhook body and
// returns one SwapEvent per parseable SOL-side swap.
//
// Payload shape: Helius enhanced-transaction webhooks deliver the body as a bare
// JSON array.  Some integration layers (e.g. custom proxies or older Helius
// webhook versions) wrap the array in an object:
//
//	{"transactions": [...]}
//
// NormalizeHelius tries the bare-array form first; on failure it retries with the
// wrapped form.  Only an unrecoverable parse failure on both attempts returns an
// error.
//
// Per-transaction normalisation rules:
//   - Transactions with transactionError != null are skipped.
//   - A BUY is identified by events.swap.nativeInput != nil with len(tokenOutputs) > 0.
//     For multi-hop routes only tokenOutputs[0] is used.
//   - A SELL is identified by len(tokenInputs) > 0 with events.swap.nativeOutput != nil.
//     Only tokenInputs[0] is used.
//   - SPL-to-SPL swaps (both native fields absent) are skipped.
//   - events.swap.nativeInput/Output.amount is a decimal lamport string ("1000000000").
//   - tokenAmount fields are float64 numbers.
//   - Mints in the shared denylist are silently skipped.
//   - Malformed individual transactions are skipped without failing the call.
func NormalizeHelius(body []byte) ([]model.SwapEvent, error) {
	txns, err := parseHeliusBody(body)
	if err != nil {
		return nil, fmt.Errorf("ingestor: parse webhook body: %w", err)
	}

	var out []model.SwapEvent
	for i := range txns {
		ev, ok := toSwapEvent(&txns[i])
		if !ok {
			continue
		}
		out = append(out, ev)
	}
	return out, nil
}

// parseHeliusBody attempts to decode the body first as a bare JSON array, then
// as a wrapped object {"transactions":[...]}.  Returns an error only when both
// attempts fail.
func parseHeliusBody(body []byte) ([]heliusTx, error) {
	// Attempt 1: bare array  [{ ... }, ...]
	var txns []heliusTx
	if err := json.Unmarshal(body, &txns); err == nil {
		return txns, nil
	}

	// Attempt 2: wrapped object  {"transactions": [...]}
	var wrapped struct {
		Transactions []heliusTx `json:"transactions"`
	}
	if err := json.Unmarshal(body, &wrapped); err != nil {
		return nil, fmt.Errorf("not a bare array or {\"transactions\":[...]} object: %w", err)
	}
	return wrapped.Transactions, nil
}

func toSwapEvent(tx *heliusTx) (model.SwapEvent, bool) {
	if tx.hasError() {
		return model.SwapEvent{}, false
	}
	if tx.Events.Swap == nil {
		return model.SwapEvent{}, false
	}
	if tx.Signature == "" {
		return model.SwapEvent{}, false
	}

	sw := tx.Events.Swap

	var (
		mint        string
		sol         float64
		tokenAmount float64
		isBuy       bool
	)

	switch {
	case sw.NativeInput != nil && len(sw.TokenOutputs) > 0:
		// BUY: SOL → token
		var err error
		sol, err = parseLamports(sw.NativeInput.Amount)
		if err != nil || sol <= 0 {
			return model.SwapEvent{}, false
		}
		mint = sw.TokenOutputs[0].Mint
		tokenAmount = sw.TokenOutputs[0].TokenAmount
		isBuy = true

	case len(sw.TokenInputs) > 0 && sw.NativeOutput != nil:
		// SELL: token → SOL
		var err error
		sol, err = parseLamports(sw.NativeOutput.Amount)
		if err != nil || sol <= 0 {
			return model.SwapEvent{}, false
		}
		mint = sw.TokenInputs[0].Mint
		tokenAmount = sw.TokenInputs[0].TokenAmount
		isBuy = false

	default:
		// SPL-to-SPL or unrecognised shape — skip silently
		return model.SwapEvent{}, false
	}

	if mint == "" || filter.IsDenylisted(mint) {
		return model.SwapEvent{}, false
	}

	programID := tx.Source
	if sw.ProgramInfo != nil && sw.ProgramInfo.Account != "" {
		programID = sw.ProgramInfo.Account
	}

	return model.SwapEvent{
		Signature:        tx.Signature,
		Slot:             tx.Slot,
		BlockTime:        time.Unix(tx.Timestamp, 0).UTC(),
		TokenMint:        mint,
		IsBuy:            isBuy,
		WalletAddr:       tx.FeePayer,
		SOLAmount:        sol,
		TokenAmount:      tokenAmount,
		ProgramID:        programID,
		PoolAccountAddr:  extractPoolAccount(tx),
		RealPoolDepthSOL: -1, // pc_vault lookup not yet implemented; see docs/real_liquidity_discovery_gap.md
	}, true
}

// parseLamports converts a lamport string to SOL.
// Accepts both integer strings ("1000000000") and float strings ("1.5e9").
func parseLamports(s string) (float64, error) {
	v, err := strconv.ParseInt(s, 10, 64)
	if err == nil {
		return float64(v) / 1e9, nil
	}
	// Fallback for float-encoded lamport values
	f, ferr := strconv.ParseFloat(s, 64)
	if ferr != nil {
		return 0, fmt.Errorf("parseLamports %q: %w", s, err)
	}
	return f / 1e9, nil
}

// --- Helius enhanced-transaction raw types ---

// Known DEX program IDs used for pool account extraction.
const (
	// raydiumAMMV4 is the Raydium AMM V4 program. In its swap inner instructions
	// accounts[0] is the AMM pool account (from which pc_vault can be derived via RPC).
	raydiumAMMV4 = "675kPX9MHTjS2zt1qfr1NYHuzeLXfQM9H24wFSUt1Mp8"
	// pumpFun is the Pump.fun bonding-curve program. In its buy/sell inner instructions
	// accounts[3] is the bonding-curve account (pool equivalent).
	pumpFun = "6EF8rrecthR5Dkzon8Nwu78hRvfCKubJ14M5uBEwF6P"
)

type heliusTx struct {
	Signature        string          `json:"signature"`
	Slot             uint64          `json:"slot"`
	Timestamp        int64           `json:"timestamp"`
	Type             string          `json:"type"`
	Source           string          `json:"source"`
	FeePayer         string          `json:"feePayer"`
	TransactionError json.RawMessage `json:"transactionError"`
	Events           struct {
		Swap *heliusSwap `json:"swap"`
	} `json:"events"`
	// Accounts is the ordered list of all account pubkeys involved in the transaction.
	// Inner instruction account indices are offsets into this slice.
	Accounts []string `json:"accounts"`
	// InnerInstructions contains CPI (cross-program invocation) instruction data.
	// Used for best-effort pool account extraction when the enhanced swap event
	// does not directly expose the AMM pool address.
	InnerInstructions []heliusInnerInstruction `json:"innerInstructions"`
}

// hasError returns true when transactionError is a non-null JSON value.
func (tx *heliusTx) hasError() bool {
	return tx.TransactionError != nil && string(tx.TransactionError) != "null"
}

type heliusSwap struct {
	NativeInput  *heliusNative `json:"nativeInput"`
	NativeOutput *heliusNative `json:"nativeOutput"`
	TokenInputs  []heliusToken `json:"tokenInputs"`
	TokenOutputs []heliusToken `json:"tokenOutputs"`
	ProgramInfo  *heliusProg   `json:"programInfo"`
}

type heliusNative struct {
	Account string `json:"account"`
	Amount  string `json:"amount"` // decimal lamports, e.g. "1000000000"
}

type heliusToken struct {
	Mint            string  `json:"mint"`
	TokenAmount     float64 `json:"tokenAmount"`
	FromUserAccount string  `json:"fromUserAccount"`
	ToUserAccount   string  `json:"toUserAccount"`
}

func (t *heliusToken) UnmarshalJSON(data []byte) error {
	type rawAmount struct {
		TokenAmount string `json:"tokenAmount"`
		Decimals    int    `json:"decimals"`
	}
	type alias struct {
		Mint            string          `json:"mint"`
		TokenAmount     json.RawMessage `json:"tokenAmount"`
		RawTokenAmount  *rawAmount      `json:"rawTokenAmount"`
		FromUserAccount string          `json:"fromUserAccount"`
		ToUserAccount   string          `json:"toUserAccount"`
	}
	var aux alias
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	t.Mint = aux.Mint
	t.FromUserAccount = aux.FromUserAccount
	t.ToUserAccount = aux.ToUserAccount

	if len(aux.TokenAmount) > 0 && string(aux.TokenAmount) != "null" {
		if err := json.Unmarshal(aux.TokenAmount, &t.TokenAmount); err == nil {
			return nil
		}
		var s string
		if err := json.Unmarshal(aux.TokenAmount, &s); err == nil {
			if f, ferr := strconv.ParseFloat(s, 64); ferr == nil {
				t.TokenAmount = f
				return nil
			}
		}
	}

	if aux.RawTokenAmount != nil {
		raw, err := strconv.ParseFloat(aux.RawTokenAmount.TokenAmount, 64)
		if err != nil {
			return fmt.Errorf("parse raw token amount %q: %w", aux.RawTokenAmount.TokenAmount, err)
		}
		t.TokenAmount = raw / math.Pow10(aux.RawTokenAmount.Decimals)
	}

	return nil
}

type heliusProg struct {
	Account     string `json:"account"`
	ProgramName string `json:"programName"`
	Source      string `json:"source"`
}

type heliusInnerInstruction struct {
	Index        int                  `json:"index"`
	Instructions []heliusInstruction  `json:"instructions"`
}

// heliusInstruction represents one inner CPI instruction.
// Accounts is a slice of integer indices into the parent heliusTx.Accounts slice.
type heliusInstruction struct {
	Accounts  []int  `json:"accounts"`
	Data      string `json:"data"`
	ProgramId string `json:"programId"`
}

// extractPoolAccount scans inner instructions for known DEX programs and returns
// the pool account pubkey when one can be identified.
//
// Coverage:
//   - Raydium AMM V4: pool account is at accounts[0] in the swap instruction.
//   - Pump.fun:        bonding curve is at accounts[3] in the buy/sell instruction.
//
// Returns "" when no known DEX inner instruction is found or when the indexed
// account is out of range. This is best-effort; pc_vault lookup (needed to get
// actual reserve depth) requires a separate Solana RPC call.
// See docs/real_liquidity_discovery_gap.md.
func extractPoolAccount(tx *heliusTx) string {
	for _, inner := range tx.InnerInstructions {
		for _, instr := range inner.Instructions {
			switch instr.ProgramId {
			case raydiumAMMV4:
				if len(instr.Accounts) > 0 {
					idx := instr.Accounts[0]
					if idx >= 0 && idx < len(tx.Accounts) {
						return tx.Accounts[idx]
					}
				}
			case pumpFun:
				if len(instr.Accounts) > 3 {
					idx := instr.Accounts[3]
					if idx >= 0 && idx < len(tx.Accounts) {
						return tx.Accounts[idx]
					}
				}
			}
		}
	}
	return ""
}
