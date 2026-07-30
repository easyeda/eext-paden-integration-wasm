package mesh

import (
	"math"
	"testing"

	"github.com/easyeda/eext-paden-integration/go-service/internal/geometry"
)

func squareWithHole() geometry.Polygon {
	return geometry.Polygon{
		geometry.Ring{{0, 0}, {10, 0}, {10, 10}, {0, 10}},
		geometry.Ring{{3, 3}, {3, 7}, {7, 7}, {7, 3}},
	}
}

func TestFilterMeshSafetyKeepsValidSliver(t *testing.T) {
	points := []Point{{0, 0}, {10, 0}, {0, 0.01}}
	triangles := [][3]int{{0, 1, 2}}
	got := filterMeshSafety(points, triangles, geometry.Polygon{geometry.Ring{{-1, -1}, {11, -1}, {11, 1}, {-1, 1}}})
	if len(got) != 1 {
		t.Fatalf("kept %d triangles, want 1", len(got))
	}
}

func TestFilterMeshSafetyDropsUnsafeTriangles(t *testing.T) {
	points := []Point{{0, 0}, {1, 0}, {0, 1}, {math.NaN(), 0}}
	triangles := [][3]int{{0, 1, 2}, {0, 0, 1}, {0, 1, 3}}
	got := filterMeshSafety(points, triangles, geometry.Polygon{geometry.Ring{{-1, -1}, {2, -1}, {2, 2}, {-1, 2}}})
	if len(got) != 1 || got[0] != [3]int{0, 1, 2} {
		t.Fatalf("got triangles %#v, want only valid triangle", got)
	}
}

func TestFromTriangleSoupPreservesSquareCoverage(t *testing.T) {
	points := []Point{{0, 0}, {10, 0}, {10, 10}, {0, 10}}
	triangles := [][3]int{{0, 1, 2}, {0, 2, 3}}
	mesh := FromTriangleSoup(points, triangles)
	if len(mesh.Boundary) != 1 {
		t.Fatalf("boundary loops = %d, want 1", len(mesh.Boundary))
	}
	if got := math.Abs(mesh.Faces[0].Area()) + math.Abs(mesh.Faces[1].Area()); math.Abs(got-100) > 1e-9 {
		t.Fatalf("mesh area = %g, want 100", got)
	}
}

func TestFromTriangleSoupPreservesHoleBoundary(t *testing.T) {
	points := []Point{{0, 0}, {10, 0}, {10, 10}, {0, 10}, {3, 3}, {7, 3}, {7, 7}, {3, 7}}
	triangles := [][3]int{
		{0, 1, 5}, {0, 5, 4}, {1, 2, 6}, {1, 6, 5},
		{2, 3, 7}, {2, 7, 6}, {3, 0, 4}, {3, 4, 7},
	}
	mesh := FromTriangleSoup(points, triangles)
	if len(mesh.Boundary) != 2 {
		t.Fatalf("boundary loops = %d, want exterior plus hole", len(mesh.Boundary))
	}
}

// long-thin triangle whose centroid lies inside the poly and whose area is
// non-degenerate: safety filter must keep it. This guards against a regression
// to the previous behaviour that dropped any triangle with aspect > 4 — which
// left visible holes in otherwise-solid copper pours.
func TestFilterMeshSafetyKeepsLongThin(t *testing.T) {
	pts := []Point{{0, 0}, {10, 0}, {5, 0.005}}
	tris := [][3]int{{0, 1, 2}}
	poly := geometry.Polygon{geometry.Ring{{-1, -1}, {11, -1}, {11, 1}, {-1, 1}}}
	kept := filterMeshSafety(pts, tris, poly)
	if len(kept) != 1 {
		t.Fatalf("kept %d, want 1 (long thin but non-degenerate triangle)", len(kept))
	}
}

// Boundary regression: a thin-but-valid boundary edge triangle whose centroid
// sits on the polygon outline. The safety filter's centroid-containment check
// uses pointInPolygon, which can give true/false on the boundary depending on
// ordering; the filter must not reject the triangle either way.
func TestFilterMeshSafetyKeepsBoundaryEdgeSliver(t *testing.T) {
	pts := []Point{{0, 0}, {10, 0}, {5, 1e-4}}
	tris := [][3]int{{0, 1, 2}}
	poly := geometry.Polygon{geometry.Ring{{0, 0}, {10, 0}, {10, 10}, {0, 10}}}
	kept := filterMeshSafety(pts, tris, poly)
	if len(kept) != 1 {
		t.Fatalf("kept %d, want 1 (boundary sliver)", len(kept))
	}
}

