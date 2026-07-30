// Show only the wedges (red triangles) for each mesh in test-3 result.
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

type ringInfo struct {
	face    *mesh.Face
	area    float64
	absArea float64
}

type island struct {
	ext   []mesh.Point
	holes [][]mesh.Point
}

func main() {
	if len(os.Args) < 3 {
		fmt.Println("usage: visualmesh <input.json> <output_prefix>")
		os.Exit(1)
	}
	in, prefix := os.Args[1], os.Args[2]
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
			meshObj := mesh.FromTriangleSoup(pts, tris)
			xmin, xmax := math.Inf(1), math.Inf(-1)
			ymin, ymax := math.Inf(1), math.Inf(-1)
			for _, v := range pts {
				xmin = math.Min(xmin, v.X)
				xmax = math.Max(xmax, v.X)
				ymin = math.Min(ymin, v.Y)
				ymax = math.Max(ymax, v.Y)
			}
			w := xmax - xmin
			h := ymax - ymin
			if w == 0 || h == 0 {
				continue
			}
			scale := 800.0 / math.Max(w, h)

			svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 820 820">
<rect width="100%%" height="100%%" fill="#1a1a1a"/>
`)

			allRings := []ringInfo{}
			for _, face := range meshObj.Boundary {
				vs := face.Vertices()
				lp := make([]mesh.Point, len(vs))
				for j, v := range vs {
					lp[j] = v.P
				}
				a := signedArea(lp)
				allRings = append(allRings, ringInfo{face: face, area: a, absArea: math.Abs(a)})
			}
			for i := 1; i < len(allRings); i++ {
				for j := i; j > 0 && allRings[j].absArea > allRings[j-1].absArea; j-- {
					allRings[j], allRings[j-1] = allRings[j-1], allRings[j]
				}
			}
			var islands []island
			for _, ri := range allRings {
				if ri.area >= 0 || ri.absArea < 0.5 {
					continue
				}
				vs := ri.face.Vertices()
				extPts := make([]mesh.Point, len(vs))
				for j, v := range vs {
					extPts[j] = v.P
				}
				islands = append(islands, island{ext: extPts})
			}
			for _, ri := range allRings {
				if ri.area <= 0 || ri.absArea < 0.5 {
					continue
				}
				vs := ri.face.Vertices()
				holePts := make([]mesh.Point, len(vs))
				for j, v := range vs {
					holePts[j] = v.P
				}
				var cx, cy float64
				for _, p := range holePts {
					cx += p.X
					cy += p.Y
				}
				cx /= float64(len(holePts))
				cy /= float64(len(holePts))
				for i := range islands {
					if pointInPolygon(mesh.Point{X: cx, Y: cy}, islands[i].ext) {
						islands[i].holes = append(islands[i].holes, holePts)
						break
					}
				}
			}
			// Draw all triangles as faint blue
			for _, t := range tris {
				a := pts[t[0]]
				b := pts[t[1]]
				c := pts[t[2]]
				ax := (a.X - xmin) * scale + 10
				ay := (ymax - a.Y) * scale + 10
				bx := (b.X - xmin) * scale + 10
				by := (ymax - b.Y) * scale + 10
				cx2 := (c.X - xmin) * scale + 10
				cy2 := (ymax - c.Y) * scale + 10
				svg += fmt.Sprintf(`<polygon points="%.1f,%.1f %.1f,%.1f %.1f,%.1f" fill="#223355" stroke="none" opacity="0.5"/>
`, ax, ay, bx, by, cx2, cy2)
			}
			// Draw wedges (outside any island) as red
			wedgeCount := 0
			for _, t := range tris {
				cx := (pts[t[0]].X + pts[t[1]].X + pts[t[2]].X) / 3
				cy := (pts[t[0]].Y + pts[t[1]].Y + pts[t[2]].Y) / 3
				inside := false
				if len(islands) > 0 {
					inside = pointInAnyIsland(mesh.Point{X: cx, Y: cy}, islands)
				}
				if !inside {
					a := pts[t[0]]
					b := pts[t[1]]
					c := pts[t[2]]
					ax := (a.X - xmin) * scale + 10
					ay := (ymax - a.Y) * scale + 10
					bx := (b.X - xmin) * scale + 10
					by := (ymax - b.Y) * scale + 10
					cx2 := (c.X - xmin) * scale + 10
					cy2 := (ymax - c.Y) * scale + 10
					svg += fmt.Sprintf(`<polygon points="%.1f,%.1f %.1f,%.1f %.1f,%.1f" fill="#ff2244" stroke="#ffaa66" stroke-width="0.3"/>
`, ax, ay, bx, by, cx2, cy2)
					wedgeCount++
				}
			}
			// Draw island outlines in green
			for _, isl := range islands {
				var ptsStr string
				for _, p := range isl.ext {
					px := (p.X - xmin) * scale + 10
					py := (ymax - p.Y) * scale + 10
					ptsStr += fmt.Sprintf("%.1f,%.1f ", px, py)
				}
				svg += fmt.Sprintf(`<polygon points="%s" fill="none" stroke="#22ff88" stroke-width="2" opacity="0.9"/>
`, ptsStr)
				for _, hole := range isl.holes {
					var holeStr string
					for _, p := range hole {
						px := (p.X - xmin) * scale + 10
						py := (ymax - p.Y) * scale + 10
						holeStr += fmt.Sprintf("%.1f,%.1f ", px, py)
					}
					svg += fmt.Sprintf(`<polygon points="%s" fill="none" stroke="#ffaa22" stroke-width="1.5" opacity="0.9"/>
`, holeStr)
				}
			}
			svg += "</svg>"
			path := fmt.Sprintf("%s.mesh%d.svg", prefix, mi)
			if err := os.WriteFile(path, []byte(svg), 0644); err != nil {
				fmt.Println("write:", err)
			}
			fmt.Printf("mesh=%d tris=%d wedges=%d islands=%d -> %s\n", mi, len(tris), wedgeCount, len(islands), path)
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

func pointInPolygon(p mesh.Point, poly []mesh.Point) bool {
	if len(poly) < 3 {
		return false
	}
	inside := false
	n := len(poly)
	j := n - 1
	for i := 0; i < n; i++ {
		xi, yi := poly[i].X, poly[i].Y
		xj, yj := poly[j].X, poly[j].Y
		intersect := ((yi > p.Y) != (yj > p.Y)) && (p.X < (xj-xi)*(p.Y-yi)/(yj-yi+1e-15)+xi)
		if intersect {
			inside = !inside
		}
		j = i
	}
	return inside
}

func pointInAnyIsland(p mesh.Point, islands []island) bool {
	for _, isl := range islands {
		if !pointInPolygon(p, isl.ext) {
			continue
		}
		inHole := false
		for _, hole := range isl.holes {
			if pointInPolygon(p, hole) {
				inHole = true
				break
			}
		}
		if !inHole {
			return true
		}
	}
	return false
}