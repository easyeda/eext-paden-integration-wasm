// Package wasmapi serializes solver results to JSON matching the Python backend format.
package wasmapi

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/easyeda/eext-paden-integration/go-service/internal/geometry"
	"github.com/easyeda/eext-paden-integration/go-service/internal/pipeline"
	"github.com/easyeda/eext-paden-integration/go-service/internal/problem"
	"github.com/easyeda/eext-paden-integration/go-service/internal/solver"
)

// CurrentCheckOutput matches Python's CurrentCheckOutput.
type CurrentCheckOutput struct {
	NetworkName       string  `json:"network_name"`
	LayerName         string  `json:"layer_name"`
	CalculatedCurrent float64 `json:"calculated_current"`
	MaxAllowedCurrent float64 `json:"max_allowed_current"`
	Utilization       float64 `json:"utilization"`
	IsExceeded        bool    `json:"is_exceeded"`
	TraceWidthMm      float64 `json:"trace_width_mm"`
	CopperOz          float64 `json:"copper_oz"`
	TempRise          float64 `json:"temp_rise"`
	Message           string  `json:"message"`
}

// MeshTriangleOutput matches Python's MeshTriangleOutput.
type MeshTriangleOutput struct {
	Vertices [3]int `json:"vertices"`
}

// MeshOutput matches Python's MeshOutput.
type MeshOutput struct {
	Vertices         [][]float64          `json:"vertices"`
	Triangles        []MeshTriangleOutput `json:"triangles"`
	Potentials       []float64            `json:"potentials"`
	PowerDensities   []float64            `json:"power_densities"`
	CurrentDensities [][]float64          `json:"current_densities"`
}

// DisconnectedMeshOutput matches Python's DisconnectedMeshOutput.
type DisconnectedMeshOutput struct {
	Vertices  [][]float64          `json:"vertices"`
	Triangles []MeshTriangleOutput `json:"triangles"`
}

// LayerSolutionOutput matches Python's LayerSolutionOutput.
type LayerSolutionOutput struct {
	LayerName          string                   `json:"layer_name"`
	Meshes             []MeshOutput             `json:"meshes"`
	DisconnectedMeshes []DisconnectedMeshOutput `json:"disconnected_meshes"`
}

// SolverInfoOutput matches Python's SolverInfoOutput.
type SolverInfoOutput struct {
	GroundNodeCurrent float64 `json:"ground_node_current"`
	ResidualNorm      float64 `json:"residual_norm"`
}

// AnalyzeResponse matches Python's AnalyzeResponse.
type AnalyzeResponse struct {
	Success          bool                         `json:"success"`
	Message          string                       `json:"message"`
	LayerSolutions   []LayerSolutionOutput        `json:"layer_solutions"`
	SolverInfo       *SolverInfoOutput            `json:"solver_info"`
	ConnectionPoints map[string][]ConnectionPoint `json:"connection_points"`
	LayerBoundaries  map[string][]BoundaryPolygon `json:"layer_boundaries"`
	Diagnostics      []string                     `json:"diagnostics"`
	CurrentWarnings  []CurrentCheckOutput         `json:"current_warnings"`
}

// ConnectionPoint is a single connection point entry.
type ConnectionPoint struct {
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	IsSource bool    `json:"is_source"`
}

// BoundaryPolygon describes layer boundary exterior and holes as [x,y] arrays
// so the JS renderer can consume them directly.
type BoundaryPolygon struct {
	Exterior [][]float64   `json:"exterior"`
	Holes    [][][]float64 `json:"holes"`
}

// SerializeSolution converts a solver.Solution to JSON bytes.
func SerializeSolution(sol *solver.Solution) ([]byte, error) {
	extras, ok := sol.UserData.(*pipeline.SolutionExtras)
	if !ok {
		return nil, fmt.Errorf("solution missing pipeline extras")
	}

	resp := AnalyzeResponse{
		Success:          true,
		Message:          "求解完成",
		LayerSolutions:   serializeLayerSolutions(sol, extras.Transform),
		SolverInfo:       &SolverInfoOutput{GroundNodeCurrent: sol.SolverInfo.GroundNodeCurrent, ResidualNorm: sol.SolverInfo.ResidualNorm},
		ConnectionPoints: serializeConnectionPoints(sol, extras.Transform),
		LayerBoundaries:  serializeLayerBoundaries(sol, extras.Transform),
		Diagnostics:      extras.Diagnostics.Lines,
		CurrentWarnings:  checkCurrentCapacities(sol, extras.Config),
	}

	return json.Marshal(resp)
}

