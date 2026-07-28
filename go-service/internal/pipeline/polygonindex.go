package pipeline

import (
	"math"
	"sort"

	"github.com/easyeda/eext-paden-integration/go-service/internal/geometry"
)

// PolygonIndex is a deterministic, reusable spatial index over a layer's
// polygons. It returns candidate polygon indices for a point query by
// intersecting the point's cell against a uniform grid; callers must still
// rerun exact pointInPolygonMesh on each candidate to preserve semantics.
//
// The index is built once per layer (typically right before net inference) and
// reused for many point queries. It is safe to share across goroutines because
// it is read-only after construction.
//
// The index is intentionally allocation-paranoid: PCB layers commonly include a
// large ground/power pour that touches every grid cell. Without a cap a single
// polygon can bloat the index, so candidates that exceed a per-cell limit are
// moved to a small "global" list that is returned for every query.
type PolygonIndex struct {
	// Areas[i] is the absolute area of polygon i (polygon.Shape[0].Area()).
	// We compute it once so the IPC-D-356A smallest-area priority lookup is
	// O(1) per candidate instead of repeating the shoelace formula.
	Areas []float64
	// CellSize is the uniform grid cell size chosen at build time.
	CellSize float64
	// OrigMinX, OrigMinY are the layer bounds minimums.
	OrigMinX, OrigMinY float64
	// GridX, GridY are the grid dimensions in cells.
	GridX, GridY int
	// Cells[y*GridX+x] holds polygon indices whose bounding box intersects
	// that cell. Polygons inserted into more than cellMaxEntries cells go
	// into Globals instead.
	Cells [][]int
	// Globals is appended to every query's candidate list. Use it for huge
	// polygons that would otherwise dominate the index.
	Globals []int
}

// polygonIndexConfig controls how the grid is built.
type polygonIndexConfig struct {
	// cellMaxEntries is the per-cell candidate limit. Polygons whose bbox
	// overlap more than this many cells are placed in Globals. 64 is a
	// good default for PCB layers: most cells hold a handful of pads or
	// traces, and a single plane pours across hundreds of cells.
	cellMaxEntries int
	// minCellSize prevents the grid from exploding when many polygons
	// share a tiny region. 0 disables the cap.
	minCellSize float64
	// maxGridDim caps the side length of the grid so degenerate bounding
	// boxes (e.g. a single polygon that nearly equals the board) do not
	// allocate huge slices.
	maxGridDim int
}

var defaultPolygonIndexConfig = polygonIndexConfig{
	cellMaxEntries: 64,
	minCellSize:    0.05, // 50 microns
	maxGridDim:     4096,
}

// BuildPolygonIndex constructs a uniform-grid spatial index over the polygon's
// bounding boxes. The shape slice may be empty (returns a nil index, which
// callers handle defensively). Polygons with empty or degenerate rings are
// skipped and absent from the candidate lists.
func BuildPolygonIndex(shapes geometry.MultiPolygon) *PolygonIndex {
	return BuildPolygonIndexWithConfig(shapes, defaultPolygonIndexConfig)
}

