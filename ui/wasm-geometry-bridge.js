/**
 * Geometry bridge loaded by ui/wasm-host.html.
 *
 * Exposes window.padenGeometry with:
 *   - clipperUnion(polygons) -> polygons
 *   - clipperDifference(subject, clip) -> polygons
 *   - clipperIntersect(a, b) -> polygons
 *   - clipperOffset(polygons, delta) -> polygons
 *   - clipperMorphologicalClose(polygons, delta) -> polygons
 *   - earcutTriangulate(polygon) -> { vertices: Float64Array, triangles: Uint32Array }
 *   - cdtTriangulate(polygon, seedPoints, options)
 *       -> { vertices: Float64Array, triangles: Uint32Array }
 *
 * All polygons use the format:
 *   [ [ [{x,y}, ...], hole, hole, ... ], ... ] ]
 *
 * ODB++ parsing happens natively in Go; this bridge only handles the
 * polygon operations and the two triangulators.
 */

// Clipper2 factory and the earcut / Triangle triangulators are injected on
// window by ui/wasm-geometry-entry.js before this module loads.
const CLIPPER_PRECISION = 6;
const CLIPPER_ARC_TOLERANCE = 0.005; // mm; smaller = smoother round caps/arcs

let clipperModule = null;

function getEarcut() {
	return window.earcut;
}

async function initClipper() {
	if (clipperModule)
		return clipperModule;
	const factory = window.Clipper2ZFactory;
	if (!factory) {
		throw new Error('Clipper2ZFactory not available on window');
	}
	clipperModule = await factory();
	return clipperModule;
}

function toClipperPaths(polygons) {
	const module = clipperModule;
	const paths = new module.PathsD();
	for (const polygon of polygons) {
		for (const ring of polygon) {
			const path = module.MakePathD(ring.flatMap(p => [p.x, p.y]));
			paths.push_back(path);
		}
	}
	return paths;
}

function fromClipperPaths(paths) {
	const rings = [];
	const n = paths.size();
	for (let i = 0; i < n; i++) {
		const path = paths.get(i);
		const ring = [];
		const m = path.size();
		for (let j = 0; j < m; j++) {
			const pt = path.get(j);
			ring.push({ x: pt.x, y: pt.y });
		}
		rings.push(ring);
	}

	return groupRingsIntoPolygons(rings);
}

function ringSignedArea(ring) {
	let a = 0;
	const n = ring.length;
	for (let i = 0; i < n; i++) {
		const j = (i + 1) % n;
		a += ring[i].x * ring[j].y - ring[j].x * ring[i].y;
	}
	return a / 2;
}

function ringContainsPoint(ring, p) {
	let inside = false;
	const n = ring.length;
	for (let i = 0, j = n - 1; i < n; j = i, i++) {
		const xi = ring[i].x;
		const yi = ring[i].y;
		const xj = ring[j].x;
		const yj = ring[j].y;
		if (((yi > p.y) !== (yj > p.y)) && (p.x < (xj - xi) * (p.y - yi) / (yj - yi) + xi)) {
			inside = !inside;
		}
	}
	return inside;
}

function pointOnSegment(p, a, b, eps) {
	const dx = b.x - a.x;
	const dy = b.y - a.y;
	const len2 = dx * dx + dy * dy;
	if (len2 === 0)
		return Math.hypot(p.x - a.x, p.y - a.y) <= eps;
	let t = ((p.x - a.x) * dx + (p.y - a.y) * dy) / len2;
	if (t < 0)
		t = 0;
	else if (t > 1)
		t = 1;
	const projX = a.x + t * dx;
	const projY = a.y + t * dy;
	return Math.hypot(p.x - projX, p.y - projY) <= eps;
}

function pointInRingOrOnBoundary(ring, p, eps = 1e-6) {
	if (ringContainsPoint(ring, p))
		return true;
	const n = ring.length;
	for (let i = 0, j = n - 1; i < n; j = i, i++) {
		if (pointOnSegment(p, ring[i], ring[j], eps))
			return true;
	}
	return false;
}

function ringContainsRing(outer, inner) {
	let minX = Infinity;
	let minY = Infinity;
	let maxX = -Infinity;
	let maxY = -Infinity;
	for (const p of outer) {
		if (p.x < minX)
			minX = p.x;
		if (p.x > maxX)
			maxX = p.x;
		if (p.y < minY)
			minY = p.y;
		if (p.y > maxY)
			maxY = p.y;
	}
	const eps = 1e-6;
	for (const p of inner) {
		if (p.x < minX - eps || p.x > maxX + eps || p.y < minY - eps || p.y > maxY + eps)
			return false;
	}
	for (const p of inner) {
		if (!pointInRingOrOnBoundary(outer, p, eps))
			return false;
	}
	return true;
}

