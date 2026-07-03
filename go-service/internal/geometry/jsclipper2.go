package geometry

import (
	"fmt"
)

// Union returns the union of all polygons in a and b.
func Union(a, b MultiPolygon) (MultiPolygon, error) {
	var args []interface{}
	args = append(args, polygonsToJS(a))
	if len(b) > 0 {
		args = append(args, polygonsToJS(b))
	}
	result, err := Call("clipperUnion", args...)
	if err != nil {
		return nil, fmt.Errorf("clipper union failed: %w", err)
	}
	return polygonsFromJS(result)
}

// Difference returns subject minus clip.
func Difference(subject, clip MultiPolygon) (MultiPolygon, error) {
	result, err := Call("clipperDifference", polygonsToJS(subject), polygonsToJS(clip))
	if err != nil {
		return nil, fmt.Errorf("clipper difference failed: %w", err)
	}
	return polygonsFromJS(result)
}

// Intersect returns the intersection of a and b.
func Intersect(a, b MultiPolygon) (MultiPolygon, error) {
	result, err := Call("clipperIntersect", polygonsToJS(a), polygonsToJS(b))
	if err != nil {
		return nil, fmt.Errorf("clipper intersect failed: %w", err)
	}
	return polygonsFromJS(result)
}

// Offset inflates (delta > 0) or shrinks (delta < 0) polygons.
func Offset(mp MultiPolygon, delta float64) (MultiPolygon, error) {
	result, err := Call("clipperOffset", polygonsToJS(mp), delta)
	if err != nil {
		return nil, fmt.Errorf("clipper offset failed: %w", err)
	}
	return polygonsFromJS(result)
}
