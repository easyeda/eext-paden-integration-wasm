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
const TOPIC_WASM_LOG = 'pdn-wasm-log';
const TOPIC_ANALYZING_LOG = 'pdn-analyzing-log';
const TOPIC_ANALYZING_LOG_REQ = 'pdn-analyzing-log-req';

const RECENT_LOG_LIMIT = 200;

export class PdnWasmClient {
	private initialized = false;
	private initPromise: Promise<void> | null = null;
	private recentLogs: string[] = [];
	private logSubscribed = false;

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

		this.subscribeLogBridge();

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

	async analyzeODB(odbBlob: Blob, configJson: string): Promise<SerializedSolution> {
		await this.init();

		const bytes = await odbBlob.arrayBuffer();
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
			}, 1800000); // 30 minutes for large boards

			progressSub = eda.sys_MessageBus.subscribe(TOPIC_PROGRESS, () => {
				// Any progress heartbeat means the worker is still alive, so reset
				// the analysis timeout.
				clearTimeout(timeout);
				timeout = setTimeout(() => {
					cleanup();
					reject(new Error('WASM analysis timed out'));
				}, 1800000);
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
				odbBytes: bytes,
				configJson,
				replyTopic,
			});
		});
	}

	private subscribeLogBridge() {
		if (this.logSubscribed)
			return;
		this.logSubscribed = true;

		// Capture every worker log line into a ring buffer so the analyzing
		// dialog can fetch history when it opens, and forward each new line
		// to live subscribers.
		try {
			eda.sys_MessageBus.subscribe(TOPIC_WASM_LOG, (msg: any) => {
				const line = this.formatLogLine(msg);
				if (!line)
					return;
				this.recentLogs.push(line);
				if (this.recentLogs.length > RECENT_LOG_LIMIT)
					this.recentLogs.splice(0, this.recentLogs.length - RECENT_LOG_LIMIT);
				try {
					eda.sys_MessageBus.publish(TOPIC_ANALYZING_LOG, { line });
				}
				catch {}
			});
		}
		catch (e) {
			console.warn('paden: failed to subscribe to wasm logs:', e);
		}

		// Snapshot endpoint so the analyzing dialog can backfill any logs that
		// were emitted before its iframe finished loading.
		try {
			eda.sys_MessageBus.subscribe(TOPIC_ANALYZING_LOG_REQ, (msg: any) => {
				const reply = msg?.replyTopic;
				if (!reply)
					return;
				try {
					eda.sys_MessageBus.publish(reply, {
						logs: this.recentLogs.slice(),
					});
				}
				catch {}
			});
		}
		catch (e) {
			console.warn('paden: failed to subscribe to log history requests:', e);
		}
	}

	private formatLogLine(msg: any): string {
		if (!msg)
			return '';
		if (typeof msg === 'string')
			return msg;
		const message = typeof msg.message === 'string' ? msg.message : '';
		if (!message)
			return '';
		const args = Array.isArray(msg.args) ? msg.args : [];
		if (args.length > 0) {
			const tail = args
				.map((a: any) => (typeof a === 'string' ? a : JSON.stringify(a)))
				.join(' ');
			return tail ? `${message} ${tail}` : message;
		}
		return message;
	}
}
