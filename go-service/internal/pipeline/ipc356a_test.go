package pipeline

import (
	"os"
	"testing"

	"github.com/easyeda/eext-paden-integration/go-service/internal/geometry"
	"github.com/easyeda/eext-paden-integration/go-service/internal/problem"
)

func TestParseIPC356A(t *testing.T) {
	data, err := os.ReadFile("../../../test/test-2.356a")
	if err != nil {
		t.Skipf("test 356a file not found: %v", err)
	}
	nl, err := ParseIPC356A(string(data))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(nl.Pads) == 0 {
		t.Fatalf("expected pads, got none")
	}
	if len(nl.Vias) == 0 {
		t.Fatalf("expected vias, got none")
	}
	if len(nl.Traces) == 0 {
		t.Fatalf("expected traces, got none")
	}
	t.Logf("pads=%d vias=%d traces=%d", len(nl.Pads), len(nl.Vias), len(nl.Traces))

	// IPC CUST 0 conversion: 1 IPC unit = 0.00254 mm.
	const cust0 = 0.00254

	// C1 GND pad: raw "PA01X 004884Y-005098X0585Y0680R000"
	// After fix: y center = 4884, x center = -5098 -> (-12.949, 12.405) mm.
	var foundC1GND bool
	for _, p := range nl.Pads {
		if p.RefDes == "C1" && p.Pin == "-1" {
			foundC1GND = true
			if p.Net != "GND" {
				t.Errorf("C1-1 net=%s want GND", p.Net)
			}
			if diff(p.X, -5098*cust0) > 0.001 {
				t.Errorf("C1-1 x=%f want ~-12.949", p.X)
			}
			if diff(p.Y, 4884*cust0) > 0.001 {
				t.Errorf("C1-1 y=%f want ~12.405", p.Y)
			}
		}
	}
	if !foundC1GND {
		t.Errorf("C1-1 pad not found")
	}

	// VCC trace should have the vertices from raw: X2429Y-2848 X3526Y-1752 Y-700
	// X3826Y-1000 078 X6435 X8100Y-2665 Y-3200
	var vccTrace *IPC356ATrace
	for i := range nl.Traces {
		if nl.Traces[i].Net == "VCC" && nl.Traces[i].Layer == "L01" {
			vccTrace = &nl.Traces[i]
			break
		}
	}
	if vccTrace == nil {
		t.Fatalf("VCC L01 trace not found")
	}
	if len(vccTrace.Vertices) < 4 {
		t.Fatalf("VCC L01 expected at least 4 vertices, got %d", len(vccTrace.Vertices))
	}
	// 378 traces use X-prefixed pairs, so x,y stay in document order.
	want := [][2]float64{
		{2429 * cust0, -2848 * cust0},
		{3526 * cust0, -1752 * cust0},
		{3526 * cust0, -700 * cust0},
		{3826 * cust0, -1000 * cust0},
		{6435 * cust0, -1000 * cust0},
		{8100 * cust0, -2665 * cust0},
		{8100 * cust0, -3200 * cust0},
	}
	for i, w := range want {
		if i >= len(vccTrace.Vertices) {
			break
		}
		v := vccTrace.Vertices[i]
		if diff(v.X, w[0]) > 0.001 || diff(v.Y, w[1]) > 0.001 {
			t.Errorf("VCC L01 vertex[%d] = %.4f,%.4f want %.4f,%.4f", i, v.X, v.Y, w[0], w[1])
		}
	}
}

func TestParseIPC356APUnits(t *testing.T) {
	cases := []struct {
		header string
		want   float64
	}{
		{"P UNITS CUST 0\n", 0.00254},
		{"P UNITS CUST 1\n", 0.001},
		{"P  UNITS  CUST 0\n", 0.00254},
		{"no units directive\n", 0.00254},
		{"P UNITS CUST 5\n", 0.00254},
	}
	for _, c := range cases {
		got, _ := parseIPC356APUnits(c.header)
		if diff(got, c.want) > 1e-9 {
			t.Errorf("parseIPC356APUnits(%q) = %v, want %v", c.header, got, c.want)
		}
	}
}

func TestComputeCoordinateTransformUsesSharedCoordinates(t *testing.T) {
	layer := &problem.Layer{
		Name: "Top Layer",
		Shape: geometry.MultiPolygon{
			geometry.Polygon{geometry.Ring{
				{X: 0, Y: -15},
				{X: 41, Y: -15},
				{X: 41, Y: 0},
				{X: 0, Y: 0},
			}},
		},
	}
	bounds := &Bounds{MinX: 5, MinY: -13, MaxX: 31, MaxY: -1}
	transform := computeCoordinateTransform(bounds, []*problem.Layer{layer}, Config{}, nil, &DiagCollector{})

	if transform == nil {
		t.Fatal("expected identity transform, got nil")
	}
	want := [4]float64{1, 1, 0, 0}
	if *transform != want {
		t.Fatalf("transform=%v want %v", *transform, want)
	}
}

func diff(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}
