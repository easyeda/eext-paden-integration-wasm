package mesh

import (
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

// PolygonToMesh triangulates a polygon with holes.
func (m *Mesher) PolygonToMesh(poly geometry.Polygon, seedPoints []Point) (*Mesh, error) {
	if len(poly) == 0 || len(poly[0]) < 3 {
		return NewMesh(), nil
	}

	// Generate points: boundary + interior adaptive grid + seeds
	pts := m.generatePoints(poly, seedPoints)
	if len(pts) < 3 {
		return NewMesh(), nil
	}

	// Very dense point clouds make our pure-Go Delaunay too slow in WASM.
	// Fall back to earcut (JS) for these cases; it uses polygon vertices only.
	if len(pts) > 12000 {
		return EarcutFallback(poly)
	}

	// Delaunay triangulation
	tris := delaunayTriangulate(pts)

	// Filter triangles to those inside the polygon
	filtered := filterTrianglesInsidePolygon(pts, tris, poly)
	if len(filtered) == 0 {
		return NewMesh(), nil
	}

	mesh := FromTriangleSoup(pts, filtered)

	// Refine thin triangles
	if m.Config.MinimumAngle > 0 {
		mesh = m.refineMesh(mesh, poly)
	}

	return mesh, nil
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
		key := [2]float64{round(p.X, 4), round(p.Y, 4)}
		pts[key] = true
	}

	densifyRing := func(ring geometry.Ring) {
		n := len(ring)
		if ring[0] == ring[n-1] {
			n--
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
	if length <= maxSize*1.5 || length < 1e-9 {
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

func round(v float64, decimals int) float64 {
	pow := math.Pow(10, float64(decimals))
	return math.Round(v*pow) / pow
}

// EarcutFallback triangulates a polygon using the JS earcut library.
func EarcutFallback(poly geometry.Polygon) (*Mesh, error) {
	tri, err := Earcut(poly)
	if err != nil {
		return nil, err
	}
	return FromTriangleSoup(tri.Vertices, tri.Triangles), nil
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
