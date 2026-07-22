package geometry

// Point is a 2D point with floating-point coordinates.
type Point struct {
	X float64
	Y float64
}

// Vec2 returns the components as a slice for JS marshaling.
func (p Point) Vec2() []float64 {
	return []float64{p.X, p.Y}
}

// Pt is a convenience constructor.
func Pt(x, y float64) Point {
	return Point{X: x, Y: y}
}

// Box is an axis-aligned bounding box.
type Box struct {
	MinX float64
	MinY float64
	MaxX float64
	MaxY float64
}

// Extend grows the box to include p.
func (b *Box) Extend(p Point) {
	if p.X < b.MinX {
		b.MinX = p.X
	}
	if p.Y < b.MinY {
		b.MinY = p.Y
	}
	if p.X > b.MaxX {
		b.MaxX = p.X
	}
	if p.Y > b.MaxY {
		b.MaxY = p.Y
	}
}

// Center returns the box center.
func (b Box) Center() Point {
	return Point{X: (b.MinX + b.MaxX) / 2, Y: (b.MinY + b.MaxY) / 2}
}

// Width returns the box width.
func (b Box) Width() float64 {
	return b.MaxX - b.MinX
}

// Height returns the box height.
func (b Box) Height() float64 {
	return b.MaxY - b.MinY
}

// Area returns the box area.
func (b Box) Area() float64 {
	return b.Width() * b.Height()
}
