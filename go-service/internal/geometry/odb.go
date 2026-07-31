package geometry

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"math"
	"path"
	"strconv"
	"strings"
)

type ODBData struct {
	Layers      map[string]Layer
	// AllLayers holds every copper polygon on the requested layers, regardless of
	// whether its net is part of the current analysis. It is used for the viewer's
	// full-board copper stencil so the rendered board does not look empty when
	// only a subset of nets is being solved.
	AllLayers   map[string]Layer
	DrillHoles  MultiPolygon
	DrillPoints []DrillPoint
}

type odbSymbol struct {
	kind          string
	width, height float64
	cornerRadius  float64
}

type odbFeature struct {
	index    int
	polygons MultiPolygon
}

func ParseODB(tgzBytes []byte, layerNames []string, targetNets map[string]bool) (*ODBData, error) {
	files, err := readODBTar(tgzBytes)
	if err != nil {
		return nil, err
	}

	componentRotations := make(map[string]map[string]float64)
	for _, key := range []string{"comp_+_top", "comp_+_bot"} {
		path := findODBFile(files, "/steps/", "/"+key+"/components")
		if path != "" {
			componentRotations[key] = parseODBComponents(string(files[path]))
		}
	}

	edaPath := findODBFile(files, "/steps/", "/eda/data")
	if edaPath == "" {
		return nil, fmt.Errorf("ODB++ eda/data not found")
	}
	layerOrder, _ := parseODBNetReferences(string(files[edaPath]), targetNets)
	// Build a second net-reference map that includes every net so the viewer can
	// render all copper as context, even nets that are not being solved.
	_, allRefs := parseODBNetReferences(string(files[edaPath]), nil)
	wanted := make(map[string]string, len(layerNames))
	for _, configured := range layerNames {
		wanted[normalizeODBName(configured)] = configured
	}

	result := &ODBData{Layers: make(map[string]Layer), AllLayers: make(map[string]Layer)}
	// Collect per-layer polygons and labels first, then propagate across layers
	// via vias, and finally build the Layer records for the solver and viewer.
	tmpPolygons := make(map[string]MultiPolygon)
	tmpLabels := make(map[string][]string)
	var configuredLayers []string
	for layerIndex, odbName := range layerOrder {
		configured, ok := wanted[normalizeODBName(odbName)]
		if !ok {
			continue
		}
		configuredLayers = append(configuredLayers, configured)
		featurePath := findODBLayerFile(files, odbName, "features")
		if featurePath == "" {
			continue
		}

		var padRotations map[string]float64
		if strings.Contains(strings.ToLower(featurePath), "/layers/top_layer/") {
			padRotations = componentRotations["comp_+_top"]
		} else if strings.Contains(strings.ToLower(featurePath), "/layers/bottom_layer/") {
			padRotations = componentRotations["comp_+_bot"]
		}

		// Parse every copper feature. Use all net labels (not just the target
		// nets) for cross-layer propagation so we do not accidentally weld
		// unrelated nets together when a via is shared.
		features, err := parseODBFeatures(string(files[featurePath]), nil, padRotations)
		if err != nil {
			return nil, fmt.Errorf("parse ODB++ layer %s: %w", odbName, err)
		}
		var polygons MultiPolygon
		var labels []string
		for _, feature := range features {
			net := allRefs[layerIndex][feature.index]
			for _, polygon := range feature.polygons {
				polygon.EnsureOrientation()
				polygons = append(polygons, polygon)
				labels = append(labels, net)
			}
		}
		tmpPolygons[configured] = polygons
		tmpLabels[configured] = labels
	}

	profilePath := findODBFile(files, "/steps/", "/profile")
	if profilePath != "" {
		features, err := parseODBFeatures(string(files[profilePath]), nil, nil)
		if err != nil {
			return nil, fmt.Errorf("parse ODB++ profile: %w", err)
		}
		var outline MultiPolygon
		for _, feature := range features {
			outline = append(outline, feature.polygons...)
		}
		result.Layers["board_outline_layer"] = Layer{
			Name: "board_outline_layer", Polygons: outline,
		}
	}

	for filename, contents := range files {
		if !strings.HasSuffix(filename, "/features") || !strings.Contains(filename, "/layers/") {
			continue
		}
		layerName := path.Base(path.Dir(filename))
		if !strings.Contains(strings.ToLower(layerName), "drill") {
			continue
		}
		tools := parseODBTools(string(files[path.Join(path.Dir(filename), "tools")]))
		holes, points, err := parseODBDrills(string(contents), tools)
		if err != nil {
			return nil, fmt.Errorf("parse ODB++ drill layer %s: %w", layerName, err)
		}
		result.DrillHoles = append(result.DrillHoles, holes...)
		result.DrillPoints = append(result.DrillPoints, points...)
	}

	// Propagate net labels across layers through plated via holes. ODB++ only
	// labels a small set of representative features (pads, traces); the
	// surrounding copper pours and via-connected islands are usually unlabeled.
	// Without propagation the solver drops large areas of the analysed net and
	// the viewer omits via-connected islands.
	propagatedLabels := propagateNetLabelsWithVias(configuredLayers, tmpPolygons, tmpLabels, result.DrillPoints, nil)

	// AllLayers: every copper polygon on the requested layers, with its propagated
	// net label. Unlabeled copper remains empty-string so the viewer can still
	// render it as board context.
	for _, name := range configuredLayers {
		polys := tmpPolygons[name]
		if len(polys) == 0 {
			continue
		}
		result.AllLayers[name] = Layer{
			Name:      name,
			Polygons:  polys,
			NetLabels: propagatedLabels[name],
		}
	}

	// Layers: only polygons whose propagated net is in the target net set.
	for _, name := range configuredLayers {
		allPolys := tmpPolygons[name]
		allLabs := propagatedLabels[name]
		if len(allPolys) == 0 {
			continue
		}
		var selectedPolys MultiPolygon
		var selectedLabels []string
		for i, poly := range allPolys {
			label := ""
			if i < len(allLabs) {
				label = allLabs[i]
			}
			if targetNets != nil && targetNets[label] {
				selectedPolys = append(selectedPolys, poly)
				selectedLabels = append(selectedLabels, label)
			}
		}
		if len(selectedPolys) == 0 {
			continue
		}
		result.Layers[name] = Layer{
			Name:      name,
			Polygons:  selectedPolys,
			NetLabels: selectedLabels,
		}
	}

	return result, nil
}

