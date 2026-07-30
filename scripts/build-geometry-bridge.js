/**
 * Bundle the geometry bridge into dist/ as an IIFE.
 *
 * earcut and triangle-wasm are bundled so the host does not need to resolve
 * ES imports or depend on UMD environment detection inside EasyEDA's
 * sandboxed iframe. Clipper2-WASM is kept external and loaded beforehand.
 *
 * The Emscripten-generated triangle.out.js references Node builtins
 * (`require('path')`, `require('fs')`) inside an `if (ENVIRONMENT_IS_NODE)`
 * branch that is never executed in the browser; we mark those as external
 * so esbuild doesn't refuse to bundle them.
 */

const process = require('node:process');
const esbuild = require('esbuild');

async function main() {
	await esbuild.build({
		entryPoints: ['./ui/wasm-geometry-entry.js'],
		outfile: './dist/wasm-geometry-bridge.js',
		bundle: true,
		minify: false,
		format: 'iife',
		platform: 'browser',
		treeShaking: true,
		external: ['path', 'fs'],
	});
	console.log('[build-geometry-bridge] done');
}

main().catch((e) => {
	console.error(e);
	process.exit(1);
});
