//go:build js && wasm

package geometry

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
)

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
