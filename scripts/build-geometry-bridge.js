/**
 * Bundle the geometry/Gerber bridge into dist/ as an IIFE.
 *
 * tracespace and earcut are bundled so the host does not need to resolve
 * ES imports or depend on UMD environment detection inside EasyEDA's
 * sandboxed iframe. Clipper2-WASM is kept external and loaded beforehand.
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
	});
	console.log('[build-geometry-bridge] done');
}

main().catch((e) => {
	console.error(e);
	process.exit(1);
});
