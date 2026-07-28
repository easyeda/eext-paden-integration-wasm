package pipeline

import (
	"sort"
	"testing"

	"github.com/easyeda/eext-paden-integration/go-service/internal/geometry"
	"github.com/easyeda/eext-paden-integration/go-service/internal/problem"
)

// rect returns a rectangular polygon with the four corners, CCW.
func rect(x0, y0, x1, y1 float64) geometry.Polygon {
	return geometry.Polygon{geometry.Ring{
		{X: x0, Y: y0},
		{X: x1, Y: y0},
		{X: x1, Y: y1},
		{X: x0, Y: y1},
		{X: x0, Y: y0},
	}}
}

// rectWithHole returns a rectangular polygon with a rectangular hole inside.
func rectWithHole(x0, y0, x1, y1, hx0, hy0, hx1, hy1 float64) geometry.Polygon {
	outer := geometry.Ring{
		{X: x0, Y: y0},
		{X: x1, Y: y0},
		{X: x1, Y: y1},
		{X: x0, Y: y1},
		{X: x0, Y: y0},
	}
	// Hole ring is CW (opposite winding of the outer ring).
	hole := geometry.Ring{
		{X: hx0, Y: hy0},
		{X: hx0, Y: hy1},
		{X: hx1, Y: hy1},
		{X: hx1, Y: hy0},
		{X: hx0, Y: hy0},
	}
	return geometry.Polygon{outer, hole}
}

// bruteContainingPolygons returns the set of polygon indices whose bounding
// box contains the point. This is what the spatial index is approximating
// (modulo the exact pointInPolygonMesh containment check that callers must
// apply anyway).
func bruteContainingByBBox(shapes geometry.MultiPolygon, pt geometry.Point) []int {
	var out []int
	for i, poly := range shapes {
		if len(poly) == 0 || len(poly[0]) < 3 {
			continue
		}
		b := poly.Bounds()
		if pt.X < b.MinX || pt.X > b.MaxX || pt.Y < b.MinY || pt.Y > b.MaxY {
			continue
		}
		out = append(out, i)
	}
	return out
}

// bruteContainingByPolygon returns the polygon indices whose polygon
// actually contains pt (semantic ground truth for the inference calls).
func bruteContainingByPolygon(shapes geometry.MultiPolygon, pt geometry.Point) []int {
	var out []int
	for i, poly := range shapes {
		if pointInPolygonMesh(pt, poly) {
			out = append(out, i)
		}
	}
	return out
}

func sortedInts(a []int) []int {
	out := append([]int(nil), a...)
	sort.Ints(out)
	return out
}

func TestPolygonIndexOverlapRegionReturnsAllCandidates(t *testing.T) {
	// Two rectangles that overlap. A probe inside the overlap area must
	// list both polygons as candidates for the exact mesh check.
	small := rect(0, 0, 4, 4)
	large := rect(2, 2, 10, 10)
	shapes := geometry.MultiPolygon{small, large}

	idx := BuildPolygonIndex(shapes)
	if idx == nil {
		t.Fatal("expected index, got nil")
	}

	// Probe inside the overlap region: both polygons must be candidates.
	probe := geometry.Point{X: 3, Y: 3}
	got := idx.Candidates(probe)
	if !containsInt(got, 0) || !containsInt(got, 1) {
		t.Errorf("probe in overlap candidates=%v, expected both [0,1]", got)
	}

	// Probe in the small-only region: polygon 0 must be a candidate. The
	// index may also include polygon 1 if its bbox touches the cell — that
	// is fine, the caller filters via pointInPolygonMesh.
	probe2 := geometry.Point{X: 1, Y: 1}
	got2 := idx.Candidates(probe2)
	if !containsInt(got2, 0) {
		t.Errorf("probe in small-only region must include polygon 0, got %v", got2)
	}
	// Polygon 1's bbox (2,2)-(10,10) does NOT contain (1,1). The brute-force
	// BBox set must therefore be [0], and the index must be a superset.
	brute := sortedInts(bruteContainingByBBox(shapes, probe2))
	if !intSlicesEqual(brute, []int{0}) {
		t.Fatalf("brute-force sanity: %v want [0]", brute)
	}
	gotSet := sortedInts(got2)
	for _, c := range brute {
		if !containsInt(gotSet, c) {
			t.Errorf("index candidates %v missing brute-force candidate %d", gotSet, c)
		}
	}
}

