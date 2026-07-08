package solver

import (
	"math"
	"testing"
)

func TestSolveGMRES_SPD(t *testing.T) {
	// Simple 3x3 SPD system.
	triplets := []Triplet{
		{Row: 0, Col: 0, Val: 4},
		{Row: 0, Col: 1, Val: -1},
		{Row: 1, Col: 0, Val: -1},
		{Row: 1, Col: 1, Val: 4},
		{Row: 1, Col: 2, Val: -1},
		{Row: 2, Col: 1, Val: -1},
		{Row: 2, Col: 2, Val: 4},
	}
	A := NewCSRFromTriplets(3, triplets)
	b := []float64{1, 2, 3}
	x, err := SolveGMRES(A, b, 10, 100, 1e-10, nil)
	if err != nil {
		t.Fatalf("GMRES failed: %v", err)
	}
	y := make([]float64, 3)
	A.Multiply(x, y)
	for i := range b {
		if math.Abs(y[i]-b[i]) > 1e-8 {
			t.Fatalf("residual too large at %d: got %g, want %g", i, y[i], b[i])
		}
	}
}

func TestSolveGMRES_Indefinite(t *testing.T) {
	// Small saddle-point system similar to MNA:
	// [ -2  0  1 ] [v1]   [0]
	// [  0 -2 -1 ] [v2] = [0]
	// [  1 -1  0 ] [i ]   [5]
	triplets := []Triplet{
		{Row: 0, Col: 0, Val: -2},
		{Row: 0, Col: 2, Val: 1},
		{Row: 1, Col: 1, Val: -2},
		{Row: 1, Col: 2, Val: -1},
		{Row: 2, Col: 0, Val: 1},
		{Row: 2, Col: 1, Val: -1},
	}
	A := NewCSRFromTriplets(3, triplets)
	b := []float64{0, 0, 5}
	x, err := SolveGMRES(A, b, 10, 100, 1e-10, nil)
	if err != nil {
		t.Fatalf("GMRES failed: %v", err)
	}
	y := make([]float64, 3)
	A.Multiply(x, y)
	for i := range b {
		if math.Abs(y[i]-b[i]) > 1e-8 {
			t.Fatalf("residual too large at %d: got %g, want %g", i, y[i], b[i])
		}
	}
	// Check solution: v1 - v2 = 5, -2*v1 + i = 0, -2*v2 - i = 0
	// => v1 = 2.5, v2 = -2.5, i = 5
	if math.Abs(x[0]-2.5) > 1e-6 || math.Abs(x[1]+2.5) > 1e-6 || math.Abs(x[2]-5) > 1e-6 {
		t.Fatalf("unexpected solution: %v", x)
	}
}
