package pipeline

import (
	"github.com/easyeda/paden-wasm/internal/geometry"
)

func pointInPolygonMesh(p geometry.Point, poly geometry.Polygon) bool {
	if len(poly) == 0 {
		return false
	}
	if !pointInRingMesh(p, poly[0]) {
		return false
	}
	for i := 1; i < len(poly); i++ {
		if pointInRingMesh(p, poly[i]) {
			return false
		}
	}
	return true
}

func pointInRingMesh(p geometry.Point, ring geometry.Ring) bool {
	n := len(ring)
	if n < 3 {
		return false
	}
	inside := false
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		xi, yi := ring[i].X, ring[i].Y
		xj, yj := ring[j].X, ring[j].Y
		if ((yi > p.Y) != (yj > p.Y)) &&
			(p.X < (xj-xi)*(p.Y-yi)/(yj-yi)+xi) {
			inside = !inside
		}
	}
	return inside
}
