//go:build js && wasm

package geometry

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"strconv"
)

// DrillPoint is a single drilled hole centre together with its tool diameter.
//
// These drive the viewer's via overlay. Connection points cannot be used for
// that: they only exist for vias on analysed power/ground nets, so vias on
// signal nets (which are still physically present on the board) would be
// invisible. The Excellon drill files list every hole regardless of net.
type DrillPoint struct {
	X, Y     float64
	Diameter float64
	// Via is true when the hole came from a drill file dedicated to vias,
	// e.g. EasyEDA's "Drill_PTH_Through_Via.DRL".
	Via bool
}

// ParseDrillPoints extracts hole centres from every Excellon drill file in the
// Gerber ZIP. Coordinates are returned in Gerber space (mm).
func ParseDrillPoints(zipBytes []byte) ([]DrillPoint, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, fmt.Errorf("failed to open Gerber ZIP: %w", err)
	}

	var all []DrillPoint
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !isDrillFile(f.Name) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("failed to open %s: %w", f.Name, err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", f.Name, err)
		}

		isVia := contains(stringsToLower(f.Name), "via")
		pts := parseExcellonPoints(string(data), isVia)
		fmt.Printf("[DrillPoints] %s -> %d hole(s) (via=%v)\n", f.Name, len(pts), isVia)
		all = append(all, pts...)
	}
	return all, nil
}

// parseExcellonPoints extracts X/Y coordinates from an Excellon drill file.
// Supports both trailing-zero and leading-zero suppression, METRIC/INCH units,
// and G85 slot commands (linearMove).
func parseExcellonPoints(text string, via bool) []DrillPoint {
	var pts []DrillPoint
	tools := make(map[string]float64) // T01 -> diameter (mm)
	var activeTool string
	metric := true              // M48/METRIC default
	format := [2]int{3, 3}      // digits before/after decimal
	zeroMode := ""              // LZ=leading, TZ=trailing
	lastX, lastY := 0.0, 0.0    // modal coordinates
	lastXset, lastYset := false, false

	lines := splitLines(text)
	for _, line := range lines {
		line = trimSpace(line)
		if line == "" || line[0] == ';' {
			continue
		}
		upper := stringsToUpper(line)

		// M48 header
		if upper == "M48" {
			continue
		}
		// Units
		if contains(upper, "METRIC") {
			metric = true
		}
		if contains(upper, "INCH") {
			metric = false
		}
		// Zero suppression
		if contains(upper, "LZ") {
			zeroMode = "LZ"
		}
		if contains(upper, "TZ") {
			zeroMode = "TZ"
		}
		// Format: typically "METRIC,LZ,0000.00000" -> 4 integral, 5 fractional
		// We parse the digit count from the pattern if present.
		if idx := indexByte(upper, ','); idx >= 0 {
			rest := upper[idx+1:]
			if idx2 := indexByte(rest, '.'); idx2 >= 0 {
				beforeDot := rest[:idx2]
				afterDot := rest[idx2+1:]
				format[0] = countDigits(beforeDot)
				format[1] = countDigits(afterDot)
			}
		}

		// Tool definition: T01C0.25400
		if len(upper) > 1 && upper[0] == 'T' && contains(upper, "C") {
			parts := splitOn(upper, 'C')
			if len(parts) == 2 {
				toolName := parts[0]
				dia := parseFloat(parts[1])
				if !metric {
					dia *= 25.4
				}
				tools[toolName] = dia
			}
			continue
		}

		// Tool select: T01
		if len(upper) > 1 && upper[0] == 'T' && !contains(upper, "C") {
			activeTool = upper
			continue
		}

		// Hole command: X...Y...
		if upper[0] == 'X' || upper[0] == 'Y' {
			x, y, xok, yok := parseCoord(upper, format, zeroMode, metric)
			if xok {
				lastX = x
				lastXset = true
			}
			if yok {
				lastY = y
				lastYset = true
			}
			if lastXset && lastYset {
				dia := 0.3 // default
				if activeTool != "" && tools[activeTool] > 0 {
					dia = tools[activeTool]
				}
				pts = append(pts, DrillPoint{X: lastX, Y: lastY, Diameter: dia, Via: via})
			}
		}

		// G85 slot: X...Y... with linearMove
		if contains(upper, "G85") {
			// Slot endpoints are modal-continued, same as normal X/Y
			continue
		}
	}
	return pts
}

func parseCoord(line string, format [2]int, zeroMode string, metric bool) (x, y float64, xok, yok bool) {
	// Extract X and Y substrings
	xi := indexByte(line, 'X')
	yi := indexByte(line, 'Y')
	if xi < 0 && yi < 0 {
		return
	}

	if xi >= 0 {
		end := len(line)
		if yi > xi {
			end = yi
		}
		raw := line[xi+1 : end]
		x = decodeExcellonNumber(raw, format, zeroMode)
		if !metric {
			x *= 25.4
		}
		xok = true
	}
	if yi >= 0 {
		end := len(line)
		for i := yi + 1; i < len(line); i++ {
			if line[i] < '0' || line[i] > '9' {
				if line[i] != '.' && line[i] != '+' && line[i] != '-' {
					end = i
					break
				}
			}
		}
		raw := line[yi+1 : end]
		y = decodeExcellonNumber(raw, format, zeroMode)
		if !metric {
			y *= 25.4
		}
		yok = true
	}
	return
}

func decodeExcellonNumber(raw string, format [2]int, zeroMode string) float64 {
	// If there's already a decimal point, parse directly
	if indexByte(raw, '.') >= 0 {
		v, _ := strconv.ParseFloat(raw, 64)
		return v
	}
	// Pad to expected total width
	totalDigits := format[0] + format[1]
	if len(raw) < totalDigits {
		if zeroMode == "LZ" {
			// Leading zero suppression: pad left
			for len(raw) < totalDigits {
				raw = "0" + raw
			}
		} else {
			// Trailing zero suppression (TZ) or unknown: pad right
			for len(raw) < totalDigits {
				raw = raw + "0"
			}
		}
	}
	// Insert decimal point
	if len(raw) >= format[0] {
		intPart := raw[:format[0]]
		fracPart := ""
		if len(raw) > format[0] {
			fracPart = raw[format[0]:]
		}
		raw = intPart + "." + fracPart
	}
	v, _ := strconv.ParseFloat(raw, 64)
	return v
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' || s[i] == '\r' {
			if i > start {
				lines = append(lines, s[start:i])
			}
			start = i + 1
			if i+1 < len(s) && s[i] == '\r' && s[i+1] == '\n' {
				i++
				start++
			}
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func splitOn(s string, sep byte) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\r' || s[start] == '\n') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\r' || s[end-1] == '\n') {
		end--
	}
	return s[start:end]
}

func stringsToUpper(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'a' && b[i] <= 'z' {
			b[i] -= 'a' - 'A'
		}
	}
	return string(b)
}

func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

func countDigits(s string) int {
	c := 0
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			c++
		}
	}
	return c
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