func readODBTar(data []byte) (map[string][]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("open ODB++ gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	files := make(map[string][]byte)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read ODB++ tar: %w", err)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			continue
		}
		contents, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("read ODB++ entry %s: %w", header.Name, err)
		}
		files[path.Clean(strings.TrimPrefix(header.Name, "./"))] = contents
	}
	return files, nil
}

func findODBFile(files map[string][]byte, containsPart, suffix string) string {
	for filename := range files {
		lower := strings.ToLower(filename)
		if strings.Contains(lower, containsPart) && strings.HasSuffix(lower, suffix) {
			return filename
		}
	}
	return ""
}

func findODBLayerFile(files map[string][]byte, layerName, basename string) string {
	suffix := "/layers/" + strings.ToLower(layerName) + "/" + basename
	for filename := range files {
		if strings.HasSuffix(strings.ToLower(filename), suffix) {
			return filename
		}
	}
	return ""
}

func normalizeODBName(value string) string {
	value = strings.ToLower(value)
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func parseODBNetReferences(data string, targetNets map[string]bool) ([]string, map[int]map[int]string) {
	var layerOrder []string
	refs := make(map[int]map[int]string)
	currentNet := ""
	selected := false
	for _, raw := range strings.Split(strings.ReplaceAll(data, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "LYR":
			layerOrder = append(layerOrder, fields[1:]...)
		case "NET":
			currentNet = strings.TrimSpace(strings.TrimPrefix(line, "NET"))
			selected = len(targetNets) == 0 || targetNets[currentNet]
		case "FID":
			if !selected || currentNet == "" || len(fields) < 4 || fields[1] != "C" {
				continue
			}
			layerIndex, err1 := strconv.Atoi(fields[2])
			featureIndex, err2 := strconv.Atoi(fields[3])
			if err1 != nil || err2 != nil || layerIndex < 0 || layerIndex >= len(layerOrder) {
				continue
			}
			if refs[layerIndex] == nil {
				refs[layerIndex] = make(map[int]string)
			}
			refs[layerIndex][featureIndex] = currentNet
		}
	}
	return layerOrder, refs
}

// parseODBComponents reads a component placement file (comp_+_top/components or
// comp_+_bot/components) and returns a map from pad centre coordinates to the
// parent component's rotation in degrees. Pad features in the copper layer
// use a running dcode in their rotation field instead of the actual angle; the
// real orientation comes from the component record.
func parseODBComponents(data string) map[string]float64 {
	rotations := make(map[string]float64)
	for _, raw := range strings.Split(strings.ReplaceAll(data, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[0] != "TOP" {
			continue
		}
		x, ok1 := odbFloat(fields[2])
		y, ok2 := odbFloat(fields[3])
		rotation, err := strconv.ParseFloat(fields[4], 64)
		if !ok1 || !ok2 || err != nil {
			continue
		}
		key := fmt.Sprintf("%.6f|%.6f", x, y)
		rotations[key] = rotation
	}
	return rotations
}

func padRotationAt(padRotations map[string]float64, x, y float64) float64 {
	if padRotations == nil {
		return 0
	}
	key := fmt.Sprintf("%.6f|%.6f", x, y)
	if rot, ok := padRotations[key]; ok {
		return rot
	}
	// Fall back to the nearest component pad within a small tolerance in case
	// the ODB exporter rounds coordinates differently between the copper and
	// component files.
	bestKey := ""
	bestTol := 1e9
	for k, rot := range padRotations {
		var px, py float64
		if _, err := fmt.Sscanf(k, "%f|%f", &px, &py); err != nil {
			continue
		}
		dx, dy := px-x, py-y
		if t := dx*dx + dy*dy; t < bestTol {
			bestTol = t
			bestKey = k
			_ = rot
		}
	}
	if bestTol < 1e-4 {
		return padRotations[bestKey]
	}
	return 0
}

func parseODBFeatures(data string, selected map[int]string, padRotations map[string]float64) ([]odbFeature, error) {
	lines := strings.Split(strings.ReplaceAll(data, "\r\n", "\n"), "\n")
	symbols := make(map[int]odbSymbol)
	inFeatures := false
	featureIndex := 0
	var features []odbFeature
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "#Layer features" {
			inFeatures = true
			continue
		}
		if !inFeatures {
			if strings.HasPrefix(line, "$") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					idx, err := strconv.Atoi(strings.TrimPrefix(fields[0], "$"))
					if err == nil {
						symbols[idx] = parseODBSymbol(fields[1])
					}
				}
			}
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		include := selected == nil
		if selected != nil {
			_, include = selected[featureIndex]
		}
		var polygons MultiPolygon
		switch {
		case strings.HasPrefix(line, "S "):
			start := i
			for i+1 < len(lines) && strings.TrimSpace(lines[i]) != "SE" {
				i++
			}
			if include {
				polygons = parseODBSurface(lines[start : i+1])
			}
		case strings.HasPrefix(line, "L "):
			if include {
				polygons = parseODBLine(line, symbols)
			}
		case strings.HasPrefix(line, "A "):
			if include {
				polygons = parseODBArc(line, symbols)
			}
		case strings.HasPrefix(line, "P "):
			if include {
				polygons = parseODBPad(line, symbols, padRotations)
			}
		default:
			continue
		}
		if include && len(polygons) > 0 {
			features = append(features, odbFeature{index: featureIndex, polygons: polygons})
		}
		featureIndex++
	}
	return features, nil
}

