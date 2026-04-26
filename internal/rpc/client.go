// Package rpc provides a minimal Solana JSON-RPC 2.0 client.
// Only the two methods needed for real pool depth discovery are implemented:
// GetAccountInfo (to decode AMM account layout and find pc_vault) and
// GetTokenAccountBalance (to read the SOL/WSOL reserve balance).
package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// ErrAccountNotFound is returned when the RPC responds with a null account value.
var ErrAccountNotFound = errors.New("rpc: account not found")

// Client is a minimal Solana JSON-RPC client backed by a single HTTP endpoint.
// All methods are safe for concurrent use.
type Client struct {
	url    string
	http   *http.Client
	nextID int64 // atomic
}

// NewClient constructs a Client for the given Solana RPC endpoint URL.
// Timeout applies per request; 5 seconds is a reasonable default.
func NewClient(rpcURL string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Client{
		url:  rpcURL,
		http: &http.Client{Timeout: timeout},
	}
}

// rpcRequest is the standard Solana JSON-RPC 2.0 request envelope.
type rpcRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      int64         `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *Client) nextRequestID() int64 {
	return atomic.AddInt64(&c.nextID, 1)
}

func (c *Client) call(ctx context.Context, method string, params []interface{}, out interface{}) error {
	reqBody, err := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      c.nextRequestID(),
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return fmt.Errorf("rpc marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("rpc build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("rpc http: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("rpc read body: %w", err)
	}

	var rr rpcResponse
	if err := json.Unmarshal(data, &rr); err != nil {
		return fmt.Errorf("rpc unmarshal: %w", err)
	}
	if rr.Error != nil {
		return fmt.Errorf("rpc error %d: %s", rr.Error.Code, rr.Error.Message)
	}
	return json.Unmarshal(rr.Result, out)
}

// getAccountInfoResult mirrors the Solana getAccountInfo response shape.
type getAccountInfoResult struct {
	Value *accountValue `json:"value"`
}

type accountValue struct {
	// Data is ["base64encodedBytes", "base64"] per the Solana RPC spec.
	Data []json.RawMessage `json:"data"`
}

// GetAccountInfo fetches the raw binary data for pubkey.
// Returns ErrAccountNotFound when the account does not exist on-chain.
func (c *Client) GetAccountInfo(ctx context.Context, pubkey string) ([]byte, error) {
	var result getAccountInfoResult
	err := c.call(ctx, "getAccountInfo", []interface{}{
		pubkey,
		map[string]string{"encoding": "base64"},
	}, &result)
	if err != nil {
		return nil, err
	}
	if result.Value == nil {
		return nil, ErrAccountNotFound
	}
	if len(result.Value.Data) < 1 {
		return nil, fmt.Errorf("rpc: getAccountInfo returned empty data array for %s", pubkey)
	}

	// data[0] is a quoted base64 string.
	var encoded string
	if err := json.Unmarshal(result.Value.Data[0], &encoded); err != nil {
		return nil, fmt.Errorf("rpc: decode data[0] for %s: %w", pubkey, err)
	}
	raw, err := decodeBase64(encoded)
	if err != nil {
		return nil, fmt.Errorf("rpc: base64 decode for %s: %w", pubkey, err)
	}
	return raw, nil
}

// getTokenAccountBalanceResult mirrors the Solana getTokenAccountBalance response.
type getTokenAccountBalanceResult struct {
	Value *tokenBalanceValue `json:"value"`
}

type tokenBalanceValue struct {
	// UIAmount is the human-readable balance (amount / 10^decimals).
	// For WSOL this is the SOL-denominated amount.
	UIAmount *float64 `json:"uiAmount"`
}

// GetTokenAccountBalance returns the token balance of pubkey as a UI amount
// (e.g. SOL for WSOL with decimals=9). Returns ErrAccountNotFound when the
// token account does not exist.
func (c *Client) GetTokenAccountBalance(ctx context.Context, pubkey string) (float64, error) {
	var result getTokenAccountBalanceResult
	err := c.call(ctx, "getTokenAccountBalance", []interface{}{pubkey}, &result)
	if err != nil {
		return 0, err
	}
	if result.Value == nil || result.Value.UIAmount == nil {
		return 0, ErrAccountNotFound
	}
	return *result.Value.UIAmount, nil
}
