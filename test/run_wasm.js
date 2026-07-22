const fs = require('fs');
const path = require('path');
const { pathToFileURL } = require('url');

const repoRoot = path.resolve(__dirname, '..');
const distDir = path.join(repoRoot, 'dist');

// Expose browser globals that the geometry bridge expects.
globalThis.window = globalThis;
globalThis.earcut = require('earcut');

const Clipper2Z = require('clipper2-wasm').default;
const clipperWasmPath = path.join(distDir, 'clipper2z.wasm');
globalThis.Clipper2ZFactory = () => {
	return globalThis.Clipper2Z({
		locateFile: () => 'file://' + clipperWasmPath.replace(/\\/g, '/'),
	});
};
globalThis.Clipper2Z = Clipper2Z;

// Load Go WASM runtime (defines globalThis.Go).
require(path.join(distDir, 'wasm_exec.js'));

async function main() {
	// Load the geometry bridge; it attaches window.padenGeometry.
	await import(pathToFileURL(path.join(distDir, 'wasm-geometry-bridge.js')).href);

	console.log('[Node] initializing geometry bridge...');
	await globalThis.padenGeometry.init();
	console.log('[Node] geometry bridge ready');

	// Load and instantiate the Go WASM module.
	const wasmPath = path.join(distDir, 'paden.wasm');
	const wasmBuffer = fs.readFileSync(wasmPath);
	const go = new globalThis.Go();
	const { instance } = await WebAssembly.instantiate(wasmBuffer, go.importObject);
	go.run(instance);

	console.log('[Node] WASM runtime ready, version:', globalThis.padne.version());

	// Read config JSON used by the Python reference.
	const configJson = fs.readFileSync(path.join(__dirname, 'config.json'), 'utf8');
	const zipBytes = fs.readFileSync(path.join(__dirname, 'test-paden.zip'));

	console.log('[Node] calling analyzeGerber...');
	const resultJson = await globalThis.padne.analyzeGerber(zipBytes, configJson);
	const result = JSON.parse(resultJson);

	const outPath = path.join(__dirname, 'wasm_result.json');
	fs.writeFileSync(outPath, JSON.stringify(result, null, 2), 'utf8');
	console.log('[Node] result written to', outPath);
	console.log('Success:', result.success);
	console.log('Message:', result.message);
	if (result.diagnostics) {
		console.log('Diagnostics lines:', result.diagnostics.length);
		result.diagnostics.forEach(line => console.log(line));
	}

	process.exit(0);
}

main().catch((err) => {
	console.error(err);
	process.exit(1);
});