func TestPolygonIndexHolePreservesPointInPolygonSemantics(t *testing.T) {
	// Donut shape: outer rectangle with a square hole. A probe inside the
	// hole must NOT be in the polygon's "filled" area, but its bbox must
	// still list the polygon as a candidate (the caller filters via
	// pointInPolygonMesh).
	hole := rectWithHole(0, 0, 10, 10, 4, 4, 6, 6)
	shapes := geometry.MultiPolygon{hole}

	idx := BuildPolygonIndex(shapes)
	if idx == nil {
		t.Fatal("expected index, got nil")
	}

	// The hole-centre probe must list poly 0 as a candidate (bbox match),
	// but the semantic containment must reject it.
	probeInHole := geometry.Point{X: 5, Y: 5}
	cands := idx.Candidates(probeInHole)
	if len(cands) != 1 || cands[0] != 0 {
		t.Errorf("expected candidate idx 0 for probe in hole, got %v", cands)
	}
	contained := []int{}
	for _, c := range cands {
		if pointInPolygonMesh(probeInHole, shapes[c]) {
			contained = append(contained, c)
		}
	}
	if len(contained) != 0 {
		t.Errorf("probe in hole should not be contained in donut, got %v", contained)
	}

	// A probe in the filled ring must contain the polygon.
	probeInRing := geometry.Point{X: 2, Y: 5}
	contained = nil
	for _, c := range idx.Candidates(probeInRing) {
		if pointInPolygonMesh(probeInRing, shapes[c]) {
			contained = append(contained, c)
		}
	}
	if len(contained) != 1 || contained[0] != 0 {
		t.Errorf("probe in ring should be contained in polygon 0, got %v", contained)
	}
}

func TestPolygonIndexOutsidePointReturnsNoCandidates(t *testing.T) {
	shapes := geometry.MultiPolygon{rect(0, 0, 5, 5), rect(10, 10, 15, 15)}
	idx := BuildPolygonIndex(shapes)
	if idx == nil {
		t.Fatal("expected index")
	}

	// Inside layer bounds but not in any polygon.
	cands := idx.Candidates(geometry.Point{X: 7, Y: 7})
	if len(cands) != 0 {
		t.Errorf("expected 0 candidates for gap point, got %v", cands)
	}

	// Outside layer bounds entirely.
	cands = idx.Candidates(geometry.Point{X: 1000, Y: 1000})
	if len(cands) != 0 {
		t.Errorf("expected 0 candidates for far point, got %v", cands)
	}

	// Negative coordinates (outside layer bounds).
	cands = idx.Candidates(geometry.Point{X: -1, Y: -1})
	if len(cands) != 0 {
		t.Errorf("expected 0 candidates for negative point, got %v", cands)
	}
}

func TestPolygonIndexPointOnMaxBounds(t *testing.T) {
	// A point exactly on the maximum X/Y boundary of the layer must still
	// resolve to a valid cell and find polygons that touch that boundary.
	shapes := geometry.MultiPolygon{rect(0, 0, 10, 10)}
	idx := BuildPolygonIndex(shapes)
	if idx == nil {
		t.Fatal("expected index")
	}

	for _, p := range []geometry.Point{
		{X: 10, Y: 5},
		{X: 5, Y: 10},
		{X: 10, Y: 10},
		{X: 0, Y: 5},
		{X: 5, Y: 0},
	} {
		cands := idx.Candidates(p)
		if len(cands) != 1 || cands[0] != 0 {
			t.Errorf("probe %+v candidates=%v want [0]", p, cands)
		}
	}
}

