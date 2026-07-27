//go:build js && wasm

package pipeline

import "fmt"

// echoDiag mirrors a diagnostic line to the JavaScript console so the WASM
// worker's console.log interceptor forwards it to the analyzing dialog. It is
// a no-op on non-WASM targets (see log_other.go).
func echoDiag(line string) {
	fmt.Println(line)
}