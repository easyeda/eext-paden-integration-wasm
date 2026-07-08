//go:build !(js && wasm)

package mesh

import (
	"fmt"

	"github.com/easyeda/eext-paden-integration/go-service/internal/geometry"
)

// Earcut is a non-WASM stub; triangulation requires the JS earcut bridge.
func Earcut(poly geometry.Polygon) (*Triangulation, error) {
	return nil, fmt.Errorf("earcut triangulation is only available in WASM builds")
}