func parseODBSymbol(value string) odbSymbol {
	lower := strings.ToLower(value)
	parse := func(v string) float64 {
		f, _ := strconv.ParseFloat(v, 64)
		return f / 1000
	}
	if strings.HasPrefix(lower, "rect") {
		body := strings.TrimPrefix(lower, "rect")
		// ODB++ rounded-rectangle symbol: rect<w>x<h>xr<radius> (radius may be
		// followed by extra fields). Try the rounded form first.
		if idx := strings.Index(body, "xr"); idx > 0 {
			before := body[:idx]
			after := body[idx+2:]
			// after may be "<radius>x<extra>..." or just "<radius>".
			if endIdx := strings.Index(after, "x"); endIdx >= 0 {
				after = after[:endIdx]
			}
			parts := strings.Split(before, "x")
			if len(parts) >= 2 {
				return odbSymbol{kind: "rect", width: parse(parts[0]), height: parse(parts[1]), cornerRadius: parse(after)}
			}
		}
		parts := strings.Split(body, "x")
		if len(parts) >= 3 {
			return odbSymbol{kind: "rect", width: parse(parts[0]), height: parse(parts[1]), cornerRadius: parse(parts[2])}
		}
		if len(parts) >= 2 {
			return odbSymbol{kind: "rect", width: parse(parts[0]), height: parse(parts[1])}
		}
	}
	if strings.HasPrefix(lower, "oval") {
		parts := strings.Split(strings.TrimPrefix(lower, "oval"), "x")
		if len(parts) >= 2 {
			return odbSymbol{kind: "oval", width: parse(parts[0]), height: parse(parts[1])}
		}
	}
	if strings.HasPrefix(lower, "r") {
		d := parse(strings.TrimPrefix(lower, "r"))
		return odbSymbol{kind: "round", width: d, height: d}
	}
	if strings.HasPrefix(lower, "s") {
		d := parse(strings.TrimPrefix(lower, "s"))
		return odbSymbol{kind: "rect", width: d, height: d}
	}
	return odbSymbol{}
}

func parseODBLine(line string, symbols map[int]odbSymbol) MultiPolygon {
	fields := strings.Fields(line)
	if len(fields) < 8 || fields[6] != "P" {
		return nil
	}
	x1, ok1 := odbFloat(fields[1])
	y1, ok2 := odbFloat(fields[2])
	x2, ok3 := odbFloat(fields[3])
	y2, ok4 := odbFloat(fields[4])
	symbolIndex, err := strconv.Atoi(fields[5])
	if !ok1 || !ok2 || !ok3 || !ok4 || err != nil {
		return nil
	}
	width := symbols[symbolIndex].width
	if width <= 0 {
		return nil
	}
	return MultiPolygon{{capsuleRing(Point{x1, y1}, Point{x2, y2}, width/2)}}
}

