// ============================================================
// 几何类型
// ============================================================

/** 二维点 */
export interface Point {
  x: number;
  y: number;
}

// ============================================================
// EasyEDA 原始数据类型
// ============================================================

/** EasyEDA 走线 */
export interface EasyEDA_Track {
  net: string;
  x1: number;
  y1: number;
  x2: number;
  y2: number;
  width: number;
  layer: number;
}

/** EasyEDA 过孔 */
export interface EasyEDA_Via {
  net: string;
  x: number;
  y: number;
  diameter: number;
  hole_diameter: number;
  /** 盲孔/埋孔类型名称 (如 'Blind: Top-Layer2', 'Buried: Layer2-Layer5') */
  via_type?: string;
}

/** EasyEDA 焊盘 */
export interface EasyEDA_Pad {
  net: string;
  x: number;
  y: number;
  pad_number: string;
  width: number;
  height: number;
  hole_diameter: number;
  layer?: number;
  ref_des?: string;
  device_name?: string;
}

/** EasyEDA 铺铜区域 */
export interface EasyEDA_CopperPour {
  net: string;
  layer: number;
  vertices: Array<{ x: number; y: number }>;
  holes: Array<Array<{ x: number; y: number }>>;
  is_fill: boolean;
}

/** EasyEDA PCB 完整数据 */
export interface EasyEDA_PcbData {
  tracks: EasyEDA_Track[];
  vias: EasyEDA_Via[];
  pads: EasyEDA_Pad[];
  copperPours: EasyEDA_CopperPour[];
  layerNames: Record<number, string>;
  outerLayerIds: Set<number>;
}

// ============================================================
// 序列化格式类型（发送到 Python 后端）
// ============================================================

/** 序列化点 [x, y] */
export type SerializedPoint = [number, number];

// ============================================================
// 求解结果类型（从 Python 后端返回）
// ============================================================

/** 三角面片 */
export interface MeshTriangle {
  vertices: [number, number, number];
}

/** 序列化网格 */
export interface SerializedMesh {
  vertices: SerializedPoint[];
  triangles: MeshTriangle[];
  potentials: number[];
  power_densities: number[];
  current_densities?: [number, number][];
}

/** 序列化断开连接的网格 */
export interface SerializedDisconnectedMesh {
  vertices: SerializedPoint[];
  triangles: MeshTriangle[];
}

/** 序列化层求解结果 */
export interface SerializedLayerSolution {
  layer_name: string;
  meshes: SerializedMesh[];
  disconnected_meshes: SerializedDisconnectedMesh[];
}

/** 序列化求解器信息 */
export interface SerializedSolverInfo {
  ground_node_current: number;
  residual_norm: number;
}

/** 序列化完整求解结果 */
export interface SerializedSolution {
  layer_solutions: SerializedLayerSolution[];
  solver_info: SerializedSolverInfo;
  diagnostics?: string[];
  current_warnings?: CurrentCheckWarning[];
}

// ============================================================
// 显示用类型
// ============================================================

/** 网格数据 */
export interface MeshData {
  vertices: Point[];
  triangles: [number, number, number][];
  potentials: number[];
  powerDensities: number[];
  currentDensities?: [number, number][];
}

/** 层求解结果数据 */
export interface LayerSolutionData {
  layerName: string;
  meshes: MeshData[];
  disconnectedMeshes: MeshData[];
}

/** 求解器信息 */
export interface SolverInfoData {
  groundNodeCurrent: number;
  residualNorm: number;
}

/** 完整求解结果数据 */
export interface SolutionData {
  layerSolutions: LayerSolutionData[];
  solverInfo: SolverInfoData;
  diagnostics?: string[];
  currentWarnings?: CurrentCheckWarning[];
}

// ============================================================
// 用户配置类型（config.html → index.ts）
// ============================================================

/** 用户的 PDN 分析配置 */
export interface PdnConfig {
  rails: PdnRailConfig[];
  layerCuThickness: Record<number, number>;
  tempRise?: number;  // 允许温升 (°C)，用于电流容量检查，默认 10°C
}

/** 单个电源轨道配置 */
export interface PdnRailConfig {
  net: string;
  voltage: number;
  sources: PdnSourceConfig[];
  loads: PdnLoadConfig[];
  gnd_net?: string;
}

/** 电压源配置 */
export interface PdnSourceConfig {
  ref_des: string;
  pads: Array<{ x: number; y: number; layer: string }>;
  gnd_pads?: Array<{ x: number; y: number; layer: string }>;
}

/** 电流负载配置 */
export interface PdnLoadConfig {
  ref_des: string;
  current: number;
  pads: Array<{ x: number; y: number; layer: string }>;
  gnd_pads?: Array<{ x: number; y: number; layer: string }>;
}

// ============================================================
// 可视化图片类型（Python 后端 → 前端）
// ============================================================

/** 分析结果中的可视化图片 */
export interface AnalysisImages {
  view_3d?: string;
  layers: Record<string, string>;
}

// ============================================================
// PCB 上下文数据（用于热力图与 PCB 叠加显示）
// ============================================================

/** 上下文走线（非分析网络的走线，已转换为 mm） */
export interface ContextTrack {
  x1: number; y1: number;
  x2: number; y2: number;
  width: number;
  layer: number;
  net: string;
}

/** 上下文焊盘（已转换为 mm） */
export interface ContextPad {
  x: number;
  y: number;
  width: number;
  height: number;
  hole_diameter: number;
  layer?: number;
  net: string;
  ref_des?: string;
  pad_number: string;
}

/** PCB 上下文数据，传递给 results.html 用于叠加显示 */
export interface PcbContextData {
  contextTracks: ContextTrack[];
  contextPads: ContextPad[];
}

// ============================================================
// 网络切换类型
// ============================================================

/** 网络焊盘位置（mm） */
export interface NetworkPadPos {
  x: number;
  y: number;
  layer: string;
}

/** 单个网络的信息，用于结果页面切换网络 */
export interface NetworkInfo {
  name: string;
  voltage: number;
  sourcePads: NetworkPadPos[];
  sourceGndPads: NetworkPadPos[];
  loadPads: NetworkPadPos[];
  loadGndPads: NetworkPadPos[];
}

// ============================================================
// 电流容量检查类型
// ============================================================

/** 电流容量检查警告 */
export interface CurrentCheckWarning {
  network_name: string;         // 网络名称
  layer_name: string;           // 层名
  calculated_current: number;   // 计算电流
  max_allowed_current: number;  // 最大允许电流
  utilization: number;          // 利用率 (0-1), >1 表示超限
  is_exceeded: boolean;         // 是否超过限制
  trace_width_mm: number;       // 走线宽度
  copper_oz: number;           // 铜厚
  temp_rise: number;           // 温升 (°C)
  message: string;              // 警告消息
}

// ============================================================
// 多网络分析结果类型（N+1 次求解）
// ============================================================

/** 单次分析结果的完整数据包 */
export interface AnalysisResultEntry {
  label: string;
  result: SolutionData;
  networkInfo: NetworkInfo[];
  connectionPoints: Record<string, Array<{ x: number; y: number; is_source: boolean }>>;
  layerBoundaries: Record<string, Array<{ exterior: number[][]; holes: number[][][] }>>;
  pcbContext: PcbContextData;
  warningMessage?: string;
  currentWarnings?: CurrentCheckWarning[];
  extractorDiagnostics?: string[];
}

/** 多次分析的结果集（1次合并 + N次单独） */
export interface AnalysisResultSet {
  results: AnalysisResultEntry[];
}
