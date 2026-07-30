package geometry

// Layer holds polygons parsed from one fabrication layer of an ODB++ archive.
//
// Polygons carry an authoritative per-feature net label via NetLabels (one
// entry per polygon in the same order as Polygons). Reflected indicates a
// bottom-side copper layer exported as a mirrored Gerber/ODB image — pipeline
// callers may need to un-mirror it before FEM assembly.
type Layer struct {
	Name      string
	Polygons  MultiPolygon
	NetLabels []string
	Reflected bool
}

// DrillPoint is a single drilled hole centre together with its tool diameter.
//
// These drive the viewer's via overlay. Connection points cannot be used for
// that: they only exist for vias on analysed power/ground nets, so vias on
// signal nets (which are still physically present on the board) would be
// invisible. The ODB++ drill layer lists every hole regardless of net.
type DrillPoint struct {
	X, Y     float64
	Diameter float64
	// Via is true when the tool that produced the hole is dedicated to vias
	// (ODB++ tool TYPE field == "VIA").
	Via bool
}