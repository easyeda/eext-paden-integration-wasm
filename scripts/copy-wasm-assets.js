/**
 * Copy WASM-related assets into dist/ after the Go build.
 */

const { execSync } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');

const repoRoot = path.join(__dirname, '..');
const distDir = path.join(repoRoot, 'dist');

function copyFile(src, dst) {
	if (!fs.existsSync(src)) {
		console.warn(`[copy-wasm-assets] source not found: ${src}`);
		return;
	}
	fs.mkdirSync(path.dirname(dst), { recursive: true });
	fs.copyFileSync(src, dst);
	const sizeKB = (fs.statSync(dst).size / 1024).toFixed(1);
	console.log(`[copy-wasm-assets] ${path.basename(dst)} -> dist/ (${sizeKB} KB)`);
}

function findGoRoot() {
	try {
		return execSync('go env GOROOT', { encoding: 'utf-8', cwd: repoRoot }).trim();
	}
	catch (e) {
		throw new Error(`Failed to locate Go root: ${e.message}`);
	}
}

function main() {
	const goRoot = findGoRoot();

	// Go WASM JavaScript support file (location changed in Go 1.24+).
	const wasmExecCandidates = [
		path.join(goRoot, 'lib', 'wasm', 'wasm_exec.js'),
		path.join(goRoot, 'misc', 'wasm', 'wasm_exec.js'),
	];

	let wasmExecFound = false;
	for (const candidate of wasmExecCandidates) {
		if (fs.existsSync(candidate)) {
			copyFile(candidate, path.join(distDir, 'wasm_exec.js'));
			wasmExecFound = true;
			break;
		}
	}

	if (!wasmExecFound) {
		throw new Error(`wasm_exec.js not found under ${goRoot}`);
	}

	// Clipper2 WASM ES module and its binary (used by the geometry bridge).
	const clipperJsSrc = path.join(repoRoot, 'node_modules', 'clipper2-wasm', 'dist', 'es', 'clipper2z.js');
	const clipperWasmSrc = path.join(repoRoot, 'node_modules', 'clipper2-wasm', 'dist', 'es', 'clipper2z.wasm');

	if (fs.existsSync(clipperJsSrc) && fs.existsSync(clipperWasmSrc)) {
		copyFile(clipperJsSrc, path.join(distDir, 'clipper2z.js'));
		copyFile(clipperWasmSrc, path.join(distDir, 'clipper2z.wasm'));
	}
	else {
		console.warn('[copy-wasm-assets] clipper2-wasm ES build not found; skip');
	}

	// tracespace Gerber parser/plotter (UMD global builds for classic script loading).
	copyFile(
		path.join(repoRoot, 'node_modules', '@tracespace', 'parser', 'dist', 'tracespace-parser.umd.cjs'),
		path.join(distDir, 'tracespace-parser.umd.cjs'),
	);
	copyFile(
		path.join(repoRoot, 'node_modules', '@tracespace', 'plotter', 'dist', 'tracespace-plotter.umd.cjs'),
		path.join(distDir, 'tracespace-plotter.umd.cjs'),
	);

	// earcut triangulation (UMD global).
	copyFile(
		path.join(repoRoot, 'node_modules', 'earcut', 'dist', 'earcut.min.js'),
		path.join(distDir, 'earcut.min.js'),
	);

	// Geometry bridge (classic script, reads globals exposed by the host).
	copyFile(
		path.join(repoRoot, 'ui', 'wasm-geometry-bridge.js'),
		path.join(distDir, 'wasm-geometry-bridge.js'),
	);
}

main();
