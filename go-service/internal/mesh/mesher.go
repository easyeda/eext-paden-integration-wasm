package mesh

import (
	"fmt"
	"math"
	"sort"

	"github.com/easyeda/eext-paden-integration/go-service/internal/geometry"
)

// Config controls mesh generation.
type Config struct {
	MinimumAngle float64
	MaximumSize  float64
}

// DefaultConfig returns a reasonable default config.
func DefaultConfig() Config {
	return Config{
		MinimumAngle: 20.0,
		MaximumSize:  1.2,
	}
}

// Mesher generates triangular meshes for polygons.
type Mesher struct {
	Config      Config
	MaxVertices int // per-polygon cap (0 = use adaptiveVertexBudget based on poly area)
}

// NewMesher creates a mesher with the given config.
func NewMesher(cfg Config) *Mesher {
	return &Mesher{Config: cfg}
}

// PolygonToMesh triangulates a polygon with holes using a boundary-conforming
// earcut mesh, followed by edge-split refinement, interior Steiner point
// insertion for large triangles, and edge-flip quality improvement. The
// interior-point pass is what stops large copper pours from degenerating into
// the long thin slivers that the previous earcut-only path produced.
func (m *Mesher) PolygonToMesh(poly geometry.Polygon, seedPoints []Point) (*Mesh, error) {
	if len(poly) == 0 || len(poly[0]) < 3 {
		return NewMesh(), nil
	}

	maxSize := m.Config.MaximumSize
	if maxSize <= 0 {
		maxSize = 1.2
	}

	// Per-polygon vertex budget: scale with area when the caller did not
	// specify one. For test-3 (~1071 mm² total copper over many pours)
	// something like ~2000–4000 verts per polygon keeps solves fast while
	// giving every pour enough interior Steiner points to avoid slivers.
	maxVerts := m.MaxVertices
	if maxVerts <= 0 {
		maxVerts = adaptiveVertexBudget(polygonAreaSum(poly))
	}

	// Tighten the filter thresholds so genuine slivers near pads are
	// rejected rather than polluting the FEM system.
	minSeedDist := math.Max(math.Min(maxSize*0.04, 0.08), 0.02)

	// Scale simplification tolerance with mesh size, and ensure it always
	// exceeds the bridge's circle chord tolerance (0.05mm). The bridge
	// emits up to 128 points per Gerber circle for visual smoothness; if
	// the mesher's simplify tolerance is finer than that chord, every
	// circle point survives and crowds out the per-polygon vertex budget
	// before interior refinement runs. By making simplTol >= 2x the chord
	// the mesher cleanly drops the redundant boundary points while the
	// bridge-rendered geometry (and the FEM solve) keep their shape.
	simplTol := math.Max(0.05, maxSize*0.05)
	poly = poly.Simplify(simplTol)
	if len(poly) == 0 || len(poly[0]) < 3 {
		return NewMesh(), nil
	}

	// Primary path: boundary-conforming earcut (gives us a correct boundary).
	tri, err := Earcut(poly)
	if err != nil {
		return nil, err
	}
	pts := append([]Point(nil), tri.Vertices...)
	tris := append([][3]int(nil), tri.Triangles...)
	tris = filterValidTriangles(pts, tris, poly)
	if len(tris) == 0 {
		return NewMesh(), nil
	}

	// Insert seed points (connection terminals) so boundary conditions are
	// applied at exact locations. Reject seed points too close to existing
	// vertices or edges.
	for _, sp := range seedPoints {
		if !pointInPolygon(sp, poly) {
			continue
		}
		inserted := false
		for ti := range tris {
			t := tris[ti]
			a, b, c := pts[t[0]], pts[t[1]], pts[t[2]]
			if pointInTriangle(sp, a, b, c) {
				if distTo(sp, a) < minSeedDist || distTo(sp, b) < minSeedDist || distTo(sp, c) < minSeedDist {
					inserted = true // treated as inserted so no warning is logged
					break
				}
				dEdge := math.Min(math.Min(distToSegment(sp, a, b), distToSegment(sp, b, c)), distToSegment(sp, c, a))
				if dEdge < minSeedDist {
					inserted = true
					break
				}
				insertPointInSoup(&pts, &tris, ti, sp)
				inserted = true
				break
			}
		}
		if !inserted {
			fmt.Printf("[PADEN mesh] seed point (%.4f,%.4f) not inserted\n", sp.X, sp.Y)
		}
	}

	// Pass 1: edge-split refinement. Only splits boundary edges so large
	// polygons keep their outline exact.
	for iter := 0; iter < 30 && len(pts) < maxVerts; iter++ {
		edgeMap := buildEdgeMap(tris)
		var candidates [][2]int
		for e, tis := range edgeMap {
			if len(tis) == 0 {
				continue
			}
			if edgeLen(pts, e[0], e[1]) > maxSize {
				candidates = append(candidates, e)
			}
		}
		if len(candidates) == 0 {
			break
		}
		sort.Slice(candidates, func(i, j int) bool {
			return edgeLen(pts, candidates[i][0], candidates[i][1]) > edgeLen(pts, candidates[j][0], candidates[j][1])
		})
		splitCount := 0
		for _, e := range candidates {
			if len(pts) >= maxVerts {
				break
			}
			if edgeLen(pts, e[0], e[1]) <= maxSize {
				continue
			}
			splitEdgeInSoup(&pts, &tris, edgeMap, e[0], e[1])
			splitCount++
		}
		if splitCount == 0 {
			break
		}
	}

	// Pass 2: interior Steiner point insertion. Two complementary rules:
	//   (a) Long-edge rule — any triangle whose longest edge exceeds
	//       `maxSize` gets a centroid Steiner point (catches the wide
	//       sparse earcut output on large copper pours).
	//   (b) Aspect rule — any triangle with aspect ratio ≥ 3 gets the
	//       midpoint of its longest edge inserted as a Steiner point.
	//       This directly attacks the "long thin sliver" failure mode that
	//       the boundary-only refinement cannot reach.
	// Both rounds cascade: each insertion produces three smaller
	// triangles that get re-evaluated in the same iteration until the
	// mesh converges or the per-polygon budget is exhausted.
	// Pass 2: quality-driven refinement. Repeatedly find the worst-quality
	// triangle and split its longest edge at the midpoint until every
	// triangle is well shaped or the per-polygon budget is exhausted.
	//
	// Quality metric: aspect ratio Q = longest_edge / shortest_edge.
	// Triangles with Q ≥ 3 are split (their smallest interior angle is
	// then ~19° max, well below the boundary-conforming earcut baseline).
	// After splitting, each new triangle has the longest edge halved,
	// so cascading iterations drive Q down geometrically.
	//
	// The previous version used an area threshold alongside Q, which
	// left tiny slivers (sub-millimetre scale, sub-0.001 mm² area)
	// untouched. The current rule is Q-only — splitting a tiny sliver
	// is cheap (one midpoint point) and is the real source of the broken
	// faces the user reported.
	// Pass 2: quality-driven refinement. Each iteration finds every
	// triangle whose aspect ratio Q = longest/shortest exceeds
	// `targetAspect` and splits its longest edge at the midpoint. The
	// cascade converges geometrically: splitting a Q=10 triangle halves
	// the resulting Q, so 3-4 outer iterations bring even pathological
	// obtuse slivers under targetAspect.
	targetAspect := 2.5
	for iter := 0; iter < 30 && len(pts) < maxVerts; iter++ {
		// Build the list of (triangle, longest-edge endpoint pair) for
		// every bad-quality triangle. Sort by descending Q so the worst
		// slivers are processed first.
		type splitJob struct {
			tri    [3]int
			edge   [2]int
			other  int // the third vertex that forms the (edge[0],edge[1],other) triangle
			q      float64
		}
		var jobs []splitJob
		for ti := range tris {
			t := tris[ti]
			a, b, c := pts[t[0]], pts[t[1]], pts[t[2]]
			e0 := math.Hypot(b.X-a.X, b.Y-a.Y)
			e1 := math.Hypot(c.X-b.X, c.Y-b.Y)
			e2 := math.Hypot(a.X-c.X, a.Y-c.Y)
			edges := [3]float64{e0, e1, e2}
			longest := edges[0]
			shortest := edges[0]
			longIdx := 0
			for i := 1; i < 3; i++ {
				if edges[i] > longest {
					longest = edges[i]
					longIdx = i
				}
				if edges[i] < shortest {
					shortest = edges[i]
				}
			}
			if shortest < 1e-9 {
				continue
			}
			q := longest / shortest
			if q >= targetAspect {
				pairs := [][3]int{{t[0], t[1], t[2]}, {t[1], t[2], t[0]}, {t[2], t[0], t[1]}}
				choice := pairs[longIdx]
				jobs = append(jobs, splitJob{
					tri:   [3]int{choice[0], choice[1], choice[2]},
					edge:   [2]int{choice[0], choice[1]},
					other:  choice[2],
					q:      q,
				})
			}
		}
		if len(jobs) == 0 {
			break
		}
		// Process worst jobs first. Edges are split via the shared
		// splitEdgeInSoup helper, which handles triangles on both
		// sides of an interior edge.
		sort.Slice(jobs, func(i, j int) bool { return jobs[i].q > jobs[j].q })
		processedEdges := make(map[[2]int]bool)
		splitsDone := 0
		for _, jb := range jobs {
			if len(pts) >= maxVerts {
				break
			}
			edgeKey := [2]int{jb.edge[0], jb.edge[1]}
			if jb.edge[0] > jb.edge[1] {
				edgeKey = [2]int{jb.edge[1], jb.edge[0]}
			}
			if processedEdges[edgeKey] {
				continue
			}
			beforeN := len(tris)
			splitEdgeInSoup(&pts, &tris, buildEdgeMap(tris), jb.edge[0], jb.edge[1])
			if len(tris) > beforeN {
				splitsDone++
				processedEdges[edgeKey] = true
			}
		}
		if splitsDone == 0 {
			break
		}
	}

	// Improve element shape by flipping interior edges.
	if m.Config.MinimumAngle > 0 && len(pts) < maxVerts {
		minAngleRad := math.Pi * m.Config.MinimumAngle / 180.0
		for iter := 0; iter < 10; iter++ {
			edgeMap := buildEdgeMap(tris)
			flipped := false
			for e, tis := range edgeMap {
				if len(tis) != 2 {
					continue
				}
				if triMinAngle(pts, tris[tis[0]]) >= minAngleRad && triMinAngle(pts, tris[tis[1]]) >= minAngleRad {
					continue
				}
				if tryFlipEdge(pts, &tris, edgeMap, e[0], e[1], poly) {
					flipped = true
				}
			}
			if !flipped {
				break
			}
		}
	}

	tris = filterValidTriangles(pts, tris, poly)
	if len(tris) == 0 {
		return NewMesh(), nil
	}
	return FromTriangleSoup(pts, tris), nil
}