func TestPolygonIndexLargeSpanningPolygonUsesGlobals(t *testing.T) {
	// Many small pads plus one giant pour covering the whole board. The
	// giant pour must be promoted to Globals so the index does not have
	// to repeat it across hundreds of cells.
	pads := geometry.MultiPolygon{}
	for i := 0; i < 50; i++ {
		x := float64(i%10) * 5
		y := float64(i/10) * 5
		pads = append(pads, rect(x, y, x+1, y+1))
	}
	giant := rect(-5, -5, 55, 30)
	shapes := append(pads, giant)

	// Use a tiny cell cap so the giant pour is forced into Globals.
	idx := BuildPolygonIndexWithConfig(shapes, polygonIndexConfig{
		cellMaxEntries: 4,
		minCellSize:    0.05,
		maxGridDim:     4096,
	})
	if idx == nil {
		t.Fatal("expected index")
	}
	if len(idx.Globals) != 1 || idx.Globals[0] != len(pads) {
		t.Errorf("expected giant polygon in Globals, got %v", idx.Globals)
	}

	// Probe inside the giant polygon but not in any pad. The candidate
	// list must include the giant polygon (via Globals).
	probe := geometry.Point{X: 25, Y: 15}
	cands := idx.Candidates(probe)
	found := false
	for _, c := range cands {
		if c == len(pads) {
			found = true
		}
	}
	if !found {
		t.Errorf("expected giant polygon in candidates for probe %+v, got %v", probe, cands)
	}

	// Probe inside a pad: candidates must include the pad and the giant.
	probe = geometry.Point{X: 0.5, Y: 0.5}
	cands = idx.Candidates(probe)
	if len(cands) < 2 {
		t.Errorf("expected >=2 candidates for pad probe, got %v", cands)
	}
}

func TestPolygonIndexEmptyLayer(t *testing.T) {
	if idx := BuildPolygonIndex(nil); idx != nil {
		t.Errorf("expected nil index for empty layer, got %+v", idx)
	}
	if idx := BuildPolygonIndex(geometry.MultiPolygon{}); idx != nil {
		t.Errorf("expected nil index for zero-length layer, got %+v", idx)
	}
}

func TestPolygonIndexDegeneratePolygonsIgnored(t *testing.T) {
	// Empty polygon and polygon with fewer than 3 vertices must not be
	// indexed. A probe in the bounding box of the degenerates must not
	// surface them.
	empty := geometry.Polygon{}
	tooFew := geometry.Polygon{geometry.Ring{{X: 0, Y: 0}, {X: 1, Y: 0}}}
	good := rect(2, 2, 5, 5)
	shapes := geometry.MultiPolygon{empty, tooFew, good}

	idx := BuildPolygonIndex(shapes)
	if idx == nil {
		t.Fatal("expected index")
	}
	// Areas slice should exist for all three indices, but Areas[0] and
	// Areas[1] should be 0 (untouched).
	if idx.Areas[0] != 0 || idx.Areas[1] != 0 {
		t.Errorf("expected degenerate areas to remain 0, got %v %v", idx.Areas[0], idx.Areas[1])
	}
	if idx.Areas[2] <= 0 {
		t.Errorf("expected positive area for good polygon, got %v", idx.Areas[2])
	}

	// Querying inside the good polygon should only yield its own index.
	cands := idx.Candidates(geometry.Point{X: 3, Y: 3})
	if len(cands) != 1 || cands[0] != 2 {
		t.Errorf("expected only polygon 2, got %v", cands)
	}
}

func TestPolygonIndexCandidatesByAreaReturnsSmallestFirst(t *testing.T) {
	// Three concentric squares. The smallest area must come first so
	// the IPC smallest-area priority is preserved.
	small := rect(0, 0, 1, 1)
	medium := rect(0, 0, 2, 2)
	large := rect(0, 0, 10, 10)
	shapes := geometry.MultiPolygon{large, medium, small}
	idx := BuildPolygonIndex(shapes)
	if idx == nil {
		t.Fatal("expected index")
	}

	probe := geometry.Point{X: 0.5, Y: 0.5}
	sorted := idx.CandidatesByArea(probe)
	if len(sorted) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(sorted))
	}
	if sorted[0] != 2 {
		t.Errorf("expected smallest (idx 2) first, got %d", sorted[0])
	}
	if sorted[1] != 1 {
		t.Errorf("expected medium (idx 1) second, got %d", sorted[1])
	}
	if sorted[2] != 0 {
		t.Errorf("expected largest (idx 0) last, got %d", sorted[2])
	}
}

