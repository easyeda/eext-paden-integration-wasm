// Check mesh coverage: sample points on a regular grid and verify
// every sampled point is inside at least one triangle.
package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"

	"github.com/easyeda/eext-paden-integration/go-service/internal/mesh"
)

type triJSON struct {
	Vertices [3]int `json:"vertices"`
}

type meshJSON struct {
	Vertices  [][2]float64 `json:"vertices"`
	Triangles []triJSON    `json:"triangles"`
}

type layerSol struct {
	LayerName string     `json:"layer_name"`
	Meshes    []meshJSON `json:"meshes"`
}

type result struct {
	Success       bool       `json:"success"`
	LayerSolutions []layerSol `json:"layer_solutions"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: checkcover <input.json> [step]")
		os.Exit(1)
	}
	in := os.Args[1]
	step := 0.05
	if len(os.Args) >= 3 {
		fmt.Sscanf(os.Args[2], "%f", &step)
	}
	data, err := os.ReadFile(in)
	if err != nil {
		fmt.Println("read:", err)
		os.Exit(1)
	}
	var r result
	if err := json.Unmarshal(data, &r); err != nil {
		fmt.Println("parse:", err)
		os.Exit(1)
	}
	for _, l := range r.LayerSolutions {
		for mi, m := range l.Meshes {
			pts := make([]mesh.Point, len(m.Vertices))
			for j, v := range m.Vertices {
				pts[j] = mesh.Point{X: v[0], Y: v[1]}
			}
			tris := make([][3]int, len(m.Triangles))
			for j, t := range m.Triangles {
				tris[j] = t.Vertices
			}
			// Bbox
			xmin, xmax := math.Inf(1), math.Inf(-1)
			ymin, ymax := math.Inf(1), math.Inf(-1)
			for _, v := range pts {
				xmin = math.Min(xmin, v.X)
				xmax = math.Max(xmax, v.X)
				ymin = math.Min(ymin, v.Y)
				ymax = math.Max(ymax, v.Y)
			}
			// Sample grid
			total := 0
			covered := 0
			type uncoveredPt struct{ X, Y float64 }
			var uncovered []uncoveredPt
			for y := ymin; y <= ymax; y += step {
				for x := xmin; x <= xmax; x += step {
					total++
					if pointInAnyTri(pts, tris, mesh.Point{X: x, Y: y}) {
						covered++
					} else if len(uncovered) < 5 {
						uncovered = append(uncovered, uncoveredPt{x, y})
					}
				}
			}
			ratio := float64(covered) / float64(total)
			fmt.Printf("layer=%s mesh=%d bbox=[%.2f..%.2f,%.2f..%.2f] step=%.3f sampled=%d covered=%d ratio=%.4f\n",
				l.LayerName, mi, xmin, xmax, ymin, ymax, step, total, covered, ratio)
			for _, u := range uncovered {
				fmt.Printf("  uncovered: (%.3f, %.3f)\n", u.X, u.Y)
			}
		}
	}
}

func pointInTri(p, a, b, c mesh.Point) bool {
	d1 := sign(p, a, b)
	d2 := sign(p, b, c)
	d3 := sign(p, c, a)
	hasNeg := d1 < 0 || d2 < 0 || d3 < 0
	hasPos := d1 > 0 || d2 > 0 || d3 > 0
	return !(hasNeg && hasPos)
}

func sign(p1, p2, p3 mesh.Point) float64 {
	return (p1.X-p3.X)*(p2.Y-p3.Y) - (p2.X-p3.X)*(p1.Y-p3.Y)
}

func pointInAnyTri(pts []mesh.Point, tris [][3]int, p mesh.Point) bool {
	for _, t := range tris {
		if pointInTri(p, pts[t[0]], pts[t[1]], pts[t[2]]) {
			return true
		}
	}
	return false
}