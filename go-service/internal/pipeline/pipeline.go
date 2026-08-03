package pipeline

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/easyeda/eext-paden-integration/go-service/internal/geometry"
	"github.com/easyeda/eext-paden-integration/go-service/internal/problem"
	"github.com/easyeda/eext-paden-integration/go-service/internal/solver"
)

// DiagCollector collects diagnostic messages.
type DiagCollector struct {
	Lines []string
}

func (d *DiagCollector) Info(msg string) {
	d.Lines = append(d.Lines, "[INFO] "+msg)
	echoDiag("[INFO] " + msg)
}

func (d *DiagCollector) Warn(msg string) {
	d.Lines = append(d.Lines, "[WARN] "+msg)
	echoDiag("[WARN] " + msg)
}

func (d *DiagCollector) Error(msg string) {
	d.Lines = append(d.Lines, "[ERROR] "+msg)
	echoDiag("[ERROR] " + msg)
}

// Analyze runs the full PDN analysis pipeline.
func Analyze(odbTgz []byte, configJSON string) (*solver.Solution, *DiagCollector, error) {
	analyzeStart := time.Now()
	d := &DiagCollector{}

	var cfg Config
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return nil, d, fmt.Errorf("failed to parse config: %w", err)
	}

	d.Info(fmt.Sprintf("project=%s, layers=%d, vias=%d, pads=%d, sources=%d, loads=%d",
		cfg.ProjectName, len(cfg.Layers), len(cfg.Vias), len(cfg.Pads), len(cfg.Sources), len(cfg.Loads)))

	t0 := time.Now()

	if len(cfg.Layers) == 0 {
		return nil, d, fmt.Errorf("no layer configs")
	}

	// 1. Parse ODB++ geometry and its authoritative net-to-feature mapping.
	d.Info("Step 1: Parse ODB++ archive")
	layerNames := make([]string, len(cfg.Layers))
	for i, lc := range cfg.Layers {
		layerNames[i] = lc.Name
	}
	targetNets := make(map[string]bool)
	if cfg.GndNet != "" {
		targetNets[cfg.GndNet] = true
	}
	for _, source := range cfg.Sources {
		targetNets[source.Net] = true
		targetNets[source.GndNet] = true
	}
	for _, load := range cfg.Loads {
		targetNets[load.Net] = true
		targetNets[load.GndNet] = true
	}
	parsedODB, err := geometry.ParseODB(odbTgz, layerNames, targetNets)
	if err != nil {
		return nil, d, fmt.Errorf("ODB++ parse failed: %w", err)
	}

	// Build the layer ID -> name map early; it is needed by the live-geometry
	// net-label override below.
	layerIDToName := make(map[int]string)
	for _, lc := range cfg.Layers {
		layerIDToName[lc.LayerID] = lc.Name
	}

	// 2. Coordinate transform.  Compute it as soon as ODB++ bounds are available
	// so live EasyEDA geometry can be aligned with ODB++ polygons for net
	// label override.
	transform := computeCoordinateTransform(cfg.EasyEDABounds, nil, parsedODB.AllLayers, cfg, nil, d)
	if transform != nil {
		d.Info(fmt.Sprintf("Transform: scale=(%.4f,%.4f), offset=(%.2f,%.2f)", transform[0], transform[1], transform[2], transform[3]))
	}

	// 2a. Override ODB++ net labels using live copper pour geometry from the
	// EasyEDA canvas.  ODB++ only annotates a subset of features; the override
	// labels otherwise-unlabeled polygons that fall inside a live pour so the
	// preview and solver see the correct net.
	overrideNetLabelsWithLiveGeometry(parsedODB, cfg.CopperPours, cfg.Pads, cfg.Tracks, layerIDToName, transform, d)
	// Re-propagate so the newly-seeded labels spread to touching polygons.
	repropagateNetLabels(parsedODB, layerNames, targetNets, d)

	// Now build the solver layers from the (possibly corrected) ODB++ data.
	parsed := parsedODB.Layers
	var layers []*problem.Layer
	for _, lc := range cfg.Layers {
		gl, ok := parsed[lc.Name]
		if !ok || gl.Name == "" {
			d.Warn(fmt.Sprintf("Layer '%s': not found in ODB++ stackup", lc.Name))
			continue
		}
		if len(gl.Polygons) == 0 {
			d.Warn(fmt.Sprintf("Layer '%s': no selected-net copper in ODB++", lc.Name))
			continue
		}
		layer := &problem.Layer{
			Shape:       gl.Polygons,
			NetLabels:   gl.NetLabels,
			Name:        lc.Name,
			Conductance: lc.EffectiveConductance(),
		}
		layers = append(layers, layer)
		d.Info(fmt.Sprintf("Layer '%s': %d net-attributed ODB++ features", lc.Name, len(gl.Polygons)))
	}

	if len(layers) == 0 {
		return nil, d, fmt.Errorf("no selected-net copper layers from ODB++")
	}

	// Extract the board outline early; its centre is the reference for un-mirroring
	// any reflected Gerber layers (common for bottom copper in some exports).
	outline, outlineName := extractBoardOutline(parsed)

	// Clean each layer: normalize ring orientations. We deliberately skip the
	// pre-inference global Union — merging polygons before net inference would
	// weld adjacent different-net pours (e.g. a 3V3 trace touching a GND plane)
	// into one MultiPolygon, hiding the visual boundary between them and
	// producing "shorts / missing content" in the rendered output. Same-net
	// merging happens after net inference further down this function, which
	// preserves the boundary between distinct nets while still coalescing
	// fragmented same-net copper pours.
	for _, layer := range layers {
		if len(layer.Shape) == 0 {
			continue
		}
		for i := range layer.Shape {
			layer.Shape[i].EnsureOrientation()
		}
		d.Info(fmt.Sprintf("Layer '%s': %d polygon(s) area=%.3f (pre-inference, no cross-net merge)",
			layer.Name, len(layer.Shape), layer.Area()))
	}

	layerDict := make(map[string]*problem.Layer)
	for _, l := range layers {
		layerDict[l.Name] = l
	}

	// ODB++ uses board coordinates for every layer, so no reflected-layer correction is needed.

	// 2. Board outline clipping
	d.Info(fmt.Sprintf("Step 1 (parse ODB++) done in %v", time.Since(t0)))
	t0 = time.Now()
	d.Info("Step 2: Board outline clipping")
	tSub := time.Now()
	if outline != nil {
		d.Info(fmt.Sprintf("Using outline layer '%s' with %d polygon(s)", outlineName, len(outline)))
		clipLayersWithOutline(layers, outline, d)
		d.Info(fmt.Sprintf("Step 2a (clip outline) done in %v", time.Since(tSub)))
	} else {
		d.Info("No board outline found")
	}

	// 2a. Subtract ODB++ drill holes from every copper layer.
	tSub = time.Now()
	drillHoles := parsedODB.DrillHoles
	d.Info(fmt.Sprintf("Step 2b (ODB++ drill holes: %d polygons) done in %v", len(drillHoles), time.Since(tSub)))
	if len(drillHoles) > 0 {
		d.Info(fmt.Sprintf("Subtracting %d drill-hole polygon(s) from all copper layers", len(drillHoles)))
		tSub = time.Now()
		for _, layer := range layers {
			tLayer := time.Now()
			groups := make(map[string]geometry.MultiPolygon)
			for i, polygon := range layer.Shape {
				net := ""
				if i < len(layer.NetLabels) {
					net = layer.NetLabels[i]
				}
				groups[net] = append(groups[net], polygon)
			}
			var punched geometry.MultiPolygon
			var labels []string
			for net, group := range groups {
				pieces, err := geometry.Difference(group, drillHoles)
				if err != nil {
					d.Warn(fmt.Sprintf("Layer '%s' net '%s': drill subtraction failed, keeping original", layer.Name, net))
					pieces = group
				}
				for _, piece := range pieces {
					piece.EnsureOrientation()
					punched = append(punched, piece)
					labels = append(labels, net)
				}
			}
			if len(punched) == 0 {
				d.Warn(fmt.Sprintf("Layer '%s': empty after drill subtraction, keeping original", layer.Name))
				continue
			}
			before := len(layer.Shape)
			layer.Shape = punched
			layer.NetLabels = labels
			d.Info(fmt.Sprintf("Step 2b sub: layer '%s' by net (%d polys - %d holes) -> %d polys in %v",
				layer.Name, before, len(drillHoles), len(punched), time.Since(tLayer)))
		}
		d.Info(fmt.Sprintf("Step 2b (drill subtract all layers) done in %v", time.Since(tSub)))
	}

	// 2c. Subtract the same drill holes from the full-board copper stencil used
	// by the viewer. This keeps the board-fill context complete while also
	// punching pad/via holes through it.
	if len(drillHoles) > 0 {
		tAllCopper := time.Now()
		for name, gl := range parsedODB.AllLayers {
			if len(gl.Polygons) == 0 {
				continue
			}
			polygons, labels, err := subtractDrillHolesFromCopper(gl.Polygons, gl.NetLabels, drillHoles)
			if err != nil {
				d.Warn(fmt.Sprintf("AllCopper layer '%s': drill subtraction failed, keeping original", name))
				continue
			}
			gl.Polygons = polygons
			gl.NetLabels = labels
			parsedODB.AllLayers[name] = gl
		}
		d.Info(fmt.Sprintf("Step 2c (AllCopper drill subtract) done in %v", time.Since(tAllCopper)))
	}

	// 4. Build stackup
	stackup := buildStackup(cfg.LayerCuThickness, layers)

	// ODB++ already assigns every selected copper feature to its authoritative net.
	d.Info("Step 2b: Use authoritative ODB++ polygon nets")
	logLayerPolygonSummary(layers, d)

	// Merge polygons that share the same inferred net so electrically connected
	// copper of the same net becomes one FEM mesh. This matches Python's
	// shapely.unary_union post-processing, which joins touching/island polygons.
	for _, layer := range layers {
		groups := make(map[string]geometry.MultiPolygon)
		for i, poly := range layer.Shape {
			net := ""
			if i < len(layer.NetLabels) {
				net = layer.NetLabels[i]
			}
			groups[net] = append(groups[net], poly)
		}
		var merged geometry.MultiPolygon
		var labels []string
		for net, group := range groups {
			if len(group) == 0 {
				continue
			}
			if unioned, err := geometry.Union(group, nil); err == nil && len(unioned) > 0 {
				for _, poly := range unioned {
					merged = append(merged, poly)
					labels = append(labels, net)
				}
			} else {
				for _, poly := range group {
					merged = append(merged, poly)
					labels = append(labels, net)
				}
			}
		}
		layer.Shape = merged
		layer.NetLabels = labels
	}

	// Remove sub-resolution slivers produced by net-based separation. Keep the
	// threshold small enough that real small pads/traces (>= ~0.01 mm^2) survive.
	for _, layer := range layers {
		before := len(layer.Shape)
		newShape, newLabels := removeTinyPolygonsWithLabels(layer.Shape, layer.NetLabels, 1e-3)
		layer.Shape = newShape
		layer.NetLabels = newLabels
		if removed := before - len(layer.Shape); removed > 0 {
			d.Info(fmt.Sprintf("Layer '%s': removed %d tiny polygon(s)", layer.Name, removed))
		}
	}

	// 5. Via specs
	d.Info(fmt.Sprintf("Step 2b (infer nets) done in %v", time.Since(t0)))
	t0 = time.Now()
	d.Info("Step 3: Via specs")
	// Convert ODB++ drill points that are marked as vias into via specs so
	// natural board vias participate in layer-to-layer connectivity even when
	// the frontend does not send explicit via geometry.
	cfg.Vias = append(cfg.Vias, drillPointsToVias(parsedODB.DrillPoints, cfg.Layers)...)
	viaSpecs := extractViaSpecs(cfg.Vias, layerDict, transform)
	d.Info(fmt.Sprintf("Via specs: %d", len(viaSpecs)))

	// Punch via anti-pads so vias of one net do not sit in solid copper of another.
	punchViaHoles(layers, viaSpecs, d)
	for _, layer := range layers {
		for i := range layer.Shape {
			layer.Shape[i].EnsureOrientation()
		}
	}

	// 6. Via networks
	d.Info(fmt.Sprintf("Step 3 (via specs) done in %v", time.Since(t0)))
	t0 = time.Now()
	d.Info("Step 4: Via resistor networks")
	viaNetworks := buildViaNetworks(viaSpecs, layerDict, stackup, cfg, d)
	d.Info(fmt.Sprintf("Via networks: %d", len(viaNetworks)))

	// 7. User networks
	d.Info(fmt.Sprintf("Step 4 (via networks) done in %v", time.Since(t0)))
	t0 = time.Now()
	d.Info("Step 5: User networks")
	userNetworks := buildUserNetworks(cfg, layerDict, transform, d)
	d.Info(fmt.Sprintf("User networks: %d", len(userNetworks)))

	// 7a. Track networks connect copper polygons that are linked by traces.
	d.Info(fmt.Sprintf("Step 5 (user networks) done in %v", time.Since(t0)))
	t0 = time.Now()
	d.Info("Step 5b: Track networks")
	trackNetworks := buildTrackNetworks(cfg, layerDict, layerIDToName, transform, d)
	d.Info(fmt.Sprintf("Track networks: %d", len(trackNetworks)))

	allNetworks := append(viaNetworks, userNetworks...)
	allNetworks = append(allNetworks, trackNetworks...)
	if len(allNetworks) == 0 {
		return nil, d, fmt.Errorf("no valid networks")
	}

	// Filter layers with no connections
	connectedLayers := make(map[*problem.Layer]bool)
	for _, net := range allNetworks {
		for _, conn := range net.Connections {
			connectedLayers[conn.Layer] = true
		}
	}
	var filteredLayers []*problem.Layer
	for _, l := range layers {
		if connectedLayers[l] {
			filteredLayers = append(filteredLayers, l)
		} else {
			d.Info(fmt.Sprintf("Filtered layer: %s (no connections)", l.Name))
		}
	}
	if len(filteredLayers) == 0 {
		return nil, d, fmt.Errorf("no layers with network connections")
	}
	// Update layerDict
	layerDict = make(map[string]*problem.Layer)
	for _, l := range filteredLayers {
		layerDict[l.Name] = l
	}

	// 8. Solve
	d.Info(fmt.Sprintf("Step 5b (track networks) done in %v", time.Since(t0)))
	t0 = time.Now()
	d.Info("Step 6: Assemble + solve")
	prob := &problem.Problem{
		Layers:      filteredLayers,
		Networks:    allNetworks,
		ProjectName: cfg.ProjectName,
	}
	problem.ResetNodeIDCounter()

	sol, err := solver.Solve(prob)
	if err != nil {
		return nil, d, fmt.Errorf("solve failed: %w", err)
	}

	gni := sol.SolverInfo.GroundNodeCurrent
	rn := sol.SolverInfo.ResidualNorm
	if math.IsNaN(gni) || math.IsNaN(rn) {
		return nil, d, fmt.Errorf("singular matrix (ground_current=%v, residual=%v)", gni, rn)
	}
	d.Info(fmt.Sprintf("Solve OK: ground_current=%.6e, residual=%.6e", gni, rn))

	// Attach diagnostics context and ODB++ drill overlay geometry.
	drillVias := parsedODB.DrillPoints
	var viaCount int
	for _, p := range drillVias {
		if p.Via {
			viaCount++
		}
	}
	d.Info(fmt.Sprintf("ODB++ drill points: %d total, %d vias", len(drillVias), viaCount))

	d.Info(fmt.Sprintf("Step 6 (solve + drill) done in %v, total=%v", time.Since(t0), time.Since(analyzeStart)))

	sol.UserData = &SolutionExtras{
		Diagnostics: d,
		Config:      cfg,
		Transform:   transform,
		DrillVias:   drillVias,
		AllCopper:   parsedODB.AllLayers,
	}

	return sol, d, nil
}

