# Real Liquidity Discovery Gap

Date: 2026-04-26

## Summary

The Helius enhanced-transaction webhook does not expose the AMM `pc_vault` address
directly. As a result, `real_pool_depth_sol` cannot be populated from webhook data
alone and is set to `-1` (the unavailable sentinel) in all current paths. The system
falls back to `observed_swaps_proxy` (TotalBuySOL + TotalSellSOL) everywhere
`real_pool_depth_sol < 0`.

---

## Why pc_vault Matters

`pc_vault` is the Raydium AMM V4 pool's SOL/WSOL reserve token account. Its on-chain
SOL balance is the executable depth figure — the amount of SOL that can actually be
absorbed by the pool at a given slippage tolerance. The current `observed_swaps_proxy`
is cumulative swap flow, which:

- over-estimates depth on tokens with high historical volume but thin current reserves
- under-estimates depth on fresh tokens with few observed swaps but funded reserves
- cannot distinguish pool SOL from swapped-out-and-gone SOL

---

## What the Webhook Payload Actually Contains

### Available — enhanced transaction `events.swap`:

| Field | Value |
|---|---|
| `nativeInput.account` | User's SOL account (feePayer) |
| `nativeOutput.account` | User's SOL account (feePayer) |
| `tokenInputs[].fromUserAccount` | User's source token account |
| `tokenOutputs[].toUserAccount` | User's destination token account |
| `programInfo.account` | Top-level DEX program ID |

### Available — `innerInstructions[].instructions[]`:

| Field | Value |
|---|---|
| `programId` | CPI program ID (may be Raydium AMM V4 or Pump.fun) |
| `accounts` | Integer indices into the transaction's top-level `accounts` array |

The top-level `accounts` array contains all pubkeys for the transaction, including
pool-related accounts. For **Raydium AMM V4** (`675kPX9MHTjS2zt1qfr1NYHuzeLXfQM9H24wFSUt1Mp8`),
the swap CPI instruction account layout is:

| Index | Account |
|---|---|
| 0 | **AMM pool account** (extractable) |
| 1 | Raydium AMM authority |
| 2 | User transfer authority |
| 3 | User source token account |
| 4 | AMM open orders |
| 5 | AMM target orders |
| 6 | Pool coin token account (coin vault) |
| **7** | **Pool PC token account (pc_vault — SOL/WSOL reserve)** |
| 8 | Serum program ID |
| 9+ | Serum market accounts |

For **Pump.fun** (`6EF8rrecthR5Dkzon8Nwu78hRvfCKubJ14M5uBEwF6P`):

| Index | Account |
|---|---|
| 0 | Global state |
| 1 | Fee recipient |
| 2 | Token mint |
| **3** | **Bonding curve (pool equivalent — extractable)** |
| 4 | Associated bonding curve (token vault) |
| 5 | User's associated token account |
| 6 | User (feePayer) |

### Not Available in Webhook Payload:

- `pc_vault` pubkey directly (requires resolving from AMM account state via RPC)
- `pc_vault` SOL balance (requires getAccountInfo RPC call)
- Pool reserve ratio (coin reserve / pc reserve)

---

## Current Implementation

`internal/ingestor/normalize.go` → `extractPoolAccount()`:

- Scans `innerInstructions` for Raydium V4 and Pump.fun program IDs.
- For Raydium V4: returns `accounts[0]` (AMM pool account) from the matched instruction.
- For Pump.fun: returns `accounts[3]` (bonding curve) from the matched instruction.
- Returns `""` when no known DEX inner instruction is found or accounts are out of range.

`model.SwapEvent.PoolAccountAddr` — populated when extraction succeeds, `""` otherwise.  
`model.SwapEvent.RealPoolDepthSOL` — always `-1` in current paths (fallback sentinel).

---

## Required Next Step: RPC-Backed Depth Query

To populate `real_pool_depth_sol` with actual reserve depth:

1. **Confirm pool account**: use `SwapEvent.PoolAccountAddr` (already extracted).
2. **Resolve pc_vault pubkey**: call `getAccountInfo(poolAccountAddr)` and base64-decode
   the 752-byte Raydium AMM V4 `AmmInfo` binary layout. Read 32 bytes at byte offset
   **368** (`pcVaultOffset = 368` in `internal/rpc/raydium.go`) — that is
   `poolPcTokenAccount`, the WSOL reserve token account.
   Layout breakdown to reach offset 368: 32 × u64 (256 B) + 2 × u128 (32 B) + u64 (8 B)
   + 2 × u128 (32 B) + u64 (8 B) = 336 B, then coinVault pubkey (32 B) at 336,
   then pcVault pubkey (32 B) at **368**.
   For Pump.fun use the `realSolReserves` field from the bonding curve account layout.
3. **Query pc_vault balance**: call `getTokenAccountBalance(pcVaultAddr)` to get the
   current WSOL reserve amount (UI amount, decimals = 9, result is SOL).
4. **Populate `RealPoolDepthSOL`**: set on the event / call `store.UpdateDepth`.
   Set `LiquidityEvidenceSource = "raydium_pc_vault"` and `LiquidityProxyReliable = true`
   in the downstream snapshot.

**Environment variable to add**: `SOLANA_RPC_URL` (e.g. Helius RPC endpoint or a
dedicated private node). This is not configured yet.

**Latency budget**: a single `getTokenAccountBalance` call via Helius RPC adds ~50–150ms.
Cache pc_vault pubkeys keyed by pool account (they are stable) to avoid redundant
`getAccountInfo` calls. Refresh pool depth at most once per new swap event per token.

---

## Fallback Behaviour (Current)

When `real_pool_depth_sol == -1`:

- `LiquidityProxySOL` = `TotalBuySOL + TotalSellSOL` (observed_swaps_proxy)
- `LiquidityEvidenceSource` = `"observed_swaps_proxy"`
- `LiquidityProxyReliable` = `false`
- All gates, impact estimates, and early proxy scoring operate on the proxy
- Audit rows with `-1` depth are explicitly labelled as fallback — the audit passes
  as long as the fallback is declared and no gate treats the proxy as verified depth

---

## Audit Pass Condition

Rows with `real_pool_depth_sol = -1` pass the audit when:

1. `liquidity_evidence_source` is `"observed_swaps_proxy"` (not `"rpc_pc_vault"`)
2. `liquidity_proxy_reliable` is `false`
3. No user-facing copy describes the proxy as verified reserve depth
4. Early proxy logic does not promote DEAD → WATCH unless compensating buyer-flow
   evidence is present (existing `qualifiesForUnreliableLiquidityWatch` gate)

These conditions are satisfied in the current codebase. The gap is declared and the
fallback is explicit. No silent promotion occurs.
