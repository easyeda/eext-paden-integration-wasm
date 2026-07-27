import type { AnalysisImages, AnalysisResultSet, NetworkInfo, PcbContextData, SolutionData } from './types';

/**
 * display.ts - 结果展示模块
 * 负责将后端求解结果可视化展示给用户
 */
export class ResultDisplay {
	/**
	 * 清理 Storage 中的旧数据（一次性迁移用）。
	 * 历史版本把 resultSet 写入 SysStorage；现在完全走 MessageBus，
	 * 这个方法只在运行期清理残留数据，避免老用户看到过期结果。
	 */
	static cleanupStorage(): void {
		try {
			eda.sys_Storage.setExtensionUserConfig('pdn-results', '');
			eda.sys_Storage.setExtensionUserConfig('pdn-results-images', '');
			console.warn('[Display] Storage cleaned up');
		}
		catch (e) {
			console.warn('[Display] Storage cleanup failed:', e);
		}
	}

	/** 展示求解结果，返回用户操作：'close' 或 'reanalyze' */
	show(
		result: SolutionData,
		layerNames?: Record<number, string>,
		images?: AnalysisImages,
		connectionPoints?: Record<string, Array<{ x: number; y: number; is_source: boolean }>>,
		layerBoundaries?: Record<string, Array<{ exterior: number[][]; holes: number[][][] }>>,
		warningMessage?: string,
		pcbContext?: PcbContextData,
		networkInfo?: NetworkInfo[],
	): Promise<'close' | 'reanalyze'> {
		// Wrap single result into a result set for backward compatibility
		const resultSet: AnalysisResultSet = {
			results: [{
				label: '全部',
				result,
				networkInfo: networkInfo || [],
				connectionPoints: connectionPoints || {},
				layerBoundaries: layerBoundaries || {},
				pcbContext: pcbContext || { contextTracks: [], contextPads: [] },
				warningMessage,
			}],
		};
		return this.showResultSet(resultSet, layerNames, images);
	}

	/** 展示多结果集（合并 + 单独网络分析），返回用户操作 */
	showResultSet(
		resultSet: AnalysisResultSet,
		layerNames?: Record<number, string>,
		images?: AnalysisImages,
	): Promise<'close' | 'reanalyze'> {
		return new Promise((resolve) => {
			// 先关闭已有面板
			try {
				eda.sys_IFrame.closeIFrame('pdne-results');
			}
			catch {}

			let resolved = false;
			// 订阅追踪数组，确保所有订阅都能被清理
			const subscriptions: any[] = [];

			const done = (action: 'close' | 'reanalyze') => {
				if (resolved)
					return;
				resolved = true;
				// 清理所有追踪的订阅
				for (const sub of subscriptions) {
					try {
						sub.cancel();
					}
					catch {}
				}
				subscriptions.length = 0;
				try {
					eda.sys_IFrame.closeIFrame('pdne-results');
				}
				catch {}
				// 关闭时清理 storage 中残留的旧数据（兼容老版本）
				if (action === 'close') {
					ResultDisplay.cleanupStorage();
				}
				resolve(action);
			};

			// 一次性清理可能残留的旧 storage 数据（迁移用）
			ResultDisplay.cleanupStorage();

			// 全部数据通过 MessageBus 推送，彻底绕过 SysStorage 1MB 容量限制。
			// Single publish payload 当前在 EasyEDA host 上没有明确上限文档，
			// 已能跨过多个 test-3 级别（≈1MB original）的板子。
			let payloadSize = 0;
			const sendViaBus = () => {
				try {
					const payload = {
						resultSet,
						layerNames: layerNames || {},
						images: images || null,
					};
					payloadSize = JSON.stringify(payload).length;
					eda.sys_MessageBus.publish('pdn-results-data', payload);
				}
				catch (e) {
					console.warn('[Display] MessageBus publish failed:', e);
				}
			};

			// results.html 主动请求时重发（处理订阅挂上前的窗口）
			const task = eda.sys_MessageBus.subscribe('padne-results-ready', () => {
				task.cancel();
				console.warn('[Display] Received padne-results-ready, sending data via message bus');
				sendViaBus();
				console.warn('[Display] Payload sent via message bus, size =', payloadSize, 'chars, results =', resultSet.results.length);
			});
			subscriptions.push(task);

			// 兜底：iframe 打开后立即推一次，避免 results.html 已 publish ready
			// 但我们的订阅还没挂上的竞态。
			setTimeout(() => {
				if (!resolved)
					sendViaBus();
			}, 150);

			// 监听重新分析
			const reanalyzeTask = eda.sys_MessageBus.subscribe('pdn-reanalyze', () => {
				done('reanalyze');
			});
			subscriptions.push(reanalyzeTask);

			// 监听关闭
			const closeTask = eda.sys_MessageBus.subscribe('pdn-results-close', () => {
				done('close');
			});
			subscriptions.push(closeTask);

			eda.sys_IFrame.openIFrame('/ui/results.html', 960, 900, 'pdne-results', {
				maximizeButton: true,
				minimizeButton: true,
				minimizeStyle: 'collapsed',
				grayscaleMask: false,
				title: 'PDN 分析结果',
				buttonCallbackFn: (btn) => {
					if (btn === 'close') {
						done('close');
					}
				},
			}).catch(() => {
				done('close');
			});
		});
	}
}
