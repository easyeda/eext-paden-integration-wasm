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

func buildViaNetworks(specs []viaSpec, layerDict map[string]*problem.Layer, stackup []float64, selectedNets map[string]bool, d *DiagCollector) []*problem.Network {
	var networks []*problem.Network
	for _, spec := range specs {
		if spec.Net != "" && !selectedNets[spec.Net] {
			continue
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

			nearestA, okA := findNearestPointOnLayer(spec.Point, a.layer)
			nearestB, okB := findNearestPointOnLayer(spec.Point, b.layer)
			if !okA || !okB {
				continue
			}

			connA := problem.NewConnection(a.layer, nearestA)
			connB := problem.NewConnection(b.layer, nearestB)
			net, err := problem.NewNetwork(
				[]*problem.Connection{connA, connB},
				[]problem.LumpedElement{
					&problem.Resistor{A: connA.NodeID, B: connB.NodeID, Resistance: res},
				},
			)
			if err != nil {
				d.Warn(fmt.Sprintf("Via network error: %v", err))
				continue
			}
			networks = append(networks, net)
		}
	}
	return networks
}

func findNearestPointOnLayer(pt geometry.Point, layer *problem.Layer) (geometry.Point, bool) {
	for _, poly := range layer.Shape {
		if pointInPolygonMesh(pt, poly) {
			return pt, true
		}
	}
	// Find nearest point on polygon boundary
	nearest := pt
	minDist := math.Inf(1)
	found := false
	for _, poly := range layer.Shape {
		for _, ring := range poly {
			for i := 0; i < len(ring); i++ {
				a := ring[i]
				b := ring[(i+1)%len(ring)]
				np := nearestPointOnSegment(pt, a, b)
				d := math.Hypot(np.X-pt.X, np.Y-pt.Y)
				if d < minDist {
					minDist = d
					nearest = np
					found = true
				}
			}
		}
	}
	return nearest, found
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

	connectPad := func(pad Pad) []*problem.Connection {
		pt := transformPt(pad.X, pad.Y)
		d.Info(fmt.Sprintf("connectPad: pad layer='%s' isTHT=%v pt=%.3f,%.3f", pad.Layer, pad.IsTHT, pt.X, pt.Y))

		tryConnect := func(l *problem.Layer, p geometry.Point) *problem.Connection {
			// Prefer exact containment; if just outside, snap to nearest boundary point.
			if pointOnLayer(p, l) {
				return problem.NewConnection(l, p)
			}
			nearest, ok := findNearestPointOnLayer(p, l)
			if !ok {
				return nil
			}
			dist := math.Hypot(nearest.X-p.X, nearest.Y-p.Y)
			if dist <= 0.5 {
				return problem.NewConnection(l, nearest)
			}
			return nil
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
					nearest, ok := findNearestPointOnLayer(pt, layer)
					if !ok {
						continue
					}
					dist := math.Hypot(nearest.X-pt.X, nearest.Y-pt.Y)
					if dist <= snapTol {
						conns = append(conns, problem.NewConnection(layer, nearest))
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
					nearest, ok := findNearestPointOnLayer(pt, layer)
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
					d.Info(fmt.Sprintf("connectPad THT fallback: nearest layer '%s' dist=%.3f", bestLayer.Name, bestDist))
					conns = append(conns, problem.NewConnection(bestLayer, bestPt))
				}
			}
			d.Info(fmt.Sprintf("connectPad THT: %d connections", len(conns)))
			return conns
		}

		layer := layerDict[pad.Layer]
		if layer == nil {
			d.Info(fmt.Sprintf("connectPad SMD: layer '%s' not found in layerDict, falling back to nearest layer", pad.Layer))
			var bestLayer *problem.Layer
			var bestPt geometry.Point
			bestDist := math.Inf(1)
			for _, l := range layerDict {
				nearest, ok := findNearestPointOnLayer(pt, l)
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
				return []*problem.Connection{problem.NewConnection(bestLayer, bestPt)}
			}
			return nil
		}
		if c := tryConnect(layer, pt); c != nil {
			nearest, _ := findNearestPointOnLayer(pt, layer)
			d.Info(fmt.Sprintf("connectPad SMD: layer '%s' connected nearest=%.3f,%.3f", pad.Layer, nearest.X, nearest.Y))
			return []*problem.Connection{c}
		}
		nearest, ok := findNearestPointOnLayer(pt, layer)
		if !ok {
			d.Info(fmt.Sprintf("connectPad SMD: layer '%s' has no geometry", pad.Layer))
			return nil
		}
		dist := math.Hypot(nearest.X-pt.X, nearest.Y-pt.Y)
		d.Info(fmt.Sprintf("connectPad SMD: layer '%s' not contained, snapping nearest dist=%.3f", pad.Layer, dist))
		return []*problem.Connection{problem.NewConnection(layer, nearest)}
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
		return problem.NewConnection(nil, geometry.Point{})
	}

	// Match VS + CS by net
	matchedLoads := make(map[string]*Load)
	for i := range cfg.Loads {
		ld := &cfg.Loads[i]
		matchedLoads[ld.Net] = ld
	}

	for i := range cfg.Sources {
		src := &cfg.Sources[i]
		d.Info(fmt.Sprintf("Source '%s': raw pads=%d gnd_pads=%d voltage=%.3f", src.Net, len(src.Pads), len(src.GndPads), src.Voltage))
		pConns := connectSourcePads(src.Pads, connectPad)
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
		nConns := connectSourcePads(gndPads, connectPad)
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
			fConns := connectSourcePads(load.Pads, connectPad)
			var tConns []*problem.Connection
			if len(load.GndPads) > 0 {
				tConns = connectSourcePads(load.GndPads, connectPad)
			} else {
				var gndPads []Pad
				for _, p := range cfg.Pads {
					if p.Net == load.GndNet || (load.GndNet == "" && p.Net == gndNet) {
						gndPads = append(gndPads, p)
					}
				}
				gndPads = limitGndPads(gndPads, load.Pads)
				tConns = connectSourcePads(gndPads, connectPad)
			}
			if len(fConns) > 0 {
				csT := nConns[0]
				if len(tConns) > 0 {
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
		fConns := connectSourcePads(load.Pads, connectPad)
		var tConns []*problem.Connection
		if len(load.GndPads) > 0 {
			tConns = connectSourcePads(load.GndPads, connectPad)
		} else {
			var gndPads []Pad
			for _, p := range cfg.Pads {
				if p.Net == load.GndNet || (load.GndNet == "" && p.Net == gndNet) {
					gndPads = append(gndPads, p)
				}
			}
			gndPads = limitGndPads(gndPads, load.Pads)
			tConns = connectSourcePads(gndPads, connectPad)
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

func pointOnLayer(pt geometry.Point, layer *problem.Layer) bool {
	for _, poly := range layer.Shape {
		if pointInPolygonMesh(pt, poly) {
			return true
		}
	}
	return false
}
