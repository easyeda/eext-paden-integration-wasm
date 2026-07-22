/* global Go */
const fs = require('node:fs');
const path = require('node:path');
const process = require('node:process');

const NODE_MAJOR = Number.parseInt(process.version.slice(1).split('.')[0], 10);
if (NODE_MAJOR < 20) {
	console.error('Node >= 20 required for WebAssembly.Memory growth');
	process.exit(1);
}

globalThis.window = globalThis;

const clipperPath = path.resolve('node_modules/clipper2-wasm/dist/umd/clipper2z.js');
globalThis.Clipper2ZFactory = require(clipperPath);

const bridgePath = path.resolve('dist/wasm-geometry-bridge.js');
require(bridgePath);

const wasmExecPath = path.resolve('dist/wasm_exec.js');
require(wasmExecPath);

function polygonArea(ring) {
	if (!ring || ring.length < 3)
		return 0;
	let area = 0;
	const n = ring.length;
	for (let i = 0; i < n; i++) {
		const j = (i + 1) % n;
		area += ring[i][0] * ring[j][1] - ring[j][0] * ring[i][1];
	}
	return Math.abs(area) / 2;
}

async function main() {
	await globalThis.padenGeometry.init();

	const wasmBuffer = fs.readFileSync('dist/paden.wasm');
	const go = new Go();
	const result = await WebAssembly.instantiate(wasmBuffer, go.importObject);
	go.run(result.instance);

	const zipData = fs.readFileSync('test/test-paden-2.zip');
	const configJson = fs.readFileSync('test/config.json', 'utf8');
	const ipc356aText = fs.readFileSync('test/test-paden-2.356a', 'utf8');

	console.log('[test_analyze] calling analyzeGerber...');
	const resultJson = await globalThis.padne.analyzeGerber(zipData, configJson, ipc356aText);
	const parsed = JSON.parse(resultJson);

	console.log('[test_analyze] success:', parsed.success);
	if (!parsed.success)
		console.error('[test_analyze] message:', parsed.message);

	if (parsed.diagnostics) {
		console.log('[test_analyze] diagnostics:');
		for (const line of parsed.diagnostics)
			console.log(' ', line);
	}

	if (parsed.layer_solutions) {
		console.log('[test_analyze] layer solutions:', parsed.layer_solutions.length);
		for (let i = 0; i < parsed.layer_solutions.length; i++) {
			const ls = parsed.layer_solutions[i];
			console.log(`  layer[${i}] meshes=${ls.meshes?.length ?? 0} potentials=${ls.potentials?.length ?? 0}`);
		}
	}

	if (parsed.solver_info)
		console.log('[test_analyze] solver_info:', parsed.solver_info);

	if (parsed.current_warnings)
		console.log('[test_analyze] current_warnings:', parsed.current_warnings);

	if (parsed.layer_boundaries) {
		console.log('[test_analyze] layer_boundaries:');
		for (const [layerName, polys] of Object.entries(parsed.layer_boundaries)) {
			console.log(`  layer='${layerName}' polygons=${polys.length}`);
			for (let i = 0; i < polys.length; i++) {
				const p = polys[i];
				const area = polygonArea(p.exterior);
				console.log(`    poly[${i}] area=${area.toFixed(4)} holes=${p.holes?.length ?? 0}`);
			}
		}
	}

	process.exit(parsed.success ? 0 : 1);
}

main().catch((e) => {
	console.error(e);
	process.exit(1);
});