// adaptiveVertexBudget returns a per-polygon vertex budget that scales with
// the polygon's copper area. The shape is intentionally generous: most
// polygons spend a large fraction of their budget on Gerber arc
// discretisation along the boundary, and a separate interior budget for
// Steiner refinement is essential for killing slivers. The cap (30000 for
// the largest polygons) is well below the global mesh budget (60000) so a
// single large pour still leaves room for connected polygons on other
// layers / regions.
func adaptiveVertexBudget(area float64) int {
	switch {
	case area < 20:
		return 600
	case area < 100:
		return 1500
	case area < 400:
		return 3000
	case area < 1500:
		return 6000
	case area < 5000:
		return 12000
	default:
		return 30000
	}
}

func polygonAreaSum(poly geometry.Polygon) float64 {
	var area float64
	for _, ring := range poly {
		area += math.Abs(ring.Area())
	}
	return area
}

func triArea(a, b, c Point) float64 {
	return math.Abs((b.X-a.X)*(c.Y-a.Y) - (c.X-a.X)*(b.Y-a.Y)) * 0.5
}

func findTriIndex(tris [][3]int, target [3]int) int {
	// Match one triangle in any cyclic rotation.
	for i, t := range tris {
		if triEquals(t, target) {
			return i
		}
	}
	return -1
}

