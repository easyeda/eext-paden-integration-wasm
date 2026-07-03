# padne 代码功能分析

## 项目概述

**padne** 是一个面向 KiCad 的 **直流电源分配网络 (PDN) 分析工具**。它使用有限元方法 (FEM) 模拟 PCB 铜层上的直流电压降和电流密度���布，帮助识别电阻瓶颈、设计高电流分配网络或实现复杂的加热元件。

核心流程：

```
KiCad 项目文件 (.kicad_pro)
        │
        ▼
┌─────────────────┐
│   kicad.py      │ ← 加载 PCB 几何图形 + 解析原理图���令
└────────┬────────┘
         │  problem.Problem (层几何 + 集总元件网络)
         ▼
┌─────────────────┐
│   solver.py     │ ← 构建全局 FEM 方程组并求解
└────────┬────────┘
         │  solver.Solution (每层网格 + 电���/功率密度)
         ▼
┌──────────┬──────────────┐
│  ui.py   │  paraview.py │ ← GUI 可视化 / VTK 导出
└──────────┴──────────────┘
```

---

## 模块总览

| 模块 | 文件 | 职责 |
|------|------|------|
| `cli` | `padne/cli.py` | 命令行入口，参数解析，调度子命令 |
| `kicad` | `padne/kicad.py` | 加载 KiCad 项目，提取几何/叠层/过孔/指令 |
| `problem` | `padne/problem.py` | 定义问题的数据结构（层、网络、集总元件） |
| `mesh` | `padne/mesh.py` | 三角网格生成与离散微分形式（0/1/2-form） |
| `solver` | `padne/solver.py` | FEM 求解器，构建并求解 Laplace 方程组 |
| `units` | `padne/units.py` | SI 单位前缀解析与格式化 |
| `colormaps` | `padne/colormaps.py` | 颜色映射表（Viridis/Plasma/Inferno��� |
| `ui` | `padne/ui.py` | 基于 PySide6 + OpenGL 的交互式 GUI |
| `paraview` | `padne/paraview.py` | ParaView VTK 格式导出 |
| `tests` | `padne/tests.py` | 类型守卫测试辅助 |

---

## 模块详细分析

---

### 1. `padne/cli.py` — 命令行接口

**功能**：应用程序入口点，定义子命令 `gui`、`solve`、`show`、`paraview`，解析命令行参数并调度对应处理函数。

#### 关键函数

| 函数 | 用途 |
|------|------|
| `main()` | 程序入口，解析参数并分发到对应子命令 |
| `parse_args()` | 定义四个子命令 `gui/solve/show/paraview` 及网格参数 |
| `do_gui(args)` | 加载 KiCad 项目 → 求解 → 启动 GUI |
| `do_solve(args)` | 加载 KiCad 项目 → 求解 → pickle 保存到文件 |
| `do_show(args)` | 从 pickle 文件加载已有解 → 启动 GUI |
| `do_paraview(args)` | 从 pickle 文件加载已有解 → 导出 VTK |
| `add_mesher_args(parser)` | 向参数解析器添加网格配置选项 |
| `mesher_config_from_args(args)` | 从解析结果构造 `Mesher.Config` |
| `setup_logging(debug_mode)` | 配置日志级别 |
| `collect_warnings()` | 上下文管理器，收集运行时警告 |
| `handle_errors(func)` | 装饰器，捕获异常并美化输出 |

#### 执行流程

```
用户运行 padne gui project.kicad_pro
    │
    ▼
parse_args() → 命令 = "gui"
    │
    ▼
do_gui():
    1. kicad.load_kicad_project() → Problem
    2. solver.solve(Problem) → Solution
    3. ui.main(Solution) → 显示 GUI
```

---

### 2. `padne/kicad.py` — KiCad 项目加载器

**功能**：从 KiCad 项目文件加载 PCB 几何、叠层信息、过孔规格和原理图指令，构建 `Problem` 对象。

#### 关键数据结构

