package rpc_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"memecoin_scorer/internal/rpc"
)

// buildAMMData constructs a minimal 752-byte Raydium AMM V4 data blob with
// the pc_vault pubkey set at offset 368. All other bytes are zero.
func buildAMMData(pcVaultPubkey [32]byte) []byte {
	return buildAMMDataWithVaults([32]byte{9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9}, pcVaultPubkey)
}

func buildAMMDataWithVaults(coinVaultPubkey [32]byte, pcVaultPubkey [32]byte) []byte {
	data := make([]byte, rpc.RaydiumAMMV4DataSize)
	copy(data[rpc.CoinVaultOffsetExported:], coinVaultPubkey[:])
	copy(data[rpc.PCVaultOffsetExported:], pcVaultPubkey[:])
	return data
}

func buildTokenAccountData(mint string, amount uint64) []byte {
	data := make([]byte, 165)
	mintBytes := mustBase58Decode(mint)
	copy(data[0:32], mintBytes)
	copy(data[32:64], bytes.Repeat([]byte{7}, 32))
	binary.LittleEndian.PutUint64(data[64:72], amount)
	return data
}

// knownPCVault is a deterministic 32-byte pubkey used across tests.
var knownPCVault = [32]byte{
	1, 2, 3, 4, 5, 6, 7, 8,
	9, 10, 11, 12, 13, 14, 15, 16,
	17, 18, 19, 20, 21, 22, 23, 24,
	25, 26, 27, 28, 29, 30, 31, 32,
}

var knownCoinVault = [32]byte{
	32, 31, 30, 29, 28, 27, 26, 25,
	24, 23, 22, 21, 20, 19, 18, 17,
	16, 15, 14, 13, 12, 11, 10, 9,
	8, 7, 6, 5, 4, 3, 2, 1,
}

// knownPoolAccount is a placeholder pool account address for tests.
const knownPoolAccount = "POOL1111111111111111111111111111111111111111"

// rpcHandler builds an httptest.Server that dispatches getAccountInfo and
// getTokenAccountBalance calls based on the provided handlers.
func rpcServer(
	t *testing.T,
	accountInfoFn func(pubkey string) ([]byte, bool),
	balanceFn func(pubkey string) (*float64, bool),
) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", 400)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		switch req.Method {
		case "getAccountInfo":
			var pubkey string
			_ = json.Unmarshal(req.Params[0], &pubkey)
			data, ok := accountInfoFn(pubkey)
			if !ok {
				// Account not found: return null value.
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"jsonrpc": "2.0", "id": 1,
					"result": map[string]interface{}{"value": nil},
				})
				return
			}
			encoded := base64.StdEncoding.EncodeToString(data)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0", "id": 1,
				"result": map[string]interface{}{
					"value": map[string]interface{}{
						"data": []string{encoded, "base64"},
					},
				},
			})

		case "getTokenAccountBalance":
			var pubkey string
			_ = json.Unmarshal(req.Params[0], &pubkey)
			uiAmount, ok := balanceFn(pubkey)
			if !ok {
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"jsonrpc": "2.0", "id": 1,
					"result": map[string]interface{}{"value": nil},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0", "id": 1,
				"result": map[string]interface{}{
					"value": map[string]interface{}{
						"uiAmount": *uiAmount,
					},
				},
			})

		default:
			http.Error(w, "method not found", 400)
		}
	}))
}

func floatPtr(v float64) *float64 { return &v }

func mustBase58Decode(s string) []byte {
	alphabet := "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	n := big.NewInt(0)
	base := big.NewInt(58)
	for _, r := range s {
		idx := int64(bytes.IndexRune([]byte(alphabet), r))
		if idx < 0 {
			panic("invalid base58")
		}
		n.Mul(n, base)
		n.Add(n, big.NewInt(idx))
	}
	out := n.Bytes()
	leading := 0
	for leading < len(s) && s[leading] == '1' {
		leading++
	}
	out = append(bytes.Repeat([]byte{0}, leading), out...)
	if len(out) < 32 {
		out = append(bytes.Repeat([]byte{0}, 32-len(out)), out...)
	}
	return out
}

