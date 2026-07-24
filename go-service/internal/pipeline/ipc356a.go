package pipeline

import (
	"bufio"
	"fmt"
	"math"
	"regexp"
	"sort"
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
	DrillDia float64
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

	// Modal coordinate state carried across 078 continuation lines.
	lastX, lastY float64
	hasLastX     bool
	hasLastY     bool
}

// ipc356aUnitFactorDefault is the conversion from EasyEDA IPC-D-356A units
// to millimetres when no P UNITS directive is present (assumes CUST 0).
const ipc356aUnitFactorDefault = 0.00254

// parseIPC356APUnits scans the header for `P UNITS CUST N` and returns the
// corresponding conversion factor from IPC units to millimetres. Supported
// values: CUST 0 -> 0.00254 (0.0001 inch), CUST 1 -> 0.001 (1 micron).
// Unknown values fall back to the default with a warning.
func parseIPC356APUnits(text string) (float64, string) {
	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r\n")
		if len(line) < 3 || line[:3] != "P  " {
			continue
		}
		fields := wsRe.Split(strings.TrimSpace(line[3:]), -1)
		if len(fields) < 3 {
			continue
		}
		if strings.ToUpper(fields[0]) != "UNITS" {
			continue
		}
		if strings.ToUpper(fields[1]) != "CUST" {
			continue
		}
		n, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
		}
		switch n {
		case 0:
			return 0.00254, "CUST 0 (0.0001 inch)"
		case 1:
			return 0.001, "CUST 1 (metric)"
		default:
			return ipc356aUnitFactorDefault, fmt.Sprintf("CUST %d (unsupported, falling back to CUST 0)", n)
		}
	}
	return ipc356aUnitFactorDefault, "default (no P UNITS directive)"
}