func parseODBArc(line string, symbols map[int]odbSymbol) MultiPolygon {
	fields := strings.Fields(line)
	if len(fields) < 11 || fields[8] != "P" {
		return nil
	}
	values := make([]float64, 6)
	for i := range values {
		v, ok := odbFloat(fields[i+1])
		if !ok {
			return nil
		}
		values[i] = v
	}
	symbolIndex, err := strconv.Atoi(fields[7])
	if err != nil || symbols[symbolIndex].width <= 0 {
		return nil
	}
	centerline := sampleODBArc(Point{values[0], values[1]}, Point{values[2], values[3]}, Point{values[4], values[5]}, fields[10])
	return MultiPolygon{{polylineStrokeRing(centerline, symbols[symbolIndex].width/2)}}
}

func parseODBPad(line string, symbols map[int]odbSymbol, padRotations map[string]float64) MultiPolygon {
	fields := strings.Fields(line)
	if len(fields) < 7 || fields[4] != "P" {
		return nil
	}
	x, ok1 := odbFloat(fields[1])
	y, ok2 := odbFloat(fields[2])
	symbolIndex, err := strconv.Atoi(fields[3])
	if !ok1 || !ok2 || err != nil {
		return nil
	}
	var rotation float64
	if padRotations != nil {
		rotation = padRotationAt(padRotations, x, y)
	} else {
		rotation, _ = strconv.ParseFloat(fields[5], 64)
	}
	symbol := symbols[symbolIndex]
	ring := odbSymbolRing(symbol, Point{x, y})
	if len(ring) < 3 {
		return nil
	}
	rotateRing(ring, Point{x, y}, rotation*math.Pi/180)
	return MultiPolygon{{ring}}
}

func odbSymbolRing(symbol odbSymbol, center Point) Ring {
	switch symbol.kind {
	case "round":
		return circleRing(center, symbol.width/2, 24)
	case "rect":
		hw, hh := symbol.width/2, symbol.height/2
		if symbol.cornerRadius > 0 {
			return roundedRectRing(center, hw*2, hh*2, symbol.cornerRadius)
		}
		return Ring{{center.X - hw, center.Y - hh}, {center.X + hw, center.Y - hh}, {center.X + hw, center.Y + hh}, {center.X - hw, center.Y + hh}}
	case "oval":
		if symbol.width >= symbol.height {
			d := (symbol.width - symbol.height) / 2
			return capsuleRing(Point{center.X - d, center.Y}, Point{center.X + d, center.Y}, symbol.height/2)
		}
		d := (symbol.height - symbol.width) / 2
		return capsuleRing(Point{center.X, center.Y - d}, Point{center.X, center.Y + d}, symbol.width/2)
	}
	return nil
}

func parseODBSurface(lines []string) MultiPolygon {
	var polygons MultiPolygon
	var polygon Polygon
	var ring Ring
	var ringHole bool
	var current Point
	finishRing := func() {
		if len(ring) < 3 {
			ring = nil
			return
		}
		if ringHole && len(polygon) > 0 {
			polygon = append(polygon, ring)
		} else {
			if len(polygon) > 0 {
				polygons = append(polygons, polygon)
			}
			polygon = Polygon{ring}
		}
		ring = nil
	}
	for _, raw := range lines {
		fields := strings.Fields(strings.TrimSpace(raw))
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "OB":
			finishRing()
			if len(fields) >= 3 {
				x, ok1 := odbFloat(fields[1])
				y, ok2 := odbFloat(fields[2])
				if ok1 && ok2 {
					ringHole = len(fields) >= 4 && strings.EqualFold(fields[3], "H")
					current = Point{x, y}
					ring = Ring{current}
				}
			}
		case "OS":
			if len(fields) >= 3 {
				x, ok1 := odbFloat(fields[1])
				y, ok2 := odbFloat(fields[2])
				if ok1 && ok2 {
					current = Point{x, y}
					ring = append(ring, current)
				}
			}
		case "OC":
			if len(fields) >= 6 {
				ex, ok1 := odbFloat(fields[1])
				ey, ok2 := odbFloat(fields[2])
				cx, ok3 := odbFloat(fields[3])
				cy, ok4 := odbFloat(fields[4])
				if ok1 && ok2 && ok3 && ok4 {
					arc := sampleODBArc(current, Point{ex, ey}, Point{cx, cy}, fields[5])
					if len(arc) > 1 {
						ring = append(ring, arc[1:]...)
					}
					current = Point{ex, ey}
				}
			}
		case "OE":
			finishRing()
		}
	}
	finishRing()
	if len(polygon) > 0 {
		polygons = append(polygons, polygon)
	}
	for i := range polygons {
		polygons[i].EnsureOrientation()
	}
	return polygons
}

