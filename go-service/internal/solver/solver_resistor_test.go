package solver

import (
	"math"
	"testing"

	"github.com/easyeda/eext-paden-integration/go-service/internal/mesh"
	"github.com/easyeda/eext-paden-integration/go-service/internal/problem"
)

func TestStampResistorsAppendsConductance(t *testing.T) {
	a := problem.NewNodeID()
	b := problem.NewNodeID()
	net := &problem.Network{
		Elements: []problem.LumpedElement{
			&problem.Resistor{A: a, B: b, Resistance: 2},
		},
	}
	indexer := &nodeIndexer{
		nodeToGlobal: map[*problem.NodeID]int{a: 0, b: 1},
	}

	matrix := NewCSRFromTriplets(2, stampResistors(net, indexer, nil)).ToDense()
	want := [][]float64{{0.5, -0.5}, {-0.5, 0.5}}
	for i := range want {
		for j := range want[i] {
			if matrix[i][j] != want[i][j] {
				t.Fatalf("matrix[%d][%d] = %g, want %g", i, j, matrix[i][j], want[i][j])
			}
		}
	}
}

func TestCoarsenMeshConfigUsesNextAttempt(t *testing.T) {
	cfg := mesh.Config{MaximumSize: 2, MinimumAngle: 20}
	got := coarsenMeshConfig(cfg, 15354, 15000)
	wantSize := cfg.MaximumSize * math.Sqrt(15354.0/15000.0) * 1.2

	if math.Abs(got.MaximumSize-wantSize) > 1e-12 {
		t.Fatalf("MaximumSize = %g, want %g", got.MaximumSize, wantSize)
	}
	if got.MinimumAngle != 17 {
		t.Fatalf("MinimumAngle = %g, want 17", got.MinimumAngle)
	}
}
