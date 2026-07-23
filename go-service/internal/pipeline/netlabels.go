package pipeline

import (
	"fmt"
	"math"

	"github.com/easyeda/eext-paden-integration/go-service/internal/problem"
	"github.com/easyeda/eext-paden-integration/go-service/internal/geometry"
)

// inferPolygonNets labels every polygon in each layer with the net of the pads
// that fall inside it. Empty label means the net could not be inferred.
// If onlyEmpty is true, polygons that already have a net label are skipped.
func inferPolygonNets(layers []*problem.Layer, pads []Pad, transform *[4]float64, d *DiagCollector, onlyEmpty bool) {
	if !onlyEmpty {
		for _, l := range layers {
			l.NetLabels = make([]string, len(l.Shape))
		}
	} else {
		for _, l := range layers {
			if len(l.NetLabels) != len(l.Shape) {
				l.NetLabels = make([]string, len(l.Shape))
			}
		}
	}
	if len(pads) == 0 {
		return
	}

	type padInfo struct {
		pt           geometry.Point
		net          string
		layer        string
		tht          bool
		holeDiameter float64
	}
	infos := make([]padInfo, 0, len(pads))
	for _, p := range pads {
		x, y := p.X, p.Y
		if transform != nil {
			x = x*transform[0] + transform[2]
			y = y*transform[1] + transform[3]
		}
		infos = append(infos, padInfo{
			pt:           geometry.Point{X: x, Y: y},
			net:          p.Net,
			layer:        p.Layer,
			tht:          p.IsTHT,
			holeDiameter: p.HoleDiameter,
		})
	}

	conflicts := 0
	for _, l := range layers {
		votes := make([]map[string]int, len(l.Shape))
		for i := range votes {
			votes[i] = make(map[string]int)
		}
		for _, pi := range infos {
			if !pi.tht && pi.layer != l.Name {
				continue
			}
			if pi.tht {
				// A THT pad sits in a drilled hole; the copper belonging to the pad is
				// the annular ring on every layer it passes through. Do NOT vote based
				// on the pad centre being inside a polygon exterior — that would wrongly
				// label a large copper pour with the pad's net when the pad is isolated
				// by an anti-pad. Instead sample the annular ring at several radii to
				// catch both small rings and rings connected to a pour via thermal
				// relief.
				radii := []float64{pi.holeDiameter * 0.55, pi.holeDiameter * 0.7, pi.holeDiameter * 0.85}
				if pi.holeDiameter <= 0 {
					radii = []float64{0.2, 0.35, 0.5}
				}
				for _, radius := range radii {
					for _, probe := range thtAnnularProbes(pi.pt, radius) {
						for polyIdx, poly := range l.Shape {
							if pointInPolygonMesh(probe, poly) {
								if pi.net != "" {
									votes[polyIdx][pi.net]++
								}
							}
						}
					}
				}
				continue
			}
			for polyIdx, poly := range l.Shape {
				if pointInPolygonMesh(pi.pt, poly) {
					if pi.net != "" {
						votes[polyIdx][pi.net]++
					}
				}
			}
		}
		for i, v := range votes {
			if onlyEmpty && l.NetLabels[i] != "" {
				continue
			}
			bestNet := ""
			bestCnt := 0
			total := 0
			for net, cnt := range v {
				total += cnt
				if cnt > bestCnt {
					bestCnt = cnt
					bestNet = net
				}
			}
			// Flag polygons that contain pads from more than one net.
			if total > bestCnt {
				conflicts++
			}
			l.NetLabels[i] = bestNet
		}
	}
	if conflicts > 0 {
		d.Warn(fmt.Sprintf("Net inference: %d polygons contain pads from multiple nets", conflicts))
	}
}

// inferPolygonNetsFromTracks labels polygons that contain track endpoints.
// This helps connect trace runs that do not pass through any labelled pad.
func inferPolygonNetsFromTracks(layers []*problem.Layer, tracks []Track, layerIDToName map[int]string, transform *[4]float64) {
	for _, t := range tracks {
		if t.Net == "" {
			continue
		}
		layerName := layerIDToName[t.Layer]
		if layerName == "" {
			continue
		}
		var layer *problem.Layer
		for _, l := range layers {
			if l.Name == layerName {
				layer = l
				break
			}
		}
		if layer == nil {
			continue
		}
		for _, p := range []geometry.Point{
			transformPoint(t.X1, t.Y1, transform),
			transformPoint(t.X2, t.Y2, transform),
		} {
			for i, poly := range layer.Shape {
				if pointInPolygonMesh(p, poly) {
					if layer.NetLabels[i] == "" {
						layer.NetLabels[i] = t.Net
					}
				}
			}
		}
	}
}

func transformPoint(x, y float64, transform *[4]float64) geometry.Point {
	if transform != nil {
		return geometry.Point{X: x*transform[0] + transform[2], Y: y*transform[1] + transform[3]}
	}
	return geometry.Point{X: x, Y: y}
}

// pointInsidePolygonRings reports whether p is inside any ring (exterior or hole)
// of poly. This is used for THT pad net inference where the pad centre lies in
// the drilled hole.
func pointInsidePolygonRings(p geometry.Point, poly geometry.Polygon) bool {
	for _, ring := range poly {
		if pointInRingMesh(p, ring) {
			return true
		}
	}
	return false
}

// thtAnnularProbes returns sample points around a THT pad centre at the given
// radius. Sampling the annular ring avoids voting from the drilled hole itself,
// which may be filled by a copper pour of the wrong net.
func thtAnnularProbes(centre geometry.Point, radius float64) []geometry.Point {
	const n = 8
	probes := make([]geometry.Point, n)
	for i := 0; i < n; i++ {
		a := 2 * math.Pi * float64(i) / float64(n)
		probes[i] = geometry.Point{
			X: centre.X + radius*math.Cos(a),
			Y: centre.Y + radius*math.Sin(a),
		}
	}
	return probes
}
