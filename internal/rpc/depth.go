package rpc

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	// LiquiditySourcePCVault is kept for legacy readers; new verified depth uses LiquiditySourceWSOLVault.
	LiquiditySourcePCVault = "raydium_pc_vault"
	// LiquiditySourceWSOLVault is set only when a Raydium vault's SPL token mint is verified as WSOL.
	LiquiditySourceWSOLVault = "raydium_wsol_vault"
	// LiquiditySourceProxy is the fallback observed-swap-flow source name.
	LiquiditySourceProxy = "observed_swaps_proxy"

	// depthCacheTTL is how long a fetched depth value is reused before a fresh RPC call.
	// Short enough to catch meaningful reserve changes; long enough to avoid hammering RPC.
	depthCacheTTL = 5 * time.Second
)

var ErrNoWSOLVault = errors.New("rpc: raydium pool has no verified WSOL vault")

// DepthResult holds a fetched pool depth plus its evidence label.
type DepthResult struct {
	SOL    float64 // >= 0 when real depth available; -1 = unavailable
	Source string  // LiquiditySourceWSOLVault or LiquiditySourceProxy
}

// UnavailableDepth is the sentinel returned when real depth cannot be fetched.
var UnavailableDepth = DepthResult{SOL: -1, Source: LiquiditySourceProxy}

type cachedDepth struct {
	sol float64
	at  time.Time
}

type resolvedVault struct {
	addr string
	err  error
}

// DepthClient fetches real Raydium AMM V4 pool depth via Solana JSON-RPC.
//
// Discovery path:
//  1. Given the AMM pool account address (from SwapEvent.PoolAccountAddr).
//  2. Call getAccountInfo(poolAccount) to fetch the 752-byte AmmInfo layout.
//  3. Decode coin_vault and pc_vault pubkeys.
//  4. Fetch each vault account with getAccountInfo and verify its SPL token mint is WSOL.
//  5. Call getTokenAccountBalance on the verified WSOL vault.
//  6. Return the UI amount (WSOL / 10^9 = SOL).
//
// Caching:
//   - Verified WSOL vault addresses are cached permanently per pool account.
//   - Depth values are cached for depthCacheTTL per verified WSOL vault address.
//
// All errors result in DepthResult{SOL: -1} — callers always fall back gracefully.
type DepthClient struct {
	rpc        *Client
	poolCache  sync.Map // poolAccountAddr string → verified WSOL vault addr string
	depthCache sync.Map // WSOL vault addr string → cachedDepth

	// inflight deduplication: prevents stampede when many events arrive simultaneously
	// for the same pool account before the first RPC call completes.
	inflightMu sync.Mutex
	inflight   map[string]*inflightEntry
}

type inflightEntry struct {
	wg    sync.WaitGroup
	vault resolvedVault
}

// NewDepthClient wraps an rpc.Client with caching and inflight dedup.
func NewDepthClient(c *Client) *DepthClient {
	return &DepthClient{
		rpc:      c,
		inflight: make(map[string]*inflightEntry),
	}
}

// FetchDepth returns the real SOL depth for the Raydium AMM pool identified by
// poolAccountAddr. Returns UnavailableDepth when any step fails or when
// poolAccountAddr is empty.
//
// This call may block for up to the Client's configured HTTP timeout on a cache miss.
// Callers should invoke it from a goroutine and apply their own context deadline.
func (d *DepthClient) FetchDepth(ctx context.Context, poolAccountAddr string) DepthResult {
	if poolAccountAddr == "" {
		return UnavailableDepth
	}

	wsolVault, err := d.resolveWSOLVault(ctx, poolAccountAddr)
	if err != nil || wsolVault == "" {
		return UnavailableDepth
	}

	// Check depth cache.
	if v, ok := d.depthCache.Load(wsolVault); ok {
		cd := v.(cachedDepth)
		if time.Since(cd.at) < depthCacheTTL {
			if cd.sol >= 0 {
				return DepthResult{SOL: cd.sol, Source: LiquiditySourceWSOLVault}
			}
			return UnavailableDepth
		}
	}

	// Fetch fresh balance from the already verified WSOL vault.
	sol, err := d.rpc.GetTokenAccountBalance(ctx, wsolVault)
	if err != nil {
		// Cache the failure briefly to avoid retry storms.
		d.depthCache.Store(wsolVault, cachedDepth{sol: -1, at: time.Now()})
		return UnavailableDepth
	}

	d.depthCache.Store(wsolVault, cachedDepth{sol: sol, at: time.Now()})
	return DepthResult{SOL: sol, Source: LiquiditySourceWSOLVault}
}

// resolveWSOLVault returns the verified WSOL vault address for poolAccountAddr,
// fetching and caching it via getAccountInfo when not already known.
func (d *DepthClient) resolveWSOLVault(ctx context.Context, poolAccountAddr string) (string, error) {
	// Fast path: permanent cache hit.
	if v, ok := d.poolCache.Load(poolAccountAddr); ok {
		return v.(string), nil
	}

	// In-flight dedup.
	d.inflightMu.Lock()
	if e, ok := d.inflight[poolAccountAddr]; ok {
		d.inflightMu.Unlock()
		e.wg.Wait()
		return e.vault.addr, e.vault.err
	}
	e := &inflightEntry{}
	e.wg.Add(1)
	d.inflight[poolAccountAddr] = e
	d.inflightMu.Unlock()

	defer func() {
		e.wg.Done()
		d.inflightMu.Lock()
		delete(d.inflight, poolAccountAddr)
		d.inflightMu.Unlock()
	}()

	data, err := d.rpc.GetAccountInfo(ctx, poolAccountAddr)
	if err != nil {
		e.vault.err = err
		return "", err
	}

	coinVault, pcVault, err := VaultsFromAMMData(data)
	if err != nil {
		e.vault.err = err
		return "", err
	}

	for _, vault := range []string{pcVault, coinVault} {
		info, err := d.rpc.GetTokenAccountInfo(ctx, vault)
		if err != nil {
			e.vault.err = err
			return "", err
		}
		if info.Mint == WSOLMint {
			d.poolCache.Store(poolAccountAddr, vault)
			e.vault.addr = vault
			return vault, nil
		}
	}

	e.vault.err = ErrNoWSOLVault
	return "", ErrNoWSOLVault
}
