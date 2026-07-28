/* Generate a real pad-based config for test-3 and run WASM analysis. */
const fs = require('fs');
const path = require('path');
const { pathToFileURL } = require('url');
const JSZip = require('jszip');

const repoRoot = path.resolve(__dirname, '..');
const distDir = path.join(repoRoot, 'dist');

const MIL_TO_MM = 0.0254;
const milToMm = v => v * MIL_TO_MM;

globalThis.window = globalThis;
globalThis.earcut = require('earcut');

const Clipper2Z = require('clipper2-wasm').default;
const clipperWasmPath = path.join(distDir, 'clipper2z.wasm');
globalThis.Clipper2ZFactory = () => globalThis.Clipper2Z({
	locateFile: () => 'file://' + clipperWasmPath.replace(/\\/g, '/'),
});
globalThis.Clipper2Z = Clipper2Z;

require(path.join(distDir, 'wasm_exec.js'));

function layerName(code) {
	if (code === 'T') return 'TopLayer';
	if (code === 'B') return 'BottomLayer';
	return code;
}

async function main() {
	await import(pathToFileURL(path.join(distDir, 'wasm-geometry-bridge.js')).href);
	await globalThis.padenGeometry.init();
	const wasmPath = path.join(distDir, 'paden.wasm');
	const wasmBuffer = fs.readFileSync(wasmPath);
	const go = new globalThis.Go();
	const { instance } = await WebAssembly.instantiate(wasmBuffer, go.importObject);
	go.run(instance);
	console.log('[Node] WASM ready, version:', globalThis.padne.version());

	// Read FlyingProbeTesting.json from inside the zip to get real pad coords
	const zipBytes = fs.readFileSync(path.join(__dirname, 'test-3.zip'));
	const zip = await JSZip.loadAsync(zipBytes);
	const fpText = await zip.file('FlyingProbeTesting.json').async('string');
	const fp = JSON.parse(fpText);
	const f = fp.pins.fields;
	const idx = Object.fromEntries(f.map((n, i) => [n, i]));
	const rows = fp.pins.rows;

	// Build all pads
	const seen = new Set();
	const pads = [];
	let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
	for (const r of rows) {
		const x = r[idx.PIN_X];
		const y = r[idx.PIN_Y];
		const net = r[idx.NET_NAME];
		const layer = layerName(r[idx.LAYER]);
		const hole = r[idx.HOLE_SIZE];
		const pinName = r[idx.PIN_NAME];
		const key = `${round(x,4)}_${round(y,4)}_${net}_${layer}`;
		if (seen.has(key)) continue;
		seen.add(key);
		const isTHT = hole > 0;
		const xmm = milToMm(x);
		const ymm = milToMm(y);
		if (xmm < minX) minX = xmm;
		if (ymm < minY) minY = ymm;
		if (xmm > maxX) maxX = xmm;
		if (ymm > maxY) maxY = ymm;
		pads.push({
			x: xmm, y: ymm, layer, net,
			is_tht: isTHT,
			hole_diameter: isTHT ? milToMm(hole) : 0.0,
			pin_name: pinName,
		});
	}

	// Pick source/load pairs on VCC (3V3) with a GND ground ref
	const sourceVccPads = [pads.find(p => p.net === '3V3' && p.layer === 'TopLayer' && p.pin_name === 'C2_1')].filter(Boolean);
	let loadVccPads = [pads.find(p => p.net === '3V3' && p.layer === 'TopLayer' && p.pin_name === 'C17_1')].filter(Boolean);
	if (loadVccPads.length === 0) {
		loadVccPads = pads.filter(p => p.net === '3V3' && !sourceVccPads.includes(p)).slice(0, 3);
	}
	if (sourceVccPads.length === 0) {
		sourceVccPads.push(pads.find(p => p.net === '3V3' && p.layer === 'TopLayer'));
	}
	const gndPads = pads.filter(p => p.net === 'GND').slice(0, 5);
	console.log('[Node] sourceVcc pads:', sourceVccPads.map(p => `${p.pin_name}@(${p.x.toFixed(3)},${p.y.toFixed(3)})`));
	console.log('[Node] loadVcc pads:', loadVccPads.map(p => `${p.pin_name}@(${p.x.toFixed(3)},${p.y.toFixed(3)})`));
	console.log('[Node] gnd pads:', gndPads.map(p => `${p.pin_name}@(${p.x.toFixed(3)},${p.y.toFixed(3)})`));
	console.log('[Node] total pads:', pads.length);

	const config = {
		project_name: 'test-3',
		layers: [
			{ name: 'TopLayer', conductance: 59500.0, layer_id: 1 },
			{ name: 'BottomLayer', conductance: 59500.0, layer_id: 2 },
		],
		layer_cu_thickness: { TopLayer: 0.035, BottomLayer: 0.035 },
		vias: [],
		pads,
		tracks: [],
		rails: [],
		gnd_net: 'GND',
		temp_rise: 10.0,
		easyeda_bounds: { minX: minX - 1, minY: minY - 1, maxX: maxX + 1, maxY: maxY + 1 },
		sources: [{
			net: '3V3', voltage: 3.3, gnd_net: 'GND', ref_des: 'VSRC',
			pads: sourceVccPads, gnd_pads: gndPads,
		}],
		loads: [{
			net: '3V3', current: 0.5, gnd_net: 'GND', ref_des: 'LOAD',
			pads: loadVccPads, gnd_pads: gndPads,
		}],
	};
	const configJson = JSON.stringify(config);
	const ipcText = fs.readFileSync(path.join(__dirname, 'test-3.356a'), 'utf8');
	console.log('[Node] calling analyzeGerber...');
	const t0 = Date.now();
	const resultJson = await globalThis.padne.analyzeGerber(zipBytes, configJson, ipcText);
	const dt = Date.now() - t0;
	const result = JSON.parse(resultJson);
	console.log('[Node] analyze took', dt, 'ms');
	console.log('[Node] success:', result.success, 'message:', result.message);
	fs.writeFileSync(path.join(__dirname, 'wasm_test3_result.json'), JSON.stringify(result, null, 2));
	console.log('[Node] saved result');

	// Print last 12 diagnostics to check if mesh step ran
	for (const line of (result.diagnostics || []).slice(-15)) console.log(' ', line);

	const meshes = (result.layer_solutions || []).flatMap((s, li) => (s.meshes || []).map(m => ({ ...m, _layer: s.layer, _net: s.net })));
	console.log('\nMesh stats (per network on each layer):');
	for (const [mi, m] of meshes.entries()) {
		const verts = m.vertices;
		const tris = m.triangles;
		let edgeMin = Infinity, edgeMax = -Infinity, aspectMax = 0, sliverCount = 0;
		let totalArea = 0, largestArea = 0;
		for (const tri of tris) {
			const idx = tri.vertices || tri;
			const a = verts[idx[0]], b = verts[idx[1]], c = verts[idx[2]];
			const ax = a[0], ay = a[1], bx = b[0], by = b[1], cx_ = c[0], cy = c[1];
			const e0 = Math.hypot(bx-ax, by-ay);
			const e1 = Math.hypot(cx_-bx, cy-by);
			const e2 = Math.hypot(ax-cx_, ay-cy);
			const mn2 = Math.min(e0, e1, e2);
			const mx2 = Math.max(e0, e1, e2);
			if (mn2 < edgeMin) edgeMin = mn2;
			if (mx2 > edgeMax) edgeMax = mx2;
			const aspect = mx2 / Math.max(mn2, 1e-9);
			if (aspect > aspectMax) aspectMax = aspect;
			const cax = ((bx-ax)*(cx_-ax)+(by-ay)*(cy-ay))/(e0*Math.hypot(cx_-ax,cy-ay));
			const angA = Math.acos(Math.min(1, Math.max(-1, cax)));
			const cbx = ((cx_-bx)*(ax-bx)+(cy-by)*(ay-by))/(e1*Math.hypot(ax-bx,ay-by));
			const angB = Math.acos(Math.min(1, Math.max(-1, cbx)));
			const ccx = ((ax-cx_)*(bx-cx_)+(ay-cy)*(by-cy))/(e2*Math.hypot(bx-cx_,by-cy));
			const angC = Math.acos(Math.min(1, Math.max(-1, ccx)));
			const angMin = Math.min(angA, angB, angC) * 180 / Math.PI;
			if (angMin < 10) sliverCount++;
			const triArea = Math.abs((bx-ax)*(cy-ay) - (cx_-ax)*(by-ay)) / 2;
			totalArea += triArea;
			if (triArea > largestArea) largestArea = triArea;
		}
		const avgArea = tris.length ? totalArea / tris.length : 0;
		console.log(`  net='${m._net}' layer=${m._layer} verts=${verts.length} tris=${tris.length} edgeLen=[${edgeMin.toExponential(2)},${edgeMax.toExponential(2)}] maxAspect=${aspectMax.toFixed(1)} slivers(<10°):${sliverCount}/${tris.length} largestTri=${largestArea.toExponential(3)} avgTri=${avgArea.toExponential(3)}`);
	}
	process.exit(0);
}

function round(x, digits) {
	const f = Math.pow(10, digits);
	return Math.round(x * f) / f;
}

main().catch(err => { console.error(err); process.exit(1); });