| 类 | 用途 |
|----|------|
| `KiCadProject` | 封装 `.kicad_pro` / `.kicad_pcb` / `.kicad_sch` 文件路径 |
| `Stackup` / `StackupItem` | PCB 叠层结构（铜层/介质层，厚度，电导率） |
| `Directive` | 原理图中的 `!padne` 指令 |
| `Endpoint` | 焊盘标识 `DESIGNATOR.PAD`（如 `R1.1`） |
| `ViaSpec` | 过孔规格（位置、钻孔直径、连接层） |
| `PadIndex` | Endpoint → LayerPoint 的映射表 |
| `BaseLumpedSpec` | 集总元件规格的基类 |
| `VoltageSourceSpec` / `CurrentSourceSpec` / `ResistorSpec` / `RegulatorSpec` | 各类集总元件规格 |
| `CopperSpec` | 自定义铜电导率规格 |
| `PlottedGerberLayer` | 从 Gerber 渲染的层几何 |
| `SchemaInstance` | 原理图层级节点 |

#### 关键函数

| 函数 | 用途 |
|------|------|
| `load_kicad_project(pro_file_path)` | **核心入口**：加载整个 KiCad 项目，返回 `Problem` |
| `find_pcbnew_module()` | 查找 KiCad 的 `pcbnew` Python 模块 |
| `extract_stackup_from_kicad_pcb(board)` | 从 PCB 文件解析叠层信息 |
| `extract_via_specs_from_pcb(board)` | 提取所有过孔规格 |
| `extract_tht_pad_specs_from_pcb(board)` | 提取所有通孔焊盘规格 |
| `render_gerbers_from_kicad(board, layer_ids)` | 通过 KiCad 绘制 Gerber → 转换为 Shapely 几何 |
| `gerber_file_to_shapely(gerber_path)` | 将 Gerber 文件转换为 Shapely 多边形 |
| `extract_board_outline(board)` | 提取 PCB 板框轮廓 |
| `clip_layer_with_outline(plotted_layer, outline)` | 用板框裁剪铜层 |
| `punch_via_holes(plotted_layers, via_specs)` | 在铜层几何上冲出过孔孔洞 |
| `process_via_spec(via_spec, layer_dict, stackup)` | 将过孔转换为层间电阻网络 |
| `build_schema_hierarchy(sch_file_path)` | 构建原理图层级结构 |
| `extract_directives_from_hierarchy(schema_instance)` | 从原理图层级中提取所有 `!padne` 指令 |
| `process_directives(directives)` | 将指令分类为集总元件和铜电导率规格 |
| `construct_layer_dict(plotted_layers, stackup)` | 构建层名 → `Layer` 对象的映射 |

#### `load_kicad_project` 核心流程

```
load_kicad_project(pro_file_path):
    1. 加载 PCB 板 → 渲染铜层 Gerber → Shapely 几何
    2. 提取板框 → 裁剪铜层
    3. 解析原理图层级 → 提取 !padne 指令
    4. 提取叠层信息
    5. 提取过孔 + 通孔焊盘 → 冲孔
    6. 构建 PadIndex（SMD 焊盘 + 过孔边界点）
    7. 过孔 → 层间电阻网络
    8. 指令 → 集总元件网络
    9. 返回 Problem(layers, networks)
```

---

### 3. `padne/problem.py` — 问题定义数据结构

**功能**：定义 PDN 仿真问题的不可变数据结构。

#### 数据结构

| 类 | 用途 |
|----|------|
| `Layer` | 单个铜层：几何形状 (`MultiPolygon`) + 名称 + 表面电导率 (S) |
| `NodeID` | 网络节点的唯一标识 |
| `Connection` | 节点与层上点的连接 |
| `Network` | 一个电路网络：包含连接列表和集总元件列表 |
| `Resistor` | 电阻元件 (a, b, resistance) |
| `VoltageSource` | 电压源 (p, n, voltage)，引入额外未知量（电流） |
| `CurrentSource` | 电流源 (f, t, current) |
| `VoltageRegulator` | 压控电流源 (v_p, v_n, s_f, s_t, voltage, gain) |
| `Problem` | 最终问题定义：层列表 + 网络列表 |

#### 元件与方程的对应

