//go:build !(js && wasm)

package geometry

import "fmt"

// CDTTriangulate is a non-WASM stub; constrained Delaunay triangulation
// requires the JS triangle-wasm bridge.
func CDTTriangulate(poly Polygon, seedPts []Point, minAngle, maxArea float64) ([]Point, [][3]int, error) {
	return nil, nil, fmt.Errorf("cdt triangulation is only available in WASM builds")
}