// ParseIPC356A parses an IPC-D-356A text file.
func ParseIPC356A(text string) (*IPC356ANetlist, error) {
	nl := &IPC356ANetlist{}

	unitToMm, unitsTag := parseIPC356APUnits(text)

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
			pad, err := parseIPC356APad(line, unitToMm)
			if err == nil {
				nl.Pads = append(nl.Pads, pad)
			}
		case "317":
			pad, via, err := parseIPC356A317(line, unitToMm)
			if err == nil {
				if via != nil {
					nl.Vias = append(nl.Vias, *via)
				} else {
					nl.Pads = append(nl.Pads, pad)
				}
			}
		case "378":
			trace, err := parseIPC356ATrace(line, unitToMm)
			if err == nil {
				nl.Traces = append(nl.Traces, trace)
				lastTrace = &nl.Traces[len(nl.Traces)-1]
			}
		case "078":
			if lastTrace == nil {
				continue
			}
			pts, lx, ly, hx, hy, err := extractIPC356APathWithStart(line[3:], lastTrace.lastX, lastTrace.lastY, lastTrace.hasLastX, lastTrace.hasLastY, unitToMm)
			if err == nil {
				lastTrace.Vertices = append(lastTrace.Vertices, pts...)
				lastTrace.lastX, lastTrace.lastY = lx, ly
				lastTrace.hasLastX, lastTrace.hasLastY = hx, hy
			}
		case "389":
			pts, err := parseIPC356AVertices(line[3:], unitToMm)
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
	_ = unitsTag
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

func parseIPC356APad(line string, unitToMm float64) (IPC356APad, error) {
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

	// 327 records have the fixed layout: net refdes pin SHAPE X..Y..X..Y..R.. side.
	if len(parts) < 5 {
		return IPC356APad{}, fmt.Errorf("short 327 record")
	}
	shapeIdx := 3
	pad.Shape = parts[shapeIdx]

	side := parts[len(parts)-1]
	pad.Side = side
	pad.IsTHT = side == "S0" || side == "S3"

	// Concatenate everything between shape and side into one string.
	body := strings.Join(parts[shapeIdx+1:len(parts)-1], "")

	x, y, w, h, rot, err := parseIPC356AXYXYR(body)
	if err != nil {
		return pad, err
	}
	pad.X, pad.Y = x*unitToMm, y*unitToMm
	pad.Width, pad.Height = w*unitToMm, h*unitToMm
	pad.Rotation = rot
	return pad, nil
}

func parseIPC356A317(line string, unitToMm float64) (IPC356APad, *IPC356AVia, error) {
	parts := wsRe.Split(strings.TrimSpace(line[3:]), -1)
	if len(parts) < 5 {
		return IPC356APad{}, nil, fmt.Errorf("short 317 record")
	}
	net := parts[0]
	ref := parts[1]
	pin := parts[2]

	// 317 records have the fixed layout: net refdes pin DxxxSHAPEX X..Y.. side.
	diaIdx := 3
	if !strings.HasPrefix(parts[diaIdx], "D") {
		return IPC356APad{}, nil, fmt.Errorf("missing D token in 317 record")
	}

	// The D token embeds the drill diameter as a leading number, followed by a
	// shape code such as "PA00X". Extract only the numeric prefix.
	diaToken := parts[diaIdx]
	var drill float64
	for i := 1; i < len(diaToken); i++ {
		c := diaToken[i]
		if c < '0' || c > '9' {
			if i > 1 {
				drill, _ = strconv.ParseFloat(diaToken[1:i], 64)
			}
			break
		}
		if i == len(diaToken)-1 {
			drill, _ = strconv.ParseFloat(diaToken[1:], 64)
		}
	}

	// The coordinate body immediately follows the D token. There is no separate
	// shape token in EasyEDA's 317 records.
	side := parts[len(parts)-1]
	body := strings.Join(parts[diaIdx+1:len(parts)-1], "")
	if body == "" {
		return IPC356APad{}, nil, fmt.Errorf("empty coordinate body in 317 record")
	}

	x, y, w, h, rot, err := parseIPC356AXYXYR(body)
	if err != nil {
		return IPC356APad{}, nil, err
	}

	if strings.ToUpper(ref) == "VIA" || pin == "-" {
		via := IPC356AVia{
			Net:      net,
			RefDes:   ref,
			X:        x * unitToMm,
			Y:        y * unitToMm,
			DrillDia: drill * unitToMm,
			OuterDia: w * unitToMm,
			Side:     side,
		}
		return IPC356APad{}, &via, nil
	}

	pad := IPC356APad{
		Net:      net,
		RefDes:   ref,
		Pin:      pin,
		Shape:    diaToken,
		X:        x * unitToMm,
		Y:        y * unitToMm,
		Width:    w * unitToMm,
		Height:   h * unitToMm,
		DrillDia: drill * unitToMm,
		Rotation: rot,
		Side:     side,
		IsTHT:    side == "S0" || side == "S3",
	}
	return pad, nil, nil
}

// parseIPC356AXYXYR parses a string like "006138Y-005098X0585Y0680R000" or
// "002429Y-002848X0669Y" into x, y, width, height, rotation.
//
// IPC-D-356A coordinate bodies use value-then-suffix emission: each number is
// followed by its own X/Y/R suffix. EasyEDA's 327/317 records emit the values
// in the order: center Y, center X, width X, height Y, rotation R. When the
// height equals the width the trailing Y suffix may be omitted (e.g.
// "0669R000"), in which case the rotation suffix immediately follows the
// implicit Y value.
func parseIPC356AXYXYR(s string) (x, y, w, h, rot float64, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, 0, 0, 0, fmt.Errorf("empty coordinate body")
	}

	tokens, err := extractIPC356ASuffixCoords(s)
	if err != nil {
		return 0, 0, 0, 0, 0, err
	}
	if len(tokens) < 2 {
		return 0, 0, 0, 0, 0, fmt.Errorf("missing coordinates in %q", s)
	}

	// The first two explicit tokens are the center Y and center X in that
	// order. The leading marker "X" carried by the shape token (e.g. "PA01X")
	// is consumed by the caller before this function is invoked.
	for _, tk := range tokens[:2] {
		switch tk.suffix {
		case 'Y':
			if y == 0 {
				y = tk.value
			}
		case 'X':
			if x == 0 {
				x = tk.value
			}
		}
	}
	if y == 0 && x == 0 {
		return 0, 0, 0, 0, 0, fmt.Errorf("missing center coordinates in %q", s)
	}

	// Subsequent tokens supply width/height/rotation.
	for _, tk := range tokens[2:] {
		switch tk.suffix {
		case 'X':
			if w == 0 {
				w = tk.value
			}
		case 'Y':
			if h == 0 {
				h = tk.value
			}
		case 'R':
			rot = tk.value
		}
	}
	return x, y, w, h, rot, nil
}

