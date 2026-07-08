// Package solver implements the FEM PDN solver.
package solver

import (
	"fmt"
	"math"

	"github.com/easyeda/eext-paden-integration/go-service/internal/geometry"
	"github.com/easyeda/eext-paden-integration/go-service/internal/mesh"
	"github.com/easyeda/eext-paden-integration/go-service/internal/problem"
)

// SolverInfo holds diagnostic information.
type SolverInfo struct {
	GroundNodeCurrent float64
	ResidualNorm      float64
}

// LayerSolution holds solution data for one layer.
type LayerSolution struct {
	CompactMeshes       []*mesh.CompactMesh
	Potentials          [][]float64
	PowerDensities      [][]float64
	CurrentDensities    [][][]float64
	DisconnectedCompact []*mesh.CompactMesh
}

// Solution is the complete FEM solution.
type Solution struct {
	Problem            *problem.Problem
	LayerSolutions     []*LayerSolution
	SolverInfo         SolverInfo
	OriginalGeometries []geometry.MultiPolygon
	UserData           interface{}
}

// Solve solves the PDN problem.
func Solve(prob *problem.Problem) (*Solution, error) {
	if len(prob.Layers) == 0 {
		return nil, fmt.Errorf("no layers")
	}
	if len(prob.Networks) == 0 {
		return nil, fmt.Errorf("no networks")
	}

	// Save original geometries
	original := make([]geometry.MultiPolygon, len(prob.Layers))
	for i, layer := range prob.Layers {
		original[i] = append(geometry.MultiPolygon{}, layer.Shape...)
	}

	// Build layer geom indices
	layerGeoms := make([][]geometry.Polygon, len(prob.Layers))
	for i, layer := range prob.Layers {
		layerGeoms[i] = append([]geometry.Polygon{}, layer.Shape...)
	}

	// Find connected layer-geom pairs
	connected := findConnectedPairs(prob, layerGeoms)
	if len(connected) == 0 {
		return nil, fmt.Errorf("no connected copper regions")
	}

	// Generate meshes
	meshes, meshToLayer := generateMeshes(prob, layerGeoms, connected)
	if len(meshes) == 0 {
		return nil, fmt.Errorf("mesh generation failed")
	}

	// Disconnected meshes
	disconnected := generateDisconnectedMeshes(prob, layerGeoms, connected)

	// Build vertex indexer
	vindex := buildVertexIndexer(meshes)

	// Guard against meshes that are too large for WASM memory.
	totalMeshVerts := 0
	for _, m := range meshes {
		totalMeshVerts += len(m.Vertices)
	}
	const maxMeshVerts = 500000
	fmt.Printf("[PADEN solver] total mesh vertices: %d (limit %d)\n", totalMeshVerts, maxMeshVerts)
	if totalMeshVerts > maxMeshVerts {
		return nil, fmt.Errorf("mesh too large: %d vertices (limit %d); reduce board complexity or increase element size", totalMeshVerts, maxMeshVerts)
	}

	// Filter dead networks
	filteredNetworks := filterDeadNetworks(prob, layerGeoms, connected)
	if len(filteredNetworks) == 0 {
		return nil, fmt.Errorf("all networks have dead terminals")
	}

	// Build node indexer
	nodeIndexer := buildNodeIndexer(prob, meshes, meshToLayer, vindex, filteredNetworks)

	// System size
	N := len(vindex.globalToLocal) + nodeIndexer.internalCount + len(nodeIndexer.extraVars) + 1
	fmt.Printf("[PADEN solver] system size: %d\n", N)

	// Stamp Laplacian
	triplets := stampLaplacian(meshes, meshToLayer, prob, vindex)

	// Stamp networks
	rhs := make([]float64, N)
	for _, net := range filteredNetworks {
		stampNetwork(net, nodeIndexer, triplets, rhs)
	}

	// Ground node
	iGnd := findBestGroundNode(prob, nodeIndexer)
	groundVar := N - 1
	triplets = append(triplets, Triplet{Row: groundVar, Col: iGnd, Val: 1.0})
	triplets = append(triplets, Triplet{Row: iGnd, Col: groundVar, Val: 1.0})
	rhs[groundVar] = 0.0

	// Build matrix and regularize. Regularize every diagonal entry that is
	// (near) zero, including internal nodes and voltage-source extra variables.
	A := NewCSRFromTriplets(N, triplets)
	diag := A.Diagonal()
	reg := make([]float64, N)
	for i := 0; i < N; i++ {
		if math.Abs(diag[i]) < 1e-12 {
			reg[i] = 1e-6
		} else {
			reg[i] = 1e-9
		}
	}
	A = A.AddDiagonal(reg)

	// Solve. The MNA matrix is symmetric indefinite (Laplacian + voltage-source
	// constraints). MINRES uses short Lanczos recurrences and O(n) memory, so it
	// is far cheaper in WASM than restarted GMRES for systems of this size.
	fmt.Printf("[PADEN solver] starting MINRES for N=%d\n", N)
	v, err := SolveMINRES(A, rhs, N, 1e-9)
	if err != nil {
		return nil, fmt.Errorf("solver failed: %w", err)
	}

	groundCurrent := v[groundVar]
	resNorm := ResidualNorm(A, v, rhs)

	// Produce layer solutions
	layerSols := produceLayerSolutions(prob, vindex, meshes, meshToLayer, v, disconnected)

	return &Solution{
		Problem:            prob,
		LayerSolutions:     layerSols,
		SolverInfo:         SolverInfo{GroundNodeCurrent: groundCurrent, ResidualNorm: resNorm},
		OriginalGeometries: original,
	}, nil
}

