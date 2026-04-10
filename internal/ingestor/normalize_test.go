package ingestor_test

import (
	"testing"
	"time"

	"memecoin_scorer/internal/ingestor"
)

// memeMint is a made-up mint that is not on the denylist.
const memeMint = "MEMExxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"

// raydiumProgram is a representative DEX program ID.
const raydiumProgram = "675kPX9MHTjS2zt1qfr1NYHuzeLXfQM9H24wFSUt1Mp8"

// buyPayload is a minimal Helius enhanced-transaction array representing a BUY
// (SOL → token): nativeInput = 1 SOL, tokenOutputs[0].mint = memeMint.
const buyPayload = `[{
  "signature": "sigBUY111",
  "slot": 300000000,
  "timestamp": 1700000000,
  "type": "SWAP",
  "source": "RAYDIUM",
  "feePayer": "buyerWallet111",
  "transactionError": null,
  "events": {
    "swap": {
      "nativeInput":  {"account": "buyerWallet111", "amount": "1000000000"},
      "nativeOutput": null,
      "tokenInputs":  [],
      "tokenOutputs": [{"mint": "` + memeMint + `", "tokenAmount": 1000000,
                        "fromUserAccount": "", "toUserAccount": "buyerWallet111"}],
      "programInfo":  {"account": "` + raydiumProgram + `", "programName": "RAYDIUM", "source": "RAYDIUM"}
    }
  }
}]`

// sellPayload represents a SELL (token → SOL): tokenInputs[0].mint = memeMint, nativeOutput = 0.5 SOL.
const sellPayload = `[{
  "signature": "sigSELL222",
  "slot": 300000001,
  "timestamp": 1700000001,
  "type": "SWAP",
  "source": "RAYDIUM",
  "feePayer": "sellerWallet222",
  "transactionError": null,
  "events": {
    "swap": {
      "nativeInput":  null,
      "nativeOutput": {"account": "sellerWallet222", "amount": "500000000"},
      "tokenInputs":  [{"mint": "` + memeMint + `", "tokenAmount": 500000,
                        "fromUserAccount": "sellerWallet222", "toUserAccount": ""}],
      "tokenOutputs": [],
      "programInfo":  null
    }
  }
}]`

// errorTxPayload has a non-null transactionError and must be skipped.
const errorTxPayload = `[{
  "signature": "sigERR333",
  "slot": 300000002,
  "timestamp": 1700000002,
  "type": "SWAP",
  "source": "RAYDIUM",
  "feePayer": "wallet333",
  "transactionError": {"InstructionError": [0, {"Custom": 6001}]},
  "events": {
    "swap": {
      "nativeInput":  {"account": "wallet333", "amount": "1000000000"},
      "nativeOutput": null,
      "tokenInputs":  [],
      "tokenOutputs": [{"mint": "` + memeMint + `", "tokenAmount": 100}]
    }
  }
}]`

// denylistedPayload uses wSOL as the output mint — must be filtered out.
const denylistedPayload = `[{
  "signature": "sigDENY444",
  "slot": 300000003,
  "timestamp": 1700000003,
  "type": "SWAP",
  "source": "RAYDIUM",
  "feePayer": "wallet444",
  "transactionError": null,
  "events": {
    "swap": {
      "nativeInput":  {"account": "wallet444", "amount": "1000000000"},
      "nativeOutput": null,
      "tokenInputs":  [],
      "tokenOutputs": [{"mint": "So11111111111111111111111111111111111111112",
                        "tokenAmount": 1}]
    }
  }
}]`

// noSwapPayload has no events.swap object (non-swap transaction).
const noSwapPayload = `[{
  "signature": "sigNOSWAP555",
  "slot": 300000004,
  "timestamp": 1700000004,
  "type": "TRANSFER",
  "source": "SYSTEM_PROGRAM",
  "feePayer": "wallet555",
  "transactionError": null,
  "events": {}
}]`

// splToSplPayload has no nativeInput or nativeOutput — must be skipped.
const splToSplPayload = `[{
  "signature": "sigSPLSPL666",
  "slot": 300000005,
  "timestamp": 1700000005,
  "type": "SWAP",
  "source": "ORCA",
  "feePayer": "wallet666",
  "transactionError": null,
  "events": {
    "swap": {
      "nativeInput":  null,
      "nativeOutput": null,
      "tokenInputs":  [{"mint": "USDC_MINT", "tokenAmount": 100}],
      "tokenOutputs": [{"mint": "` + memeMint + `", "tokenAmount": 200}]
    }
  }
}]`

// zeroSolPayload has amount "0" and must be skipped (no economic activity).
const zeroSolPayload = `[{
  "signature": "sigZERO777",
  "slot": 300000006,
  "timestamp": 1700000006,
  "type": "SWAP",
  "source": "RAYDIUM",
  "feePayer": "wallet777",
  "transactionError": null,
  "events": {
    "swap": {
      "nativeInput":  {"account": "wallet777", "amount": "0"},
      "nativeOutput": null,
      "tokenInputs":  [],
      "tokenOutputs": [{"mint": "` + memeMint + `", "tokenAmount": 1}]
    }
  }
}]`

