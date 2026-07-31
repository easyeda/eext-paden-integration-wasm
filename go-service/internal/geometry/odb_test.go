package geometry

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestParseODBSymbolRoundedRect(t *testing.T) {
	// ODB++ symbol dimensions are in 0.001 mm; parseODBSymbol divides by 1000.
	// Two common rounded-rectangle forms: rectWxHxR and rectWxHxrR.
	cases := []struct {
		name   string
		width  float64
		height float64
		r     float64
	}{
		{"rect2000x1000x200", 2.0, 1.0, 0.2},
		{"rect1524.0x3048.0xr152.4x1234", 1.524, 3.048, 0.1524},
	}
	for _, c := range cases {
		s := parseODBSymbol(c.name)
		if s.kind != "rect" || s.width != c.width || s.height != c.height || s.cornerRadius != c.r {
			t.Fatalf("unexpected symbol for %q: %+v", c.name, s)
		}
	}
}

func TestRoundedRectRingShape(t *testing.T) {
	// 4x4 square with 1 mm corner radius centered at origin.
	ring := roundedRectRing(Point{}, 4, 4, 1)
	if len(ring) == 0 {
		t.Fatal("empty ring")
	}
	// Check bounding box is approximately [-2,2]x[-2,2].
	var minX, maxX, minY, maxY float64
	for i, p := range ring {
		if i == 0 || p.X < minX {
			minX = p.X
		}
		if i == 0 || p.X > maxX {
			maxX = p.X
		}
		if i == 0 || p.Y < minY {
			minY = p.Y
		}
		if i == 0 || p.Y > maxY {
			maxY = p.Y
		}
	}
	if math.Abs(minX+2) > 1e-3 || math.Abs(maxX-2) > 1e-3 ||
		math.Abs(minY+2) > 1e-3 || math.Abs(maxY-2) > 1e-3 {
		t.Fatalf("bbox wrong: [%v,%v] x [%v,%v]", minX, maxX, minY, maxY)
	}
	// Count approximate vertices in each straight edge: 4 corners * 4 segs + 4 straight segments.
	if len(ring) < 20 {
		t.Fatalf("ring too small: %d vertices", len(ring))
	}
}

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

func TestParseODB3V3Area(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "test", "test-3.tgz"))
	if err != nil {
		t.Skipf("ODB++ fixture unavailable: %v", err)
	}
	parsed, err := ParseODB(data, []string{"Top Layer", "Bottom Layer"}, map[string]bool{"3V3": true})
	if err != nil {
		t.Fatalf("ParseODB: %v", err)
	}
	top, ok := parsed.Layers["Top Layer"]
	if !ok {
		t.Fatal("Top Layer missing")
	}
	var area float64
	for i, p := range top.Polygons {
		if top.NetLabels[i] == "3V3" {
			area += p.Area()
		}
	}
	t.Logf("Top Layer 3V3 polygons: %d, total area: %f", len(top.Polygons), area)
}
