// Standalone mesh boundary check for test-3 results.
// Reads wasm_test3_result.json and reports boundary loop count + area per mesh.
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
	data, err := os.ReadFile("../test/wasm_test3_result.json")
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
		for i, m := range l.Meshes {
			pts := make([]mesh.Point, len(m.Vertices))
			for j, v := range m.Vertices {
				pts[j] = mesh.Point{X: v[0], Y: v[1]}
			}
			tris := make([][3]int, len(m.Triangles))
			for j, t := range m.Triangles {
				tris[j] = t.Vertices
			}
			meshObj := mesh.FromTriangleSoup(pts, tris)
			loops := meshObj.Boundary
			fmt.Printf("\n--- mesh=%d all boundary rings ---\n", i)
			for fi, face := range loops {
				vs := face.Vertices()
				if len(vs) < 3 {
					fmt.Printf("  ring[%d] degenerate (%d verts)\n", fi, len(vs))
					continue
				}
				lp := make([]mesh.Point, len(vs))
				for j, v := range vs {
					lp[j] = v.P
				}
				a := signedArea(lp)
				fmt.Printf("  ring[%d] verts=%d signedArea=%.4f\n", fi, len(vs), a)
			}
			var minxAll, minyAll, maxxAll, maxyAll float64
			allFirst := true
			for _, v := range pts {
				if allFirst || v.X < minxAll {
					minxAll = v.X
				}
				if allFirst || v.X > maxxAll {
					maxxAll = v.X
				}
				if allFirst || v.Y < minyAll {
					minyAll = v.Y
				}
				if allFirst || v.Y > maxyAll {
					maxyAll = v.Y
				}
				allFirst = false
			}
			bboxArea := (maxxAll - minxAll) * (maxyAll - minyAll)
			var extArea, totalLoopArea, holeArea float64
			holeCount := 0
			var extBbox [4]float64
			first := true
			for _, face := range loops {
				vs := face.Vertices()
				if len(vs) < 3 {
					continue
				}
				lp := make([]mesh.Point, len(vs))
				var minx, miny, maxx, maxy float64
				for j, v := range vs {
					lp[j] = v.P
					if first || v.P.X < minx {
						minx = v.P.X
					}
					if first || v.P.X > maxx {
						maxx = v.P.X
					}
					if first || v.P.Y < miny {
						miny = v.P.Y
					}
					if first || v.P.Y > maxy {
						maxy = v.P.Y
					}
				}
				first = false
				a := signedArea(lp)
				area := math.Abs(a)
				totalLoopArea += area
				if a > 0 {
					extArea += area
					extBbox = [4]float64{minx, miny, maxx, maxy}
				} else {
					holeArea += area
					holeCount++
				}
			}
			extBoxArea := (extBbox[2] - extBbox[0]) * (extBbox[3] - extBbox[1])
			var triArea float64
			for _, t := range tris {
				a := pts[t[0]]
				b := pts[t[1]]
				c := pts[t[2]]
				triArea += math.Abs((b.X-a.X)*(c.Y-a.Y)-(c.X-a.X)*(b.Y-a.Y)) * 0.5
			}
			ratio := triArea / math.Max(totalLoopArea, 1e-12)
			fmt.Printf("layer=%s mesh=%d verts=%d tris=%d loops=%d(outer=%.4f,holes=%d*%.4f) triArea=%.4f bbox=%.4f extBbox=%.4f ratio=%.3f\n",
				l.LayerName, i, len(m.Vertices), len(m.Triangles), len(loops), extArea, holeCount, holeArea, triArea, bboxArea, extBoxArea, ratio)
		}
	}
}

func signedArea(loop []mesh.Point) float64 {
	var s float64
	n := len(loop)
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		s += loop[i].X*loop[j].Y - loop[j].X*loop[i].Y
	}
	return s * 0.5
}