// mixedPayload contains one valid buy and one error tx — only the buy should be returned.
const mixedPayload = `[
  {
    "signature": "sigMIX_OK",
    "slot": 300000010,
    "timestamp": 1700000010,
    "type": "SWAP",
    "source": "RAYDIUM",
    "feePayer": "walletOK",
    "transactionError": null,
    "events": {
      "swap": {
        "nativeInput":  {"account": "walletOK", "amount": "2000000000"},
        "nativeOutput": null,
        "tokenInputs":  [],
        "tokenOutputs": [{"mint": "` + memeMint + `", "tokenAmount": 2000000}]
      }
    }
  },
  {
    "signature": "sigMIX_ERR",
    "slot": 300000011,
    "timestamp": 1700000011,
    "type": "SWAP",
    "source": "RAYDIUM",
    "feePayer": "walletBad",
    "transactionError": {"InstructionError": [0, {"Custom": 100}]},
    "events": {
      "swap": {
        "nativeInput":  {"account": "walletBad", "amount": "1000000000"},
        "nativeOutput": null,
        "tokenInputs":  [],
        "tokenOutputs": [{"mint": "` + memeMint + `", "tokenAmount": 1000000}]
      }
    }
  }
]`

const rawTokenAmountPayload = `[{
  "signature": "sigRAW888",
  "slot": 300000012,
  "timestamp": 1700000012,
  "type": "SWAP",
  "source": "RAYDIUM",
  "feePayer": "walletRaw",
  "transactionError": null,
  "events": {
    "swap": {
      "nativeInput":  {"account": "walletRaw", "amount": "250000000"},
      "nativeOutput": null,
      "tokenInputs":  [],
      "tokenOutputs": [{
        "mint": "` + memeMint + `",
        "rawTokenAmount": {"tokenAmount": "1250000", "decimals": 6}
      }]
    }
  }
}]`

