// Package mesh provides half-edge mesh data structures and operators for FEM.
package mesh

import (
	"math"

	"github.com/easyeda/eext-paden-integration/go-service/internal/geometry"
)

// Point is a 2D point alias for geometry.Point.
type Point = geometry.Point

// Triangulation holds vertices and triangle indices from earcut.
type Triangulation struct {
	Vertices  []geometry.Point
	Triangles [][3]int
}

// Vector is a 2D vector.
type Vector struct {
	DX, DY float64
}

// Dot returns the dot product.
func (v Vector) Dot(o Vector) float64 { return v.DX*o.DX + v.DY*o.DY }

// Cross returns the 2D cross product.
func (v Vector) Cross(o Vector) float64 { return v.DX*o.DY - v.DY*o.DX }

// Vertex is a mesh vertex.
type Vertex struct {
	P   Point
	Out *HalfEdge
	Idx int
}

// HalfEdge is a directed edge.
type HalfEdge struct {
	Origin *Vertex
	Twin   *HalfEdge
	Next   *HalfEdge
	Prev   *HalfEdge
	Face   *Face
	Idx    int
}

// IsBoundary reports whether this edge is on a boundary face.
func (h *HalfEdge) IsBoundary() bool {
	return h.Face != nil && h.Face.IsBoundary
}

// Face is a triangular or boundary face.
type Face struct {
	Edge       *HalfEdge
	IsBoundary bool
	Idx        int
}

// Vertices returns the vertices of the face.
// The loop is bounded to avoid runaway growth if Next links are corrupted.
func (f *Face) Vertices() []*Vertex {
	var out []*Vertex
	if f.Edge == nil {
		return out
	}
	seen := make(map[*HalfEdge]bool)
	e := f.Edge
	for {
		if e == nil || seen[e] {
			break
		}
		seen[e] = true
		out = append(out, e.Origin)
		e = e.Next
		if e == f.Edge {
			break
		}
	}
	return out
}

// Centroid returns the face centroid.
func (f *Face) Centroid() Point {
	verts := f.Vertices()
	var x, y float64
	for _, v := range verts {
		x += v.P.X
		y += v.P.Y
	}
	n := float64(len(verts))
	return Point{X: x / n, Y: y / n}
}

// Area returns the face area.
func (f *Face) Area() float64 {
	verts := f.Vertices()
	if len(verts) < 3 {
		return 0
	}
	var a float64
	for i := 0; i < len(verts); i++ {
		j := (i + 1) % len(verts)
		a += verts[i].P.X*verts[j].P.Y - verts[j].P.X*verts[i].P.Y
	}
	return 0.5 * math.Abs(a)
}

// Mesh is a half-edge mesh.
type Mesh struct {
	Vertices  []*Vertex
	Edges     []*HalfEdge
	Faces     []*Face
	Boundary  []*Face
	Triangles [][3]int // direct triangle index list (defensive, for ToCompact)
	edgeMap   map[[2]int]*HalfEdge
}

// NewMesh creates an empty mesh.
func NewMesh() *Mesh {
	return &Mesh{
		edgeMap: make(map[[2]int]*HalfEdge),
	}
}

// AddVertex adds a vertex to the mesh.
func (m *Mesh) AddVertex(p Point) *Vertex {
	v := &Vertex{P: p, Idx: len(m.Vertices)}
	m.Vertices = append(m.Vertices, v)
	return v
}

// ConnectVertices adds a half-edge from v1 to v2, creating its twin.
func (m *Mesh) ConnectVertices(v1, v2 *Vertex) *HalfEdge {
	key := [2]int{v1.Idx, v2.Idx}
	if e, ok := m.edgeMap[key]; ok {
		return e
	}

	e12 := &HalfEdge{Origin: v1, Idx: len(m.Edges)}
	m.Edges = append(m.Edges, e12)
	e21 := &HalfEdge{Origin: v2, Idx: len(m.Edges)}
	m.Edges = append(m.Edges, e21)
	e12.Twin = e21
	e21.Twin = e12

	m.edgeMap[[2]int{v1.Idx, v2.Idx}] = e12
	m.edgeMap[[2]int{v2.Idx, v1.Idx}] = e21

	if v1.Out == nil {
		v1.Out = e12
	}
	if v2.Out == nil {
		v2.Out = e21
	}
	return e12
}

