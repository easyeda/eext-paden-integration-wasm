package mesh

import (
	"fmt"

	"github.com/easyeda/paden-wasm/internal/geometry"
)

// Triangulation holds vertices and triangle indices from earcut.
type Triangulation struct {
	Vertices []geometry.Point
	Triangles [][3]int
}

// Earcut triangulates a polygon with holes using the JS earcut library.
func Earcut(poly geometry.Polygon) (*Triangulation, error) {
	result, err := geometry.Call("earcutTriangulate", polygonsToJS(poly))
	if err != nil {
		return nil, fmt.Errorf("earcut failed: %w", err)
	}

	verticesJS := result.Get("vertices")
	trianglesJS := result.Get("triangles")

	vertexCount := verticesJS.Length() / 2
	vertices := make([]geometry.Point, vertexCount)
	for i := 0; i < vertexCount; i++ {
		vertices[i] = geometry.Point{
			X: verticesJS.Index(i * 2).Float(),
			Y: verticesJS.Index(i*2 + 1).Float(),
		}
	}

	triangleCount := trianglesJS.Length() / 3
	triangles := make([][3]int, triangleCount)
	for i := 0; i < triangleCount; i++ {
		triangles[i] = [3]int{
			trianglesJS.Index(i * 3).Int(),
			trianglesJS.Index(i*3 + 1).Int(),
			trianglesJS.Index(i*3 + 2).Int(),
		}
	}

	return &Triangulation{Vertices: vertices, Triangles: triangles}, nil
}

func polygonsToJS(poly geometry.Polygon) interface{} {
	out := make([]interface{}, len(poly))
	for j, ring := range poly {
		ringOut := make([]interface{}, len(ring))
		for k, p := range ring {
			ringOut[k] = map[string]interface{}{"x": p.X, "y": p.Y}
		}
		out[j] = ringOut
	}
	return out
}
