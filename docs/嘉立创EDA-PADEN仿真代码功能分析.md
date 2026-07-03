# PADEN 仿真扩展 - 代码详细分析文档

## 项目概述

**项目名称**: PADEN 仿真 (eext-paden-integration)
**版本**: 1.0.8
**类型**: 嘉立创EDA & EasyEDA 专业版扩展
**分类**: PCB 电源分配网络 (PDN) 分析工具

### 核心功能

通过与 PADEN 桥接实现 PCB 电源分配网络的 DC 压降和电流分布分析：

1. 从 EasyEDA PCB 提取走线、过孔、焊盘、铺铜数据
2. 用户配置电源轨道（电压源、电流负载）
3. 通过 Gerber 文件获取几何信息
4. 调用本地 Python 后端进行 FEM (有限元法) 求解
5. WebGL 可视化电压分布和功率密度热力图
6. 多网络分析支持（N+1 次求解：1 次合并 + N 次单独）

---

## 架构设计

### 系统架构图

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                        EasyEDA Pro PCB 编辑器                                 │
│                              (扩展宿主)                                         │
└─────────────────────────────────────────────────────────────────────────────┘
                                            │
                                            ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                           TypeScript 前端扩展                                  │
│  ┌──────────────┐      ┌──────────────┐      ┌──────────────┐             │
│  │  extract.ts  │─────▶│  convert.ts  │─────▶│    api.ts     │             │
│  │  数据提取      │      │  数据转换      │      │  HTTP 通信     │             │
│  └──────────────┘      └──────────────┘      └──────────────┘             │
│         │                      │                      │                     │
│         │                      ▼                      │                     │
│         │              ┌──────────────┐              │                     │
│         │              │  types.ts    │              │                     │
│         │              │  类型定义      │              │                     │
│         │              └──────────────┘              │                     │
│         │                                            │                     │
│         ▼                                            ▼                     │
│  ┌──────────────┐                            ┌──────────────┐              │
│  │  index.ts    │                            │ display.ts    │              │
│  │  主流程编排   │                            │ 结果展示       │              │
│  └──────────────┘                            └──────────────┘              │
│                                                                               │
│  ┌──────────────┐      ┌──────────────┐      ┌──────────────┐               │
│  │config.html   │      │results.html  │      │service-check │               │
│  │配置界面       │      │可视化界面     │      │  .html       │               │
│  └──────────────┘      └──────────────┘      └──────────────┘               │
└─────────────────────────────────────────────────────────────────────────────┘
                                            │
                    ┌───────────────────────┼───────────────────────┐
                    │                       │                       │
                    ▼                       ▼                       ▼
        ┌───────────────────┐   ┌───────────────────┐   ┌───────────────────┐
        │   Gerber ZIP     │   │   配置 JSON        │   │  HTTP Request      │
        │   (几何数据)      │   │   (声明式配置)      │   │  (multipart)       │
        └───────────────────┘   └───────────────────┘   └───────────────────┘
                    │                       │                       │
                    └───────────────────────┼───────────────────────┘
                                            ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                            Python 后端服务                                      │
│                         (FastAPI + localhost:5000)                             │
│  ┌──────────────┐      ┌──────────────┐      ┌──────────────┐             │
│  │   main.py    │─────▶│  solver.py   │─────▶│  mesh_pure   │             │
│  │  服务入口     │      │  FEM 求解器    │      │  网格数据结构   │             │
│  └──────────────┘      └──────────────┘      └──────────────┘             │
│         │                                                                     │
│         ▼                                                                     │
│  ┌──────────────┐      ┌──────────────┐                                       │
│  │ problem.py   │      │ calculation  │                                       │
│  │ 问题定义      │      │ 电流容量计算   │                                       │
│  └──────────────┘      └──────────────┘                                       │
└─────────────────────────────────────────────────────────────────────────────┘
                                            │
                                            ▼
                              ┌─────────────────────┐
                              │   JSON Response      │
                              │   - layer_solutions  │
                              │   - potentials       │
                              │   - power_densities  │
                              │   - current_warnings│
                              └─────────────────────┘
```

### 数据流图

```
用户操作 (EasyEDA)
    │
    ▼