// TestGetTokenAccountBalance_Success verifies that a successful RPC call
// returns the UI amount as a float64.
func TestGetTokenAccountBalance_Success(t *testing.T) {
	expected := 42.5
	srv := rpcServer(t,
		func(string) ([]byte, bool) { return nil, false },
		func(string) (*float64, bool) { return floatPtr(expected), true },
	)
	defer srv.Close()

	c := rpc.NewClient(srv.URL, 2*time.Second)
	got, err := c.GetTokenAccountBalance(context.Background(), "anyPubkey")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != expected {
		t.Errorf("balance = %v, want %v", got, expected)
	}
}

// TestGetTokenAccountBalance_AccountNotFound verifies that a null value response
// is mapped to ErrAccountNotFound.
func TestGetTokenAccountBalance_AccountNotFound(t *testing.T) {
	srv := rpcServer(t,
		func(string) ([]byte, bool) { return nil, false },
		func(string) (*float64, bool) { return nil, false },
	)
	defer srv.Close()

	c := rpc.NewClient(srv.URL, 2*time.Second)
	_, err := c.GetTokenAccountBalance(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error for missing account, got nil")
	}
}

// TestGetTokenAccountBalance_Timeout verifies that a context deadline is
// respected and propagated as an error.
func TestGetTokenAccountBalance_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate slow server — sleep longer than the caller's deadline.
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := rpc.NewClient(srv.URL, 5*time.Second) // client timeout is generous
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := c.GetTokenAccountBalance(ctx, "anyPubkey")
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

// TestFetchDepth_FallbackDoesNotPanic verifies that FetchDepth with an empty
// pool account addr returns UnavailableDepth without panicking.
func TestFetchDepth_FallbackDoesNotPanic(t *testing.T) {
	c := rpc.NewClient("http://127.0.0.1:0", 1*time.Second) // unreachable — must not be called
	dc := rpc.NewDepthClient(c)

	result := dc.FetchDepth(context.Background(), "")
	if result.SOL >= 0 {
		t.Errorf("expected unavailable sentinel (-1), got %v", result.SOL)
	}
}

// TestFetchDepth_RealDepthOverridesProxy verifies the full two-step path:
// getAccountInfo → decode pc_vault → getTokenAccountBalance → real SOL depth.
func TestFetchDepth_RealDepthOverridesProxy(t *testing.T) {
	ammData := buildAMMData(knownPCVault)
	expectedPCVault := rpc.Base58EncodeExported(knownPCVault[:])
	expectedDepth := 150.25

	srv := rpcServer(t,
		func(pubkey string) ([]byte, bool) {
			if pubkey == knownPoolAccount {
				return ammData, true
			}
			if pubkey == expectedPCVault {
				return buildTokenAccountData(rpc.WSOLMint, 0), true
			}
			return nil, false
		},
		func(pubkey string) (*float64, bool) {
			if pubkey == expectedPCVault {
				return floatPtr(expectedDepth), true
			}
			return nil, false
		},
	)
	defer srv.Close()

	c := rpc.NewClient(srv.URL, 2*time.Second)
	dc := rpc.NewDepthClient(c)

	result := dc.FetchDepth(context.Background(), knownPoolAccount)
	if result.SOL != expectedDepth {
		t.Errorf("depth = %v, want %v", result.SOL, expectedDepth)
	}
	if result.Source != rpc.LiquiditySourceWSOLVault {
		t.Errorf("source = %q, want %q", result.Source, rpc.LiquiditySourceWSOLVault)
	}
}

// TestFetchDepth_ProxyFallbackWhenRPCFails verifies that when the RPC server
// is unreachable, FetchDepth returns UnavailableDepth with the proxy source.
func TestFetchDepth_ProxyFallbackWhenRPCFails(t *testing.T) {
	c := rpc.NewClient("http://127.0.0.1:1", 100*time.Millisecond) // port 1 = refused
	dc := rpc.NewDepthClient(c)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	result := dc.FetchDepth(ctx, knownPoolAccount)
	if result.SOL >= 0 {
		t.Errorf("expected fallback (-1), got %v", result.SOL)
	}
	if result.Source != rpc.LiquiditySourceProxy {
		t.Errorf("source = %q, want %q", result.Source, rpc.LiquiditySourceProxy)
	}
}

