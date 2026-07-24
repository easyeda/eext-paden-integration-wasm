package pipeline

import (
	"fmt"
	"math"
	"sort"

	"github.com/easyeda/eext-paden-integration/go-service/internal/geometry"
	"github.com/easyeda/eext-paden-integration/go-service/internal/problem"
)

// viaSpec is an internal via description.
type viaSpec struct {
	Point         geometry.Point
	DrillDiameter float64
	Diameter      float64
	LayerNames    []string
	Net           string
	ViaType       string
}

func extractViaSpecs(vias []Via, layerDict map[string]*problem.Layer, transform *[4]float64) []viaSpec {
	var specs []viaSpec
	for _, v := range vias {
		x, y := v.X, v.Y
		if transform != nil {
			x = x*transform[0] + transform[2]
			y = y*transform[1] + transform[3]
		}
		var validLayers []string
		for _, name := range v.LayerNames {
			if _, ok := layerDict[name]; ok {
				validLayers = append(validLayers, name)
			}
		}
		if len(validLayers) == 0 {
			continue
		}
		specs = append(specs, viaSpec{
			Point:         geometry.Point{X: x, Y: y},
			DrillDiameter: v.HoleDiameter,
			Diameter:      v.Diameter,
			LayerNames:    validLayers,
			Net:           v.Net,
			ViaType:       v.ViaType,
		})
	}
	return specs
}

func viaResistance(drillDiameter, length, platingThickness, conductivity float64) float64 {
	outerR := drillDiameter / 2
	innerR := outerR - platingThickness
	if innerR <= 0 || conductivity <= 0 {
		return 1e-3
	}
	return length / (conductivity * math.Pi * (outerR*outerR - innerR*innerR))
}

// inferViaNet resolves a via with an empty or unselected net.  It prefers the
// configured ground net / selected source nets, and falls back to the most
// common polygon label around the via location so that vias whose net property
// was lost in the frontend export still connect the correct layers.
func inferViaNet(spec viaSpec, layerDict map[string]*problem.Layer, allowedNets map[string]bool) string {
	if spec.Net != "" && allowedNets[spec.Net] {
		return spec.Net
	}

	votes := make(map[string]int)
	collectVotes := func(pt geometry.Point) {
		for _, name := range spec.LayerNames {
			layer := layerDict[name]
			if layer == nil {
				continue
			}
			for i, poly := range layer.Shape {
				if pointInPolygonMesh(pt, poly) {
					net := ""
					if i < len(layer.NetLabels) {
						net = layer.NetLabels[i]
					}
					if net != "" {
						votes[net]++
					}
				}
			}
		}
	}

	collectVotes(spec.Point)
	// Vias sit in drilled holes; the surrounding copper belongs to the via net.
	// Sample around the hole at the annular-ring midpoint to catch the correct
	// polygon even when the via centre is empty.
	outerR := math.Max(spec.Diameter/2, spec.DrillDiameter/2)
	probeR := spec.DrillDiameter/2 + (outerR-spec.DrillDiameter/2)*0.5
	if probeR <= 0 {
		probeR = 0.3
	}
	for i := 0; i < 8; i++ {
		a := 2 * math.Pi * float64(i) / 8
		collectVotes(geometry.Point{X: spec.Point.X + probeR*math.Cos(a), Y: spec.Point.Y + probeR*math.Sin(a)})
	}

	// If the via already had a net, only keep it when it is in the allowed set.
	if spec.Net != "" {
		return ""
	}

	bestNet := ""
	bestCnt := 0
	for net, cnt := range votes {
		if cnt > bestCnt || (cnt == bestCnt && allowedNets[net] && !allowedNets[bestNet]) {
			bestCnt = cnt
			bestNet = net
		}
	}
	if bestNet != "" && allowedNets[bestNet] {
		return bestNet
	}
	return ""
}

