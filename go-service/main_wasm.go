//go:build js && wasm

package main

import (
	"encoding/json"
	"fmt"
	"runtime/debug"
	"syscall/js"

	"github.com/easyeda/eext-paden-integration/go-service/internal/pipeline"
	"github.com/easyeda/eext-paden-integration/go-service/internal/wasmapi"
)

func main() {
	fmt.Println("[PADEN WASM] runtime initialized")

	padne := js.Global().Get("Object").New()
	padne.Set("version", js.FuncOf(version))
	padne.Set("analyzeODB", js.FuncOf(analyzeODB))

	js.Global().Set("padne", padne)

	// Keep the Go runtime alive.
	select {}
}

func version(this js.Value, args []js.Value) interface{} {
	return js.ValueOf("1.0.0-wasm")
}

func analyzeODB(this js.Value, args []js.Value) interface{} {
	handler := js.FuncOf(func(this js.Value, p []js.Value) interface{} {
		resolve := p[0]
		reject := p[1]

		go func() {
			defer func() {
				if r := recover(); r != nil {
					fmt.Printf("[PADEN WASM] panic: %v\n%s\n", r, debug.Stack())
					reject.Invoke(js.Global().Get("Error").New(fmt.Sprintf("panic: %v", r)))
				}
			}()

			if len(args) < 2 {
				reject.Invoke(js.Global().Get("Error").New("expected 2 arguments: odbTgz ArrayBuffer, configJson string"))
				return
			}

			odbJs := args[0]
			// syscall/js CopyBytesToGo requires a Uint8Array view, not a raw ArrayBuffer.
			if odbJs.InstanceOf(js.Global().Get("ArrayBuffer")) {
				odbJs = js.Global().Get("Uint8Array").New(odbJs)
			}
			if !odbJs.InstanceOf(js.Global().Get("Uint8Array")) && !odbJs.InstanceOf(js.Global().Get("Uint8ClampedArray")) {
				reject.Invoke(js.Global().Get("Error").New("odbBytes must be Uint8Array or ArrayBuffer"))
				return
			}
			odbBytes := make([]byte, odbJs.Get("byteLength").Int())
			js.CopyBytesToGo(odbBytes, odbJs)
			configJson := args[1].String()

			fmt.Printf("[PADEN WASM] analyzeODB called: %d bytes, config length %d\n", len(odbBytes), len(configJson))

			sol, d, err := pipeline.Analyze(odbBytes, configJson)
			if err != nil {
				errResult := map[string]interface{}{
					"success":     false,
					"message":     err.Error(),
					"diagnostics": d.Lines,
				}
				errJSON, _ := json.Marshal(errResult)
				resolve.Invoke(js.ValueOf(string(errJSON)))
				return
			}

			jsonBytes, err := wasmapi.SerializeSolution(sol)
			if err != nil {
				errResult := map[string]interface{}{
					"success":     false,
					"message":     fmt.Sprintf("serialization failed: %v", err),
					"diagnostics": d.Lines,
				}
				errJSON, _ := json.Marshal(errResult)
				resolve.Invoke(js.ValueOf(string(errJSON)))
				return
			}

			resolve.Invoke(js.ValueOf(string(jsonBytes)))
		}()

		return nil
	})

	return js.Global().Get("Promise").New(handler)
}
