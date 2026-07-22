import type { SerializedSolution } from './types';

/**
 * wasmClient.ts - WASM backend client
 *
 * Loads the Go-compiled analysis engine in a hidden IFrame and communicates
 * via the EasyEDA MessageBus so the heavy work stays out of the extension
 * main thread.
 */

const WASM_HOST_FRAME = 'pdn-wasm-host';
const TOPIC_READY = 'pdn-wasm-ready';
const TOPIC_ERROR = 'pdn-wasm-error';
const TOPIC_ANALYZE = 'pdn-wasm-analyze';
const TOPIC_RESULT = 'pdn-wasm-analyze-result';
const TOPIC_PROGRESS = 'pdn-wasm-progress';

export class PdnWasmClient {
	private initialized = false;
	private initPromise: Promise<void> | null = null;

	async init(): Promise<void> {
		if (this.initialized)
			return;
		if (this.initPromise)
			return this.initPromise;

		this.initPromise = this.doInit();
		return this.initPromise;
	}

	private async doInit(): Promise<void> {
		// Close any previous host frame.
		try {
			await eda.sys_IFrame.closeIFrame(WASM_HOST_FRAME);
		}
		catch {}

		return new Promise((resolve, reject) => {
			let readySub: any;
			let errorSub: any;
			let timeout: any;

			const cleanup = () => {
				clearTimeout(timeout);
				try {
					readySub.cancel();
				}
				catch {}
				try {
					errorSub.cancel();
				}
				catch {}
			};

			timeout = setTimeout(() => {
				cleanup();
				reject(new Error('WASM host initialization timed out'));
			}, 30000);

			readySub = eda.sys_MessageBus.subscribe(TOPIC_READY, () => {
				cleanup();
				this.initialized = true;
				resolve();
			});

			errorSub = eda.sys_MessageBus.subscribe(TOPIC_ERROR, (msg: any) => {
				cleanup();
				reject(new Error(msg?.error || 'WASM host initialization failed'));
			});

			eda.sys_IFrame.openIFrame('/ui/wasm-host.html', 1, 1, WASM_HOST_FRAME, {
				grayscaleMask: false,
				buttonCallbackFn: () => {},
			}).then(() => {
				// The host IFrame must exist for MessageBus/worker communication,
				// but it should never be visible as a dialog.
				try {
					eda.sys_IFrame.hideIFrame(WASM_HOST_FRAME);
				}
				catch {}
			}).catch((e) => {
				cleanup();
				reject(e);
			});
		});
	}

	async analyzeGerber(gerberBlob: Blob, configJson: string, ipc356aText?: string): Promise<SerializedSolution> {
		await this.init();

		const bytes = await gerberBlob.arrayBuffer();
		const replyTopic = `${TOPIC_RESULT}-${Date.now()}-${Math.random().toString(36).slice(2)}`;

		return new Promise((resolve, reject) => {
			let progressSub: any;
			let resultSub: any;
			let timeout: any;

			const cleanup = () => {
				clearTimeout(timeout);
				try {
					progressSub.cancel();
				}
				catch {}
				try {
					resultSub.cancel();
				}
				catch {}
			};

			timeout = setTimeout(() => {
				cleanup();
				reject(new Error('WASM analysis timed out'));
			}, 600000); // 10 minutes for large boards

			progressSub = eda.sys_MessageBus.subscribe(TOPIC_PROGRESS, () => {
				// Any progress heartbeat means the worker is still alive, so reset
				// the analysis timeout.
				clearTimeout(timeout);
				timeout = setTimeout(() => {
					cleanup();
					reject(new Error('WASM analysis timed out'));
				}, 600000);
			});

			resultSub = eda.sys_MessageBus.subscribe(replyTopic, (msg: any) => {
				cleanup();
				if (msg?.error) {
					reject(new Error(msg.error));
				}
				else {
					try {
						const parsed: SerializedSolution = JSON.parse(msg?.result ?? '{}');
						resolve(parsed);
					}
					catch (e) {
						reject(new Error(`Failed to parse WASM result: ${e}`));
					}
				}
			});

			eda.sys_MessageBus.publish(TOPIC_ANALYZE, {
				gerberBytes: bytes,
				configJson,
				ipc356aText: ipc356aText ?? '',
				replyTopic,
			});
		});
	}
}
