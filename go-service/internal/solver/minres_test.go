package solver

import (
	"math"
	"testing"
)

func TestSolveMINRES_SPD(t *testing.T) {
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
	x, err := SolveMINRES(A, b, 100, 1e-10)
	if err != nil {
		t.Fatalf("MINRES failed: %v", err)
	}
	y := make([]float64, 3)
	A.Multiply(x, y)
	for i := range b {
		if math.Abs(y[i]-b[i]) > 1e-8 {
			t.Fatalf("residual too large at %d: got %g, want %g", i, y[i], b[i])
		}
	}
}

func TestSolveMINRES_Indefinite(t *testing.T) {
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
	x, err := SolveMINRES(A, b, 100, 1e-10)
	if err != nil {
		t.Fatalf("MINRES failed: %v", err)
	}
	y := make([]float64, 3)
	A.Multiply(x, y)
	for i := range b {
		if math.Abs(y[i]-b[i]) > 1e-8 {
			t.Fatalf("residual too large at %d: got %g, want %g", i, y[i], b[i])
		}
	}
	if math.Abs(x[0]-2.5) > 1e-6 || math.Abs(x[1]+2.5) > 1e-6 || math.Abs(x[2]-5) > 1e-6 {
		t.Fatalf("unexpected solution: %v", x)
	}
}
