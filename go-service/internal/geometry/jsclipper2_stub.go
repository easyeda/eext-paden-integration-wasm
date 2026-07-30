//go:build !(js && wasm)

package geometry

import "fmt"

// Union, Difference, Intersect, Offset, Close all delegate to the Clipper2
// WASM bridge at runtime. In a native build (tests, CLI tools) they fall back
// to errors; callers that exercise the polygon pipeline should only run under
// the WASM target.
func Union(a, b MultiPolygon) (MultiPolygon, error) {
	return nil, fmt.Errorf("geometry.Union requires the WASM build")
}

func Difference(subject, clip MultiPolygon) (MultiPolygon, error) {
	return nil, fmt.Errorf("geometry.Difference requires the WASM build")
}

func Intersect(a, b MultiPolygon) (MultiPolygon, error) {
	return nil, fmt.Errorf("geometry.Intersect requires the WASM build")
}

func Offset(mp MultiPolygon, delta float64) (MultiPolygon, error) {
	return nil, fmt.Errorf("geometry.Offset requires the WASM build")
}

func Close(mp MultiPolygon, delta float64) (MultiPolygon, error) {
	return nil, fmt.Errorf("geometry.Close requires the WASM build")
}