1. 提取 PCB 数据
   - 走线 (EasyEDA_Track)
   - 过孔 (EasyEDA_Via)  
   - 焊盘 (EasyEDA_Pad)
   - 层名 (layerNames)
   │
   ▼
2. 获取 Gerber 文件
   - 几何形状
   - 层边界
   │
   ▼
3. 配置分析参数
   - 电源轨道 (PdnRailConfig)
   - 电压源 (PdnSourceConfig)
   - 电流负载 (PdnLoadConfig)
   │
   ▼
4. 构建 Gerber 配置
   - 层配置 (导电率、厚度)
   - 过孔规格 (位置、直径、连接层)
   - 焊盘规格 (位置、网络)
   - 电源/负载声明
   │
   ▼
5. 发送到 Python 后端
   POST /analyze-gerber
   - FormData: gerber.zip + config.json
   │
   ▼
6. Python 处理
   - 解析 Gerber (PyGerber)
   - 构建 Shapely 几何
   - 生成网格 (Delaunay 三角剖分)
   - 求解 FEM 方程 (Scipy 稀疏矩阵)
   - 电流容量检查 (IPC-2221)
   │
   ▼
7. 返回结果
   - 层求解结果 (电压分布)
   - 功率密度
   - 求解器信息
   - 电流警告
   │
   ▼
8. 可视化展示
   - WebGL 热力图
   - 网络切换
   - 层切换
```

---

## 核心模块详解

### 1. src/index.ts - 主流程编排

**职责**: 整个 PDN 分析流程的入口和编排器

**核心函数**:

#### `runPdnAnalysis()` - 主分析流程

```typescript
async function runPdnAnalysis(): Promise<void>
```

**执行流程**:

```
开始
  │
  ▼
┌─────────────────────────────────────┐
│ 1. 清理旧存储数据                    │
│    ResultDisplay.cleanupStorage()   │
└─────────────────────────────────────┘
  │
  ▼
┌─────────────────────────────────────┐
│ 2. 检查后端服务状态                  │
│    - 如果服务未运行                   │
│      → 显示 service-check.html       │
│      → 等待用户启动服务               │
└─────────────────────────────────────┘
  │
  ▼
┌─────────────────────────────────────┐
│ 3. 并行执行:                         │
│    - 检查服务                         │
│    - 提取 PCB 数据                    │
│    (PcbExtractor.extractNetworkInfo) │
└─────────────────────────────────────┘
  │
  ▼
┌─────────────────────────────────────┐
│ 4. 验证 PCB 数据                     │
│    - 检查是否有焊盘/过孔              │
│    - 如无数据 → 显示警告并退出        │
└─────────────────────────────────────┘
  │
  ▼
┌─────────────────────────────────────┐
│ 5. 配置循环 (while true):            │
│    │                                 │
│    ├─► 打开配置面板 (config.html)     │
│    │   - 用户选择电源网络              │
│    │   - 设置电压源和负载              │
│    │   - 配置层铜厚度                  │
│    │                                  │
│    ├─► 获取 Gerber 文件               │
│    │   - eda.pcb_ManufactureData      │
│    │     .getGerberFile()             │
│    │                                  │
│    ├─► 多轮分析循环:                   │
│    │   │                              │
│    │   ├─ 轮次 1: 合并分析             │
│    │   │   (所有电源网络一起)          │
│    │   │                              │
│    │   └─ 轮次 2..N+1: 单独分析        │
│    │       (每个电源网络单独)          │
│    │                                  │
│    ├─► 显示分析中对话框                │
│    │   (analyzing.html)               │
│    │                                  │
│    ├─► 执行分析:                       │
│    │   - buildGerberConfig()          │
│    │   - api.analyzeGerber()          │
│    │   - deserializeSolution()        │
│    │                                  │
│    ├─► 显示结果                        │
│    │   (results.html)                 │
│    │                                  │
│    └─► 用户选择:                       │
│        - 'close': 退出                │
│        - 'reanalyze': 返回配置步骤    │
└─────────────────────────────────────┘
```

**关键特性**:

1. **N+1 分析策略**: 当配置多个电源网络时，执行 1 次合并分析 + N 次单独分析
2. **内存管理**: 及时释放大对象（Gerber Blob）
3. **错误恢复**: 配置错误时返回配置界面而非直接退出
4. **进度反馈**: 通过进度条显示各阶段进度

#### 其他导出函数

| 函数 | 功能 |
|------|------|
| `showResults()` | 显示已缓存的分析结果 |
| `about()` | 显示扩展信息对话框 |
| `activate()` | 扩展激活钩子（当前为空实现） |

---

### 2. src/extract.ts - PCB 数据提取

**职责**: 从 EasyEDA PCB 中提取原始数据

**核心类**: `PcbExtractor`

#### `extractNetworkInfo()` - 数据提取主函数

```typescript
async extractNetworkInfo(onProgress?: (percent: number) => void): Promise<EasyEDA_PcbData>
```

**提取内容**:

| 数据类型 | API 调用 | 用途 |
|---------|---------|------|
| 走线 (Tracks) | `pcb_PrimitiveLine.getAll()` 等 | 电流容量检查 |
| 过孔 (Vias) | `pcb_PrimitiveVia.getAll()` | 层间连接建模 |
| 焊盘 (Pads) | `pcb_PrimitivePad.getAll()` | 电源/负载位置 |
| 层名 (LayerNames) | `pcb_Layer.getAllLayers()` | 层配置 |

**提取流程**:

```
开始
  │
  ▼
