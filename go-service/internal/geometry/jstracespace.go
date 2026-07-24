//go:build js && wasm

package geometry

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
)

// GerberLayer holds polygons parsed from a single Gerber file.
type GerberLayer struct {
	Name      string
	Filename  string
	Polygons  MultiPolygon
	Reflected bool // true if Gerber header says the layer is mirrored
}

// ParseGerberZip extracts each Gerber file from the ZIP and converts it to
// polygons using the tracespace parser/plotter bridge running in JS.
func ParseGerberZip(zipBytes []byte, layerNames []string) (map[string]GerberLayer, error) {
	r := bytes.NewReader(zipBytes)
	zr, err := zip.NewReader(r, int64(len(zipBytes)))
	if err != nil {
		return nil, fmt.Errorf("failed to open Gerber ZIP: %w", err)
	}

	layers := make(map[string]GerberLayer)
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		nameLower := stringsToLower(f.Name)
		if !isGerberFile(nameLower) {
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

		polygons, err := GerberToPolygons(string(data))
		if err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", f.Name, err)
		}

		layerName := MatchLayerName(f.Name, layerNames)
		// Outline / mechanical layers should never masquerade as copper layers.
		// Keep them under their filename so extractBoardOutline can find them.
		if isOutlineFile(nameLower) && layerName != baseNameNoExt(f.Name) {
			fmt.Printf("[GerberZip] %s looks like an outline/mechanical file; storing as %s instead of %s\n", f.Name, baseNameNoExt(f.Name), layerName)
			layerName = baseNameNoExt(f.Name)
		}
		fmt.Printf("[GerberZip] %s -> layer '%s' (%d polygons)\n", f.Name, layerName, len(polygons))
		layers[layerName] = GerberLayer{
			Name:      layerName,
			Filename:  f.Name,
			Polygons:  polygons,
			Reflected: isGerberReflected(string(data)),
		}
	}

	return layers, nil
}

// GerberToPolygons converts a single Gerber file contents to polygons via JS.
func GerberToPolygons(gerberText string) (MultiPolygon, error) {
	result, err := Call("gerberToPolygons", gerberText)
	if err != nil {
		return nil, err
	}
	return polygonsFromJS(result)
}

// DrillToPolygons converts a single Excellon drill file to hole polygons via JS.
func DrillToPolygons(drillText string) (MultiPolygon, error) {
	result, err := Call("drillToPolygons", drillText)
	if err != nil {
		return nil, err
	}
	return polygonsFromJS(result)
}

// ParseDrillHoles extracts plated/non-plated drill holes from all Excellon
// drill files in the Gerber ZIP and returns them as a single MultiPolygon.
func ParseDrillHoles(zipBytes []byte) (MultiPolygon, error) {
	r := bytes.NewReader(zipBytes)
	zr, err := zip.NewReader(r, int64(len(zipBytes)))
	if err != nil {
		return nil, fmt.Errorf("failed to open Gerber ZIP: %w", err)
	}

	var allHoles MultiPolygon
	var checked int
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if !isDrillFile(f.Name) {
			continue
		}
		checked++

		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("failed to open %s: %w", f.Name, err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", f.Name, err)
		}

		holes, err := DrillToPolygons(string(data))
		if err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", f.Name, err)
		}
		if len(holes) == 0 {
			fmt.Printf("[GerberZip] drill %s parsed but produced 0 holes\n", f.Name)
			continue
		}
		fmt.Printf("[GerberZip] drill %s -> %d holes\n", f.Name, len(holes))
		allHoles, err = Union(allHoles, holes)
		if err != nil {
			return nil, fmt.Errorf("failed to union drill holes from %s: %w", f.Name, err)
		}
	}
	if checked == 0 {
		fmt.Printf("[GerberZip] no drill files found in ZIP\n")
	}
	if len(allHoles) > 0 {
		fmt.Printf("[GerberZip] total drill holes: %d polygon(s)\n", len(allHoles))
	}
	return allHoles, nil
}

func isGerberFile(name string) bool {
	return hasSuffix(name, ".gbr") || hasSuffix(name, ".ger") || hasSuffix(name, ".gtl") ||
		hasSuffix(name, ".gbl") || hasSuffix(name, ".g1") || hasSuffix(name, ".g2") ||
		hasSuffix(name, ".g3") || hasSuffix(name, ".g4") || hasSuffix(name, ".g5") ||
		hasSuffix(name, ".g6") || hasSuffix(name, ".g7") || hasSuffix(name, ".g8") ||
		hasSuffix(name, ".gko") || hasSuffix(name, ".gbo") || hasSuffix(name, ".gto") ||
		hasSuffix(name, ".gm1") || hasSuffix(name, ".gm2") || hasSuffix(name, ".gm3") ||
		hasSuffix(name, ".gbp") || hasSuffix(name, ".gtp")
}

func isDrillFile(name string) bool {
	ln := stringsToLower(name)
	if hasSuffix(ln, ".drl") || hasSuffix(ln, ".drd") || hasSuffix(ln, ".tap") {
		return true
	}
	if hasSuffix(ln, ".txt") || hasSuffix(ln, ".xln") {
		if contains(ln, "drill") || contains(ln, "hole") || contains(ln, "npth") || contains(ln, "plated") {
			return true
		}
	}
	return false
}

// isOutlineFile returns true for mechanical/outline layers that should not be
// treated as signal/plane copper layers.
func isGerberReflected(text string) bool {
	ln := stringsToLower(text)
	// EasyEDA Pro gerber comments look like:
	//   G04 Scale: 100 percent, Rotated: No, Reflected: No*
	return contains(ln, "reflected: yes") || contains(ln, "reflected:yes")
}

func isOutlineFile(name string) bool {
	checks := []string{"outline", "edge", "board", "profile", "gko", "gml", "gm1", "gm2", "gm3", "gm4", "gm5"}
	for _, c := range checks {
		if contains(name, c) {
			return true
		}
	}
	return false
}

func baseNameNoExt(filename string) string {
	base := filename
	if idx := lastIndex(base, "/"); idx >= 0 {
		base = base[idx+1:]
	}
	if idx := lastIndex(base, "."); idx >= 0 {
		base = base[:idx]
	}
	return base
}

func MatchLayerName(filename string, layerNames []string) string {
	base := filename
	if idx := lastIndex(base, "/"); idx >= 0 {
		base = base[idx+1:]
	}
	baseNoExt := base
	if idx := lastIndex(baseNoExt, "."); idx >= 0 {
		baseNoExt = baseNoExt[:idx]
	}

	// Prefer an exact match with configured layer names.
	for _, name := range layerNames {
		if name == baseNoExt || name == base {
			return name
		}
	}

	// Fuzzy match: handles EasyEDA "Gerber_TopLayer.GTL" vs "Top Layer",
	// KiCad "F.Cu.gbr" vs "Top Layer", and similar variants across tools.
	for _, name := range layerNames {
		if FuzzyMatchLayer(name, filename) {
			return name
		}
	}

	// Fallback: use the filename without extension as the layer name.
	return baseNoExt
}

// Small string helpers to avoid importing strings (keeps WASM small).
func stringsToLower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

func lastIndex(s, substr string) int {
	for i := len(s) - len(substr); i >= 0; i-- {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