// AddFace adds a triangular face from three vertices.
func (m *Mesh) AddFace(v1, v2, v3 *Vertex) *Face {
	e1 := m.ConnectVertices(v1, v2)
	e2 := m.ConnectVertices(v2, v3)
	e3 := m.ConnectVertices(v3, v1)

	m.Triangles = append(m.Triangles, [3]int{v1.Idx, v2.Idx, v3.Idx})

	f := &Face{Idx: len(m.Faces)}
	m.Faces = append(m.Faces, f)

	e1.Next = e2
	e2.Next = e3
	e3.Next = e1
	e1.Prev = e3
	e2.Prev = e1
	e3.Prev = e2

	e1.Face = f
	e2.Face = f
	e3.Face = f
	f.Edge = e1
	return f
}

// FromTriangleSoup builds a half-edge mesh from a list of points and triangles.
func FromTriangleSoup(points []Point, triangles [][3]int) *Mesh {
	m := NewMesh()
	verts := make([]*Vertex, len(points))
	for i, p := range points {
		verts[i] = m.AddVertex(p)
	}

	for _, tri := range triangles {
		v0, v1, v2 := verts[tri[0]], verts[tri[1]], verts[tri[2]]
		area := math.Abs((v1.P.X-v0.P.X)*(v2.P.Y-v0.P.Y) - (v2.P.X-v0.P.X)*(v1.P.Y-v0.P.Y))
		if area < 1e-12 {
			continue
		}
		m.AddFace(v0, v1, v2)
	}

	m.BuildBoundaries()
	return m
}

// BuildBoundaries walks boundary edges and creates boundary faces.
func (m *Mesh) BuildBoundaries() {
	boundaryEdges := make(map[*HalfEdge]bool)
	for _, e := range m.Edges {
		if e.Face == nil {
			boundaryEdges[e] = true
		}
	}

	for len(boundaryEdges) > 0 {
		var start *HalfEdge
		for e := range boundaryEdges {
			start = e
			break
		}
		delete(boundaryEdges, start)

		bf := &Face{IsBoundary: true, Idx: len(m.Boundary)}
		m.Boundary = append(m.Boundary, bf)
		start.Face = bf
		bf.Edge = start

		cur := start
		for {
			// find next boundary edge from cur.Twin.Origin
			nextOrigin := cur.Twin.Origin
			var nextEdge *HalfEdge
			for e := range boundaryEdges {
				if e.Origin == nextOrigin {
					nextEdge = e
					break
				}
			}
			if nextEdge == nil {
				break
			}
			delete(boundaryEdges, nextEdge)
			cur.Next = nextEdge
			nextEdge.Prev = cur
			nextEdge.Face = bf
			cur = nextEdge
			if cur == start {
				break
			}
		}
		cur.Next = start
		start.Prev = cur
	}
}

// Orbit returns the outgoing edges from a vertex.
func (v *Vertex) Orbit() []*HalfEdge {
	var out []*HalfEdge
	if v.Out == nil {
		return out
	}
	e := v.Out
	for {
		out = append(out, e)
		if e.Twin == nil || e.Twin.Next == nil {
			break
		}
		e = e.Twin.Next
		if e == v.Out {
			break
		}
	}
	return out
}