┌─────────────────────────────────────┐
│ 1. 提取层信息                        │
│    - 获取所有铜层                    │
│    - 过滤铜层 ID (1, 2, 15-44)      │
└─────────────────────────────────────┘
  │
  ▼
┌─────────────────────────────────────┐
│ 2. 并行提取基础数据                   │
│    - 过孔 (bulkVias)                 │
│    - 焊盘 (bulkPads)                 │
│    - 走线 (bulkTracks) - 多API尝试    │
└─────────────────────────────────────┘
  │
  ▼
┌─────────────────────────────────────┐
│ 3. 处理过孔数据                      │
│    - 提取网络、位置、直径             │
│    - 提取盲孔/埋孔类型                │
│    - 转换为内部格式                   │
└─────────────────────────────────────┘
  │
  ▼
┌─────────────────────────────────────┐
│ 4. 处理走线数据                      │
│    - 提取网络、位置、宽度、层         │
│    - 单位保持 mil                    │
│    - 用于电流容量检查                 │
└─────────────────────────────────────┘
  │
  ▼
┌─────────────────────────────────────┐
│ 5. 处理焊盘数据 (Phase 2)            │
│    - 获取所有器件                     │
│    - 并行获取器件引脚 (并发控制=8)     │
│    - 合并基础焊盘和器件焊盘            │
│    - 去重 (基于位置+网络)              │
└─────────────────────────────────────┘
  │
  ▼
