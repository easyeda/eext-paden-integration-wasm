package geometry

import (
	"strings"
)

// FuzzyMatchLayer reports whether the given Gerber filename corresponds to the
// user-configured layer name. It is robust to:
//
//   - EasyEDA exports ("Gerber_TopLayer.GTL" vs config "Top Layer")
//   - KiCad exports ("F.Cu.gbr" vs "Top Layer", "In1.Cu.gbr" vs "Inner Layer 1")
//   - Altium/other tools ("GTL", "GBL", "G1", "G2" suffixes)
//   - Naming variants (case, separators, accents)
//
// The match is intentionally liberal: when in doubt the caller still verifies
// the candidate via geometry, so a false positive only costs an extra check.
func FuzzyMatchLayer(configName, filename string) bool {
	if configName == "" || filename == "" {
		return false
	}

	base := stripGerberPath(filename)
	ext := stripGerberExt(base)

	cn := layerKey(configName)
	bn := layerKey(base)

	if cn == "" || bn == "" {
		return false
	}
	if cn == bn {
		return true
	}
	basic := false
	if strings.Contains(bn, cn) || strings.Contains(cn, bn) {
		basic = true
	} else if tokenOverlap(cn, bn) {
		basic = true
	}
	if basic && !silkCompatible(configName, base) {
		return false
	}
	if basic {
		return true
	}

	switch strings.ToLower(ext) {
	case ".gtl", ".gto", ".gts", ".gtp":
		return looksLikeTop(configName) && !isNonCopper(configName) && !isNonCopper(base)
	case ".gbl", ".gbo", ".gbs", ".gbp":
		return looksLikeBottom(configName) && !isNonCopper(configName) && !isNonCopper(base)
	case ".g1", ".gm1":
		return looksLikeInner(configName, 1)
	case ".g2", ".gm2":
		return looksLikeInner(configName, 2)
	case ".g3", ".gm3":
		return looksLikeInner(configName, 3)
	case ".g4", ".gm4":
		return looksLikeInner(configName, 4)
	case ".g5", ".gm5":
		return looksLikeInner(configName, 5)
	}

	// KiCad-style "F.Cu" / "B.Cu" / "In*.Cu" mid-name hints.
	lowerBase := strings.ToLower(base)
	switch {
	case strings.Contains(lowerBase, "f.cu") || strings.Contains(lowerBase, "f_cu"):
		return looksLikeTop(configName) && !isNonCopper(configName) && !isNonCopper(base)
	case strings.Contains(lowerBase, "b.cu") || strings.Contains(lowerBase, "b_cu"):
		return looksLikeBottom(configName) && !isNonCopper(configName) && !isNonCopper(base)
	case hasKiCadInnerMatch(lowerBase):
		n := kiCadInnerIndex(lowerBase)
		return looksLikeInner(configName, n)
	}
	return false
}

// isNonCopper reports whether the layer name clearly belongs to a non-copper
// layer (silk, paste, mask, legend, profile). Such names must not match a
// copper config keyword ("Top", "Bottom", "Inner N") and vice versa.
func isNonCopper(s string) bool {
	k := strings.ToLower(s)
	return strings.Contains(k, "silk") || strings.Contains(k, "silkscreen") ||
		strings.Contains(k, "legend") || strings.Contains(k, "soldermask") ||
		strings.Contains(k, "solderpaste") || strings.Contains(k, "paste") ||
		strings.Contains(k, "mask") || strings.Contains(k, "profile") ||
		strings.Contains(k, "outline") || strings.Contains(k, "edge")
}

// silkCompatible returns true when the filament/keywords carry the same kind
// of side-channel markers (both copper, both silk, both mask, ...). Used after a
// basic match succeeds to avoid "Top Layer" matching a GTO silkscreen file.
func silkCompatible(configName, filename string) bool {
	cfgNonCopper := isNonCopper(configName)
	fileNonCopper := isNonCopper(filename)
	if cfgNonCopper != fileNonCopper {
		return false
	}
	cfgPaste := strings.Contains(strings.ToLower(configName), "paste")
	filePaste := strings.Contains(strings.ToLower(filename), "paste")
	if cfgPaste != filePaste {
		return false
	}
	cfgMask := strings.Contains(strings.ToLower(configName), "mask")
	fileMask := strings.Contains(strings.ToLower(filename), "mask")
	if cfgMask != fileMask {
		return false
	}
	return true
}