// Build a nesting tree from a flat list of rings. For each ring, find the
// smallest (by absolute area) ring that strictly contains it. A ring with no
// parent is a top-level exterior. A child with opposite winding is a hole;
// a child with the same winding is an independent exterior (nested island).
function buildRingTree(rings) {
	const areas = rings.map(ringSignedArea);
	const parents = Array.from({ length: rings.length }).fill(-1);
	const children = rings.map(() => []);

	for (let i = 0; i < rings.length; i++) {
		let bestParent = -1;
		let bestArea = Infinity;
		for (let j = 0; j < rings.length; j++) {
			if (i === j)
				continue;
			if (Math.abs(areas[j]) <= Math.abs(areas[i]))
				continue;
			if (!ringContainsRing(rings[j], rings[i]))
				continue;
			if (Math.abs(areas[j]) < bestArea) {
				bestArea = Math.abs(areas[j]);
				bestParent = j;
			}
		}
		parents[i] = bestParent;
		if (bestParent >= 0)
			children[bestParent].push(i);
	}

	return { areas, parents, children };
}

// Group rings into polygons with holes using the nesting tree. Nested islands
// (rings inside holes with the same winding as the exterior) become separate
// polygons so they are not swallowed as holes.
function groupRingsIntoPolygons(rings) {
	if (rings.length === 0)
		return [];
	if (rings.length === 1)
		return [[rings[0]]];

	const { areas, parents, children } = buildRingTree(rings);
	const polygons = [];
	const used = new Set();

	function buildPolygon(extIdx) {
		if (used.has(extIdx))
			return;
		used.add(extIdx);
		const extArea = areas[extIdx];
		const poly = [rings[extIdx]];
		for (const childIdx of children[extIdx]) {
			if (extArea * areas[childIdx] < 0) {
				// Opposite winding: this child is a hole of extIdx.
				poly.push(rings[childIdx]);
				// Any descendants inside the hole with the same winding as the
				// exterior are nested islands and become separate polygons.
				for (const grandChildIdx of children[childIdx])
					buildPolygon(grandChildIdx);
			}
			else {
				// Same winding: independent exterior nested inside extIdx.
				buildPolygon(childIdx);
			}
		}
		polygons.push(poly);
	}

	for (let i = 0; i < rings.length; i++) {
		if (parents[i] < 0)
			buildPolygon(i);
	}

	return polygons;
}

function ensureModule() {
	if (!clipperModule) {
		throw new Error('Clipper2 module not initialized');
	}
}

function clipperUnion(polygonsA, polygonsB) {
	ensureModule();
	const a = toClipperPaths(polygonsA);
	const b = polygonsB ? toClipperPaths(polygonsB) : null;
	const result = b
		? clipperModule.UnionD(a, b, clipperModule.FillRule.NonZero, CLIPPER_PRECISION)
		: clipperModule.UnionSelfD(a, clipperModule.FillRule.NonZero, CLIPPER_PRECISION);
	return fromClipperPaths(result);
}

function clipperDifference(subject, clip) {
	ensureModule();
	const a = toClipperPaths(subject);
	const b = toClipperPaths(clip);
	const result = clipperModule.DifferenceD(a, b, clipperModule.FillRule.NonZero, CLIPPER_PRECISION);
	return fromClipperPaths(result);
}

function clipperIntersect(a, b) {
	ensureModule();
	const sa = toClipperPaths(a);
	const sb = toClipperPaths(b);
	const result = clipperModule.IntersectD(sa, sb, clipperModule.FillRule.NonZero, CLIPPER_PRECISION);
	return fromClipperPaths(result);
}

function clipperOffset(polygons, delta) {
	ensureModule();
	const module = clipperModule;
	const expanded = new module.PathsD();
	for (const poly of polygons) {
		if (!poly || poly.length === 0)
			continue;
		// Offset the exterior ring outward (or inward if delta < 0).
		const outerPath = module.MakePathD(poly[0].flatMap(p => [p.x, p.y]));
		const outerPaths = new module.PathsD();
		outerPaths.push_back(outerPath);
		const expandedOuter = module.InflatePathsD(
			outerPaths,
			delta,
			module.JoinType.Miter,
			module.EndType.Polygon,
			2,
			CLIPPER_PRECISION,
			CLIPPER_ARC_TOLERANCE,
		);
		// Offset holes in the opposite direction so they shrink/grow consistently
		// with the polygon boundary.
		if (poly.length > 1) {
			const holePaths = new module.PathsD();
			for (let i = 1; i < poly.length; i++) {
				const path = module.MakePathD(poly[i].flatMap(p => [p.x, p.y]));
				holePaths.push_back(path);
			}
			const expandedHoles = module.InflatePathsD(
				holePaths,
				-delta,
				module.JoinType.Miter,
				module.EndType.Polygon,
				2,
				CLIPPER_PRECISION,
				CLIPPER_ARC_TOLERANCE,
			);
			if (expandedHoles.size() > 0) {
				const diff = module.DifferenceD(
					expandedOuter,
					expandedHoles,
					module.FillRule.NonZero,
					CLIPPER_PRECISION,
				);
				for (let i = 0; i < diff.size(); i++)
					expanded.push_back(diff.get(i));
				continue;
			}
		}
		for (let i = 0; i < expandedOuter.size(); i++)
			expanded.push_back(expandedOuter.get(i));
	}
	return fromClipperPaths(expanded);
}