// BuildPolygonIndexWithConfig is the config-driven variant of BuildPolygonIndex.
// It exists so tests can shrink the cell cap to force the Globals path.
func BuildPolygonIndexWithConfig(shapes geometry.MultiPolygon, cfg polygonIndexConfig) *PolygonIndex {
	if len(shapes) == 0 {
		return nil
	}

	// Compute bounding boxes and the overall layer bounds. Skip degenerate
	// polygons (empty or fewer than 3 vertices in the exterior ring).
	bboxes := make([]geometry.Box, len(shapes))
	valid := make([]bool, len(shapes))
	var layerBBox geometry.Box
	layerBBox.MinX = math.Inf(1)
	layerBBox.MinY = math.Inf(1)
	layerBBox.MaxX = math.Inf(-1)
	layerBBox.MaxY = math.Inf(-1)
	anyValid := false
	for i, poly := range shapes {
		if len(poly) == 0 || len(poly[0]) < 3 {
			continue
		}
		b := poly.Bounds()
		// Reject pathological bounds (zero-extent or otherwise invalid).
		if !isFiniteBox(b) {
			continue
		}
		bboxes[i] = b
		valid[i] = true
		anyValid = true
		if b.MinX < layerBBox.MinX {
			layerBBox.MinX = b.MinX
		}
		if b.MinY < layerBBox.MinY {
			layerBBox.MinY = b.MinY
		}
		if b.MaxX > layerBBox.MaxX {
			layerBBox.MaxX = b.MaxX
		}
		if b.MaxY > layerBBox.MaxY {
			layerBBox.MaxY = b.MaxY
		}
	}
	if !anyValid {
		return nil
	}

	// Inflate the Layer bounds by a tiny epsilon so that points exactly on
	// MaxX/MaxY still resolve to a valid cell. The polygon mask itself is
	// inclusive on both ends, but a point at MaxX would otherwise map to
	// cell index (GridX) which is out of range.
	const eps = 1e-9
	width := layerBBox.MaxX - layerBBox.MinX
	height := layerBBox.MaxY - layerBBox.MinY
	if width <= 0 {
		width = eps
		layerBBox.MaxX = layerBBox.MinX + width
	}
	if height <= 0 {
		height = eps
		layerBBox.MaxY = layerBBox.MinY + height
	}

	// Choose a cell size that makes the index collide-free for small pad
	// clusters. We aim for ~16 cells across the small axis of the layer,
	// clamped to the configured min/max.
	short := width
	if height < short {
		short = height
	}
	cellSize := short / 16.0
	if cfg.minCellSize > 0 && cellSize < cfg.minCellSize {
		cellSize = cfg.minCellSize
	}
	if cellSize <= 0 {
		cellSize = 1.0
	}

	gridX := int(math.Ceil((width + eps) / cellSize))
	gridY := int(math.Ceil((height + eps) / cellSize))
	if gridX < 1 {
		gridX = 1
	}
	if gridY < 1 {
		gridY = 1
	}
	if cfg.maxGridDim > 0 {
		if gridX > cfg.maxGridDim {
			gridX = cfg.maxGridDim
			cellSize = (width + eps) / float64(gridX)
		}
		if gridY > cfg.maxGridDim {
			gridY = cfg.maxGridDim
			cellSize = math.Max(cellSize, (height+eps)/float64(gridY))
		}
	}

	idx := &PolygonIndex{
		Areas:    make([]float64, len(shapes)),
		CellSize: cellSize,
		OrigMinX: layerBBox.MinX,
		OrigMinY: layerBBox.MinY,
		GridX:    gridX,
		GridY:    gridY,
		Cells:    make([][]int, gridX*gridY),
	}
	for i := range shapes {
		if !valid[i] {
			continue
		}
		idx.Areas[i] = math.Abs(shapes[i][0].Area())
	}

	// Insert each polygon into the cells its bounding box touches. If a
	// polygon's bbox would push it into more than cellMaxEntries cells, it
	// is treated as "global" and inserted into Globals instead. This keeps
	// memory bounded when a giant pour covers the whole board.
	cap := cfg.cellMaxEntries
	for i := range bboxes {
		if !valid[i] {
			continue
		}
		b := bboxes[i]
		c0, c1 := idx.cellRange(b)
		cellsHit := (c1[0] - c0[0] + 1) * (c1[1] - c0[1] + 1)
		if cap > 0 && cellsHit > cap {
			idx.Globals = append(idx.Globals, i)
			continue
		}
		for cy := c0[1]; cy <= c1[1]; cy++ {
			base := cy * idx.GridX
			for cx := c0[0]; cx <= c1[0]; cx++ {
				idx.Cells[base+cx] = append(idx.Cells[base+cx], i)
			}
		}
	}

	// Sort each cell's candidate list by ascending area so the
	// small-area-first IPC priority is preserved with no extra work at
	// query time. This is the same ordering the brute-force code used
	// (sort.Slice over polyOrders).
	for cell := range idx.Cells {
		if len(idx.Cells[cell]) <= 1 {
			continue
		}
		arr := idx.Cells[cell]
		sort.SliceStable(arr, func(a, b int) bool {
			return idx.Areas[arr[a]] < idx.Areas[arr[b]]
		})
	}
	return idx
}