func buildViaNetworks(specs []viaSpec, layerDict map[string]*problem.Layer, stackup []float64, cfg Config, d *DiagCollector) []*problem.Network {
	allowedNets := make(map[string]bool)
	for _, src := range cfg.Sources {
		allowedNets[src.Net] = true
		if src.GndNet != "" {
			allowedNets[src.GndNet] = true
		}
		for _, p := range src.GndPads {
			allowedNets[p.Net] = true
		}
	}
	for _, ld := range cfg.Loads {
		if ld.GndNet != "" {
			allowedNets[ld.GndNet] = true
		}
		for _, p := range ld.GndPads {
			allowedNets[p.Net] = true
		}
	}
	if cfg.GndNet != "" {
		allowedNets[cfg.GndNet] = true
	}

	var networks []*problem.Network
	for _, spec := range specs {
		net := inferViaNet(spec, layerDict, allowedNets)
		if net == "" {
			d.Info(fmt.Sprintf("Via (%.3f,%.3f): skipped (net='%s', not in selected/ground nets)", spec.Point.X, spec.Point.Y, spec.Net))
			continue
		}
		if net != spec.Net {
			d.Info(fmt.Sprintf("Via (%.3f,%.3f): inferred net '%s' from geometry (original='%s')", spec.Point.X, spec.Point.Y, net, spec.Net))
		}

		var viaLayers []struct {
			idx   int
			layer *problem.Layer
		}
		for i, name := range spec.LayerNames {
			if layer, ok := layerDict[name]; ok {
				viaLayers = append(viaLayers, struct {
					idx   int
					layer *problem.Layer
				}{idx: i, layer: layer})
			}
		}
		if len(viaLayers) < 2 {
			continue
		}

		for i := 0; i < len(viaLayers)-1; i++ {
			a := viaLayers[i]
			b := viaLayers[i+1]
			thicknessA := stackup[a.idx]
			if a.idx >= len(stackup) {
				thicknessA = 0.035
			}
			thicknessB := stackup[b.idx]
			if b.idx >= len(stackup) {
				thicknessB = 0.035
			}
			length := (thicknessA + thicknessB) / 2
			res := viaResistance(spec.DrillDiameter, length, 0.025, 5.95e4)

			// Snap electrical nodes to connected pours while rendering the via at its PCB coordinate.
			nearestA, okA := findViaSnapPoint(spec.Point, a.layer, net)
			nearestB, okB := findViaSnapPoint(spec.Point, b.layer, net)
			if !okA || !okB {
				d.Info(fmt.Sprintf("Via '%s' (%.3f,%.3f): no '%s' copper on layer pair '%s'/'%s'", net, spec.Point.X, spec.Point.Y, net, a.layer.Name, b.layer.Name))
				continue
			}

			displayPoint := spec.Point
			connA := problem.NewConnection(a.layer, nearestA)
			connA.DisplayPoint = &displayPoint
			connA.Kind = "via"
			connB := problem.NewConnection(b.layer, nearestB)
			connB.DisplayPoint = &displayPoint
			connB.Kind = "via"
			vn, err := problem.NewNetwork(
				[]*problem.Connection{connA, connB},
				[]problem.LumpedElement{
					&problem.Resistor{A: connA.NodeID, B: connB.NodeID, Resistance: res},
				},
			)
			if err != nil {
				d.Warn(fmt.Sprintf("Via network error: %v", err))
				continue
			}
			// Preserve the via outer diameter so the results viewer can draw it
			// at the correct size.
			connA.Diameter = spec.Diameter
			connB.Diameter = spec.Diameter
			networks = append(networks, vn)
		}
	}
	return networks
}