// SolutionExtras holds non-solver data attached to the solution.
type SolutionExtras struct {
	Diagnostics *DiagCollector
	Config      Config
	Transform   *[4]float64
	// DrillVias feed the viewer's all-net via overlay in ODB++ board space.
	DrillVias []geometry.DrillPoint
	// AllCopper stores every copper polygon on the configured layers so the
	// viewer can render the full board context, not just the solved nets.
	AllCopper map[string]geometry.Layer
}

func extractBoardOutline(layers map[string]geometry.Layer) (geometry.MultiPolygon, string) {
	for name, gl := range layers {
		ln := strings.ToLower(name)
		if strings.Contains(ln, "outline") || strings.Contains(ln, "edge") ||
			strings.Contains(ln, "board") || strings.Contains(ln, "profile") ||
			strings.Contains(ln, "gko") || strings.Contains(ln, "gml") {
			if len(gl.Polygons) > 0 {
				return gl.Polygons, name
			}
		}
	}
	return nil, ""
}

func clipLayersWithOutline(layers []*problem.Layer, outline geometry.MultiPolygon, d *DiagCollector) {
	if len(outline) == 0 {
		return
	}

	// Board outline should be the largest polygon in the outline layer.
	// Some outline layers contain small circular keepouts/test points as the
	// first polygon; picking the largest avoids clipping the whole board to a
	// tiny circle.
	bestIdx := 0
	bestArea := polygonSignedArea(outline[0])
	for i := 1; i < len(outline); i++ {
		a := polygonSignedArea(outline[i])
		if math.Abs(a) > math.Abs(bestArea) {
			bestArea = a
			bestIdx = i
		}
	}
	outlinePoly := outline[bestIdx]
	b := outlinePoly.Bounds()
	d.Info(fmt.Sprintf("Board outline: poly[%d] rings=%d area=%.3f bounds=[%.2f,%.2f]x[%.2f,%.2f]",
		bestIdx, len(outlinePoly), bestArea, b.MinX, b.MaxX, b.MinY, b.MaxY))

	filled := fillOutlinePolygon(outlinePoly, d)
	if len(filled) == 0 {
		return
	}

	for _, layer := range layers {
		origArea := layer.Area()
		lb := layer.Bounds()
		groups := make(map[string]geometry.MultiPolygon)
		for i, polygon := range layer.Shape {
			net := ""
			if i < len(layer.NetLabels) {
				net = layer.NetLabels[i]
			}
			groups[net] = append(groups[net], polygon)
		}
		var clipped geometry.MultiPolygon
		var labels []string
		for net, group := range groups {
			pieces, err := geometry.Intersect(group, filled)
			if err != nil {
				d.Warn(fmt.Sprintf("Layer '%s' net '%s': outline clipping failed, keeping original", layer.Name, net))
				pieces = group
			}
			for _, piece := range pieces {
				piece.EnsureOrientation()
				clipped = append(clipped, piece)
				labels = append(labels, net)
			}
		}
		if len(clipped) == 0 {
			d.Warn(fmt.Sprintf("Layer '%s': empty after clipping, keeping original", layer.Name))
			continue
		}
		newArea := layerArea(clipped)
		if origArea > 0 && newArea/origArea < 0.1 {
			d.Warn(fmt.Sprintf("Layer '%s': clipping removed %.1f%% of copper, keeping original",
				layer.Name, 100*(1-newArea/origArea)))
			continue
		}
		cb := clipped.Bounds()
		layer.Shape = clipped
		layer.NetLabels = labels
		d.Info(fmt.Sprintf("Layer '%s': clipped by net (%d polygons) area %.3f->%.3f bounds [%.2f,%.2f]x[%.2f,%.2f]->[%.2f,%.2f]x[%.2f,%.2f]",
			layer.Name, len(clipped), origArea, newArea,
			lb.MinX, lb.MaxX, lb.MinY, lb.MaxY,
			cb.MinX, cb.MaxX, cb.MinY, cb.MaxY))
	}
}