// Cotan returns the cotangent weight for a half-edge.
func (h *HalfEdge) Cotan() float64 {
	if h.Twin == nil || h.Next == nil || h.Next.Next == nil {
		return 0
	}
	vi := h.Origin
	vk := h.Twin.Origin

	ratio := 0.0
	for _, other := range []*HalfEdge{h.Next.Next, h.Twin.Next.Next} {
		if other == nil || other.Next == nil || other.Next.Face == nil || other.Next.Face.IsBoundary {
			continue
		}
		vvi := Vector{DX: vi.P.X - other.Origin.P.X, DY: vi.P.Y - other.Origin.P.Y}
		vvk := Vector{DX: vk.P.X - other.Origin.P.X, DY: vk.P.Y - other.Origin.P.Y}
		cross := vvi.Cross(vvk)
		if math.Abs(cross) < 1e-12 {
			if cross >= 0 {
				cross = 1e-12
			} else {
				cross = -1e-12
			}
		}
		ratio += math.Abs(vvi.Dot(vvk)/cross) / 2
	}
	return ratio
}

// CompactMesh is a lightweight mesh representation.
type CompactMesh struct {
	VertexXY  [][2]float64
	Triangles [][3]int
}

// ToCompact converts the half-edge mesh to a compact representation.
func (m *Mesh) ToCompact() *CompactMesh {
	cm := &CompactMesh{
		VertexXY: make([][2]float64, len(m.Vertices)),
		Triangles: append([][3]int{}, m.Triangles...),
	}
	for i, v := range m.Vertices {
		cm.VertexXY[i] = [2]float64{v.P.X, v.P.Y}
	}
	return cm
}

// ExtractBoundaries extracts boundary rings from the compact mesh.
func (cm *CompactMesh) ExtractBoundaries() []map[string]interface{} {
	if len(cm.Triangles) == 0 {
		return nil
	}

	edgeCount := make(map[[2]int]int)
	addEdge := func(a, b int) {
		if a > b {
			a, b = b, a
		}
		edgeCount[[2]int{a, b}]++
	}
	for _, tri := range cm.Triangles {
		addEdge(tri[0], tri[1])
		addEdge(tri[1], tri[2])
		addEdge(tri[2], tri[0])
	}

	adj := make(map[int][]int)
	for e, c := range edgeCount {
		if c == 1 {
			adj[e[0]] = append(adj[e[0]], e[1])
			adj[e[1]] = append(adj[e[1]], e[0])
		}
	}

	visited := make(map[int]bool)
	var rings [][]int
	for start := range adj {
		if visited[start] {
			continue
		}
		ring := []int{start}
		visited[start] = true
		prev := -1
		cur := start
		for {
			nexts := adj[cur]
			var next int
			found := false
			for _, n := range nexts {
				if n != prev {
					next = n
					found = true
					break
				}
			}
			if !found || visited[next] {
				break
			}
			visited[next] = true
			ring = append(ring, next)
			prev, cur = cur, next
			if cur == start {
				break
			}
		}
		if len(ring) >= 3 {
			rings = append(rings, ring)
		}
	}

	var result []map[string]interface{}
	var exteriors [][]geometry.Point
	var holes [][]geometry.Point
	for _, ring := range rings {
		pts := make([]geometry.Point, len(ring))
		for i, idx := range ring {
			pts[i] = geometry.Point{X: cm.VertexXY[idx][0], Y: cm.VertexXY[idx][1]}
		}
		area := 0.0
		for i := 0; i < len(pts); i++ {
			j := (i + 1) % len(pts)
			area += pts[i].X*pts[j].Y - pts[j].X*pts[i].Y
		}
		if area > 0 {
			exteriors = append(exteriors, pts)
		} else {
			holes = append(holes, pts)
		}
	}

	for _, ext := range exteriors {
		result = append(result, map[string]interface{}{
			"exterior": ext,
			"holes":    holes,
		})
	}
	if len(exteriors) == 0 && len(holes) > 0 {
		for _, hole := range holes {
			result = append(result, map[string]interface{}{
				"exterior": hole,
				"holes":    [][]geometry.Point{},
			})
		}
	}
	return result
}