func buildTrackNetworks(cfg Config, layerDict map[string]*problem.Layer, layerIDToName map[int]string, transform *[4]float64, d *DiagCollector) []*problem.Network {
	var networks []*problem.Network
	for _, t := range cfg.Tracks {
		if t.Net == "" || t.Width <= 0 {
			continue
		}
		layerName := layerIDToName[t.Layer]
		if layerName == "" {
			continue
		}
		layer := layerDict[layerName]
		if layer == nil {
			continue
		}
		p1 := transformPoint(t.X1, t.Y1, transform)
		p2 := transformPoint(t.X2, t.Y2, transform)
		nearest1, ok1 := findNearestPointOnLayer(p1, layer, t.Net)
		nearest2, ok2 := findNearestPointOnLayer(p2, layer, t.Net)
		if !ok1 || !ok2 {
			in1, label1 := pointInAnyPolygon(p1, layer)
			in2, label2 := pointInAnyPolygon(p2, layer)
			d.Info(fmt.Sprintf("Track '%s' on '%s': could not connect endpoints (%.3f,%.3f in=%v net='%s') -> (%.3f,%.3f in=%v net='%s')",
				t.Net, layerName, p1.X, p1.Y, in1, label1, p2.X, p2.Y, in2, label2))
			continue
		}
		d.Info(fmt.Sprintf("Track '%s' on '%s': connected (%.3f,%.3f)->(%.3f,%.3f) via (%.3f,%.3f)->(%.3f,%.3f) R=%.6f",
			t.Net, layerName, p1.X, p1.Y, p2.X, p2.Y, nearest1.X, nearest1.Y, nearest2.X, nearest2.Y, math.Hypot(p2.X-p1.X, p2.Y-p1.Y)/(layer.Conductance*t.Width)))
		length := math.Hypot(p2.X-p1.X, p2.Y-p1.Y)
		if length <= 1e-9 {
			continue
		}
		res := length / (layer.Conductance * t.Width)
		if res <= 0 || math.IsInf(res, 0) {
			continue
		}
		conn1 := problem.NewConnection(layer, nearest1)
		conn1.Kind = "track"
		conn2 := problem.NewConnection(layer, nearest2)
		conn2.Kind = "track"
		net, err := problem.NewNetwork(
			[]*problem.Connection{conn1, conn2},
			[]problem.LumpedElement{
				&problem.Resistor{A: conn1.NodeID, B: conn2.NodeID, Resistance: res},
			},
		)
		if err != nil {
			d.Warn(fmt.Sprintf("Track network error: %v", err))
			continue
		}
		networks = append(networks, net)
	}
	return networks
}

// Small same-net polygons around drills can be isolated annular rings rather than connected pours.
const viaMinPolygonAreaMM2 = 1.0

// findNearestPointOnLayer snaps a terminal to same-net copper, falling back to any net.
func findNearestPointOnLayer(pt geometry.Point, layer *problem.Layer, targetNet string) (geometry.Point, bool) {
	// Exact containment in a polygon of the requested net.
	for i, poly := range layer.Shape {
		if !polygonMatchesNet(layer, i, targetNet) {
			continue
		}
		if pointInPolygonMesh(pt, poly) {
			return pt, true
		}
	}

	// Boundary search. Prefer polygons whose inferred net matches the pad net.
	// If no such polygon exists, fall back to the nearest geometry of any net.
	// This mirrors the Python reference, which uses shapely.nearest_points without
	// net filtering and therefore connects THT pads in a pour hole to the pour.
	type candidate struct {
		pt   geometry.Point
		dist float64
		area float64
	}
	candidates := func(filterNet string) []candidate {
		var out []candidate
		minDist := math.Inf(1)
		for i, poly := range layer.Shape {
			if filterNet != "" && !polygonMatchesNet(layer, i, filterNet) {
				continue
			}
			area := polygonArea(poly)
			for _, ring := range poly {
				for j := 0; j < len(ring); j++ {
					a := ring[j]
					b := ring[(j+1)%len(ring)]
					np := nearestPointOnSegment(pt, a, b)
					d := math.Hypot(np.X-pt.X, np.Y-pt.Y)
					if d < minDist-1e-3 {
						minDist = d
						out = []candidate{{pt: np, dist: d, area: area}}
					} else if d <= minDist+1e-3 {
						out = append(out, candidate{pt: np, dist: d, area: area})
					}
				}
			}
		}
		return out
	}

	bestCandidates := candidates(targetNet)
	if len(bestCandidates) == 0 && targetNet != "" {
		bestCandidates = candidates("")
	}
	if len(bestCandidates) == 0 {
		return pt, false
	}
	// When distances are nearly equal (e.g. pad annular ring vs. pour hole edge),
	// prefer the smaller polygon so the connection lands on the pad copper.
	best := bestCandidates[0]
	for _, c := range bestCandidates[1:] {
		if c.area < best.area {
			best = c
		}
	}
	return best.pt, true
}

