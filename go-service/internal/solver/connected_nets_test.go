package solver

import (
	"testing"

	"github.com/easyeda/eext-paden-integration/go-service/internal/geometry"
	"github.com/easyeda/eext-paden-integration/go-service/internal/problem"
)

func ring(pts ...[2]float64) geometry.Ring {
	r := make(geometry.Ring, len(pts))
	for i, p := range pts {
		r[i] = geometry.Point{X: p[0], Y: p[1]}
	}
	return r
}

// buildIslandInPour models the layout that shorted 3V3 to GND: a small island
// sitting inside a hole of a large pour, separated by 0.02 in (0.5 mm) of
// clearance. That gap is under the 0.05 tolerance in pointHitsGeom, so the
// adjacency test reports the two polygons as touching and only the net labels
// can keep them apart.
//
// Shape[0] is the pour, Shape[1] is the island.
func buildIslandInPour(pourNet, islandNet string) *problem.Problem {
	pour := geometry.Polygon{
		ring([2]float64{0, 0}, [2]float64{2.0, 0}, [2]float64{2.0, 0.8}, [2]float64{0, 0.8}),
		ring([2]float64{0.98, 0.38}, [2]float64{1.12, 0.38}, [2]float64{1.12, 0.52}, [2]float64{0.98, 0.52}),
	}
	island := geometry.Polygon{
		ring([2]float64{1.00, 0.40}, [2]float64{1.10, 0.40}, [2]float64{1.10, 0.50}, [2]float64{1.00, 0.50}),
	}
	pour.EnsureOrientation()
	island.EnsureOrientation()

	layer := &problem.Layer{
		Shape:       geometry.MultiPolygon{pour, island},
		NetLabels:   []string{pourNet, islandNet},
		Name:        "Top Layer",
		Conductance: 2082.5,
	}

	// One connection landing inside the island, so the island's component is the
	// one reachable from the network.
	c1 := problem.NewConnection(layer, geometry.Point{X: 1.05, Y: 0.45}, islandNet)
	c2 := problem.NewConnection(layer, geometry.Point{X: 1.06, Y: 0.46}, islandNet)
	net := &problem.Network{
		Connections: []*problem.Connection{c1, c2},
		Elements: []problem.LumpedElement{
			&problem.Resistor{A: c1.NodeID, B: c2.NodeID, Resistance: 1},
		},
	}

	return &problem.Problem{Layers: []*problem.Layer{layer}, Networks: []*problem.Network{net}}
}

func TestFindConnectedPairsKeepsNetsApart(t *testing.T) {
	const (
		pourIdx   = 0
		islandIdx = 1
	)

	tests := []struct {
		name          string
		pourNet       string
		islandNet     string
		wantPourMerge bool
		why           string
	}{
		{
			name:          "different nets stay separate",
			pourNet:       "GND",
			islandNet:     "3V3",
			wantPourMerge: false,
			why:           "welding them shares FEM vertices and shorts the rails",
		},
		{
			name:          "same net still merges",
			pourNet:       "3V3",
			islandNet:     "3V3",
			wantPourMerge: true,
			why:           "fragmented same-net copper must still coalesce",
		},
		{
			name:          "unknown label falls back to geometry",
			pourNet:       "",
			islandNet:     "3V3",
			wantPourMerge: true,
			why:           "copper we cannot attribute must not be fragmented",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prob := buildIslandInPour(tc.pourNet, tc.islandNet)
			layerGeoms := [][]geometry.Polygon{prob.Layers[0].Shape}

			connected := findConnectedPairs(prob, layerGeoms)

			if !connected[[2]int{0, islandIdx}] {
				t.Fatalf("island holds the connection point but was not marked connected")
			}
			got := connected[[2]int{0, pourIdx}]
			if got != tc.wantPourMerge {
				t.Errorf("pour merged with island = %v, want %v: %s", got, tc.wantPourMerge, tc.why)
			}
		})
	}
}

// A terminal that falls outside every polygon must not be pulled onto a
// different net's copper just because that copper happens to be closer. Binding
// across nets makes both terminals share one FEM vertex, which is a short.
func TestNearestGeomOnLayerPrefersOwnNet(t *testing.T) {
	// geom 0: GND, near the probe point. geom 1: 3V3, further away.
	geoms := []geometry.Polygon{
		{ring([2]float64{0, 0}, [2]float64{0.1, 0}, [2]float64{0.1, 0.1}, [2]float64{0, 0.1})},
		{ring([2]float64{0.5, 0}, [2]float64{0.6, 0}, [2]float64{0.6, 0.1}, [2]float64{0.5, 0.1})},
	}
	labels := []string{"GND", "3V3"}
	probe := geometry.Point{X: 0.2, Y: 0.05} // 0.1 from GND, 0.3 from 3V3

	tests := []struct {
		name string
		net  string
		want int
		why  string
	}{
		{"own net wins over proximity", "3V3", 1, "binding to the nearer GND copper would short the rails"},
		{"own net when it is also nearest", "GND", 0, "no reason to look further"},
		{"unknown net falls back to nearest", "", 0, "cannot attribute, so keep the old geometric behaviour"},
		{"net absent from layer falls back", "1V1", 0, "dropping the terminal would disconnect the network"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := nearestGeomOnLayer(probe, 0, geoms, labels, tc.net); got != tc.want {
				t.Errorf("nearestGeomOnLayer(net=%q) = %d, want %d: %s", tc.net, got, tc.want, tc.why)
			}
		})
	}
}