┌─────────────────────────────────────┐
│ 6. 返回完整数据                       │
│    - tracks: 走线数组                │
│    - vias: 过孔数组                  │
│    - pads: 焊盘数组                  │
│    - layerNames: 层名映射             │
│    - outerLayerIds: 外层ID集合        │
└─────────────────────────────────────┘
```

**关键方法**:

| 方法 | 功能 |
|------|------|
| `extractTrack()` | 提取单条走线数据 |
| `extractVia()` | 提取单个过孔，包含盲孔/埋孔类型 |
| `extractPad()` | 提取单个焊盘，支持回退网络 |
| `getNetFromPrimitive()` | 从图元获取网络名 |
| `parallelMap()` | 并发控制器，限制同时API调用数 |

**诊断日志**: `extractor.diagnostics` 数组记录提取过程中的调试信息

---

### 3. src/convert.ts - 数据转换

**职责**: 将 EasyEDA 数据转换为后端格式 + 求解结果反序列化

**核心类**: `PcbDataConverter`

#### `buildGerberConfig()` - 构建 Gerber 配置

```typescript
buildGerberConfig(easyedaData: EasyEDA_PcbData, config: PdnConfig): Record<string, any>
```

**输出配置结构**:

```json
{
  "layers": [
    {
      "name": "Top Layer",
      "conductance": 2082.5,
      "thickness": 0.035,
      "layer_id": 1
    }
  ],
  "vias": [
    {
      "x": 10.5,
      "y": 20.3,
      "hole_diameter": 0.3,
      "diameter": 0.6,
      "net": "VCC",
      "layer_names": ["Top Layer", "Bottom Layer"],
      "via_type": "Blind: Top-Layer2"
    }
  ],
  "pads": [
    {
      "ref_des": "U1",
      "pad_number": "1",
      "net": "VCC",
      "x": 15.2,
      "y": 25.8,
      "layer": "Top Layer",
      "is_tht": false
    }
  ],
  "sources": [
    {
      "net": "VCC",
      "voltage": 3.3,
      "gnd_net": "GND",
      "pads": [...],
      "gnd_pads": [...]
    }
  ],
  "loads": [
    {
      "net": "VCC",
      "current": 0.5,
      "gnd_net": "GND",
      "pads": [...],
      "gnd_pads": [...]
    }
  ],
  "tracks": [...],
  "easyeda_bounds": {
    "minX": 0,
    "minY": 0,
    "maxX": 100,
    "maxY": 80
  },
  "temp_rise": 10
}
```

**转换要点**:

1. **坐标转换**: EasyEDA mil → mm (× 0.0254)
2. **层排序**: 物理 stacking order (Top → Inner → Bottom)
3. **过孔层解析**: 解析盲孔/埋孔类型确定连接范围
4. **边界计算**: 使用所有焊盘/过孔计算边界（不仅分析网络）

#### `deserializeSolution()` - 反序列化求解结果

```typescript
deserializeSolution(solution: SerializedSolution, layerNames: string[]): SolutionData
```

将后端返回的序列化数据转换为前端显示格式。

---

### 4. src/api.ts - HTTP 通信

**职责**: 与 Python 后端服务的 HTTP 通信

**核心类**: `PdnApiClient`

| 方法 | 功能 |
|------|------|
| `checkService()` | 检测后端服务是否运行 |
| `analyzeGerber()` | 发送 Gerber 分析请求 |

**通信协议**:

- **检查服务**: `GET http://localhost:5000/test`
- **分析请求**: `POST http://localhost:5000/analyze-gerber`
  - Content-Type: `multipart/form-data`
  - 字段: `gerber` (文件), `config` (JSON字符串)

---

### 5. src/display.ts - 结果展示

**职责**: 管理结果展示界面的打开和数据传递

**核心类**: `ResultDisplay`

| 方法 | 功能 |
|------|------|
| `cleanupStorage()` | 清理 Storage 中的旧数据 |
| `showResultSet()` | 展示多结果集，返回用户操作 |

**数据传递方式** (双保险):

1. **Storage**: `eda.sys_Storage.setExtensionUserConfig('pdn-results', jsonStr)`
2. **MessageBus**: 订阅 `padne-results-ready` 后发送数据

---

### 6. src/types.ts - 类型定义

**职责**: 定义整个项目的 TypeScript 类型

**主要类型分类**:

| 分类 | 类型示例 |
|------|---------|
| 几何类型 | `Point`, `MeshTriangle` |
| EasyEDA 原始 | `EasyEDA_Track`, `EasyEDA_Via`, `EasyEDA_Pad` |
| 序列化格式 | `SerializedPoint`, `SerializedMesh` |
| 求解结果 | `SolutionData`, `LayerSolutionData` |
| 用户配置 | `PdnConfig`, `PdnRailConfig` |
| 可视化 | `AnalysisImages`, `PcbContextData` |
| 电流检查 | `CurrentCheckWarning` |

---

## Python 后端详解

### paden-service/main.py - 服务入口

**职责**: FastAPI 服务器，处理 Gerber 分析请求

**主要端点**:

| 端点 | 方法 | 功能 |
|------|------|------|
| `/test` | GET | 健康检查 |
| `/analyze-gerber` | POST | Gerber 分析主入口 |

**分析流程**:

