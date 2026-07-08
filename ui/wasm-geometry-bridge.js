/**
 * Geometry bridge loaded by ui/wasm-host.html.
 *
 * Exposes window.padenGeometry with:
 *   - gerberToPolygons(gerberText) -> MultiPolygon as nested arrays
 *   - clipperUnion(polygons) -> polygons
 *   - clipperDifference(subject, clip) -> polygons
 *   - clipperIntersect(a, b) -> polygons
 *   - clipperOffset(polygons, delta) -> polygons
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

function fromClipperPaths(paths) {
	const out = [];
	const n = paths.size();
	for (let i = 0; i < n; i++) {
		const path = paths.get(i);
		const ring = [];
		const m = path.size();
		for (let j = 0; j < m; j++) {
			const pt = path.get(j);
			ring.push({ x: pt.x, y: pt.y });
		}
		out.push(ring);
	}
	return [out];
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
	if (graphic.type === 'imageShape') {
		return shapeToPolygons(graphic.shape);
	}
	if (graphic.type === 'imageRegion') {
		const pts = pathSegmentsToPoints(graphic.segments);
		if (pts.length >= 3)
			return [[pts]];
	}
	if (graphic.type === 'imagePath') {
		// Stroke with width: convert to polygon by offsetting the path.
		const pts = pathSegmentsToPoints(graphic.segments);
		if (pts.length < 2)
			return [];
		const linePoly = [[pts]];
		try {
			return clipperOffset(linePoly, graphic.width / 2);
		}
		catch {
			return [];
		}
	}
	return [];
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
	console.warn('[geometry bridge] ===== bridge v20260708-nbsp-fix =====');
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
	let all = [];
	for (const child of image.children || []) {
		const childPolys = imageGraphicToPolygons(child);
		console.warn('[geometry bridge] image child:', { type: child?.type, shapeType: child?.shape?.type, region: child?.region, width: child?.width, polygons: childPolys.length });
		all.push(...childPolys);
	}
	all = flattenLayeredErasures(all);
	console.warn('[geometry bridge] gerberToPolygons result:', { totalPolygons: all.length });
	return all;
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
	const paths = toClipperPaths(polygons);
	const result = clipperModule.InflatePathsD(
		paths,
		delta,
		clipperModule.JoinType.Miter,
		clipperModule.EndType.Polygon,
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
	earcutTriangulate,
};
