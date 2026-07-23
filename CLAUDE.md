# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project overview

This is **PADEN仿真** (`paden-integration`), an EasyEDA Pro (嘉立创EDA 专业版) extension that performs PCB power-delivery network (PDN) DC analysis using a built-in Go/WebAssembly backend.

- **Frontend**: TypeScript, bundled with esbuild into an IIFE for EasyEDA's extension runtime.
- **Backend**: Go 1.26+ compiled to WebAssembly (`GOOS=js GOARCH=wasm`). The WASM module runs in a hidden EasyEDA IFrame (`ui/wasm-host.html`) and communicates with the extension via `eda.sys_MessageBus`.
- **Geometry/ Gerber**: The WASM backend calls browser libraries through `syscall/js`:
  - Gerber parsing: `@tracespace/parser` + `@tracespace/plotter`
  - Polygon booleans / offsets: `clipper2-wasm`
  - Triangulation: `earcut`
- **Solver**: Custom sparse matrix (CSR/COO) + preconditioned conjugate gradient (CG) implemented in Go.

## Common commands

All TypeScript/build commands run from the repo root:

| Command | Purpose |
|---------|---------|
| `npm install` | Install Node dependencies (Node >= 20.17.0). |
| `npm run compile` | Compile `src/index.ts` into `dist/index.js` via esbuild. |
| `npm run lint` | Run ESLint with `@antfu/eslint-config`. |
| `npm run fix` | Run ESLint with `--fix`. |
| `npm run build:geometry-bridge` | Bundle `ui/wasm-geometry-bridge.js` into `dist/`. |
| `npm run build:wasm` | Compile `go-service/main_wasm.go` to `dist/paden.wasm`. |
| `npm run build:wasm:dev` | Compile WASM without `-s -w` for debugging (larger file). |
| `npm run build` | Full release build: compile TypeScript → build bridge → build WASM → copy assets → package `.eext` into `build/dist/`. |

### Go commands

Run from `go-service/`:

```bash
# Build the WASM module manually
cd go-service
GOOS=js GOARCH=wasm go build -ldflags="-s -w" -o ../dist/paden.wasm main_wasm.go

# Run tests under the WASM target (the repo currently has no test files)
cd go-service
GOOS=js GOARCH=wasm go test ./...
```

## Architecture

### Extension runtime

EasyEDA loads the extension from `extension.json` (`entry: "./dist/index"`). The TypeScript entry is compiled as an IIFE with `globalName: 'edaEsbuildExportName'` (`config/esbuild.common.ts`). The build output must remain IIFE/UMD-like; do not change `format`, `platform`, `bundle`, or `minify` in `esbuild.common.ts` unless you know the EasyEDA runtime requires otherwise.

### Source layout

- `src/index.ts` — Main flow orchestration: extract PCB data → config dialog → call WASM backend → show results. Handles multi-network analysis (1 combined solve + N individual solves).
- `src/extract.ts` — Extracts pads, vias, tracks, layer names from the EasyEDA Pro API. Includes diagnostic logging (`PcbExtractor.diagnostics`) shown on solve failures.
- `src/convert.ts` — Builds the declarative config JSON sent to the WASM backend and deserializes the FEM solution for display.
- `src/wasmClient.ts` — Loads the hidden `ui/wasm-host.html` IFrame and communicates with the Go WASM runtime over MessageBus.
- `src/display.ts` — Pushes results into EasyEDA Storage/MessageBus and opens `ui/results.html` in an IFrame.
- `src/types.ts` — Shared TypeScript types.

### UI dialogs

Plain HTML files using EasyEDA's IFrame + MessageBus APIs:

- `ui/config.html` — Power-rail configuration (voltage sources, current loads, layer copper thickness).
- `ui/results.html` — WebGL heatmap viewer for voltage/power density results.
- `ui/analyzing.html` — Progress spinner shown during solve.
- `ui/wasm-host.html` — Hidden IFrame that loads the Go WASM runtime, `clipper2-wasm`, `tracespace`, and `earcut`.

