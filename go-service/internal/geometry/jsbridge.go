package geometry

import (
	"fmt"
	"syscall/js"
)

var padenGeometry js.Value

func geometryAPI() (js.Value, error) {
	if !padenGeometry.IsUndefined() {
		return padenGeometry, nil
	}
	api := js.Global().Get("padenGeometry")
	if api.IsUndefined() || api.IsNull() {
		return js.Value{}, fmt.Errorf("padenGeometry bridge not available")
	}
	padenGeometry = api
	return padenGeometry, nil
}

// Call invokes a JS function by name on padenGeometry and awaits its result.
func Call(name string, args ...interface{}) (js.Value, error) {
	api, err := geometryAPI()
	if err != nil {
		return js.Value{}, err
	}
	fn := api.Get(name)
	if fn.IsUndefined() || fn.IsNull() {
		return js.Value{}, fmt.Errorf("padenGeometry.%s is not defined", name)
	}
	jsArgs := make([]interface{}, len(args))
	copy(jsArgs, args)
	result := fn.Invoke(jsArgs...)
	return Await(result)
}

// Await unwraps a JS Promise.
func Await(promise js.Value) (js.Value, error) {
	if promise.Type() != js.TypeObject || promise.Get("then").IsUndefined() {
		return promise, nil
	}

	ch := make(chan struct {
		value js.Value
		err   error
	})

	onResolve := js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		var v js.Value
		if len(args) > 0 {
			v = args[0]
		}
		ch <- struct {
			value js.Value
			err   error
		}{value: v}
		return nil
	})
	defer onResolve.Release()

	onReject := js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		var err error
		if len(args) > 0 {
			err = fmt.Errorf("%s", args[0].String())
		}
		else {
			err = fmt.Errorf("promise rejected")
		}
		ch <- struct {
			value js.Value
			err   error
		}{err: err}
		return nil
	})
	defer onReject.Release()

	promise.Call("then", onResolve, onReject)
	r := <-ch
	return r.value, r.err
}

// polygonsToJS converts a MultiPolygon to the JS nested-object format.
func polygonsToJS(mp MultiPolygon) interface{} {
	out := make([]interface{}, len(mp))
	for i, poly := range mp {
		polyOut := make([]interface{}, len(poly))
		for j, ring := range poly {
			ringOut := make([]interface{}, len(ring))
			for k, p := range ring {
				ringOut[k] = map[string]interface{}{"x": p.X, "y": p.Y}
			}
			polyOut[j] = ringOut
		}
		out[i] = polyOut
	}
	return out
}

// polygonsFromJS parses the JS nested-object format back to a MultiPolygon.
func polygonsFromJS(v js.Value) (MultiPolygon, error) {
	if v.IsUndefined() || v.IsNull() {
		return nil, nil
	}
	n := v.Length()
	mp := make(MultiPolygon, n)
	for i := 0; i < n; i++ {
		poly := v.Index(i)
		polyN := poly.Length()
		p := make(Polygon, polyN)
		for j := 0; j < polyN; j++ {
			ring := poly.Index(j)
			ringN := ring.Length()
			r := make(Ring, ringN)
			for k := 0; k < ringN; k++ {
				pt := ring.Index(k)
				r[k] = Point{X: pt.Get("x").Float(), Y: pt.Get("y").Float()}
			}
			p[j] = r
		}
		mp[i] = p
	}
	return mp, nil
}
