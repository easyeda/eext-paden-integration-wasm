package geometry

import (
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
)

func TestPadOverlay(t *testing.T) {
	b, err := os.ReadFile("../../../test/test-3.tgz")
	if err != nil {
		t.Fatal(err)
	}
	data, err := ParseODB(b, []string{"top_layer"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	layer := data.AllLayers["top_layer"]
	_ = layer
	// Write SVG overlay for all polygons from the top layer
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" version="1.1" viewBox="0 0 55 25" width="55mm" height="25mm" style="background:#222">
`)
	for _, poly := range layer.Polygons {
		for ri, ring := range poly {
			if len(ring) == 0 {
				continue
			}
			var d []string
			for j, p := range ring {
				cmd := "L"
				if j == 0 {
					cmd = "M"
				}
				d = append(d, fmt.Sprintf("%s%.6f %.6f", cmd, p.X, -p.Y))
			}
			d = append(d, "Z")
			draw := strings.Join(d, "")
			fill := "none"
			stroke := "red"
			if ri == 0 {
				fill = "rgba(255,0,0,0.2)"
			}
			sb.WriteString(fmt.Sprintf(`<path d="%s" fill="%s" stroke="%s" stroke-width="0.02"/>`+"\n", draw, fill, stroke))
		}
	}
	_ = math.Pi
	sb.WriteString("</svg>\n")
	// dump the polygon closest to the sample pad
	sample := Point{X: 24.257, Y: 14.6034887}
	bestDist := math.MaxFloat64
	var best Ring
	for _, poly := range layer.Polygons {
		for _, ring := range poly {
			var cx, cy float64
			for _, p := range ring {
				cx += p.X
				cy += p.Y
			}
			n := float64(len(ring))
			dx, dy := cx/n-sample.X, cy/n-sample.Y
			if dist := dx*dx + dy*dy; dist < bestDist {
				bestDist = dist
				best = ring
			}
		}
	}
	var pts []string
	for _, p := range best {
		pts = append(pts, fmt.Sprintf("(%.6f, %.6f)", p.X, p.Y))
	}
	t.Log("sample nearest ring:", strings.Join(pts, " "))
	if err := os.WriteFile("../../../test/pad_overlay.svg", []byte(sb.String()), 0644); err != nil {
		t.Fatal(err)
	}
	t.Log("wrote pad_overlay.svg")
}
