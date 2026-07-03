import * as extensionConfig from '../extension.json';
import { PcbExtractor } from './extract';
import { PcbDataConverter } from './convert';
import { PdnApiClient } from './api';
import { ResultDisplay } from './display';
import type { PdnConfig, EasyEDA_Track, EasyEDA_Pad, PcbContextData, NetworkInfo, AnalysisResultSet, AnalysisResultEntry } from './types';

const CONFIG = {
  host: 'localhost',
  port: 5000,
  testEndpoint: '/test',
};

function getServiceAddress(): string {
  return `${CONFIG.host}:${CONFIG.port}`;
}

// ============================================================
// 导出函数
// ============================================================

export async function runPdnAnalysis(): Promise<void> {
  try {
    // 在开始新分析前清理 Storage 中的旧数据
    ResultDisplay.cleanupStorage();

    const api = new PdnApiClient(CONFIG.host, CONFIG.port, {
      testEndpoint: CONFIG.testEndpoint,
    });

    eda.sys_LoadingAndProgressBar.showProgressBar(0, 'pdn-extract');
    const extractor = new PcbExtractor();
    const converter = new PcbDataConverter();

    const [isRunning, easyedaData] = await Promise.all([
      api.checkService().then(async (isRunning) => {
        if (!isRunning) {
          const started = await showServiceCheckDialog();
          if (!started) throw new Error('__CANCEL__');
        }
        return true;
      }),
      extractor.extractNetworkInfo((p) => {
        eda.sys_LoadingAndProgressBar.showProgressBar(p, 'pdn-extract');
      }),
    ]);

    if (!easyedaData || (easyedaData.vias.length === 0 && easyedaData.pads.length === 0)) {
      eda.sys_Dialog.showInformationMessage('未找到 PCB 数据，请确保打开了 PCB 文件', '警告');
      eda.sys_LoadingAndProgressBar.showProgressBar(100, 'pdn-extract');
      return;
    }

    eda.sys_LoadingAndProgressBar.showProgressBar(100, 'pdn-extract');

    const layerNames = easyedaData.layerNames;

    let lastError = '';

    while (true) {
      const config = await openConfigPanel(easyedaData.pads, layerNames, lastError);
      lastError = '';
      if (!config) return;

      eda.sys_LoadingAndProgressBar.showProgressBar(0, 'pdn-convert');

      // === Multi-run analysis: 1 combined + N individual ===
      const isMultiNetwork = config.rails.length > 1;
      const totalRuns = isMultiNetwork ? config.rails.length + 1 : 1;
      const allResults: AnalysisResultEntry[] = [];

      // Get Gerber file (required — no fallback to manual geometry extraction)
      let gerberBlob: Blob | null = null;
      try {
        const gerberFile = await eda.pcb_ManufactureData.getGerberFile();
        if (!gerberFile) {
          throw new Error('无法获取 Gerber 文件：getGerberFile() 返回空');
        }
        gerberBlob = gerberFile;
      } catch (e) {
        throw new Error(`无法获取 Gerber 文件，分析终止：${e}`);
      }

      // Helper: run one analysis for a given config
      const runAnalysis = async (runConfig: PdnConfig, runLabel: string) => {
        const gerberConfig = converter.buildGerberConfig(easyedaData, runConfig);
        const solution = await api.analyzeGerber(gerberBlob!, JSON.stringify(gerberConfig));
        console.log(`[PDN] Backend response: success=${solution?.success}, layer_solutions=${solution?.layer_solutions?.length}, has connection_points=${!!(solution as any)?.connection_points}`);


        if (!solution || !solution.layer_solutions || solution.layer_solutions.length === 0) {
          const backendMsg = solution?.message ? `：${solution.message}` : '';
          const diagLines: string[] = solution?.diagnostics;
          if (diagLines && diagLines.length > 0) {
            eda.sys_Dialog.showInformationMessage(
              `[${runLabel}] 求解失败${backendMsg}\n\n诊断日志:\n${diagLines.join('\n')}`, '错误');
          }
          throw new Error(`[${runLabel}] 求解失败：未生成有效结果${backendMsg}`);
        }
        const solverInfo = solution.solver_info;
        const gni = solverInfo?.ground_node_current;
        const rn = solverInfo?.residual_norm;
        if (gni == null || rn == null || isNaN(gni) || isNaN(rn)) {
          throw new Error(`[${runLabel}] 矩阵奇异，无法求解`);
        }

        const solutionData = converter.deserializeSolution(solution, Object.values(layerNames));
        const connectionPoints = (solution as any).connection_points ?? {};
        const layerBoundaries = (solution as any).layer_boundaries ?? {};
        const currentWarnings = (solution as any).current_warnings ?? [];
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
          pcbContext: buildPcbContext(easyedaData.tracks, easyedaData.pads, runConfig),
          warningMessage,
          currentWarnings,
          extractorDiagnostics: extractor.diagnostics,
        } as AnalysisResultEntry;
      };

      // Show analyzing dialog
      eda.sys_IFrame.openIFrame('/ui/analyzing.html', 360, 160, 'pdn-analyzing', {
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
            } catch (indError) {
              // Individual run failed — skip, don't abort everything
              console.warn(`Individual analysis for ${label} failed:`, indError);
            }
            completedRuns++;
            eda.sys_LoadingAndProgressBar.showProgressBar(Math.round(completedRuns / totalRuns * 100), 'pdn-analyze');
          }
        }
      } catch (e) {
        try { await eda.sys_IFrame.closeIFrame('pdn-analyzing'); } catch {}
        eda.sys_LoadingAndProgressBar.showProgressBar(100, 'pdn-convert');
        eda.sys_LoadingAndProgressBar.showProgressBar(100, 'pdn-analyze');
        // 显式释放大对象，防止内存泄漏
        gerberBlob = null;
        allResults.length = 0;
        lastError = `${e}`;
        continue;
      }

      // Close analyzing dialog
      try { await eda.sys_IFrame.closeIFrame('pdn-analyzing'); } catch {}
      eda.sys_LoadingAndProgressBar.showProgressBar(100, 'pdn-convert');
      eda.sys_LoadingAndProgressBar.showProgressBar(100, 'pdn-analyze');

      // 释放 Gerber Blob，大对象用完即释放
      gerberBlob = null;

      const display = new ResultDisplay();
      const resultSet: AnalysisResultSet = { results: allResults };
      const action = await display.showResultSet(resultSet, layerNames);

      // 清理结果集，释放内存
      if (action === 'reanalyze') {
        allResults.length = 0;
      }
      if (action !== 'reanalyze') return;
    }
  } catch (error) {
    if (error === '__CANCEL__' || (error instanceof Error && error.message === '__CANCEL__')) return;
    eda.sys_Dialog.showInformationMessage(`分析失败: ${error}`, '错误');
    for (const id of ['pdn-extract', 'pdn-convert', 'pdn-analyze']) {
      try { eda.sys_LoadingAndProgressBar.showProgressBar(100, id); } catch {}
    }
  }
}

