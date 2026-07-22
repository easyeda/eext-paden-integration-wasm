/**
 * Geometry bridge loaded by ui/wasm-host.html.
 *
 * Exposes window.padenGeometry with:
 *   - gerberToPolygons(gerberText) -> MultiPolygon as nested arrays
 *   - clipperUnion(polygons) -> polygons
 *   - clipperDifference(subject, clip) -> polygons
 *   - clipperIntersect(a, b) -> polygons
 *   - clipperOffset(polygons, delta) -> polygons
 *   - clipperMorphologicalClose(polygons, delta) -> polygons
 *   - earcutTriangulate(polygon) -> { vertices: Float64Array, triangles: Uint32Array }
 *
 * All polygons use the format:
 *   [ [ [{x,y}, ...], hole, hole, ... ], ... ] ]
 */

// Dependencies are loaded by the host as modules and exposed as globals.
const CLIPPER_PRECISION = 6;

let clipperModule = null;

function getClipperFactory() {
	return window.Clipper2ZFactory;
}

function getEarcut() {
	return window.earcut;
}

function getParse() {
	return window.tracespaceParser?.parse;
}

function getPlot() {
	return window.tracespacePlotter?.plot;
}

async function initClipper() {
	if (clipperModule)
		return clipperModule;
	const factory = getClipperFactory();
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

function ensureModule() {
	if (!clipperModule) {
		throw new Error('Clipper2 module not initialized');
	}
}

function interpolateArc(start, end, center, radius) {
	const points = [];
	const startAngle = Math.atan2(start[1] - center[1], start[0] - center[0]);
	const endAngle = Math.atan2(end[1] - center[1], end[0] - center[0]);
	let sweep = endAngle - startAngle;
	while (sweep <= -Math.PI) sweep += 2 * Math.PI;
	while (sweep > Math.PI) sweep -= 2 * Math.PI;
	const steps = Math.max(8, Math.ceil(Math.abs(sweep) / (Math.PI / 16)));
	for (let i = 0; i <= steps; i++) {
		const t = i / steps;
		const angle = startAngle + sweep * t;
		points.push({
			x: center[0] + radius * Math.cos(angle),
			y: center[1] + radius * Math.sin(angle),
		});
	}
	return points;
}

function pathSegmentsToPoints(segments) {
	if (!segments || segments.length === 0)
		return [];
	const points = [{ x: segments[0].start[0], y: segments[0].start[1] }];
	for (const seg of segments) {
		if (seg.type === 'line') {
			points.push({ x: seg.end[0], y: seg.end[1] });
		}
		else if (seg.type === 'arc') {
			const arcPoints = interpolateArc(seg.start, seg.end, seg.center, seg.radius);
			for (const p of arcPoints.slice(1)) {
				points.push(p);
			}
		}
	}
	return points;
}

// tracespace may group several disjoint strokes (separated by D02 moves) into a
// single imagePath.  Split the segment list into connected polylines so each
// stroke is offset independently.
function pathSegmentsToConnectedPolylines(segments) {
	if (!segments || segments.length === 0)
		return [];
	const polylines = [];
	let current = [{ x: segments[0].start[0], y: segments[0].start[1] }];
	function pushSeg(seg) {
		if (seg.type === 'line') {
			current.push({ x: seg.end[0], y: seg.end[1] });
		}
		else if (seg.type === 'arc') {
			const arcPoints = interpolateArc(seg.start, seg.end, seg.center, seg.radius);
			for (const p of arcPoints.slice(1))
				current.push(p);
		}
	}
	pushSeg(segments[0]);
	for (let i = 1; i < segments.length; i++) {
		const prev = segments[i - 1];
		const seg = segments[i];
		const dx = seg.start[0] - prev.end[0];
		const dy = seg.start[1] - prev.end[1];
		if (Math.hypot(dx, dy) > 1e-9) {
			polylines.push(current);
			current = [{ x: seg.start[0], y: seg.start[1] }];
		}
		pushSeg(seg);
	}
	polylines.push(current);
	return polylines;
}

function shapeToPolygons(shape) {
	const polygons = [];
	if (shape.type === 'circle') {
		const { cx, cy, r } = shape;
		const ring = [];
		const steps = 32;
		for (let i = 0; i < steps; i++) {
			const angle = (i / steps) * 2 * Math.PI;
			ring.push({ x: cx + r * Math.cos(angle), y: cy + r * Math.sin(angle) });
		}
		polygons.push([ring]);
	}
	else if (shape.type === 'rectangle') {
		const { x, y, xSize, ySize } = shape;
		polygons.push([[
			{ x, y },
			{ x: x + xSize, y },
			{ x: x + xSize, y: y + ySize },
			{ x, y: y + ySize },
		]]);
	}
	else if (shape.type === 'polygon') {
		polygons.push([shape.points.map(p => ({ x: p[0], y: p[1] }))]);
	}
	else if (shape.type === 'outline') {
		const pts = pathSegmentsToPoints(shape.segments);
		if (pts.length >= 3)
			polygons.push([pts]);
	}
	else if (shape.type === 'layeredShape') {
		for (const sub of shape.shapes || []) {
			const subPolys = shapeToPolygons(sub);
			if (sub.erase) {
				// Holes are handled by clipping; for now return as separate polygons.
				// The caller should use clipperDifference to apply erasures.
				subPolys.forEach(p => p._erase = true);
			}
			polygons.push(...subPolys);
		}
	}
	return polygons;
}

function imageGraphicToPolygons(graphic) {
	let polys = [];
	if (graphic.type === 'imageShape') {
		polys = shapeToPolygons(graphic.shape);
	}
	else if (graphic.type === 'imageRegion') {
		const pts = pathSegmentsToPoints(graphic.segments);
		if (pts.length >= 3) {
			// tracespace returns a region's outer boundary and holes as one long
			// chain of segments that may self-intersect. Clipper2's UnionSelfD
			// normalizes this into a clean polygon with holes.
			try {
				polys = clipperUnion([[pts]]);
			}
			catch {
				polys = [[pts]];
			}
		}
	}
	else if (graphic.type === 'imagePath') {
		// Stroke with width: convert to polygon by offsetting each connected
		// sub-path independently.  tracespace groups disjoint D01 strokes under
		// one imagePath, so concatenating them creates self-intersecting garbage.
		const polylines = pathSegmentsToConnectedPolylines(graphic.segments);
		for (const pts of polylines) {
			if (pts.length < 2)
				continue;
			const linePoly = [[pts]];
			try {
				polys.push(...clipperOffsetOpen(linePoly, graphic.width / 2));
			}
			catch {
				// ignore single failed stroke
			}
		}
	}

	// tracespace plotter uses graphic.polarity === 'clear' for negative geometry
	// (e.g. tracks/pads carved out of a copper pour).  Mark these so they are
	// subtracted from the dark polygons later.
	if ((graphic.polarity === 'clear' || graphic.polarity === 'erase') && polys.length > 0) {
		polys.forEach(p => p._erase = true);
	}

	return polys;
}

function flattenLayeredErasures(polygons) {
	let result = [];
	const erasePolys = [];
	for (const p of polygons) {
		if (p._erase) {
			delete p._erase;
			erasePolys.push(p);
		}
		else {
			result.push(p);
		}
	}
	if (erasePolys.length > 0) {
		result = clipperDifference(result, erasePolys);
	}
	return result;
}

function gerberToPolygons(gerberText) {
	console.warn('[geometry bridge] ===== bridge v20260709-hole-grouping =====');
	const parseFn = getParse();
	const plotFn = getPlot();

	// EasyEDA's Gerber generator inserts non-breaking spaces (U+00A0) in comments.
	// tracespace's lexer only recognizes regular spaces/tabs as whitespace, so NBSP
	// is treated as an unexpected token and aborts parsing. Normalize it first.
	gerberText = (gerberText ?? '').replace(/\xA0/g, ' ');

	const headLines = gerberText.split('\n').slice(0, 40).join('\n');
	console.warn('[geometry bridge] gerberToPolygons input length:', gerberText?.length, 'parseFn:', typeof parseFn, 'plotFn:', typeof plotFn);
	console.warn(`[geometry bridge] gerberText head:\n${headLines}`);
	if (!parseFn || !plotFn) {
		throw new Error('tracespace parser/plotter not available on window');
	}
	let tree;
	try {
		tree = parseFn(gerberText);
	}
	catch (e) {
		console.error('[geometry bridge] tracespace parse error:', e);
		throw e;
	}
	console.warn('[geometry bridge] parsed tree:', { type: tree?.type, filetype: tree?.filetype, childrenCount: tree?.children?.length });
	if (tree?.children) {
		for (let i = 0; i < Math.min(tree.children.length, 20); i++) {
			const c = tree.children[i];
			console.warn('[geometry bridge] tree child', i, { type: c?.type, graphic: c?.graphic, code: c?.code, name: c?.name, comment: c?.comment });
		}
	}
	let image;
	try {
		image = plotFn(tree);
	}
	catch (e) {
		console.error('[geometry bridge] tracespace plot error:', e);
		throw e;
	}
	console.warn('[geometry bridge] plotted image:', { type: image?.type, units: image?.units, size: image?.size, childrenCount: image?.children?.length });

	let darkCount = 0;
	let eraseCount = 0;
	let all = [];
	for (const child of image.children || []) {
		const childPolys = imageGraphicToPolygons(child);
		const isErase = childPolys.some(p => p._erase);
		if (isErase)
			eraseCount += childPolys.length;
		else
			darkCount += childPolys.length;
		console.warn('[geometry bridge] image child:', { type: child?.type, shapeType: child?.shape?.type, region: child?.region, width: child?.width, polarity: child?.polarity, polygons: childPolys.length, isErase });
		all.push(...childPolys);
	}
	console.warn('[geometry bridge] raw graphics:', { darkCount, eraseCount, total: all.length });

	all = flattenLayeredErasures(all);
	console.warn('[geometry bridge] after erasure subtraction:', { totalPolygons: all.length });

	// tracespace returns a flat list of single-ring polygons whose nesting may be
	// ambiguous (holes touching their outer boundary). Use Clipper2's UnionSelfD
	// to normalise winding and produce clean polygons with holes.
	const rings = [];
	for (const poly of all) {
		for (const ring of poly)
			rings.push(ring);
	}
	try {
		all = clipperUnion([rings]);
	}
	catch {
		all = groupRingsIntoPolygons(rings);
	}
	console.warn('[geometry bridge] after hole grouping:', { totalPolygons: all.length, totalRings: rings.length });
	for (let i = 0; i < all.length; i++) {
		const poly = all[i];
		const bounds = polygonBounds(poly);
		const area = polygonSignedArea(poly);
		console.warn(`[geometry bridge] grouped poly[${i}]: rings=${poly.length} area=${area.toFixed(3)} bounds=[${bounds.minX.toFixed(2)},${bounds.maxX.toFixed(2)}]x[${bounds.minY.toFixed(2)},${bounds.maxY.toFixed(2)}]`);
	}

	return all;
}

function polygonBounds(poly) {
	let minX = Infinity;
	let minY = Infinity;
	let maxX = -Infinity;
	let maxY = -Infinity;
	for (const ring of poly) {
		for (const p of ring) {
			if (p.x < minX)
				minX = p.x;
			if (p.x > maxX)
				maxX = p.x;
			if (p.y < minY)
				minY = p.y;
			if (p.y > maxY)
				maxY = p.y;
		}
	}
	return { minX, minY, maxX, maxY };
}

function polygonSignedArea(poly) {
	let area = 0;
	for (let i = 0; i < poly.length; i++) {
		const ring = poly[i];
		let a = 0;
		const n = ring.length;
		for (let j = 0; j < n; j++) {
			const k = (j + 1) % n;
			a += ring[j].x * ring[k].y - ring[k].x * ring[j].y;
		}
		area += (i === 0 ? 1 : -1) * a / 2;
	}
	return area;
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
			0.25,
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
				0.25,
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
		0.25,
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

window.padenGeometry = {
	init: initClipper,
	gerberToPolygons,
	clipperUnion,
	clipperDifference,
	clipperIntersect,
	clipperOffset,
	clipperMorphologicalClose,
	clipperOffsetOpen,
	earcutTriangulate,
};
