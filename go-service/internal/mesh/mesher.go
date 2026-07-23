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
	Config Config
}

// NewMesher creates a mesher with the given config.
func NewMesher(cfg Config) *Mesher {
	return &Mesher{Config: cfg}
}

// PolygonToMesh triangulates a polygon with holes using a boundary-conforming
// earcut mesh followed by edge-split refinement and edge-flip quality
// improvement. Unlike the previous Delaunay+centroid-filter path, this never
// creates triangles that bridge concave boundaries or holes, so the rendered
// copper fill exactly matches the polygon outline.
func (m *Mesher) PolygonToMesh(poly geometry.Polygon, seedPoints []Point) (*Mesh, error) {
	if len(poly) == 0 || len(poly[0]) < 3 {
		return NewMesh(), nil
	}

	maxSize := m.Config.MaximumSize
	if maxSize <= 0 {
		maxSize = 1.2
	}

	// Very light simplification: remove only nearly duplicate points from the
	// high-resolution Gerber arc approximation while preserving shape.
	simplTol := math.Max(0.0005, maxSize*0.001)
	poly = poly.Simplify(simplTol)
	if len(poly) == 0 || len(poly[0]) < 3 {
		return NewMesh(), nil
	}

	// Primary path: boundary-conforming earcut.
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
	// applied at exact locations. Reject seed points that are too close to
	// existing vertices or edges — they create degenerate fan triangles.
	minSeedDist := math.Max(math.Min(maxSize*0.04, 0.08), 0.02)
	for _, sp := range seedPoints {
		if !pointInPolygon(sp, poly) {
			continue
		}
		inserted := false
		for ti := range tris {
			t := tris[ti]
			a, b, c := pts[t[0]], pts[t[1]], pts[t[2]]
			if pointInTriangle(sp, a, b, c) {
				// Skip if seed is too close to any vertex or edge — the fan
				// triangles would be degenerate spikes. The solver will snap the
				// connection to the nearest mesh vertex instead.
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

	// Refine long edges by splitting them. This preserves boundaries because
	// edges are only subdivided, never crossed.
	const maxVerts = 12000
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
		return math.Hypot(p.X-a.X, p.Y-a.Y)
	}
	if t > 1 {
		return math.Hypot(p.X-b.X, p.Y-b.Y)
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

// filterValidTriangles drops degenerate or hole-spanning triangles.
func filterValidTriangles(points []Point, triangles [][3]int, poly geometry.Polygon) [][3]int {
	// Compute a characteristic scale from the polygon to set a relative
	// minimum-area threshold. Cap the scale so very large boards do not
	// over-filter small-but-real copper features.
	box := poly.Bounds()
	charLen := math.Hypot(box.MaxX-box.MinX, box.MaxY-box.MinY)
	if charLen > 200 {
		charLen = 200
	}
	// Tighten thresholds relative to the previous version.  The absolute floors
	// keep small-but-real features (fine tracks/pads) while rejecting the
	// degenerate spike triangles that appear near inserted seed points.
	minArea := math.Max(1e-9, charLen*charLen*1e-7)
	minEdge := math.Max(2e-3, charLen*5e-5) // minimum edge length
	minAngle := 2.0 * math.Pi / 180.0       // reject needle-like spike triangles
	maxAspect := 25.0                       // reject extremely elongated triangles

	var filtered [][3]int
	dropped := 0
	for _, t := range triangles {
		a, b, c := points[t[0]], points[t[1]], points[t[2]]
		ab := math.Hypot(b.X-a.X, b.Y-a.Y)
		bc := math.Hypot(c.X-b.X, c.Y-b.Y)
		ca := math.Hypot(a.X-c.X, a.Y-c.Y)

		// Drop if any edge is shorter than the minimum (produces spike triangles).
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
		// Reject needle-like triangles (spikes) by minimum angle and aspect ratio.
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
