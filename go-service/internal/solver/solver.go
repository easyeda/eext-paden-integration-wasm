// Package solver implements the FEM PDN solver.
package solver

import (
	"fmt"
	"math"
	"strings"
	"time"

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

	// Build layer geom indices (do not deep-copy layer shapes to save memory;
	// the pipeline no longer mutates shapes after this point).
	layerGeoms := make([][]geometry.Polygon, len(prob.Layers))
	for i, layer := range prob.Layers {
		layerGeoms[i] = layer.Shape
	}

	// Find connected layer-geom pairs
	connected := findConnectedPairs(prob, layerGeoms)
	if len(connected) == 0 {
		return nil, fmt.Errorf("no connected copper regions")
	}

	// Generate meshes with iterative coarsening until the vertex budget is met.
	totalArea := totalCopperArea(layerGeoms)
	cfg := initialMeshConfig(totalArea)
	const maxMeshVerts = 15000
	var meshes []*mesh.Mesh
	var meshToLayer []int
	totalMeshVerts := 0
	for attempt := 0; attempt < 5; attempt++ {
		meshes, meshToLayer = generateMeshes(prob, layerGeoms, connected, cfg)
		totalMeshVerts = 0
		for _, m := range meshes {
			totalMeshVerts += len(m.Vertices)
		}
		fmt.Printf("[PADEN solver] total mesh vertices: %d (limit %d) attempt=%d\n", totalMeshVerts, maxMeshVerts, attempt)
		if totalMeshVerts <= maxMeshVerts || totalMeshVerts == 0 {
			break
		}
		ratio := math.Sqrt(float64(totalMeshVerts) / float64(maxMeshVerts))
		cfg.MaximumSize *= math.Min(ratio*1.2, 2.0)
		cfg.MaximumSize = math.Min(cfg.MaximumSize, 12.0)
		cfg.MinimumAngle = math.Max(cfg.MinimumAngle-3.0, 10.0)
		fmt.Printf("[PADEN solver] mesh too large, coarsening: maxSize=%.3f minAngle=%.1f\n", cfg.MaximumSize, cfg.MinimumAngle)
	}
	if len(meshes) == 0 {
		return nil, fmt.Errorf("mesh generation failed")
	}
	if totalMeshVerts > maxMeshVerts {
		return nil, fmt.Errorf("mesh too large: %d vertices (limit %d); reduce board complexity or increase element size", totalMeshVerts, maxMeshVerts)
	}

	// Disconnected meshes: render-only, keep a tight memory budget.
	disconnected := generateDisconnectedMeshes(prob, layerGeoms, connected, 20000)

	// Build vertex indexer
	vindex := buildVertexIndexer(meshes)

	// Log raw network info for debugging zero-voltage issues.
	logNetworkInfo("before filtering", prob.Networks)

	// Filter dead networks
	filteredNetworks := filterDeadNetworks(prob, layerGeoms, connected)
	if len(filteredNetworks) == 0 {
		return nil, fmt.Errorf("all networks have dead terminals")
	}
	logNetworkInfo("after filtering", filteredNetworks)

	// Build node indexer
	nodeIndexer := buildNodeIndexer(prob, meshes, meshToLayer, vindex, filteredNetworks)

	// Merge nodes that are shorted by ideal 0 V voltage sources.
	originalN := len(vindex.globalToLocal) + nodeIndexer.internalCount
	uf := newUnionFind(originalN)
	for _, net := range filteredNetworks {
		for _, elem := range net.Elements {
			vs, ok := elem.(*problem.VoltageSource)
			if !ok || math.Abs(vs.Voltage) > 1e-12 {
				continue
			}
			ip, okP := nodeIndexer.nodeToGlobal[vs.P]
			in, okN := nodeIndexer.nodeToGlobal[vs.N]
			if okP && okN && ip != in {
				uf.union(ip, in)
			}
		}
	}

	newIndex := make([]int, originalN)
	for i := range newIndex {
		newIndex[i] = -1
	}
	nextNew := 0
	for i := 0; i < originalN; i++ {
		r := uf.find(i)
		if newIndex[r] < 0 {
			newIndex[r] = nextNew
			nextNew++
		}
	}
	M := nextNew
	for i := 0; i < originalN; i++ {
		newIndex[i] = newIndex[uf.find(i)]
	}

	// Remap node IDs to the reduced variable space.
	for node, orig := range nodeIndexer.nodeToGlobal {
		nodeIndexer.nodeToGlobal[node] = newIndex[orig]
	}
	globalToNew := make([]int, len(vindex.globalToLocal))
	for i := range globalToNew {
		globalToNew[i] = newIndex[i]
	}

	// Stamp FEM Laplacian, via resistors, and current sources in reduced space.
	triplets := stampLaplacian(meshes, meshToLayer, prob, vindex, globalToNew)
	rhs := make([]float64, M)
	for _, net := range filteredNetworks {
		stampResistors(net, nodeIndexer, triplets)
		stampCurrentSources(net, nodeIndexer, rhs)
	}
	A := NewCSRFromTriplets(M, triplets)

	// Identify Dirichlet (fixed-potential) nodes: the ground reference and the
	// positive terminals of all non-zero voltage sources.  Voltage-source
	// negative terminals are forced to the ground potential.
	known := make(map[int]float64)
	iGnd := findBestGroundNode(prob, nodeIndexer)
	if iGnd >= 0 {
		known[iGnd] = 0
	}
	for _, net := range filteredNetworks {
		for _, elem := range net.Elements {
			vs, ok := elem.(*problem.VoltageSource)
			if !ok || math.Abs(vs.Voltage) < 1e-12 {
				continue
			}
			ip, okP := nodeIndexer.nodeToGlobal[vs.P]
			in, okN := nodeIndexer.nodeToGlobal[vs.N]
			if !okP || !okN {
				continue
			}
			if _, ok := known[in]; !ok {
				known[in] = 0
			}
			known[ip] = known[in] + vs.Voltage
		}
	}
	fmt.Printf("[PADEN solver] reduced system M=%d, known nodes=%d\n", M, len(known))

	// Every connected component of the conductance graph must have at least one
	// Dirichlet node, otherwise the Laplacian block for that component is singular.
	ensureComponentGrounding(A, known)

	// Apply Dirichlet boundary conditions symmetrically: keep the full MxM
	// matrix but zero out known rows/columns and set known diagonal to 1.
	Abc, rhsBc := applyDirichletSym(A, rhs, known)

	// Mild regularization to keep any isolated/floating unknowns well behaved.
	d := Abc.Diagonal()
	reg := make([]float64, M)
	for i := 0; i < M; i++ {
		if math.Abs(d[i]) < 1e-12 {
			reg[i] = 1e-9
		} else {
			reg[i] = 1e-12
		}
	}
	Areg := Abc.AddDiagonal(reg)

	maxIter := M
	if maxIter > 5000 {
		maxIter = 5000
	}
	tol := 1e-9
	fmt.Printf("[PADEN solver] starting CG for M=%d, maxIter=%d\n", M, maxIter)
	x, err := SolveCG(Areg, rhsBc, maxIter, tol, NewJacobiPreconditioner(Areg))
	if err != nil {
		return nil, fmt.Errorf("solver failed: %w", err)
	}
	v := x

	// Ground current = total current flowing into the ground node (KCL residual).
	groundCurrent := 0.0
	if iGnd >= 0 {
		y := make([]float64, M)
		A.Multiply(v, y)
		groundCurrent = rhs[iGnd] - y[iGnd]
	}
	resNorm := ResidualNorm(A, v, rhs)
	vMin, vMax := math.Inf(1), math.Inf(-1)
	for i := 0; i < M; i++ {
		if v[i] < vMin {
			vMin = v[i]
		}
		if v[i] > vMax {
			vMax = v[i]
		}
	}
	fmt.Printf("[PADEN solver] solution vrange=[%.6f,%.6f], groundCurrent=%.6e, residualNorm=%.6e\n", vMin, vMax, groundCurrent, resNorm)
	logNetworkPotentials(prob, nodeIndexer, v)

	// Produce layer solutions
	layerSols := produceLayerSolutions(prob, vindex, meshes, meshToLayer, v, disconnected, globalToNew)

	return &Solution{
		Problem:        prob,
		LayerSolutions: layerSols,
		SolverInfo:     SolverInfo{GroundNodeCurrent: groundCurrent, ResidualNorm: resNorm},
		// Intentionally nil: deep-copying all layer shapes can consume hundreds of MB
		// and the fallback path in serialization now uses prob.Layers[i].Shape.
		OriginalGeometries: nil,
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

	// Robust layer lookup by pointer (preferred) or by name (fallback).
	layerByPtr := make(map[*problem.Layer]int)
	layerByName := make(map[string]int)
	layerNames := make([]string, len(prob.Layers))
	for i, layer := range prob.Layers {
		layerByPtr[layer] = i
		layerByName[layer.Name] = i
		layerNames[i] = fmt.Sprintf("%d:%s", i, layer.Name)
	}
	fmt.Printf("[PADEN solver] layer order: %s\n", strings.Join(layerNames, ", "))
	fmt.Printf("[PADEN solver] meshToLayer: %v\n", meshToLayer)
	for li, verts := range layerVerts {
		if len(verts) > 0 {
			fmt.Printf("[PADEN solver] layer %d vertex range [%d,%d] count=%d\n", li, verts[0].globalIdx, verts[len(verts)-1].globalIdx, len(verts))
		}
	}

	// Index connection nodes
	for _, net := range networks {
		for _, conn := range net.Connections {
			li := -1
			if conn.Layer == nil {
				continue
			}
			if idx, ok := layerByPtr[conn.Layer]; ok {
				li = idx
			} else if idx, ok := layerByName[conn.Layer.Name]; ok {
				li = idx
			}
			if li < 0 {
				fmt.Printf("[PADEN solver] conn layer '%s' not in problem layers\n", conn.Layer.Name)
				continue
			}
			verts := layerVerts[li]
			if len(verts) == 0 {
				fmt.Printf("[PADEN solver] conn layer '%s' has no mesh vertices\n", conn.Layer.Name)
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
			fmt.Printf("[PADEN solver] conn %s (%.3f,%.3f) -> layerIdx=%d nearest=(%.3f,%.3f) global=%d dist=%.4f\n",
				conn.Layer.Name, conn.Point.X, conn.Point.Y, li, best.p.X, best.p.Y, best.globalIdx, bestDist)
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

	// Voltage sources are enforced as Dirichlet (fixed-potential) boundary
	// conditions rather than MNA extra variables, so no extra variables are
	// allocated here.

	return ni
}

func findConnectedPairs(prob *problem.Problem, layerGeoms [][]geometry.Polygon) map[[2]int]bool {
	connected := make(map[[2]int]bool)
	layerIdx := layerIndexMap(prob)

	// Mark polygons hit by connections from networks with sources
	for _, net := range prob.Networks {
		for _, conn := range net.Connections {
			if conn.Layer == nil {
				continue
			}
			li, ok := layerIdx[conn.Layer]
			if !ok {
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

	for li := range layerGeoms {
		connCount := 0
		for i := range layerGeoms[li] {
			if connected[[2]int{li, i}] {
				connCount++
			}
		}
		fmt.Printf("[PADEN solver] layer %d connected geoms=%d / %d\n", li, connCount, len(layerGeoms[li]))
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

func totalCopperArea(layerGeoms [][]geometry.Polygon) float64 {
	totalArea := 0.0
	for _, geoms := range layerGeoms {
		for _, g := range geoms {
			totalArea += polygonArea(g)
		}
	}
	return totalArea
}

func initialMeshConfig(totalArea float64) mesh.Config {
	cfg := mesh.DefaultConfig()
	// Adjust max size based on total copper area to keep vertex count under control.
	if totalArea < 30000 {
		// Medium boards: coarsen enough to keep the solve interactive in WASM
		// without sacrificing too much accuracy.
		scale := math.Min(math.Sqrt(30000/math.Max(totalArea, 100)), 4.0)
		cfg.MaximumSize = math.Min(cfg.MaximumSize*scale, 4.0)
	} else {
		// Large boards: coarsen mesh so memory does not explode.
		scale := math.Min(math.Sqrt(totalArea/30000), 4.0)
		cfg.MaximumSize = math.Max(math.Min(cfg.MaximumSize*scale, 5.0), 1.5)
		cfg.MinimumAngle = 25.0
	}
	return cfg
}

func generateMeshes(prob *problem.Problem, layerGeoms [][]geometry.Polygon, connected map[[2]int]bool, cfg mesh.Config) ([]*mesh.Mesh, []int) {
	fmt.Printf("[PADEN solver] total copper area=%.1f mm^2, max mesh size=%.3f mm\n", totalCopperArea(layerGeoms), cfg.MaximumSize)
	mesher := mesh.NewMesher(cfg)

	var meshes []*mesh.Mesh
	var meshToLayer []int

	for li, layer := range prob.Layers {
		seedPoints := collectSeedPoints(prob, layer)
		for gi, geom := range layerGeoms[li] {
			if !connected[[2]int{li, gi}] {
				continue
			}
			geom.EnsureOrientation()
			t0 := time.Now()
			m, err := mesher.PolygonToMesh(geom, seedPoints)
			dt := time.Since(t0)
			if err != nil || len(m.Vertices) == 0 {
				t0 = time.Now()
				m, err = mesh.EarcutFallback(geom)
				dt = time.Since(t0)
				if err != nil || len(m.Vertices) == 0 {
					continue
				}
			}
			fmt.Printf("[PADEN solver] layer %d geom %d meshed in %v -> %d vertices\n", li, gi, dt, len(m.Vertices))
			meshes = append(meshes, m)
			meshToLayer = append(meshToLayer, li)
		}
	}

	layerMeshStats := make(map[int][2]int)
	for i, m := range meshes {
		li := meshToLayer[i]
		s := layerMeshStats[li]
		s[0]++
		s[1] += len(m.Vertices)
		layerMeshStats[li] = s
	}
	for li := range prob.Layers {
		s := layerMeshStats[li]
		fmt.Printf("[PADEN solver] layer %d meshes=%d verts=%d\n", li, s[0], s[1])
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

func generateDisconnectedMeshes(prob *problem.Problem, layerGeoms [][]geometry.Polygon, connected map[[2]int]bool, maxVerts int) [][]*mesh.CompactMesh {
	result := make([][]*mesh.CompactMesh, len(prob.Layers))
	// Use a coarse mesher for disconnected regions; they are only for display.
	relaxed := mesh.NewMesher(mesh.Config{MaximumSize: 4.0, MinimumAngle: 0})
	totalVerts := 0
	for li, geoms := range layerGeoms {
		for gi, geom := range geoms {
			if connected[[2]int{li, gi}] {
				continue
			}
			if totalVerts >= maxVerts {
				fmt.Printf("[PADEN solver] disconnected mesh budget reached (%d), skipping remaining\n", maxVerts)
				return result
			}
			geom.EnsureOrientation()
			m, err := relaxed.PolygonToMesh(geom, nil)
			if err != nil || len(m.Vertices) == 0 {
				m, err = mesh.EarcutFallback(geom)
				if err != nil || len(m.Vertices) == 0 {
					continue
				}
			}
			totalVerts += len(m.Vertices)
			result[li] = append(result[li], m.ToCompact())
		}
	}
	fmt.Printf("[PADEN solver] disconnected meshes total vertices=%d\n", totalVerts)
	return result
}

func filterDeadNetworks(prob *problem.Problem, layerGeoms [][]geometry.Polygon, connected map[[2]int]bool) []*problem.Network {
	var filtered []*problem.Network
	layerIdx := layerIndexMap(prob)
	for _, net := range prob.Networks {
		alive := false
		for _, conn := range net.Connections {
			if conn.Layer == nil {
				continue
			}
			li, ok := layerIdx[conn.Layer]
			if !ok {
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

func layerIndexMap(prob *problem.Problem) map[*problem.Layer]int {
	m := make(map[*problem.Layer]int, len(prob.Layers))
	for i, layer := range prob.Layers {
		m[layer] = i
	}
	return m
}

func stampLaplacian(meshes []*mesh.Mesh, meshToLayer []int, prob *problem.Problem, vindex *vertexIndexer, globalToNew []int) []Triplet {
	totalTris := 0
	totalVerts := 0
	for _, m := range meshes {
		totalTris += len(m.Triangles)
		totalVerts += len(m.Vertices)
	}
	// Each triangle contributes up to 3 edge weights; each edge adds 2 off-diagonals + 2 diagonals.
	triplets := make([]Triplet, 0, totalTris*3+totalVerts*2)

	for mi, m := range meshes {
		conductance := prob.Layers[meshToLayer[mi]].Conductance
		N := len(m.Vertices)
		diag := make([]float64, N)
		pts := make([]mesh.Point, N)
		for i, v := range m.Vertices {
			pts[i] = v.P
		}

		// Accumulate cotangent weights directly from the triangle list.  This avoids
		// relying on the half-edge structure, whose Next/Prev links can be corrupted
		// when FromTriangleSoup shares edges between adjacent triangles.
		edgeCotan := make(map[[2]int]float64)
		for _, tri := range m.Triangles {
			a, b, c := tri[0], tri[1], tri[2]
			if a < 0 || b < 0 || c < 0 || a >= N || b >= N || c >= N {
				continue
			}
			if w := cotanWeight(pts[a], pts[b], pts[c]); w > 0 {
				edgeCotan[orderedEdge(a, b)] += w
			}
			if w := cotanWeight(pts[b], pts[c], pts[a]); w > 0 {
				edgeCotan[orderedEdge(b, c)] += w
			}
			if w := cotanWeight(pts[c], pts[a], pts[b]); w > 0 {
				edgeCotan[orderedEdge(c, a)] += w
			}
		}

		nonzero := 0
		const minEdgeG = 1e-12
		for e, w := range edgeCotan {
			if w <= 0 {
				continue
			}
			nonzero++
			i, j := e[0], e[1]
			gi := globalToNew[vindex.localToGlobal[[2]int{mi, i}]]
			gj := globalToNew[vindex.localToGlobal[[2]int{mi, j}]]
			g := conductance * w
			if g < minEdgeG {
				g = minEdgeG
			}
			triplets = append(triplets, Triplet{Row: gi, Col: gj, Val: -g})
			triplets = append(triplets, Triplet{Row: gj, Col: gi, Val: -g})
			diag[i] += g
			diag[j] += g
		}
		for vi, d := range diag {
			if d == 0 {
				continue
			}
			gi := globalToNew[vindex.localToGlobal[[2]int{mi, vi}]]
			triplets = append(triplets, Triplet{Row: gi, Col: gi, Val: d})
		}
		fmt.Printf("[PADEN solver] mesh %d layer %d vertices=%d triangles=%d cotanEdges=%d\n",
			mi, meshToLayer[mi], N, len(m.Triangles), nonzero)
	}
	return triplets
}

func orderedEdge(i, j int) [2]int {
	if i < j {
		return [2]int{i, j}
	}
	return [2]int{j, i}
}

func cotanWeight(pi, pj, pk mesh.Point) float64 {
	// Cotangent of the angle at pk opposite edge (pi,pj), scaled by 1/2 to
	// match the FEM discrete Laplacian weight used in Mesh.HalfEdge.Cotan.
	vki := mesh.Vector{DX: pi.X - pk.X, DY: pi.Y - pk.Y}
	vkj := mesh.Vector{DX: pj.X - pk.X, DY: pj.Y - pk.Y}
	cross := vki.Cross(vkj)
	if math.Abs(cross) < 1e-15 {
		return 0
	}
	return math.Abs(vki.Dot(vkj)/cross) * 0.5
}

func stampResistors(net *problem.Network, ni *nodeIndexer, triplets []Triplet) {
	for _, elem := range net.Elements {
		r, ok := elem.(*problem.Resistor)
		if !ok {
			continue
		}
		ia, okA := ni.nodeToGlobal[r.A]
		ib, okB := ni.nodeToGlobal[r.B]
		if !okA || !okB {
			continue
		}
		g := 1.0 / r.Resistance
		triplets = append(triplets,
			Triplet{Row: ia, Col: ia, Val: g},
			Triplet{Row: ia, Col: ib, Val: -g},
			Triplet{Row: ib, Col: ib, Val: g},
			Triplet{Row: ib, Col: ia, Val: -g},
		)
	}
}

func stampCurrentSources(net *problem.Network, ni *nodeIndexer, rhs []float64) {
	for _, elem := range net.Elements {
		cs, ok := elem.(*problem.CurrentSource)
		if !ok {
			continue
		}
		iF, okF := ni.nodeToGlobal[cs.F]
		iT, okT := ni.nodeToGlobal[cs.T]
		if !okF || !okT {
			continue
		}
		rhs[iF] += cs.Current
		rhs[iT] -= cs.Current
	}
}

func ensureComponentGrounding(A *CSRMatrix, known map[int]float64) {
	n := A.N
	visited := make([]bool, n)
	for i := 0; i < n; i++ {
		if visited[i] {
			continue
		}
		component := []int{i}
		visited[i] = true
		queue := []int{i}
		for len(queue) > 0 {
			u := queue[0]
			queue = queue[1:]
			for k := A.Ap[u]; k < A.Ap[u+1]; k++ {
				v := A.Aj[k]
				if visited[v] || A.Ax[k] == 0 {
					continue
				}
				visited[v] = true
				component = append(component, v)
				queue = append(queue, v)
			}
		}
		hasKnown := false
		for _, node := range component {
			if _, ok := known[node]; ok {
				hasKnown = true
				break
			}
		}
		if !hasKnown && len(component) > 0 {
			known[component[0]] = 0
			fmt.Printf("[PADEN solver] component grounded: nodes=%d first=%d\n", len(component), component[0])
		}
	}
}

func applyDirichletSym(A *CSRMatrix, b []float64, known map[int]float64) (*CSRMatrix, []float64) {
	n := A.N
	bNew := make([]float64, n)
	triplets := make([]Triplet, 0, A.nnz)
	for i := 0; i < n; i++ {
		if val, ok := known[i]; ok {
			bNew[i] = val
			triplets = append(triplets, Triplet{Row: i, Col: i, Val: 1.0})
			continue
		}
		rhs := b[i]
		for k := A.Ap[i]; k < A.Ap[i+1]; k++ {
			j := A.Aj[k]
			val := A.Ax[k]
			if _, ok := known[j]; ok {
				rhs -= val * known[j]
			} else {
				triplets = append(triplets, Triplet{Row: i, Col: j, Val: val})
			}
		}
		bNew[i] = rhs
	}
	return NewCSRFromTriplets(n, triplets), bNew
}

func reduceDirichlet(A *CSRMatrix, b []float64, known map[int]float64, unknownIdx []int, uCount int) (*CSRMatrix, []float64, error) {
	bNew := make([]float64, uCount)
	triplets := make([]Triplet, 0, A.nnz)
	for i := 0; i < A.N; i++ {
		ui := unknownIdx[i]
		if ui < 0 {
			continue
		}
		rhs := b[i]
		for k := A.Ap[i]; k < A.Ap[i+1]; k++ {
			j := A.Aj[k]
			val := A.Ax[k]
			if uj := unknownIdx[j]; uj >= 0 {
				triplets = append(triplets, Triplet{Row: ui, Col: uj, Val: val})
			} else {
				rhs -= val * known[j]
			}
		}
		bNew[ui] = rhs
	}
	return NewCSRFromTriplets(uCount, triplets), bNew, nil
}


func findBestGroundNode(prob *problem.Problem, ni *nodeIndexer) int {
	maxVoltage := math.Inf(-1)
	groundIdx := -1
	for _, net := range prob.Networks {
		for _, elem := range net.Elements {
			if vs, ok := elem.(*problem.VoltageSource); ok {
				if idx, ok := ni.nodeToGlobal[vs.N]; ok && vs.Voltage > maxVoltage {
					maxVoltage = vs.Voltage
					groundIdx = idx
				}
			}
		}
	}
	return groundIdx
}

func produceLayerSolutions(prob *problem.Problem, vindex *vertexIndexer, meshes []*mesh.Mesh, meshToLayer []int, v []float64, disconnected [][]*mesh.CompactMesh, globalToNew []int) []*LayerSolution {
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
			giOrig := vindex.localToGlobal[[2]int{mi, vi}]
			potentials[vi] = v[globalToNew[giOrig]]
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

	for li, ls := range layerSols {
		totalTris := 0
		vMin, vMax := math.Inf(1), math.Inf(-1)
		for _, pots := range ls.Potentials {
			for _, p := range pots {
				if p < vMin { vMin = p }
				if p > vMax { vMax = p }
			}
		}
		for _, cm := range ls.CompactMeshes {
			totalTris += len(cm.Triangles)
		}
		fmt.Printf("[PADEN solver] layer %d result meshes=%d triangles=%d disconnected=%d vrange=[%.4f,%.4f]\n",
			li, len(ls.CompactMeshes), totalTris, len(ls.DisconnectedCompact), vMin, vMax)
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

func logNetworkPotentials(prob *problem.Problem, ni *nodeIndexer, v []float64) {
	for _, net := range prob.Networks {
		for _, conn := range net.Connections {
			idx, ok := ni.nodeToGlobal[conn.NodeID]
			layerName := "internal"
			if conn.Layer != nil {
				layerName = conn.Layer.Name
			}
			if ok {
				fmt.Printf("[PADEN solver] conn %s (%.3f,%.3f) idx=%d v=%.6f\n", layerName, conn.Point.X, conn.Point.Y, idx, v[idx])
			} else {
				fmt.Printf("[PADEN solver] conn %s (%.3f,%.3f) not indexed\n", layerName, conn.Point.X, conn.Point.Y)
			}
		}
		for _, elem := range net.Elements {
			switch e := elem.(type) {
			case *problem.VoltageSource:
				ip, okP := ni.nodeToGlobal[e.P]
				in, okN := ni.nodeToGlobal[e.N]
				fmt.Printf("[PADEN solver] VS %.3fV P=%d(v=%.6f) N=%d(v=%.6f)\n",
					e.Voltage, ip, mapV(v, ip, okP), in, mapV(v, in, okN))
			case *problem.CurrentSource:
				iF, okF := ni.nodeToGlobal[e.F]
				iT, okT := ni.nodeToGlobal[e.T]
				fmt.Printf("[PADEN solver] CS %.3fA F=%d(v=%.6f) T=%d(v=%.6f)\n",
					e.Current, iF, mapV(v, iF, okF), iT, mapV(v, iT, okT))
			}
		}
	}
}

func mapV(v []float64, i int, ok bool) float64 {
	if !ok || i < 0 || i >= len(v) {
		return math.NaN()
	}
	return v[i]
}

func logNetworkInfo(stage string, networks []*problem.Network) {
	numVS, numCS, numR, numVR := 0, 0, 0, 0
	var voltages, currents []float64
	for _, net := range networks {
		for _, elem := range net.Elements {
			switch e := elem.(type) {
			case *problem.VoltageSource:
				numVS++
				voltages = append(voltages, e.Voltage)
			case *problem.CurrentSource:
				numCS++
				currents = append(currents, e.Current)
			case *problem.Resistor:
				numR++
			case *problem.VoltageRegulator:
				numVR++
				voltages = append(voltages, e.Voltage)
			}
		}
	}
	fmt.Printf("[PADEN solver] networks %s: count=%d VS=%d CS=%d R=%d VR=%d voltages=%v currents=%v\n",
		stage, len(networks), numVS, numCS, numR, numVR, voltages, currents)
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

type unionFind struct {
	parent []int
}

func newUnionFind(n int) *unionFind {
	uf := &unionFind{parent: make([]int, n)}
	for i := range uf.parent {
		uf.parent[i] = i
	}
	return uf
}

func (uf *unionFind) find(x int) int {
	p := uf.parent[x]
	if p == x {
		return x
	}
	uf.parent[x] = uf.find(p)
	return uf.parent[x]
}

func (uf *unionFind) union(a, b int) {
	ra := uf.find(a)
	rb := uf.find(b)
	if ra != rb {
		uf.parent[rb] = ra
	}
}

// scaleSymmetric returns D^{-1/2} A D^{-1/2} and D^{-1/2} b, along with the
// scale vector s = diag(D^{-1/2}). The caller can recover x = s .* xScaled.
func scaleSymmetric(A *CSRMatrix, b []float64) (*CSRMatrix, []float64, []float64) {
	d := A.Diagonal()
	s := make([]float64, A.N)
	for i, v := range d {
		if v > 1e-12 {
			s[i] = 1.0 / math.Sqrt(v)
		} else {
			s[i] = 1.0
		}
	}

	triplets := make([]Triplet, 0, A.nnz)
	for i := 0; i < A.N; i++ {
		si := s[i]
		for k := A.Ap[i]; k < A.Ap[i+1]; k++ {
			j := A.Aj[k]
			triplets = append(triplets, Triplet{
				Row: i,
				Col: j,
				Val: A.Ax[k] * si * s[j],
			})
		}
	}

	scaledB := make([]float64, A.N)
	for i := range b {
		scaledB[i] = b[i] * s[i]
	}

	return NewCSRFromTriplets(A.N, triplets), scaledB, s
}