// ipc356aSuffixCoord represents a single coordinate token from a body string,
// preserving the order in which they were emitted (the IPC body uses
// value-then-suffix, so the first emitted token is always the first number).
type ipc356aSuffixCoord struct {
	suffix byte
	value  float64
}

// extractIPC356ASuffixCoords parses a body like "006138Y-005098X0585Y0680R000"
// or "006491Y-005098X0585Y0689" (height before rotation may omit the Y
// suffix). The returned slice preserves emission order so callers can pair the
// first Y with the first X (the center coordinates) and treat subsequent
// tokens as width/height/rotation.
func extractIPC356ASuffixCoords(s string) ([]ipc356aSuffixCoord, error) {
	var out []ipc356aSuffixCoord
	i := 0
	for i < len(s) {
		c := s[i]
		if !(c == '-' || (c >= '0' && c <= '9')) {
			i++
			continue
		}
		j := i
		if s[j] == '-' {
			j++
		}
		hadDigits := false
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
			hadDigits = true
		}
		if !hadDigits {
			i++
			continue
		}
		v, err := strconv.ParseFloat(s[i:j], 64)
		if err != nil {
			return nil, err
		}
		suffix := byte(0)
		if j < len(s) {
			sj := s[j]
			if sj == 'X' || sj == 'Y' || sj == 'R' {
				suffix = sj
				j++
			}
		}
		out = append(out, ipc356aSuffixCoord{suffix: suffix, value: v})
		i = j
	}
	return out, nil
}

func parseIPC356ATrace(line string, unitToMm float64) (IPC356ATrace, error) {
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
			trace.Width = parseIPC356ANum(p[1:]) * unitToMm
			bodyStart = i + 1
			break
		}
	}

	body := strings.Join(parts[bodyStart:], "")
	pts, lx, ly, hx, hy, err := extractIPC356APathWithStart(body, 0, 0, false, false, unitToMm)
	if err != nil {
		return IPC356ATrace{}, err
	}
	trace.Vertices = pts
	trace.lastX, trace.lastY = lx, ly
	trace.hasLastX, trace.hasLastY = hx, hy
	return trace, nil
}

func parseIPC356AVertices(body string, unitToMm float64) ([]geometry.Point, error) {
	pts, _, _, _, _, err := extractIPC356APathWithStart(body, 0, 0, false, false, unitToMm)
	return pts, err
}

type ipc356aCoordToken struct {
	typ byte
	val float64
}

// extractIPC356ATokens scans the body for X/Y-prefixed signed integers.
func extractIPC356ATokens(body string) []ipc356aCoordToken {
	var tokens []ipc356aCoordToken
	for i := 0; i < len(body); {
		c := body[i]
		if c != 'X' && c != 'Y' {
			i++
			continue
		}
		j := i + 1
		for j < len(body) && (body[j] == '-' || (body[j] >= '0' && body[j] <= '9')) {
			j++
		}
		if j > i+1 {
			if v, err := strconv.ParseFloat(body[i+1:j], 64); err == nil {
				tokens = append(tokens, ipc356aCoordToken{typ: c, val: v})
			}
		}
		i = j
	}
	return tokens
}