func serializeLayerSolutions(sol *solver.Solution, transform *[4]float64) []LayerSolutionOutput {
	out := make([]LayerSolutionOutput, len(sol.Problem.Layers))
	for i, layer := range sol.Problem.Layers {
		ls := sol.LayerSolutions[i]
		lso := LayerSolutionOutput{
			LayerName:          layer.Name,
			Meshes:             make([]MeshOutput, 0),
			DisconnectedMeshes: make([]DisconnectedMeshOutput, 0),
		}
		for mi, cm := range ls.CompactMeshes {
			vertices := make([][]float64, len(cm.VertexXY))
			for vi, xy := range cm.VertexXY {
				x, y := toEasyEDA(xy[0], xy[1], transform)
				vertices[vi] = []float64{x, y}
			}
			triangles := make([]MeshTriangleOutput, len(cm.Triangles))
			for ti, tri := range cm.Triangles {
				triangles[ti] = MeshTriangleOutput{Vertices: tri}
			}
			var cd [][]float64
			if mi < len(ls.CurrentDensities) {
				cd = ls.CurrentDensities[mi]
			}
			var pd []float64
			if mi < len(ls.PowerDensities) {
				pd = ls.PowerDensities[mi]
			}
			var pot []float64
			if mi < len(ls.Potentials) {
				pot = ls.Potentials[mi]
			}
			lso.Meshes = append(lso.Meshes, MeshOutput{
				Vertices:         vertices,
				Triangles:        triangles,
				Potentials:       pot,
				PowerDensities:   pd,
				CurrentDensities: cd,
			})
		}
		for _, dcm := range ls.DisconnectedCompact {
			vertices := make([][]float64, len(dcm.VertexXY))
			for vi, xy := range dcm.VertexXY {
				x, y := toEasyEDA(xy[0], xy[1], transform)
				vertices[vi] = []float64{x, y}
			}
			triangles := make([]MeshTriangleOutput, len(dcm.Triangles))
			for ti, tri := range dcm.Triangles {
				triangles[ti] = MeshTriangleOutput{Vertices: tri}
			}
			lso.DisconnectedMeshes = append(lso.DisconnectedMeshes, DisconnectedMeshOutput{
				Vertices:  vertices,
				Triangles: triangles,
			})
		}
		out[i] = lso
	}
	return out
}

func serializeConnectionPoints(sol *solver.Solution, transform *[4]float64) map[string][]ConnectionPoint {
	out := make(map[string][]ConnectionPoint)
	for _, net := range sol.Problem.Networks {
		for _, conn := range net.Connections {
			name := conn.Layer.Name
			x, y := toEasyEDA(conn.Point.X, conn.Point.Y, transform)
			out[name] = append(out[name], ConnectionPoint{X: x, Y: y, IsSource: net.HasSource})
		}
	}
	return out
}

func serializeLayerBoundaries(sol *solver.Solution, transform *[4]float64) map[string][]BoundaryPolygon {
	out := make(map[string][]BoundaryPolygon)
	for i, layer := range sol.Problem.Layers {
		var polys []BoundaryPolygon
		// Use the original Gerber polygon structure for the copper-fill stencil.
		// It already has holes grouped with their exterior, so the stencil fill
		// matches the PCB canvas exactly. The solved mesh is drawn on top.
		for _, poly := range sol.Problem.Layers[i].Shape {
			exterior := toPointSlice(poly[0], transform)
			var holes [][][]float64
			for hi := 1; hi < len(poly); hi++ {
				holes = append(holes, toPointSlice(poly[hi], transform))
			}
			polys = append(polys, BoundaryPolygon{Exterior: exterior, Holes: holes})
		}
		out[layer.Name] = polys
	}
	return out
}

func toPointSlice(pts interface{}, transform *[4]float64) [][]float64 {
	switch v := pts.(type) {
	case []geometry.Point:
		out := make([][]float64, len(v))
		for i, p := range v {
			x, y := toEasyEDA(p.X, p.Y, transform)
			out[i] = []float64{x, y}
		}
		return out
	case [][]float64:
		out := make([][]float64, len(v))
		for i, p := range v {
			x, y := toEasyEDA(p[0], p[1], transform)
			out[i] = []float64{x, y}
		}
		return out
	default:
		return nil
	}
}

func toEasyEDA(x, y float64, transform *[4]float64) (float64, float64) {
	if transform == nil {
		return x, y
	}
	sx, sy, ox, oy := transform[0], transform[1], transform[2], transform[3]
	return (x - ox) / sx, (y - oy) / sy
}