type vertexIndexer struct {
	globalToLocal []struct{ meshIdx, vertIdx int }
	localToGlobal map[[2]int]int
}

func buildVertexIndexer(meshes []*mesh.Mesh) *vertexIndexer {
	vi := &vertexIndexer{
		localToGlobal: make(map[[2]int]int),
	}
	for mi, m := range meshes {
		for viLocal, v := range m.Vertices {
			gi := len(vi.globalToLocal)
			vi.globalToLocal = append(vi.globalToLocal, struct{ meshIdx, vertIdx int }{mi, viLocal})
			vi.localToGlobal[[2]int{mi, viLocal}] = gi
			_ = v
		}
	}
	return vi
}

type nodeIndexer struct {
	nodeToGlobal  map[*problem.NodeID]int
	extraVars     map[problem.LumpedElement]int
	internalCount int
}

func buildNodeIndexer(prob *problem.Problem, meshes []*mesh.Mesh, meshToLayer []int, vindex *vertexIndexer, networks []*problem.Network) *nodeIndexer {
	ni := &nodeIndexer{
		nodeToGlobal: make(map[*problem.NodeID]int),
		extraVars:    make(map[problem.LumpedElement]int),
	}

	// Build per-layer kdtree-like nearest vertex lookup using simple linear search
	type layerVert struct {
		globalIdx int
		p         mesh.Point
	}
	layerVerts := make(map[int][]layerVert)
	for mi, m := range meshes {
		li := meshToLayer[mi]
		for vii, v := range m.Vertices {
			gi := vindex.localToGlobal[[2]int{mi, vii}]
			layerVerts[li] = append(layerVerts[li], layerVert{globalIdx: gi, p: v.P})
		}
	}

	// Index connection nodes
	for _, net := range networks {
		for _, conn := range net.Connections {
			li := -1
			for i, layer := range prob.Layers {
				if layer == conn.Layer {
					li = i
					break
				}
			}
			if li < 0 {
				continue
			}
			verts := layerVerts[li]
			if len(verts) == 0 {
				continue
			}
			best := verts[0]
			bestDist := math.Hypot(best.p.X-conn.Point.X, best.p.Y-conn.Point.Y)
			for _, lv := range verts[1:] {
				d := math.Hypot(lv.p.X-conn.Point.X, lv.p.Y-conn.Point.Y)
				if d < bestDist {
					bestDist = d
					best = lv
				}
			}
			ni.nodeToGlobal[conn.NodeID] = best.globalIdx
		}
	}

	// Internal nodes
	iAt := len(vindex.globalToLocal)
	for _, net := range networks {
		for node := range net.Nodes {
			if _, ok := ni.nodeToGlobal[node]; !ok {
				ni.nodeToGlobal[node] = iAt
				iAt++
				ni.internalCount++
			}
		}
	}

	// Extra variables for voltage sources
	for _, net := range networks {
		for _, elem := range net.Elements {
			for i := 0; i < elem.ExtraVariableCount(); i++ {
				ni.extraVars[elem] = iAt
				iAt++
			}
		}
	}

	return ni
}

