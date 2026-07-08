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
	padne.Set("analyzeGerber", js.FuncOf(analyzeGerber))

	js.Global().Set("padne", padne)

	// Keep the Go runtime alive.
	select {}
}

func version(this js.Value, args []js.Value) interface{} {
	return js.ValueOf("1.0.0-wasm")
}

func analyzeGerber(this js.Value, args []js.Value) interface{} {
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
				reject.Invoke(js.Global().Get("Error").New("expected 2 arguments: gerberZip ArrayBuffer, configJson string"))
				return
			}

			gerberJs := args[0]
			// syscall/js CopyBytesToGo requires a Uint8Array view, not a raw ArrayBuffer.
			if gerberJs.InstanceOf(js.Global().Get("ArrayBuffer")) {
				gerberJs = js.Global().Get("Uint8Array").New(gerberJs)
			}
			if !gerberJs.InstanceOf(js.Global().Get("Uint8Array")) && !gerberJs.InstanceOf(js.Global().Get("Uint8ClampedArray")) {
				reject.Invoke(js.Global().Get("Error").New("gerberBytes must be Uint8Array or ArrayBuffer"))
				return
			}
			gerberBytes := make([]byte, gerberJs.Get("byteLength").Int())
			js.CopyBytesToGo(gerberBytes, gerberJs)
			configJson := args[1].String()

			fmt.Printf("[PADEN WASM] analyzeGerber called: %d bytes, config length %d\n", len(gerberBytes), len(configJson))

			sol, d, err := pipeline.Analyze(gerberBytes, configJson)
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
