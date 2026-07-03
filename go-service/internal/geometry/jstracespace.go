package geometry

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
)

// GerberLayer holds polygons parsed from a single Gerber file.
type GerberLayer struct {
	Name     string
	Filename string
	Polygons MultiPolygon
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

		layerName := matchLayerName(f.Name, layerNames)
		layers[layerName] = GerberLayer{
			Name:     layerName,
			Filename: f.Name,
			Polygons: polygons,
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

func isGerberFile(name string) bool {
	return hasSuffix(name, ".gbr") || hasSuffix(name, ".ger") || hasSuffix(name, ".gtl") ||
		hasSuffix(name, ".gbl") || hasSuffix(name, ".g1") || hasSuffix(name, ".g2") ||
		hasSuffix(name, ".g3") || hasSuffix(name, ".g4") || hasSuffix(name, ".g5") ||
		hasSuffix(name, ".g6") || hasSuffix(name, ".g7") || hasSuffix(name, ".g8") ||
		hasSuffix(name, ".gko") || hasSuffix(name, ".gbo") || hasSuffix(name, ".gto") ||
		hasSuffix(name, ".gm1") || hasSuffix(name, ".gm2") || hasSuffix(name, ".gm3") ||
		hasSuffix(name, ".gbp") || hasSuffix(name, ".gtp")
}

func matchLayerName(filename string, layerNames []string) string {
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