```
接收请求 (Gerber ZIP + Config JSON)
  │
  ▼
┌─────────────────────────────────────┐
│ 1. 解析 Gerber 文件                 │
│    - 读取 ZIP 中的 .gt1, .gt2 等    │
│    - 使用 PyGerber 解析             │
│    - 构建 Shapely 几何               │
└─────────────────────────────────────┘
  │
  ▼
┌─────────────────────────────────────┐
│ 2. 提取板框和边界                    │
│    - 识别板轮廓 (通常在 Bottom 层)   │
│    - 裁剪铜区域到板框内              │
└─────────────────────────────────────┘
  │
  ▼
┌─────────────────────────────────────┐
│ 3. 坐标转换                           │
│    - EasyEDA 坐标系 → Gerber 坐标系  │
│    - 使用 easyeda_bounds 对齐        │
└─────────────────────────────────────┘
  │
  ▼
┌─────────────────────────────────────┐
│ 4. 过孔处理                           │
│    - 在铜层上打过孔抗焊盘孔            │
│    - 构建过孔电阻网络                 │
└─────────────────────────────────────┘
  │
  ▼
┌─────────────────────────────────────┐
│ 5. 构建网络                           │
│    - 电压源 (VoltageSource)          │
│    - 电流负载 (CurrentSource)        │
└─────────────────────────────────────┘
  │
  ▼
┌─────────────────────────────────────┐
│ 6. FEM 求解                          │
│    - solver.solve(problem)           │
│    - 返回电压分布、功率密度           │
└─────────────────────────────────────┘
  │
  ▼
┌─────────────────────────────────────┐
│ 7. 电流容量检查                       │
│    - calculation.check_current_...   │
│    - 基于 IPC-2221 标准              │
└─────────────────────────────────────┘
  │
  ▼
┌─────────────────────────────────────┐
│ 8. 序列化响应                         │
│    - 层求解结果                       │
│    - 求解器信息                       │
│    - 电流警告                         │
│    - 连接点和层边界                   │
└─────────────────────────────────────┘
```

### paden-service/solver.py - FEM 求解器

**职责**: 有限元法求解器

**核心数据结构**:

```python
@dataclass
class Solution:
    problem: problem.Problem
    layer_solutions: list[LayerSolution]
    solver_info: SolverInfo
```

**求解步骤**:

1. **构建连接图**: `ConnectivityGraph.create_from_problem()`
2. **计算连接节点**: 找出与电源/负载相连的网格节点
3. **构建拉普拉斯算子**: `laplace_operator()` - 计算离散拉普拉斯矩阵
4. **组装系统矩阵**: 包含拉普拉斯算子、电压源、电流源
5. **求解线性方程组**: 使用 Scipy 稀疏求解器
6. **计算功率密度**: $P = V \cdot J$ (电压 × 电流密度)

### paden-service/problem.py - 问题定义

**职责**: 定义 FEM 问题的数据结构

**核心类**:

| 类 | 功能 |
|------|------|
| `Layer` | 铜层定义（几何、导电率） |
| `Connection` | 物理连接点（层 + 位置） |
| `Network` | 电气网络（连接 + 元件） |
| `VoltageSource` | 电压源元件 |
| `CurrentSource` | 电流源元件 |
| `Resistor` | 电阻元件（用于过孔建模） |
| `Problem` | 完整问题定义 |

### paden-service/calculation.py - 电流容量计算

**职责**: 基于 IPC-2221 标准的走线电流容量计算

**核心函数**:

| 函数 | 功能 |
|------|------|
| `current_to_width()` | 计算承载指定电流所需线宽 |
| `width_to_current()` | 计算给定线宽的最大电流 |
| `check_current_capacity()` | 检查走线是否满足电流要求 |

**IPC-2221 公式**:

$$
A = \left(\frac{I}{k \cdot \Delta T^B}\right)^{1/C} \\
W = \frac{A}{t_{Cu}}
$$

其中:
- $A$: 截面积 (mil²)
- $I$: 电流 (A)
- $\Delta T$: 温升 (°C)
- $t_{Cu}$: 铜厚 (mil)
- $k$: 层系数 (外层 0.048, 内层 0.024)
- $B = 0.44$
- $C = 0.725$

---

## UI 界面分析

### config.html - 配置界面

**功能**: 用户配置电源轨道、电压源、电流负载

**界面布局**:

