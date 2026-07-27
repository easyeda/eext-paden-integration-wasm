//go:build !js || !wasm

package pipeline

// echoDiag is a no-op on non-WASM targets. Real diagnostics still accumulate
// in DiagCollector.Lines; only the live console mirror is WASM-specific.
func echoDiag(line string) {}