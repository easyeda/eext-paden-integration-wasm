package pipeline

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/easyeda/paden-wasm/internal/geometry"
	"github.com/easyeda/paden-wasm/internal/problem"
	"github.com/easyeda/paden-wasm/internal/solver"
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
func Analyze(gerberZip []byte, configJSON string) (*solver.Solution, *DiagCollector, error) {
	d := &DiagCollector{}

	var cfg Config
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return nil, d, fmt.Errorf("failed to parse config: %w", err)
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

	layerDict := make(map[string]*problem.Layer)
	for _, l := range layers {
		layerDict[l.Name] = l
	}

	// 2. Extract board outline and clip
	d.Info("Step 2: Board outline clipping")
	outline := extractBoardOutline(parsed)
	if outline != nil {
		clipLayersWithOutline(layers, outline, d)
	} else {
		d.Info("No board outline found")
	}

	// 3. Coordinate transform
	transform := computeCoordinateTransform(cfg.EasyEDABounds, layers, d)
	if transform != nil {
		d.Info(fmt.Sprintf("Transform: scale=(%.4f,%.4f), offset=(%.2f,%.2f)", transform[0], transform[1], transform[2], transform[3]))
	}

	// 4. Build stackup
	stackup := buildStackup(cfg.LayerCuThickness, layers)

	// 5. Via specs
	d.Info("Step 3: Via specs")
	viaSpecs := extractViaSpecs(cfg.Vias, layerDict, transform)
	d.Info(fmt.Sprintf("Via specs: %d", len(viaSpecs)))

	// 6. Via networks
	d.Info("Step 4: Via resistor networks")
	selectedNets := selectedNetworks(cfg.Sources)
	viaNetworks := buildViaNetworks(viaSpecs, layerDict, stackup, selectedNets, d)
	d.Info(fmt.Sprintf("Via networks: %d", len(viaNetworks)))

	// 7. User networks
	d.Info("Step 5: User networks")
	userNetworks := buildUserNetworks(cfg, layerDict, transform, d)
	d.Info(fmt.Sprintf("User networks: %d", len(userNetworks)))

	allNetworks := append(viaNetworks, userNetworks...)
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

func extractBoardOutline(layers map[string]geometry.GerberLayer) geometry.MultiPolygon {
	for name, gl := range layers {
		ln := strings.ToLower(name)
		if strings.Contains(ln, "outline") || strings.Contains(ln, "edge") ||
			strings.Contains(ln, "board") || strings.Contains(ln, "profile") ||
			strings.Contains(ln, "gko") || strings.Contains(ln, "gml") {
			if len(gl.Polygons) > 0 {
				return gl.Polygons
			}
		}
	}
	return nil
}

func clipLayersWithOutline(layers []*problem.Layer, outline geometry.MultiPolygon, d *DiagCollector) {
	if len(outline) == 0 {
		return
	}
	// Use the first polygon of the outline
	outlinePoly := outline[0]
	filled := fillOutlinePolygon(outlinePoly)
	if len(filled) == 0 {
		return
	}

	for _, layer := range layers {
		origArea := layer.Area()
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
		layer.Shape = clipped
		d.Info(fmt.Sprintf("Layer '%s': clipped OK (%d polygons)", layer.Name, len(clipped)))
	}
}

func fillOutlinePolygon(poly geometry.Polygon) geometry.MultiPolygon {
	// Strip holes, keep only exterior ring
	if len(poly) == 0 || len(poly[0]) < 3 {
		return nil
	}
	exterior := poly[0]
	return geometry.MultiPolygon{{exterior}}
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

func computeCoordinateTransform(bounds *Bounds, layers []*problem.Layer, d *DiagCollector) *[4]float64 {
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

	return &[4]float64{1.0, 1.0, ox, oy}
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
