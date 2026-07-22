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
	const n = 32
	ring := make(geometry.Ring, n)
	for i := 0; i < n; i++ {
		a := 2 * math.Pi * float64(i) / float64(n)
		ring[i] = geometry.Point{X: cx + r*math.Cos(a), Y: cy + r*math.Sin(a)}
	}
	return geometry.Polygon{ring}
}

// punchViaHoles subtracts via drill holes from every copper layer that the via
// passes through. This creates anti-pads around vias and prevents a via of one
// net from being modelled as solid copper belonging to a different net.
func punchViaHoles(layers []*problem.Layer, specs []viaSpec, d *DiagCollector) {
	if len(specs) == 0 {
		return
	}
	for _, layer := range layers {
		var holes geometry.MultiPolygon
		for _, vs := range specs {
			if layerNameIn(vs.LayerNames, layer.Name) {
				r := vs.DrillDiameter / 2
				if r <= 0 {
					continue
				}
				holes = append(holes, circlePolygon(vs.Point.X, vs.Point.Y, r))
			}
		}
		if len(holes) == 0 {
			continue
		}
		merged, err := geometry.Union(holes, nil)
		if err != nil || len(merged) == 0 {
			merged = holes
		}
		punched, err := geometry.Difference(layer.Shape, merged)
		if err != nil || len(punched) == 0 {
			d.Warn(fmt.Sprintf("Layer '%s': via hole punch failed, keeping original", layer.Name))
			continue
		}
		layer.Shape = punched
		d.Info(fmt.Sprintf("Layer '%s': punched %d via hole(s) -> %d polygon(s)", layer.Name, len(holes), len(punched)))
	}
}

func layerNameIn(names []string, target string) bool {
	for _, n := range names {
		if n == target {
			return true
		}
	}
	return false
}

// removeTinyPolygons drops polygons whose area is below the threshold. Tiny
// slivers produced by boolean operations do not contribute to the physics and
// can create z-fighting / broken faces in the WebGL preview.
func removeTinyPolygons(mp geometry.MultiPolygon, minArea float64) geometry.MultiPolygon {
	if len(mp) == 0 {
		return mp
	}
	out := make(geometry.MultiPolygon, 0, len(mp))
	for _, poly := range mp {
		area := 0.0
		for i, ring := range poly {
			a := math.Abs(ring.Area())
			if i == 0 {
				area += a
			} else {
				area -= a
			}
		}
		if area >= minArea {
			out = append(out, poly)
		}
	}
	return out
}
