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
	DrillHoles  MultiPolygon
	DrillPoints []DrillPoint
}

type odbSymbol struct {
	kind          string
	width, height float64
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

	edaPath := findODBFile(files, "/steps/", "/eda/data")
	if edaPath == "" {
		return nil, fmt.Errorf("ODB++ eda/data not found")
	}
	layerOrder, refs := parseODBNetReferences(string(files[edaPath]), targetNets)
	wanted := make(map[string]string, len(layerNames))
	for _, configured := range layerNames {
		wanted[normalizeODBName(configured)] = configured
	}

	result := &ODBData{Layers: make(map[string]Layer)}
	for layerIndex, odbName := range layerOrder {
		configured, ok := wanted[normalizeODBName(odbName)]
		if !ok {
			continue
		}
		featurePath := findODBLayerFile(files, odbName, "features")
		if featurePath == "" {
			continue
		}
		features, err := parseODBFeatures(string(files[featurePath]), refs[layerIndex])
		if err != nil {
			return nil, fmt.Errorf("parse ODB++ layer %s: %w", odbName, err)
		}
		var polygons MultiPolygon
		var labels []string
		for _, feature := range features {
			net, referenced := refs[layerIndex][feature.index]
			if !referenced || net == "" {
				continue
			}
			for _, polygon := range feature.polygons {
				polygon.EnsureOrientation()
				polygons = append(polygons, polygon)
				labels = append(labels, net)
			}
		}
		result.Layers[configured] = Layer{
			Name:      configured,
			Polygons:  polygons,
			NetLabels: labels,
		}
	}

	profilePath := findODBFile(files, "/steps/", "/profile")
	if profilePath != "" {
		features, err := parseODBFeatures(string(files[profilePath]), nil)
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

func parseODBFeatures(data string, selected map[int]string) ([]odbFeature, error) {
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
				polygons = parseODBPad(line, symbols)
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
		parts := strings.Split(strings.TrimPrefix(lower, "rect"), "x")
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

func parseODBPad(line string, symbols map[int]odbSymbol) MultiPolygon {
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
	rotation, _ := strconv.ParseFloat(fields[5], 64)
	symbol := symbols[symbolIndex]
	ring := odbSymbolRing(symbol, Point{x, y})
	if len(ring) < 3 {
		return nil
	}
	rotateRing(ring, Point{x, y}, rotation/10*math.Pi/180)
	return MultiPolygon{{ring}}
}

func odbSymbolRing(symbol odbSymbol, center Point) Ring {
	switch symbol.kind {
	case "round":
		return circleRing(center, symbol.width/2, 24)
	case "rect":
		hw, hh := symbol.width/2, symbol.height/2
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
	segments := int(math.Ceil(math.Abs(sweep) * radius / 0.05))
	if segments < 4 {
		segments = 4
	}
	if segments > 128 {
		segments = 128
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
