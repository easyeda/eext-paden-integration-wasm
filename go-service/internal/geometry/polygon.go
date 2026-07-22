package geometry

import "math"

// Ring is a closed polygon ring. The first and last point are not required to
// repeat, but callers may store them either way.
type Ring []Point

// Polygon is a polygon with holes. The first ring is the exterior, subsequent
// rings are holes.
type Polygon []Ring

// MultiPolygon groups multiple polygons.
type MultiPolygon []Polygon

// Bounds returns the bounding box of all polygons in the multi-polygon.
func (mp MultiPolygon) Bounds() Box {
	if len(mp) == 0 || len(mp[0]) == 0 || len(mp[0][0]) == 0 {
		return Box{}
	}
	b := mp[0].Bounds()
	for i := 1; i < len(mp); i++ {
		bi := mp[i].Bounds()
		if bi.MinX < b.MinX {
			b.MinX = bi.MinX
		}
		if bi.MinY < b.MinY {
			b.MinY = bi.MinY
		}
		if bi.MaxX > b.MaxX {
			b.MaxX = bi.MaxX
		}
		if bi.MaxY > b.MaxY {
			b.MaxY = bi.MaxY
		}
	}
	return b
}

// Area returns the signed area of a ring using the shoelace formula.
func (r Ring) Area() float64 {
	n := len(r)
	if n < 3 {
		return 0
	}
	var a float64
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		a += r[i].X * r[j].Y
		a -= r[j].X * r[i].Y
	}
	return a / 2
}

// IsCCW reports whether the ring is counter-clockwise (positive area).
func (r Ring) IsCCW() bool {
	return r.Area() > 0
}

// Simplify reduces the number of vertices using the Douglas-Peucker algorithm.
// Closed rings (first point repeated at the end) preserve their closure.
func (r Ring) Simplify(tolerance float64) Ring {
	n := len(r)
	if n <= 2 {
		return append(Ring(nil), r...)
	}
	closed := r[0] == r[n-1]
	end := n - 1
	if !closed {
		end = n
	}
	indices := douglasPeucker(r, 0, end-1, tolerance)
	out := make(Ring, 0, len(indices))
	for _, i := range indices {
		out = append(out, r[i])
	}
	if closed && (len(out) == 0 || out[0] != out[len(out)-1]) {
		out = append(out, r[0])
	}
	return out
}

// Simplify applies Douglas-Peucker simplification to every ring.
func (p Polygon) Simplify(tolerance float64) Polygon {
	out := make(Polygon, len(p))
	for i, ring := range p {
		out[i] = ring.Simplify(tolerance)
	}
	return out
}

// Area returns the net area of the polygon (exterior minus holes).
func (p Polygon) Area() float64 {
	if len(p) == 0 {
		return 0
	}
	area := math.Abs(p[0].Area())
	for i := 1; i < len(p); i++ {
		area -= math.Abs(p[i].Area())
	}
	return area
}

func douglasPeucker(pts Ring, start, end int, tol float64) []int {
	if start > end {
		return nil
	}
	if start == end {
		return []int{start}
	}
	dmax := 0.0
	index := start
	for i := start + 1; i < end; i++ {
		d := perpendicularDistance(pts[i], pts[start], pts[end])
		if d > dmax {
			index = i
			dmax = d
		}
	}
	if dmax > tol {
		left := douglasPeucker(pts, start, index, tol)
		right := douglasPeucker(pts, index, end, tol)
		return append(left, right[1:]...)
	}
	return []int{start, end}
}

func perpendicularDistance(p, a, b Point) float64 {
	if a == b {
		return math.Hypot(p.X-a.X, p.Y-a.Y)
	}
	num := math.Abs((b.Y-a.Y)*p.X - (b.X-a.X)*p.Y + b.X*a.Y - b.Y*a.X)
	den := math.Hypot(b.X-a.X, b.Y-a.Y)
	return num / den
}

// Reverse reverses the ring in place.
func (r Ring) Reverse() {
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
}

// EnsureOrientation makes the exterior ring CCW and hole rings CW.
func (p Polygon) EnsureOrientation() {
	for i, ring := range p {
		isHole := i > 0
		ccw := ring.IsCCW()
		if isHole && ccw {
			ring.Reverse()
		}
		if !isHole && !ccw {
			ring.Reverse()
		}
	}
}

// Bounds returns the bounding box of all rings in the polygon.
func (p Polygon) Bounds() Box {
	if len(p) == 0 || len(p[0]) == 0 {
		return Box{}
	}
	b := Box{MinX: p[0][0].X, MinY: p[0][0].Y, MaxX: p[0][0].X, MaxY: p[0][0].Y}
	for _, ring := range p {
		for _, pt := range ring {
			b.Extend(pt)
		}
	}
	return b
}

// Flatten returns a flat float64 slice [x0,y0,x1,y1,...] for all rings.
func (r Ring) Flatten() []float64 {
	out := make([]float64, 0, len(r)*2)
	for _, p := range r {
		out = append(out, p.X, p.Y)
	}
	return out
}

// ToJS returns the ring as a JavaScript-compatible array of {x,y} objects.
func (r Ring) ToJS() []map[string]float64 {
	out := make([]map[string]float64, len(r))
	for i, p := range r {
		out[i] = map[string]float64{"x": p.X, "y": p.Y}
	}
	return out
}
