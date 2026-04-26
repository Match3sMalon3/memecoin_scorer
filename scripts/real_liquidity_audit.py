#!/usr/bin/env python3
"""
real_liquidity_audit.py
Validates that the real-pool-depth implementation is complete and correct.
Prints REAL_LIQUIDITY_AUDIT_PASS on success; exits non-zero on failure.
"""
import subprocess, sys, os, re

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FAILURES = []

def fail(msg):
    FAILURES.append(msg)
    print(f"  FAIL: {msg}")

def ok(msg):
    print(f"  ok:   {msg}")

def read(path):
    full = os.path.join(ROOT, path)
    if not os.path.exists(full):
        return None
    return open(full).read()

def contains(path, pattern, label):
    src = read(path)
    if src is None:
        fail(f"{path} does not exist")
        return
    if re.search(pattern, src):
        ok(label)
    else:
        fail(f"{path}: pattern not found — {label}")

def file_exists(path, label):
    if os.path.exists(os.path.join(ROOT, path)):
        ok(label)
    else:
        fail(f"missing file: {path} — {label}")


print("=== REAL LIQUIDITY AUDIT ===\n")

# ── 1. Package structure ────────────────────────────────────────────────────
print("[1] rpc package files")
file_exists("internal/rpc/client.go",    "client.go exists")
file_exists("internal/rpc/raydium.go",   "raydium.go exists")
file_exists("internal/rpc/depth.go",     "depth.go exists")
file_exists("internal/rpc/depth_test.go","depth_test.go exists")

# ── 2. getTokenAccountBalance implementation ────────────────────────────────
print("\n[2] GetTokenAccountBalance")
contains("internal/rpc/client.go",
    r"GetTokenAccountBalance",
    "GetTokenAccountBalance function defined")
contains("internal/rpc/client.go",
    r"getTokenAccountBalance",
    "getTokenAccountBalance RPC method name correct")
contains("internal/rpc/client.go",
    r"uiAmount",
    "uiAmount field parsed (not raw lamports)")
contains("internal/rpc/client.go",
    r"ErrAccountNotFound",
    "ErrAccountNotFound sentinel defined")

# ── 3. No getAccountInfo.lamports used as depth ─────────────────────────────
print("\n[3] getAccountInfo.lamports NOT used as depth")
depth_src = read("internal/rpc/depth.go") or ""
client_src = read("internal/rpc/client.go") or ""
raydium_src = read("internal/rpc/raydium.go") or ""
# GetAccountInfo is only used to decode account data, not for lamport balance
if "lamports" in depth_src:
    fail("depth.go references 'lamports' — must not use lamport balance as depth")
else:
    ok("depth.go does not use lamports as depth")
if "lamports" in raydium_src:
    fail("raydium.go references 'lamports'")
else:
    ok("raydium.go does not use lamports")

# ── 4. Raydium pc_vault offset ───────────────────────────────────────────────
print("\n[4] Raydium AMM V4 layout offsets")
contains("internal/rpc/raydium.go",
    r"pcVaultOffset\s*=\s*368",
    "pcVaultOffset = 368 (correct layout position)")
contains("internal/rpc/raydium.go",
    r"coinVaultOffset\s*=\s*336",
    "coinVaultOffset = 336 (correct layout position)")
contains("internal/rpc/raydium.go",
    r"PCVaultFromAMMData",
    "PCVaultFromAMMData function defined")
contains("internal/rpc/raydium.go",
    r"base58Encode",
    "base58 encoder present")

# ── 5. SwapEvent fields ──────────────────────────────────────────────────────
print("\n[5] SwapEvent fields")
contains("internal/model/types.go",
    r"PoolAccountAddr\s+string",
    "SwapEvent.PoolAccountAddr field")
contains("internal/model/types.go",
    r"RealPoolDepthSOL\s+float64",
    "SwapEvent.RealPoolDepthSOL field")
contains("internal/ingestor/normalize.go",
    r"RealPoolDepthSOL:\s*-1",
    "normalize sets RealPoolDepthSOL = -1 sentinel")
contains("internal/ingestor/normalize.go",
    r"PoolAccountAddr:\s*extractPoolAccount",
    "normalize calls extractPoolAccount")

# ── 6. TokenSnapshot evidence fields ────────────────────────────────────────
print("\n[6] TokenSnapshot evidence fields")
contains("internal/model/types.go",
    r"LiquidityEvidenceSource\s+string",
    "TokenSnapshot.LiquidityEvidenceSource field")
contains("internal/model/types.go",
    r"LiquidityProxyReliable\s+bool",
    "TokenSnapshot.LiquidityProxyReliable field")

# ── 7. Store: UpdateDepth + real depth logic ─────────────────────────────────
print("\n[7] store.UpdateDepth and real depth selection")
contains("internal/state/store.go",
    r"func \(s \*Store\) UpdateDepth",
    "Store.UpdateDepth method defined")
contains("internal/state/store.go",
    r"realPoolDepthSOL[:\s]*-1",
    "tokenState initialised with realPoolDepthSOL = -1")
contains("internal/state/store.go",
    r"realPoolDepthSOL >= 0",
    "deriveSnapshot checks realPoolDepthSOL >= 0")
