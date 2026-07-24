package geometry

import "testing"

func TestFuzzyMatchLayer(t *testing.T) {
	cases := []struct {
		name     string
		config   string
		filename string
		want     bool
	}{
		// EasyEDA canonical exports
		{"EasyEDA top", "Top Layer", "Gerber_TopLayer.GTL", true},
		{"EasyEDA bottom", "Bottom Layer", "Gerber_BottomLayer.GBL", true},
		{"EasyEDA inner 1", "Inner Layer 1", "Gerber_InnerLayer1.G1", true},
		{"EasyEDA inner 2", "Inner Layer 2", "Gerber_InnerLayer2.G2", true},
		{"Mixed case", "top layer", "GERBER_TOPLAYER.GTL", true},
		{"Separator differ", "Top_Layer", "Top Layer.GTL", true},
		{"No prefix folder", "Top Layer", "TopLayer.GTL", true},

		// KiCad naming
		{"KiCad top", "Top Layer", "F.Cu.gbr", true},
		{"KiCad bottom", "Bottom Layer", "B.Cu.gbr", true},
		{"KiCad inner 1", "Inner Layer 1", "In1.Cu.gbr", true},
		{"KiCad inner 1 alt", "Inner Layer 1", "In1_Cu.gbr", true},
		{"KiCad inner 2", "Inner Layer 2", "In2.Cu.gbr", true},

		// File-extension fallback for unnamed configs
		{"Ext top -> F.Cu config", "F.Cu", "Design.GTL", true},
		{"Ext top -> top config", "TopLayer", "X.GTL", true},
		{"Ext bottom -> bottom config", "BottomLayer", "X.GBL", true},

		// Negative cases
		{"Mismatch top vs bottom", "Top Layer", "Gerber_BottomLayer.GBL", false},
		{"Empty config", "", "Gerber_TopLayer.GTL", false},
		{"Empty filename", "Top Layer", "", false},
		{"Silk vs copper", "Top Layer", "Gerber_TopSilkscreenLayer.GTO", false},
		{"Mask vs copper", "Bottom Layer", "Gerber_BottomSolderMask.GBS", false},
		{"Paste vs copper", "Top Layer", "Gerber_TopPaste.GTP", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := FuzzyMatchLayer(c.config, c.filename)
			if got != c.want {
				t.Errorf("FuzzyMatchLayer(%q, %q) = %v, want %v", c.config, c.filename, got, c.want)
			}
		})
	}
}