function showServiceCheckDialog(): Promise<boolean> {
  return new Promise((resolve) => {
    try { eda.sys_IFrame.closeIFrame('pdn-service-check'); } catch {}

    let resolved = false;
    // 订阅追踪数组，确保所有订阅都能被清理
    const subscriptions: any[] = [];

    const cleanup = () => {
      if (!resolved) { resolved = true; resolve(false); }
      // 清理所有追踪的订阅
      for (const sub of subscriptions) {
        try { sub.cancel(); } catch {}
      }
      subscriptions.length = 0;
    };

    const readyTask = eda.sys_MessageBus.subscribe('pdn-service-check-ready', () => {
      readyTask.cancel();
      eda.sys_MessageBus.publish('pdn-service-check-config', {
        host: CONFIG.host,
        port: CONFIG.port,
      });
    });
    subscriptions.push(readyTask);

    const successTask = eda.sys_MessageBus.subscribe('pdn-service-check-success', () => {
      if (resolved) return;
      resolved = true;
      try { eda.sys_IFrame.closeIFrame('pdn-service-check'); } catch {}
      resolve(true);
      cleanup();
    });
    subscriptions.push(successTask);

    eda.sys_IFrame.openIFrame('/ui/service-check.html', 480, 440, 'pdn-service-check', {
      buttonCallbackFn: (btn) => {
        if (btn === 'close') cleanup();
      },
    }).catch(() => cleanup());
  });
}

function openConfigPanel(pads: EasyEDA_Pad[], layerNames: Record<number, string>, lastError?: string): Promise<PdnConfig | null> {
  return new Promise((resolve) => {
    try { eda.sys_IFrame.closeIFrame('pdn-config'); } catch {}

    let resolved = false;
    // 订阅追踪数组，确保所有订阅都能被清理
    const subscriptions: any[] = [];

    const cleanup = () => {
      if (!resolved) { resolved = true; resolve(null); }
      // 清理所有追踪的订阅
      for (const sub of subscriptions) {
        try { sub.cancel(); } catch {}
      }
      subscriptions.length = 0;
    };

    const configReadyTask = eda.sys_MessageBus.subscribe('pdn-config-ready', () => {
      configReadyTask.cancel();
      const padsByNet: Record<string, EasyEDA_Pad[]> = {};
      for (const pad of pads) {
        if (!pad.net) continue;
        const list = padsByNet[pad.net] ?? [];
        list.push(pad);
        padsByNet[pad.net] = list;
      }
      eda.sys_MessageBus.publish('pdn-config-data', { padsByNet, layerNames, lastError: lastError || '' });
    });
    subscriptions.push(configReadyTask);

    const configResultTask = eda.sys_MessageBus.subscribe('pdn-config-result', (msg: any) => {
      if (resolved) return;
      resolved = true;
      try { eda.sys_IFrame.closeIFrame('pdn-config'); } catch {}
      resolve(msg.config as PdnConfig);
      cleanup();
    });
    subscriptions.push(configResultTask);

    const configCancelTask = eda.sys_MessageBus.subscribe('pdn-config-cancel', () => {
      cleanup();
      try { eda.sys_IFrame.closeIFrame('pdn-config'); } catch {}
    });
    subscriptions.push(configCancelTask);

    eda.sys_IFrame.openIFrame('/ui/config.html', 860, 620, 'pdn-config', {
      maximizeButton: true,
      minimizeButton: true,
      minimizeStyle: 'collapsed',
      grayscaleMask: false,
      title: 'PDN 分析配置',
      buttonCallbackFn: (btn) => {
        if (btn === 'close') cleanup();
      },
    }).catch(() => cleanup());
  });
}