func TestNormalize_Buy(t *testing.T) {
	events, err := ingestor.NormalizeHelius([]byte(buyPayload))
	if err != nil {
		t.Fatalf("NormalizeHelius: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	ev := events[0]

	if ev.Signature != "sigBUY111" {
		t.Errorf("Signature = %q, want sigBUY111", ev.Signature)
	}
	if ev.Slot != 300000000 {
		t.Errorf("Slot = %d, want 300000000", ev.Slot)
	}
	wantTime := time.Unix(1700000000, 0).UTC()
	if !ev.BlockTime.Equal(wantTime) {
		t.Errorf("BlockTime = %v, want %v", ev.BlockTime, wantTime)
	}
	if ev.TokenMint != memeMint {
		t.Errorf("TokenMint = %q, want %q", ev.TokenMint, memeMint)
	}
	if !ev.IsBuy {
		t.Error("IsBuy = false, want true")
	}
	if ev.WalletAddr != "buyerWallet111" {
		t.Errorf("WalletAddr = %q, want buyerWallet111", ev.WalletAddr)
	}
	if ev.SOLAmount != 1.0 {
		t.Errorf("SOLAmount = %.6f, want 1.0", ev.SOLAmount)
	}
	if ev.TokenAmount != 1_000_000 {
		t.Errorf("TokenAmount = %.0f, want 1000000", ev.TokenAmount)
	}
	if ev.ProgramID != raydiumProgram {
		t.Errorf("ProgramID = %q, want %q", ev.ProgramID, raydiumProgram)
	}
}

func TestNormalize_Sell(t *testing.T) {
	events, err := ingestor.NormalizeHelius([]byte(sellPayload))
	if err != nil {
		t.Fatalf("NormalizeHelius: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	ev := events[0]

	if ev.TokenMint != memeMint {
		t.Errorf("TokenMint = %q, want %q", ev.TokenMint, memeMint)
	}
	if ev.IsBuy {
		t.Error("IsBuy = true, want false for sell")
	}
	if ev.SOLAmount != 0.5 {
		t.Errorf("SOLAmount = %.6f, want 0.5", ev.SOLAmount)
	}
	if ev.WalletAddr != "sellerWallet222" {
		t.Errorf("WalletAddr = %q, want sellerWallet222", ev.WalletAddr)
	}
	// Source used as ProgramID because programInfo is null
	if ev.ProgramID != "RAYDIUM" {
		t.Errorf("ProgramID = %q, want RAYDIUM (from source field)", ev.ProgramID)
	}
}

func TestNormalize_RawTokenAmount(t *testing.T) {
	events, err := ingestor.NormalizeHelius([]byte(rawTokenAmountPayload))
	if err != nil {
		t.Fatalf("NormalizeHelius: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if got := events[0].TokenAmount; got != 1.25 {
		t.Fatalf("TokenAmount = %.6f, want 1.25", got)
	}
}

func TestNormalize_ErrorTransaction_Skipped(t *testing.T) {
	events, err := ingestor.NormalizeHelius([]byte(errorTxPayload))
	if err != nil {
		t.Fatalf("NormalizeHelius: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("len(events) = %d, want 0 (error tx must be skipped)", len(events))
	}
}

func TestNormalize_DenylistedMint_Skipped(t *testing.T) {
	events, err := ingestor.NormalizeHelius([]byte(denylistedPayload))
	if err != nil {
		t.Fatalf("NormalizeHelius: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("len(events) = %d, want 0 (wSOL must be filtered)", len(events))
	}
}

func TestNormalize_NoSwapEvent_Skipped(t *testing.T) {
	events, err := ingestor.NormalizeHelius([]byte(noSwapPayload))
	if err != nil {
		t.Fatalf("NormalizeHelius: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("len(events) = %d, want 0 (no swap event)", len(events))
	}
}

func TestNormalize_SplToSpl_Skipped(t *testing.T) {
	events, err := ingestor.NormalizeHelius([]byte(splToSplPayload))
	if err != nil {
		t.Fatalf("NormalizeHelius: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("len(events) = %d, want 0 (SPL-to-SPL swap, no SOL side)", len(events))
	}
}

func TestNormalize_ZeroSolAmount_Skipped(t *testing.T) {
	events, err := ingestor.NormalizeHelius([]byte(zeroSolPayload))
	if err != nil {
		t.Fatalf("NormalizeHelius: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("len(events) = %d, want 0 (zero SOL amount must be skipped)", len(events))
	}
}

// wrappedBuyPayload is the same buy transaction delivered in the wrapped-object form
// {"transactions":[...]} instead of the bare-array form.
const wrappedBuyPayload = `{
  "transactions": [{
    "signature": "sigBUY_WRAPPED",
    "slot": 300000020,
    "timestamp": 1700000020,
    "type": "SWAP",
    "source": "RAYDIUM",
    "feePayer": "buyerWrapped",
    "transactionError": null,
    "events": {
      "swap": {
        "nativeInput":  {"account": "buyerWrapped", "amount": "500000000"},
        "nativeOutput": null,
        "tokenInputs":  [],
        "tokenOutputs": [{"mint": "` + memeMint + `", "tokenAmount": 500000}]
      }
    }
  }]
}`

// wrappedEmptyPayload is a wrapped-object form with an empty transactions array.
const wrappedEmptyPayload = `{"transactions": []}`

func TestNormalize_WrappedObjectForm_Buy(t *testing.T) {
	events, err := ingestor.NormalizeHelius([]byte(wrappedBuyPayload))
	if err != nil {
		t.Fatalf("NormalizeHelius (wrapped): %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	ev := events[0]
	if ev.Signature != "sigBUY_WRAPPED" {
		t.Errorf("Signature = %q, want sigBUY_WRAPPED", ev.Signature)
	}
	if ev.SOLAmount != 0.5 {
		t.Errorf("SOLAmount = %.6f, want 0.5", ev.SOLAmount)
	}
	if !ev.IsBuy {
		t.Error("IsBuy = false, want true")
	}
	if ev.TokenMint != memeMint {
		t.Errorf("TokenMint = %q, want %q", ev.TokenMint, memeMint)
	}
}

func TestNormalize_WrappedEmptyTransactions(t *testing.T) {
	events, err := ingestor.NormalizeHelius([]byte(wrappedEmptyPayload))
	if err != nil {
		t.Fatalf("NormalizeHelius (wrapped empty): %v", err)
	}
	if len(events) != 0 {
		t.Errorf("len(events) = %d, want 0", len(events))
	}
}

func TestNormalize_MalformedJSON_ReturnsError(t *testing.T) {
	_, err := ingestor.NormalizeHelius([]byte(`not json`))
	if err == nil {
		t.Error("expected error for malformed JSON, got nil")
	}
}

func TestNormalize_EmptyArray_ReturnsEmpty(t *testing.T) {
	events, err := ingestor.NormalizeHelius([]byte(`[]`))
	if err != nil {
		t.Fatalf("NormalizeHelius: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("len(events) = %d, want 0 for empty array", len(events))
	}
}

func TestNormalize_Mixed_OnlyValidReturned(t *testing.T) {
	events, err := ingestor.NormalizeHelius([]byte(mixedPayload))
	if err != nil {
		t.Fatalf("NormalizeHelius: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1 (error tx skipped)", len(events))
	}
	if events[0].Signature != "sigMIX_OK" {
		t.Errorf("Signature = %q, want sigMIX_OK", events[0].Signature)
	}
	if events[0].SOLAmount != 2.0 {
		t.Errorf("SOLAmount = %.6f, want 2.0", events[0].SOLAmount)
	}
}
