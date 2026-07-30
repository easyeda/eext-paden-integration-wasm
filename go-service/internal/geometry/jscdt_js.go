//go:build js && wasm

package geometry

import (
	"fmt"
	"syscall/js"
)

// CDTTriangulate runs Shewchuk Triangle (via the triangle-wasm JS bridge) on
// one polygon with optional holes, with the given seed points added as forced
// Steiner vertices. minAngle is the minimum interior angle (degrees) and
// maxArea is the maximum triangle area (mm^2). The returned triangle soup
// has no duplicate vertices (Triangle's exact arithmetic + -j jettison drops
// coincident input points).
func CDTTriangulate(poly Polygon, seedPts []Point, minAngle, maxArea float64) ([]Point, [][3]int, error) {
	opts := map[string]interface{}{
		"minAngle": minAngle,
		"maxArea":  maxArea,
	}
	result, err := Call("cdtTriangulate", polygonsToJS([]Polygon{poly}), pointsToJS(seedPts), opts)
	if err != nil {
		return nil, nil, fmt.Errorf("cdt triangulate failed: %w", err)
	}
	vertices, triangles, err := typedArrayTriFromJS(result)
	if err != nil {
		return nil, nil, fmt.Errorf("cdt triangulate result decode: %w", err)
	}
	return vertices, triangles, nil
}

// pointsToJS converts []Point to the nested [{x,y}, ...] array shape expected
// by the geometry bridge.
func pointsToJS(pts []Point) interface{} {
	out := make([]interface{}, len(pts))
	for i, p := range pts {
		out[i] = map[string]interface{}{"x": p.X, "y": p.Y}
	}
	return out
}

// typedArrayTriFromJS decodes the { vertices: Float64Array, triangles:
// Uint32Array } result returned by cdtTriangulate into the in-memory
// triangle soup used by the rest of the mesh package.
func typedArrayTriFromJS(v js.Value) ([]Point, [][3]int, error) {
	verticesVal := v.Get("vertices")
	trianglesVal := v.Get("triangles")
	if verticesVal.IsUndefined() || verticesVal.IsNull() {
		return nil, nil, fmt.Errorf("cdt result missing vertices")
	}
	if trianglesVal.IsUndefined() || trianglesVal.IsNull() {
		return nil, nil, fmt.Errorf("cdt result missing triangles")
	}
	vLen := verticesVal.Length()
	if vLen%2 != 0 {
		return nil, nil, fmt.Errorf("cdt vertices length %d is not even", vLen)
	}
	pts := make([]Point, vLen/2)
	for i := 0; i < vLen; i += 2 {
		pts[i/2] = Point{X: verticesVal.Index(i).Float(), Y: verticesVal.Index(i + 1).Float()}
	}
	tLen := trianglesVal.Length()
	if tLen%3 != 0 {
		return nil, nil, fmt.Errorf("cdt triangles length %d is not a multiple of 3", tLen)
	}
	tris := make([][3]int, tLen/3)
	for i := 0; i < tLen; i += 3 {
		tris[i/3] = [3]int{
			trianglesVal.Index(i).Int(),
			trianglesVal.Index(i + 1).Int(),
			trianglesVal.Index(i + 2).Int(),
		}
	}
	return pts, tris, nil
}