import type { AnalysisResultEntry, AnalysisResultSet, EasyEDA_Pad, EasyEDA_PcbData, NetworkInfo, PdnConfig } from './types';
import * as extensionConfig from '../extension.json';
import { PcbDataConverter } from './convert';
import { ResultDisplay } from './display';
import { PcbExtractor } from './extract';
import { PdnWasmClient } from './wasmClient';

// ============================================================
// 导出函数
// ============================================================

export async function runPdnAnalysis(): Promise<void> {
	try {
		// 在开始新分析前清理 Storage 中的旧数据
		ResultDisplay.cleanupStorage();

		const wasmClient = new PdnWasmClient();
		eda.sys_LoadingAndProgressBar.showProgressBar(0, 'pdn-extract');
		const extractor = new PcbExtractor();

		const [, easyedaData] = await Promise.all([
			wasmClient.init(),
			extractor.extractNetworkInfo((p) => {
				eda.sys_LoadingAndProgressBar.showProgressBar(p, 'pdn-extract');
			}),
		]);

		if (!easyedaData || (easyedaData.vias.length === 0 && easyedaData.pads.length === 0)) {
			const msg = '未找到 PCB 数据，请确保打开了 PCB 文件';
			console.warn('[PDN]', msg);
			eda.sys_Dialog.showInformationMessage(msg, '警告');
			eda.sys_LoadingAndProgressBar.showProgressBar(100, 'pdn-extract');
			return;
		}

		eda.sys_LoadingAndProgressBar.showProgressBar(100, 'pdn-extract');

		const layerNames = easyedaData.layerNames;

		let lastError = '';

		let lastConfig: PdnConfig | null = null;

		while (true) {
			const config = await openConfigPanel(easyedaData.pads, layerNames, lastError, lastConfig);
			lastError = '';
			if (!config)
				return;
			lastConfig = config;

			try {
				const action = await runPdnAnalysisOnce(easyedaData, config, wasmClient, extractor.diagnostics);
				if (action !== 'reanalyze')
					return;
			}
			catch (e) {
				lastError = `${e}`;
			}
		}
	}
	catch (error) {
		if (error === '__CANCEL__' || (error instanceof Error && error.message === '__CANCEL__'))
			return;
		console.error('[PDN] 分析失败:', error);
		eda.sys_Dialog.showInformationMessage(`分析失败: ${error}`, '错误');
		for (const id of ['pdn-extract', 'pdn-convert', 'pdn-analyze']) {
			try {
				eda.sys_LoadingAndProgressBar.showProgressBar(100, id);
			}
			catch {}
		}
	}
}