func fillOutlinePolygon(poly geometry.Polygon, d *DiagCollector) geometry.MultiPolygon {
	if len(poly) == 0 || len(poly[0]) < 3 {
		return nil
	}

	// Strip interior rings (holes): board outline Gerbers draw the edge as a
	// closed line stroke, so the filled board area is the exterior only. Real
	// slots/cutouts are handled by the copper-layer Gerbers themselves.
	filled := geometry.MultiPolygon{{poly[0]}}

	// Detect thin frame outlines (stroke width only). If the filled area is much
	// smaller than the bounding box, use the bounding box rectangle for clipping
	// so copper is not reduced to a thin border.
	area := math.Abs(polygonSignedArea(poly))
	b := poly.Bounds()
	bboxArea := (b.MaxX - b.MinX) * (b.MaxY - b.MinY)
	if bboxArea > 0 && area/bboxArea < 0.5 {
		d.Info(fmt.Sprintf("Outline is thin frame (area=%.3f, bbox=%.3f, ratio=%.4f), using bounding box",
			area, bboxArea, area/bboxArea))
		rect := geometry.Ring{
			{X: b.MinX, Y: b.MinY},
			{X: b.MaxX, Y: b.MinY},
			{X: b.MaxX, Y: b.MaxY},
			{X: b.MinX, Y: b.MaxY},
		}
		return geometry.MultiPolygon{{rect}}
	}

	return filled
}

