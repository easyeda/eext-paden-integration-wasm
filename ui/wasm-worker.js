/**
 * Web Worker that runs the Go WASM solver off the main UI thread.
 *
 * This file is loaded by ui/wasm-host.html as a Blob URL with the following
 * placeholders injected at runtime:
 *   __CLIPPER_JS_URL__, __CLIPPER_WASM_URL__,
 *   __BRIDGE_JS_URL__, __WASM_EXEC_URL__, __PADEN_WASM_URL__
 */

/* global importScripts Go */

// In a worker the global object is `globalThis`; bridge scripts reference `window`.
globalThis.window = globalThis;

// Injected by the host at worker creation time.
const CLIPPER_JS_URL = '__CLIPPER_JS_URL__';
const CLIPPER_WASM_URL = '__CLIPPER_WASM_URL__';
const BRIDGE_JS_URL = '__BRIDGE_JS_URL__';
const WASM_EXEC_URL = '__WASM_EXEC_URL__';
const PADEN_WASM_URL = '__PADEN_WASM_URL__';

// Load classic scripts into the worker global scope.
importScripts(CLIPPER_JS_URL);

// Configure Clipper2 factory before the geometry bridge loads.
globalThis.Clipper2ZFactory = () => globalThis.Clipper2Z({ locateFile: () => CLIPPER_WASM_URL });

importScripts(BRIDGE_JS_URL);
importScripts(WASM_EXEC_URL);

let goRuntime = null;
let wasmInstance = null;
let initError = null;

async function initWASM() {
	try {
		await globalThis.padenGeometry.init();

		const response = await fetch(PADEN_WASM_URL);
		if (!response.ok) {
			throw new Error(`failed to fetch paden.wasm: ${response.status}`);
		}
		const wasmBuffer = await response.arrayBuffer();

		goRuntime = new Go();
		const result = await WebAssembly.instantiate(wasmBuffer, goRuntime.importObject);
		wasmInstance = result.instance;
		goRuntime.run(wasmInstance);

		globalThis.postMessage({ type: 'ready' });
	}
	catch (e) {
		initError = e;
		globalThis.postMessage({ type: 'error', error: String(e) });
	}
}

function gerberToUint8Array(rawBytes) {
	if (rawBytes instanceof Uint8Array || rawBytes instanceof Uint8ClampedArray) {
		return rawBytes;
	}
	try {
		const view = new Uint8Array(rawBytes);
		if (view.length > 0 || rawBytes.byteLength === 0) {
			return view;
		}
	}
	catch {
		// fall through
	}
	if (rawBytes && rawBytes.buffer instanceof ArrayBuffer) {
		return new Uint8Array(rawBytes.buffer, rawBytes.byteOffset || 0, rawBytes.byteLength);
	}
	if (rawBytes && typeof rawBytes === 'object') {
		const keys = Object.keys(rawBytes)
			.map(Number)
			.filter(k => Number.isFinite(k))
			.sort((a, b) => a - b);
		if (keys.length > 0) {
			return new Uint8Array(keys.map(k => rawBytes[k]));
		}
	}
	throw new Error(`Unsupported gerberBytes type: ${typeof rawBytes}`);
}

async function handleAnalyze(msg) {
	const { gerberBytes: rawBytes, configJson, replyTopic } = msg;
	try {
		if (initError)
			throw initError;

		if (!globalThis.padne || !globalThis.padne.analyzeGerber) {
			throw new Error('Go WASM not initialized');
		}

		const gerberBytes = gerberToUint8Array(rawBytes);
		const result = await globalThis.padne.analyzeGerber(gerberBytes, configJson);
		globalThis.postMessage({ type: 'analyze-result', replyTopic, result });
	}
	catch (e) {
		globalThis.postMessage({ type: 'analyze-result', replyTopic, error: String(e) });
	}
}

globalThis.onmessage = (event) => {
	const msg = event.data;
	if (!msg || !msg.type)
		return;

	switch (msg.type) {
		case 'init':
			initWASM();
			break;
		case 'analyze':
			handleAnalyze(msg);
			break;
	}
};

// Auto-init when loaded (host may also send an explicit 'init' message).
initWASM();