async function runPdnAnalysisOnce(
	easyedaData: EasyEDA_PcbData,
	config: PdnConfig,
	wasmClient: PdnWasmClient,
	extractorDiagnostics: string[] = [],
): Promise<'reanalyze' | void> {
	const layerNames = easyedaData.layerNames;
	const converter = new PcbDataConverter();

	eda.sys_LoadingAndProgressBar.showProgressBar(0, 'pdn-convert');

	// === Multi-run analysis: 1 combined + N individual ===
	const isMultiNetwork = config.rails.length > 1;
	const totalRuns = isMultiNetwork ? config.rails.length + 1 : 1;
	const allResults: AnalysisResultEntry[] = [];

	// Get ODB++ archive with geometry and authoritative net attribution.
	let odbBlob: Blob | null = null;
	try {
		const odbFile = await eda.pcb_ManufactureData.getOpenDatabaseDoublePlusFile();
		if (!odbFile) {
			throw new Error('getOpenDatabaseDoublePlusFile() 返回空');
		}
		odbBlob = odbFile;
	}
	catch (e) {
		throw new Error(`无法获取 ODB++ 文件，分析终止：${e}`);
	}

	// Helper: run one analysis for a given config
	const runAnalysis = async (runConfig: PdnConfig, runLabel: string) => {
		const backendConfig = converter.buildODBConfig(easyedaData, runConfig);
		const solution: any = await wasmClient.analyzeODB(odbBlob!, JSON.stringify(backendConfig));
		console.warn(`[PDN] Backend response: success=${solution?.success}, message=${solution?.message ?? '(none)'}, layer_solutions=${solution?.layer_solutions?.length}, has connection_points=${!!(solution as any)?.connection_points}`);

		if (!solution || !solution.layer_solutions || solution.layer_solutions.length === 0) {
			const backendMsg = solution?.message ? `：${solution.message}` : '';
			const diagLines: string[] = solution?.diagnostics;
			const dialogMsg = diagLines && diagLines.length > 0
				? `[${runLabel}] 求解失败${backendMsg}\n\n诊断日志:\n${diagLines.join('\n')}`
				: `[${runLabel}] 求解失败：未生成有效结果${backendMsg}`;
			console.error('[PDN]', dialogMsg, { solution });
			eda.sys_Dialog.showInformationMessage(dialogMsg, '错误');
			throw new Error(dialogMsg);
		}
		const solverInfo = solution.solver_info;
		const gni = solverInfo?.ground_node_current;
		const rn = solverInfo?.residual_norm;
		if (gni == null || rn == null || Number.isNaN(gni) || Number.isNaN(rn)) {
			throw new Error(`[${runLabel}] 矩阵奇异，无法求解`);
		}

		const solutionData = converter.deserializeSolution(solution, Object.values(layerNames));
		const connectionPoints = (solution as any).connection_points ?? {};
		const layerBoundaries = (solution as any).layer_boundaries ?? {};
		const layerOutlines = (solution as any).layer_outlines ?? {};
		const currentWarnings = (solution as any).current_warnings ?? [];
		const viaPositions = (solution as any).via_positions ?? [];
		const warningMessage = solution.success === false && solution.message ? solution.message : undefined;
		// 显式清理大对象，防止内存泄漏
		solution.layer_solutions.length = 0;
		(solution as any).connection_points = null;
		(solution as any).layer_boundaries = null;

		return {
			label: runLabel,
			result: solutionData,
			networkInfo: buildNetworkInfo(runConfig),
			connectionPoints,
			layerBoundaries,
			layerOutlines,
			viaPositions,
			warningMessage,
			currentWarnings,
			extractorDiagnostics,
		} as AnalysisResultEntry;
	};

	// Show analyzing dialog
	eda.sys_IFrame.openIFrame('/ui/analyzing.html', 480, 360, 'pdn-analyzing', {
		buttonCallbackFn: () => {},
		grayscaleMask: false,
	}).catch(() => {});

	try {
		let completedRuns = 0;

		// Run 1: Combined analysis (all networks)
		// 单网络仿真时显示网络名，多网络仿真时显示"全部"
		const firstRunLabel = isMultiNetwork ? '全部' : `${config.rails[0].net} (${config.rails[0].voltage}V)`;
		const combinedResult = await runAnalysis(config, firstRunLabel);
		allResults.push(combinedResult);
		completedRuns++;
		eda.sys_LoadingAndProgressBar.showProgressBar(100, 'pdn-convert');
		eda.sys_LoadingAndProgressBar.showProgressBar(Math.round(completedRuns / totalRuns * 100), 'pdn-analyze');

		// Runs 2..N+1: Individual network analyses (only for multi-network)
		if (isMultiNetwork) {
			for (const rail of config.rails) {
				const singleConfig: PdnConfig = {
					rails: [rail],
					layerCuThickness: config.layerCuThickness,
				};
				const label = `${rail.net} (${rail.voltage}V)`;
				try {
					const individualResult = await runAnalysis(singleConfig, label);
					allResults.push(individualResult);
				}
				catch (indError) {
					// Individual run failed — skip, don't abort everything
					console.warn(`Individual analysis for ${label} failed:`, indError);
				}
				completedRuns++;
				eda.sys_LoadingAndProgressBar.showProgressBar(Math.round(completedRuns / totalRuns * 100), 'pdn-analyze');
			}
		}
	}
	catch (e) {
		try {
			await eda.sys_IFrame.closeIFrame('pdn-analyzing');
		}
		catch {}
		eda.sys_LoadingAndProgressBar.showProgressBar(100, 'pdn-convert');
		eda.sys_LoadingAndProgressBar.showProgressBar(100, 'pdn-analyze');
		// 显式释放大对象，防止内存泄漏
		odbBlob = null;
		allResults.length = 0;
		throw e;
	}

	// Close analyzing dialog
	try {
		await eda.sys_IFrame.closeIFrame('pdn-analyzing');
	}
	catch {}
	eda.sys_LoadingAndProgressBar.showProgressBar(100, 'pdn-convert');
	eda.sys_LoadingAndProgressBar.showProgressBar(100, 'pdn-analyze');

	// 释放 ODB++ Blob，大对象用完即释放
	odbBlob = null;

	const display = new ResultDisplay();
	const resultSet: AnalysisResultSet = { results: allResults };
	const action = await display.showResultSet(resultSet, layerNames);

	// 清理结果集，释放内存
	if (action === 'reanalyze') {
		allResults.length = 0;
	}

	return action;
}