// Triangle outside the polygon: must be dropped, regardless of shape.
func TestFilterMeshSafetyDropsOutsidePolygon(t *testing.T) {
	pts := []Point{{0, 0}, {1, 0}, {0, 1}, {50, 50}, {51, 50}, {50, 51}}
	tris := [][3]int{
		{0, 1, 2}, // inside
		{3, 4, 5}, // far outside
	}
	poly := geometry.Polygon{geometry.Ring{{-1, -1}, {2, -1}, {2, 2}, {-1, 2}}}
	kept := filterMeshSafety(pts, tris, poly)
	if len(kept) != 1 || kept[0] != [3]int{0, 1, 2} {
		t.Fatalf("kept %v, want only {{0,1,2}}", kept)
	}
}

// Repeated-index, NaN, Inf, and zero-area triangles are all unsafe and must
// be dropped. The single valid triangle in the soup is the only survivor.
func TestFilterMeshSafetyRejectsDegenerateEdges(t *testing.T) {
	pts := []Point{
		{0, 0}, {1, 0}, {0, 1}, {math.NaN(), 0}, {math.Inf(1), 0}, {0, math.NaN()},
	}
	tris := [][3]int{
		{0, 0, 1}, // repeated index
		{0, 1, 2}, // valid
		{0, 3, 1}, // NaN vertex
		{0, 4, 1}, // Inf vertex
		{5, 1, 2}, // NaN vertex
		{0, 2, 2}, // repeated index
		{0, 1, 1}, // repeated index
	}
	poly := geometry.Polygon{geometry.Ring{{-1, -1}, {2, -1}, {2, 2}, {-1, 2}}}
	kept := filterMeshSafety(pts, tris, poly)
	if len(kept) != 1 || kept[0] != [3]int{0, 1, 2} {
		t.Fatalf("kept %v, want only {0,1,2}", kept)
	}
}

// Zero-area triangle: three points that are collinear and produce a triangle
// whose 2*area is below minArea. Safety filter must drop it.
func TestFilterMeshSafetyDropsCollinearTriangle(t *testing.T) {
	pts := []Point{{0, 0}, {1, 0}, {2, 0}, {0, 1}}
	tris := [][3]int{
		{0, 1, 2}, // collinear → zero area
		{0, 1, 3}, // valid
	}
	poly := geometry.Polygon{geometry.Ring{{-1, -1}, {3, -1}, {3, 2}, {-1, 2}}}
	kept := filterMeshSafety(pts, tris, poly)
	if len(kept) != 1 || kept[0] != [3]int{0, 1, 3} {
		t.Fatalf("kept %v, want only {{0,1,3}}", kept)
	}
}

// Out-of-range index: the safety filter must not panic and must drop the
// bad triangle.
func TestFilterMeshSafetyDropsOutOfRangeIndex(t *testing.T) {
	pts := []Point{{0, 0}, {1, 0}, {0, 1}}
	tris := [][3]int{
		{0, 1, 2}, // valid
		{0, 1, 5}, // index 5 out of range
		{0, -1, 2}, // negative index
	}
	poly := geometry.Polygon{geometry.Ring{{-1, -1}, {2, -1}, {2, 2}, {-1, 2}}}
	kept := filterMeshSafety(pts, tris, poly)
	if len(kept) != 1 || kept[0] != [3]int{0, 1, 2} {
		t.Fatalf("kept %v, want only {{0,1,2}}", kept)
	}
}

// Verify that filterMeshSafety is idempotent: filtering an already-clean soup
// returns the same set. This is the contract test that ensures the filter is
// not introducing new boundary rings during normal PolygonToMesh passes.
func TestFilterMeshSafetyIdempotent(t *testing.T) {
	pts := []Point{
		{0, 0}, {1, 0}, {1, 1}, {0, 1}, {2, 0}, {2, 1},
	}
	tris := [][3]int{
		{0, 1, 2}, {0, 2, 3}, // first square
		{1, 4, 5}, {1, 5, 2}, // second square (shares edge 1-2)
	}
	poly := geometry.Polygon{geometry.Ring{{-1, -1}, {3, -1}, {3, 2}, {-1, 2}}}
	once := filterMeshSafety(pts, tris, poly)
	twice := filterMeshSafety(pts, once, poly)
	if len(once) != len(twice) {
		t.Fatalf("filter is not idempotent: once=%d twice=%d", len(once), len(twice))
	}
	for i := range once {
		if once[i] != twice[i] {
			t.Fatalf("filter changed a triangle on second pass: %v -> %v", once[i], twice[i])
		}
	}
}

