const fs = require('fs');
const path = require('path');
const zlib = require('zlib');
const tar = require('tar-stream');

async function readArchive(filename) {
	const files = new Map();
	const extract = tar.extract();
	const done = new Promise((resolve, reject) => {
		extract.on('entry', (header, stream, next) => {
			const chunks = [];
			stream.on('data', chunk => chunks.push(chunk));
			stream.on('end', () => {
				files.set(path.posix.normalize(header.name), Buffer.concat(chunks));
				next();
			});
			stream.on('error', reject);
			stream.resume();
		});
		extract.on('finish', resolve);
		extract.on('error', reject);
	});
	extract.end(zlib.gunzipSync(fs.readFileSync(filename)));
	await done;
	return files;
}

function findFile(files, suffix) {
	for (const name of files.keys()) {
		if (name.toLowerCase().endsWith(suffix.toLowerCase()))
			return name;
	}
	return null;
}

function parseNetRefs(edaText, wantedNets) {
	let layers = [];
	let net = '';
	const refs = new Map();
	for (const raw of edaText.split(/\r?\n/)) {
		const line = raw.trim();
		const fields = line.split(/\s+/);
		if (fields[0] === 'LYR')
			layers = fields.slice(1);
		else if (fields[0] === 'NET')
			net = line.slice(3).trim();
		else if (fields[0] === 'FID' && fields[1] === 'C' && wantedNets.has(net)) {
			const layerIndex = Number(fields[2]);
			const featureIndex = Number(fields[3]);
			if (!refs.has(net)) refs.set(net, []);
			refs.get(net).push({ layerName: layers[layerIndex], featureIndex });
		}
	}
	return refs;
}

function featureCenters(text) {
	const lines = text.split(/\r?\n/);
	let i = lines.findIndex(line => line.trim() === '#Layer features') + 1;
	let featureIndex = 0;
	const centers = new Map();
	while (i > 0 && i < lines.length) {
		const line = lines[i].trim();
		if (!line || line.startsWith('#')) {
			i++;
			continue;
		}
		const fields = line.split(/\s+/);
		let point = null;
		if (fields[0] === 'S') {
			while (i < lines.length && lines[i].trim() !== 'SE') {
				const outline = lines[i].trim().split(/\s+/);
				if (!point && outline[0] === 'OB') point = [Number(outline[1]), Number(outline[2])];
				i++;
			}
		}
		else if (['L', 'A'].includes(fields[0]))
			point = [(Number(fields[1]) + Number(fields[3])) / 2, (Number(fields[2]) + Number(fields[4])) / 2];
		else if (fields[0] === 'P')
			point = [Number(fields[1]), Number(fields[2])];
		else {
			i++;
			continue;
		}
		if (point && point.every(Number.isFinite)) centers.set(featureIndex, point);
		featureIndex++;
		i++;
	}
	return centers;
}

function normalize(value) {
	return value.toLowerCase().replace(/[^a-z0-9]/g, '');
}

async function fixturePoints(filename, wantedNets, configuredLayers) {
	const files = await readArchive(filename);
	const edaName = findFile(files, '/eda/data');
	if (!edaName) throw new Error('ODB++ eda/data not found');
	const refs = parseNetRefs(files.get(edaName).toString('utf8'), new Set(wantedNets));
	const configuredByODB = new Map(configuredLayers.map(name => [normalize(name), name]));
	const centerCache = new Map();
	const result = new Map();
	for (const net of wantedNets) {
		const points = [];
		for (const ref of refs.get(net) || []) {
			const configured = configuredByODB.get(normalize(ref.layerName));
			if (!configured) continue;
			const suffix = `/layers/${ref.layerName}/features`;
			const featureName = findFile(files, suffix);
			if (!featureName) continue;
			if (!centerCache.has(featureName))
				centerCache.set(featureName, featureCenters(files.get(featureName).toString('utf8')));
			const point = centerCache.get(featureName).get(ref.featureIndex);
			if (!point) continue;
			points.push({ x: point[0], y: point[1], layer: configured, net, is_tht: false, hole_diameter: 0 });
		}
		result.set(net, points);
	}
	return { bytes: fs.readFileSync(filename), points: result };
}

module.exports = { fixturePoints };