function openConfigPanel(pads: EasyEDA_Pad[], layerNames: Record<number, string>, lastError?: string, lastConfig?: PdnConfig | null): Promise<PdnConfig | null> {
	return new Promise((resolve) => {
		try {
			eda.sys_IFrame.closeIFrame('pdn-config');
		}
		catch {}

		let resolved = false;
		// 订阅追踪数组，确保所有订阅都能被清理
		const subscriptions: any[] = [];

		const cleanup = () => {
			if (!resolved) {
				resolved = true;
				resolve(null);
			}
			// 清理所有追踪的订阅
			for (const sub of subscriptions) {
				try {
					sub.cancel();
				}
				catch {}
			}
			subscriptions.length = 0;
		};

		const configReadyTask = eda.sys_MessageBus.subscribe('pdn-config-ready', () => {
			configReadyTask.cancel();
			const padsByNet: Record<string, EasyEDA_Pad[]> = {};
			for (const pad of pads) {
				if (!pad.net)
					continue;
				const list = padsByNet[pad.net] ?? [];
				list.push(pad);
				padsByNet[pad.net] = list;
			}
			eda.sys_MessageBus.publish('pdn-config-data', {
				padsByNet,
				layerNames,
				lastError: lastError || '',
				lastConfig: lastConfig || null,
			});
		});
		subscriptions.push(configReadyTask);

		const configResultTask = eda.sys_MessageBus.subscribe('pdn-config-result', (msg: any) => {
			if (resolved)
				return;
			resolved = true;
			try {
				eda.sys_IFrame.closeIFrame('pdn-config');
			}
			catch {}
			resolve(msg.config as PdnConfig);
			cleanup();
		});
		subscriptions.push(configResultTask);

		const configCancelTask = eda.sys_MessageBus.subscribe('pdn-config-cancel', () => {
			cleanup();
			try {
				eda.sys_IFrame.closeIFrame('pdn-config');
			}
			catch {}
		});
		subscriptions.push(configCancelTask);

		eda.sys_IFrame.openIFrame('/ui/config.html', 860, 620, 'pdn-config', {
			maximizeButton: true,
			minimizeButton: true,
			minimizeStyle: 'collapsed',
			grayscaleMask: false,
			title: 'PDN 分析配置',
			buttonCallbackFn: (btn) => {
				if (btn === 'close')
					cleanup();
			},
		}).catch(() => cleanup());
	});
}

const MIL_TO_MM = 0.0254;

function buildNetworkInfo(config: PdnConfig): NetworkInfo[] {
	return config.rails.map(rail => ({
		name: rail.net,
		voltage: rail.voltage,
		sourcePads: rail.sources.flatMap(s =>
			s.pads.map(p => ({ x: p.x * MIL_TO_MM, y: p.y * MIL_TO_MM, layer: p.layer })),
		),
		sourceGndPads: rail.sources.flatMap(s =>
			(s.gnd_pads || []).map(p => ({ x: p.x * MIL_TO_MM, y: p.y * MIL_TO_MM, layer: p.layer })),
		),
		loadPads: rail.loads.flatMap(l =>
			l.pads.map(p => ({ x: p.x * MIL_TO_MM, y: p.y * MIL_TO_MM, layer: p.layer })),
		),
		loadGndPads: rail.loads.flatMap(l =>
			(l.gnd_pads || []).map(p => ({ x: p.x * MIL_TO_MM, y: p.y * MIL_TO_MM, layer: p.layer })),
		),
	}));
}