func layerArea(mp geometry.MultiPolygon) float64 {
	var area float64
	for _, poly := range mp {
		area += polygonSignedArea(poly)
	}
	return math.Abs(area)
}

// polygonCentroid returns the (unweighted) centroid of a polygon's outer ring.
func polygonCentroid(poly geometry.Polygon) geometry.Point {
	if len(poly) == 0 || len(poly[0]) == 0 {
		return geometry.Point{}
	}
	ring := poly[0]
	var cx, cy float64
	n := 0
	for _, pt := range ring {
		cx += pt.X
		cy += pt.Y
		n++
	}
	if n == 0 {
		return geometry.Point{}
	}
	return geometry.Point{X: cx / float64(n), Y: cy / float64(n)}
}

// labelForCentroid returns the net label of the original polygon that
// contains the given centroid. Falling back to the first non-empty label
// preserves net attribution when the centroid lies on a polygon edge.
func labelForCentroid(cen geometry.Point, original geometry.MultiPolygon, originalLabels []string) string {
	for i, poly := range original {
		if pointInPolygonMesh(cen, poly) {
			if i < len(originalLabels) {
				return originalLabels[i]
			}
			return ""
		}
	}
	for i, lbl := range originalLabels {
		if lbl != "" {
			_ = i
			return lbl
		}
	}
	return ""
}