func findConnectedPairs(prob *problem.Problem, layerGeoms [][]geometry.Polygon) map[[2]int]bool {
	connected := make(map[[2]int]bool)

	// Mark polygons hit by connections from networks with sources
	for _, net := range prob.Networks {
		for _, conn := range net.Connections {
			li := -1
			for i, layer := range prob.Layers {
				if layer == conn.Layer {
					li = i
					break
				}
			}
			if li < 0 {
				continue
			}
			for gi, geom := range layerGeoms[li] {
				if pointHitsGeom(conn.Point, geom) {
					connected[[2]int{li, gi}] = true
				}
			}
		}
	}

	// Build adjacency: two geoms on same layer are adjacent if their bounding boxes overlap
	for li, geoms := range layerGeoms {
		for i := 0; i < len(geoms); i++ {
			for j := i + 1; j < len(geoms); j++ {
				b1 := geoms[i].Bounds()
				b2 := geoms[j].Bounds()
				if boxesOverlap(b1, b2) {
					// Mark both connected if either is connected
					if connected[[2]int{li, i}] {
						connected[[2]int{li, j}] = true
					}
					if connected[[2]int{li, j}] {
						connected[[2]int{li, i}] = true
					}
				}
			}
		}
	}

	return connected
}

func pointHitsGeom(p geometry.Point, geom geometry.Polygon) bool {
	if pointInPolygonMesh(p, geom) {
		return true
	}
	return distanceToPolygon(p, geom) <= 0.05
}

func boxesOverlap(a, b geometry.Box) bool {
	return a.MinX <= b.MaxX && a.MaxX >= b.MinX && a.MinY <= b.MaxY && a.MaxY >= b.MinY
}

func generateMeshes(prob *problem.Problem, layerGeoms [][]geometry.Polygon, connected map[[2]int]bool) ([]*mesh.Mesh, []int) {
	cfg := mesh.DefaultConfig()
	// Adjust max size based on total copper area to keep vertex count under control.
	totalArea := 0.0
	for _, geoms := range layerGeoms {
		for _, g := range geoms {
			totalArea += polygonArea(g)
		}
	}
	if totalArea < 30000 {
		scale := math.Min(math.Sqrt(30000/math.Max(totalArea, 100)), 4.0)
		cfg.MaximumSize = math.Min(cfg.MaximumSize*scale, 2.0)
	} else {
		// Large boards: coarsen mesh so memory does not explode.
		scale := math.Min(math.Sqrt(totalArea/30000), 4.0)
		cfg.MaximumSize = math.Max(math.Min(cfg.MaximumSize*scale, 5.0), 1.5)
		cfg.MinimumAngle = 25.0
	}
	fmt.Printf("[PADEN solver] total copper area=%.1f mm^2, max mesh size=%.3f mm\n", totalArea, cfg.MaximumSize)
	mesher := mesh.NewMesher(cfg)

	var meshes []*mesh.Mesh
	var meshToLayer []int

	for li, layer := range prob.Layers {
		seedPoints := collectSeedPoints(prob, layer)
		for gi, geom := range layerGeoms[li] {
			if !connected[[2]int{li, gi}] {
				continue
			}
			m, err := mesher.PolygonToMesh(geom, seedPoints)
			if err != nil || len(m.Vertices) == 0 {
				// Fallback to earcut
				m, err = mesh.EarcutFallback(geom)
				if err != nil || len(m.Vertices) == 0 {
					continue
				}
			}
			meshes = append(meshes, m)
			meshToLayer = append(meshToLayer, li)
		}
	}
	return meshes, meshToLayer
}

func collectSeedPoints(prob *problem.Problem, layer *problem.Layer) []mesh.Point {
	var seeds []mesh.Point
	for _, net := range prob.Networks {
		for _, conn := range net.Connections {
			if conn.Layer == layer {
				seeds = append(seeds, mesh.Point{X: conn.Point.X, Y: conn.Point.Y})
			}
		}
	}
	return seeds
}

func generateDisconnectedMeshes(prob *problem.Problem, layerGeoms [][]geometry.Polygon, connected map[[2]int]bool) [][]*mesh.CompactMesh {
	result := make([][]*mesh.CompactMesh, len(prob.Layers))
	relaxed := mesh.NewMesher(mesh.Config{MaximumSize: 0, MinimumAngle: 5.0})
	for li, geoms := range layerGeoms {
		for gi, geom := range geoms {
			if connected[[2]int{li, gi}] {
				continue
			}
			m, err := relaxed.PolygonToMesh(geom, nil)
			if err != nil || len(m.Vertices) == 0 {
				m, err = mesh.EarcutFallback(geom)
				if err != nil || len(m.Vertices) == 0 {
					continue
				}
			}
			result[li] = append(result[li], m.ToCompact())
		}
	}
	return result
}

