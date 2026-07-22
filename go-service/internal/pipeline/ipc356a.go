package pipeline

import (
	"bufio"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/easyeda/eext-paden-integration/go-service/internal/geometry"
	"github.com/easyeda/eext-paden-integration/go-service/internal/problem"
)

// IPC356ANetlist holds the relevant subset of an IPC-D-356A netlist file.
// Coordinates are stored in millimetres (Gerber coordinate space) using the
// standard EasyEDA conversion: 1 IPC-D-356A unit = 0.0001 inch = 0.00254 mm.
type IPC356ANetlist struct {
	Pads       []IPC356APad
	Vias       []IPC356AVia
	Traces     []IPC356ATrace
	BoardEdge  []geometry.Point
	LayerNames []string
}

// IPC356APad is a component pad or test point entry.
type IPC356APad struct {
	Net      string
	RefDes   string
	Pin      string
	X, Y     float64
	Shape    string
	Width    float64
	Height   float64
	Rotation float64
	Side     string
	IsTHT    bool
}

// IPC356AVia is a via or mounting hole entry.
type IPC356AVia struct {
	Net        string
	RefDes     string
	X, Y       float64
	DrillDia   float64
	Side       string
	OuterDia   float64
}

// IPC356ATrace is a conductive trace segment.
type IPC356ATrace struct {
	Net      string
	Layer    string
	Width    float64
	Vertices []geometry.Point
}

// ipc356aUnitToMm converts EasyEDA IPC-D-356A units to millimetres.
const ipc356aUnitToMm = 0.00254

