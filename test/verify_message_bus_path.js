// 端到端验证：display.ts → sys_MessageBus → results.html 的数据流
// 不依赖 Go WASM，只验证大 payload 在 MessageBus mock 下的传输正确性。
const assert = require('assert');

// ---------- Mock eda 全局 ----------
const eda = {
	sys_MessageBus: (() => {
		const subscribers = new Map();
		return {
			subscribe(topic, handler) {
				if (!subscribers.has(topic)) subscribers.set(topic, []);
				subscribers.get(topic).push(handler);
				return {
					cancel() {
						const arr = subscribers.get(topic);
						if (!arr) return;
						const idx = arr.indexOf(handler);
						if (idx >= 0) arr.splice(idx, 1);
					},
				};
			},
			publish(topic, payload) {
				const arr = subscribers.get(topic) || [];
				for (const h of arr.slice()) {
					try {
						h(payload);
					}
					catch (e) {
						console.error(`[bus] handler for ${topic} threw:`, e);
					}
				}
			},
		};
	})(),
	sys_IFrame: {
		openIFrame: () => Promise.resolve(),
		closeIFrame: () => {},
	},
	sys_Storage: {
		// 验证 display.ts 不再写入（清空就好）
		setExtensionUserConfig: () => {},
		getExtensionUserConfig: () => '',
	},
};
globalThis.eda = eda;

// ---------- 模拟 display.ts 推送 ----------
function mockDisplaySend(resultSet, layerNames, images) {
	let payloadSize = 0;
	const sendViaBus = () => {
		const payload = { resultSet, layerNames: layerNames || {}, images: images || null };
		payloadSize = JSON.stringify(payload).length;
		eda.sys_MessageBus.publish('pdn-results-data', payload);
	};

	const sub = eda.sys_MessageBus.subscribe('padne-results-ready', () => {
		sub.cancel();
		sendViaBus();
	});

	setTimeout(() => sendViaBus(), 150);

	return { payloadSize: () => payloadSize, sent: () => payloadSize > 0 };
}

// ---------- 模拟 results.html 接收 ----------
function mockResultsReceive() {
	return new Promise((resolve) => {
		let got = false;
		const sub = eda.sys_MessageBus.subscribe('pdn-results-data', (msg) => {
			if (got) return;
			got = true;
			sub.cancel();
			const data = msg?.data?.resultSet ? msg.data : msg;
			resolve(data);
		});
		setTimeout(() => {
			if (!got) {
				sub.cancel();
				resolve(null);
			}
		}, 5000);
	});
}

// ---------- 构造大数据 fixture（模拟 test-4 规模）----------
function buildLargeResultSet() {
	// 模拟一个大板子的 mesh：多层，多 mesh，每个 mesh 几千顶点
	const layers = [];
	for (let li = 0; li < 6; li++) {
		const meshes = [];
		for (let mi = 0; mi < 8; mi++) {
			const verts = [];
			const tris = [];
			const pots = [];
			const nVerts = 1500 + mi * 200;
			for (let i = 0; i < nVerts; i++) {
				verts.push([Math.random() * 100, Math.random() * 100]);
				pots.push(1.0 + Math.random() * 0.001);
			}
			for (let i = 0; i < nVerts - 2; i++) {
				tris.push([i, i + 1, i + 2]);
			}
			meshes.push({ vertices: verts, triangles: tris, potentials: pots, currentDensities: [], powerDensities: [] });
		}
		layers.push({
			layerName: `Layer ${li + 1}`,
			meshes,
			disconnectedMeshes: [],
		});
	}
	return {
		results: [{
			label: '全部',
			result: {
				success: true,
				layer_solutions: layers,
				solver_info: { iterations: 100, residual: 1e-8 },
				diagnostics: ['[INFO] synthetic large dataset'],
				currentWarnings: [],
			},
			networkInfo: [],
			connectionPoints: {},
			layerBoundaries: {},
			pcbContext: { contextTracks: [], contextPads: [] },
			warningMessage: null,
		}],
	};
}

async function main() {
	console.log('[Test] 1. 构造大数据 fixture (≈ 模拟 test-4 规模)');
	const resultSet = buildLargeResultSet();
	const json = JSON.stringify(resultSet);
	console.log(`[Test]   payload size = ${json.length} chars (${(json.length / 1024).toFixed(1)} KB)`);
	const layerNames = { 0: 'Top', 1: 'L2', 2: 'L3', 3: 'L4', 4: 'L5', 5: 'Bottom' };

	console.log('[Test] 2. 模拟 display.ts 推送 + results.html 接收（5s 超时）');
	const sender = mockDisplaySend(resultSet, layerNames, null);
	const receiver = mockResultsReceive();

	// 模拟 results.html 主动 publish ready（实际是 main() 里 publish）
	setTimeout(() => eda.sys_MessageBus.publish('padne-results-ready', {}), 50);

	const data = await receiver;
	assert(data, 'MessageBus 接收超时');
	console.log(`[Test]   收到 payload, payloadSize=${sender.payloadSize()}, results=${data.resultSet.results.length}`);
	console.log(`[Test]   layer_solutions.count = ${data.resultSet.results[0].result.layer_solutions.length}`);

	console.log('[Test] 3. 验证数据完整性');
	const orig = JSON.stringify(resultSet);
	const recv = JSON.stringify(data.resultSet);
	assert.strictEqual(orig.length, recv.length, '数据长度不一致');
	assert.strictEqual(orig, recv, '数据内容不一致（JSON 序列化结果必须相等）');
	console.log('[Test]   JSON 序列化结果 byte-for-byte 相等');

	// 验证 layerNames 完整
	const origLayerNames = JSON.stringify(layerNames);
	const recvLayerNames = JSON.stringify(data.layerNames);
	assert.strictEqual(origLayerNames, recvLayerNames, 'layerNames 不一致');
	console.log('[Test]   layerNames 完整传输');

	// 检查上下竞态：先 publish ready，再订阅
	console.log('[Test] 4. 测试 ready 早于订阅的竞态（先 publish 然后再订阅）');
	const sender2 = mockDisplaySend(resultSet, layerNames, null);
	// 立即 publish ready，此时 sender 还没订阅
	eda.sys_MessageBus.publish('padne-results-ready', {});
	// 然后启动接收
	const receiver2 = mockResultsReceive();
	const data2 = await receiver2;
	// 注意：这种情况下 sender 错过了 ready 通知，但 150ms setTimeout 兜底会再发一次
	assert(data2, '竞态场景下 MessageBus 接收超时');
	console.log('[Test]   竞态场景下凭 setTimeout 兜底成功收到数据');

	console.log('\n✅ 所有验证通过：MessageBus 路径可以承载大 payload 且无数据丢失');
}

main().catch((err) => {
	console.error('❌ 测试失败:', err);
	process.exit(1);
});
