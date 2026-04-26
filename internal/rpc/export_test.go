// export_test.go exposes package-internal constants and functions for use in
// external test files (package rpc_test). Nothing here is part of the public API.
package rpc

const (
	RaydiumAMMV4DataSize  = raydiumAMMV4DataSize
	PCVaultOffsetExported = pcVaultOffset
)

// Base58EncodeExported exposes the internal base58 encoder for test assertions.
func Base58EncodeExported(b []byte) string { return base58Encode(b) }
