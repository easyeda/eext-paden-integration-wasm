const fs = require('fs');
const path = require('path');
const { pathToFileURL } = require('url');

const repoRoot = path.resolve(__dirname, '..');
const distDir = path.join(repoRoot, 'dist');

globalThis.window = globalThis;
globalThis.earcut = require('earcut');
const Clipper2Z = require('clipper2-wasm').default;
globalThis.Clipper2ZFactory = () => globalThis.Clipper2Z({ locateFile: () => 'file://' + path.join(distDir, 'clipper2z.wasm').replace(/\\/g, '/') });
globalThis.Clipper2Z = Clipper2Z;

(async () => {
	await import(pathToFileURL(path.join(distDir, 'wasm-geometry-bridge.js')).href);
	await globalThis.padenGeometry.init();

	const zipPath = path.join(__dirname, 'test-paden.zip');
	const JSZip = require('jszip');
	const zip = await JSZip.loadAsync(fs.readFileSync(zipPath));
	const gtl = await zip.file('Gerber_TopLayer.GTL').async('string');
	const polys = globalThis.padenGeometry.gerberToPolygons(gtl);
	console.log('raw polygons', polys.length);
	polys.forEach((poly, i) => {
		const rings = poly.length;
		let area = 0;
		const xs = [];
		const ys = [];
		for (const ring of poly) {
			for (const p of ring) { xs.push(p.x); ys.push(p.y); }
		}
		console.log(`poly${i} rings=${rings} x=[${Math.min(...xs).toFixed(3)},${Math.max(...xs).toFixed(3)}] y=[${Math.min(...ys).toFixed(3)},${Math.max(...ys).toFixed(3)}]`);
	});

	polys.slice(0, 8).forEach((poly, i) => {
		console.log(`--- raw poly${i} rings=${poly.length}`);
		poly.forEach((ring, ri) => {
			console.log(` ring${ri} len=${ring.length} first=${JSON.stringify(ring[0])} last=${JSON.stringify(ring[ring.length - 1])}`);
		});
	});
	[5, 6, 7].forEach((i) => {
		const poly = polys[i];
		if (!poly) return;
		console.log(`--- raw poly${i} ALL POINTS rings=${poly.length}`);
		poly.forEach((ring, ri) => {
			const pts = ring.map(p => `(${p.x.toFixed(3)},${p.y.toFixed(3)})`).join(' ');
			console.log(` ring${ri} len=${ring.length}: ${pts}`);
		});
	});

	const { parse } = require('@tracespace/parser');
	const { plot } = require('@tracespace/plotter');
	const image2 = plot(parse(gtl.replace(/\xA0/g, ' ')));
	console.log('--- path offsets ---');
	image2.children.forEach((child, i) => {
		if (child.type === 'imagePath' && child.segments) {
			// replicate bridge pathSegmentsToPoints
			const pts = [{ x: child.segments[0].start[0], y: child.segments[0].start[1] }];
			for (const seg of child.segments) {
				if (seg.type === 'line') pts.push({ x: seg.end[0], y: seg.end[1] });
			}
			console.log(`path${i} width=${child.width} segs=${child.segments.length}`);
			child.segments.forEach((seg, si) => {
				console.log(`  seg${si} ${seg.type} start=[${seg.start[0].toFixed(3)},${seg.start[1].toFixed(3)}] end=[${seg.end[0].toFixed(3)},${seg.end[1].toFixed(3)}]`);
			});
			const offset = globalThis.padenGeometry.clipperOffset([[pts]], child.width / 2);
			console.log(`  offsetPolys=${offset.length}`);
			offset.forEach((poly, pi) => {
				const xs = poly[0].map(p => p.x); const ys = poly[0].map(p => p.y);
				console.log(`  offset${pi} rings=${poly.length} x=[${Math.min(...xs).toFixed(3)},${Math.max(...xs).toFixed(3)}] y=[${Math.min(...ys).toFixed(3)},${Math.max(...ys).toFixed(3)}]`);
			});
		}
	});

	const unioned = globalThis.padenGeometry.clipperUnion(polys);
	console.log('unioned polygons', unioned.length);
	unioned.forEach((poly, i) => {
		const xs = []; const ys = [];
		for (const ring of poly) for (const p of ring) { xs.push(p.x); ys.push(p.y); }
		console.log(`union${i} rings=${poly.length} x=[${Math.min(...xs).toFixed(3)},${Math.max(...xs).toFixed(3)}] y=[${Math.min(...ys).toFixed(3)},${Math.max(...ys).toFixed(3)}]`);
	});
	[2, 6, 7].forEach((i) => {
		const poly = unioned[i];
		if (!poly) return;
		console.log(`--- union${i} ALL POINTS rings=${poly.length}`);
		poly.forEach((ring, ri) => {
			const pts = ring.map(p => `(${p.x.toFixed(3)},${p.y.toFixed(3)})`).join(' ');
			console.log(` ring${ri} len=${ring.length}: ${pts}`);
		});
	});

	process.exit(0);
})().catch(e => { console.error(e); process.exit(1); });
