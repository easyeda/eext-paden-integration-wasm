package geometry

// GerberLayer holds polygons parsed from a single Gerber file.
type GerberLayer struct {
	Name      string
	Filename  string
	Polygons  MultiPolygon
	Reflected bool
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
	for _, name := range layerNames {
		if name == baseNoExt || name == base {
			return name
		}
	}
	for _, name := range layerNames {
		if FuzzyMatchLayer(name, filename) {
			return name
		}
	}
	return baseNoExt
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
		return contains(ln, "drill") || contains(ln, "hole") || contains(ln, "npth") || contains(ln, "plated")
	}
	return false
}

func isGerberReflected(text string) bool {
	ln := stringsToLower(text)
	return contains(ln, "reflected: yes") || contains(ln, "reflected:yes")
}

func isOutlineFile(name string) bool {
	for _, check := range []string{"outline", "edge", "board", "profile", "gko", "gml", "gm1", "gm2", "gm3", "gm4", "gm5"} {
		if contains(name, check) {
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