func filterDeadNetworks(prob *problem.Problem, layerGeoms [][]geometry.Polygon, connected map[[2]int]bool) []*problem.Network {
	var filtered []*problem.Network
	for _, net := range prob.Networks {
		alive := false
		for _, conn := range net.Connections {
			li := -1
			for i, layer := range prob.Layers {
				if layer == conn.Layer {
					li = i
					break
				}
			}
			if li < 0 {
				continue
			}
			for gi, geom := range layerGeoms[li] {
				if !connected[[2]int{li, gi}] {
					continue
				}
				if pointHitsGeom(conn.Point, geom) {
					alive = true
					break
				}
			}
			if alive {
				break
			}
		}
		if alive {
			filtered = append(filtered, net)
		}
	}
	return filtered
}

func stampLaplacian(meshes []*mesh.Mesh, meshToLayer []int, prob *problem.Problem, vindex *vertexIndexer) []Triplet {
	// Rough estimate: ~3 off-diagonal + 1 diagonal triplet per vertex.
	totalVerts := 0
	for _, m := range meshes {
		totalVerts += len(m.Vertices)
	}
	triplets := make([]Triplet, 0, totalVerts*4)

	for mi, m := range meshes {
		conductance := prob.Layers[meshToLayer[mi]].Conductance
		N := len(m.Vertices)
		diag := make([]float64, N)
		// Iterate half-edges directly to avoid per-vertex Orbit allocations.
		for _, edge := range m.Edges {
			if edge.Twin == nil {
				continue
			}
			ratio := edge.Cotan()
			if ratio == 0 {
				continue
			}
			vi := edge.Origin.Idx
			kj := edge.Twin.Origin.Idx
			gi := vindex.localToGlobal[[2]int{mi, vi}]
			gj := vindex.localToGlobal[[2]int{mi, kj}]
			triplets = append(triplets, Triplet{Row: gi, Col: gj, Val: conductance * ratio})
			diag[vi] -= conductance * ratio
		}
		for vi, d := range diag {
			gi := vindex.localToGlobal[[2]int{mi, vi}]
			triplets = append(triplets, Triplet{Row: gi, Col: gi, Val: d})
		}
	}
	return triplets
}

func stampNetwork(net *problem.Network, ni *nodeIndexer, triplets []Triplet, rhs []float64) []Triplet {
	for _, elem := range net.Elements {
		switch e := elem.(type) {
		case *problem.Resistor:
			ia := ni.nodeToGlobal[e.A]
			ib := ni.nodeToGlobal[e.B]
			g := 1.0 / e.Resistance
			triplets = append(triplets,
				Triplet{Row: ia, Col: ia, Val: -g},
				Triplet{Row: ia, Col: ib, Val: g},
				Triplet{Row: ib, Col: ib, Val: -g},
				Triplet{Row: ib, Col: ia, Val: g},
			)
		case *problem.CurrentSource:
			iF := ni.nodeToGlobal[e.F]
			iT := ni.nodeToGlobal[e.T]
			rhs[iF] += e.Current
			rhs[iT] -= e.Current
		case *problem.VoltageSource:
			ip := ni.nodeToGlobal[e.P]
			in := ni.nodeToGlobal[e.N]
			iv := ni.extraVars[e]
			if ip == in {
				triplets = append(triplets, Triplet{Row: iv, Col: iv, Val: 1.0})
				rhs[iv] = 0
				continue
			}
			triplets = append(triplets,
				Triplet{Row: iv, Col: ip, Val: 1.0},
				Triplet{Row: iv, Col: in, Val: -1.0},
				Triplet{Row: ip, Col: iv, Val: 1.0},
				Triplet{Row: in, Col: iv, Val: -1.0},
			)
			rhs[iv] = e.Voltage
		case *problem.VoltageRegulator:
			ivp := ni.nodeToGlobal[e.VP]
			ivn := ni.nodeToGlobal[e.VN]
			isf := ni.nodeToGlobal[e.SF]
			ist := ni.nodeToGlobal[e.ST]
			iv := ni.extraVars[e]
			triplets = append(triplets,
				Triplet{Row: iv, Col: ivp, Val: 1.0},
				Triplet{Row: iv, Col: ivn, Val: -1.0},
				Triplet{Row: ivp, Col: iv, Val: 1.0},
				Triplet{Row: ivn, Col: iv, Val: -1.0},
				Triplet{Row: iv, Col: isf, Val: e.Gain},
				Triplet{Row: iv, Col: ist, Val: -e.Gain},
			)
			rhs[iv] += e.Voltage
		}
	}
	return triplets
}

