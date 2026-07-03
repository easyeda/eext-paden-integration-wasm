//go:build js && wasm

package main

import (
	"encoding/json"
	"fmt"
	"syscall/js"

	"github.com/easyeda/paden-wasm/internal/pipeline"
	"github.com/easyeda/paden-wasm/internal/wasmapi"
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
			if len(args) < 2 {
				reject.Invoke(js.Global().Get("Error").New("expected 2 arguments: gerberZip ArrayBuffer, configJson string"))
				return
			}

			gerberBytes := make([]byte, args[0].Get("byteLength").Int())
			js.CopyBytesToGo(gerberBytes, args[0])
			configJson := args[1].String()

			fmt.Printf("[PADEN WASM] analyzeGerber called: %d bytes, config length %d\n", len(gerberBytes), len(configJson))

			sol, d, err := pipeline.Analyze(gerberBytes, configJson)
			if err != nil {
				errResult := map[string]interface{}{
					"success":     false,
					"message":     err.Error(),
					"diagnostics": d.Lines,
				}
				resolve.Invoke(js.Global().Get("JSON").Call("stringify", errResult))
				return
			}

			jsonBytes, err := wasmapi.SerializeSolution(sol)
			if err != nil {
				errResult := map[string]interface{}{
					"success":     false,
					"message":     fmt.Sprintf("serialization failed: %v", err),
					"diagnostics": d.Lines,
				}
				resolve.Invoke(js.Global().Get("JSON").Call("stringify", errResult))
				return
			}

			resolve.Invoke(js.Global().Get("JSON").Call("stringify", json.RawMessage(jsonBytes)))
		}()

		return nil
	})

	return js.Global().Get("Promise").New(handler)
}