func TestFetchDepth_UsesCoinVaultWhenCoinVaultIsWSOL(t *testing.T) {
	ammData := buildAMMDataWithVaults(knownCoinVault, knownPCVault)
	pcVault := rpc.Base58EncodeExported(knownPCVault[:])
	coinVault := rpc.Base58EncodeExported(knownCoinVault[:])
	expectedDepth := 88.75

	srv := rpcServer(t,
		func(pubkey string) ([]byte, bool) {
			switch pubkey {
			case knownPoolAccount:
				return ammData, true
			case pcVault:
				return buildTokenAccountData("11111111111111111111111111111111", 999_000_000_000), true
			case coinVault:
				return buildTokenAccountData(rpc.WSOLMint, 0), true
			default:
				return nil, false
			}
		},
		func(pubkey string) (*float64, bool) {
			if pubkey == coinVault {
				return floatPtr(expectedDepth), true
			}
			return nil, false
		},
	)
	defer srv.Close()

	c := rpc.NewClient(srv.URL, 2*time.Second)
	result := rpc.NewDepthClient(c).FetchDepth(context.Background(), knownPoolAccount)
	if result.SOL != expectedDepth || result.Source != rpc.LiquiditySourceWSOLVault {
		t.Fatalf("got %+v want %.2f from verified coin WSOL vault", result, expectedDepth)
	}
}

func TestFetchDepth_NoWSOLVaultFallsBack(t *testing.T) {
	ammData := buildAMMDataWithVaults(knownCoinVault, knownPCVault)
	pcVault := rpc.Base58EncodeExported(knownPCVault[:])
	coinVault := rpc.Base58EncodeExported(knownCoinVault[:])

	srv := rpcServer(t,
		func(pubkey string) ([]byte, bool) {
			switch pubkey {
			case knownPoolAccount:
				return ammData, true
			case pcVault, coinVault:
				return buildTokenAccountData("11111111111111111111111111111111", 999_000_000_000), true
			default:
				return nil, false
			}
		},
		func(pubkey string) (*float64, bool) {
			return floatPtr(165_951_523.412432), true
		},
	)
	defer srv.Close()

	result := rpc.NewDepthClient(rpc.NewClient(srv.URL, 2*time.Second)).FetchDepth(context.Background(), knownPoolAccount)
	if result.SOL != -1 || result.Source != rpc.LiquiditySourceProxy {
		t.Fatalf("got %+v want unavailable fallback for non-WSOL vaults", result)
	}
}

func TestFetchDepth_VaultAccountInfoErrorFallsBack(t *testing.T) {
	ammData := buildAMMDataWithVaults(knownCoinVault, knownPCVault)

	srv := rpcServer(t,
		func(pubkey string) ([]byte, bool) {
			if pubkey == knownPoolAccount {
				return ammData, true
			}
			return nil, false
		},
		func(pubkey string) (*float64, bool) {
			return floatPtr(100), true
		},
	)
	defer srv.Close()

	result := rpc.NewDepthClient(rpc.NewClient(srv.URL, 2*time.Second)).FetchDepth(context.Background(), knownPoolAccount)
	if result.SOL != -1 || result.Source != rpc.LiquiditySourceProxy {
		t.Fatalf("got %+v want unavailable fallback when vault info cannot be verified", result)
	}
}

