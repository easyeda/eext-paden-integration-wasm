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
//
// Inner copper layers (InnerLayer1..N) are real analysis targets — multi-layer
// boards route power/ground on inner layers and they need to be parsed for
// IR-drop analysis. We rely on the frontend (extract.ts → convert.ts) to put
// every copper layer into cfg.layers, so the only safe filter is:
// (a) keep layers named in layerNames (TopLayer, BottomLayer, InnerLayer1..N, ...), plus
// (b) one outline/mechanical file for board-edge clipping.
// Non-copper Gerbers (silk/solder-mask/paste/assembly/drill-drawing) and
// anything not referenced by the config are dropped unopened.
func ParseGerberZip(zipBytes []byte, layerNames []string) (map[string]GerberLayer, error) {
	r := bytes.NewReader(zipBytes)
	zr, err := zip.NewReader(r, int64(len(zipBytes)))
	if err != nil {
		return nil, fmt.Errorf("failed to open Gerber ZIP: %w", err)
	}

	wanted := wantedLayerFiles(zr, layerNames)
	layers := make(map[string]GerberLayer)
	for _, entry := range zr.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		nameLower := stringsToLower(entry.Name)
		if !isGerberFile(nameLower) {
			continue
		}
		if !wanted[entry.Name] {
			continue
		}

		rc, err := entry.Open()
		if err != nil {
			return nil, fmt.Errorf("failed to open %s: %w", entry.Name, err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", entry.Name, err)
		}

		polygons, err := GerberToPolygons(string(data))
		if err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", entry.Name, err)
		}

		layerName := MatchLayerName(entry.Name, layerNames)
		if isOutlineFile(nameLower) && layerName != baseNameNoExt(entry.Name) {
			fmt.Printf("[GerberZip] %s looks like an outline/mechanical file; storing as %s instead of %s\n", entry.Name, baseNameNoExt(entry.Name), layerName)
			layerName = baseNameNoExt(entry.Name)
		}
		fmt.Printf("[GerberZip] %s -> layer '%s' (%d polygons)\n", entry.Name, layerName, len(polygons))
		layers[layerName] = GerberLayer{
			Name:      layerName,
			Filename:  entry.Name,
			Polygons:  polygons,
			Reflected: isGerberReflected(string(data)),
		}
	}

	return layers, nil
}

// wantedLayerFiles returns the set of ZIP entries (by raw filename) that we
// intend to parse. We keep every layer the config explicitly names (this
// includes all copper layers — Top, Bottom, InnerLayer1..N — because they
// all carry power/ground pours that the FEM solver must analyze), plus one
// outline/mechanical file for board-edge clipping. Anything that isn't a
// referenced copper layer or the selected outline is skipped unopened.
func wantedLayerFiles(zr *zip.Reader, layerNames []string) map[string]bool {
	wanted := make(map[string]bool)
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		nameLower := stringsToLower(f.Name)
		if !isGerberFile(nameLower) {
			continue
		}
		layerName := MatchLayerName(f.Name, layerNames)
		// MatchLayerName returns baseNoExt as a fallback when no match is found;
		// treat that as "not wanted" (silk/solder/paste/assembly/etc).
		matched := false
		for _, want := range layerNames {
			if layerName == want {
				matched = true
				break
			}
		}
		if matched {
			wanted[f.Name] = true
			continue
		}
		// Keep one outline/mechanical file for board-edge clipping.
		if isOutlineFile(nameLower) && !isDrillFile(nameLower) {
			wanted[f.Name] = true
		}
	}
	return wanted
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
