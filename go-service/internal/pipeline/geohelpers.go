package pipeline

import (
	"math"

	"github.com/easyeda/eext-paden-integration/go-service/internal/geometry"
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

// pointInsidePolygonRings reports whether pt sits in any of the polygon's
// rings (outer or hole). Used to detect pad centres that fall inside drilled
// holes after anti-pad subtraction.
func pointInsidePolygonRings(p geometry.Point, poly geometry.Polygon) bool {
	for _, ring := range poly {
		if pointInRingMesh(p, ring) {
			return true
		}
	}
	return false
}

// transformPoint maps an EasyEDA-space point into geometry space using the
// [scaleX, scaleY, offsetX, offsetY] tuple computed by computeCoordinateTransform.
// A nil transform passes the point through unchanged.
func transformPoint(x, y float64, transform *[4]float64) geometry.Point {
	if transform == nil {
		return geometry.Point{X: x, Y: y}
	}
	return geometry.Point{X: x*transform[0] + transform[2], Y: y*transform[1] + transform[3]}
}

// distanceToSegment returns the shortest Euclidean distance from p to the
// line segment a-b.
func distanceToSegment(p, a, b geometry.Point) float64 {
	dx := b.X - a.X
	dy := b.Y - a.Y
	if dx == 0 && dy == 0 {
		return math.Hypot(p.X-a.X, p.Y-a.Y)
	}
	t := ((p.X-a.X)*dx + (p.Y-a.Y)*dy) / (dx*dx + dy*dy)
	if t < 0 {
		return math.Hypot(p.X-a.X, p.Y-a.Y)
	}
	if t > 1 {
		return math.Hypot(p.X-b.X, p.Y-b.Y)
	}
	return math.Hypot(p.X-(a.X+t*dx), p.Y-(a.Y+t*dy))
}