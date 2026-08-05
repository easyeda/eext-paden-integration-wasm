/* Run test-1 (single-layer 3V3 trace) through the WASM and inspect the mesh
 * for broken faces that do not match the 3V3 copper geometry. */
const fs = require('fs');
const path = require('path');
const { pathToFileURL } = require('url');

const repoRoot = path.resolve(__dirname, '..');
const distDir = path.join(repoRoot, 'dist');

globalThis.window = globalThis;
globalThis.earcut = require('earcut');

const Clipper2Z = require('clipper2-wasm').default;
const clipperWasmPath = path.join(distDir, 'clipper2z.wasm');
globalThis.Clipper2ZFactory = () => globalThis.Clipper2Z({
	locateFile: () => 'file://' + clipperWasmPath.replace(/\\/g, '/'),
});
globalThis.Clipper2Z = Clipper2Z;

require(path.join(distDir, 'wasm_exec.js'));

async function main() {
	await import(pathToFileURL(path.join(distDir, 'wasm-geometry-bridge.js')).href);
	await globalThis.padenGeometry.init();
	const triangleDataUrl = `data:application/wasm;base64,${fs.readFileSync(path.join(distDir, 'triangle.out.wasm')).toString('base64')}`;
	await globalThis.Triangle.init(triangleDataUrl);
	const wasmBuffer = fs.readFileSync(path.join(distDir, 'paden.wasm'));
	const go = new globalThis.Go();
	const { instance } = await WebAssembly.instantiate(wasmBuffer, go.importObject);
	go.run(instance);
	console.log('[Node] WASM ready, version:', globalThis.padne.version());

	const NET = '3V3';
	const odbBytes = fs.readFileSync(path.join(__dirname, 'test-1.tgz'));

	// Pads from test-1 netlist (y is already in ODB/board space, positives up).
	const srcVcc = { x: 26.07818, y: 19.11290564, layer: 'TopLayer', net: NET, is_tht: false, hole_diameter: 0, pin_name: 'S1' };
	const loadVcc = { x: 52.840636, y: 16.129, layer: 'TopLayer', net: NET, is_tht: false, hole_diameter: 0, pin_name: 'L1' };
	const gndA = { x: 18.87982, y: 19.11290564, layer: 'TopLayer', net: 'GND', is_tht: false, hole_diameter: 0, pin_name: 'G1' };
	const gndB = { x: 54.347364, y: 16.129, layer: 'TopLayer', net: 'GND', is_tht: false, hole_diameter: 0, pin_name: 'G2' };
	const pads = [srcVcc, loadVcc, gndA, gndB];

	const config = {
		project_name: 'test-1',
		layers: [
			{ name: 'TopLayer', conductance: 59500.0, layer_id: 1 },
		],
		layer_cu_thickness: { TopLayer: 0.035 },
		vias: [],
		pads,
		tracks: [],
		rails: [],
		gnd_net: 'GND',
		temp_rise: 10.0,
		easyeda_bounds: { minX: 4.19, minY: -32.89, maxX: 61.49, maxY: -1.50 },
		sources: [{
			net: NET, voltage: 3.3, gnd_net: 'GND', ref_des: 'S',
			pads: [srcVcc], gnd_pads: [gndA],
		}],
		loads: [{
			net: NET, current: 0.5, gnd_net: 'GND', ref_des: 'L',
			pads: [loadVcc], gnd_pads: [gndB],
		}],
	};
	const t0 = Date.now();
	const resultJson = await globalThis.padne.analyzeODB(odbBytes, JSON.stringify(config));
	const dt = Date.now() - t0;
	const result = JSON.parse(resultJson);
	console.log('[Node] analyze took', dt, 'ms  success:', result.success, 'msg:', result.message);
	fs.writeFileSync(path.join(__dirname, 'wasm_test1_result.json'), JSON.stringify(result, null, 2));
	console.log('[Node] saved wasm_test1_result.json');

	for (const line of (result.diagnostics || []).slice(-15)) console.log('  ', line);

	const meshes = (result.layer_solutions || []).flatMap((s, li) => (s.meshes || []).map(m => ({ ...m, _layer: s.layer_name, _li: li })));
	console.log('\nMesh stats (per network on each layer):');
	for (const [mi, m] of meshes.entries()) {
		const verts = m.vertices;
		const tris = m.triangles;
		let totalArea = 0, largestArea = 0, sliverCount = 0;
		let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
		for (const v of verts) {
			if (v[0] < minX) minX = v[0]; if (v[0] > maxX) maxX = v[0];
			if (v[1] < minY) minY = v[1]; if (v[1] > maxY) maxY = v[1];
		}
		for (const tri of tris) {
			const idx = tri.vertices || tri;
			const a = verts[idx[0]], b = verts[idx[1]], c = verts[idx[2]];
			const triArea = Math.abs((b[0]-a[0])*(c[1]-a[1]) - (c[0]-a[0])*(b[1]-a[1])) / 2;
			totalArea += triArea;
			if (triArea > largestArea) largestArea = triArea;
			const e0 = Math.hypot(b[0]-a[0], b[1]-a[1]);
			const e1 = Math.hypot(c[0]-b[0], c[1]-b[1]);
			const e2 = Math.hypot(a[0]-c[0], a[1]-c[1]);
			const mn2 = Math.min(e0,e1,e2);
			const aspect = Math.max(e0,e1,e2) / Math.max(mn2, 1e-9);
			if (aspect > 5) sliverCount++;
		}
		console.log(`  net='${m.net}' layer=${m._layer} verts=${verts.length} tris=${tris.length} area=${totalArea.toFixed(4)} bbox=[${minX.toFixed(2)},${maxX.toFixed(2)}]x[${minY.toFixed(2)},${maxY.toFixed(2)}] largestTri=${largestArea.toExponential(3)} aspect>5:${sliverCount}/${tris.length}`);
	}
	process.exit(0);
}

main().catch(err => { console.error(err); process.exit(1); });