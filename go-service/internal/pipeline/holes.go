package pipeline

import (
	"fmt"
	"math"

	"github.com/easyeda/eext-paden-integration/go-service/internal/geometry"
	"github.com/easyeda/eext-paden-integration/go-service/internal/problem"
)

// subtractTHTPadHoles drills pad holes out of every copper layer so that the
// remaining annular ring is correctly associated with the pad's net. Without
// this, a THT pad centre can fall inside a solid copper pour of the wrong net.
func subtractTHTPadHoles(layers []*problem.Layer, pads []Pad, transform *[4]float64, d *DiagCollector) {
	var holes []geometry.Polygon
	for _, p := range pads {
		if !p.IsTHT || p.HoleDiameter <= 0 {
			continue
		}
		x, y := p.X, p.Y
		if transform != nil {
			x = x*transform[0] + transform[2]
			y = y*transform[1] + transform[3]
		}
		holes = append(holes, circlePolygon(x, y, p.HoleDiameter/2))
	}
	if len(holes) == 0 {
		return
	}
	holeMP := geometry.MultiPolygon(holes)
	for _, layer := range layers {
		clipped, err := geometry.Difference(layer.Shape, holeMP)
		if err != nil || len(clipped) == 0 {
			d.Warn(fmt.Sprintf("Layer '%s': hole subtraction failed, keeping original", layer.Name))
			continue
		}
		layer.Shape = clipped
	}
}

func circlePolygon(cx, cy, r float64) geometry.Polygon {
	const n = 16
	ring := make(geometry.Ring, n)
	for i := 0; i < n; i++ {
		a := 2 * math.Pi * float64(i) / float64(n)
		ring[i] = geometry.Point{X: cx + r*math.Cos(a), Y: cy + r*math.Sin(a)}
	}
	return geometry.Polygon{ring}
}