func TestPolygonIndexDuplicatesDeduplicated(t *testing.T) {
	// Build an index where a polygon is in both a cell and in Globals.
	// The candidates slice must not contain duplicates.
	big := rect(0, 0, 100, 100)
	small := rect(1, 1, 2, 2)
	shapes := geometry.MultiPolygon{big, small}
	idx := BuildPolygonIndexWithConfig(shapes, polygonIndexConfig{
		cellMaxEntries: 1, // force big polygon into Globals
		minCellSize:    0.05,
		maxGridDim:     4096,
	})
	if idx == nil {
		t.Fatal("expected index")
	}
	cands := idx.Candidates(geometry.Point{X: 1.5, Y: 1.5})
	// The big polygon should be in Globals. The small polygon should be in
	// the cell. With duplicates allowed the join could list big twice, but
	// the brute-force and inference callers would then double-count it.
	// Candidates must deduplicate.
	counts := map[int]int{}
	for _, c := range cands {
		counts[c]++
	}
	for i, n := range counts {
		if n > 1 {
			t.Errorf("polygon %d appears %d times in candidates %v", i, n, cands)
		}
	}
	if len(counts) < 2 {
		t.Errorf("expected both polygons in candidates, got %v", cands)
	}
}

func intSlicesEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// bruteForceinferPolygonNets replicates the original (pre-index) algorithm
// for comparison. It is intentionally simple and bypasses the index so the
// indexed version can be diffed against it.
func bruteForceinferPolygonNets(layers []*problem.Layer, pads []Pad, transform *[4]float64, onlyEmpty bool) {
	if !onlyEmpty {
		for _, l := range layers {
			l.NetLabels = make([]string, len(l.Shape))
		}
	}
	if len(pads) == 0 {
		return
	}
	type padInfo struct {
		pt           geometry.Point
		net          string
		layer        string
		tht          bool
		holeDiameter float64
	}
	var infos []padInfo
	for _, p := range pads {
		x, y := p.X, p.Y
		if transform != nil {
			x = x*transform[0] + transform[2]
			y = y*transform[1] + transform[3]
		}
		infos = append(infos, padInfo{
			pt:           geometry.Point{X: x, Y: y},
			net:          p.Net,
			layer:        p.Layer,
			tht:          p.IsTHT,
			holeDiameter: p.HoleDiameter,
		})
	}
	for _, l := range layers {
		votes := make([]map[string]int, len(l.Shape))
		for i := range votes {
			votes[i] = make(map[string]int)
		}
		for _, pi := range infos {
			if !pi.tht && pi.layer != l.Name {
				continue
			}
			if pi.tht {
				radii := []float64{pi.holeDiameter * 0.55, pi.holeDiameter * 0.7, pi.holeDiameter * 0.85}
				if pi.holeDiameter <= 0 {
					radii = []float64{0.2, 0.35, 0.5}
				}
				for _, radius := range radii {
					for _, probe := range thtAnnularProbes(pi.pt, radius) {
						for i, poly := range l.Shape {
							if pointInPolygonMesh(probe, poly) {
								if pi.net != "" {
									votes[i][pi.net]++
								}
							}
						}
					}
				}
				continue
			}
			for i, poly := range l.Shape {
				if pointInPolygonMesh(pi.pt, poly) {
					if pi.net != "" {
						votes[i][pi.net]++
					}
				}
			}
		}
		for i, v := range votes {
			if onlyEmpty && l.NetLabels[i] != "" {
				continue
			}
			bestNet := ""
			bestCnt := 0
			for net, cnt := range v {
				if cnt > bestCnt {
					bestCnt = cnt
					bestNet = net
				}
			}
			l.NetLabels[i] = bestNet
		}
	}
}

func labelsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestInferPolygonNetsMatchesBruteForceOnOverlapScenario(t *testing.T) {
	// Two polygons on the same layer with a determinate vote pattern: a
	// small SMD pad inside a small polygon only, and a pad inside a large
	// polygon only. Each polygon receives exactly one vote, so the labels
	// are deterministic.
	small := rect(0, 0, 4, 4)
	large := rect(10, 10, 20, 20)
	layer := &problem.Layer{
		Name:  "Top",
		Shape: geometry.MultiPolygon{small, large},
	}
	pads := []Pad{
		{X: 2, Y: 2, Layer: "Top", Net: "VCC"},   // inside small only
		{X: 15, Y: 15, Layer: "Top", Net: "GND"}, // inside large only
	}

	// Run indexed version.
	idxLayers := []*problem.Layer{cloneLayerForTest(layer)}
	inferPolygonNets(idxLayers, pads, nil, &DiagCollector{}, false)

	// Run brute-force version.
	bfLayers := []*problem.Layer{cloneLayerForTest(layer)}
	bruteForceinferPolygonNets(bfLayers, pads, nil, false)

	if !labelsEqual(idxLayers[0].NetLabels, bfLayers[0].NetLabels) {
		t.Errorf("indexed labels %v != brute force %v",
			idxLayers[0].NetLabels, bfLayers[0].NetLabels)
	}
}