```
┌─────────────────────────────────────────────────┐
│  错误提示横幅 (条件显示)                          │
├────────────┬────────────────────────────────────┤
│            │  轨道属性                            │
│  电源网络   │  电压(V): [3.3]                     │
│            │  接地: [自动检测 ▼]                 │
│  [搜索框]   ├────────────────────────────────────┤
│            │  表格区域                            │
│  网络列表   │  角色 | 位号 | 参数 | 电源引脚|GND │
│  - VCC     │  ─────────────────────────────────  │
│  - GND     │  🔴电源 | U1  | 3.3V | 1,4    | -   │
│  - +5V     │  🔵负载 | U2  | 0.5A | 5      |6,7  │
│            │                                     │
│            │  [+电压源] [+负载] [-移除]           │
├────────────┴────────────────────────────────────┤
│  层铜厚度配置 (每层独立输入框)                     │
│  温升限制: [10] °C                                │
│                            [取消] [开始分析 ▶]     │
└─────────────────────────────────────────────────┘
```

**数据流**:

1. **接收数据**: MessageBus 订阅 `pdn-config-ready`
2. **发送配置**: MessageBus 发布 `pdn-config-result`
3. **取消**: MessageBus 发布 `pdn-config-cancel`

### results.html - 可视化界面

**功能**: WebGL 可视化分析结果

**核心功能**:

1. **热力图渲染**: 电压/功率密度颜色映射
2. **网络切换**: 多个电源网络的结果切换
3. **层切换**: 不同铜层的结果切换
4. **过孔标记**: 显示过孔位置和连接关系
5. **网格边显示**: 可选显示三角网格边
6. **叠加显示**: 显示非分析网络的走线作为上下文

**渲染管线**:

```
数据接收 (Storage/MessageBus)
  │
  ▼
┌─────────────────────────────────────┐
│ 1. 解析结果数据                       │
│    - 网格顶点、三角形                 │
│    - 电势、功率密度                   │
└─────────────────────────────────────┘
  │
  ▼
┌─────────────────────────────────────┐
│ 2. WebGL 初始化                       │
│    - 创建着色器程序                   │
│    - 设置缓冲区                       │
└─────────────────────────────────────┘
  │
  ▼
┌─────────────────────────────────────┐
│ 3. 每帧渲染                           │
│    - 更新颜色缓冲区 (基于当前值模式)   │
│    - 绘制三角形                       │
│    - 绘制过孔标记                     │
│    - 绘制上下文走线                    │
└─────────────────────────────────────┘
  │
  ▼
┌─────────────────────────────────────┐
│ 4. 用户交互                           │
│    - 鼠标移动显示数值                  │
│    - 图例控制                         │
└─────────────────────────────────────┘
```

### service-check.html - 服务检查

**功能**: 检测和引导用户启动 Python 后端服务

**流程**:

```
打开对话框
  │
  ▼
┌─────────────────────────────────────┐
│ 1. 尝试连接服务                       │
│    - GET http://localhost:5000/test   │
└─────────────────────────────────────┘
  │
  ▼
┌─────────────────────────────────────┐
│ 2. 显示状态                           │
│    - 服务运行中: 显示 "继续" 按钮      │
│    - 服务未运行: 显示启动指南          │
└─────────────────────────────────────┘
  │
  ▼
┌─────────────────────────────────────┐
│ 3. 用户确认                           │
│    - 发布 "pdn-service-check-success" │
└─────────────────────────────────────┘
```

---

## 技术要点

### 1. 坐标系统

| 坐标系 | 单位 | 用途 | 转换 |
|--------|------|------|------|
| EasyEDA 内部 | mil | PCB 数据存储 | × 0.0254 → mm |
| Gerber | 取决于 FSLA | 几何描述 | 自动检测 |
| 后端计算 | mm | FEM 求解 | - |

### 2. 单位转换常量

```typescript
const MIL_TO_MM = 0.0254;  // 1 mil = 0.0254 mm
```

### 3. 内存管理策略

1. **及时释放大对象**: Gerber Blob 用完立即置 null
2. **数组清空**: `arr.length = 0` 清空大数组
3. **清理订阅**: MessageBus 订阅取消机制

### 4. 并发控制

```typescript
private async parallelMap<T, R>(
  items: T[],
  fn: (item: T) => Promise<R>,
  concurrency: number,  // 并发限制
  onBatchDone?: (done: number) => void,
): Promise<R[]>
```

### 5. 盲孔/埋孔解析