// A complete, well-formed triangulation of a 10×10 square into 8 triangles
// (4×4 grid of squares → 8 triangles). After filtering, the mesh should
// still cover exactly 100 area units with a single outer boundary ring — no
// interior holes are introduced by the safety filter.
func TestFilterMeshSafetySquareGridTopology(t *testing.T) {
	pts := []Point{
		{0, 0}, {5, 0}, {10, 0},
		{0, 5}, {5, 5}, {10, 5},
		{0, 10}, {5, 10}, {10, 10},
	}
	// Eight triangles, oriented CCW. Each 5×5 square becomes two triangles.
	tris := [][3]int{
		{0, 1, 3}, {1, 4, 3}, // lower-left
		{1, 2, 4}, {2, 5, 4}, // lower-right
		{3, 4, 6}, {4, 7, 6}, // upper-left
		{4, 5, 7}, {5, 8, 7}, // upper-right
	}
	poly := geometry.Polygon{geometry.Ring{{0, 0}, {10, 0}, {10, 10}, {0, 10}}}
	kept := filterMeshSafety(pts, tris, poly)
	if len(kept) != 8 {
		t.Fatalf("kept %d triangles, want 8", len(kept))
	}
	mesh := FromTriangleSoup(pts, kept)
	if got := sumFaceAreas(mesh); math.Abs(got-100) > 1e-6 {
		t.Fatalf("mesh area = %g, want 100", got)
	}
	bounds := mesh.ToCompact().ExtractBoundaries()
	if len(bounds) != 1 {
		t.Fatalf("got %d boundaries, want 1 (single square, no phantom holes)", len(bounds))
	}
	holes, _ := bounds[0]["holes"].([][]geometry.Point)
	if len(holes) != 0 {
		t.Fatalf("full-area triangulation produced %d phantom holes — filter must not introduce them", len(holes))
	}
}

func sumFaceAreas(m *Mesh) float64 {
	total := 0.0
	for _, f := range m.Faces {
		total += math.Abs(f.Area())
	}
	return total
}

// cdtProbeCallsCDT verifies the bridge is reachable; if not (e.g. native
// `go test` builds where CDTTriangulate is a stub) the test is skipped so the
// rest of the suite still runs.
func cdtProbeCallsCDT(t *testing.T) {
	t.Helper()
	_, _, err := geometry.CDTTriangulate(geometry.Polygon{}, nil, 20, 1.5)
	if err != nil {
		t.Skipf("CDT bridge unavailable (run under WASM): %v", err)
	}
}

// TestPolygonToMeshCDTNoDuplicateVertices feeds a polygon whose exterior ring
// contains two vertices that differ only by 1e-4 mm (the residue left over
// from Gerber parser output). Shewchuk Triangle's exact-arithmetic jettison
// switch must collapse these into a single vertex at source — no Go-side
// dedupNearVertices pass is required anymore, and the resulting mesh must
// cover the polygon without introducing phantom interior holes.
func TestPolygonToMeshCDTNoDuplicateVertices(t *testing.T) {
	cdtProbeCallsCDT(t)
	// 10×10 square with a near-duplicate vertex pair: (10,0) is repeated
	// as (10+1e-4, 0+1e-4).
	eps := 1e-4
	poly := geometry.Polygon{
		geometry.Ring{
			{0, 0}, {10, 0},
			{10 + eps, eps},
			{10, 10}, {0, 10},
		},
	}
	mesher := NewMesher(Config{MinimumAngle: 20, MaximumSize: 1.2})
	m, err := mesher.PolygonToMesh(poly, nil)
	if err != nil {
		t.Fatalf("PolygonToMesh: %v", err)
	}
	if len(m.Faces) == 0 {
		t.Fatalf("expected non-empty mesh")
	}
	// Triangle's exact arithmetic must merge (10,0) with (10+1e-4, 1e-4) into
	// a single vertex; verify no two vertices lie within 1e-3 mm of each other.
	const tol = 1e-3
	for i := 0; i < len(m.Vertices); i++ {
		for j := i + 1; j < len(m.Vertices); j++ {
			a := m.Vertices[i].P
			b := m.Vertices[j].P
			if math.Hypot(a.X-b.X, a.Y-b.Y) < tol {
				t.Fatalf("vertices %d (%v) and %d (%v) are within %.4f mm — duplicate not removed", i, a, j, b, tol)
			}
		}
	}
}