function clipperMorphologicalClose(polygons, delta) {
	ensureModule();
	if (delta <= 0)
		return polygons;
	const expanded = clipperOffset(polygons, delta);
	return clipperOffset(expanded, -delta);
}

function clipperOffsetOpen(polylines, delta) {
	ensureModule();
	const paths = toClipperPaths(polylines);
	const result = clipperModule.InflatePathsD(
		paths,
		delta,
		clipperModule.JoinType.Round,
		clipperModule.EndType.Round,
		2,
		CLIPPER_PRECISION,
		CLIPPER_ARC_TOLERANCE,
	);
	return fromClipperPaths(result);
}

function earcutTriangulate(polygon) {
	const earcutFn = getEarcut();
	if (!earcutFn) {
		throw new Error('earcut not available on window');
	}
	const vertices = [];
	const holes = [];
	for (let i = 0; i < polygon.length; i++) {
		if (i > 0)
			holes.push(vertices.length / 2);
		for (const p of polygon[i]) {
			vertices.push(p.x, p.y);
		}
	}
	const triangles = earcutFn(vertices, holes, 2);
	return {
		vertices: new Float64Array(vertices),
		triangles: new Uint32Array(triangles),
	};
}

// cdtTriangulate runs Shewchuk Triangle (via triangle-wasm) to produce a
// constrained Delaunay triangulation of one polygon (with optional holes) at
// exact-arithmetic precision. Seed points (e.g. pad centres) are added as
// forced Steiner vertices so boundary conditions line up exactly. Triangle's
// `-j` switch drops coincident input vertices, replacing the old Go-side
// dedupNearVertices heuristic. `-q` enforces a minimum interior angle and
// `-a` an upper area bound, eliminating the sliver faces that broke the
// earlier earcut pipeline.
//
// Inputs (all coordinates are in mm):
//   polygon    : [[ringExterior, hole, hole, ...], ...]
//   seedPoints : [{x, y}, ...]
//   options    : { minAngle: number, maxArea: number }
//
// Returns: { vertices: Float64Array, triangles: Uint32Array }
async function cdtTriangulate(polygon, seedPoints, options) {
	const Triangle = window.Triangle;
	if (!Triangle || typeof Triangle.triangulate !== 'function') {
		throw new Error('triangle-wasm not initialised on window.Triangle');
	}
	const polys = Array.isArray(polygon) ? polygon : [polygon];
	const pointlist = [];
	const segmentlist = [];
	const holelist = [];

	for (const poly of polys) {
		if (!Array.isArray(poly) || poly.length === 0)
			continue;
		const exterior = poly[0];
		const extBase = pointlist.length / 2;
		const extIdx = [];
		for (const p of exterior) {
			if (!Number.isFinite(p.x) || !Number.isFinite(p.y))
				continue;
			pointlist.push(p.x, p.y);
			extIdx.push(extBase + extIdx.length);
		}
		if (extIdx.length < 3)
			continue;
		for (let i = 0; i < extIdx.length; i++) {
			segmentlist.push(extIdx[i], extIdx[(i + 1) % extIdx.length]);
		}
		for (let h = 1; h < poly.length; h++) {
			const hole = poly[h];
			const holeBase = pointlist.length / 2;
			const hIdx = [];
			let cx = 0;
			let cy = 0;
			for (const p of hole) {
				if (!Number.isFinite(p.x) || !Number.isFinite(p.y))
					continue;
				pointlist.push(p.x, p.y);
				hIdx.push(holeBase + hIdx.length);
				cx += p.x;
				cy += p.y;
			}
			if (hIdx.length < 3)
				continue;
			for (let i = 0; i < hIdx.length; i++) {
				segmentlist.push(hIdx[i], hIdx[(i + 1) % hIdx.length]);
			}
			holelist.push(cx / hole.length, cy / hole.length);
		}
	}
	for (const sp of seedPoints || []) {
		if (!sp || !Number.isFinite(sp.x) || !Number.isFinite(sp.y))
			continue;
		pointlist.push(sp.x, sp.y);
	}
	const minAngle = options && Number.isFinite(options.minAngle) ? options.minAngle : 20;
	const maxArea = options && Number.isFinite(options.maxArea) ? options.maxArea : 1.5;
	const input = Triangle.makeIO({ pointlist, segmentlist, holelist });
	const output = Triangle.makeIO();
	Triangle.triangulate(
		{
			pslg: true,
			quality: minAngle,
			area: maxArea,
			jettison: true,
			quiet: true,
			bndMarkers: false,
		},
		input,
		output,
	);
	// The output arrays are subarray views onto the WASM heap; copy them out
	// before freeIO invalidates the underlying buffer.
	const vertices = new Float64Array(output.pointlist);
	const triangles = new Uint32Array(output.trianglelist);
	Triangle.freeIO(input, true);
	Triangle.freeIO(output);
	return { vertices, triangles };
}

window.padenGeometry = {
	init: initClipper,
	clipperUnion,
	clipperDifference,
	clipperIntersect,
	clipperOffset,
	clipperMorphologicalClose,
	clipperOffsetOpen,
	earcutTriangulate,
	cdtTriangulate,
};