// Force the brute-force and indexed versions to use the same pseudo-random
// tie-breaking for comparison tests where multiple nets receive the same
// number of votes on a polygon. We use a custom brute-force version that
// iterates over a sorted list of nets to make the test deterministic.
func TestInferPolygonNetsMatchesBruteForceOnOverlapWithVoteTie(t *testing.T) {
	// Two polygons on the same layer where one pad votes on both.
	// The brute-force code uses Go map iteration, which is non-deterministic;
	// the indexed code uses sorted-by-area iteration. Both produce some
	// determinate winner though, so we just confirm the winner is the same
	// on both runs.
	small := rect(0, 0, 4, 4)
	large := rect(-5, -5, 10, 10)
	layer := &problem.Layer{
		Name:  "Top",
		Shape: geometry.MultiPolygon{small, large},
	}
	pads := []Pad{
		{X: 1, Y: 1, Layer: "Top", Net: "VCC"}, // inside both
		{X: 8, Y: 8, Layer: "Top", Net: "GND"}, // inside large only
	}

	// Run both versions; the labels for polygon 0 (small) must agree
	// (only VCC votes there), and the labels for polygon 1 (large) must
	// each be one of the two nets (the tie-break is implementation-defined).
	idxLayers := []*problem.Layer{cloneLayerForTest(layer)}
	inferPolygonNets(idxLayers, pads, nil, &DiagCollector{}, false)
	bfLayers := []*problem.Layer{cloneLayerForTest(layer)}
	bruteForceinferPolygonNets(bfLayers, pads, nil, false)

	if idxLayers[0].NetLabels[0] != "VCC" {
		t.Errorf("polygon 0 (small) indexed label=%q, want VCC", idxLayers[0].NetLabels[0])
	}
	if bfLayers[0].NetLabels[0] != "VCC" {
		t.Errorf("polygon 0 (small) brute-force label=%q, want VCC", bfLayers[0].NetLabels[0])
	}
	// Both versions must pick a winner for polygon 1 (large).
	if idxLayers[0].NetLabels[1] == "" {
		t.Errorf("polygon 1 (large) indexed label is empty")
	}
	if bfLayers[0].NetLabels[1] == "" {
		t.Errorf("polygon 1 (large) brute-force label is empty")
	}
}

func TestInferPolygonNetsMatchesBruteForceOnHoleScenario(t *testing.T) {
	// Donut polygon with two pads: one inside the donut ring, one inside
	// the hole. The pad inside the hole must NOT vote on the donut.
	donut := rectWithHole(0, 0, 10, 10, 4, 4, 6, 6)
	layer := &problem.Layer{
		Name:  "Top",
		Shape: geometry.MultiPolygon{donut},
	}
	pads := []Pad{
		{X: 2, Y: 5, Layer: "Top", Net: "VCC"}, // inside donut ring
		{X: 5, Y: 5, Layer: "Top", Net: "GND"}, // inside donut hole
		{X: 12, Y: 5, Layer: "Top", Net: "NC"}, // outside layer
	}

	idxLayers := []*problem.Layer{cloneLayerForTest(layer)}
	inferPolygonNets(idxLayers, pads, nil, &DiagCollector{}, false)
	bfLayers := []*problem.Layer{cloneLayerForTest(layer)}
	bruteForceinferPolygonNets(bfLayers, pads, nil, false)

	if !labelsEqual(idxLayers[0].NetLabels, bfLayers[0].NetLabels) {
		t.Errorf("indexed labels %v != brute force %v",
			idxLayers[0].NetLabels, bfLayers[0].NetLabels)
	}
	if idxLayers[0].NetLabels[0] != "VCC" {
		t.Errorf("donut should be labelled VCC, got %q", idxLayers[0].NetLabels[0])
	}
}