// unmirrorReflectedLayers flips layers whose Gerber header says they are
// mirrored (typically bottom copper viewed from the bottom side) back to board
// coordinates. This lets IPC-D-356A netlist points and EasyEDA pad coordinates
// align with the bottom Gerber.
func unmirrorReflectedLayers(layers []*problem.Layer, outline geometry.MultiPolygon, d *DiagCollector) {
	cx, cy := boardCenter(layers, outline)
	for _, layer := range layers {
		if !layer.Reflected {
			continue
		}
		layer.Shape = mirrorMultiPolygonX(layer.Shape, cx)
		for i := range layer.Shape {
			layer.Shape[i].EnsureOrientation()
		}
		layer.Reflected = false
		b := layer.Bounds()
		d.Info(fmt.Sprintf("Layer '%s': un-mirrored about (%.2f,%.2f); bounds=[%.2f,%.2f]x[%.2f,%.2f]",
			layer.Name, cx, cy, b.MinX, b.MaxX, b.MinY, b.MaxY))
	}
}

// boardCenter returns a stable pivot for mirroring. Prefer the board outline
// centre; fall back to the centre of all layer bounds.
func boardCenter(layers []*problem.Layer, outline geometry.MultiPolygon) (float64, float64) {
	if len(outline) > 0 {
		b := outline.Bounds()
		return (b.MinX + b.MaxX) / 2, (b.MinY + b.MaxY) / 2
	}
	if len(layers) == 0 {
		return 0, 0
	}
	b := layers[0].Bounds()
	for i := 1; i < len(layers); i++ {
		bi := layers[i].Bounds()
		if bi.MinX < b.MinX {
			b.MinX = bi.MinX
		}
		if bi.MinY < b.MinY {
			b.MinY = bi.MinY
		}
		if bi.MaxX > b.MaxX {
			b.MaxX = bi.MaxX
		}
		if bi.MaxY > b.MaxY {
			b.MaxY = bi.MaxY
		}
	}
	return (b.MinX + b.MaxX) / 2, (b.MinY + b.MaxY) / 2
}

func mirrorMultiPolygonX(mp geometry.MultiPolygon, cx float64) geometry.MultiPolygon {
	out := make(geometry.MultiPolygon, len(mp))
	for i, poly := range mp {
		p := make(geometry.Polygon, len(poly))
		for j, ring := range poly {
			r := make(geometry.Ring, len(ring))
			for k, pt := range ring {
				r[k] = geometry.Point{X: 2*cx - pt.X, Y: pt.Y}
			}
			p[j] = r
		}
		out[i] = p
	}
	return out
}

func polygonSignedArea(poly geometry.Polygon) float64 {
	var area float64
	for i, ring := range poly {
		a := ring.Area()
		if i == 0 {
			area += a
		} else {
			area -= a
		}
	}
	return area
}

func logLayerPolygonSummary(layers []*problem.Layer, d *DiagCollector) {
	for _, l := range layers {
		b := l.Bounds()
		summary := fmt.Sprintf("Layer '%s' summary: polygons=%d area=%.3f bounds=[%.2f,%.2f]x[%.2f,%.2f]",
			l.Name, len(l.Shape), l.Area(), b.MinX, b.MaxX, b.MinY, b.MaxY)
		d.Info(summary)
		fmt.Printf("[LayerSummary] %s\n", summary)
		for i, poly := range l.Shape {
			pb := poly.Bounds()
			label := ""
			if i < len(l.NetLabels) {
				label = l.NetLabels[i]
			}
			rings := len(poly)
			polyLog := fmt.Sprintf("  poly[%d]: net='%s' rings=%d bounds=[%.2f,%.2f]x[%.2f,%.2f] area=%.3f",
				i, label, rings, pb.MinX, pb.MaxX, pb.MinY, pb.MaxY, polygonSignedArea(poly))
			d.Info(polyLog)
			fmt.Printf("[LayerSummary] %s\n", polyLog)
		}
	}
}

