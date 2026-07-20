package pipeline

import (
	"fmt"

	"github.com/easyeda/eext-paden-integration/go-service/internal/geometry"
	"github.com/easyeda/eext-paden-integration/go-service/internal/problem"
)

// inferPolygonNets labels every polygon in each layer with the net of the pads
// that fall inside it. Empty label means the net could not be inferred.
func inferPolygonNets(layers []*problem.Layer, pads []Pad, transform *[4]float64, d *DiagCollector) {
	for _, l := range layers {
		l.NetLabels = make([]string, len(l.Shape))
	}
	if len(pads) == 0 {
		return
	}

	type padInfo struct {
		pt    geometry.Point
		net   string
		layer string
		tht   bool
	}
	infos := make([]padInfo, 0, len(pads))
	for _, p := range pads {
		x, y := p.X, p.Y
		if transform != nil {
			x = x*transform[0] + transform[2]
			y = y*transform[1] + transform[3]
		}
		infos = append(infos, padInfo{
			pt:    geometry.Point{X: x, Y: y},
			net:   p.Net,
			layer: p.Layer,
			tht:   p.IsTHT,
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
			for polyIdx, poly := range l.Shape {
				if pointInPolygonMesh(pi.pt, poly) {
					if pi.net != "" {
						votes[polyIdx][pi.net]++
					}
					continue
				}
				// For THT pads the pad centre is inside the drilled hole, so it is not
				// contained in the filled copper area. The copper annular ring (or the
				// copper pour surrounding the hole) still belongs to the pad's net, so
				// treat the pad as a vote if it lies inside any ring of the polygon.
				if pi.tht && pointInsidePolygonRings(pi.pt, poly) {
					if pi.net != "" {
						votes[polyIdx][pi.net]++
					}
				}
			}
		}
		for i, v := range votes {
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