- **Resistor**: `R × (V_a - V_b) = 0` → 在系统矩阵中填入 `1/R` 导纳
- **CurrentSource**: `I_f += current, I_t -= current` → 填入右端向量
- **VoltageSource**: 引入额外变量 `I_v`（电流），约束 `V_p - V_n = voltage`
- **VoltageRegulator**: 电压源 + 电流增益，`I_input = gain × I_output`

---

### 4. `padne/mesh.py` — 网格与微分形式

**功能**：定义半边数据结构 (Half-Edge Mesh) 三角网格、离散微分形式 (0-form/1-form/2-form) 和基于 CGAL 的三角剖分器。

#### 基础几何类

| 类 | 用途 |
|----|------|
| `Vector(dx, dy)` | 2D 向量，支持点积、叉积、缩放 |
| `Point(x, y)` | 2D 点，支持距离计算、转 Shapely Point |

#### 网格拓扑类
E:\jlceda-extension\eext-paden-integration\src\extract.ts

| 类 | 用途 |
|----|------|
| `Vertex(p, out, i)` | 顶点：坐标 + 出发半边 + 索引 |
| `HalfEdge(origin, twin, next, prev, face, i)` | 半边：构成网格拓扑核心 |
| `Face(edge, is_boundary, i)` | 面（三角形）：关联一条半边 |
| `Mesh` | 三角网格容器：管理顶点/半边/面/边界 |
| `IndexStore[T]` | 带索引的对象存储器 |

#### Mesh 关键方法

| 方法 | 用途 |
|------|------|
| `Mesh.from_triangle_soup(points, triangles)` | 从三角形汤构建半边网格（含边界检测） |
| `Mesh.make_vertex(p)` | 创建顶点 |
| `Mesh.connect_vertices(v1, v2)` | 获取或创建两顶点间的半边对 |
| `Mesh.euler_characteristic()` | 计算欧拉特征数 `V - E + F` |

#### HalfEdge 关键方法

| 方法 | 用途 |
|------|------|
| `Vertex.orbit()` | 迭代围绕顶点的所有半边（一环邻域） |
| `HalfEdge.walk()` | 沿 `next` 指针遍历半边环 |
| `HalfEdge.cotan()` | 计算 cotangent 权重（FEM 刚度矩阵的关键） |
| `HalfEdge.connect(e1, e2)` | 连接两个半边 (`e1.next = e2, e2.prev = e1`) |

#### Face 关键属性

| 属性 | 用途 |
|------|------|
| `Face.edges` | 迭代面的所有边 |
| `Face.vertices` | 迭代面的所有顶点 |
| `Face.centroid` | 计算面重心 |
| `Face.area` | 用鞋带公式计算面积 |

#### 离散微分形式

| 类 | 定义在 | 用途 |
|----|--------|------|
| `ZeroForm(mesh)` | 顶点 | 标量场（如电压），`values` 为 `numpy` 数组 |
| `OneForm(mesh)` | 半边 | 向量场的线积分，自动保证反对称性 |
| `TwoForm(mesh)` | 面 | 面上的标量场（如功率密度），边界面返回 0 |

**ZeroForm 关键方法**：
- `d()` — 计算外微分（梯度），返回 `OneForm`：`(df)[edge] = f(target) - f(source)`
- 支持加、减、乘、除、取负运算

#### 网格生成器

| 类/方法 | 用途 |
|---------|------|
| `Mesher` | 网格生成器，封装 CGAL 调用 |
| `Mesher.Config` | 网格参数：最小角度、最大尺寸、变密度参数 |
| `Mesher.poly_to_mesh(poly, seed_points)` | 将 Shapely 多边形三角化为 `Mesh` |
| `PolyBoundaryDistanceMap` | CGAL 距离图（用于变密度网格） |
| `MeshingException` | CGAL 网格化失败异常 |

---

### 5. `padne/solver.py` — FEM 求解器

**功能**：构建全局 FEM 方程组 `L × v = r` 并求解，得到 PCB 各层的电压分布。

#### 关键数据结构