func checkCurrentCapacities(sol *solver.Solution, cfg pipeline.Config) []CurrentCheckOutput {
	var warnings []CurrentCheckOutput

	networkCurrents := make(map[string]float64)
	for _, load := range cfg.Loads {
		if load.Net != "" && load.Current > 0 {
			networkCurrents[load.Net] += load.Current
		}
	}
	if len(networkCurrents) == 0 {
		return nil
	}

	layerDict := make(map[string]*problem.Layer)
	for _, layer := range sol.Problem.Layers {
		layerDict[layer.Name] = layer
	}

	layerNameToID := make(map[string]int)
	for _, lc := range cfg.Layers {
		layerNameToID[lc.Name] = lc.LayerID
	}

	// network -> layer -> min width
	networkMinWidths := make(map[string]map[string]float64)
	for _, track := range cfg.Tracks {
		if track.Net == "" || networkCurrents[track.Net] == 0 {
			continue
		}
		layerName := ""
		for lname, lid := range layerNameToID {
			if lid == track.Layer {
				layerName = lname
				break
			}
		}
		if layerName == "" || layerDict[layerName] == nil {
			continue
		}
		if track.Width <= 0 {
			continue
		}
		if networkMinWidths[track.Net] == nil {
			networkMinWidths[track.Net] = make(map[string]float64)
		}
		if w, ok := networkMinWidths[track.Net][layerName]; !ok || track.Width < w {
			networkMinWidths[track.Net][layerName] = track.Width
		}
	}

	for _, layer := range sol.Problem.Layers {
		cuMm := cfg.LayerCuThickness[layer.Name]
		if cuMm == 0 {
			cuMm = 0.035
		}
		cuOz := cuMm / ozToMm
		isOuter := containsLower(layer.Name, "top") || containsLower(layer.Name, "bottom")
		layerType := "inner"
		if isOuter {
			layerType = "outer"
		}

		layerMinWidth := estimateMinTraceWidth(layer)
		for _, widths := range networkMinWidths {
			if w, ok := widths[layer.Name]; ok {
				if layerMinWidth == 0 || w < layerMinWidth {
					layerMinWidth = w
				}
			}
		}

		for netName, current := range networkCurrents {
			traceWidth := layerMinWidth
			if widths, ok := networkMinWidths[netName]; ok {
				if w, ok2 := widths[layer.Name]; ok2 {
					traceWidth = w
				}
			}
			if traceWidth <= 0 {
				traceWidth = 0.2
			}
			maxCurrent := widthToCurrent(traceWidth*mmToMil, cuOz, cfg.TempRise, layerType)
			utilization := current / maxCurrent
			isExceeded := utilization > 1.0
			if !isExceeded && utilization <= 0.8 {
				continue
			}
			msg := ""
			if isExceeded {
				msg = fmt.Sprintf("[%s@%s] 电流超限！ 配置电流: %.2fA, 最大允许: %.2fA (走线宽: %.2fmm, 铜厚: %.2foz, 温升: %.0f°C, 层级: %s)",
					netName, layer.Name, current, maxCurrent, traceWidth, cuOz, cfg.TempRise, layerType)
			} else {
				msg = fmt.Sprintf("[%s@%s] 电流接近上限: %.1f%% (%.2fA / %.2fA)",
					netName, layer.Name, utilization*100, current, maxCurrent)
			}
			warnings = append(warnings, CurrentCheckOutput{
				NetworkName:       netName,
				LayerName:         layer.Name,
				CalculatedCurrent: current,
				MaxAllowedCurrent: maxCurrent,
				Utilization:       utilization,
				IsExceeded:        isExceeded,
				TraceWidthMm:      traceWidth,
				CopperOz:          cuOz,
				TempRise:          cfg.TempRise,
				Message:           msg,
			})
		}
	}

	return warnings
}

func estimateMinTraceWidth(layer *problem.Layer) float64 {
	minWidth := math.Inf(1)
	for _, poly := range layer.Shape {
		for _, ring := range poly {
			// Simple estimate: sample grid and find distance to boundary
			box := ringBounds(ring)
			dx := box.MaxX - box.MinX
			dy := box.MaxY - box.MinY
			if dx <= 0 || dy <= 0 {
				continue
			}
			n := 20
			stepX := dx / float64(n)
			stepY := dy / float64(n)
			for ix := 0; ix <= n; ix++ {
				for iy := 0; iy <= n; iy++ {
					p := geometry.Point{X: box.MinX + float64(ix)*stepX, Y: box.MinY + float64(iy)*stepY}
					if pointInRingMesh(p, ring) {
						d := distanceToRing(p, ring)
						if d > 0 && d < minWidth {
							minWidth = d
						}
					}
				}
			}
		}
	}
	if math.IsInf(minWidth, 1) || minWidth <= 0 {
		return 0.2
	}
	w := 2 * minWidth
	if w < 0.05 {
		w = 0.05
	}
	if w > 10 {
		w = 10
	}
	return w
}

func ringBounds(ring geometry.Ring) geometry.Box {
	if len(ring) == 0 {
		return geometry.Box{}
	}
	b := geometry.Box{MinX: ring[0].X, MinY: ring[0].Y, MaxX: ring[0].X, MaxY: ring[0].Y}
	for _, p := range ring {
		if p.X < b.MinX {
			b.MinX = p.X
		}
		if p.Y < b.MinY {
			b.MinY = p.Y
		}
		if p.X > b.MaxX {
			b.MaxX = p.X
		}
		if p.Y > b.MaxY {
			b.MaxY = p.Y
		}
	}
	return b
}

func distanceToRing(p geometry.Point, ring geometry.Ring) float64 {
	minDist := math.Inf(1)
	for i := 0; i < len(ring); i++ {
		a := ring[i]
		b := ring[(i+1)%len(ring)]
		d := distanceToSegment(p, a, b)
		if d < minDist {
			minDist = d
		}
	}
	return minDist
}

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

func containsLower(s, substr string) bool {
	return contains(stringsLower(s), stringsLower(substr))
}

func stringsLower(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			b.WriteRune(r + ('a' - 'A'))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