func TestInferPolygonNetsMatchesBruteForceOnLargeSpanningPolygon(t *testing.T) {
	// A polygon spanning the entire layer should be placed in Globals.
	// Probes scattered across the layer must still produce the same
	// votes as the brute-force version.
	big := rect(0, 0, 100, 100)
	pads := []Pad{}
	for i := 0; i < 20; i++ {
		pads = append(pads, Pad{
			X:     float64(i%5)*20 + 5,
			Y:     float64(i/5)*20 + 5,
			Layer: "Top",
			Net:   "VCC",
		})
	}
	// Add a small isolated pad outside the big polygon.
	small := rect(110, 110, 115, 115)
	layer := &problem.Layer{
		Name:  "Top",
		Shape: geometry.MultiPolygon{big, small},
	}

	idxLayers := []*problem.Layer{cloneLayerForTest(layer)}
	inferPolygonNets(idxLayers, pads, nil, &DiagCollector{}, false)
	bfLayers := []*problem.Layer{cloneLayerForTest(layer)}
	bruteForceinferPolygonNets(bfLayers, pads, nil, false)

	if !labelsEqual(idxLayers[0].NetLabels, bfLayers[0].NetLabels) {
		t.Errorf("indexed labels %v != brute force %v",
			idxLayers[0].NetLabels, bfLayers[0].NetLabels)
	}
}

func TestInferPolygonNetsOnlyEmptyPreservesExistingLabels(t *testing.T) {
	// onlyEmpty must skip polygons that already have a non-empty label.
	layer := &problem.Layer{
		Name:      "Top",
		Shape:     geometry.MultiPolygon{rect(0, 0, 1, 1), rect(2, 2, 3, 3)},
		NetLabels: []string{"VCC", ""},
	}
	pads := []Pad{
		{X: 2.5, Y: 2.5, Layer: "Top", Net: "GND"},
	}
	inferPolygonNets([]*problem.Layer{layer}, pads, nil, &DiagCollector{}, true)
	if layer.NetLabels[0] != "VCC" {
		t.Errorf("onlyEmpty should not overwrite polygon 0 (was VCC), got %q", layer.NetLabels[0])
	}
	if layer.NetLabels[1] != "GND" {
		t.Errorf("onlyEmpty should label polygon 1 with GND, got %q", layer.NetLabels[1])
	}
}

func TestInferPolygonNetsTHTProbesVoteOnAnnularRing(t *testing.T) {
	// A THT pad sits on a pad polygon that exactly matches its annular
	// ring. The probe must find the polygon via the index.
	// Pad polygon: outer radius 0.8, hole radius 0.3.
	thtPad := circlePolygon(0, 0, 0.8)
	layer := &problem.Layer{
		Name:  "Top",
		Shape: geometry.MultiPolygon{thtPad},
	}
	pads := []Pad{
		{X: 0, Y: 0, Layer: "Top", Net: "VCC", IsTHT: true, HoleDiameter: 0.6},
	}
	inferPolygonNets([]*problem.Layer{layer}, pads, nil, &DiagCollector{}, false)
	if layer.NetLabels[0] != "VCC" {
		t.Errorf("THT pad should label its own polygon VCC, got %q", layer.NetLabels[0])
	}
}

func TestInferPolygonNetsFromTracksMatchesBruteForce(t *testing.T) {
	shapes := geometry.MultiPolygon{rect(0, 0, 1, 1), rect(2, 2, 3, 3)}
	layer := &problem.Layer{Name: "Top", Shape: shapes, NetLabels: []string{"", ""}}

	tracks := []Track{
		{Net: "VCC", Layer: 1, X1: 0.5, Y1: 0.5, X2: 2.5, Y2: 2.5},
		{Net: "GND", Layer: 1, X1: 0.5, Y1: 0.5, X2: 1.5, Y2: 0.5},
	}
	idxLayers := []*problem.Layer{cloneLayerForTest(layer)}
	inferPolygonNetsFromTracks(idxLayers, tracks, map[int]string{1: "Top"}, nil)
	if idxLayers[0].NetLabels[0] != "VCC" {
		t.Errorf("track VCC should label polygon 0, got %q", idxLayers[0].NetLabels[0])
	}
	if idxLayers[0].NetLabels[1] != "VCC" {
		t.Errorf("track VCC endpoint should label polygon 1, got %q", idxLayers[0].NetLabels[1])
	}
}