| 类 | 用途 |
|----|------|
| `SolverInfo` | 求解器诊断信息（地线电流、残差范数） |
| `LayerSolution` | 单层解：网格列表 + 电势 + 功率密度 + 断连网格 |
| `Solution` | 完整解：Problem + 各层解 + 求解信息 |
| `ConnectivityGraph` | 层间连接图，识别电连通区域 |
| `VertexIndexer` | 网格顶点到全局矩阵索引的映射 |
| `NodeIndexer` | 电路节点到全局矩阵索引的映射 |

#### 关键函数

| 函数 | 用途 |
|------|------|
| `solve(prob, mesher_config)` | **核心入口**：完整求解流程 |
| `construct_strtrees_from_layers(layers)` | 为每层构建 STR-tree 空间索引 |
| `ConnectivityGraph.create_from_problem(prob, strtrees)` | 构建层间连通图 |
| `collect_seed_points(prob, layer)` | 收集层上的种子点（焊盘位置） |
| `generate_meshes_for_problem(prob, mesher, ...)` | 为连通区域生成网格 |
| `generate_disconnected_meshes(prob, ...)` | 为未连通铜区生成简化网格 |
| `laplace_operator(mesh)` | 构建单个网格的 Laplace 算子（cotangent 权重稀疏矩阵） |
| `stamp_network_into_system(network, node_indexer, L, r)` | 将集总元件网络 "盖章" 到系统矩阵 |
| `setup_ground_node(i_gnd, L, r)` | 设置接地节点（约束电压为 0V） |
| `find_best_ground_node_index(prob, node_indexer)` | 找最佳接地节点（最高电压源的负极） |
| `process_mesh_laplace_operators(meshes, conductances, vindex, L)` | 将各网格的 Laplace 算子组装到全局矩阵 |
| `compute_power_density(voltage, conductivity)` | 计算面功率密度 `P = σ|∇V|²` |
| `produce_layer_solutions(...)` | 从解向量重构各层 Solution |
| `network_has_a_dead_terminal(...)` | 检测是否有连接到断连铜区的终端 |

#### `solve()` 核心流程

```
solve(Problem):
    1. 构建连通图 → 找出所有电连通的 (layer_i, geom_i) 对
    2. 为连通区域生成网格（CGAL 三角化）
    3. 为断连铜区生成简化网格
    4. 构建顶点全局索引 (VertexIndexer)
    5. 过滤掉死终端网络
    6. 构建节点全局索引 (NodeIndexer)
    7. 组装全局方程组:
       - 各网格的 Laplace 算子 × 电导率 → 填入 L
       - 各网络的集总元件 → 填入 L 和 r
       - 设置接地节点
    8. scipy.sparse.linalg.spsolve(L, r) → 求解 v
    9. 从 v 重构各层 LayerSolution
   10. 返回 Solution
```

#### 系统矩阵结构

```
全局矩阵 L (N×N), N = 总顶点数 + 内部节点数 + 电压源数 + 1(地线)

┌                        ┐
│  Mesh Laplace (cotan)  │  ← 各网格的 FEM 刚度矩阵
│                        │
│     + Network stamps   │  ← 集总元件（电阻/电流源/电压源）
│                        │
│     + Ground node      │  ← 最后一行/列用于接地约束
└                        ┘
```

---

### 6. `padne/units.py` — SI 单位解析与格式化

**功能**：解析带 SI 前缀的物理量字符串（如 `"100mA"`, `"3.3V"`），以及将数值格式化为人类可读形式。

#### 类

| 类 | 用途 |
|----|------|
| `Value(value, unit)` | 带单位的数值，支持解析和格式化 |

#### 支持的 SI 前缀

`T`(10^12), `G`(10^9), `M`(10^6), `k`(10^3), `m`(10^-3), `μ/u`(10^-6), `n`(10^-9), `p`(10^-12)

#### 关键方法

| 方法 | 用途 | 示例 |
|------|------|------|
| `Value.parse(s)` | 解析字符串为 Value | `"100mA"` → `Value(0.1, "A")` |
| `Value.pretty_format(decimal_places)` | 格式化为可读字符串 | `Value(0.1, "A")` → `"100.0 mA"` |

---

### 7. `padne/colormaps.py` — 颜色映射

**功能**：定义用于可视化的颜色映射表。

