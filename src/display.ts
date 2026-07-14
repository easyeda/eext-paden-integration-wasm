import type { AnalysisImages, AnalysisResultSet, NetworkInfo, PcbContextData, SolutionData } from './types';

/**
 * display.ts - 结果展示模块
 * 负责将后端求解结果可视化展示给用户
 */
export class ResultDisplay {
	/** 清理 Storage 中的旧数据 */
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
				// 如果关闭（不是重新分析），清理 Storage 中的大对象
				if (action === 'close') {
					try {
						eda.sys_Storage.setExtensionUserConfig('pdn-results', '');
						eda.sys_Storage.setExtensionUserConfig('pdn-results-images', '');
					}
					catch (e) {
						console.warn('[Display] Storage cleanup failed:', e);
					}
				}
				resolve(action);
			};

			// Storage 传递数据
			const jsonStr = JSON.stringify({
				resultSet,
				layerNames: layerNames || {},
			});
			console.warn('[Display] Storage write: data size =', jsonStr.length, 'chars, results =', resultSet.results.length);
			try {
				eda.sys_Storage.setExtensionUserConfig('pdn-results', jsonStr);
			}
			catch (e) {
				console.warn('[Display] Storage write failed (data too large?):', e);
			}
			if (images) {
				try {
					eda.sys_Storage.setExtensionUserConfig('pdn-results-images', JSON.stringify(images));
				}
				catch (e) {
					console.warn('[Display] Images Storage write failed:', e);
				}
			}

			// MessageBus 双保险
			const task = eda.sys_MessageBus.subscribe('padne-results-ready', () => {
				task.cancel();
				console.warn('[Display] Received padne-results-ready, sending data via message bus');
				eda.sys_MessageBus.publish('pdn-results-data', {
					resultSet,
					layerNames: layerNames || {},
					images: images || null,
				});
			});
			subscriptions.push(task);

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
