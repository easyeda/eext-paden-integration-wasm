//go:build js && wasm

package main

import (
	"fmt"
	"syscall/js"
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

			// TODO: implement actual analysis pipeline.
			result := map[string]interface{}{
				"success": true,
				"message": "placeholder result",
				"layer_solutions": []interface{}{},
				"solver_info": map[string]interface{}{
					"ground_node_current": 0.0,
					"residual_norm":       0.0,
				},
				"connection_points": map[string]interface{}{},
				"layer_boundaries":  map[string]interface{}{},
				"diagnostics":       []interface{}{"[INFO] WASM runtime placeholder"},
				"current_warnings":  []interface{}{},
			}

			resolve.Invoke(js.Global().Get("JSON").Call("stringify", result))
		}()

		return nil
	})

	return js.Global().Get("Promise").New(handler)
}