#### 预定义颜色映射

| 常量 | 用途 |
|------|------|
| `VIRIDIS` | Viridis 色表（256 色） |
| `PLASMA` | Plasma 色表（256 色），用于电压显示 |
| `INFERNO` | Inferno 色表（256 色），用于功率密度显示 |

#### `UniformColorMap`

- `__call__(v)`: 输入 `[0, 1]` 范围的浮点数，返回 `(r, g, b)` 元组

---

### 8. `padne/ui.py` — 交互式 GUI

**功能**：基于 PySide6 (Qt) + OpenGL 的交互式可视化界面，支持缩放/平移、层切换、模式切换、颜色刻度编辑等。

#### 关键类

| 类 | 用途 |
|----|------|
| `MeshViewer` | 核心 OpenGL 渲染控件，显示网格和电势 |
| `MainWindow` | 主窗口，包含工具栏、状态栏、颜色刻度 |
| `AppToolBar` | 应用工具栏（工具选择、视图、层、模式切换） |
| `ColorScaleWidget` | 颜色刻度尺控件 |
| `EditableValueLabel` | 可双击编辑的数值标签 |
| `ToolManager` | 工具管理器，分发鼠标/键盘事件 |
| `BaseTool` / `PanTool` / `SetMinValueTool` / `SetMaxValueTool` | 交互工具 |
| `ShaderProgram` | OpenGL 着色器程序封装 |
| `RenderedMesh` | 已上传到 GPU 的网格数据（VAO/VBO） |
| `RenderedPoints` | 已上传到 GPU 的点数据 |
| `DeferedDict` | 延迟字典，支持 Future 值（异步网格准备） |
| `BaseSpatialIndex` / `VertexSpatialIndex` / `FaceSpatialIndex` | 空间索引（KD-Tree），用于快速拾取 |

#### 渲染模式

| 模式 | 类 | 显示内容 | 色表 |
|------|-----|----------|------|
| Potential (电压) | `VoltageRenderingMode` | 顶点电压 (V) | Plasma |
| Power Density (功率密度) | `PowerDensityRenderingMode` | 面功率密度 (W/mm²) | Inferno |

#### 着色器

| 着色器 | 用途 |
|--------|------|
| `mesh_shader` | 渲染三角形面片（带颜色映射） |
| `disconnected_shader` | 渲染断连铜区（灰色） |
| `edge_shader` | 渲染网格边和轮廓 |
| `points_shader` | 渲染连接点 |

#### 交互操作

| 操作 | 快捷键 | 功能 |
|------|--------|------|
| 平移 | 鼠标左键/中键拖拽 | 平移视图 |
| 缩放 | 鼠标滚轮 | 以光标为中心缩放 |
| 切换层 | `V` / `Shift+V` | 切换上/下一层 |
| 切换边显示 | `E` | 显示/隐藏网格边 |
| 切换轮廓 | `Shift+E` | 显示/隐藏轮廓 |
| 切换连接点 | `C` | 显示/隐藏连接点 |
| 重置视图 | `F` | 自动适应视图 |
| 重置色标 | `A` | 自动调整颜色范围 |
| 设置最小值 | `M` | 从光标位置设置颜色最小值 |
| 设置最大值 | `Shift+M` | 从光标位置设置颜色最大值 |

#### 异步渲染

网格数据准备在后台线程池中执行，通过 `DeferedDict` + `Future` 实现非阻塞 UI：

```
set_solution():
    → 后台线程: prepare_rendered_meshes (CPU 密集)
    → 后台线程: prepare_disconnected_meshes
    → paintGL() 中: 检查 Future 是否完成
        → 完成: 上传 VAO/VBO 到 GPU
        → 未完成: 跳过本次渲染
```

---

### 9. `padne/paraview.py` — VTK 导出

**功能**：将 `Solution` 导出为 ParaView 兼容的 VTK XML UnstructuredGrid (`.vtu`) 格式。

#### 关键函数