export async function showResults(): Promise<void> {
	try {
		// Try showing existing hidden iframe first
		const ok = await eda.sys_IFrame.showIFrame('pdne-results');
		if (ok)
			return;
	}
	catch {}

	// No existing iframe — cannot reopen from cache because SysStorage no longer
	// holds results (we deliver results via sys_MessageBus to avoid the 1MB cap).
	eda.sys_Dialog.showInformationMessage('没有可显示的分析结果，请先运行 PDN 分析', '提示');
}

function buildDebugPdnConfig(easyedaData: EasyEDA_PcbData): PdnConfig {
	const targetNet = '3V3';
	const sourcePads = easyedaData.pads.filter(p => p.ref_des === 'J2');
	const loadPads = easyedaData.pads.filter(p => p.ref_des === 'H1');

	if (sourcePads.length === 0)
		throw new Error('未找到 J2 焊盘，无法自动构建调试配置');
	if (loadPads.length === 0)
		throw new Error('未找到 H1 焊盘，无法自动构建调试配置');

	const layerNameOf = (pad: EasyEDA_Pad) => {
		if (pad.layer != null && easyedaData.layerNames[pad.layer])
			return easyedaData.layerNames[pad.layer];
		// 通孔焊盘默认使用顶层
		return easyedaData.layerNames[1];
	};

	const toPadSpec = (pad: EasyEDA_Pad) => ({ x: pad.x, y: pad.y, layer: layerNameOf(pad) });

	const layerCuThickness: Record<number, number> = {};
	for (const id of Object.keys(easyedaData.layerNames).map(Number)) {
		layerCuThickness[id] = 0.035;
	}

	return {
		rails: [
			{
				net: targetNet,
				voltage: 3.3,
				sources: [
					{
						ref_des: 'J2',
						pads: sourcePads.map(toPadSpec),
					},
				],
				loads: [
					{
						ref_des: 'H1',
						current: 0.1,
						pads: loadPads.map(toPadSpec),
					},
				],
			},
		],
		layerCuThickness,
	};
}

export async function runPdnAnalysisDebug(): Promise<void> {
	try {
		// 在开始新分析前清理 Storage 中的旧数据
		ResultDisplay.cleanupStorage();

		const wasmClient = new PdnWasmClient();
		eda.sys_LoadingAndProgressBar.showProgressBar(0, 'pdn-extract');
		const extractor = new PcbExtractor();

		const [, easyedaData] = await Promise.all([
			wasmClient.init(),
			extractor.extractNetworkInfo((p) => {
				eda.sys_LoadingAndProgressBar.showProgressBar(p, 'pdn-extract');
			}),
		]);

		if (!easyedaData || (easyedaData.vias.length === 0 && easyedaData.pads.length === 0)) {
			const msg = '未找到 PCB 数据，请确保打开了 PCB 文件';
			console.warn('[PDN]', msg);
			eda.sys_Dialog.showInformationMessage(msg, '警告');
			eda.sys_LoadingAndProgressBar.showProgressBar(100, 'pdn-extract');
			return;
		}

		eda.sys_LoadingAndProgressBar.showProgressBar(100, 'pdn-extract');

		const config = buildDebugPdnConfig(easyedaData);
		await runPdnAnalysisOnce(easyedaData, config, wasmClient, extractor.diagnostics);
	}
	catch (error) {
		if (error === '__CANCEL__' || (error instanceof Error && error.message === '__CANCEL__'))
			return;
		console.error('[PDN] 调试分析失败:', error);
		eda.sys_Dialog.showInformationMessage(`调试分析失败: ${error}`, '错误');
		for (const id of ['pdn-extract', 'pdn-convert', 'pdn-analyze']) {
			try {
				eda.sys_LoadingAndProgressBar.showProgressBar(100, id);
			}
			catch {}
		}
	}
}

export function about(): void {
	const content = `PDN 分析插件 v${extensionConfig.version}

用于从 EasyEDA 提取 PCB 数据并进行 PDN 电源分配网络分析

功能：
• 从 EasyEDA 提取 PCB 走线、过孔、焊盘、铺铜数据
• 转换为 padne 分析格式
• 通过内置 Go/WASM 引擎进行 FEM 求解，无需 Python 后端
• 展示电压分布和功率密度结果`;
	eda.sys_Dialog.showInformationMessage(content, '关于');
}

export function activate(_status?: 'onStartupFinished', _arg?: string): void {}