func sampleODBArc(start, end, center Point, direction string) []Point {
	radius := math.Hypot(start.X-center.X, start.Y-center.Y)
	if radius <= 1e-12 {
		return []Point{start, end}
	}
	startAngle := math.Atan2(start.Y-center.Y, start.X-center.X)
	endAngle := math.Atan2(end.Y-center.Y, end.X-center.X)
	sweep := endAngle - startAngle
	clockwise := strings.EqualFold(direction, "Y")
	if clockwise {
		for sweep >= 0 {
			sweep -= 2 * math.Pi
		}
	} else {
		for sweep <= 0 {
			sweep += 2 * math.Pi
		}
	}
	segments := int(math.Ceil(math.Abs(sweep) * radius / 0.02))
	if segments < 8 {
		segments = 8
	}
	if segments > 256 {
		segments = 256
	}
	points := make([]Point, segments+1)
	for i := range points {
		a := startAngle + sweep*float64(i)/float64(segments)
		points[i] = Point{center.X + radius*math.Cos(a), center.Y + radius*math.Sin(a)}
	}
	points[0], points[len(points)-1] = start, end
	return points
}

func capsuleRing(a, b Point, radius float64) Ring {
	if radius <= 0 {
		return nil
	}
	if math.Hypot(b.X-a.X, b.Y-a.Y) <= 1e-12 {
		return circleRing(a, radius, 20)
	}
	angle := math.Atan2(b.Y-a.Y, b.X-a.X)
	ring := make(Ring, 0, 22)
	for i := 0; i <= 10; i++ {
		theta := angle - math.Pi/2 + math.Pi*float64(i)/10
		ring = append(ring, Point{b.X + radius*math.Cos(theta), b.Y + radius*math.Sin(theta)})
	}
	for i := 0; i <= 10; i++ {
		theta := angle + math.Pi/2 + math.Pi*float64(i)/10
		ring = append(ring, Point{a.X + radius*math.Cos(theta), a.Y + radius*math.Sin(theta)})
	}
	return ring
}

func roundedRectRing(center Point, width, height, cornerRadius float64) Ring {
	r := cornerRadius
	if r <= 0 {
		hw, hh := width/2, height/2
		return Ring{{center.X - hw, center.Y - hh}, {center.X + hw, center.Y - hh}, {center.X + hw, center.Y + hh}, {center.X - hw, center.Y + hh}}
	}
	// Clamp radius so it does not exceed half width/height.
	maxR := math.Min(width/2, height/2)
	if r > maxR {
		r = maxR
	}
	hw, hh := width/2-r, height/2-r
	// Number of segments per quarter circle: scale by radius so tiny radii
	// still look rounded while large ones stay smooth.
	segments := int(math.Ceil(math.Pi/2 * r / 0.02))
	if segments < 4 {
		segments = 4
	}
	if segments > 32 {
		segments = 32
	}
	makeCorner := func(cx, cy float64, startAngle float64) Ring {
		corner := make(Ring, segments+1)
		for i := 0; i <= segments; i++ {
			a := startAngle + math.Pi/2*float64(i)/float64(segments)
			corner[i] = Point{cx + r*math.Cos(a), cy + r*math.Sin(a)}
		}
		return corner
	}
	// Four corners: top-right, top-left, bottom-left, bottom-right (Y up).
	var ring Ring
	ring = append(ring, makeCorner(center.X+hw, center.Y+hh, 0)...)
	ring = append(ring, makeCorner(center.X-hw, center.Y+hh, math.Pi/2)...)
	ring = append(ring, makeCorner(center.X-hw, center.Y-hh, math.Pi)...)
	ring = append(ring, makeCorner(center.X+hw, center.Y-hh, 3*math.Pi/2)...)
	return ring
}

func polylineStrokeRing(points []Point, radius float64) Ring {
	if len(points) < 2 || radius <= 0 {
		return nil
	}
	left := make(Ring, 0, len(points))
	right := make(Ring, 0, len(points))
	for i, p := range points {
		var dx, dy float64
		switch i {
		case 0:
			dx, dy = points[1].X-p.X, points[1].Y-p.Y
		case len(points) - 1:
			dx, dy = p.X-points[i-1].X, p.Y-points[i-1].Y
		default:
			dx, dy = points[i+1].X-points[i-1].X, points[i+1].Y-points[i-1].Y
		}
		length := math.Hypot(dx, dy)
		if length == 0 {
			continue
		}
		nx, ny := -dy/length*radius, dx/length*radius
		left = append(left, Point{p.X + nx, p.Y + ny})
		right = append(right, Point{p.X - nx, p.Y - ny})
	}
	ring := append(Ring(nil), left...)
	for i := len(right) - 1; i >= 0; i-- {
		ring = append(ring, right[i])
	}
	return ring
}

func circleRing(center Point, radius float64, segments int) Ring {
	ring := make(Ring, segments)
	for i := range ring {
		a := 2 * math.Pi * float64(i) / float64(segments)
		ring[i] = Point{center.X + radius*math.Cos(a), center.Y + radius*math.Sin(a)}
	}
	return ring
}

