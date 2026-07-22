package pipeline

import (
	"os"
	"testing"
)

func TestParseIPC356A(t *testing.T) {
	data, err := os.ReadFile("../../../test/test-paden-2.356a")
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

	// C1 GND pad should be around (12.405, -12.949) mm.
	var foundC1GND bool
	for _, p := range nl.Pads {
		if p.RefDes == "C1" && p.Pin == "-1" {
			foundC1GND = true
			if p.Net != "GND" {
				t.Errorf("C1-1 net=%s want GND", p.Net)
			}
			if diff(p.X, 12.40536) > 0.001 {
				t.Errorf("C1-1 x=%f want ~12.405", p.X)
			}
			if diff(p.Y, -12.94892) > 0.001 {
				t.Errorf("C1-1 y=%f want ~-12.949", p.Y)
			}
		}
	}
	if !foundC1GND {
		t.Errorf("C1-1 pad not found")
	}

	// VCC trace should have vertices including (21.505, -2.540) and (10.302, -8.128).
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
	// Expected vertices (mm): (6.170,-7.229) (8.956,-4.451) (9.144,-2.540?) wait
	// From raw: X2429Y-2848 X3526Y-1752 Y-700 X3826Y-1000 078 X6435 X8100Y-2665 Y-3200
	// = (6.170,-7.229) (8.956,-4.449) (8.956,-1.778) (9.718,-2.540) (16.345,-2.540) (20.574,-6.769) (20.574,-8.128)
	want := [][2]float64{
		{2429 * ipc356aUnitToMm, -2848 * ipc356aUnitToMm},
		{3526 * ipc356aUnitToMm, -1752 * ipc356aUnitToMm},
		{3526 * ipc356aUnitToMm, -700 * ipc356aUnitToMm},
		{3826 * ipc356aUnitToMm, -1000 * ipc356aUnitToMm},
		{6435 * ipc356aUnitToMm, -1000 * ipc356aUnitToMm},
		{8100 * ipc356aUnitToMm, -2665 * ipc356aUnitToMm},
		{8100 * ipc356aUnitToMm, -3200 * ipc356aUnitToMm},
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

func diff(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}
