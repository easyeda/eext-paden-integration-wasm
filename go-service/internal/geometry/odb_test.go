package geometry

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseODBAuthoritativeNetGeometry(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "test", "test-3.tgz"))
	if err != nil {
		t.Skipf("ODB++ fixture unavailable: %v", err)
	}
	parsed, err := ParseODB(data, []string{"Top Layer", "Bottom Layer"}, map[string]bool{"GND": true})
	if err != nil {
		t.Fatalf("ParseODB: %v", err)
	}
	for _, name := range []string{"Top Layer", "Bottom Layer"} {
		layer, ok := parsed.Layers[name]
		if !ok || len(layer.Polygons) == 0 {
			t.Fatalf("layer %q has no GND geometry", name)
		}
		if len(layer.Polygons) != len(layer.NetLabels) {
			t.Fatalf("layer %q polygons=%d labels=%d", name, len(layer.Polygons), len(layer.NetLabels))
		}
		for _, net := range layer.NetLabels {
			if net != "GND" {
				t.Fatalf("layer %q got non-authoritative net %q", name, net)
			}
		}
	}
	if len(parsed.Layers["board_outline_layer"].Polygons) == 0 {
		t.Fatal("board profile was not parsed")
	}
	if len(parsed.DrillHoles) == 0 || len(parsed.DrillPoints) == 0 {
		t.Fatal("drill geometry was not parsed")
	}
}