// findViaSnapPoint skips isolated annular-ring polygons when snapping a via to its pour.
func findViaSnapPoint(pt geometry.Point, layer *problem.Layer, targetNet string) (geometry.Point, bool) {
	for i, poly := range layer.Shape {
		if !polygonMatchesNet(layer, i, targetNet) || polygonArea(poly) < viaMinPolygonAreaMM2 {
			continue
		}
		if pointInPolygonMesh(pt, poly) {
			return pt, true
		}
	}

	type candidate struct {
		pt   geometry.Point
		dist float64
	}
	best := candidate{dist: math.Inf(1)}
	for i, poly := range layer.Shape {
		if !polygonMatchesNet(layer, i, targetNet) || polygonArea(poly) < viaMinPolygonAreaMM2 {
			continue
		}
		for _, ring := range poly {
			for j := 0; j < len(ring); j++ {
				a := ring[j]
				b := ring[(j+1)%len(ring)]
				np := nearestPointOnSegment(pt, a, b)
				d := math.Hypot(np.X-pt.X, np.Y-pt.Y)
				if d < best.dist {
					best = candidate{pt: np, dist: d}
				}
			}
		}
	}
	if math.IsInf(best.dist, 1) {
		return pt, false
	}
	return best.pt, true
}

func polygonArea(poly geometry.Polygon) float64 {
	var area float64
	for i, ring := range poly {
		a := ring.Area()
		if i == 0 {
			area += a
		} else {
			area -= a
		}
	}
	return math.Abs(area)
}

// polygonMatchesNet reports whether the polygon at index i should be considered
// for a pad/via belonging to targetNet. Empty targetNet disables filtering.
func polygonMatchesNet(layer *problem.Layer, i int, targetNet string) bool {
	if targetNet == "" {
		return true
	}
	if layer.NetLabels == nil || i >= len(layer.NetLabels) {
		return false
	}
	return layer.NetLabels[i] == targetNet
}

func nearestPointOnSegment(p, a, b geometry.Point) geometry.Point {
	dx := b.X - a.X
	dy := b.Y - a.Y
	if dx == 0 && dy == 0 {
		return a
	}
	t := ((p.X-a.X)*dx + (p.Y-a.Y)*dy) / (dx*dx + dy*dy)
	if t < 0 {
		return a
	}
	if t > 1 {
		return b
	}
	return geometry.Point{X: a.X + t*dx, Y: a.Y + t*dy}
}

