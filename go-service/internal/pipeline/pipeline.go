package pipeline

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

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
}

func (d *DiagCollector) Warn(msg string) {
	d.Lines = append(d.Lines, "[WARN] "+msg)
}

func (d *DiagCollector) Error(msg string) {
	d.Lines = append(d.Lines, "[ERROR] "+msg)
}

// Analyze runs the full PDN analysis pipeline.
func Analyze(gerberZip []byte, configJSON string, ipc356aText string) (*solver.Solution, *DiagCollector, error) {
	d := &DiagCollector{}

	var cfg Config
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return nil, d, fmt.Errorf("failed to parse config: %w", err)
	}

	// Parse authoritative IPC-D-356A netlist if provided.
	var ipcNetlist *IPC356ANetlist
	if ipc356aText != "" {
		var err error
		ipcNetlist, err = ParseIPC356A(ipc356aText)
		if err != nil {
			d.Warn(fmt.Sprintf("IPC-D-356A parse failed: %v", err))
			ipcNetlist = nil
		} else {
			d.Info(fmt.Sprintf("IPC-D-356A netlist: %d pads, %d vias, %d traces, %d board edges",
				len(ipcNetlist.Pads), len(ipcNetlist.Vias), len(ipcNetlist.Traces), len(ipcNetlist.BoardEdge)))
		}
	}

	d.Info(fmt.Sprintf("project=%s, layers=%d, vias=%d, pads=%d, sources=%d, loads=%d",
		cfg.ProjectName, len(cfg.Layers), len(cfg.Vias), len(cfg.Pads), len(cfg.Sources), len(cfg.Loads)))

	if len(cfg.Layers) == 0 {
		return nil, d, fmt.Errorf("no layer configs")
	}

	// 1. Parse Gerber
	d.Info("Step 1: Parse Gerber files")
	layerNames := make([]string, len(cfg.Layers))
	for i, lc := range cfg.Layers {
		layerNames[i] = lc.Name
	}
	parsed, err := geometry.ParseGerberZip(gerberZip, layerNames)
	if err != nil {
		return nil, d, fmt.Errorf("Gerber parse failed: %w", err)
	}

	var layers []*problem.Layer
	for _, lc := range cfg.Layers {
		gl, ok := parsed[lc.Name]
		if !ok {
			// Try matching by normalized name
			for _, candidate := range parsed {
				if matchLayerName(lc.Name, candidate.Filename) {
					gl = candidate
					d.Info(fmt.Sprintf("Layer '%s' matched file '%s'", lc.Name, candidate.Filename))
					break
				}
			}
		}
		if gl.Name == "" {
			continue
		}
		if len(gl.Polygons) == 0 {
			d.Warn(fmt.Sprintf("Layer '%s': Gerber parse result empty", lc.Name))
			continue
		}
		layer := &problem.Layer{
			Shape:       gl.Polygons,
			Name:        lc.Name,
			Conductance: lc.EffectiveConductance(),
		}
		layers = append(layers, layer)
		d.Info(fmt.Sprintf("Layer '%s': %d polygons from Gerber", lc.Name, len(gl.Polygons)))
	}

	if len(layers) == 0 {
		return nil, d, fmt.Errorf("no valid copper layers from Gerber")
	}

	// Clean each layer: normalize ring orientations first, then union overlapping
	// dark polygons. Clipper2 is sensitive to winding direction, so orient before
	// union to avoid negative polygons being treated as holes and cancelling out
	// the real copper.
	for _, layer := range layers {
		if len(layer.Shape) == 0 {
			continue
		}
		for i := range layer.Shape {
			layer.Shape[i].EnsureOrientation()
		}
		unioned, err := geometry.Union(layer.Shape, nil)
		if err != nil || len(unioned) == 0 {
			d.Warn(fmt.Sprintf("Layer '%s': union failed (%v), using raw geometry", layer.Name, err))
		} else {
			layer.Shape = unioned
		}
		// Morphological close to weld sub-resolution gaps between touching copper
		// islands. This mirrors Python's shapely buffer(1e-4).buffer(-1e-4) cleanup.
		// Disabled for now: Clipper2's union already produces clean polygons with
		// holes from tracespace's region output; closing here merged distinct nets.
		// const closeDelta = 1e-4
		// closed, err := geometry.Close(layer.Shape, closeDelta)
		// if err == nil && len(closed) > 0 {
		// 	layer.Shape = closed
		// } else if err != nil {
		// 	d.Warn(fmt.Sprintf("Layer '%s': morphological close failed (%v), using unioned geometry", layer.Name, err))
		// }
		for i := range layer.Shape {
			layer.Shape[i].EnsureOrientation()
		}
		d.Info(fmt.Sprintf("Layer '%s': cleaned to %d polygon(s) area=%.3f", layer.Name, len(layer.Shape), layer.Area()))
	}

	layerDict := make(map[string]*problem.Layer)
	for _, l := range layers {
		layerDict[l.Name] = l
	}

	// 2. Extract board outline and clip
	d.Info("Step 2: Board outline clipping")
	outline, outlineName := extractBoardOutline(parsed)
	if outline != nil {
		d.Info(fmt.Sprintf("Using outline layer '%s' with %d polygon(s)", outlineName, len(outline)))
		clipLayersWithOutline(layers, outline, d)
	} else {
		d.Info("No board outline found")
	}

	// 2a. Subtract Gerber drill-file holes from every copper layer so the FEM
	// mesh and copper preview accurately represent the drilled board.
	drillHoles, err := geometry.ParseDrillHoles(gerberZip)
	if err != nil {
		d.Warn(fmt.Sprintf("Drill hole parsing failed: %v", err))
	}
	if len(drillHoles) > 0 {
		d.Info(fmt.Sprintf("Subtracting %d drill-hole polygon(s) from all copper layers", len(drillHoles)))
		for _, layer := range layers {
			newShape, err := geometry.Difference(layer.Shape, drillHoles)
			if err != nil {
				d.Warn(fmt.Sprintf("Layer '%s': drill subtraction failed (%v), keeping original", layer.Name, err))
				continue
			}
			if len(newShape) == 0 {
				d.Warn(fmt.Sprintf("Layer '%s': empty after drill subtraction, keeping original", layer.Name))
				continue
			}
			layer.Shape = newShape
			for i := range layer.Shape {
				layer.Shape[i].EnsureOrientation()
			}
		}
	}

	// 3. Coordinate transform
	transform := computeCoordinateTransform(cfg.EasyEDABounds, layers, cfg, outline, d)
	if transform != nil {
		d.Info(fmt.Sprintf("Transform: scale=(%.4f,%.4f), offset=(%.2f,%.2f)", transform[0], transform[1], transform[2], transform[3]))
	}

	// 4. Build stackup
	stackup := buildStackup(cfg.LayerCuThickness, layers)

	// 4a. Infer net labels for each copper polygon from pad positions and tracks.
	d.Info("Step 2b: Infer polygon nets")
	layerIDToName := make(map[int]string)
	for _, lc := range cfg.Layers {
		layerIDToName[lc.LayerID] = lc.Name
	}

	if ipcNetlist != nil {
		ensureNetLabels(layers)
		sx, sy, ox, oy := alignIPC356AToGerber(ipcNetlist, outline)
		d.Info(fmt.Sprintf("IPC-D-356A alignment: scale=(%.4f,%.4f), offset=(%.4f,%.4f)", sx, sy, ox, oy))
		applyIPC356AOffset(ipcNetlist, ox, oy)
		inferPolygonNetsFromIPC356A(layers, ipcNetlist, d)
		// Fall back to pad/track inference only for polygons the netlist did not label.
		inferPolygonNets(layers, cfg.Pads, transform, d, true)
		inferPolygonNetsFromTracks(layers, cfg.Tracks, layerIDToName, transform)
	} else {
		inferPolygonNets(layers, cfg.Pads, transform, d, false)
		inferPolygonNetsFromTracks(layers, cfg.Tracks, layerIDToName, transform)
	}
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

	// 5. Via specs
	d.Info("Step 3: Via specs")
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
	d.Info("Step 4: Via resistor networks")
	selectedNets := selectedNetworks(cfg.Sources)
	viaNetworks := buildViaNetworks(viaSpecs, layerDict, stackup, selectedNets, d)
	d.Info(fmt.Sprintf("Via networks: %d", len(viaNetworks)))

	// 7. User networks
	d.Info("Step 5: User networks")
	userNetworks := buildUserNetworks(cfg, layerDict, transform, d)
	d.Info(fmt.Sprintf("User networks: %d", len(userNetworks)))

	// 7a. Track networks connect copper polygons that are linked by traces.
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

	// Attach diagnostics context for later serialization
	sol.UserData = &SolutionExtras{
		Diagnostics: d,
		Config:      cfg,
		Transform:   transform,
	}

	return sol, d, nil
}