**格式**:
- 盲孔: `"Blind: Top-Layer2"` - 连接顶层到第2层
- 埋孔: `"Buried: Layer2-Layer5"` - 连接第2层到第5层
- 通孔: 无 via_type 字段 - 连接所有层

### 6. 多网络 N+1 分析

**目的**: 提供总体视图和单独网络视图

```
配置: Rails = [VCC_3V3, VCC_5V, VCC_12V]

分析轮次:
1. 合并分析: 所有3个网络一起分析
2. 单独 VCC_3V3
3. 单独 VCC_5V
4. 单独 VCC_12V

结果: 4 个 AnalysisResultEntry
```

---

## 依赖关系图

### 前端依赖

```
src/index.ts
  ├─→ src/extract.ts
  ├─→ src/convert.ts
  ├─→ src/api.ts
  ├─→ src/display.ts
  └─→ src/types.ts

src/convert.ts
  └─→ src/types.ts

src/display.ts
  └─→ src/types.ts

ui/config.html
  └─→ src/types.ts (通过 MessageBus)

ui/results.html
  └─→ src/types.ts (通过 Storage/MessageBus)
```

### 后端依赖

```
paden-service/main.py
  ├─→ paden-service/solver.py
  ├─→ paden-service/problem.py
  ├─→ paden-service/mesh_pure.py
  └─→ paden-service/calculation.py

paden-service/solver.py
  ├─→ paden-service/problem.py
  └─→ paden-service/mesh_pure.py
```

---

## 错误处理

### 前端错误处理

1. **服务未运行**: 显示 service-check.html
2. **配置错误**: 返回配置界面，显示错误消息
3. **求解失败**: 显示诊断日志对话框
4. **矩阵奇异**: 检测 `ground_node_current` 和 `residual_norm`

### 后端错误处理

1. **Gerber 解析失败**: 返回错误消息 + 诊断日志
2. **无有效网络**: 返回 success=false + message
3. **电流超限**: 返回 current_warnings 数组

---

## 性能考虑

1. **并发 API 调用**: parallelMap 限制并发数为 8
2. **大对象管理**: 及时释放 Gerber Blob
3. **网格缓存**: 层几何缓存到 Layer.geoms
4. **增量渲染**: WebGL 只更新必要数据

---

## 开发指南

### 前端开发

```bash
# 安装依赖
npm install

# 编译
npm run compile

# 修复 lint
npm run fix
```

### 后端开发

```bash
# Windows
cd paden-service
start-paden-windows.bat

# Linux
cd paden-service
./start-paden-linux.sh
```

### 调试技巧

1. **前端日志**: 查看 `extractor.diagnostics`
2. **后端日志**: 查看 Python 控制台输出
3. **网络调试**: 检查 `/test` 端点响应

---

## 文件索引

| 文件 | 行数 | 功能描述 |
|------|------|---------|
| src/index.ts | 411 | 主入口、流程编排 |
| src/extract.ts | 749 | PCB 数据提取 |
| src/convert.ts | 313 | 数据转换、Gerber 配置构建 |
| src/api.ts | 55 | HTTP 通信 |
| src/display.ts | 141 | 结果展示管理 |
| src/types.ts | 293 | 类型定义 |
| ui/config.html | ~800 | 配置界面 |
| ui/results.html | ~3500 | 可视化界面 |
| ui/service-check.html | ~400 | 服务检查界面 |
| paden-service/main.py | ~3000 | 后端服务入口 |
| paden-service/solver.py | ~900 | FEM 求解器 |
| paden-service/problem.py | ~185 | 问题定义 |
| paden-service/calculation.py | ~300 | 电流容量计算 |

---

## 总结

这是一个完整的 PCB 电源分配网络分析工具，通过以下技术实现:

1. **前端**: TypeScript + EasyEDA Pro API
2. **后端**: Python + FastAPI + Scipy + Shapely
3. **可视化**: WebGL 自定义渲染
4. **数值方法**: 有限元法 (FEM) 求解拉普拉斯方程
5. **标准遵循**: IPC-2221 电流容量计算

该项目展示了如何在 EDA 工具中集成复杂的数值分析功能，是桌面应用扩展开发的优秀范例。