// TestPolygonToMeshCDTMinAngleGuarantee feeds a thin elongated polygon that
// the old earcut pipeline rendered as long slivers, then asserts that the
// constrained-Delaunay output meets the requested minimum interior angle
// (within a 1° tolerance for floating-point rounding).
func TestPolygonToMeshCDTMinAngleGuarantee(t *testing.T) {
	cdtProbeCallsCDT(t)
	// 50×1 thin rectangle: earcut would produce very long, thin triangles.
	poly := geometry.Polygon{geometry.Ring{
		{0, 0}, {50, 0}, {50, 1}, {0, 1},
	}}
	mesher := NewMesher(Config{MinimumAngle: 20, MaximumSize: 1.2})
	m, err := mesher.PolygonToMesh(poly, nil)
	if err != nil {
		t.Fatalf("PolygonToMesh: %v", err)
	}
	if len(m.Faces) == 0 {
		t.Fatalf("expected non-empty mesh")
	}
	minAngle := math.Pi
	for _, f := range m.Faces {
		verts := f.Vertices()
		if len(verts) != 3 {
			continue
		}
		p0 := verts[0].P
		p1 := verts[1].P
		p2 := verts[2].P
		minA := triMinAngleTest(p0, p1, p2)
		if minA < minAngle {
			minAngle = minA
		}
	}
	minAngleDeg := minAngle * 180 / math.Pi
	const target = 20.0
	if minAngleDeg < target-1 {
		t.Fatalf("min angle %.2f° below target %.2f°", minAngleDeg, target)
	}
}

// TestPolygonToMeshCDTPreservesHoles feeds a polygon with a single interior
// hole and asserts that the resulting compact mesh's boundary extraction
// finds both the exterior and the hole loop.
func TestPolygonToMeshCDTPreservesHoles(t *testing.T) {
	cdtProbeCallsCDT(t)
	poly := geometry.Polygon{
		geometry.Ring{{0, 0}, {20, 0}, {20, 20}, {0, 20}}, // exterior
		geometry.Ring{{8, 8}, {12, 8}, {12, 12}, {8, 12}}, // hole
	}
	mesher := NewMesher(Config{MinimumAngle: 20, MaximumSize: 1.2})
	m, err := mesher.PolygonToMesh(poly, nil)
	if err != nil {
		t.Fatalf("PolygonToMesh: %v", err)
	}
	if len(m.Faces) == 0 {
		t.Fatalf("expected non-empty mesh")
	}
	bounds := m.ToCompact().ExtractBoundaries()
	if len(bounds) < 2 {
		t.Fatalf("got %d boundary loops, want >= 2 (exterior + hole)", len(bounds))
	}
	holes, _ := bounds[0]["holes"].([][]geometry.Point)
	if len(holes) != 1 {
		t.Fatalf("got %d holes, want 1", len(holes))
	}
}

// triMinAngleTest returns the minimum interior angle (radians) of the
// triangle (p0, p1, p2). Local helper because the production triMinAngle
// in the legacy path was removed when PolygonToMesh switched to CDT.
func triMinAngleTest(p0, p1, p2 geometry.Point) float64 {
	a := math.Hypot(p1.X-p0.X, p1.Y-p0.Y)
	b := math.Hypot(p2.X-p1.X, p2.Y-p1.Y)
	c := math.Hypot(p0.X-p2.X, p0.Y-p2.Y)
	if a < 1e-12 || b < 1e-12 || c < 1e-12 {
		return 0
	}
	ang0 := math.Acos(clampTest((a*a+c*c-b*b)/(2*a*c), -1, 1))
	ang1 := math.Acos(clampTest((a*a+b*b-c*c)/(2*a*b), -1, 1))
	ang2 := math.Pi - ang0 - ang1
	return math.Min(ang0, math.Min(ang1, ang2))
}

func clampTest(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