func findBestGroundNode(prob *problem.Problem, ni *nodeIndexer) int {
	maxVoltage := math.Inf(-1)
	groundIdx := 0
	for _, net := range prob.Networks {
		for _, elem := range net.Elements {
			if vs, ok := elem.(*problem.VoltageSource); ok {
				if vs.Voltage > maxVoltage {
					maxVoltage = vs.Voltage
					groundIdx = ni.nodeToGlobal[vs.N]
				}
			}
		}
	}
	return groundIdx
}

func produceLayerSolutions(prob *problem.Problem, vindex *vertexIndexer, meshes []*mesh.Mesh, meshToLayer []int, v []float64, disconnected [][]*mesh.CompactMesh) []*LayerSolution {
	layerSols := make([]*LayerSolution, len(prob.Layers))
	for li := range prob.Layers {
		layerSols[li] = &LayerSolution{}
	}

	for mi, m := range meshes {
		li := meshToLayer[mi]
		cm := m.ToCompact()
		N := len(cm.VertexXY)
		potentials := make([]float64, N)
		for vi := 0; vi < N; vi++ {
			gi := vindex.localToGlobal[[2]int{mi, vi}]
			potentials[vi] = v[gi]
		}
		pd, cd := computePowerCurrent(cm.VertexXY, cm.Triangles, potentials, prob.Layers[li].Conductance)
		layerSols[li].CompactMeshes = append(layerSols[li].CompactMeshes, cm)
		layerSols[li].Potentials = append(layerSols[li].Potentials, potentials)
		layerSols[li].PowerDensities = append(layerSols[li].PowerDensities, pd)
		layerSols[li].CurrentDensities = append(layerSols[li].CurrentDensities, cd)
	}

	for li, dms := range disconnected {
		layerSols[li].DisconnectedCompact = dms
	}

	return layerSols
}

func computePowerCurrent(vertexXY [][2]float64, triangles [][3]int, potentials []float64, conductance float64) ([]float64, [][]float64) {
	if len(triangles) == 0 {
		return nil, nil
	}
	pd := make([]float64, len(triangles))
	cd := make([][]float64, len(triangles))
	for i, tri := range triangles {
		v0 := vertexXY[tri[0]]
		v1 := vertexXY[tri[1]]
		v2 := vertexXY[tri[2]]
		p0 := potentials[tri[0]]
		p1 := potentials[tri[1]]
		p2 := potentials[tri[2]]
		x0, y0 := v0[0], v0[1]
		x1, y1 := v1[0], v1[1]
		x2, y2 := v2[0], v2[1]
		D := (y1-y2)*(x0-x2) + (x2-x1)*(y0-y2)
		if math.Abs(D) < 1e-15 {
			D = 1e-15
		}
		dVdx := ((y1-y2)*p0 + (y2-y0)*p1 + (y0-y1)*p2) / D
		dVdy := ((x2-x1)*p0 + (x0-x2)*p1 + (x1-x0)*p2) / D
		pd[i] = conductance * (dVdx*dVdx + dVdy*dVdy)
		cd[i] = []float64{-dVdx * conductance, -dVdy * conductance}
	}
	return pd, cd
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
	return area
}

func pointInPolygonMesh(p geometry.Point, poly geometry.Polygon) bool {
	if len(poly) == 0 {
		return false
	}
	if !pointInRingMesh(p, poly[0]) {
		return false
	}
	for i := 1; i < len(poly); i++ {
		if pointInRingMesh(p, poly[i]) {
			return false
		}
	}
	return true
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

func distanceToPolygon(p geometry.Point, poly geometry.Polygon) float64 {
	minDist := math.Inf(1)
	for _, ring := range poly {
		for i := 0; i < len(ring); i++ {
			a := ring[i]
			b := ring[(i+1)%len(ring)]
			d := distanceToSegment(p, a, b)
			if d < minDist {
				minDist = d
			}
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
