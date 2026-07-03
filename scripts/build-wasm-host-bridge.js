/**
 * Bundle the WASM host geometry bridge into dist/.
 *
 * The heavy dependencies (tracespace, clipper2-wasm, earcut) are kept external
 * and copied into dist/ by copy-wasm-assets.js so the browser can load them
 * directly alongside the bundled bridge.
 */

const process = require('node:process');
const esbuild = require('esbuild');

async function main() {
	const ctx = await esbuild.context({
		entryPoints: {
			'wasm-geometry-bridge': './ui/wasm-geometry-bridge.js',
		},
		entryNames: '[name]',
		assetNames: '[name]',
		bundle: true,
		minify: false,
		outdir: './dist/',
		platform: 'browser',
		format: 'esm',
		treeShaking: true,
		ignoreAnnotations: true,
		external: [
			'/dist/tracespace-parser.js',
			'/dist/tracespace-plotter.js',
			'/dist/clipper2z.js',
			'/dist/earcut.js',
		],
	});
	await ctx.rebuild();
	await ctx.dispose();
	console.log('[build-wasm-host-bridge] done');
}

main().catch((e) => {
	console.error(e);
	process.exit(1);
});