contains("internal/state/store.go",
    r"raydium_pc_vault|liqSource",
    "store sets evidence source from real depth")

# ── 8. decision.go reads evidence from snapshot ──────────────────────────────
print("\n[8] decision.go uses snapshot evidence fields")
contains("internal/live/decision.go",
    r"snap\.LiquidityProxyReliable",
    "decision.go reads snap.LiquidityProxyReliable")
contains("internal/live/decision.go",
    r"snap\.LiquidityEvidenceSource",
    "decision.go reads snap.LiquidityEvidenceSource")
contains("internal/live/decision.go",
    r"liqSource\s*=\s*snap\.LiquidityEvidenceSource",
    "liqSource assigned from snapshot")

# ── 9. Ingestor wiring ───────────────────────────────────────────────────────
print("\n[9] cmd/ingestor wiring")
contains("cmd/ingestor/main.go",
    r"SOLANA_RPC_URL",
    "SOLANA_RPC_URL env var read")
contains("cmd/ingestor/main.go",
    r"depthClientFromEnv",
    "depthClientFromEnv function present")
contains("cmd/ingestor/main.go",
    r"makeApplyFn",
    "makeApplyFn helper present")
contains("cmd/ingestor/main.go",
    r"UpdateDepth",
    "store.UpdateDepth called after depth fetch")
contains("cmd/ingestor/main.go",
    r"rpc\.DepthClient",
    "rpc.DepthClient type referenced")

# ── 10. Fallback: observed_swaps_proxy still works ───────────────────────────
print("\n[10] observed_swaps_proxy fallback")
contains("internal/live/decision.go",
    r"LiquidityEvidenceObservedSwapsProxy",
    "fallback source constant still referenced")
contains("internal/state/store.go",
    r"observed_swaps_proxy",
    "store emits observed_swaps_proxy when no real depth")
contains("internal/rpc/depth.go",
    r"LiquiditySourceProxy",
    "UnavailableDepth uses proxy source label")

# ── 11. evidence_source = raydium_pc_vault when real ────────────────────────
print("\n[11] raydium_pc_vault source label")
contains("internal/rpc/depth.go",
    r'LiquiditySourcePCVault\s*=\s*"raydium_pc_vault"',
    'LiquiditySourcePCVault = "raydium_pc_vault"')
# reliable flag is set in store.go; depth.go only returns DepthResult
contains("internal/state/store.go",
    r"liqReliable\s*=\s*true",
    "store sets liqReliable = true when real depth available")

# ── 12. Test coverage ────────────────────────────────────────────────────────
print("\n[12] required test cases in depth_test.go")
test_src = read("internal/rpc/depth_test.go") or ""
for name, label in [
    ("TestGetTokenAccountBalance_Success",      "GetTokenAccountBalance success"),
    ("TestGetTokenAccountBalance_AccountNotFound", "account not found"),
    ("TestGetTokenAccountBalance_Timeout",      "timeout"),
    ("TestFetchDepth_FallbackDoesNotPanic",     "fallback does not panic"),
    ("TestFetchDepth_RealDepthOverridesProxy",  "real depth overrides observed proxy"),
    ("TestFetchDepth_ProxyFallbackWhenRPCFails","proxy fallback still works"),
    ("TestPCVaultFromAMMData_OffsetCorrect",    "pc_vault offset correct"),
]:
    if name in test_src:
        ok(f"test: {label}")
    else:
        fail(f"missing test: {name} — {label}")

# ── 13. docs/real_liquidity_discovery_gap.md ────────────────────────────────
print("\n[13] gap doc")
file_exists("docs/real_liquidity_discovery_gap.md", "gap doc exists")
gap = read("docs/real_liquidity_discovery_gap.md") or ""
for kw, label in [
    ("pc_vault",              "mentions pc_vault"),
    ("368",                   "states offset 368"),
    ("getTokenAccountBalance", "mentions getTokenAccountBalance"),
    ("getAccountInfo",         "mentions getAccountInfo for layout decode"),
    ("raydium_pc_vault",       "names the evidence source string"),
    ("observed_swaps_proxy",   "names the fallback source"),
    ("SOLANA_RPC_URL",         "names the required env var"),
]:
    if kw in gap:
        ok(f"gap doc: {label}")
    else:
        fail(f"gap doc missing: {label}")

# ── 14. go test ./... ────────────────────────────────────────────────────────
print("\n[14] go test ./...")
result = subprocess.run(
    ["go", "test", "./..."],
    cwd=ROOT,
    capture_output=True,
    text=True,
)
if result.returncode == 0:
    ok("all tests pass")
    for line in result.stdout.strip().splitlines():
        print(f"        {line}")
else:
    fail("go test ./... failed")
    print(result.stdout)
    print(result.stderr)

# ── Result ───────────────────────────────────────────────────────────────────
print()
if FAILURES:
    print(f"AUDIT FAILED — {len(FAILURES)} check(s) did not pass:")
    for f in FAILURES:
        print(f"  • {f}")
    sys.exit(1)
else:
    print("REAL_LIQUIDITY_AUDIT_PASS")
