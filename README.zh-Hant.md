[簡體中文](./README.md) | [English](./README.en.md) | [繁體中文](#) | [日本語](./README.ja.md) | [Русский](./README.ru.md)

# PADEN 仿真

嘉立創EDA & EasyEDA 專業版擴展 — 從 PCB 提取數據並進行 PDN 電源分配網路 FEM 分析

> **版本**: 1.0.7 | **分類**: PCB | **關鍵詞**: PDN, Power Analysis, Simulation, 仿真, PI

## 功能

- 從 EasyEDA 提取 PCB 走線、過孔、焊盤、鋪銅數據
- 使用者配置電源軌道（電壓源、電流負載、層銅厚）
- 內建 Go/WebAssembly 分析引擎 — 無需額外後端執行環境或服務
- 使用 tracespace 解析 Gerber，使用 Clipper2-WASM 進行多邊形布林/偏移運算，使用 earcut 進行三角剖分
- WebGL 視覺化電壓分佈和功率密度熱力圖
- 支援多電源軌道分析（1 次聯合求解 + N 次獨立求解）

## 架構

```
EasyEDA PCB
    │
    ▼
┌─────────────┐     ┌──────────────┐     ┌─────────────────────────┐
│  extract.ts │────▶│  convert.ts  │────▶│  ui/wasm-host.html      │
│  數據提取    │     │  數據轉換     │     │  Go/WASM 宿主 IFrame    │
└─────────────┘     └──────────────┘     └─────────────────────────┘
                                                   │
                                                   ▼
                                         ┌─────────────────────────┐
                                         │  go-service/internal/   │
                                         │  pipeline               │
                                         │  ├ Gerber 解析          │
                                         │  ├ 幾何處理 (clipper2)  │
                                         │  ├ 三角剖分             │
                                         │  └ FEM 求解             │
                                         └───────────┬─────────────┘
                                                     │
                                                     ▼
┌─────────────┐     ┌──────────────┐     ┌─────────────────────────┐
│  display.ts │◀────│ wasmClient.ts│◀────│  JSON result            │
│  結果展示    │     │  WASM 通訊    │     └─────────────────────────┘
└──────┬──────┘     └──────────────┘
       │
       ▼
┌─────────────┐
│ results.html│  WebGL 視覺化
└─────────────┘
```

## 使用流程

### 1.在嘉立創EDA專業版（3.2+）中安裝本擴展
安裝完成後進行配置

![點擊「配置」](./images/img-1.png)

![勾選「允許外部互動」](./images/img-2.png)

### 2.在工程設計下，PCB編輯視窗可以使用本擴展

### 3.透過頂部選單列 高級 → PADEN仿真 → 執行PDN分析

### 4.選取需要分析的參數，然後點擊開始分析

![提取PCB數據](./images/img-3.png)

![提取PCB數據](./images/img-4.png)

## 快速開始

### 1. 安裝前端依賴

```shell
npm install
```

### 2. 編譯擴展

```shell
npm run compile
```

### 3. 建置 WASM 分析引擎

```shell
npm run build:wasm-host-bridge
npm run build:wasm
```

完整發佈建置（TypeScript + WASM bridge + Go WASM + 資源複製 + `.eext` 打包）：

```shell
npm run build
```

開發用 WASM 建置（保留符號，檔案較大）：

```shell
npm run build:wasm:dev
```

### 4. 在 EasyEDA 中安裝

1. 開啟嘉立創EDA專業版，進入 PCB 編輯器
2. 安裝擴展包
3. 在選單中選擇 **PDN 分析 → 執行 PDN 分析...**

> 無需外部後端服務；所有分析都在 EasyEDA 的 WASM 執行環境中運行。

## 專案結構

```
├── src/                    # TypeScript 前端
│   ├── index.ts            # 主入口，分析流程編排
│   ├── extract.ts          # PCB 數據提取（走線、過孔、焊盤、鋪銅）
│   ├── convert.ts          # 數據轉換與配置建構
│   ├── wasmClient.ts       # 載入 Go/WASM 宿主 IFrame 並透過 MessageBus 通訊
│   ├── display.ts          # 結果展示（IFrame + Storage + MessageBus）
│   └── types.ts            # 類型定義
├── ui/                     # 對話框 HTML 檔案
│   ├── config.html         # 電源軌道配置介面
│   ├── results.html        # WebGL 視覺化結果介面
│   ├── analyzing.html      # 分析進度介面
│   └── wasm-host.html      # 隱藏的 Go/WASM 宿主 IFrame
├── go-service/             # Go/WebAssembly 後端
│   ├── main_wasm.go        # WASM 入口，暴露 analyzeGerber JS API
│   ├── internal/pipeline/  # 完整分析流程
│   ├── internal/problem/   # 問題定義（層、網路、過孔）
│   ├── internal/solver/    # FEM 求解器與稀疏矩陣
│   ├── internal/mesh/      # 網格與三角剖分介面
│   ├── internal/geometry/  # Gerber 解析、Clipper2、earcut 橋接
│   └── internal/wasmapi/   # 結果序列化
├── config/                 # esbuild 配置
├── scripts/                # build:wasm / build:wasm-host-bridge / copy-wasm-assets
├── build/                  # `.eext` 打包腳本
├── dist/                   # 建置輸出（index.js、paden.wasm、wasm_exec.js 等）
└── extension.json          # 擴展配置
```

## 使用流程

1. **提取數據** — 從當前 PCB 提取走線、過孔、焊盤、鋪銅區域
2. **配置分析** — 選擇電源網路，設定電壓源和電流負載
3. **Gerber 解析** — Go/WASM 引擎透過 tracespace 從 Gerber ZIP 解析銅層幾何
4. **幾何處理** — 使用 Clipper2-WASM 進行布林運算與偏移，使用 earcut 進行三角剖分
5. **FEM 求解** — 構建拉普拉斯矩陣並求解電壓分佈
6. **視覺化** — WebGL 電壓熱力圖，支援層切換、網格邊顯示、過孔標記

## 技術棧

**前端**：TypeScript, esbuild, WebGL

**後端**：Go 1.26+, WebAssembly, `syscall/js`

**幾何/網格**：`@tracespace/parser`, `@tracespace/plotter`, `clipper2-wasm`, `earcut`

**依賴**：`@jlceda/pro-api-types`, `@tracespace/parser`, `@tracespace/plotter`, `clipper2-wasm`, `earcut`

## 開源許可

本擴展使用 [Apache License 2.0](https://choosealicense.com/licenses/apache-2.0/) 開源許可協議。

---

## 連結

- **主頁**: https://github.com/easyeda/eext-paden-integration
- **問題回報**: https://github.com/easyeda/eext-paden-integration/issues
