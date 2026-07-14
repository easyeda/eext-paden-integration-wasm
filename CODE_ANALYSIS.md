# PADEN 仿真扩展 - 详细代码分析文档

> **版本**: 1.0.8
> **生成日期**: 2026-06-04
> **项目**: eext-paden-integration
> **许可**: Apache-2.0

---

## 目录

1. [项目概述](#1-项目概述)
2. [目录结构](#2-目录结构)
3. [系统架构](#3-系统架构)
4. [核心模块分析](#4-核心模块分析)
5. [数据流程](#5-数据流程)
6. [FEM 求解原理](#6-fem-求解原理)
7. [类型系统](#7-类型系统)
8. [API 接口](#8-api-接口)
9. [UI 组件](#9-ui-组件)
10. [配置与构建](#10-配置与构建)

---

## 1. 项目概述

PADEN 仿真扩展是嘉立创EDA/EasyEDA 专业版的插件，用于 PCB 电源分配网络（PDN）的直流压降和电流密度分析。

### 1.1 主要功能

- **数据提取**: 从 PCB 中提取走线、过孔、焊盘、铺铜数据
- **Gerber 解析**: 解析 Gerber 文件获取精确的铜皮几何
- **FEM 求解**: 有限元法求解电压分布和电流密度
- **可视化**: WebGL 热力图展示分析结果
- **多网络分析**: 支持多个电源网络的 N+1 次求解

### 1.2 技术栈

| 层级 | 技术 |
|------|------|
| 前端 | TypeScript, esbuild, WebGL, earcut |
| 后端 | Python, FastAPI, numpy, scipy, shapely |
| 几何处理 | Shapely, PyGerber, trimesh |
| 数值计算 | scipy.sparse, scipy.spatial |

---

## 2. 目录结构

```
eext-paden-integration/
├── src/                          # TypeScript 前端源码
│   ├── index.ts                  # 主入口，流程编排
│   ├── extract.ts                # PCB 数据提取
│   ├── convert.ts                # 数据转换与配置构建
│   ├── api.ts                    # HTTP 通信
│   ├── display.ts                # 结果展示
│   └── types.ts                  # 类型定义
│
├── ui/                           # HTML 界面
│   ├── config.html               # 配置界面
│   ├── results.html              # 结果展示
│   ├── analyzing.html            # 分析中提示
│   └── service-check.html        # 服务检查
│
├── paden-service/                # Python 后端服务
│   ├── main.py                   # FastAPI 服务器
│   ├── solver.py                 # FEM 求解器
│   ├── problem.py                # 问题定义
│   ├── mesh_pure.py              # 网格数据结构
│   ├── calculation.py            # 电流容量计算
│   └── start-paden-windows.bat   # 启动脚本
│
├── config/                       # 构建配置
│   ├── esbuild.common.ts
│   └── esbuild.prod.ts
│
├── images/                       # 图标资源
├── extension.json                # 扩展配置
├── package.json                  # npm 配置
└── README.md                     # 文档
```

---

## 3. 系统架构

### 3.1 整体架构图

```
┌─────────────────────────────────────────────────────────────────┐
│                        EasyEDA PCB 编辑器                         │
└────────────────────────────────┬────────────────────────────────┘
                                 │
        ┌────────────────────────┴────────────────────────┐
        │                 TypeScript 前端                  │
        │  ┌─────────────────────────────────────────────┐ │
        │  │  index.ts - 主流程编排                      │ │
        │  │  ├─ extract.ts - PCB 数据提取               │ │
        │  │  ├─ convert.ts - 配置构建                   │ │
        │  │  ├─ api.ts - HTTP 通信                      │ │
        │  │  └─ display.ts - 结果展示                   │ │
        │  └─────────────────────────────────────────────┘ │
        └────────────────────────────────┬───────────────────┘
                                         │
                              ┌──────────┴──────────┐
                              │                     │
                        ┌─────▼─────┐         ┌────▼────┐
                        │ Config UI │         │ Gerber  │
                        │ 用户配置  │         │  文件   │
                        └─────┬─────┘         └────┬────┘
                              └──────────┬──────────┘
                                         │
        ┌────────────────────────────────┴─────────────────────────┐
        │                    HTTP POST                             │
        │  /analyze-gerber (multipart: gerber.zip + config.json) │
        └────────────────────────────────┬────────────────────────┘
                                         │
        ┌────────────────────────────────┴─────────────────────────┐
        │                 Python 后端 (FastAPI)                     │
        │  ┌─────────────────────────────────────────────────────┐ │
        │  │  main.py - Gerber 解析、坐标变换、网络构建          │ │
        │  ├─ solver.py - FEM 求解器                              │ │
        │  ├─ problem.py - 问题定义（层/网络/连接点）             │ │
        │  ├─ mesh_pure.py - 网格数据结构（半边数据结构）         │ │
        │  └─ calculation.py - 电流容量检查                       │ │
        │  └─────────────────────────────────────────────────────┘ │
        └────────────────────────────────┬────────────────────────┘
                                         │
        ┌────────────────────────────────┴─────────────────────────┐
        │              JSON Response (求解结果)                     │
        │  { layer_solutions, solver_info, diagnostics, warnings } │
        └────────────────────────────────┬────────────────────────┘
                                         │
        ┌────────────────────────────────┴─────────────────────────┐
        │              ui/results.html (WebGL)                     │
        │     电压分布热力图 / 功率密度热力图 / 层切换            │
        └──────────────────────────────────────────────────────────┘
```

### 3.2 数据流概览

```
EasyEDA PCB → extract.ts → EasyEDA_PcbData
                    │
                    ├─ 走线 (tracks)
                    ├─ 过孔 (vias)
                    ├─ 焊盘 (pads)
                    └─ 层信息 (layerNames)
                    │
                    ▼
              config.html → PdnConfig
                    │
                    ├─ 电源轨道 (rails)
                    ├─ 电压源 (sources)
                    ├─ 电流负载 (loads)
                    └─ 层铜厚度 (layerCuThickness)
                    │
                    ▼
              Gerber ZIP + Config JSON
                    │
                    ▼
              main.py (Python)
                    │
                    ├─ _gerber_to_shapely() → Layer (Shapely几何)
                    ├─ _extract_board_outline() → 板框裁剪
                    ├─ _compute_coordinate_transform() → 坐标变换
                    ├─ _punch_via_holes() → 过孔打孔
                    └─ _build_user_networks() → Network (VS + CS)
                    │
                    ▼
              solver.solve()
                    │
                    ├─ ConnectivityGraph (连通性分析)
                    ├─ generate_meshes_for_problem() (网格生成)
                    ├─ laplace_operator() (拉普拉斯算子)
                    ├─ assemble_system() (系统矩阵组装)
                    └─ solve_system() (稀疏求解)
                    │
                    ▼
              Solution → JSON
                    │
                    ▼
              results.html (WebGL 可视化)
```

---

## 4. 核心模块分析

### 4.1 index.ts - 主流程编排

**职责**: 协调整个分析流程，从数据提取到结果展示

```typescript
// 主要流程
export async function runPdnAnalysis(): Promise<void> {
	// 1. 检查后端服务
	// 2. 提取 PCB 数据
	// 3. 打开配置面板
	// 4. 执行多网络分析 (N+1 次求解)
	// 5. 展示结果
}
```

**多网络分析策略**:

```typescript
// N 个电源轨道 → N+1 次求解
// - 1 次合并分析（所有网络一起）
// - N 次单独分析（每个网络独立）
const totalRuns = isMultiNetwork ? config.rails.length + 1 : 1;
```

**关键函数**:

| 函数 | 职责 |
|------|------|
| `runPdnAnalysis()` | 主流程 |
| `showServiceCheckDialog()` | 服务检查对话框 |
| `openConfigPanel()` | 配置面板 |
| `buildNetworkInfo()` | 构建网络信息 |
| `buildPcbContext()` | 构建 PCB 上下文数据 |

### 4.2 extract.ts - PCB 数据提取

**职责**: 从 EasyEDA API 提取原始 PCB 数据

```typescript
export class PcbExtractor {
  async extractNetworkInfo(): Promise<EasyEDA_PcbData> {
    // 1. 提取层名称
    const layerNames = await this.extractLayerNames();

    // 2. 提取过孔、焊盘、走线（并发）
    const [bulkVias, bulkPads, bulkTracks] = await Promise.all([...]);

    // 3. 提取器件焊盘（并发控制）
    const compPinResults = await this.parallelMap(compMeta, async (...) => {...}, 8);

    return { tracks, vias, pads, copperPours, layerNames, outerLayerIds };
  }
}
```

**提取的图元**:

| 图元 | API | 数据结构 |
|------|-----|----------|
| 走线 | `pcb_PrimitiveLine.getAll()` | `EasyEDA_Track` |
| 过孔 | `pcb_PrimitiveVia.getAll()` | `EasyEDA_Via` |
| 焊盘 | `pcb_PrimitivePad.getAll()` | `EasyEDA_Pad` |
| 层名 | `pcb_Layer.getAllLayers()` | `Record<number, string>` |

**并发控制**:

```typescript
// 限制并发数，避免 API 过载
private async parallelMap<T, R>(
  items: T[],
  fn: (item: T) => Promise<R>,
  concurrency: number,
): Promise<R[]>
```

### 4.3 convert.ts - 数据转换与配置构建

**职责**: 构建 Gerber 配置 JSON，反序列化求解结果

```typescript
export class PcbDataConverter {
	// 构建 Gerber 配置
	buildGerberConfig(easyedaData, config): Record<string, any> {
		return {
			layers, // 层配置（电导、厚度）
			vias, // 过孔规格
			pads, // 焊盘规格
			sources, // 电压源
			loads, // 电流负载
			tracks, // 走线数据
			easyeda_bounds, // EasyEDA 边界框
			layer_cu_thickness, // 层铜厚度
			temp_rise, // 允许温升
		};
	}

	// 反序列化求解结果
	deserializeSolution(solution: SerializedSolution): SolutionData {
		// 将 Python 格式转换为 TypeScript 格式
	}
}
```

**盲孔/埋孔解析**:

```typescript
// "Blind: Top-Layer2" → [Top, Layer2]
// "Buried: Layer2-Layer5" → [Layer2, Layer3, Layer4, Layer5]
function parseViaLayerRange(viaType: string): string[] | null {
	const blindMatch = viaType.match(/Blind:\s*(.+?)\s*-\s*(.+)/);
	const buriedMatch = viaType.match(/Buried:\s*(.+?)\s*-\s*(.+)/);
	// ...
}
```

### 4.4 api.ts - HTTP 通信

**职责**: 与 Python 后端通信

```typescript
export class PdnApiClient {
	private host: string;
	private port: number;

	// 检查服务状态
	async checkService(): Promise<boolean> {
		const response = await eda.sys_ClientUrl.request(
			`http://${this.host}:${this.port}/test`
		);
		return response.ok;
	}

	// 发送 Gerber 分析请求
	async analyzeGerber(gerberBlob: Blob, configJson: string): Promise<any> {
		const formData = new FormData();
		formData.append('gerber', gerberBlob, 'gerber.zip');
		formData.append('config', configJson);

		const response = await eda.sys_ClientUrl.request(
			`http://${this.host}:${this.port}/analyze-gerber`,
			'POST',
			formData
		);
		return await response.json();
	}
}
```

### 4.5 display.ts - 结果展示

**职责**: 展示求解结果

```typescript
export class ResultDisplay {
	// 清理 Storage
	static cleanupStorage(): void {
		eda.sys_Storage.setExtensionUserConfig('pdn-results', '');
		eda.sys_Storage.setExtensionUserConfig('pdn-results-images', '');
	}

	// 展示结果集
	showResultSet(resultSet, layerNames): Promise<'close' | 'reanalyze'> {
		// 1. 将数据写入 Storage
		// 2. 订阅 MessageBus 消息
		// 3. 打开 IFrame
		// 4. 等待用户操作
	}
}
```

**数据传递方式**:

1. **Storage**: 用于传递大数据（求解结果）
2. **MessageBus**: 用于消息通信（数据就绪、关闭、重新分析）

---

## 5. 数据流程

### 5.1 完整数据流

```
┌────────────────────────────────────────────────────────────────────┐
│                         用户触发分析                               │
└────────────────────────────┬───────────────────────────────────────┘
                             │
        ┌────────────────────┴────────────────────┐
        │                                         │
┌───────▼─────────┐                     ┌────────▼────────┐
│ extract.ts      │                     │ api.ts          │
│ 提取 PCB 数据    │                     │ 检查后端服务     │
└───────┬─────────┘                     └────────┬────────┘
        │                                         │
        │ EasyEDA_PcbData                         │
        │ (tracks, vias, pads, layerNames)        │
        │                                         │
        └────────────────────┬────────────────────┘
                             │
        ┌────────────────────┴────────────────────┐
        │                                             │
┌───────▼─────────┐                     ┌──────────▼────────┐
│ config.html     │                     │ convert.ts        │
│ 用户配置界面     │                     │ 构建配置           │
└───────┬───��─────┘                     └──────────┬────────┘
        │                                         │
        │ PdnConfig                                │
        │ (rails, sources, loads, layerCuThickness) │
        │                                         │
        └────────────────────┬────────────────────┘
                             │
                    ┌────────▼────────┐
                    │  Gerber ZIP     │
                    │  + Config JSON  │
                    └────────┬────────┘
                             │
        ┌────────────────────┴────────────────────┐
        │                                             │
┌───────▼─────────────────────────────────────┐   │
│ main.py (Python)                              │   │
│                                               │   │
│ 1. _gerber_to_shapely()                      │   │
│    └─> Layer 对象 (Shapely 几何)             │   │
│                                               │   │
│ 2. _extract_board_outline()                  │   │
│    └─> 板框裁剪                              │   │
│                                               │   │
│ 3. _compute_coordinate_transform()          │   │
│    └─> 坐标变换                              │   │
│                                               │   │
│ 4. _punch_via_holes()                        │   │
│    └─> 过孔打孔                              │   │
│                                               │   │
│ 5. _build_user_networks()                    │   │
│    └─> Network (VS + CS)                     │   │
└───────┬─────────────────────────────────────┘   │
        │                                         │
        │ Problem                                  │
        │ (layers, networks)                       │
        │                                         │
        └────────────────────┬────────────────────┘
                             │
        ┌────────────────────┴────────────────────┐
        │                                             │
┌───────▼─────────────────────────────────────┐   │
│ solver.py                                     │   │
│                                               │   │
│ 1. compute_connectivity()                    │   │
│    └─> ConnectivityGraph                      │   │
│                                               │   │
│ 2. generate_meshes_for_problem()             │   │
│    └─> Mesh[] (三角形网格)                    │   │
│                                               │   │
│ 3. NodeIndexer.create()                       │   │
│    └─> 全局节点索引                           │   │
│                                               │   │
│ 4. assemble_system()                          │   │
│    └─> L, r (拉普拉斯矩阵 + 右端项)           │   │
│                                               │   │
│ 5. solve_system()                             │   │
│    └─> v (电压向量)                           │   │
└───────┬─────────────────────────────────────┘   │
        │                                         │
        │ Solution                                │
        │ (layer_solutions, solver_info)          │
        │                                         │
        └────────────────────┬────────────────────┘
                             │
                    ┌────────▼────────┐
                    │  JSON Response  │
                    └────────┬────────┘
                             │
        ┌────────────────────┴────────────────────┐
        │                                             │
┌───────▼─────────┐                     ┌──────────▼────────┐
│ convert.ts      │                     │ display.ts         │
│ 反序列化结果     │                     │ 展示结果            │
└───────────────────┘                     └──────────┬────────┘
                                                     │
        ┌────────────────────────────────────────────┴────────┐
        │                                                     │
┌───────▼─────────┐                                 ┌────────▼────────┐
│ Storage         │                                 │ results.html    │
│ 存储结果数据     │                                 │ WebGL 可视化     │
└───────────────────┘                                 └─────────────────┘
```

### 5.2 数据格式转换

| 阶段 | 数据格式 | 内容 |
|------|----------|------|
| EasyEDA → extract.ts | EasyEDA API 对象 | 原始 PCB 数据 |
| extract.ts → config.html | TypeScript 对象 | 显示数据 |
| config.html → convert.ts | PdnConfig | 用户配置 |
| convert.ts → main.py | JSON (Gerber ZIP + Config) | 分析请求 |
| main.py → solver.py | Problem 对象 | FEM 问题定义 |
| solver.py → main.py | Solution 对象 | 求解结果 |
| main.py → convert.ts | JSON (layer_solutions) | 序列化结果 |
| convert.ts → display.ts | SolutionData | 显示用数据 |
| display.ts → Storage | JSON 字符串 | 持久化数据 |
| Storage → results.html | JSON 对象 | WebGL 渲染 |

---

## 6. FEM 求解原理

### 6.1 数学模型

PCB 的 PDN 分析可建模为电阻网络上的电压分布问题：

**拉普拉斯方程**:
```
∇ · (σ ∇V) = 0
```

其中：
- `V` 是电压（未知量）
- `σ` 是电导率（由铜厚度决定）

**边界条件**:
- 电压源: `V = V0`
- 电流源: `I = I0`

### 6.2 离散化（有限元）

**三角网格上的拉普拉斯算子**:

```python
def laplace_operator(mesh: Mesh) -> scipy.sparse.coo_matrix:
    """
    余切拉普拉斯算子
    L_ij = Σ (cot(α) + cot(β)) / 2
    """
    N = len(mesh.vertices)
    row_is, col_is, values = [], [], []
    diagonal_entries = np.zeros(N)

    for i, vertex_i in enumerate(mesh.vertices):
        for edge in vertex_i.orbit():
            ratio = edge.cotan()  # cot(α) + cot(β)
            vertex_k = edge.twin.origin
            k = mesh.vertices.to_index(vertex_k)

            row_is.append(i)
            col_is.append(k)
            values.append(ratio)
            diagonal_entries[i] -= ratio

    # 插入对角线
    for i, val in enumerate(diagonal_entries):
        row_is.append(i)
        col_is.append(i)
        values.append(val)

    return scipy.sparse.coo_matrix((values, (row_is, col_is)), shape=(N, N))
```

### 6.3 系统组装

**线性方程组**:
```
L · v = r
```

其中：
- `L` 是拉普拉斯矩阵（N×N 稀疏矩阵）
- `v` 是电压向量（N×1）
- `r` 是右端项（电流源）

**添加电压源**:

电压源引入额外的电流变量 `I_v`：
```
V_p - V_n = voltage  → 新增一行
I_p += I_v           → KCL
I_n -= I_v           → KCL
```

### 6.4 求解流程

```
1. compute_connectivity(prob)
   └─> 构建连通性图，找出与电源网络相连的铜层

2. generate_meshes_for_problem(prob)
   └─> 对每个相连的铜层生成三角形网格

3. VertexIndexer.create(meshes)
   └─> 为所有网格顶点分配全局索引

4. NodeIndexer.create(prob, meshes, vindex)
   └─> 为网络节点分配索引

5. assemble_system(prob, meshes, ...)
   └─> 组装 L 和 r

6. solve_system(L, r)
   └─> scipy.sparse.linalg.spsolve(L, r)
```

### 6.5 网格生成 (mesh_pure.py)

**半边数据结构**:

```python
@dataclass(eq=False, repr=False)
class HalfEdge:
    origin: Vertex      # 起始顶点
    twin: HalfEdge     # 对应的半边（共享边）
    next: HalfEdge      # 下一条半边
    prev: HalfEdge      # 前一条半边
    face: Face          # 所属面
    i: IndexType        # 索引
```

**余切值计算**:

```python
def cotan(self) -> float:
    """
    计算半边的余切值
    cot(α) + cot(β)
    """
    ratio = 0.
    for other in [self.next.next, self.twin.next.next]:
        vi = vertex_i.p - other.origin.p
        vk = vertex_k.p - other.origin.p
        cross = vi ^ vk  # 叉积
        ratio += abs(vi.dot(vk) / cross) / 2
    return ratio
```

**网格化算法**:

```python
class Mesher:
    @dataclass
    class Config:
        minimum_angle: float = 20.0
        maximum_size: float = 1.2  # mm
        variable_density_min_distance: float = 0.5
        variable_density_max_distance: float = 5.0

    def poly_to_mesh(self, poly, seed_points) -> Mesh:
        # 1. 清理多边形 (buffer(0))
        # 2. 简化边界 (simplify)
        # 3. CDT 三角剖分 或 Delaunay + 裁剪
        # 4. 网格细化 (去除薄三角形)
        return mesh
```

---

## 7. 类型系统

### 7.1 TypeScript 类型定义 (types.ts)

**EasyEDA 原始数据类型**:

```typescript
export interface EasyEDA_Track {
	net: string;
	x1: number;
	y1: number;
	x2: number;
	y2: number;
	width: number; // mil
	layer: number;
}

export interface EasyEDA_Via {
	net: string;
	x: number;
	y: number;
	diameter: number;
	hole_diameter: number;
	via_type?: string; // "Blind: Top-Layer2" | "Buried: Layer2-Layer5"
}

export interface EasyEDA_Pad {
	net: string;
	x: number;
	y: number;
	pad_number: string;
	width: number;
	height: number;
	hole_diameter: number;
	layer?: number; // undefined = THT (通孔焊盘)
	ref_des?: string;
}
```

**配置类型**:

```typescript
export interface PdnConfig {
	rails: PdnRailConfig[];
	layerCuThickness: Record<number, number>;
	tempRise?: number;
}

export interface PdnRailConfig {
	net: string;
	voltage: number;
	sources: PdnSourceConfig[];
	loads: PdnLoadConfig[];
	gnd_net?: string;
}
```

**求解结果类型**:

```typescript
export interface SolutionData {
	layerSolutions: LayerSolutionData[];
	solverInfo: SolverInfoData;
	diagnostics?: string[];
	currentWarnings?: CurrentCheckWarning[];
}

export interface LayerSolutionData {
	layerName: string;
	meshes: MeshData[];
	disconnectedMeshes: MeshData[];
}
```

### 7.2 Python 类型定义 (problem.py)

```python
@dataclass(frozen=True)
class Layer:
    shape: shapely.geometry.MultiPolygon
    name: str
    conductance: float  # Siemens = conductivity * thickness
    geoms: tuple[shapely.geometry.Polygon, ...]

@dataclass(frozen=True)
class Connection:
    layer: Layer
    point: shapely.geometry.Point
    node_id: NodeID

@dataclass(frozen=True)
class VoltageSource(BaseLumped):
    p: NodeID
    n: NodeID
    voltage: float

    @property
    def extra_variable_count(self) -> int:
        return 1  # 引入电流变量

@dataclass(frozen=True)
class CurrentSource(BaseLumped):
    f: NodeID  # from (电流流出)
    t: NodeID  # to (电流流入)
    current: float
```

---

## 8. API 接口

### 8.1 后端 API (FastAPI)

**测试端点**:
```
GET /test
返回: "OK"
```

**Gerber 分析端点**:
```
POST /analyze-gerber
Content-Type: multipart/form-data

Request:
  - gerber: ZIP file (Gerber 文件)
  - config: JSON string (配置)

Response (JSON):
{
  "success": true,
  "layer_solutions": [...],
  "solver_info": {
    "ground_node_current": float,
    "residual_norm": float
  },
  "connection_points": {...},
  "layer_boundaries": {...},
  "diagnostics": [...],
  "current_warnings": [...]
}
```

### 8.2 前端 API 调用

```typescript
// api.ts
async analyzeGerber(gerberBlob: Blob, configJson: string): Promise<any> {
  const formData = new FormData();
  formData.append('gerber', gerberBlob, 'gerber.zip');
  formData.append('config', configJson);

  const response = await eda.sys_ClientUrl.request(
    `http://${this.host}:${this.port}/analyze-gerber`,
    'POST',
    formData
  );

  if (!response.ok) {
    throw new Error(`HTTP ${response.status}: ${await response.text()}`);
  }

  return await response.json();
}
```

---

## 9. UI 组件

### 9.1 config.html - 配置界面

**功能**:
- 显示可用网络和焊盘
- 配置电源轨道（电压源、电流负载）
- 设置层铜厚度
- 设置温升参数

**数据流**:
```
MessageBus: 'pdn-config-ready'
  → 发布 'pdn-config-data' (padsByNet, layerNames)

MessageBus: 'pdn-config-result'
  → 接收用户配置
```

### 9.2 results.html - 结果展示

**功能**:
- WebGL 渲染电压/功率密度热力图
- 层切换
- 显示过孔/焊叠标记
- 显示诊断信息

**数据传递**:
```
Storage: 'pdn-results' (JSON 字符串)
MessageBus: 'pdn-results-ready'
  → 发布 'pdn-results-data'
```

### 9.3 service-check.html - 服务检查

**功能**:
- 检查 Python 后端是否运行
- 显示服务状态
- 提供启动指引

---

## 10. 配置与构建

### 10.1 扩展配置 (extension.json)

```json
{
	"name": "paden-integration",
	"displayName": "PADEN仿真",
	"version": "1.0.8",
	"categories": "PCB",
	"keywords": ["PDN", "Power Analysis", "Simulation"],
	"activationEvents": {
		"onEditorPcb": true
	},
	"headerMenus": {
		"pcb": [{
			"id": "PDN-Analysis",
			"title": "PADEN仿真",
			"menuItems": [
				{ "id": "RunPDNAnalysis", "title": "运行PADEN仿真..." },
				{ "id": "About", "title": "关于..." }
			]
		}]
	}
}
```

### 10.2 构建配置

**esbuild.prod.ts**:
```typescript
esbuild.build({
	entryPoints: ['./src/index.ts'],
	bundle: true,
	platform: 'browser',
	target: 'es2020',
	format: 'iife',
	outfile: './dist/index.js',
	external: ['eda'],
});
```

### 10.3 NPM 脚本

```json
{
	"scripts": {
		"compile": "rimraf ./dist/ && ts-node ./config/esbuild.prod.ts",
		"lint": "eslint",
		"fix": "eslint --fix",
		"build": "npm run compile && ts-node ./build/packaged.ts"
	}
}
```

### 10.4 Python 后端启动

**start-paden-windows.bat**:
```batch
@echo off
REM 1. 拉取最新代码
REM 2. 安装依赖
REM 3. 启动服务
python -m uvicorn main:app --host localhost --port 5000
```

---

## 附录

### A. 坐标系统

| 坐标系 | 单位 | 原点 | Y方向 |
|--------|------|------|-------|
| EasyEDA | mil | 左下角 | 向下 |
| Gerber | mm | 左下角 | 向下 |
| 显示 | mm | 中心 | 向上 |

**转换公式**:
```
x_gerber = x_easyeda * 0.0254 + offset_x
y_gerber = y_easyeda * 0.0254 + offset_y
```

### B. 电导率计算

```
conductance = conductivity * thickness

铜电导率: σ = 5.95e4 S/mm
铜厚度: t = 0.035 mm (1 oz)

典型电导率: G = 5.95e4 * 0.035 ≈ 2082.5 S
```

### C. 网格质量指标

| 指标 | 公式 | 目标值 |
|------|------|--------|
| 最小角度 | min(∠₁, ∠₂, ∠₃) | ≥ 20° |
| 长宽比 | longest_edge / shortest_edge | ≤ 5 |
| 面积 | 0.5 × ‖(v₂-v₁) × (v₃-v₁)‖ | > 1e-10 mm² |

---

**文档结束**

> 本文档由代码分析工具自动生成，内容基于源代码静态分析。