func triEquals(a, b [3]int) bool {
	for i := 0; i < 3; i++ {
		matched := false
		for j := 0; j < 3; j++ {
			if a[i] == b[j] {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// generatePoints creates boundary + adaptive interior points.
func (m *Mesher) generatePoints(poly geometry.Polygon, seedPoints []Point) []Point {
	maxSize := m.Config.MaximumSize
	if maxSize <= 0 {
		maxSize = 1.2
	}

	exterior := poly[0]
	var holes []geometry.Ring
	if len(poly) > 1 {
		holes = poly[1:]
	}

	// Densify boundary
	pts := make(map[[2]float64]bool)
	addPoint := func(p Point) {
		key := [2]float64{round(p.X, 3), round(p.Y, 3)}
		pts[key] = true
	}

	densifyRing := func(ring geometry.Ring) {
		n := len(ring)
		if n > 0 && ring[0] == ring[n-1] {
			n--
		}
		if n < 2 {
			return
		}
		for i := 0; i < n; i++ {
			a := ring[i]
			b := ring[(i+1)%n]
			addPoint(a)
			m.subdivideEdge(a, b, maxSize, addPoint)
		}
	}

	densifyRing(exterior)
	for _, hole := range holes {
		densifyRing(hole)
	}

	// Adaptive interior grid
	box := poly.Bounds()
	w := box.MaxX - box.MinX
	h := box.MaxY - box.MinY
	if w <= 0 || h <= 0 {
		return nil
	}

	// Limit total grid points to keep WASM memory reasonable.
	maxVerts := 5000
	spacing := maxSize
	if (w*h)/(spacing*spacing) > float64(maxVerts)*0.6 {
		spacing = math.Sqrt((w * h) / (float64(maxVerts) * 0.6))
	}

	nx := int(w/spacing) + 1
	ny := int(h/spacing) + 1
	for i := 0; i <= nx; i++ {
		for j := 0; j <= ny; j++ {
			x := box.MinX + float64(i)*spacing
			y := box.MinY + float64(j)*spacing
			p := Point{X: x, Y: y}
			if pointInPolygon(p, poly) {
				addPoint(p)
			}
		}
	}

	// Add seed points
	for _, p := range seedPoints {
		if pointInPolygon(p, poly) {
			addPoint(p)
		}
	}

	out := make([]Point, 0, len(pts))
	for k := range pts {
		out = append(out, Point{X: k[0], Y: k[1]})
	}
	return out
}

func (m *Mesher) subdivideEdge(a, b Point, maxSize float64, add func(Point)) {
	length := math.Hypot(b.X-a.X, b.Y-a.Y)
	if length <= maxSize || length < 1e-9 {
		return
	}
	mid := Point{X: (a.X + b.X) / 2, Y: (a.Y + b.Y) / 2}
	add(mid)
	m.subdivideEdge(a, mid, maxSize, add)
	m.subdivideEdge(mid, b, maxSize, add)
}

func (m *Mesher) refineMesh(mesh *Mesh, poly geometry.Polygon) *Mesh {
	if m.Config.MinimumAngle <= 0 || len(mesh.Vertices) > 12000 {
		return mesh
	}
	cosThresh := math.Cos(math.Pi * m.Config.MinimumAngle / 180.0)
	maxIter := 3

	for iter := 0; iter < maxIter; iter++ {
		points := make([]Point, len(mesh.Vertices))
		for i, v := range mesh.Vertices {
			points[i] = v.P
		}
		tris := make([][3]int, 0, len(mesh.Faces))
		for _, f := range mesh.Faces {
			verts := f.Vertices()
			if len(verts) == 3 {
				tris = append(tris, [3]int{verts[0].Idx, verts[1].Idx, verts[2].Idx})
			}
		}

		newPts := make([]Point, len(points))
		copy(newPts, points)
		added := false
		for _, tri := range tris {
			p0, p1, p2 := points[tri[0]], points[tri[1]], points[tri[2]]
			corners := [][3]Point{{p0, p1, p2}, {p1, p2, p0}, {p2, p0, p1}}
			for _, c := range corners {
				a, b, o := c[0], c[1], c[2]
				ab := math.Hypot(b.X-a.X, b.Y-a.Y)
				ao := math.Hypot(o.X-a.X, o.Y-a.Y)
				if ab < 1e-12 || ao < 1e-12 {
					continue
				}
				cosA := ((b.X-a.X)*(o.X-a.X) + (b.Y-a.Y)*(o.Y-a.Y)) / (ab * ao)
				if cosA > cosThresh {
					// Insert midpoint of longest edge
					edges := [][2]Point{{p0, p1}, {p1, p2}, {p2, p0}}
					maxLen := 0.0
					var longest [2]Point
					for _, e := range edges {
						l := math.Hypot(e[1].X-e[0].X, e[1].Y-e[0].Y)
						if l > maxLen {
							maxLen = l
							longest = e
						}
					}
					mid := Point{X: (longest[0].X + longest[1].X) / 2, Y: (longest[0].Y + longest[1].Y) / 2}
					if pointInPolygon(mid, poly) {
						newPts = append(newPts, mid)
						added = true
					}
					break
				}
			}
		}
		if !added {
			break
		}
		tris = delaunayTriangulate(newPts)
		filtered := filterTrianglesInsidePolygon(newPts, tris, poly)
		if len(filtered) == 0 {
			break
		}
		mesh = FromTriangleSoup(newPts, filtered)
	}
	return mesh
}

// delaunayTriangulate computes the Delaunay triangulation of pts using Bowyer-Watson.
func delaunayTriangulate(pts []Point) [][3]int {
	n := len(pts)
	if n < 3 {
		return nil
	}

	// Compute bounding box for super triangle
	minX, minY := pts[0].X, pts[0].Y
	maxX, maxY := pts[0].X, pts[0].Y
	for _, p := range pts {
		if p.X < minX {
			minX = p.X
		}
		if p.X > maxX {
			maxX = p.X
		}
		if p.Y < minY {
			minY = p.Y
		}
		if p.Y > maxY {
			maxY = p.Y
		}
	}
	dx := maxX - minX
	dy := maxY - minY
	dmax := dx
	if dy > dmax {
		dmax = dy
	}
	midX := (minX + maxX) / 2
	midY := (minY + maxY) / 2

	// Super triangle — must be counter-clockwise so that inCircumcircle(det>0)
	// works consistently for all triangles created by Bowyer-Watson.
	super := [3]Point{
		{X: midX - 20*dmax, Y: midY - dmax},
		{X: midX + 20*dmax, Y: midY - dmax},
		{X: midX, Y: midY + 20*dmax},
	}
	allPts := append([]Point{super[0], super[1], super[2]}, pts...)
	superIdx := []int{0, 1, 2}

	triangles := [][3]int{{0, 1, 2}}

	for i := 3; i < len(allPts); i++ {
		p := allPts[i]
		var bad []int
		for ti, tri := range triangles {
			if inCircumcircle(allPts[tri[0]], allPts[tri[1]], allPts[tri[2]], p) {
				bad = append(bad, ti)
			}
		}

		if len(bad) == 0 {
			continue
		}

		// Collect boundary edges of bad polygon
		edgeCount := make(map[[2]int]int)
		for _, ti := range bad {
			tri := triangles[ti]
			addTriEdge(edgeCount, tri[0], tri[1])
			addTriEdge(edgeCount, tri[1], tri[2])
			addTriEdge(edgeCount, tri[2], tri[0])
		}

		// Remove bad triangles
		newTris := make([][3]int, 0, len(triangles)-len(bad))
		badSet := make(map[int]bool)
		for _, ti := range bad {
			badSet[ti] = true
		}
		for ti, tri := range triangles {
			if !badSet[ti] {
				newTris = append(newTris, tri)
			}
		}
		triangles = newTris

		// Add new triangles from boundary to p, keeping every triangle
		// counter-clockwise so the circumcircle test remains consistent.
		for e, c := range edgeCount {
			if c == 1 {
				a, b := e[0], e[1]
				cross := (allPts[b].X-allPts[a].X)*(allPts[i].Y-allPts[a].Y) -
					(allPts[b].Y-allPts[a].Y)*(allPts[i].X-allPts[a].X)
				if cross < 0 {
					a, b = b, a
				}
				triangles = append(triangles, [3]int{a, b, i})
			}
		}
	}

	// Remove triangles sharing super-triangle vertices
	var result [][3]int
	for _, tri := range triangles {
		sharesSuper := false
		for _, idx := range tri {
			for _, sidx := range superIdx {
				if idx == sidx {
					sharesSuper = true
					break
				}
			}
			if sharesSuper {
				break
			}
		}
		if !sharesSuper {
			// Map back to original indices
			result = append(result, [3]int{tri[0] - 3, tri[1] - 3, tri[2] - 3})
		}
	}
	return result
}

func addTriEdge(m map[[2]int]int, a, b int) {
	if a > b {
		a, b = b, a
	}
	m[[2]int{a, b}]++
}

func inCircumcircle(a, b, c, p Point) bool {
	ax := a.X - p.X
	ay := a.Y - p.Y
	bx := b.X - p.X
	by := b.Y - p.Y
	cx := c.X - p.X
	cy := c.Y - p.Y

	det := (ax*ax+ay*ay)*(bx*cy-cx*by) -
		(bx*bx+by*by)*(ax*cy-cx*ay) +
		(cx*cx+cy*cy)*(ax*by-bx*ay)
	return det > 0
}

func filterTrianglesInsidePolygon(pts []Point, tris [][3]int, poly geometry.Polygon) [][3]int {
	var filtered [][3]int
	for _, tri := range tris {
		a, b, c := tri[0], tri[1], tri[2]
		if a < 0 || b < 0 || c < 0 || a >= len(pts) || b >= len(pts) || c >= len(pts) {
			continue
		}
		cx := (pts[a].X + pts[b].X + pts[c].X) / 3
		cy := (pts[a].Y + pts[b].Y + pts[c].Y) / 3
		if pointInPolygon(Point{X: cx, Y: cy}, poly) {
			filtered = append(filtered, tri)
		}
	}
	return filtered
}

// improveMesh performs edge-flips on interior edges to maximize the minimum
// angle. This cleans up the slivers produced by unconstrained Delaunay near
// narrow polygon features without moving vertices or changing the boundary.
func improveMesh(pts []Point, tris [][3]int, poly geometry.Polygon) [][3]int {
	const maxIter = 5
	for iter := 0; iter < maxIter; iter++ {
		edgeMap := make(map[[2]int][]int)
		for ti, tri := range tris {
			for k := 0; k < 3; k++ {
				a, b := tri[k], tri[(k+1)%3]
				if a > b {
					a, b = b, a
				}
				edgeMap[[2]int{a, b}] = append(edgeMap[[2]int{a, b}], ti)
			}
		}

		flipped := false
		for e, tis := range edgeMap {
			if len(tis) != 2 {
				continue
			}
			a, b := e[0], e[1]
			c := oppositeVertex(tris[tis[0]], a, b)
			d := oppositeVertex(tris[tis[1]], a, b)
			if c < 0 || d < 0 || c == d {
				continue
			}

			// The quadrilateral must be convex for a flip to be valid.
			// c and d are on opposite sides of ab (true because the two triangles
			// lie on opposite sides). We also need a and b on opposite sides of cd.
			crossCD := cross2(pts[c], pts[d], pts[a]) * cross2(pts[c], pts[d], pts[b])
			if crossCD >= 0 {
				continue
			}

			curMin := math.Min(triMinAngle(pts, tris[tis[0]]), triMinAngle(pts, tris[tis[1]]))

			n1 := [3]int{c, a, d}
			n2 := [3]int{c, d, b}
			if signedArea2(pts[c], pts[a], pts[d]) <= 1e-12 || signedArea2(pts[c], pts[d], pts[b]) <= 1e-12 {
				continue
			}

			newMin := math.Min(triMinAngle(pts, n1), triMinAngle(pts, n2))
			if newMin <= curMin+1e-6 {
				continue
			}

			cen1 := centroid(pts[c], pts[a], pts[d])
			cen2 := centroid(pts[c], pts[d], pts[b])
			if !pointInPolygon(cen1, poly) || !pointInPolygon(cen2, poly) {
				continue
			}

			tris[tis[0]] = n1
			tris[tis[1]] = n2
			flipped = true
		}
		if !flipped {
			break
		}
	}
	return tris
}

func oppositeVertex(tri [3]int, a, b int) int {
	for _, v := range tri {
		if v != a && v != b {
			return v
		}
	}
	return -1
}

func cross2(a, b, c Point) float64 {
	return (b.X-a.X)*(c.Y-a.Y) - (b.Y-a.Y)*(c.X-a.X)
}

func signedArea2(a, b, c Point) float64 {
	return cross2(a, b, c)
}

func centroid(a, b, c Point) Point {
	return Point{X: (a.X + b.X + c.X) / 3, Y: (a.Y + b.Y + c.Y) / 3}
}

func triMinAngle(pts []Point, tri [3]int) float64 {
	p0, p1, p2 := pts[tri[0]], pts[tri[1]], pts[tri[2]]
	a := math.Hypot(p1.X-p0.X, p1.Y-p0.Y)
	b := math.Hypot(p2.X-p1.X, p2.Y-p1.Y)
	c := math.Hypot(p0.X-p2.X, p0.Y-p2.Y)
	if a < 1e-12 || b < 1e-12 || c < 1e-12 {
		return 0
	}
	ang0 := math.Acos(clamp((a*a+c*c-b*b)/(2*a*c), -1, 1))
	ang1 := math.Acos(clamp((a*a+b*b-c*c)/(2*a*b), -1, 1))
	ang2 := math.Pi - ang0 - ang1
	return math.Min(ang0, math.Min(ang1, ang2))
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func pointInPolygon(p Point, poly geometry.Polygon) bool {
	if len(poly) == 0 {
		return false
	}
	// Check exterior
	if !pointInRing(p, poly[0]) {
		return false
	}
	// Check holes
	for i := 1; i < len(poly); i++ {
		if pointInRing(p, poly[i]) {
			return false
		}
	}
	return true
}

func pointInRing(p Point, ring geometry.Ring) bool {
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

func pointInTriangle(p, a, b, c Point) bool {
	// Barycentric technique with a small tolerance for points on edges.
	den := (b.Y-c.Y)*(a.X-c.X) + (c.X-b.X)*(a.Y-c.Y)
	if math.Abs(den) < 1e-12 {
		return false
	}
	w1 := ((b.Y-c.Y)*(p.X-c.X) + (c.X-b.X)*(p.Y-c.Y)) / den
	w2 := ((c.Y-a.Y)*(p.X-c.X) + (a.X-c.X)*(p.Y-c.Y)) / den
	w3 := 1 - w1 - w2
	tol := -1e-9
	return w1 >= tol && w2 >= tol && w3 >= tol
}

func round(v float64, decimals int) float64 {
	pow := math.Pow(10, float64(decimals))
	return math.Round(v*pow) / pow
}

// buildEdgeMap maps each undirected edge to the indices of triangles that use it.
func buildEdgeMap(tris [][3]int) map[[2]int][]int {
	m := make(map[[2]int][]int, len(tris)*3)
	for ti, t := range tris {
		for k := 0; k < 3; k++ {
			a, b := t[k], t[(k+1)%3]
			if a > b {
				a, b = b, a
			}
			m[[2]int{a, b}] = append(m[[2]int{a, b}], ti)
		}
	}
	return m
}

func edgeLen(pts []Point, a, b int) float64 {
	return math.Hypot(pts[b].X-pts[a].X, pts[b].Y-pts[a].Y)
}

func distTo(a, b Point) float64 {
	return math.Hypot(b.X-a.X, b.Y-a.Y)
}

func distToSegment(p, a, b Point) float64 {
	dx := b.X - a.X
	dy := b.Y - a.Y
	if dx == 0 && dy == 0 {
		return math.Hypot(p.X-a.X, p.Y-a.Y)
	}
	t := ((p.X-a.X)*dx + (p.Y-a.Y)*dy) / (dx*dx + dy*dy)
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	return math.Hypot(p.X-(a.X+t*dx), p.Y-(a.Y+t*dy))
}

// triangleEdgeInfo returns the third vertex of tri opposite the undirected
// edge {a,b}, and whether the edge appears reversed (as b->a) in tri.
func triangleEdgeInfo(tri [3]int, a, b int) (third int, reversed bool) {
	for i, v := range tri {
		if v == a {
			next := tri[(i+1)%3]
			if next == b {
				return tri[(i+2)%3], false
			}
			prev := tri[(i+2)%3]
			if prev == b {
				return next, true
			}
		}
	}
	return -1, false
}

// splitEdgeInSoup subdivides the undirected edge {a,b} at its midpoint and
// updates every triangle that uses that edge. The edgeMap is updated in place.
func splitEdgeInSoup(pts *[]Point, tris *[][3]int, edgeMap map[[2]int][]int, a, b int) {
	key := [2]int{a, b}
	if a > b {
		key = [2]int{b, a}
	}
	tis := edgeMap[key]
	if len(tis) == 0 {
		return
	}
	mid := Point{
		X: ((*pts)[a].X + (*pts)[b].X) / 2,
		Y: ((*pts)[a].Y + (*pts)[b].Y) / 2,
	}
	vi := len(*pts)
	*pts = append(*pts, mid)

	for _, ti := range tis {
		tri := (*tris)[ti]
		third, reversed := triangleEdgeInfo(tri, a, b)
		if third < 0 {
			continue
		}
		if !reversed {
			// tri = (a,b,third) -> (a,mid,third) + (mid,b,third)
			(*tris)[ti] = [3]int{a, vi, third}
			*tris = append(*tris, [3]int{vi, b, third})
		} else {
			// tri = (b,a,third) -> (b,mid,third) + (mid,a,third)
			(*tris)[ti] = [3]int{b, vi, third}
			*tris = append(*tris, [3]int{vi, a, third})
		}
	}
}

// insertPointInSoup replaces triangle triIdx that contains p with three
// triangles fanning out from p.
func insertPointInSoup(pts *[]Point, tris *[][3]int, triIdx int, p Point) {
	v0, v1, v2 := (*tris)[triIdx][0], (*tris)[triIdx][1], (*tris)[triIdx][2]
	vi := len(*pts)
	*pts = append(*pts, p)
	(*tris)[triIdx] = [3]int{v0, v1, vi}
	*tris = append(*tris, [3]int{v1, v2, vi})
	*tris = append(*tris, [3]int{v2, v0, vi})
}

// tryFlipEdge flips the interior edge {a,b} if it improves the minimum angle
// of the adjacent quadrilateral. It returns true if a flip occurred.
func tryFlipEdge(pts []Point, tris *[][3]int, edgeMap map[[2]int][]int, a, b int, poly geometry.Polygon) bool {
	key := [2]int{a, b}
	if a > b {
		key = [2]int{b, a}
	}
	tis := edgeMap[key]
	if len(tis) != 2 {
		return false
	}
	t1, t2 := (*tris)[tis[0]], (*tris)[tis[1]]
	c, rev1 := triangleEdgeInfo(t1, a, b)
	d, rev2 := triangleEdgeInfo(t2, a, b)
	if c < 0 || d < 0 || rev1 == rev2 {
		return false
	}

	// The two triangles are (a,b,c) and (b,a,d) in some order.
	// Flipping the diagonal from (a,b) to (c,d) requires a convex quadrilateral.
	if !quadConvex(pts, a, b, c, d) {
		return false
	}

	curMin := math.Min(triMinAngle(pts, t1), triMinAngle(pts, t2))
	var n1, n2 [3]int
	if !rev1 {
		n1 = [3]int{c, a, d}
		n2 = [3]int{c, d, b}
	} else {
		n1 = [3]int{c, b, d}
		n2 = [3]int{c, d, a}
	}

	// Validate centroid containment before accepting the flip.
	cen1 := Point{X: (pts[n1[0]].X + pts[n1[1]].X + pts[n1[2]].X) / 3, Y: (pts[n1[0]].Y + pts[n1[1]].Y + pts[n1[2]].Y) / 3}
	cen2 := Point{X: (pts[n2[0]].X + pts[n2[1]].X + pts[n2[2]].X) / 3, Y: (pts[n2[0]].Y + pts[n2[1]].Y + pts[n2[2]].Y) / 3}
	if !pointInPolygon(cen1, poly) || !pointInPolygon(cen2, poly) {
		return false
	}

	newMin := math.Min(triMinAngle(pts, n1), triMinAngle(pts, n2))
	if newMin <= curMin+1e-6 {
		return false
	}

	(*tris)[tis[0]] = n1
	(*tris)[tis[1]] = n2
	return true
}

// quadConvex reports whether the quadrilateral formed by the two triangles
// sharing edge {a,b} is convex (the two opposite vertices lie on opposite
// sides of the line through c and d).
func quadConvex(pts []Point, a, b, c, d int) bool {
	crossA := cross2(pts[c], pts[d], pts[a])
	crossB := cross2(pts[c], pts[d], pts[b])
	return crossA*crossB < 0
}

// filterValidTriangles drops degenerate or hole-spanning triangles. The
// minimum-angle threshold is intentionally generous (15°). The simple
// longest/shortest aspect ratio used below is a poor proxy for the
// obtuse-triangle case the PCB copper primitives produce in practice —
// two short sides meeting at a small angle can have aspect < 2 yet
// still be a clear sliver — so we use the true minimum angle.
func filterValidTriangles(points []Point, triangles [][3]int, poly geometry.Polygon) [][3]int {
	box := poly.Bounds()
	charLen := math.Hypot(box.MaxX-box.MinX, box.MaxY-box.MinY)
	if charLen > 200 {
		charLen = 200
	}
	minArea := math.Max(1e-9, charLen*charLen*1e-7)
	minEdge := math.Max(2e-3, charLen*5e-5)
	minAngle := 15.0 * math.Pi / 180.0 // reject slivers and needle-like triangles
	maxAspect := 4.0                   // reject extremely elongated triangles

	var filtered [][3]int
	dropped := 0
	for _, t := range triangles {
		a, b, c := points[t[0]], points[t[1]], points[t[2]]
		ab := math.Hypot(b.X-a.X, b.Y-a.Y)
		bc := math.Hypot(c.X-b.X, c.Y-b.Y)
		ca := math.Hypot(a.X-c.X, a.Y-c.Y)

		if ab < minEdge || bc < minEdge || ca < minEdge {
			dropped++
			continue
		}

		area := math.Abs((b.X-a.X)*(c.Y-a.Y) - (c.X-a.X)*(b.Y-a.Y))
		if area <= minArea {
			dropped++
			continue
		}
		cen := Point{X: (a.X + b.X + c.X) / 3, Y: (a.Y + b.Y + c.Y) / 3}
		if !pointInPolygon(cen, poly) {
			dropped++
			continue
		}
		minE := math.Min(ab, math.Min(bc, ca))
		maxE := math.Max(ab, math.Max(bc, ca))
		if maxE/minE > maxAspect {
			dropped++
			continue
		}
		if triMinAngle(points, t) < minAngle {
			dropped++
			continue
		}
		filtered = append(filtered, t)
	}
	if dropped > 0 {
		fmt.Printf("[PADEN mesh] dropped %d/%d invalid triangles\n", dropped, len(triangles))
	}
	return filtered
}

// EarcutFallback triangulates a polygon using the JS earcut library.
func EarcutFallback(poly geometry.Polygon) (*Mesh, error) {
	tri, err := Earcut(poly)
	if err != nil {
		return nil, err
	}
	valid := filterValidTriangles(tri.Vertices, tri.Triangles, poly)
	if len(valid) == 0 {
		return NewMesh(), nil
	}
	return FromTriangleSoup(tri.Vertices, valid), nil
}

// sort helpers for deterministic behavior
type pointSlice []Point

func (p pointSlice) Len() int      { return len(p) }
func (p pointSlice) Swap(i, j int) { p[i], p[j] = p[j], p[i] }
func (p pointSlice) Less(i, j int) bool {
	if p[i].X != p[j].X {
		return p[i].X < p[j].X
	}
	return p[i].Y < p[j].Y
}

// Ensure sort.Interface is implemented
var _ sort.Interface = pointSlice{}