| 函数 | 用途 |
|------|------|
| `export_solution(solution, output_dir)` | 导出完整解，每层一个 `.vtu` 文件 |
| `create_vtk_root()` | 创建 VTKFile 根元素 |
| `create_piece(mesh_obj, potentials)` | 创建单个网格的 Piece 元素 |
| `create_point_data(potentials)` | 创建顶点电压数据 |
| `create_points(mesh_obj)` | 创建顶点坐标（Y 轴取反） |
| `create_cells(mesh_obj)` | 创建三角形连接性数据 |
| `create_data_array(parent, data_type, values, name)` | 创建 DataArray 子元素 |
| `_sanitize_filename(name, used_names)` | 清理层名为合法文件名 |

---

### 10. `padne/tests.py` — 测试辅助

**功能**：用于验证 `typeguard` 运行时类型检查是否正常工作的测试函数。

- `add_numbers(a, b)` — 正确返回类型
- `wrong_return_type()` — 故意返回错误类型，用于测试 typeguard 捕获

---

## 数据流完整图
替换为 triangle 库
```
┌─────────────────────────────────────────────────────────────┐
│                    KiCad Project (.kicad_pro)               │
└──────┬────────────────────────┬─────────────────────────────┘
       │                        │
       ▼                        ▼
  kicad_pcb                 kicad_sch
  (PCB 几何)              (原理图指令)
       │                        │
       ▼                        ▼
┌──────────────┐      ┌──────────────────┐
│ Gerber 渲染   │      │ 解析 !padne 指令  │
│ → Shapely 几何│      │ → LumpedSpec 列表 │
└──────┬───────┘      └────────┬─────────┘
       │                       │
       │   ┌──────────────┐    │
       │   │ 叠层提取      │    │
       │   │ 过孔/通孔提取  │    │
       │   │ 冲孔处理      │    │
       │   └──────┬───────┘    │
       │          │            │
       ▼          ▼            ▼
┌──────────────────────────────────────┐
│        problem.Problem               │
│  layers: [Layer(shape, conductance)] │
│  networks: [Network(connections,     │
│             elements)]               │
└──────────────────┬───────────────────┘
                   │
                   ▼
┌──────────────────────────────────────┐
│           solver.solve()             │
│                                      │
│  1. ConnectivityGraph → 电连通区域    │
│  2. Mesher.poly_to_mesh() → 三角网格  │
│  3. VertexIndexer → 全局顶点索引      │
│  4. NodeIndexer → 全局节点索引        │
│  5. laplace_operator() → 刚度矩阵    │
│  6. stamp_network() → 集总元件填入    │
│  7. setup_ground_node() → 接地约束   │
│  8. spsolve(L, r) → 求解电压向量     │
│  9. produce_layer_solutions() → 解   │
└──────────────────┬───────────────────┘
                   │
                   ▼
┌──────────────────────────────────────┐
│        solver.Solution               │
│  problem: Problem                    │
│  layer_solutions: [LayerSolution]    │
│    meshes: [Mesh]                    │
│    potentials: [ZeroForm]  ← 电压    │
│    power_densities: [TwoForm] ← 功率 │
│  solver_info: SolverInfo             │
└──────┬───────────────┬───────────────┘
       │               │
       ▼               ▼
┌────────────┐  ┌───────────────┐
│  ui.main() │  │ paraview      │
│  Qt+OpenGL │  │ export_solution│
│  GUI 显示   │  │ → .vtu 文件   │
└────────────┘  └───────────────┘
```

---

## C++ 扩展

项目通过 pybind11 绑定了 CGAL 库（`padne/cpp/_cgal.cpp`），提供：

- `cgal.mesh(config, vertices, segments, seeds, distance_map)` — CGAL 三角网格生成
- `cgal.PolyBoundaryDistanceMap` — 多边形边界距离图（用于变密度网格）
- `cgal.CGALPolygon` — CGAL 多边形封装

---

## 依赖关系图

```
cli.py → kicad.py → problem.py
                  ↘
                   → mesh.py ← (CGAL C++ 扑展)
                  ↗
       → solver.py → problem.py
                  ↘
                   → mesh.py
       → ui.py → solver.py
               → mesh.py
               → colormaps.py
               → units.py
       → paraview.py → mesh.py, solver.py
kicad.py → units.py
```