func computeCoordinateTransform(bounds *Bounds, layers []*problem.Layer, allCopper map[string]geometry.Layer, cfg Config, outline geometry.MultiPolygon, d *DiagCollector) *[4]float64 {
	if bounds == nil || (len(layers) == 0 && len(allCopper) == 0) {
		return nil
	}

	// Prefer the full-board copper stencil when available; it covers every
	// polygon on every layer and gives a board-level bounding box comparable
	// to the old Gerber flow. Fall back to the selected-net layers.
	var allBounds geometry.Box
	hasBounds := false
	if len(allCopper) > 0 {
		for _, gl := range allCopper {
			if len(gl.Polygons) == 0 {
				continue
			}
			b := gl.Polygons.Bounds()
			if !hasBounds {
				allBounds = b
				hasBounds = true
				continue
			}
			if b.MinX < allBounds.MinX {
				allBounds.MinX = b.MinX
			}
			if b.MinY < allBounds.MinY {
				allBounds.MinY = b.MinY
			}
			if b.MaxX > allBounds.MaxX {
				allBounds.MaxX = b.MaxX
			}
			if b.MaxY > allBounds.MaxY {
				allBounds.MaxY = b.MaxY
			}
		}
	}
	if !hasBounds {
		allBounds = layers[0].Bounds()
		for i := 1; i < len(layers); i++ {
			b := layers[i].Bounds()
			if b.MinX < allBounds.MinX {
				allBounds.MinX = b.MinX
			}
			if b.MinY < allBounds.MinY {
				allBounds.MinY = b.MinY
			}
			if b.MaxX > allBounds.MaxX {
				allBounds.MaxX = b.MaxX
			}
			if b.MaxY > allBounds.MaxY {
				allBounds.MaxY = b.MaxY
			}
		}
	}

	easyedaCx := (bounds.MinX + bounds.MaxX) / 2
	easyedaCy := (bounds.MinY + bounds.MaxY) / 2
	copperCx := (allBounds.MinX + allBounds.MaxX) / 2
	copperCy := (allBounds.MinY + allBounds.MaxY) / 2
	ox := copperCx - easyedaCx
	oy := copperCy - easyedaCy

	// EasyEDA PCB primitives and ODB++ exports are both supposed to live in the
	// same millimetre coordinate system. In practice some ODB++ exporters shift
	// the board away from the design origin, in which case aligning the copper
	// centroid with the EasyEDA pad/via centroid brings the overlay back onto
	// the PCB canvas. If the centroids already agree (within tolerance) keep
	// the transform identity to avoid jittering perfectly aligned boards.
	tol := 0.001
	if math.Abs(ox) < tol && math.Abs(oy) < tol {
		d.Info(fmt.Sprintf("EasyEDA bounds: X=[%.2f,%.2f] Y=[%.2f,%.2f]",
			bounds.MinX, bounds.MaxX, bounds.MinY, bounds.MaxY))
		d.Info(fmt.Sprintf("ODB bounds:    X=[%.2f,%.2f] Y=[%.2f,%.2f]",
			allBounds.MinX, allBounds.MaxX, allBounds.MinY, allBounds.MaxY))
		d.Info("Transform: shared EasyEDA/ODB coordinates, using identity")
		return &[4]float64{1, 1, 0, 0}
	}

	d.Info(fmt.Sprintf("EasyEDA bounds: X=[%.2f,%.2f] Y=[%.2f,%.2f]",
		bounds.MinX, bounds.MaxX, bounds.MinY, bounds.MaxY))
	d.Info(fmt.Sprintf("ODB bounds:    X=[%.2f,%.2f] Y=[%.2f,%.2f]",
		allBounds.MinX, allBounds.MaxX, allBounds.MinY, allBounds.MaxY))
	d.Info(fmt.Sprintf("Transform: scale=(1.0000,1.0000), offset=(%.4f,%.4f)", ox, oy))
	return &[4]float64{1, 1, ox, oy}
}

type orientPoint struct {
	x, y   float64
	net    string
	layers []*problem.Layer
}

func collectOrientationPoints(cfg Config, layerDict map[string]*problem.Layer, allLayers []*problem.Layer) []orientPoint {
	var pts []orientPoint
	add := func(p Pad) {
		if p.IsTHT {
			pts = append(pts, orientPoint{x: p.X, y: p.Y, net: p.Net, layers: allLayers})
			return
		}
		if l := layerDict[p.Layer]; l != nil {
			pts = append(pts, orientPoint{x: p.X, y: p.Y, net: p.Net, layers: []*problem.Layer{l}})
		}
	}
	for _, p := range cfg.Pads {
		add(p)
	}
	for _, src := range cfg.Sources {
		for _, p := range src.Pads {
			add(p)
		}
		for _, p := range src.GndPads {
			add(p)
		}
	}
	for _, ld := range cfg.Loads {
		for _, p := range ld.Pads {
			add(p)
		}
		for _, p := range ld.GndPads {
			add(p)
		}
	}
	for _, v := range cfg.Vias {
		var viaLayers []*problem.Layer
		for _, name := range v.LayerNames {
			if l := layerDict[name]; l != nil {
				viaLayers = append(viaLayers, l)
			}
		}
		if len(viaLayers) == 0 {
			viaLayers = allLayers
		}
		pts = append(pts, orientPoint{x: v.X, y: v.Y, layers: viaLayers})
	}
	return pts
}