func rotateRing(ring Ring, center Point, angle float64) {
	if angle == 0 {
		return
	}
	c, s := math.Cos(angle), math.Sin(angle)
	for i, point := range ring {
		x, y := point.X-center.X, point.Y-center.Y
		ring[i] = Point{center.X + x*c - y*s, center.Y + x*s + y*c}
	}
}

func odbFloat(value string) (float64, bool) {
	v, err := strconv.ParseFloat(value, 64)
	return v, err == nil
}

func parseODBTools(data string) map[int]string {
	tools := make(map[int]string)
	current := -1
	for _, raw := range strings.Split(strings.ReplaceAll(data, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "NUM=") {
			current, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "NUM=")))
		}
		if current >= 0 && strings.HasPrefix(line, "TYPE=") {
			tools[current] = strings.TrimSpace(strings.TrimPrefix(line, "TYPE="))
		}
	}
	return tools
}

func parseODBDrills(data string, tools map[int]string) (MultiPolygon, []DrillPoint, error) {
	lines := strings.Split(strings.ReplaceAll(data, "\r\n", "\n"), "\n")
	symbols := make(map[int]odbSymbol)
	inFeatures := false
	var holes MultiPolygon
	var points []DrillPoint
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "#Layer features" {
			inFeatures = true
			continue
		}
		if !inFeatures && strings.HasPrefix(line, "$") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				idx, err := strconv.Atoi(strings.TrimPrefix(fields[0], "$"))
				if err == nil {
					symbols[idx] = parseODBSymbol(fields[1])
				}
			}
			continue
		}
		if !inFeatures || !strings.HasPrefix(line, "P ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 7 {
			continue
		}
		x, ok1 := odbFloat(fields[1])
		y, ok2 := odbFloat(fields[2])
		symbolIndex, err1 := strconv.Atoi(fields[3])
		toolNumber, err2 := strconv.Atoi(fields[5])
		if !ok1 || !ok2 || err1 != nil || err2 != nil {
			continue
		}
		diameter := symbols[symbolIndex].width
		if diameter <= 0 {
			continue
		}
		center := Point{x, y}
		holes = append(holes, Polygon{circleRing(center, diameter/2, 24)})
		points = append(points, DrillPoint{X: x, Y: y, Diameter: diameter, Via: strings.EqualFold(tools[toolNumber], "VIA")})
	}
	return holes, points, nil
}

// propagateNetLabels takes a set of polygons and their net labels and fills in
// labels for unlabeled polygons that are geometrically connected to labeled
// ones. ODB++ only annotates a few representative features (pads, traces) with a
// net name; the surrounding copper pour polygons are often unlabeled. Without
// propagation the solver treats those pours as separate/empty nets and the
// preview omits large areas of the analysed net.
func propagateNetLabels(polygons MultiPolygon, labels []string) []string {
	n := len(polygons)
	if n == 0 {
		return labels
	}
	_, out := connectedComponents(polygons, labels, nil)
	return out
}

