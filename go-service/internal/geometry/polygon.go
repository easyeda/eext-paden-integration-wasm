package geometry

// Ring is a closed polygon ring. The first and last point are not required to
// repeat, but callers may store them either way.
type Ring []Point

// Polygon is a polygon with holes. The first ring is the exterior, subsequent
// rings are holes.
type Polygon []Ring

// MultiPolygon groups multiple polygons.
type MultiPolygon []Polygon

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