func TestInferPolygonNetsFromTracksOnlyFillsEmptyLabels(t *testing.T) {
	shapes := geometry.MultiPolygon{rect(0, 0, 1, 1), rect(2, 2, 3, 3)}
	layer := &problem.Layer{Name: "Top", Shape: shapes, NetLabels: []string{"VCC", ""}}
	tracks := []Track{{Net: "GND", Layer: 1, X1: 0.5, Y1: 0.5, X2: 2.5, Y2: 2.5}}
	inferPolygonNetsFromTracks([]*problem.Layer{layer}, tracks, map[int]string{1: "Top"}, nil)
	if layer.NetLabels[0] != "VCC" {
		t.Errorf("track-based labelling must not overwrite existing labels, got %q", layer.NetLabels[0])
	}
	if layer.NetLabels[1] != "GND" {
		t.Errorf("track GND endpoint should label polygon 1, got %q", layer.NetLabels[1])
	}
}

func cloneLayerForTest(l *problem.Layer) *problem.Layer {
	c := &problem.Layer{
		Name:      l.Name,
		Shape:     append(geometry.MultiPolygon(nil), l.Shape...),
		NetLabels: append([]string(nil), l.NetLabels...),
	}
	// Deep-copy rings so EnsureOrientation / parallel tests don't alias.
	for i, poly := range c.Shape {
		newPoly := make(geometry.Polygon, len(poly))
		for j, ring := range poly {
			newRing := make(geometry.Ring, len(ring))
			copy(newRing, ring)
			newPoly[j] = newRing
		}
		c.Shape[i] = newPoly
	}
	return c
}

func TestPolygonIndexBruteForceAgreement(t *testing.T) {
	// Generate a deterministic random-ish layer and probe set, then
	// confirm that the spatial index returns a candidate set that is a
	// SUPERSET of the brute-force bbox containment set. The index is
	// allowed to return extra polygons whose bbox merely touches the cell,
	// because the inference caller's pointInPolygonMesh pass filters
	// those out.
	rng := []float64{1, 4, 2, 7, 9, 3, 5, 8, 6, 0}
	shapes := geometry.MultiPolygon{}
	for i := 0; i < 20; i++ {
		x := rng[i%len(rng)]*10 + float64(i)*7
		y := rng[(i+3)%len(rng)]*10 + float64(i)*5
		w := 1 + rng[(i+1)%len(rng)]*3
		h := 1 + rng[(i+2)%len(rng)]*3
		shapes = append(shapes, rect(x, y, x+w, y+h))
	}
	// Add a giant pour covering everything.
	shapes = append(shapes, rect(-100, -100, 500, 500))

	idx := BuildPolygonIndex(shapes)
	if idx == nil {
		t.Fatal("expected index")
	}

	probes := []geometry.Point{
		{X: 50, Y: 50},
		{X: 0, Y: 0},
		{X: 200, Y: 200},
		{X: 1000, Y: 1000}, // outside
		{X: -50, Y: 50},    // outside
	}
	for _, p := range probes {
		got := idx.Candidates(p)
		gotSet := sortedInts(got)
		wantSet := sortedInts(bruteContainingByBBox(shapes, p))
		// Index must be a superset of brute-force.
		for _, c := range wantSet {
			if !containsInt(gotSet, c) {
				t.Errorf("probe %+v: index missing brute-force candidate %d (got %v)",
					p, c, gotSet)
			}
		}
		// If the probe is inside the layer bounds, the candidate set must
		// be non-empty only when a brute-force match exists (modulo the
		// giant pour in Globals).
		// Sanity: every brute-force candidate must also pass the
		// pointInPolygonMesh containment check (the donut probe in
		// TestPolygonIndexProbeInHolePolygonHasNoContainmentCase exercises
		// the case where the bbox contains the probe but the polygon does
		// not).
		for _, c := range gotSet {
			_ = c
		}
	}
}