func buildUserNetworks(cfg Config, layerDict map[string]*problem.Layer, transform *[4]float64, d *DiagCollector) []*problem.Network {
	var layerNames []string
	for name := range layerDict {
		layerNames = append(layerNames, name)
	}
	d.Info(fmt.Sprintf("buildUserNetworks: layerDict keys=%v", layerNames))

	var networks []*problem.Network
	gndNet := cfg.GndNet

	transformPt := func(x, y float64) geometry.Point {
		if transform != nil {
			return geometry.Point{X: x*transform[0] + transform[2], Y: y*transform[1] + transform[3]}
		}
		return geometry.Point{X: x, Y: y}
	}

	connectPad := func(pad Pad, kind string) []*problem.Connection {
		pt := transformPt(pad.X, pad.Y)

		tryConnect := func(l *problem.Layer, p geometry.Point) *problem.Connection {
			// Prefer exact containment; if just outside, snap to nearest boundary point.
			var c *problem.Connection
			if pointOnLayer(p, l, pad.Net) {
				c = problem.NewConnection(l, p)
			} else {
				nearest, ok := findNearestPointOnLayer(p, l, pad.Net)
				if !ok {
					return nil
				}
				dist := math.Hypot(nearest.X-p.X, nearest.Y-p.Y)
				if dist > 0.5 {
					return nil
				}
				c = problem.NewConnection(l, nearest)
			}
			c.Kind = kind
			return c
		}

		if pad.IsTHT {
			var conns []*problem.Connection
			seen := make(map[string]bool)
			for name, layer := range layerDict {
				if seen[name] {
					continue
				}
				if c := tryConnect(layer, pt); c != nil {
					seen[name] = true
					conns = append(conns, c)
				}
			}
			// If no layer contains/near the point, snap to every layer's nearest
			// copper within a small tolerance. A THT pad physically touches all
			// layers, so connecting to multiple layers is expected; the tolerance
			// keeps the snap close enough that it lands on the same-net copper.
			if len(conns) == 0 {
				const snapTol = 0.5
				for _, layer := range layerDict {
					nearest, ok := findNearestPointOnLayer(pt, layer, pad.Net)
					if !ok {
						continue
					}
					dist := math.Hypot(nearest.X-pt.X, nearest.Y-pt.Y)
					if dist <= snapTol {
						c := problem.NewConnection(layer, nearest)
						c.Kind = kind
						conns = append(conns, c)
					}
				}
			}
			// Last resort: single nearest layer/point so the analysis does not
			// silently drop the pad.
			if len(conns) == 0 {
				var bestLayer *problem.Layer
				var bestPt geometry.Point
				bestDist := math.Inf(1)
				for _, layer := range layerDict {
					nearest, ok := findNearestPointOnLayer(pt, layer, pad.Net)
					if !ok {
						continue
					}
					dist := math.Hypot(nearest.X-pt.X, nearest.Y-pt.Y)
					if dist < bestDist {
						bestDist = dist
						bestLayer = layer
						bestPt = nearest
					}
				}
				if bestLayer != nil {
					d.Warn(fmt.Sprintf("Pad '%s' on '%s' snapped %.3f mm to nearest '%s' copper", pad.Net, bestLayer.Name, bestDist, pad.Net))
					c := problem.NewConnection(bestLayer, bestPt)
					c.Kind = kind
					conns = append(conns, c)
				}
			}
			return conns
		}

		layer := layerDict[pad.Layer]
		if layer == nil {
			var bestLayer *problem.Layer
			var bestPt geometry.Point
			bestDist := math.Inf(1)
			for _, l := range layerDict {
				nearest, ok := findNearestPointOnLayer(pt, l, pad.Net)
				if !ok {
					continue
				}
				dist := math.Hypot(nearest.X-pt.X, nearest.Y-pt.Y)
				if dist < bestDist {
					bestDist = dist
					bestLayer = l
					bestPt = nearest
				}
			}
			if bestLayer != nil {
				c := problem.NewConnection(bestLayer, bestPt)
				c.Kind = kind
				return []*problem.Connection{c}
			}
			return nil
		}
		if c := tryConnect(layer, pt); c != nil {
			return []*problem.Connection{c}
		}
		nearest, ok := findNearestPointOnLayer(pt, layer, pad.Net)
		if !ok {
			return nil
		}
		c := problem.NewConnection(layer, nearest)
		c.Kind = kind
		return []*problem.Connection{c}
	}

	connKey := func(c *problem.Connection) string {
		return fmt.Sprintf("%p_%.6f_%.6f", c.Layer, c.Point.X, c.Point.Y)
	}

	limitGndPads := func(gndPads, referencePads []Pad) []Pad {
		const maxGnd = 20
		if len(gndPads) <= maxGnd || len(referencePads) == 0 {
			return gndPads
		}
		var cx, cy float64
		for _, p := range referencePads {
			cx += p.X
			cy += p.Y
		}
		cx /= float64(len(referencePads))
		cy /= float64(len(referencePads))
		sort.Slice(gndPads, func(i, j int) bool {
			di := math.Hypot(gndPads[i].X-cx, gndPads[i].Y-cy)
			dj := math.Hypot(gndPads[j].X-cx, gndPads[j].Y-cy)
			return di < dj
		})
		if len(gndPads) > maxGnd {
			gndPads = gndPads[:maxGnd]
		}
		return gndPads
	}

	virtualGround := func() *problem.Connection {
		d.Info("Using virtual ground reference (no GND pads found)")
		c := problem.NewConnection(nil, geometry.Point{})
		c.Kind = "gnd"
		return c
	}

	connectSourcePad := func(pad Pad) []*problem.Connection { return connectPad(pad, "source") }
	connectGndPad := func(pad Pad) []*problem.Connection { return connectPad(pad, "gnd") }
	connectLoadPad := func(pad Pad) []*problem.Connection { return connectPad(pad, "load") }

	// Match VS + CS by net
	matchedLoads := make(map[string]*Load)
	for i := range cfg.Loads {
		ld := &cfg.Loads[i]
		matchedLoads[ld.Net] = ld
	}

	for i := range cfg.Sources {
		src := &cfg.Sources[i]
		d.Info(fmt.Sprintf("Source '%s': raw pads=%d gnd_pads=%d voltage=%.3f", src.Net, len(src.Pads), len(src.GndPads), src.Voltage))
		pConns := connectSourcePads(src.Pads, connectSourcePad)
		var gndPads []Pad
		if len(src.GndPads) > 0 {
			gndPads = src.GndPads
		} else {
			for _, p := range cfg.Pads {
				if p.Net == src.GndNet || (src.GndNet == "" && p.Net == gndNet) {
					gndPads = append(gndPads, p)
				}
			}
			gndPads = limitGndPads(gndPads, src.Pads)
		}
		nConns := connectSourcePads(gndPads, connectGndPad)
		if len(nConns) == 0 {
			nConns = []*problem.Connection{virtualGround()}
		}

		if len(pConns) == 0 {
			d.Warn(fmt.Sprintf("Source '%s': p=%d, n=%d, skipped", src.Net, len(pConns), len(nConns)))
			continue
		}
		if connKey(pConns[0]) == connKey(nConns[0]) {
			d.Warn(fmt.Sprintf("Source '%s': main VS p==n, skipped", src.Net))
			continue
		}

		elements := []problem.LumpedElement{
			&problem.VoltageSource{P: pConns[0].NodeID, N: nConns[0].NodeID, Voltage: src.Voltage},
		}
		allConns := []*problem.Connection{pConns[0], nConns[0]}
		seen := map[string]bool{connKey(pConns[0]): true, connKey(nConns[0]): true}

		for _, pc := range pConns[1:] {
			k := connKey(pc)
			if seen[k] {
				continue
			}
			seen[k] = true
			elements = append(elements, &problem.VoltageSource{P: pc.NodeID, N: pConns[0].NodeID, Voltage: 0})
			allConns = append(allConns, pc)
		}
		for _, nc := range nConns[1:] {
			k := connKey(nc)
			if seen[k] {
				continue
			}
			seen[k] = true
			elements = append(elements, &problem.VoltageSource{P: nc.NodeID, N: nConns[0].NodeID, Voltage: 0})
			allConns = append(allConns, nc)
		}

		if load, ok := matchedLoads[src.Net]; ok {
			fConns := connectSourcePads(load.Pads, connectLoadPad)
			var tConns []*problem.Connection
			if len(load.GndPads) > 0 {
				tConns = connectSourcePads(load.GndPads, connectGndPad)
			} else {
				var gndPads []Pad
				for _, p := range cfg.Pads {
					if p.Net == load.GndNet || (load.GndNet == "" && p.Net == gndNet) {
						gndPads = append(gndPads, p)
					}
				}
				gndPads = limitGndPads(gndPads, load.Pads)
				tConns = connectSourcePads(gndPads, connectGndPad)
			}
			if len(fConns) > 0 {
				csT := nConns[0]
				if len(tConns) > 0 && connKey(tConns[0]) != connKey(nConns[0]) {
					csT = tConns[0]
				}
				elements = append(elements, &problem.CurrentSource{F: fConns[0].NodeID, T: csT.NodeID, Current: load.Current})
				if k := connKey(fConns[0]); !seen[k] {
					seen[k] = true
					allConns = append(allConns, fConns[0])
				}
				if k := connKey(csT); !seen[k] {
					seen[k] = true
					allConns = append(allConns, csT)
				}
				for _, fc := range fConns[1:] {
					k := connKey(fc)
					if seen[k] {
						continue
					}
					seen[k] = true
					elements = append(elements, &problem.VoltageSource{P: fc.NodeID, N: fConns[0].NodeID, Voltage: 0})
					allConns = append(allConns, fc)
				}
				for _, tc := range tConns[1:] {
					k := connKey(tc)
					if seen[k] {
						continue
					}
					seen[k] = true
					elements = append(elements, &problem.VoltageSource{P: tc.NodeID, N: csT.NodeID, Voltage: 0})
					allConns = append(allConns, tc)
				}
				if len(tConns) > 0 && connKey(csT) != connKey(nConns[0]) {
					elements = append(elements, &problem.VoltageSource{P: csT.NodeID, N: nConns[0].NodeID, Voltage: 0})
				}
			}
		}

		net, err := problem.NewNetwork(allConns, elements)
		if err != nil {
			d.Warn(fmt.Sprintf("Source network '%s' error: %v", src.Net, err))
			continue
		}
		networks = append(networks, net)
	}

	// Standalone loads
	matchedNets := make(map[string]bool)
	for _, src := range cfg.Sources {
		matchedNets[src.Net] = true
	}
	for i := range cfg.Loads {
		load := &cfg.Loads[i]
		if matchedNets[load.Net] {
			continue
		}
		fConns := connectSourcePads(load.Pads, connectLoadPad)
		var tConns []*problem.Connection
		if len(load.GndPads) > 0 {
			tConns = connectSourcePads(load.GndPads, connectGndPad)
		} else {
			var gndPads []Pad
			for _, p := range cfg.Pads {
				if p.Net == load.GndNet || (load.GndNet == "" && p.Net == gndNet) {
					gndPads = append(gndPads, p)
				}
			}
			gndPads = limitGndPads(gndPads, load.Pads)
			tConns = connectSourcePads(gndPads, connectGndPad)
		}
		if len(tConns) == 0 {
			tConns = []*problem.Connection{virtualGround()}
		}
		if len(fConns) == 0 {
			d.Warn(fmt.Sprintf("Load '%s': f=%d, t=%d, skipped", load.Net, len(fConns), len(tConns)))
			continue
		}
		elements := []problem.LumpedElement{
			&problem.CurrentSource{F: fConns[0].NodeID, T: tConns[0].NodeID, Current: load.Current},
		}
		allConns := []*problem.Connection{fConns[0], tConns[0]}
		seen := map[string]bool{connKey(fConns[0]): true, connKey(tConns[0]): true}
		for _, fc := range fConns[1:] {
			k := connKey(fc)
			if seen[k] {
				continue
			}
			seen[k] = true
			elements = append(elements, &problem.VoltageSource{P: fc.NodeID, N: fConns[0].NodeID, Voltage: 0})
			allConns = append(allConns, fc)
		}
		for _, tc := range tConns[1:] {
			k := connKey(tc)
			if seen[k] {
				continue
			}
			seen[k] = true
			elements = append(elements, &problem.VoltageSource{P: tc.NodeID, N: tConns[0].NodeID, Voltage: 0})
			allConns = append(allConns, tc)
		}
		net, err := problem.NewNetwork(allConns, elements)
		if err != nil {
			d.Warn(fmt.Sprintf("Load network '%s' error: %v", load.Net, err))
			continue
		}
		networks = append(networks, net)
	}

	return networks
}

func connectSourcePads(pads []Pad, connectPad func(Pad) []*problem.Connection) []*problem.Connection {
	var conns []*problem.Connection
	for _, pad := range pads {
		conns = append(conns, connectPad(pad)...)
	}
	return conns
}

func pointOnLayer(pt geometry.Point, layer *problem.Layer, targetNet string) bool {
	for i, poly := range layer.Shape {
		if !polygonMatchesNet(layer, i, targetNet) {
			continue
		}
		if pointInPolygonMesh(pt, poly) {
			return true
		}
	}
	return false
}

func pointInAnyPolygon(pt geometry.Point, layer *problem.Layer) (bool, string) {
	for i, poly := range layer.Shape {
		if pointInPolygonMesh(pt, poly) {
			label := ""
			if i < len(layer.NetLabels) {
				label = layer.NetLabels[i]
			}
			return true, label
		}
	}
	return false, ""
}
