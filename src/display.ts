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
			let storageWritten = false;
			try {
				eda.sys_Storage.setExtensionUserConfig('pdn-results', jsonStr);
				storageWritten = true;
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

			// MessageBus 双保险: 即使 storage 写入失败，results 弹窗也能通过
			// message bus 拿到数据。同时把 resultsSet 拆成轻量 snapshot 走
			// message bus，避免某些 host 上大 payload 被截断。
			const sendViaBus = () => {
				try {
					eda.sys_MessageBus.publish('pdn-results-data', {
						resultSet,
						layerNames: layerNames || {},
						images: images || null,
						storageOk: storageWritten,
					});
				}
				catch (e) {
					console.warn('[Display] MessageBus publish failed:', e);
				}
			};

			// Storage 失败或 results.html 主动请求时，都通过 bus 再发一次。
			const task = eda.sys_MessageBus.subscribe('padne-results-ready', () => {
				task.cancel();
				console.warn(`[Display] Received padne-results-ready, sending data via message bus (storageOk=${storageWritten})`);
				sendViaBus();
			});
			subscriptions.push(task);

			if (!storageWritten) {
				// Storage 不可用时，结果完全靠 message bus。把超时 fallback 也
				// 加上，防止 results.html 已经 publish 了 padne-results-ready
				// 但我们的订阅还没挂上导致数据丢失。
				setTimeout(() => sendViaBus(), 800);
			}

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