// SolutionExtras holds non-solver data attached to the solution.
type SolutionExtras struct {
	Diagnostics *DiagCollector
	Config      Config
	Transform   *[4]float64
}

func matchLayerName(layerName, filename string) bool {
	ln := strings.ToLower(layerName)
	fn := strings.ToLower(filename)
	if strings.Contains(fn, ln) {
		return true
	}
	lnNorm := normalizeName(ln)
	fnNorm := normalizeName(fn)
	return strings.Contains(fnNorm, lnNorm)
}

func normalizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == ' ' || r == '_' || r == '-' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func extractBoardOutline(layers map[string]geometry.GerberLayer) (geometry.MultiPolygon, string) {
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
		clipped, err := geometry.Intersect(layer.Shape, filled)
		if err != nil || len(clipped) == 0 {
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
		for i := range layer.Shape {
			layer.Shape[i].EnsureOrientation()
		}
		d.Info(fmt.Sprintf("Layer '%s': clipped OK (%d polygons) area %.3f->%.3f bounds [%.2f,%.2f]x[%.2f,%.2f]->[%.2f,%.2f]x[%.2f,%.2f]",
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
		d.Info(fmt.Sprintf("Layer '%s' summary: polygons=%d area=%.3f bounds=[%.2f,%.2f]x[%.2f,%.2f]",
			l.Name, len(l.Shape), l.Area(), b.MinX, b.MaxX, b.MinY, b.MaxY))
		for i, poly := range l.Shape {
			pb := poly.Bounds()
			label := ""
			if i < len(l.NetLabels) {
				label = l.NetLabels[i]
			}
			rings := len(poly)
			d.Info(fmt.Sprintf("  poly[%d]: net='%s' rings=%d bounds=[%.2f,%.2f]x[%.2f,%.2f] area=%.3f",
				i, label, rings, pb.MinX, pb.MaxX, pb.MinY, pb.MaxY, polygonSignedArea(poly)))
		}
	}
}

func computeCoordinateTransform(bounds *Bounds, layers []*problem.Layer, cfg Config, outline geometry.MultiPolygon, d *DiagCollector) *[4]float64 {
	if bounds == nil || len(layers) == 0 {
		return nil
	}
	allBounds := layers[0].Bounds()
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

	// Match the Python backend: same scale, same Y direction (both EasyEDA and
	// pygerber/tracespace produce Y-down coordinates). Align centers to obtain
	// the origin offset. Axis flipping was causing orientation mismatches and
	// missed pad connections.
	easyedaCx := (bounds.MinX + bounds.MaxX) / 2
	easyedaCy := (bounds.MinY + bounds.MaxY) / 2
	gerberCx := (allBounds.MinX + allBounds.MaxX) / 2
	gerberCy := (allBounds.MinY + allBounds.MaxY) / 2
	ox := gerberCx - easyedaCx
	oy := gerberCy - easyedaCy

	d.Info(fmt.Sprintf("EasyEDA bounds: X=[%.2f,%.2f] Y=[%.2f,%.2f]",
		bounds.MinX, bounds.MaxX, bounds.MinY, bounds.MaxY))
	d.Info(fmt.Sprintf("Gerber bounds: X=[%.2f,%.2f] Y=[%.2f,%.2f]",
		allBounds.MinX, allBounds.MaxX, allBounds.MinY, allBounds.MaxY))
	d.Info(fmt.Sprintf("Transform: scale=(1.0000,1.0000), offset=(%.2f,%.2f)", ox, oy))

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

func selectedNetworks(sources []Source) map[string]bool {
	nets := make(map[string]bool)
	for _, src := range sources {
		nets[src.Net] = true
	}
	return nets
}
