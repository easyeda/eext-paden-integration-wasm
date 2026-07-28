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