// isFiniteBox reports whether the box coordinates are finite (no NaN/Inf).
// Degenerate polygons produce a zero-value Box{} which is technically finite
// but has no extent; we still skip those via the explicit length check.
func isFiniteBox(b geometry.Box) bool {
	return !math.IsNaN(b.MinX) && !math.IsNaN(b.MinY) &&
		!math.IsNaN(b.MaxX) && !math.IsNaN(b.MaxY) &&
		!math.IsInf(b.MinX, 0) && !math.IsInf(b.MinY, 0) &&
		!math.IsInf(b.MaxX, 0) && !math.IsInf(b.MaxY, 0)
}

// cellRange returns the inclusive cell coordinate range that the box touches.
// The returned indices are clamped to the valid grid range so that points
// floating-point above the layer bounds (or exactly on MaxX/MaxY) still map
// to a valid cell.
func (idx *PolygonIndex) cellRange(b geometry.Box) ([2]int, [2]int) {
	cx0 := int(math.Floor((b.MinX - idx.OrigMinX) / idx.CellSize))
	cy0 := int(math.Floor((b.MinY - idx.OrigMinY) / idx.CellSize))
	cx1 := int(math.Floor((b.MaxX - idx.OrigMinX) / idx.CellSize))
	cy1 := int(math.Floor((b.MaxY - idx.OrigMinY) / idx.CellSize))
	if cx0 < 0 {
		cx0 = 0
	}
	if cy0 < 0 {
		cy0 = 0
	}
	if cx1 >= idx.GridX {
		cx1 = idx.GridX - 1
	}
	if cy1 >= idx.GridY {
		cy1 = idx.GridY - 1
	}
	return [2]int{cx0, cy0}, [2]int{cx1, cy1}
}

// Candidates returns polygon indices whose bounding boxes contain the point.
// The result is deduplicated (a polygon can appear in both the cell and
// Globals) and not sorted. The IPC-D-356A "smallest-area-first" search is
// recovered by sorting the result by area before iterating.
func (idx *PolygonIndex) Candidates(pt geometry.Point) []int {
	if idx == nil {
		return nil
	}
	cx := int(math.Floor((pt.X - idx.OrigMinX) / idx.CellSize))
	cy := int(math.Floor((pt.Y - idx.OrigMinY) / idx.CellSize))
	if cx < 0 || cy < 0 || cx >= idx.GridX || cy >= idx.GridY {
		// Outside the layer bounds: still check globals (a polygon that
		// spans most of the board may have been promoted there).
		if len(idx.Globals) == 0 {
			return nil
		}
		out := make([]int, len(idx.Globals))
		copy(out, idx.Globals)
		return out
	}
	cell := idx.Cells[cy*idx.GridX+cx]
	if len(cell) == 0 && len(idx.Globals) == 0 {
		return nil
	}
	// Concatenate cell + globals. cell is already sorted by area; globals
	// may be in any order, so we keep the cell list first and append the
	// globals after. We do not sort the full result here because callers
	// that just want to enumerate all matches (e.g. for net voting) do not
	// care about order; only the IPC smallest-area search does, and it
	// sorts the result by area itself.
	out := make([]int, 0, len(cell)+len(idx.Globals))
	out = append(out, cell...)
	out = append(out, idx.Globals...)
	return out
}

// CandidatesByArea returns polygon candidates sorted by ascending area. This
// is the order that the IPC-D-356A "smallest containing polygon" lookup
// needs, and it preserves the brute-force code's exact behaviour.
func (idx *PolygonIndex) CandidatesByArea(pt geometry.Point) []int {
	cands := idx.Candidates(pt)
	if len(cands) <= 1 {
		return cands
	}
	sort.SliceStable(cands, func(i, j int) bool {
		return idx.Areas[cands[i]] < idx.Areas[cands[j]]
	})
	return cands
}

// Deduplicate returns a copy of indices with duplicates removed, preserving
// order. Polygon indices appear at most once per cell in build but the
// cell+globals union can introduce repeats for globally-promoted polygons.
func DeduplicateIndices(indices []int) []int {
	if len(indices) <= 1 {
		return indices
	}
	seen := make(map[int]struct{}, len(indices))
	out := make([]int, 0, len(indices))
	for _, i := range indices {
		if _, ok := seen[i]; ok {
			continue
		}
		seen[i] = struct{}{}
		out = append(out, i)
	}
	return out
}