// ParseIPC356A parses an IPC-D-356A text file.
func ParseIPC356A(text string) (*IPC356ANetlist, error) {
	nl := &IPC356ANetlist{}

	scanner := bufio.NewScanner(strings.NewReader(text))
	var lastTrace *IPC356ATrace
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r\n")
		if line == "" || line == "999" {
			continue
		}
		if len(line) < 3 {
			continue
		}

		code := line[:3]
		switch code {
		case "C  ":
			parseIPC356AComment(line, nl)
		case "P  ":
			// parameters, not needed for net matching
		case "327":
			pad, err := parseIPC356APad(line)
			if err == nil {
				nl.Pads = append(nl.Pads, pad)
			}
		case "317":
			pad, via, err := parseIPC356A317(line)
			if err == nil {
				if via != nil {
					nl.Vias = append(nl.Vias, *via)
				} else {
					nl.Pads = append(nl.Pads, pad)
				}
			}
		case "378":
			trace, err := parseIPC356ATrace(line)
			if err == nil {
				nl.Traces = append(nl.Traces, trace)
				lastTrace = &nl.Traces[len(nl.Traces)-1]
			}
		case "078":
			if lastTrace == nil {
				continue
			}
			pts, err := parseIPC356AVertices(line[3:])
			if err == nil {
				lastTrace.Vertices = append(lastTrace.Vertices, pts...)
			}
		case "389":
			pts, err := parseIPC356AVertices(line[3:])
			if err == nil {
				nl.BoardEdge = append(nl.BoardEdge, pts...)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(nl.Pads) == 0 && len(nl.Vias) == 0 && len(nl.Traces) == 0 {
		return nil, fmt.Errorf("no usable netlist entries found")
	}
	return nl, nil
}

func parseIPC356AComment(line string, nl *IPC356ANetlist) {
	// Stackup comments look like "C  Top Layer" or "C  Bottom Layer".
	text := strings.TrimSpace(line[2:])
	lower := strings.ToLower(text)
	if strings.Contains(lower, "layer") && !strings.Contains(lower, "stackup") {
		name := strings.TrimSpace(text)
		if name != "" {
			nl.LayerNames = append(nl.LayerNames, name)
		}
	}
}

var wsRe = regexp.MustCompile(`\s+`)

func parseIPC356APad(line string) (IPC356APad, error) {
	// 327NET REFDES PIN SHAPE_TOKEN X..Y..X..Y..R... S#
	parts := wsRe.Split(strings.TrimSpace(line[3:]), -1)
	if len(parts) < 4 {
		return IPC356APad{}, fmt.Errorf("short 327 record")
	}
	pad := IPC356APad{
		Net:    parts[0],
		RefDes: parts[1],
		Pin:    parts[2],
	}

	// Find the shape token and then the trailing side.
	shapeIdx := -1
	for i, p := range parts {
		if strings.HasPrefix(p, "PA") || strings.HasPrefix(p, "D") || strings.HasPrefix(p, "C") || strings.HasPrefix(p, "R") {
			shapeIdx = i
			break
		}
	}
	if shapeIdx < 0 || shapeIdx >= len(parts)-1 {
		return IPC356APad{}, fmt.Errorf("no shape token in 327 record")
	}
	pad.Shape = parts[shapeIdx]

	side := parts[len(parts)-1]
	pad.Side = side
	pad.IsTHT = side == "S0" || side == "S3"

	// Concatenate everything between shape and side into one string.
	body := strings.Join(parts[shapeIdx+1:len(parts)-1], "")

	x, y, w, h, rot, err := parseIPC356AXYXYR(body)
	if err != nil {
		return IPC356APad{}, err
	}
	pad.X, pad.Y = x*ipc356aUnitToMm, y*ipc356aUnitToMm
	pad.Width, pad.Height = w*ipc356aUnitToMm, h*ipc356aUnitToMm
	pad.Rotation = rot
	return pad, nil
}

func parseIPC356A317(line string) (IPC356APad, *IPC356AVia, error) {
	parts := wsRe.Split(strings.TrimSpace(line[3:]), -1)
	if len(parts) < 4 {
		return IPC356APad{}, nil, fmt.Errorf("short 317 record")
	}
	net := parts[0]
	ref := parts[1]
	pin := parts[2]

	// Diameter token is the first token beginning with D.
	diaIdx := -1
	for i, p := range parts {
		if strings.HasPrefix(p, "D") && len(p) > 1 {
			diaIdx = i
			break
		}
	}
	if diaIdx < 0 {
		return IPC356APad{}, nil, fmt.Errorf("no D token in 317 record")
	}
	drill, _ := strconv.ParseFloat(parts[diaIdx][1:], 64)

	// Shape token follows the D token.
	shapeIdx := -1
	for i := diaIdx + 1; i < len(parts); i++ {
		p := parts[i]
		if strings.HasPrefix(p, "PA") || strings.HasPrefix(p, "C") || strings.HasPrefix(p, "R") || strings.HasPrefix(p, "D") {
			shapeIdx = i
			break
		}
	}
	if shapeIdx < 0 {
		return IPC356APad{}, nil, fmt.Errorf("no shape token in 317 record")
	}

	side := parts[len(parts)-1]
	body := strings.Join(parts[shapeIdx+1:len(parts)-1], "")

	x, y, w, h, rot, err := parseIPC356AXYXYR(body)
	if err != nil {
		return IPC356APad{}, nil, err
	}

	if strings.ToUpper(ref) == "VIA" || pin == "-" {
		via := IPC356AVia{
			Net:      net,
			RefDes:   ref,
			X:        x * ipc356aUnitToMm,
			Y:        y * ipc356aUnitToMm,
			DrillDia: drill * ipc356aUnitToMm,
			OuterDia: w * ipc356aUnitToMm,
			Side:     side,
		}
		return IPC356APad{}, &via, nil
	}

	pad := IPC356APad{
		Net:      net,
		RefDes:   ref,
		Pin:      pin,
		Shape:    parts[shapeIdx],
		X:        x * ipc356aUnitToMm,
		Y:        y * ipc356aUnitToMm,
		Width:    w * ipc356aUnitToMm,
		Height:   h * ipc356aUnitToMm,
		Rotation: rot,
		Side:     side,
		IsTHT:    side == "S0" || side == "S3",
	}
	return pad, nil, nil
}

// parseIPC356AXYXYR parses a string like "006138Y-005098X0585Y0680R000" or
// "001429Y-002848X0669Y0669R000" into x, y, width, height, rotation.
func parseIPC356AXYXYR(s string) (x, y, w, h, rot float64, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, 0, 0, 0, fmt.Errorf("empty coordinate body")
	}
	// Extract all X/Y numbers.
	xs := extractIPC356ANumbers(s, 'X')
	ys := extractIPC356ANumbers(s, 'Y')
	if len(xs) < 1 || len(ys) < 1 {
		return 0, 0, 0, 0, 0, fmt.Errorf("missing coordinates in %q", s)
	}
	x = xs[0]
	y = ys[0]
	w = 0
	if len(xs) >= 2 {
		w = xs[1]
	}
	h = 0
	if len(ys) >= 2 {
		h = ys[1]
	}
	rot = 0
	if i := strings.Index(s, "R"); i >= 0 && i+1 < len(s) {
		rot, _ = strconv.ParseFloat(s[i+1:], 64)
	}
	return x, y, w, h, rot, nil
}

func extractIPC356ANumbers(s string, prefix byte) []float64 {
	var out []float64
	for i := 0; i < len(s); i++ {
		if s[i] == prefix {
			j := i + 1
			for j < len(s) && (s[j] == '-' || (s[j] >= '0' && s[j] <= '9')) {
				j++
			}
			if j > i+1 {
				if v, err := strconv.ParseFloat(s[i+1:j], 64); err == nil {
					out = append(out, v)
				}
			}
			i = j - 1
		}
	}
	return out
}

func parseIPC356ATrace(line string) (IPC356ATrace, error) {
	// 378NET LAYER WIDTH X..Y.. ...
	parts := wsRe.Split(strings.TrimSpace(line[3:]), -1)
	if len(parts) < 4 {
		return IPC356ATrace{}, fmt.Errorf("short 378 record")
	}
	trace := IPC356ATrace{Net: parts[0], Layer: parts[1]}

	// Width is the first X-prefixed token after the layer.
	bodyStart := 2
	for i := 2; i < len(parts); i++ {
		p := parts[i]
		if strings.HasPrefix(p, "X") {
			trace.Width = parseIPC356ANum(p[1:]) * ipc356aUnitToMm
			bodyStart = i + 1
			break
		}
	}

	body := strings.Join(parts[bodyStart:], "")
	pts, err := parseIPC356AVerticesBody(body)
	if err != nil {
		return IPC356ATrace{}, err
	}
	trace.Vertices = pts
	return trace, nil
}

func parseIPC356AVertices(body string) ([]geometry.Point, error) {
	return parseIPC356AVerticesBody(body)
}

func parseIPC356AVerticesBody(body string) ([]geometry.Point, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, nil
	}
	// The 078/389 formats may have implicit repeated coordinates (X without Y
	// or Y without X), so walk through the raw string in order to preserve the path.
	return extractIPC356APath(body)
}

