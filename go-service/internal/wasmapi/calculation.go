package wasmapi

import (
	"math"
)

const (
	milPerOz    = 1.378
	mmToMil     = 1.0 / 0.0254
	ozToMm      = milPerOz * 0.0254
	layerKOuter = 0.048
	layerKInner = 0.024
	bExp        = 0.44
	cExp        = 0.725
)

func widthToCurrent(widthMil, copperOz, tempRise float64, layer string) float64 {
	k := layerKOuter
	if layer == "inner" {
		k = layerKInner
	}
	thicknessMil := copperOz * milPerOz
	area := widthMil * thicknessMil
	return k * math.Pow(tempRise, bExp) * math.Pow(area, cExp)
}
