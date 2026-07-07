[简体中文](#) | [English](./README.en.md) | [繁體中文](./README.zh-Hant.md) | [日本語](./README.ja.md) | [Русский](./README.ru.md)

# PADEN仿真

嘉立创EDA & EasyEDA 专业版扩展 — 从 PCB 提取数据并进行 PDN 电源分配网络 FEM 分析

> **版本**: 1.0.7 | **分类**: PCB | **关键词**: PDN, Power Analysis, Simulation, 仿真, PI

## 功能

- 从 EasyEDA 提取 PCB 走线、过孔、焊盘、铺铜数据
- 用户配置电源轨道（电压源、电流负载、层铜厚）
- 内置 Go/WebAssembly 分析引擎，无需额外安装后端运行时或服务
- 使用 tracespace 解析 Gerber，Clipper2-WASM 进行多边形布尔/偏移，earcut 进行三角剖分
- WebGL 可视化电压分布和功率密度热力图
- 支持多电源网络分析（1 次合并求解 + N 次独立求解）

## 架构

```
EasyEDA PCB
    │
    ▼
┌─────────────┐     ┌──────────────┐     ┌─────────────────────────┐
│  extract.ts │────▶│  convert.ts  │────▶│  ui/wasm-host.html      │
│  数据提取    │     │  数据转换     │     │  Go/WASM 宿主 IFrame    │
└─────────────┘     └──────────────┘     └─────────────────────────┘
                                                   │
                                                   ▼
                                         ┌─────────────────────────┐
                                         │  go-service/internal/   │
                                         │  pipeline               │
                                         │  ├ Gerber 解析           │
                                         │  ├ 几何处理 (clipper2)   │
                                         │  ├ 三角剖分 (earcut)     │
                                         │  └ FEM 求解              │
                                         └───────────┬─────────────┘
                                                     │
                                                     ▼
┌─────────────┐     ┌──────────────┐     ┌─────────────────────────┐
│  display.ts │◀────│  wasmClient.ts│◀────│  JSON 结果              │
│  结果展示    │     │  WASM 通信    │     └─────────────────────────┘
└──────┬──────┘     └──────────────┘
       │
       ▼
┌─────────────┐
│ results.html│  WebGL 可视化
└─────────────┘
```

## 使用流程

### 1. 在嘉立创EDA专业版（3.2+）中安装本扩展
安装完后进行配置

![点击“配置”](./images/img-1.png)

![勾选“允许外部交互”](./images/img-2.png)

### 2. 在工程设计下，PCB 编辑窗口可以使用本扩展

### 3. 通过顶部菜单栏 高级 → PADEN仿真 → 运行PADEN仿真...

### 4. 选取需要分析的参数，然后点击开始分析

![提取PCB数据](./images/img-3.png)

![提取PCB数据](./images/img-4.png)

## 快速开始

### 1. 安装前端依赖

```shell
npm install
```

### 2. 编译扩展

```shell
npm run compile
```

### 3. 构建 WASM 分析引擎

```shell
npm run build:wasm-host-bridge
npm run build:wasm
```

完整发布构建（TypeScript + WASM 桥 + Go WASM + 资源复制 + 打包 `.eext`）：

```shell
npm run build
```

开发模式构建 WASM（保留符号表，体积较大）：

```shell
npm run build:wasm:dev
```

### 4. 在 EasyEDA 中安装

1. 打开嘉立创EDA专业版，进入 PCB 编辑器
2. 安装扩展包
3. 在菜单中选择 **PDN 分析 → 运行 PDN 分析...**

> 无需安装任何外部后端服务，所有分析均在 EasyEDA 内部的 WASM 运行时中完成。

## 项目结构

```
├── src/                    # TypeScript 前端
│   ├── index.ts            # 主入口，分析流程编排
│   ├── extract.ts          # PCB 数据提取（走线、过孔、焊盘、铺铜）
│   ├── convert.ts          # 数据转换：构建后端配置并反序列化结果
│   ├── wasmClient.ts       # 加载 Go/WASM 宿主 IFrame 并通过 MessageBus 通信
│   ├── display.ts          # 结果展示（IFrame + Storage + MessageBus）
│   └── types.ts            # 类型定义
├── ui/                     # 对话框 HTML
│   ├── config.html         # 电源轨道配置界面
│   ├── results.html        # WebGL 可视化结果界面
│   ├── analyzing.html      # 分析进度界面
│   └── wasm-host.html      # Go/WASM 宿主 IFrame（隐藏）
├── go-service/             # Go/WebAssembly 后端
│   ├── main_wasm.go        # WASM 入口，暴露 analyzeGerber 等 JS API
│   ├── internal/pipeline/  # 完整分析流水线
│   ├── internal/problem/   # 问题定义（层、网络、过孔等）
│   ├── internal/solver/    # FEM 求解器与稀疏矩阵
│   ├── internal/mesh/      # 网格与三角剖分接口
│   ├── internal/geometry/  # Gerber 解析、Clipper2、earcut 桥接
│   └── internal/wasmapi/   # 结果序列化
├── config/                 # esbuild 构建配置
├── scripts/                # build:wasm / build:wasm-host-bridge / copy-wasm-assets
├── build/                  # `.eext` 打包脚本
├── dist/                   # 构建产物（index.js、paden.wasm、wasm_exec.js 等）
└── extension.json          # 扩展配置
```

## 使用流程

1. **提取数据** — 从当前 PCB 提取走线、过孔、焊盘、铺铜区域
2. **配置分析** — 选择电源网络，设置电压源和电流负载
3. **Gerber 解析** — Go/WASM 引擎通过 tracespace 解析 Gerber ZIP 中的铜层几何
4. **几何处理** — 使用 Clipper2-WASM 进行布尔运算与偏移，earcut 进行三角剖分
5. **FEM 求解** — 构建拉普拉斯矩阵并求解电压分布
6. **可视化** — WebGL 渲染电压分布热力图，支持层切换、网格边显示、过孔标记

## 技术栈

**前端**: TypeScript, esbuild, WebGL

**后端**: Go 1.26+, WebAssembly, `syscall/js`

**几何/网格**: `@tracespace/parser`, `@tracespace/plotter`, `clipper2-wasm`, `earcut`

**依赖**: `@jlceda/pro-api-types`, `@tracespace/parser`, `@tracespace/plotter`, `clipper2-wasm`, `earcut`

## 开源许可

本扩展使用 [Apache License 2.0](https://choosealicense.com/licenses/apache-2.0/) 开源许可协议。

---

## 链接

- **主页**: https://github.com/easyeda/eext-paden-integration
- **问题反馈**: https://github.com/easyeda/eext-paden-integration/issues