const MIL_TO_MM = 0.0254;

function buildPcbContext(
  allTracks: EasyEDA_Track[],
  allPads: EasyEDA_Pad[],
  config: PdnConfig,
): PcbContextData {
  const analyzedNets = new Set(config.rails.map(r => r.net));
  for (const rail of config.rails) {
    if (rail.gnd_net) analyzedNets.add(rail.gnd_net);
  }
  return {
    contextTracks: allTracks
      .filter(t => !analyzedNets.has(t.net))
      .map(t => ({
        x1: t.x1 * MIL_TO_MM, y1: t.y1 * MIL_TO_MM,
        x2: t.x2 * MIL_TO_MM, y2: t.y2 * MIL_TO_MM,
        width: t.width * MIL_TO_MM,
        layer: t.layer,
        net: t.net,
      })),
    contextPads: allPads.filter(p => analyzedNets.has(p.net)).map(p => ({
      x: p.x * MIL_TO_MM,
      y: p.y * MIL_TO_MM,
      width: p.width * MIL_TO_MM,
      height: p.height * MIL_TO_MM,
      hole_diameter: p.hole_diameter * MIL_TO_MM,
      layer: p.layer,
      net: p.net,
      ref_des: p.ref_des,
      pad_number: p.pad_number,
    })),
  };
}

function buildNetworkInfo(config: PdnConfig): NetworkInfo[] {
  return config.rails.map(rail => ({
    name: rail.net,
    voltage: rail.voltage,
    sourcePads: rail.sources.flatMap(s =>
      s.pads.map(p => ({ x: p.x * MIL_TO_MM, y: p.y * MIL_TO_MM, layer: p.layer }))
    ),
    sourceGndPads: rail.sources.flatMap(s =>
      (s.gnd_pads || []).map(p => ({ x: p.x * MIL_TO_MM, y: p.y * MIL_TO_MM, layer: p.layer }))
    ),
    loadPads: rail.loads.flatMap(l =>
      l.pads.map(p => ({ x: p.x * MIL_TO_MM, y: p.y * MIL_TO_MM, layer: p.layer }))
    ),
    loadGndPads: rail.loads.flatMap(l =>
      (l.gnd_pads || []).map(p => ({ x: p.x * MIL_TO_MM, y: p.y * MIL_TO_MM, layer: p.layer }))
    ),
  }));
}

export async function showResults(): Promise<void> {
  try {
    // Try showing existing hidden iframe first
    const ok = await eda.sys_IFrame.showIFrame('pdne-results');
    if (ok) return;
  } catch {}

  // No existing iframe — check for cached results and reopen
  try {
    const raw = eda.sys_Storage.getExtensionUserConfig('pdn-results');
    if (!raw || typeof raw !== 'string') {
      eda.sys_Dialog.showInformationMessage('没有可显示的分析结果，请先运行 PDN 分析', '提示');
      return;
    }
    const data = JSON.parse(raw);
    if (!data.result || !data.result.layerSolutions) {
      eda.sys_Dialog.showInformationMessage('没有可显示的分析结果，请先运行 PDN 分析', '提示');
      return;
    }

    // Reopen results iframe (it will load data from Storage)
    eda.sys_IFrame.openIFrame('/ui/results.html', 960, 900, 'pdne-results', {
      maximizeButton: true,
      minimizeButton: false,
      grayscaleMask: false,
      title: 'PDN 分析结果',
      buttonCallbackFn: (btn) => {
        if (btn === 'close') {
          try { eda.sys_IFrame.closeIFrame('pdne-results'); } catch {}
        }
      },
    }).catch(() => {});
  } catch {
    eda.sys_Dialog.showInformationMessage('没有可显示的分析结果，请先运行 PDN 分析', '提示');
  }
}

export function about(): void {
  const content = `PDN 分析插件 v${extensionConfig.version}

用于从 EasyEDA 提取 PCB 数据并进行 PDN 电源分配网络分析

功能：
• 从 EasyEDA 提取 PCB 走线、过孔、焊盘、铺铜数据
• 转换为 padne 分析格式
• 调用本地 Python 后端进行 FEM 求解
• 展示电压分布和功率密度结果`;
  eda.sys_Dialog.showInformationMessage(content, '关于');
}

export function activate(status?: 'onStartupFinished', arg?: string): void {}
