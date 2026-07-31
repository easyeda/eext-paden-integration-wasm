package mesh

import (
	"fmt"
	"math"

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
	MaxVertices int // per-polygon cap (unused by the CDT path; kept for back-compat)
}

// NewMesher creates a mesher with the given config.
func NewMesher(cfg Config) *Mesher {
	return &Mesher{Config: cfg}
}

// PolygonToMesh triangulates a polygon with holes via Shewchuk Triangle (a
// constrained Delaunay triangulator compiled to WASM and exposed through the
// geometry bridge). Triangle's exact arithmetic + -j (jettison) switch drops
// any coincident input vertices at source, so no Go-side dedup pass is
// required. The -q switch enforces the minimum interior angle, eliminating
// the sliver faces that the previous earcut + custom refinement pipeline
// produced.
func (m *Mesher) PolygonToMesh(poly geometry.Polygon, seedPoints []Point) (*Mesh, error) {
	if len(poly) == 0 || len(poly[0]) < 3 {
		return NewMesh(), nil
	}

	maxSize := m.Config.MaximumSize
	if maxSize <= 0 {
		maxSize = 1.2
	}
	minAngle := m.Config.MinimumAngle
	if minAngle <= 0 {
		minAngle = 20.0
	}

	// Light boundary simplification. Triangle tolerates dense polygon outlines
	// but the JS bridge's circle discretisation can produce hundreds of points
	// per Gerber arc; dropping those to ~0.05 mm chord keeps Triangle's PSLG
	// step fast without altering the visible outline.
	simplTol := math.Max(0.05, maxSize*0.05)
	poly = poly.Simplify(simplTol)
	if len(poly) == 0 || len(poly[0]) < 3 {
		return NewMesh(), nil
	}

	// Filter seed points to those that lie strictly inside the polygon.
	// Triangle's exact arithmetic still produces the boundary vertices
	// itself, so seeds that fall on a segment will not introduce duplicates.
	seeds := make([]Point, 0, len(seedPoints))
	for _, sp := range seedPoints {
		if pointInPolygon(sp, poly) {
			seeds = append(seeds, sp)
		}
	}

	pts, tris, err := geometry.CDTTriangulate(poly, seeds, minAngle, maxSize*maxSize)
	if err != nil {
		return nil, err
	}
	if len(tris) == 0 {
		return NewMesh(), nil
	}

	// Safety filter: drop degenerate / NaN / out-of-polygon triangles that
	// could pollute the FEM matrix. Triangle's quality bounds keep the
	// filtered set very small in practice.
	tris = filterMeshSafety(pts, tris, poly)
	if len(tris) == 0 {
		return NewMesh(), nil
	}
	return FromTriangleSoup(pts, tris), nil
}

// minEdgeLen rejects triangles whose vertices are effectively coincident.
// Anything below this threshold is the result of a polygon outline that the
// triangulator could not resolve cleanly (e.g. earcut on a PSLG with
// near-duplicate boundary points). 1 µm is well below CDT's min-edge in
// practice, so this never trims a useful face.
const minEdgeLen = 1e-3

// filterMeshSafety removes only triangles that cannot safely participate in the
// FEM mesh. Quality problems are handled by Triangle's -q / -a switches; the
// safety filter only rejects degenerate inputs (NaN/Inf vertices, zero-area
// triangles, repeated indices, near-coincident vertices, and triangles outside
// the polygon outline).
func filterMeshSafety(points []Point, triangles [][3]int, poly geometry.Polygon) [][3]int {
	box := poly.Bounds()
	charLen := math.Hypot(box.MaxX-box.MinX, box.MaxY-box.MinY)
	minArea := math.Max(1e-12, charLen*charLen*1e-12)
	filtered := make([][3]int, 0, len(triangles))
	dropped := 0
	shortEdge := 0
	for _, t := range triangles {
		if t[0] < 0 || t[1] < 0 || t[2] < 0 || t[0] >= len(points) || t[1] >= len(points) || t[2] >= len(points) ||
			t[0] == t[1] || t[1] == t[2] || t[2] == t[0] {
			dropped++
			continue
		}
		a, b, c := points[t[0]], points[t[1]], points[t[2]]
		if !finitePoint(a) || !finitePoint(b) || !finitePoint(c) {
			dropped++
			continue
		}
		if math.Hypot(b.X-a.X, b.Y-a.Y) < minEdgeLen ||
			math.Hypot(c.X-b.X, c.Y-b.Y) < minEdgeLen ||
			math.Hypot(a.X-c.X, a.Y-c.Y) < minEdgeLen {
			shortEdge++
			continue
		}
		area2 := math.Abs((b.X-a.X)*(c.Y-a.Y) - (c.X-a.X)*(b.Y-a.Y))
		if area2 <= minArea || !pointInPolygon(centroid(a, b, c), poly) {
			dropped++
			continue
		}
		filtered = append(filtered, t)
	}
	if dropped+shortEdge > 0 {
		fmt.Printf("[PADEN mesh] dropped %d/%d unsafe triangles (short_edge=%d)\n", dropped+shortEdge, len(triangles), shortEdge)
	}
	return filtered
}

func finitePoint(p Point) bool {
	return !math.IsNaN(p.X) && !math.IsNaN(p.Y) && !math.IsInf(p.X, 0) && !math.IsInf(p.Y, 0)
}

// EarcutFallback triangulates a polygon using the JS earcut library. It is
// retained for solver-side fallback paths but is no longer used by the main
// PolygonToMesh pipeline.
func EarcutFallback(poly geometry.Polygon) (*Mesh, error) {
	tri, err := Earcut(poly)
	if err != nil {
		return nil, err
	}
	valid := filterMeshSafety(tri.Vertices, tri.Triangles, poly)
	if len(valid) == 0 {
		return NewMesh(), nil
	}
	return FromTriangleSoup(tri.Vertices, valid), nil
}

func pointInPolygon(p Point, poly geometry.Polygon) bool {
	if len(poly) == 0 {
		return false
	}
	if !pointInRing(p, poly[0]) {
		return false
	}
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

func centroid(a, b, c Point) Point {
	return Point{X: (a.X + b.X + c.X) / 3, Y: (a.Y + b.Y + c.Y) / 3}
}