// propagateNetLabelsWithVias propagates net labels both within each layer and
// across layers through plated via holes. This is needed because ODB++ only
// labels a few representative features (pads/traces) with a net name; the
// surrounding copper pours and via-connected islands on other layers would
// otherwise be left unlabeled and dropped from the solve / viewer.
func propagateNetLabelsWithVias(layers []string, polygons map[string]MultiPolygon, labels map[string][]string, drills []DrillPoint, allowedNets map[string]bool) map[string][]string {
	type compKey struct {
		layerIdx int
		compIdx  int
	}

	// Per-layer connected components.
	layerComponents := make([][][]int, len(layers))
	layerLabels := make([][]string, len(layers))
	layerMembers := make([][][]int, len(layers))
	for li, name := range layers {
		ps := polygons[name]
		ls := labels[name]
		comps, compLabels := connectedComponents(ps, ls, nil)
		layerComponents[li] = comps
		layerLabels[li] = compLabels
		members := make([][]int, len(comps))
		for ci, comp := range comps {
			members[ci] = comp
		}
		layerMembers[li] = members
	}

	// Union-Find over components keyed by (layer, component index).
	parent := make(map[compKey]compKey)
	find := func(k compKey) compKey {
		for {
			p, ok := parent[k]
			if !ok || p == k {
				return k
			}
			// path compression
			parent[k] = parent[p]
			parent[p] = p
			k = parent[k]
		}
	}
	union := func(a, b compKey) {
		pa, pb := find(a), find(b)
		if pa != pb {
			parent[pa] = pb
		}
	}

	// componentLabel returns the (possibly empty) label of a component. Empty
	// means either the component had no labeled polygon or it contained multiple
	// conflicting labels, in which case we must not propagate across it.
	componentLabel := func(k compKey) string {
		return layerLabels[k.layerIdx][k.compIdx]
	}

	// Connect components across layers via vias. Only merge components whose
	// labels are compatible (same label or one is empty). Conflicting labels
	// indicate different nets and must not be welded together.
	for _, d := range drills {
		if !d.Via {
			continue
		}
		pt := Point{d.X, d.Y}
		var found []compKey
		for li, name := range layers {
			ps := polygons[name]
			for ci := range layerComponents[li] {
				members := layerMembers[li][ci]
				if len(members) == 0 {
					continue
				}
				bounds := ps[members[0]].Bounds()
				for _, mi := range members[1:] {
					b := ps[mi].Bounds()
					if b.MinX < bounds.MinX { bounds.MinX = b.MinX }
					if b.MinY < bounds.MinY { bounds.MinY = b.MinY }
					if b.MaxX > bounds.MaxX { bounds.MaxX = b.MaxX }
					if b.MaxY > bounds.MaxY { bounds.MaxY = b.MaxY }
				}
				if pt.X < bounds.MinX || pt.X > bounds.MaxX || pt.Y < bounds.MinY || pt.Y > bounds.MaxY {
					continue
				}
				for _, mi := range members {
					if polygonContainsPoint(ps[mi], pt) {
						found = append(found, compKey{layerIdx: li, compIdx: ci})
						break
					}
				}
			}
		}
		for i := 1; i < len(found); i++ {
			a, b := found[0], found[i]
			la, lb := componentLabel(a), componentLabel(b)
			if la != "" && lb != "" && la != lb {
				continue
			}
			union(a, b)
		}
	}

	// Aggregate per-group labels.
	groups := make(map[compKey][]compKey)
	for li, comps := range layerComponents {
		for ci := range comps {
			k := compKey{layerIdx: li, compIdx: ci}
			root := find(k)
			groups[root] = append(groups[root], k)
		}
	}

	out := make(map[string][]string)
	for _, name := range layers {
		ps := polygons[name]
		ls := labels[name]
		res := make([]string, len(ps))
		for pi := range ps {
			res[pi] = ls[pi]
		}
		out[name] = res
	}

	for _, group := range groups {
		labelCounts := make(map[string]int)
		for _, k := range group {
			for _, mi := range layerMembers[k.layerIdx][k.compIdx] {
				label := labels[layers[k.layerIdx]][mi]
				if label == "" {
					continue
				}
				if allowedNets != nil && !allowedNets[label] {
					continue
				}
				labelCounts[label]++
			}
		}
		bestLabel := ""
		bestCount := 0
		distinct := 0
		for label, cnt := range labelCounts {
			if cnt > bestCount {
				bestCount = cnt
				bestLabel = label
			}
			if cnt > 0 {
				distinct++
			}
		}
		if distinct != 1 {
			continue
		}
		for _, k := range group {
			for _, mi := range layerMembers[k.layerIdx][k.compIdx] {
				out[layers[k.layerIdx]][mi] = bestLabel
			}
		}
	}

	return out
}

// selectConnectedNets keeps only polygons that are geometrically connected to a
// selected net. It also propagates the selected net label to any unlabeled
// polygons in the same connected component. This prevents the solver from
// wasting time on signal-net copper while still including unlabeled copper
// pours that touch the nets being analysed.
func selectConnectedNets(polygons MultiPolygon, labels []string, selected map[string]bool) (MultiPolygon, []string) {
	n := len(polygons)
	if n == 0 {
		return polygons, labels
	}
	components, componentLabels := connectedComponents(polygons, labels, selected)
	if len(components) == 0 {
		return nil, nil
	}
	var outPolys MultiPolygon
	var outLabels []string
	for ci, comp := range components {
		label := componentLabels[ci]
		if label == "" {
			continue
		}
		for _, i := range comp {
			outPolys = append(outPolys, polygons[i])
			outLabels = append(outLabels, label)
		}
	}
	return outPolys, outLabels
}