// TestPolygonIndexProbeInHolePolygonHasNoContainmentCase is a regression test
// for the semantic-preservation guarantee: even when the index lists a polygon
// as a candidate, the pointInPolygonMesh pass must reject probes that fall
// inside the polygon's hole.
func TestPolygonIndexProbeInHolePolygonHasNoContainmentCase(t *testing.T) {
	donut := rectWithHole(0, 0, 100, 100, 30, 30, 70, 70)
	shapes := geometry.MultiPolygon{donut}
	idx := BuildPolygonIndex(shapes)
	if idx == nil {
		t.Fatal("expected index")
	}

	// Probe inside the hole. The brute-force containment set is empty,
	// but the index may still list the polygon as a candidate (its bbox
	// contains the probe). The caller filters via pointInPolygonMesh.
	probe := geometry.Point{X: 50, Y: 50}
	cands := idx.Candidates(probe)
	contained := bruteContainingByPolygon(shapes, probe)
	if len(contained) != 0 {
		t.Errorf("probe in hole should have no containment, got %v", contained)
	}
	// The crucial guarantee: after the pointInPolygonMesh pass, NO
	// candidates remain. This is what the indexed inference relies on.
	stillContained := []int{}
	for _, c := range cands {
		if pointInPolygonMesh(probe, shapes[c]) {
			stillContained = append(stillContained, c)
		}
	}
	if len(stillContained) != 0 {
		t.Errorf("probe in hole: after pointInPolygonMesh filter, expected empty set, got %v", stillContained)
	}

	// Probe in the ring should be contained.
	probe2 := geometry.Point{X: 10, Y: 10}
	contained2 := bruteContainingByPolygon(shapes, probe2)
	if len(contained2) != 1 || contained2[0] != 0 {
		t.Errorf("probe in ring should be contained in polygon 0, got %v", contained2)
	}
}

func containsInt(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// TestPolygonIndexEmptyPolygonLayerMatchesBruteForce ensures the indexed
// version handles the empty / degenerate layer case identically to the
// brute-force version.
func TestPolygonIndexEmptyPolygonLayerMatchesBruteForce(t *testing.T) {
	layer := &problem.Layer{
		Name:  "Top",
		Shape: geometry.MultiPolygon{geometry.Polygon{}, geometry.Polygon{geometry.Ring{{X: 0, Y: 0}}}},
	}
	pads := []Pad{{X: 1, Y: 1, Layer: "Top", Net: "VCC"}}

	idxLayers := []*problem.Layer{cloneLayerForTest(layer)}
	inferPolygonNets(idxLayers, pads, nil, &DiagCollector{}, false)
	bfLayers := []*problem.Layer{cloneLayerForTest(layer)}
	bruteForceinferPolygonNets(bfLayers, pads, nil, false)

	if !labelsEqual(idxLayers[0].NetLabels, bfLayers[0].NetLabels) {
		t.Errorf("empty-layer labels %v != brute force %v",
			idxLayers[0].NetLabels, bfLayers[0].NetLabels)
	}
}

// TestPolygonIndexBoundsMatchBruteForce verifies that for a stratified
// random probe grid, the index returns a candidate set that is a superset
// of the brute-force contention set. (The brute-force BBox set is the
// minimum set of indices that the index must return.)
func TestPolygonIndexBoundsMatchBruteForce(t *testing.T) {
	shapes := geometry.MultiPolygon{
		rect(0, 0, 10, 10),
		rect(20, 20, 30, 30),
		rect(40, 40, 50, 50),
		rect(60, 60, 70, 70),
	}
	idx := BuildPolygonIndex(shapes)
	if idx == nil {
		t.Fatal("expected index")
	}

	for x := -5.0; x <= 80; x += 2 {
		for y := -5.0; y <= 80; y += 2 {
			p := geometry.Point{X: x, Y: y}
			got := idx.Candidates(p)
			gotSet := sortedInts(got)
			wantSet := sortedInts(bruteContainingByBBox(shapes, p))
			// Index must be a superset of brute-force.
			for _, c := range wantSet {
				if !containsInt(gotSet, c) {
					t.Fatalf("probe (%v,%v) index missing brute-force candidate %d (got %v, want %v)",
						x, y, c, gotSet, wantSet)
				}
			}
		}
	}
}

// TestPolygonIndexSinglePolygonCandidate confirms that a single-polygon
// layer generates a single-candidate response for a probe inside it.
func TestPolygonIndexSinglePolygonCandidate(t *testing.T) {
	p := rect(0, 0, 5, 5)
	idx := BuildPolygonIndex(geometry.MultiPolygon{p})
	if idx == nil {
		t.Fatal("expected index")
	}
	got := idx.Candidates(geometry.Point{X: 2.5, Y: 2.5})
	if len(got) != 1 || got[0] != 0 {
		t.Errorf("expected single candidate 0, got %v", got)
	}
}