// extractIPC356APath walks the body extracting X/Y coordinate pairs in order,
// carrying the last seen X or Y forward when only one is supplied.
func extractIPC356APath(body string) ([]geometry.Point, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, nil
	}
	var pts []geometry.Point
	var lastX, lastY float64
	hasX, hasY := false, false
	i := 0
	for i < len(body) {
		c := body[i]
		if c != 'X' && c != 'Y' {
			i++
			continue
		}
		j := i + 1
		for j < len(body) && (body[j] == '-' || (body[j] >= '0' && body[j] <= '9')) {
			j++
		}
		if j == i+1 {
			i = j
			continue
		}
		v, err := strconv.ParseFloat(body[i+1:j], 64)
		if err != nil {
			i = j
			continue
		}
		if c == 'X' {
			lastX = v
			hasX = true
		} else {
			lastY = v
			hasY = true
		}
		if hasX && hasY {
			pts = append(pts, geometry.Point{X: lastX * ipc356aUnitToMm, Y: lastY * ipc356aUnitToMm})
		}
		i = j
	}
	return pts, nil
}

func parseIPC356ANum(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}

// sortedLayerIndex maps an IPC-D-356A layer token such as "L01" or "L02" to
// the index in the supplied sorted layer slice. "L01" is the top physical layer.
func sortedLayerIndex(layerToken string, sortedLayers []string) int {
	token := strings.ToUpper(strings.TrimSpace(layerToken))
	if token == "" {
		return -1
	}
	if strings.HasPrefix(token, "L") {
		n, err := strconv.Atoi(token[1:])
		if err == nil && n >= 1 && n <= len(sortedLayers) {
			return n - 1
		}
	}
	// Fallback: try to match by side codes used for traces if any.
	switch token {
	case "S1":
		if len(sortedLayers) > 0 {
			return 0
		}
	case "S2":
		if len(sortedLayers) > 0 {
			return len(sortedLayers) - 1
		}
	}
	return -1
}

