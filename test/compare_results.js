const fs = require('fs');
const path = require('path');

const py = JSON.parse(fs.readFileSync(path.join(__dirname, 'python_result.json'), 'utf8'));
const wasm = JSON.parse(fs.readFileSync(path.join(__dirname, 'wasm_result.json'), 'utf8'));

console.log('=== Top-level ===');
console.log('Python success:', py.success, 'message:', py.message);
console.log('WASM success:', wasm.success, 'message:', wasm.message);
console.log('Python solver_info:', JSON.stringify(py.solver_info));
console.log('WASM solver_info:', JSON.stringify(wasm.solver_info));

function range(a) {
	if (!Array.isArray(a) || a.length === 0) return 'n/a';
	const n = a.map(Number);
	return `[${Math.min(...n).toExponential(3)}, ${Math.max(...n).toExponential(3)}]`;
}

function mean(a) {
	if (!Array.isArray(a) || a.length === 0) return 0;
	return a.reduce((s, v) => s + Number(v), 0) / a.length;
}

function summarize(label, result) {
	console.log(`\n=== ${label} meshes ===`);
	const sols = result.layer_solutions || [];
	sols.forEach((sol, si) => {
		(sol.meshes || []).forEach((m, mi) => {
			console.log(`${label}[${si}][${mi}] verts=${m.vertices?.length} tris=${m.triangles?.length} pot=${range(m.potentials)} pd=${range(m.power_densities)}`);
		});
	});
}

function meshMeanPotential(m) {
	return mean(m.potentials);
}

function meshKey(m) {
	// Sort by mean potential descending so VCC-ish meshes pair together.
	return -meshMeanPotential(m);
}

function compareArrays(a, b, name) {
	if (!Array.isArray(a) || !Array.isArray(b)) {
		console.log(`${name}: not arrays`);
		return;
	}
	if (a.length !== b.length) {
		console.log(`${name}: length mismatch ${a.length} vs ${b.length}`);
		return;
	}
	let maxD = 0, maxRel = 0, idx = -1;
	for (let i = 0; i < a.length; i++) {
		const av = Number(a[i]), bv = Number(b[i]);
		if (Number.isNaN(av) || Number.isNaN(bv)) continue;
		const d = Math.abs(av - bv);
		const rel = Math.abs(av) > 1e-12 ? d / Math.abs(av) : d;
		if (d > maxD) { maxD = d; maxRel = rel; idx = i; }
	}
	console.log(`${name}: len=${a.length} maxAbsDiff=${maxD.toExponential(3)} maxRelDiff=${maxRel.toExponential(3)} @${idx} (py=${a[idx]}, wasm=${b[idx]})`);
}

summarize('Python', py);
summarize('WASM', wasm);

const pyMeshes = [...(py.layer_solutions?.[0]?.meshes || [])].sort((a, b) => meshKey(a) - meshKey(b));
const wasmMeshes = [...(wasm.layer_solutions?.[0]?.meshes || [])].sort((a, b) => meshKey(a) - meshKey(b));
console.log(`\n=== Compare first layer: Python ${pyMeshes.length} meshes vs WASM ${wasmMeshes.length} meshes ===`);

for (let i = 0; i < Math.max(pyMeshes.length, wasmMeshes.length); i++) {
	const pm = pyMeshes[i];
	const wm = wasmMeshes[i];
	console.log(`\n--- Pair ${i} ---`);
	if (!pm || !wm) {
		console.log('Python:', pm ? `verts=${pm.vertices?.length}` : 'missing');
		console.log('WASM:  ', wm ? `verts=${wm.vertices?.length}` : 'missing');
		continue;
	}
	console.log(`Python verts=${pm.vertices?.length} tris=${pm.triangles?.length} pot=${range(pm.potentials)} meanPot=${mean(pm.potentials).toExponential(3)} meanPD=${mean(pm.power_densities).toExponential(3)}`);
	console.log(`WASM   verts=${wm.vertices?.length} tris=${wm.triangles?.length} pot=${range(wm.potentials)} meanPot=${mean(wm.potentials).toExponential(3)} meanPD=${mean(wm.power_densities).toExponential(3)}`);
	if (pm.vertices?.length === wm.vertices?.length) {
		compareArrays(pm.potentials, wm.potentials, 'potentials');
		compareArrays(pm.power_densities, wm.power_densities, 'power_densities');
		const pyCx = pm.current_densities?.map(v => v[0]);
		const pyCy = pm.current_densities?.map(v => v[1]);
		const wasmCx = wm.current_densities?.map(v => v[0]);
		const wasmCy = wm.current_densities?.map(v => v[1]);
		compareArrays(pyCx, wasmCx, 'current_density_x');
		compareArrays(pyCy, wasmCy, 'current_density_y');
	}
	else {
		console.log('Vertex counts differ; skipping per-vertex comparison.');
	}
}

console.log('\n=== Diagnostics counts ===');
console.log('Python:', (py.diagnostics || []).length);
console.log('WASM:', (wasm.diagnostics || []).length);
