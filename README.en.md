[简体中文](./README.md) | [English](#) | [繁體中文](./README.zh-Hant.md) | [日本語](./README.ja.md) | [Русский](./README.ru.md)

# PADEN Simulation

JLCEDA & EasyEDA Pro Extension — Extract PCB data and perform PDN Power Distribution Network FEM analysis

> **Version**: 1.0.7 | **Category**: PCB | **Keywords**: PDN, Power Analysis, Simulation, 仿真, PI

## Features

- Extract PCB traces, vias, pads, and copper pours from EasyEDA
- User-configurable power rails (voltage sources, current loads, layer copper thickness)
- Built-in Go/WebAssembly analysis engine — no separate backend runtime or service required
- Gerber parsing with tracespace, polygon booleans/offsets with Clipper2-WASM, triangulation with earcut
- WebGL voltage distribution and power density heatmap visualization
- Multi-rail analysis support (1 combined solve + N individual solves)

## Architecture

```
EasyEDA PCB
    │
    ▼
┌─────────────┐     ┌──────────────┐     ┌─────────────────────────┐
│  extract.ts │────▶│  convert.ts  │────▶│  ui/wasm-host.html      │
│  Extraction │     │  Conversion  │     │  Go/WASM host IFrame    │
└─────────────┘     └──────────────┘     └─────────────────────────┘
                                                   │
                                                   ▼
                                         ┌─────────────────────────┐
                                         │  go-service/internal/   │
                                         │  pipeline               │
                                         │  ├ Gerber parsing       │
                                         │  ├ Geometry (clipper2)  │
                                         │  ├ Triangulation        │
                                         │  └ FEM solver           │
                                         └───────────┬─────────────┘
                                                     │
                                                     ▼
┌─────────────┐     ┌──────────────┐     ┌─────────────────────────┐
│  display.ts │◀────│ wasmClient.ts│◀────│  JSON result            │
│  Display    │     │  WASM comms  │     └─────────────────────────┘
└──────┬──────┘     └──────────────┘
       │
       ▼
┌─────────────┐
│ results.html│  WebGL visualization
└─────────────┘
```

## Usage Guide

### 1. Install this extension in JLCEDA Pro (3.2+)
After installation, configure the extension

![Click "Configure"](./images/img-1.png)

![Check "Allow external interaction"](./images/img-2.png)

### 2. The extension is available in the PCB editor under an engineering project

### 3. Access via the top menu bar: Advanced → PADEN Simulation → Run PDN Analysis

### 4. Select the analysis parameters and click Start Analysis

![Extract PCB data](./images/img-3.png)

![Extract PCB data](./images/img-4.png)

## Quick Start

### 1. Install frontend dependencies

```shell
npm install
```

### 2. Compile extension

```shell
npm run compile
```

### 3. Build the WASM analysis engine

```shell
npm run build:wasm-host-bridge
npm run build:wasm
```

Full release build (TypeScript + WASM bridge + Go WASM + asset copy + `.eext` packaging):

```shell
npm run build
```

Development WASM build (keeps symbols, larger file):

```shell
npm run build:wasm:dev
```

### 4. Install in EasyEDA

1. Open JLCEDA Pro, enter PCB editor
2. Install the extension package
3. Select **PDN Analysis → Run PDN Analysis...** from the menu

> No external backend service is required; all analysis runs inside the EasyEDA WASM runtime.

## Project Structure

```
├── src/                    # TypeScript frontend
│   ├── index.ts            # Main entry, analysis orchestration
│   ├── extract.ts          # PCB data extraction (traces, vias, pads, copper)
│   ├── convert.ts          # Config builder and solution deserialization
│   ├── wasmClient.ts       # Loads Go/WASM host IFrame and communicates via MessageBus
│   ├── display.ts          # Result display (IFrame + Storage + MessageBus)
│   └── types.ts            # Type definitions
├── ui/                     # Dialog HTML files
│   ├── config.html         # Power rail configuration UI
│   ├── results.html        # WebGL visualization results
│   ├── analyzing.html      # Analysis progress UI
│   └── wasm-host.html      # Hidden Go/WASM host IFrame
├── go-service/             # Go/WebAssembly backend
│   ├── main_wasm.go        # WASM entry exposing analyzeGerber JS API
│   ├── internal/pipeline/  # Full analysis pipeline
│   ├── internal/problem/   # Problem definition (layers, networks, vias)
│   ├── internal/solver/    # FEM solver and sparse matrices
│   ├── internal/mesh/      # Mesh and triangulation interfaces
│   ├── internal/geometry/  # Gerber parsing, Clipper2, earcut bridge
│   └── internal/wasmapi/   # Result serialization
├── config/                 # esbuild configuration
├── scripts/                # build:wasm / build:wasm-host-bridge / copy-wasm-assets
├── build/                  # `.eext` packaging script
├── dist/                   # Build output (index.js, paden.wasm, wasm_exec.js, etc.)
└── extension.json          # Extension manifest
```

## Workflow

1. **Extract data** — Extract traces, vias, pads, and copper areas from the current PCB
2. **Configure analysis** — Select power nets, set voltage sources and current loads
3. **Gerber parsing** — The Go/WASM engine parses copper layer geometry from the Gerber ZIP via tracespace
4. **Geometry processing** — Boolean operations and offsets with Clipper2-WASM, triangulation with earcut
5. **FEM solving** — Build Laplacian matrix and solve voltage distribution
6. **Visualization** — WebGL voltage heatmap with layer switching, mesh edges, via markers

## Tech Stack

**Frontend**: TypeScript, esbuild, WebGL

**Backend**: Go 1.26+, WebAssembly, `syscall/js`

**Geometry/Mesh**: `@tracespace/parser`, `@tracespace/plotter`, `clipper2-wasm`, `earcut`

**Dependencies**: `@jlceda/pro-api-types`, `@tracespace/parser`, `@tracespace/plotter`, `clipper2-wasm`, `earcut`

## License

This extension uses the [Apache License 2.0](https://choosealicense.com/licenses/apache-2.0/) open source license.

---

## Links

- **Homepage**: https://github.com/easyeda/eext-paden-integration
- **Issue Tracker**: https://github.com/easyeda/eext-paden-integration/issues