func TestFetchDepth_HugeNonWSOLBalanceDoesNotBecomeDepth(t *testing.T) {
	ammData := buildAMMDataWithVaults(knownCoinVault, knownPCVault)
	pcVault := rpc.Base58EncodeExported(knownPCVault[:])
	coinVault := rpc.Base58EncodeExported(knownCoinVault[:])

	srv := rpcServer(t,
		func(pubkey string) ([]byte, bool) {
			switch pubkey {
			case knownPoolAccount:
				return ammData, true
			case pcVault, coinVault:
				return buildTokenAccountData("11111111111111111111111111111111", 999_000_000_000_000_000), true
			default:
				return nil, false
			}
		},
		func(pubkey string) (*float64, bool) {
			return floatPtr(165_951_523.412432), true
		},
	)
	defer srv.Close()

	result := rpc.NewDepthClient(rpc.NewClient(srv.URL, 2*time.Second)).FetchDepth(context.Background(), knownPoolAccount)
	if result.SOL >= 0 {
		t.Fatalf("huge non-WSOL token balance became real depth: %+v", result)
	}
}

func TestFetchDepth_HugeWSOLBalanceAllowedOnlyWhenVerified(t *testing.T) {
	ammData := buildAMMDataWithVaults(knownCoinVault, knownPCVault)
	pcVault := rpc.Base58EncodeExported(knownPCVault[:])
	coinVault := rpc.Base58EncodeExported(knownCoinVault[:])
	expectedDepth := 1_250_000.0

	srv := rpcServer(t,
		func(pubkey string) ([]byte, bool) {
			switch pubkey {
			case knownPoolAccount:
				return ammData, true
			case pcVault:
				return buildTokenAccountData(rpc.WSOLMint, 0), true
			case coinVault:
				return buildTokenAccountData("11111111111111111111111111111111", 0), true
			default:
				return nil, false
			}
		},
		func(pubkey string) (*float64, bool) {
			if pubkey == pcVault {
				return floatPtr(expectedDepth), true
			}
			return nil, false
		},
	)
	defer srv.Close()

	result := rpc.NewDepthClient(rpc.NewClient(srv.URL, 2*time.Second)).FetchDepth(context.Background(), knownPoolAccount)
	if result.SOL != expectedDepth || result.Source != rpc.LiquiditySourceWSOLVault {
		t.Fatalf("got %+v want verified huge WSOL depth", result)
	}
}

// TestPCVaultFromAMMData_OffsetCorrect verifies that the pc_vault bytes are
// read from the exact expected offset in the binary layout.
func TestPCVaultFromAMMData_OffsetCorrect(t *testing.T) {
	// Embed a sentinel value only at the pc_vault offset — all other bytes zero.
	// If the offset is wrong, the result will be all-zero and trigger an error.
	data := make([]byte, rpc.RaydiumAMMV4DataSize)
	sentinel := knownPCVault
	copy(data[rpc.PCVaultOffsetExported:], sentinel[:])

	got, err := rpc.PCVaultFromAMMData(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := rpc.Base58EncodeExported(sentinel[:])
	if got != want {
		t.Errorf("pc_vault = %q, want %q", got, want)
	}
}

// TestPCVaultFromAMMData_TooShort verifies that undersized data returns an error.
func TestPCVaultFromAMMData_TooShort(t *testing.T) {
	_, err := rpc.PCVaultFromAMMData(make([]byte, 100))
	if err == nil {
		t.Fatal("expected error for short data, got nil")
	}
}

// --- helpers to test u128 layout boundary ---

// TestAMMLayoutBoundary builds a synthetic 752-byte buffer where the 33rd
// u64 field (swapCoin2PcFee) is set to a sentinel value; verifies pc_vault
// reads from the correct post-u128 offset and is not confused by adjacent data.
func TestAMMLayoutBoundary(t *testing.T) {
	data := make([]byte, rpc.RaydiumAMMV4DataSize)

	// Set swapCoin2PcFee (u64 at offset 288) to a non-zero sentinel.
	binary.LittleEndian.PutUint64(data[288:], 0xDEADBEEF_CAFEBABE)

	// Set the pc_vault at the correct offset.
	copy(data[rpc.PCVaultOffsetExported:], knownPCVault[:])

	got, err := rpc.PCVaultFromAMMData(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := rpc.Base58EncodeExported(knownPCVault[:])
	if got != want {
		t.Errorf("pc_vault mismatch after sentinel boundary test: got %q, want %q", got, want)
	}
}