### Go/WebAssembly backend (`go-service/`)

- `main_wasm.go` — WASM entry point. Exposes `padne.analyzeGerber(gerberZip, configJson)` to JavaScript and returns a JSON string.
- `internal/pipeline/analyze.go` — Top-level analysis pipeline: parse Gerber, match layers, build networks, run solver.
- `internal/problem/` — Domain model: `Layer`, `Network`, lumped elements (`Resistor`, `VoltageSource`, `CurrentSource`, etc.).
- `internal/solver/` — Sparse matrix (CSR/COO), preconditioned CG, MNA stamping, Laplacian assembly, and post-processing.
- `internal/mesh/` — Half-edge mesh, boundary extraction, and `earcut` triangulation bridge.
- `internal/geometry/` — Go geometry types and `syscall/js` bridges to `tracespace`, `clipper2-wasm`, and `earcut`.
- `internal/wasmapi/` — JSON serialization matching the original Python `AnalyzeResponse`, plus IPC-2221 current-capacity checks.

### Data flow

```
EasyEDA PCB
    │
    ▼
src/extract.ts  ──pads/vias/layers/tracks──▶  src/convert.ts  ──config JSON + Gerber ZIP──▶  src/wasmClient.ts
    │                                                                                            │
    ▼                                                                                            ▼
ui/config.html  ◀── user config ──  src/index.ts                                          ui/wasm-host.html
                                                                                                 │
                                                                                                 ▼
src/display.ts  ◀── resultSet ────────────────────────────────────────────────────────────  go-service/internal/pipeline
    │                                                                                            │
    ▼                                                                                            ▼
ui/results.html (WebGL heatmap)                                                        JSON response
```

Key points:

- Geometry comes from the **Gerber ZIP** exported by `eda.pcb_ManufactureData.getGerberFile()`, not from the extracted polygon data.
- `convert.ts` builds a declarative config (layer conductance/thickness, vias, pads, sources, loads, tracks) and the backend aligns it to Gerber geometry using `easyeda_bounds`.
- For multi-rail configs, the frontend runs one combined solve plus one solve per rail; each result is stored as an `AnalysisResultEntry` in `AnalysisResultSet`.

### Packaging

- `.edaignore` controls which files are included in the `.eext` zip produced by `build/packaged.ts`. It intentionally excludes `/src/`, `/go-service/`, `/scripts/`, `/build/`, `/config/`, dev config files, and `ui/*.js` source, while keeping `dist/*.wasm` and related WASM assets.
- `build/packaged.ts` also generates a UUID in `extension.json` if the current one is missing or invalid.
- The GitHub Actions workflow (`build.yml`) runs `npm run build`, creates a version tag, then builds separate `zh-cn` and `global` `.eext` packages by swapping README and `extension.json` i18n fields.

## Development notes

- The EasyEDA global `eda` object is assumed available at runtime; it is typed via `@jlceda/pro-api-types`.
- Coordinates inside EasyEDA APIs are typically in **mil**; the frontend converts to **mm** (`MIL_TO_MM = 0.0254`) before sending to the backend.
- Storage keys used: `pdn-results`, `pdn-results-images`.
- MessageBus channels used:
  - `pdn-config-*`, `pdn-results-*`, `padne-results-ready`, `pdn-reanalyze`, `pdn-results-close`
  - `pdn-wasm-ready`, `pdn-wasm-error`, `pdn-wasm-analyze`, `pdn-wasm-analyze-result`, `pdn-wasm-progress`
- ESLint uses `@antfu/eslint-config` with tabs, single quotes, and semicolons. VS Code settings disable Prettier and enable ESLint fix-on-save for most file types.

## Release checklist

1. Update `version` in `extension.json`.
2. Add entry to `CHANGELOG.md`.
3. Run `npm run build` locally to verify.
4. Push to `main`; CI will create the GitHub Release and `.eext` artifacts.
