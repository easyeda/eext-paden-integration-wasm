//go:build !(js && wasm)

package geometry

import "fmt"

func Union(a, b MultiPolygon) (MultiPolygon, error) {
	return nil, fmt.Errorf("geometry.Union requires a js/wasm build")
}

func Difference(subject, clip MultiPolygon) (MultiPolygon, error) {
	return nil, fmt.Errorf("geometry.Difference requires a js/wasm build")
}

func Intersect(a, b MultiPolygon) (MultiPolygon, error) {
	return nil, fmt.Errorf("geometry.Intersect requires a js/wasm build")
}

func Offset(mp MultiPolygon, delta float64) (MultiPolygon, error) {
	return nil, fmt.Errorf("geometry.Offset requires a js/wasm build")
}

func Close(mp MultiPolygon, delta float64) (MultiPolygon, error) {
	return nil, fmt.Errorf("geometry.Close requires a js/wasm build")
}

func ParseGerberZip(zipBytes []byte, layerNames []string) (map[string]GerberLayer, error) {
	return nil, fmt.Errorf("geometry.ParseGerberZip requires a js/wasm build")
}

func GerberToPolygons(gerberText string) (MultiPolygon, error) {
	return nil, fmt.Errorf("geometry.GerberToPolygons requires a js/wasm build")
}

func DrillToPolygons(drillText string) (MultiPolygon, error) {
	return nil, fmt.Errorf("geometry.DrillToPolygons requires a js/wasm build")
}

func ParseDrillHoles(zipBytes []byte) (MultiPolygon, error) {
	return nil, fmt.Errorf("geometry.ParseDrillHoles requires a js/wasm build")
}