// hasKiCadInnerMatch reports whether the filename embeds a KiCad "In*.Cu"
// segment (e.g. "Gerber_In1.Cu.gbr"). Caller must pass a lowercased basename.
func hasKiCadInnerMatch(lowerBase string) bool {
	for _, prefix := range []string{"in1.cu", "in2.cu", "in3.cu", "in4.cu", "in5.cu", "in6.cu"} {
		if strings.Contains(lowerBase, prefix) {
			return true
		}
	}
	for i := 1; i <= 9; i++ {
		if strings.Contains(lowerBase, "in"+itoa(i)+"_cu") {
			return true
		}
	}
	return false
}

// kiCadInnerIndex extracts the N from an "InN.Cu" segment in the basename.
func kiCadInnerIndex(lowerBase string) int {
	for i := 1; i <= 9; i++ {
		needle := "in" + itoa(i) + ".cu"
		if strings.Contains(lowerBase, needle) {
			return i
		}
	}
	for i := 1; i <= 9; i++ {
		if strings.Contains(lowerBase, "in"+itoa(i)+"_cu") {
			return i
		}
	}
	return 0
}

// stripGerberPath returns the basename with any directory prefix and well-known
// export prefixes removed. The "Gerber_" prefix is added by EasyEDA's batch
// export; we want it gone before fuzzy matching.
func stripGerberPath(name string) string {
	base := name
	if i := strings.LastIndexAny(base, "/\\"); i >= 0 {
		base = base[i+1:]
	}
	for _, prefix := range []string{"Gerber_", "gerber_", "GERBER_"} {
		if strings.HasPrefix(base, prefix) {
			base = base[len(prefix):]
			break
		}
	}
	return base
}

func stripGerberExt(base string) string {
	if i := strings.LastIndex(base, "."); i >= 0 {
		return base[i:]
	}
	return ""
}

// layerKey normalizes a name for substring/token comparison. All non-alphanumeric
// runes collapse, leaving a single lowercase word like "toplayer" / "fcu" /
// "innerlayer1".
func layerKey(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// tokenOverlap checks for matching tokens when both names contain meaningful
// separators ("Top Layer" -> ["top", "layer"]; "F_Cu" -> ["f", "cu"]; etc.).
// Returns true when at least one significant config-side token appears in the
// filename, or vice versa.
func tokenOverlap(a, b string) bool {
	at := splitAlphanum(a)
	bt := splitAlphanum(b)
	if len(at) == 0 || len(bt) == 0 {
		return false
	}
	hits := 0
	for _, t := range at {
		if len(t) < 2 {
			continue
		}
		for _, u := range bt {
			if u == t || strings.Contains(u, t) || strings.Contains(t, u) {
				hits++
				break
			}
		}
	}
	if hits > 0 {
		return true
	}
	for _, u := range bt {
		if len(u) < 2 {
			continue
		}
		for _, t := range at {
			if u == t || strings.Contains(u, t) || strings.Contains(t, u) {
				return true
			}
		}
	}
	return false
}

func splitAlphanum(s string) []string {
	var out []string
	cur := strings.Builder{}
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			cur.WriteRune(r)
		} else if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func looksLikeTop(s string) bool {
	k := layerKey(s)
	if strings.Contains(k, "top") || strings.Contains(k, "front") {
		return true
	}
	// F.Cu / F_Cu keyword
	ln := strings.ToLower(s)
	return strings.Contains(ln, "f.cu") || strings.Contains(ln, "f_cu")
}

func looksLikeBottom(s string) bool {
	k := layerKey(s)
	if strings.Contains(k, "bottom") || strings.Contains(k, "back") {
		return true
	}
	ln := strings.ToLower(s)
	return strings.Contains(ln, "b.cu") || strings.Contains(ln, "b_cu")
}

// looksLikeInner matches "Inner Layer N", "InN.Cu", or generic inner-layer
// keywords ("L3", "Signal3", etc.).
func looksLikeInner(s string, n int) bool {
	k := layerKey(s)
	if strings.Contains(k, "inner") {
		return true
	}
	digits := []byte{}
	for _, r := range s {
		if r >= '0' && r <= '9' {
			digits = append(digits, byte(r))
		}
	}
	if len(digits) > 0 && int(digits[0]-'0') == n {
		return true
	}
	ln := strings.ToLower(s)
	return strings.Contains(ln, "in"+itoa(n)+".cu") || strings.Contains(ln, "in"+itoa(n)+"_cu")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := []byte{}
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	return string(buf)
}