func scoreOrientation(sx, sy, ox, oy float64, pts []orientPoint, outline geometry.Polygon) int {
	type polyKey struct {
		layer   *problem.Layer
		polyIdx int
	}
	polyNets := make(map[polyKey]map[string]int)
	polyPts := make(map[polyKey][]int)

	for i, p := range pts {
		xg := p.x*sx + ox
		yg := p.y*sy + oy
		pt := geometry.Point{X: xg, Y: yg}
		// Primary requirement: the point must be inside the board outline.
		if len(outline) > 0 && !pointInPolygonMesh(pt, outline) {
			continue
		}
		for _, l := range p.layers {
			for pi, poly := range l.Shape {
				if !pointTouchesPolygon(pt, poly) {
					continue
				}
				k := polyKey{layer: l, polyIdx: pi}
				if polyNets[k] == nil {
					polyNets[k] = make(map[string]int)
				}
				polyNets[k][p.net]++
				polyPts[k] = append(polyPts[k], i)
				break
			}
		}
	}

	score := 0
	for k, indices := range polyPts {
		nets := polyNets[k]
		for _, idx := range indices {
			if len(nets) == 1 && nets[pts[idx].net] > 0 {
				score++
			} else {
				score -= 2
			}
		}
	}
	return score
}

// pointTouchesPolygon reports whether pt is inside the filled area or inside
// any ring of poly. The latter catches THT pad centres that sit in drilled holes.
func pointTouchesPolygon(pt geometry.Point, poly geometry.Polygon) bool {
	if pointInPolygonMesh(pt, poly) {
		return true
	}
	return pointInsidePolygonRings(pt, poly)
}

func buildStackup(thickness map[string]float64, layers []*problem.Layer) []float64 {
	stackup := make([]float64, len(layers))
	for i, layer := range layers {
		if t, ok := thickness[layer.Name]; ok {
			stackup[i] = t
		} else {
			stackup[i] = 0.035
		}
	}
	return stackup
}

// subtractDrillHolesFromCopper returns a new copy of the copper polygons with
// all drill holes punched out. Net labels are preserved per input polygon.
func subtractDrillHolesFromCopper(polygons geometry.MultiPolygon, netLabels []string, drillHoles geometry.MultiPolygon) (geometry.MultiPolygon, []string, error) {
	if len(drillHoles) == 0 {
		return polygons, netLabels, nil
	}
	groups := make(map[string]geometry.MultiPolygon)
	for i, polygon := range polygons {
		net := ""
		if i < len(netLabels) {
			net = netLabels[i]
		}
		groups[net] = append(groups[net], polygon)
	}
	var punched geometry.MultiPolygon
	var labels []string
	for net, group := range groups {
		pieces, err := geometry.Difference(group, drillHoles)
		if err != nil {
			return nil, nil, err
		}
		for _, piece := range pieces {
			piece.EnsureOrientation()
			punched = append(punched, piece)
			labels = append(labels, net)
		}
	}
	return punched, labels, nil
}

// transformPointRing maps every point of a ring from EasyEDA space to geometry
// space using the transform tuple computed by computeCoordinateTransform.
func transformPointRing(ring geometry.Ring, transform *[4]float64) geometry.Ring {
	if transform == nil {
		return ring
	}
	out := make(geometry.Ring, len(ring))
	for i, p := range ring {
		out[i] = transformPoint(p.X, p.Y, transform)
	}
	return out
}