// extractIPC356APathWithStart extracts vertices from an IPC-D-356A coordinate
// body. Each X token pairs with the following Y token; if an X has no following
// Y before the next X, it pairs with the carried Y. Likewise a Y without a
// preceding X pairs with the carried X. The last coordinate state is returned
// so 078 continuation lines can resume correctly.
func extractIPC356APathWithStart(body string, lastX, lastY float64, hasX, hasY bool, unitToMm float64) ([]geometry.Point, float64, float64, bool, bool, error) {
	tokens := extractIPC356ATokens(body)
	if len(tokens) == 0 {
		return nil, lastX, lastY, hasX, hasY, nil
	}

	// The first supplied coordinate is fresh; the opposite coordinate, if any,
	// is carried from the previous record.
	if tokens[0].typ == 'X' {
		hasX = false
	} else {
		hasY = false
	}

	var pts []geometry.Point
	for i, tk := range tokens {
		if tk.typ == 'X' {
			nextIsY := i+1 < len(tokens) && tokens[i+1].typ == 'Y'
			if !nextIsY && hasY {
				pts = append(pts, geometry.Point{X: tk.val * unitToMm, Y: lastY * unitToMm})
				lastX = tk.val
				hasX = true
				hasY = false
			} else {
				lastX = tk.val
				hasX = true
			}
		} else {
			if hasX {
				pts = append(pts, geometry.Point{X: lastX * unitToMm, Y: tk.val * unitToMm})
				lastY = tk.val
				hasY = true
				// Keep hasX true so repeated Y tokens share the same X.
			} else {
				lastY = tk.val
				hasY = true
			}
		}
	}
	return pts, lastX, lastY, hasX, hasY, nil
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
func inferPolygonNetsFromIPC356A(layers []*problem.Layer, netlist *IPC356ANetlist, gndNet string, d *DiagCollector) {
	if netlist == nil || len(layers) == 0 {
		return
	}

	sortedLayerNames := make([]string, len(layers))
	for i, l := range layers {
		sortedLayerNames[i] = l.Name
	}

	// Precompute polygon areas and sort indices by ascending area for each layer.
	// When a pad/trace point falls in an overlap region between a small pad/trace
	// polygon and a large plane, the smallest containing polygon is the more
	// specific net indicator and gets the vote.
	type layerPolyOrder struct {
		indices []int
		areas   []float64
	}
	polyOrders := make([]layerPolyOrder, len(layers))
	for li, l := range layers {
		indices := make([]int, len(l.Shape))
		areas := make([]float64, len(l.Shape))
		for i, poly := range l.Shape {
			indices[i] = i
			if len(poly) > 0 {
				areas[i] = math.Abs(poly[0].Area())
			}
		}
		sort.Slice(indices, func(i, j int) bool {
			return areas[indices[i]] < areas[indices[j]]
		})
		polyOrders[li] = layerPolyOrder{indices: indices, areas: areas}
	}

	findSmallestContaining := func(li int, pt geometry.Point) int {
		if li < 0 || li >= len(layers) {
			return -1
		}
		for _, pi := range polyOrders[li].indices {
			if pointInPolygonMesh(pt, layers[li].Shape[pi]) {
				return pi
			}
		}
		return -1
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
		// Check all layers and let polygon containment decide. This is robust
		// against S1/S2 side-code convention differences between IPC-D-356A
		// variants and avoids missing net labels when the pad physically sits
		// on a layer whose index does not match the side code.
		indices := make([]int, len(layers))
		for i := range layers {
			indices[i] = i
		}
		for _, li := range indices {
			if pi := findSmallestContaining(li, pt); pi >= 0 {
				voteFor(li, pi, pad.Net)
			}
			// THT pads sit in drilled holes; the surrounding copper (annular ring
			// or pour) belongs to the pad net. Sample around the hole at the
			// annular-ring midpoint so probes do not reach adjacent pads.
			if pad.IsTHT {
				outerR := math.Max(pad.Width, pad.Height) / 2
				drillR := pad.DrillDia / 2
				probeRadius := drillR + (outerR-drillR)*0.5
				if probeRadius <= 0 {
					probeRadius = 0.3
				}
				for _, probe := range thtAnnularProbes(pt, probeRadius) {
					if pi := findSmallestContaining(li, probe); pi >= 0 {
						voteFor(li, pi, pad.Net)
					}
				}
				// Also sample just outside the pad so the surrounding copper pour
				// gets the net vote even after pad holes are punched and restored
				// as separate polygons.
				outerProbeRadius := outerR + 0.05
				if outerProbeRadius > probeRadius {
					for _, probe := range thtAnnularProbes(pt, outerProbeRadius) {
						if pi := findSmallestContaining(li, probe); pi >= 0 {
							voteFor(li, pi, pad.Net)
						}
					}
				}
			}
		}
	}
	for _, via := range netlist.Vias {
		pt := geometry.Point{X: via.X, Y: via.Y}
		for li := range layers {
			if pi := findSmallestContaining(li, pt); pi >= 0 {
				voteFor(li, pi, via.Net)
			}
			outerR := via.OuterDia / 2
			drillR := via.DrillDia / 2
			probeRadius := drillR + (outerR-drillR)*0.5
			if probeRadius <= 0 {
				probeRadius = 0.3
			}
			for _, probe := range thtAnnularProbes(pt, probeRadius) {
				if pi := findSmallestContaining(li, probe); pi >= 0 {
					voteFor(li, pi, via.Net)
				}
			}
			// Outer probes to ensure the surrounding copper pour is labelled.
			outerProbeRadius := outerR + 0.05
			if outerProbeRadius > probeRadius {
				for _, probe := range thtAnnularProbes(pt, outerProbeRadius) {
					if pi := findSmallestContaining(li, probe); pi >= 0 {
						voteFor(li, pi, via.Net)
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
			if pi := findSmallestContaining(li, p); pi >= 0 {
				voteFor(li, pi, trace.Net)
			}
		}
		// Midpoints for multi-vertex traces.
		for i := 1; i < len(trace.Vertices); i++ {
			a, b := trace.Vertices[i-1], trace.Vertices[i]
			mid := geometry.Point{X: (a.X + b.X) / 2, Y: (a.Y + b.Y) / 2}
			if pi := findSmallestContaining(li, mid); pi >= 0 {
				voteFor(li, pi, trace.Net)
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

	// Heuristic fallback: the dominant (largest) polygon on each layer is usually
	// the ground/reference pour. If it is still unlabelled or only weakly labelled,
	// assign it the configured ground net so the solver has a stable reference.
	if gndNet != "" {
		for _, layer := range layers {
			if len(layer.Shape) == 0 {
				continue
			}
			largestIdx, largestArea := 0, 0.0
			for i, poly := range layer.Shape {
				if len(poly) == 0 {
					continue
				}
				if a := math.Abs(poly[0].Area()); a > largestArea {
					largestArea = a
					largestIdx = i
				}
			}
			if largestIdx < len(layer.NetLabels) {
				if layer.NetLabels[largestIdx] == "" {
					layer.NetLabels[largestIdx] = gndNet
					totalLabelled++
					d.Info(fmt.Sprintf("Layer '%s': assigned ground net '%s' to dominant polygon", layer.Name, gndNet))
				}
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

// padPolygon returns a conservative polygon for an IPC-D-356A pad. Rectangular
// pads use their width/height; round or square pads use a circle.
func padPolygon(pad IPC356APad) geometry.Polygon {
	w := pad.Width
	h := pad.Height
	if w <= 0 && h <= 0 {
		return nil
	}
	if w <= 0 {
		w = h
	}
	if h <= 0 {
		h = w
	}

	// If the pad is roughly square, treat it as round. This matches the typical
	// round/annular pads described by 317 records while still covering square pads.
	if math.Abs(w-h) < 1e-9*math.Max(w, h) {
		r := math.Max(w, h) / 2
		return circlePolygon(pad.X, pad.Y, r)
	}

	hw, hh := w/2, h/2
	corners := []geometry.Point{
		{X: -hw, Y: -hh},
		{X: hw, Y: -hh},
		{X: hw, Y: hh},
		{X: -hw, Y: hh},
		{X: -hw, Y: -hh},
	}
	rot := pad.Rotation * math.Pi / 180.0
	cos, sin := math.Cos(rot), math.Sin(rot)
	ring := make(geometry.Ring, len(corners))
	for i, p := range corners {
		rx := p.X*cos - p.Y*sin
		ry := p.X*sin + p.Y*cos
		ring[i] = geometry.Point{X: pad.X + rx, Y: pad.Y + ry}
	}
	return geometry.Polygon{ring}
}

// punchIPC356APadHoles subtracts pad/via copper shapes from each layer's largest
// polygon and adds the shapes back as separate polygons. Gerber copper pours are
// often exported without holes for pads; after Clipper2 union the pad copper is
// absorbed into the plane and net inference mislabels the plane. Restoring the
// pads as separate polygons fixes the net votes.
func punchIPC356APadHoles(layers []*problem.Layer, netlist *IPC356ANetlist, d *DiagCollector) {
	if netlist == nil || len(layers) == 0 {
		return
	}

	sortedNames := make([]string, len(layers))
	for i, l := range layers {
		sortedNames[i] = l.Name
	}

	type layerShape struct {
		layerIdx int
		shape    geometry.Polygon
		net      string
	}
	var shapes []layerShape
	for _, pad := range netlist.Pads {
		indices := sideLayerIndices(pad.Side, sortedNames)
		if len(indices) == 0 {
			indices = make([]int, len(layers))
			for i := range layers {
				indices[i] = i
			}
		}
		shape := padPolygon(pad)
		if shape == nil {
			continue
		}
		for _, li := range indices {
			shapes = append(shapes, layerShape{layerIdx: li, shape: shape, net: pad.Net})
		}
	}
	for _, via := range netlist.Vias {
		dia := via.OuterDia
		if dia <= 0 {
			dia = via.DrillDia
		}
		if dia <= 0 {
			continue
		}
		shape := circlePolygon(via.X, via.Y, dia/2)
		for li := range layers {
			shapes = append(shapes, layerShape{layerIdx: li, shape: shape, net: via.Net})
		}
	}
	if len(shapes) == 0 {
		return
	}

	for li, layer := range layers {
		var holes geometry.MultiPolygon
		var holeNets []string
		for _, s := range shapes {
			if s.layerIdx == li {
				holes = append(holes, s.shape)
				holeNets = append(holeNets, s.net)
			}
		}
		if len(holes) == 0 || len(layer.Shape) == 0 {
			continue
		}

		mergedHoles, err := geometry.Union(holes, nil)
		if err != nil || len(mergedHoles) == 0 {
			mergedHoles = holes
		}

		// Shrink the holes slightly so the recovered pad polygons overlap the
		// surrounding copper. This lets same-net pads merge back with the pour
		// during net-based union, while keeping different-net pads as separate
		// polygons with only a thin overlap ring in the preview.
		punchHoles := mergedHoles
		if shrunk, err := geometry.Offset(mergedHoles, -0.02); err == nil && len(shrunk) > 0 {
			punchHoles = shrunk
		}

		largestIdx := 0
		largestArea := layer.Shape[0].Area()
		for i := 1; i < len(layer.Shape); i++ {
			if a := layer.Shape[i].Area(); a > largestArea {
				largestArea = a
				largestIdx = i
			}
		}

		punched, err := geometry.Difference(geometry.MultiPolygon{layer.Shape[largestIdx]}, punchHoles)
		if err != nil || len(punched) == 0 {
			d.Warn(fmt.Sprintf("Layer '%s': IPC356A pad hole punch failed (%v)", layer.Name, err))
			continue
		}

		newShape := make(geometry.MultiPolygon, 0, len(layer.Shape)-1+len(punched)+len(holes))
		newLabels := make([]string, 0, len(layer.NetLabels)-1+len(punched)+len(holes))
		for i, poly := range layer.Shape {
			if i == largestIdx {
				continue
			}
			newShape = append(newShape, poly)
			if i < len(layer.NetLabels) {
				newLabels = append(newLabels, layer.NetLabels[i])
			} else {
				newLabels = append(newLabels, "")
			}
		}
		largestNet := ""
		if largestIdx < len(layer.NetLabels) {
			largestNet = layer.NetLabels[largestIdx]
		}
		newShape = append(newShape, punched...)
		for range punched {
			newLabels = append(newLabels, largestNet)
		}
		newShape = append(newShape, holes...)
		newLabels = append(newLabels, holeNets...)
		layer.Shape = newShape
		layer.NetLabels = newLabels
		d.Info(fmt.Sprintf("Layer '%s': punched %d IPC356A pad/via hole(s) -> %d polygon(s)", layer.Name, len(holes), len(newShape)))
	}
}

