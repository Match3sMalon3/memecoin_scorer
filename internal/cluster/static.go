package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// StaticResolver resolves parents from a fixed map[wallet]→parent loaded at startup.
// It is safe for concurrent use (the map is read-only after construction).
//
// Load from a JSON file with LoadStaticResolver or build from a map with NewStaticResolver.
//
// JSON format:
//
//	{
//	  "walletAddr1": "parentAddr",
//	  "walletAddr2": "parentAddr",
//	  "walletAddr3": "independentParent"
//	}
type StaticResolver struct {
	parents map[string]string // wallet → canonical parent; read-only after construction
}

// NewStaticResolver constructs a StaticResolver from an existing map.
// The map is copied; the caller may modify the original after construction.
func NewStaticResolver(m map[string]string) *StaticResolver {
	cp := make(map[string]string, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return &StaticResolver{parents: cp}
}

// LoadStaticResolver reads a JSON funder-map from path and returns a StaticResolver.
// Returns an error if the file is missing, unreadable, or contains invalid JSON.
// An empty file or empty object is valid — it produces a resolver equivalent to NullResolver.
func LoadStaticResolver(path string) (*StaticResolver, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cluster: read funder map %q: %w", path, err)
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("cluster: parse funder map %q: %w", path, err)
	}
	return NewStaticResolver(m), nil
}

// ResolveParent implements FunderResolver.
// Returns (parent, true, nil) when the wallet is in the map.
// Returns (wallet, false, nil) when the wallet is not in the map (wallet is its own root).
func (r *StaticResolver) ResolveParent(_ context.Context, wallet string, _ time.Time) (string, bool, error) {
	if parent, ok := r.parents[wallet]; ok {
		return parent, true, nil
	}
	return wallet, false, nil
}

// Len returns the number of wallet→parent mappings held by the resolver.
func (r *StaticResolver) Len() int {
	return len(r.parents)
}

// IsHealthy implements HealthyResolver.  StaticResolver is always healthy once constructed.
func (r *StaticResolver) IsHealthy() bool { return true }

// BackendName implements HealthyResolver.
func (r *StaticResolver) BackendName() string { return "static" }