// overrideNetLabelsWithLiveGeometry labels otherwise-unlabeled ODB++ polygons
// using live copper pour geometry from EasyEDA.  This fixes preview/solver
// mismatches when the ODB++ exporter omits net labels for some features.
func overrideNetLabelsWithLiveGeometry(
	odb *geometry.ODBData,
	copperPours []CopperPour,
	pads []Pad,
	tracks []Track,
	layerIDToName map[int]string,
	transform *[4]float64,
	d *DiagCollector,
) {
	if len(copperPours) == 0 && len(pads) == 0 && len(tracks) == 0 {
		return
	}

	type livePour struct {
		net  string
		poly geometry.Polygon
	}
	poursByLayer := make(map[string][]livePour)
	for _, cp := range copperPours {
		layerName := layerIDToName[cp.Layer]
		if layerName == "" {
			continue
		}
		path := transformPointRing(cp.Path, transform)
		holes := make([]geometry.Ring, len(cp.Holes))
		for i, h := range cp.Holes {
			holes[i] = transformPointRing(h, transform)
		}
		poly := append(geometry.Polygon{path}, holes...)
		poursByLayer[layerName] = append(poursByLayer[layerName], livePour{net: cp.Net, poly: poly})
	}

	type padHint struct {
	net string
	pt  geometry.Point
	 tol float64
	}
	padsByLayer := make(map[string][]padHint)
	for _, p := range pads {
		layerName := p.Layer
		if layerName == "" {
			continue
		}
		pt := transformPoint(p.X, p.Y, transform)
		padsByLayer[layerName] = append(padsByLayer[layerName], padHint{net: p.Net, pt: pt, tol: 0.25})
	}

	type trackHint struct {
	net    string
	a      geometry.Point
	b      geometry.Point
	radius float64
	}
	tracksByLayer := make(map[string][]trackHint)
	for _, t := range tracks {
		layerName := layerIDToName[t.Layer]
		if layerName == "" {
			continue
		}
		a := transformPoint(t.X1, t.Y1, transform)
		b := transformPoint(t.X2, t.Y2, transform)
		tracksByLayer[layerName] = append(tracksByLayer[layerName], trackHint{net: t.Net, a: a, b: b, radius: t.Width / 2})
	}

	overrideLayer := func(name string, layer *geometry.Layer) {
		pours := poursByLayer[name]
		ps := padsByLayer[name]
		trs := tracksByLayer[name]
		if len(pours) == 0 && len(ps) == 0 && len(trs) == 0 {
			return
		}
		if layer.NetLabels == nil {
			layer.NetLabels = make([]string, len(layer.Polygons))
		}
		updated := 0
		for i, poly := range layer.Polygons {
			if i < len(layer.NetLabels) && layer.NetLabels[i] != "" {
				continue
			}
			pt := polygonCentroid(poly)
			label := ""
			for _, pour := range pours {
				if pointInPolygonMesh(pt, pour.poly) {
					label = pour.net
					break
				}
			}
			if label == "" {
				for _, p := range ps {
					if math.Hypot(pt.X-p.pt.X, pt.Y-p.pt.Y) <= p.tol {
						label = p.net
						break
					}
				}
			}
			if label == "" {
				for _, t := range trs {
					dist := distanceToSegment(pt, t.a, t.b)
					if dist <= t.radius+0.05 {
						label = t.net
						break
					}
				}
			}
			if label != "" {
				layer.NetLabels[i] = label
				updated++
			}
		}
		if updated > 0 && d != nil {
			d.Info(fmt.Sprintf("Live geometry override: layer '%s' labeled %d polygon(s)", name, updated))
		}
	}

	for name, layer := range odb.AllLayers {
		overrideLayer(name, &layer)
		odb.AllLayers[name] = layer
	}
	for name, layer := range odb.Layers {
		overrideLayer(name, &layer)
		odb.Layers[name] = layer
	}
}

// repropagateNetLabels re-runs net label propagation after live-geometry seeds
// have been added, then rebuilds the selected-net Layers from AllLayers using
// the target net filter.
func repropagateNetLabels(odb *geometry.ODBData, layerNames []string, targetNets map[string]bool, d *DiagCollector) {
	if len(layerNames) == 0 {
		return
	}
	polys := make(map[string]geometry.MultiPolygon)
	labels := make(map[string][]string)
	for _, name := range layerNames {
		layer, ok := odb.AllLayers[name]
		if !ok {
			continue
		}
		polys[name] = layer.Polygons
		labels[name] = layer.NetLabels
	}
	newLabels := geometry.PropagateNetLabelsWithVias(layerNames, polys, labels, odb.DrillPoints, nil)
	for _, name := range layerNames {
		layer, ok := odb.AllLayers[name]
		if !ok {
			continue
		}
		layer.NetLabels = newLabels[name]
		odb.AllLayers[name] = layer
	}

	// Rebuild Layers from the corrected AllLayers, filtering by target nets.
	for _, name := range layerNames {
		allLayer, ok := odb.AllLayers[name]
		if !ok {
			continue
		}
		var selectedPolys geometry.MultiPolygon
		var selectedLabels []string
		for i, poly := range allLayer.Polygons {
			label := ""
			if i < len(allLayer.NetLabels) {
				label = allLayer.NetLabels[i]
			}
			if targetNets != nil && !targetNets[label] {
				continue
			}
			selectedPolys = append(selectedPolys, poly)
			selectedLabels = append(selectedLabels, label)
		}
		if len(selectedPolys) > 0 {
			odb.Layers[name] = geometry.Layer{
				Name:      name,
				Polygons:  selectedPolys,
				NetLabels: selectedLabels,
			}
		} else {
			delete(odb.Layers, name)
		}
	}
	if d != nil {
		d.Info(fmt.Sprintf("Re-propagated net labels across %d layer(s)", len(layerNames)))
	}
}

// drillPointsToVias turns ODB++ drill points that are marked as vias into
// generic Via records. The frontend only sends user-defined vias; the ODB++
// archive already contains every physical via hole, so we derive the missing
// via geometry from it. Net inference (via inferViaNet) samples the copper
// around each hole, allowing natural board vias to stitch layers for the nets
// that flow through them.
func drillPointsToVias(drillPoints []geometry.DrillPoint, layerConfigs []LayerConfig) []Via {
	if len(drillPoints) == 0 || len(layerConfigs) == 0 {
		return nil
	}
	var layerNames []string
	for _, lc := range layerConfigs {
		layerNames = append(layerNames, lc.Name)
	}
	var vias []Via
	for _, dp := range drillPoints {
		if !dp.Via {
			continue
		}
		// Typical annular ring is ~0.1–0.15 mm per side; use a conservative
		// pad diameter so net inference and snapping can reach the copper
		// ring around the drilled hole.
		outer := dp.Diameter + 0.4
		if outer < 0.3 {
			outer = 0.3
		}
		vias = append(vias, Via{
			X:            dp.X,
			Y:            dp.Y,
			HoleDiameter: dp.Diameter,
			Diameter:     outer,
			LayerNames:   layerNames,
			Net:          "",
			ViaType:      "through",
		})
	}
	return vias
}