// sideLayerIndices returns the layer indices that a pad/via with the given side
// code may belong to. EasyEDA's exporter uses S2 for the primary (top) side and
// S1 for the secondary (bottom) side, which is the opposite of some IPC-D-356A
// variants; because the pad position itself is authoritative, we label all
// layers at that point and let containment decide.
func sideLayerIndices(side string, sortedLayers []string) []int {
	switch strings.ToUpper(side) {
	case "S0", "S3":
		var all []int
		for i := range sortedLayers {
			all = append(all, i)
		}
		return all
	case "S1":
		if len(sortedLayers) > 0 {
			return []int{len(sortedLayers) - 1}
		}
	case "S2":
		if len(sortedLayers) > 0 {
			return []int{0}
		}
	}
	return nil
}

// inferPolygonNetsFromIPC356A labels copper polygons using the IPC-D-356A
// netlist as the ground truth. It overrides any previously inferred labels.
func inferPolygonNetsFromIPC356A(layers []*problem.Layer, netlist *IPC356ANetlist, d *DiagCollector) {
	if netlist == nil || len(layers) == 0 {
		return
	}

	sortedLayerNames := make([]string, len(layers))
	for i, l := range layers {
		sortedLayerNames[i] = l.Name
	}

	// For each layer polygon collect net votes from pads/vias/traces.
	type vote struct {
		net   string
		count int
	}
	layerVotes := make([][]map[string]int, len(layers))
	for i := range layers {
		layerVotes[i] = make([]map[string]int, len(layers[i].Shape))
		for j := range layers[i].Shape {
			layerVotes[i][j] = make(map[string]int)
		}
	}

	voteFor := func(layerIdx, polyIdx int, net string) {
		if layerIdx < 0 || layerIdx >= len(layers) {
			return
		}
		if polyIdx < 0 || polyIdx >= len(layers[layerIdx].Shape) {
			return
		}
		if net == "" {
			return
		}
		layerVotes[layerIdx][polyIdx][net]++
	}

	// Pads / vias: position is authoritative. For THT/vias label every layer;
	// for SMD pads label the side layer(s). In practice the point only falls
	// inside copper on the correct layer, so even labelling all layers is safe.
	for _, pad := range netlist.Pads {
		pt := geometry.Point{X: pad.X, Y: pad.Y}
		indices := sideLayerIndices(pad.Side, sortedLayerNames)
		if len(indices) == 0 {
			indices = make([]int, len(layers))
			for i := range layers {
				indices[i] = i
			}
		}
		for _, li := range indices {
			for pi, poly := range layers[li].Shape {
				if pointInPolygonMesh(pt, poly) {
					voteFor(li, pi, pad.Net)
					break
				}
				// THT pads sit in drilled holes; the surrounding copper (annular ring
				// or pour) belongs to the pad net. Sample around the hole.
				if pad.IsTHT {
					probeRadius := pad.Width
					if probeRadius <= 0 {
						probeRadius = 0.3
					}
					for _, probe := range thtAnnularProbes(pt, probeRadius) {
						if pointInPolygonMesh(probe, poly) {
							voteFor(li, pi, pad.Net)
							break
						}
					}
				}
			}
		}
	}
	for _, via := range netlist.Vias {
		pt := geometry.Point{X: via.X, Y: via.Y}
		for li := range layers {
			for pi, poly := range layers[li].Shape {
				if pointInPolygonMesh(pt, poly) {
					voteFor(li, pi, via.Net)
					break
				}
				probeRadius := via.OuterDia
				if probeRadius <= 0 {
					probeRadius = 0.3
				}
				for _, probe := range thtAnnularProbes(pt, probeRadius) {
					if pointInPolygonMesh(probe, poly) {
						voteFor(li, pi, via.Net)
						break
					}
				}
			}
		}
	}

	// Traces: explicit layer token. Sample along the trace so diagonal/short
	// segments still land inside the correct polygon.
	for _, trace := range netlist.Traces {
		li := sortedLayerIndex(trace.Layer, sortedLayerNames)
		if li < 0 {
			d.Warn(fmt.Sprintf("IPC-D-356A trace layer '%s' not matched to config layers", trace.Layer))
			continue
		}
		for _, p := range trace.Vertices {
			for pi, poly := range layers[li].Shape {
				if pointInPolygonMesh(p, poly) {
					voteFor(li, pi, trace.Net)
					break
				}
			}
		}
		// Midpoints for multi-vertex traces.
		for i := 1; i < len(trace.Vertices); i++ {
			a, b := trace.Vertices[i-1], trace.Vertices[i]
			mid := geometry.Point{X: (a.X + b.X) / 2, Y: (a.Y + b.Y) / 2}
			for pi, poly := range layers[li].Shape {
				if pointInPolygonMesh(mid, poly) {
					voteFor(li, pi, trace.Net)
					break
				}
			}
		}
	}

	// Apply majority vote per polygon.
	totalLabelled := 0
	for li, votes := range layerVotes {
		for pi, v := range votes {
			if len(v) == 0 {
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
			if bestNet != "" {
				layers[li].NetLabels[pi] = bestNet
				totalLabelled++
			}
		}
	}
	d.Info(fmt.Sprintf("IPC-D-356A labelled %d polygons across %d layers", totalLabelled, len(layers)))
}

// ensureNetLabels initialises the NetLabels slice for each layer if needed.
func ensureNetLabels(layers []*problem.Layer) {
	for _, l := range layers {
		if len(l.NetLabels) != len(l.Shape) {
			l.NetLabels = make([]string, len(l.Shape))
		}
	}
}

// alignIPC356AToGerber checks whether the parsed IPC-D-356A coordinates need an
// offset to match the Gerber coordinate system. EasyEDA exports both with the
// same origin and scale, so usually no correction is required. When a board-edge
// polygon is present in the 356A file, we align the board-edge centre with the
// Gerber board-outline centre. The function returns (scaleX, scaleY, offsetX, offsetY).
func alignIPC356AToGerber(netlist *IPC356ANetlist, gerberOutline geometry.MultiPolygon) (sx, sy, ox, oy float64) {
	sx, sy = 1, 1
	ox, oy = 0, 0
	if netlist == nil || len(netlist.BoardEdge) < 3 {
		return
	}
	if len(gerberOutline) == 0 {
		return
	}

	var eb geometry.Box
	eb.MinX = netlist.BoardEdge[0].X
	eb.MaxX = netlist.BoardEdge[0].X
	eb.MinY = netlist.BoardEdge[0].Y
	eb.MaxY = netlist.BoardEdge[0].Y
	for _, p := range netlist.BoardEdge[1:] {
		eb.Extend(p)
	}

	gb := gerberOutline.Bounds()
	gCx := (gb.MinX + gb.MaxX) / 2
	gCy := (gb.MinY + gb.MaxY) / 2
	eCx := (eb.MinX + eb.MaxX) / 2
	eCy := (eb.MinY + eb.MaxY) / 2
	ox = gCx - eCx
	oy = gCy - eCy

	// Only trust small offsets; large offsets indicate mismatched units
	// or mirrored coordinates and should not be applied silently.
	gw := math.Max(gb.MaxX-gb.MinX, 1)
	gh := math.Max(gb.MaxY-gb.MinY, 1)
	if math.Abs(ox) > gw/4 || math.Abs(oy) > gh/4 {
		ox, oy = 0, 0
	}
	return
}

// applyIPC356AOffset adds the alignment offset to all netlist points.
func applyIPC356AOffset(netlist *IPC356ANetlist, ox, oy float64) {
	if ox == 0 && oy == 0 {
		return
	}
	for i := range netlist.Pads {
		netlist.Pads[i].X += ox
		netlist.Pads[i].Y += oy
	}
	for i := range netlist.Vias {
		netlist.Vias[i].X += ox
		netlist.Vias[i].Y += oy
	}
	for i := range netlist.Traces {
		for j := range netlist.Traces[i].Vertices {
			netlist.Traces[i].Vertices[j].X += ox
			netlist.Traces[i].Vertices[j].Y += oy
		}
	}
}

