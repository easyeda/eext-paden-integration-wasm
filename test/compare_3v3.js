const fs = require('fs');

// EasyEDA polygon format: [x1,y1,"L",x2,y2,...] or with "ARC",angle,x,y
function parseEasyPolygon(arr) {
  const pts = [];
  let i = 0;
  while (i < arr.length) {
    if (arr[i] === 'L') { i++; continue; }
    if (arr[i] === 'ARC') {
      const angle = arr[i+1];
      const x = arr[i+2];
      const y = arr[i+3];
      pts.push({x, y, arc: true, angle});
      i += 4;
    } else {
      const x = arr[i];
      const y = arr[i+1];
      pts.push({x, y});
      i += 2;
    }
  }
  return pts;
}

function edaToMm(v) { return v * 0.0254; }

const raw = JSON.parse(fs.readFileSync('test/eda_3v3_regions_raw.json', 'utf8'));
const result = JSON.parse(fs.readFileSync('test/wasm_test3_result.json', 'utf8'));

const edaRegions = raw.result.filter(p => p.polygon && p.polygon.polygon).map(p => {
  const poly = p.polygon.polygon;
  const pts = parseEasyPolygon(poly);
  return {
    id: p.id,
    layer: p.layer,
    pts: pts.map(pt => ({ x: edaToMm(pt.x), y: edaToMm(pt.y) }))
  };
});

function bbox(pts) {
  let minx = Infinity, miny = Infinity, maxx = -Infinity, maxy = -Infinity;
  for (const p of pts) { minx = Math.min(minx, p.x); miny = Math.min(miny, p.y); maxx = Math.max(maxx, p.x); maxy = Math.max(maxy, p.y); }
  return { minx, miny, maxx, maxy };
}

console.log('=== Live EDA 3V3 regions ===');
console.log('count:', edaRegions.length);
let edaBbox = { minx: Infinity, miny: Infinity, maxx: -Infinity, maxy: -Infinity };
for (const r of edaRegions) {
  const b = bbox(r.pts);
  r.bbox = b;
  edaBbox.minx = Math.min(edaBbox.minx, b.minx); edaBbox.miny = Math.min(edaBbox.miny, b.miny);
  edaBbox.maxx = Math.max(edaBbox.maxx, b.maxx); edaBbox.maxy = Math.max(edaBbox.maxy, b.maxy);
}
console.log('overall bbox mm:', edaBbox);
console.log('sample ids:', edaRegions.slice(0,5).map(r=>r.id));

const top = result.layer_boundaries.TopLayer || [];
const backend3v3 = top.filter(t => t.net === '3V3').map(t => {
  const exterior = t.exterior.map(p => ({x: p[0], y: p[1]}));
  return { exterior, bbox: bbox(exterior) };
});
console.log('=== Backend TopLayer 3V3 boundaries ===');
console.log('count:', backend3v3.length);
let backBbox = { minx: Infinity, miny: Infinity, maxx: -Infinity, maxy: -Infinity };
for (const r of backend3v3) {
  backBbox.minx = Math.min(backBbox.minx, r.bbox.minx); backBbox.miny = Math.min(backBbox.miny, r.bbox.miny);
  backBbox.maxx = Math.max(backBbox.maxx, r.bbox.maxx); backBbox.maxy = Math.max(backBbox.maxy, r.bbox.maxy);
}
console.log('overall bbox mm:', backBbox);

console.log('=== Meshes (TopLayer) ===');
const sol = result.layer_solutions[0];
for (const m of sol.meshes) {
  const vs = m.vertices.map(v => ({x: v[0], y: v[1]}));
  console.log('mesh bbox:', bbox(vs));
}
console.log('disconnected meshes:', (sol.disconnected_meshes || []).length);
for (const m of (sol.disconnected_meshes || []).slice(0,3)) {
  const vs = m.vertices.map(v => ({x: v[0], y: v[1]}));
  console.log('disc bbox:', bbox(vs));
}

// Build SVG overlay
const pad = 1.0;
const viewMinX = Math.min(edaBbox.minx, backBbox.minx) - pad;
const viewMinY = Math.min(edaBbox.miny, backBbox.miny) - pad;
const viewMaxX = Math.max(edaBbox.maxx, backBbox.maxx) + pad;
const viewMaxY = Math.max(edaBbox.maxy, backBbox.maxy) + pad;
const w = viewMaxX - viewMinX;
const h = viewMaxY - viewMinY;
const scale = 20; // px per mm
const svgW = w * scale;
const svgH = h * scale;
function tx(x) { return (x - viewMinX) * scale; }
function ty(y) { return svgH - (y - viewMinY) * scale; }
function polyPath(pts) {
  return pts.map((p, i) => (i === 0 ? 'M' : 'L') + tx(p.x).toFixed(2) + ' ' + ty(p.y).toFixed(2)).join(' ') + ' Z';
}

let svg = `<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" version="1.1" viewBox="0 0 ${svgW} ${svgH}" width="${svgW}" height="${svgH}" style="background:#222">
`;
// Backend 3V3 boundaries in blue
for (const r of backend3v3) {
  svg += `  <path d="${polyPath(r.exterior)}" fill="none" stroke="cyan" stroke-width="1.5"/>\n`;
}
// Live EDA 3V3 regions in red
for (const r of edaRegions) {
  svg += `  <path d="${polyPath(r.pts)}" fill="none" stroke="red" stroke-width="0.8"/>\n`;
}
// Backend voltage meshes in green
for (const m of sol.meshes) {
  const vs = m.vertices;
  for (const tri of m.triangles) {
    const idx = tri.vertices;
    const a = vs[idx[0]], b = vs[idx[1]], c = vs[idx[2]];
    const pts = [{x: a[0], y: a[1]}, {x: b[0], y: b[1]}, {x: c[0], y: c[1]}];
    svg += `  <path d="${polyPath(pts)}" fill="none" stroke="lime" stroke-width="0.4"/>\n`;
  }
}
svg += '</svg>\n';
fs.writeFileSync('test/compare_3v3_overlay.svg', svg);
console.log('Wrote test/compare_3v3_overlay.svg');