// connectedComponents returns the connected components of a polygon set and a
// label for each component. If selected is non-nil, a component is only
// kept (label non-empty) if it contains one of the selected nets; unlabeled
// polygons in such a component receive that net label. If selected is nil,
// every component is kept and labels are propagated from any labeled polygon
// to the unlabeled ones in the same component.
func connectedComponents(polygons MultiPolygon, labels []string, selected map[string]bool) ([][]int, []string) {
	n := len(polygons)
	if n == 0 {
		return nil, nil
	}
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[rb] = ra
		}
	}

	boxes := make([]Box, n)
	for i, p := range polygons {
		boxes[i] = p.Bounds()
	}

	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if !boxesOverlap(boxes[i], boxes[j]) {
				continue
			}
			if !polygonsTouch(polygons[i], polygons[j]) {
				continue
			}
			// Only merge polygons whose labels are compatible. Two polygons with
			// different explicit net labels must not be merged, or a large ground
			// plane will swallow smaller power-net islands during propagation.
			li, lj := "", ""
			if i < len(labels) {
				li = labels[i]
			}
			if j < len(labels) {
				lj = labels[j]
			}
			if li != "" && lj != "" && li != lj {
				continue
			}
			union(i, j)
		}
	}

	rootToComp := make(map[int]int)
	var components [][]int
	componentLabelCounts := make([]map[string]int, 0)
	componentSelected := make([]bool, 0)
	for i := 0; i < n; i++ {
		root := find(i)
		idx, ok := rootToComp[root]
		if !ok {
			idx = len(components)
			rootToComp[root] = idx
			components = append(components, nil)
			componentLabelCounts = append(componentLabelCounts, make(map[string]int))
			componentSelected = append(componentSelected, false)
		}
		components[idx] = append(components[idx], i)
		label := ""
		if i < len(labels) {
			label = labels[i]
		}
		if label != "" {
			componentLabelCounts[idx][label]++
			if selected != nil && selected[label] {
				componentSelected[idx] = true
			}
		}
	}

	componentLabels := make([]string, len(components))
	for ci, counts := range componentLabelCounts {
		if selected != nil && !componentSelected[ci] {
			continue
		}
		// A component may contain polygons with different labels when an
		// unlabeled polygon bridges two different labeled nets. In that case
		// do not propagate either label; keep the original labels.
		distinct := 0
		bestLabel := ""
		bestCount := 0
		for label, cnt := range counts {
			if selected != nil && !selected[label] {
				continue
			}
			if cnt > bestCount {
				bestCount = cnt
				bestLabel = label
			}
			if cnt > 0 {
				distinct++
			}
		}
		if distinct == 1 {
			componentLabels[ci] = bestLabel
		}
	}

	return components, componentLabels
}

func boxesOverlap(a, b Box) bool {
	return a.MinX <= b.MaxX && a.MaxX >= b.MinX && a.MinY <= b.MaxY && a.MaxY >= b.MinY
}

func polygonsTouch(a, b Polygon) bool {
	// Edge/vertex intersection for touching polygons.
	for _, ar := range a {
		for i := 0; i < len(ar); i++ {
			p1 := ar[i]
			p2 := ar[(i+1)%len(ar)]
			for _, br := range b {
				for j := 0; j < len(br); j++ {
					q1 := br[j]
					q2 := br[(j+1)%len(br)]
					if segmentsIntersect(p1, p2, q1, q2) {
						return true
					}
				}
			}
		}
	}
	return false
}

func polygonContainsPoint(p Polygon, pt Point) bool {
	if len(p) == 0 || len(p[0]) == 0 {
		return false
	}
	if !ringContainsPoint(p[0], pt) {
		return false
	}
	for i := 1; i < len(p); i++ {
		if ringContainsPoint(p[i], pt) {
			return false
		}
	}
	return true
}

func ringContainsPoint(r Ring, pt Point) bool {
	n := len(r)
	if n < 3 {
		return false
	}
	inside := false
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		pi, pj := r[i], r[j]
		if ((pi.Y > pt.Y) != (pj.Y > pt.Y)) &&
			(pt.X < (pj.X-pi.X)*(pt.Y-pi.Y)/(pj.Y-pi.Y)+pi.X) {
			inside = !inside
		}
	}
	return inside
}

func segmentsIntersect(a1, a2, b1, b2 Point) bool {
	// Orientation of ordered triplet (p, q, r): positive if counter-clockwise.
	orient := func(p, q, r Point) float64 {
		return (q.X-p.X)*(r.Y-p.Y) - (q.Y-p.Y)*(r.X-p.X)
	}
	onSegment := func(p, q, r Point) bool {
		return p.X <= math.Max(q.X, r.X)+1e-9 && p.X >= math.Min(q.X, r.X)-1e-9 &&
			p.Y <= math.Max(q.Y, r.Y)+1e-9 && p.Y >= math.Min(q.Y, r.Y)-1e-9
	}
	o1 := orient(a1, a2, b1)
	o2 := orient(a1, a2, b2)
	o3 := orient(b1, b2, a1)
	o4 := orient(b1, b2, a2)

	if o1*o2 < 1e-9 && o3*o4 < 1e-9 {
		return true
	}
	if math.Abs(o1) < 1e-9 && onSegment(b1, a1, a2) {
		return true
	}
	if math.Abs(o2) < 1e-9 && onSegment(b2, a1, a2) {
		return true
	}
	if math.Abs(o3) < 1e-9 && onSegment(a1, b1, b2) {
		return true
	}
	if math.Abs(o4) < 1e-9 && onSegment(a2, b1, b2) {
		return true
	}
	return false
}

func centroid(r Ring) Point {
	n := len(r)
	if n == 0 {
		return Point{}
	}
	var x, y float64
	for _, p := range r {
		x += p.X
		y += p.Y
	}
	return Point{x / float64(n), y / float64(n)}
}
