# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project overview

This is **PADEN仿真** (`paden-integration`), an EasyEDA Pro (嘉立创EDA 专业版) extension that performs PCB power-delivery network (PDN) DC analysis by bridging EasyEDA to a local Python FEM backend.

- **Frontend**: TypeScript, bundled with esbuild into an IIFE for EasyEDA's extension runtime.
- **Backend**: Python FastAPI service running on `localhost:5000`, using `pygerber`, `shapely`, `numpy`, `scipy` for FEM solve.
- **Communication**: HTTP multipart requests from the extension to the local Python service.

## Common commands

All TypeScript/build commands run from the repo root:

| Command | Purpose |
|---------|---------|
| `npm install` | Install Node dependencies (Node >= 20.17.0). |
| `npm run compile` | Compile `src/index.ts` into `dist/` via esbuild. |
| `npm run lint` | Run ESLint with `@antfu/eslint-config`. |
| `npm run fix` | Run ESLint with `--fix`. |
| `npm run embed-python` | Embed `paden-service/` files as `<script type="text/plain">` tags into `ui/service-check.html`. |
| `npm run build` | Full release build: `embed-python` → `compile` → package `.eext` into `build/dist/`. |

There are currently no unit tests in the repo.

### Running the Python backend locally

```bash
cd paden-service
./start-paden-windows.bat   # Windows
./start-paden-linux.sh      # Linux/macOS
```

Both scripts auto-detect Python 3.10+, install dependencies (`numpy`, `scipy`, `shapely`, `fastapi`, `uvicorn`, `pydantic`, `matplotlib`, `trimesh`, `pygerber>=3.0.0a3`), run syntax checks, then start `main.py` on port 5000.

## Architecture

### Extension runtime

EasyEDA loads the extension from `extension.json` (`entry: "./dist/index"`). The TypeScript entry is compiled as an IIFE with `globalName: 'edaEsbuildExportName'` (`config/esbuild.common.ts`). The build output must remain IIFE/UMD-like; do not change `format`, `platform`, `bundle`, or `minify` in `esbuild.common.ts` unless you know the EasyEDA runtime requires otherwise.

### Source layout

- `src/index.ts` — Main flow orchestration: extract PCB data → config dialog → call backend → show results. Handles multi-network analysis (1 combined solve + N individual solves).
- `src/extract.ts` — Extracts pads, vias, tracks, layer names from the EasyEDA Pro API. Includes diagnostic logging (`PcbExtractor.diagnostics`) shown on solve failures.
- `src/convert.ts` — Builds the declarative config JSON sent to the Python backend and deserializes the FEM solution for display.
- `src/api.ts` — HTTP client (`PdnApiClient`) talking to `localhost:5000`.
- `src/display.ts` — Pushes results into EasyEDA Storage/MessageBus and opens `ui/results.html` in an IFrame.
- `src/types.ts` — Shared TypeScript types.

### UI dialogs

Plain HTML files using EasyEDA's IFrame + MessageBus APIs:

- `ui/config.html` — Power-rail configuration (voltage sources, current loads, layer copper thickness).
- `ui/results.html` — WebGL heatmap viewer for voltage/power density results.
- `ui/service-check.html` — Backend startup helper. Also embeds the Python service files (see `npm run embed-python`).
- `ui/analyzing.html` — Progress spinner shown during solve.

### Python backend

- `paden-service/main.py` — FastAPI entry. Parses the uploaded Gerber ZIP, builds the `Problem`, runs the solver, and returns serialized layer solutions + diagnostics.
- `paden-service/solver.py` / `solver_enhanced.py` — FEM solver implementations. `main.py` prefers `solver_enhanced`.
- `paden-service/problem.py` — Problem definition (layers, networks, lumped elements).
- `paden-service/mesh_pure.py` — Mesh data structures (half-edge, discrete differential forms).
- `paden-service/calculation.py` — Trace current-capacity checks.

### Data flow

```
EasyEDA PCB
    │
    ▼
src/extract.ts  ──pads/vias/layers/tracks──▶  src/convert.ts  ──config JSON + Gerber ZIP──▶  Python backend
    │                                                                                            │
    ▼                                                                                            ▼
ui/config.html  ◀── user config ──  src/index.ts                                          solver / problem
                                                                                                 │
                                                                                                 ▼
src/display.ts  ◀── resultSet ────────────────────────────────────────────────────────────  JSON response
    │
    ▼
ui/results.html (WebGL heatmap)
```

Key points:

- Geometry comes from the **Gerber ZIP** exported by `eda.pcb_ManufactureData.getGerberFile()`, not from the extracted polygon data.
- `convert.ts` builds a declarative config (layer conductance/thickness, vias, pads, sources, loads, tracks) and the backend aligns it to Gerber geometry using `easyeda_bounds`.
- For multi-rail configs, the frontend runs one combined solve plus one solve per rail; each result is stored as an `AnalysisResultEntry` in `AnalysisResultSet`.

### Packaging

- `.edaignore` controls which files are included in the `.eext` zip produced by `build/packaged.ts`. It intentionally excludes `/src/`, `/build/`, `/config/`, and dev config files.
- `build/packaged.ts` also generates a UUID in `extension.json` if the current one is missing or invalid.
- The GitHub Actions workflow (`build.yml`) runs `npm run build`, creates a version tag, then builds separate `zh-cn` and `global` `.eext` packages by swapping README and `extension.json` i18n fields.

## Development notes

- The EasyEDA global `eda` object is assumed available at runtime; it is typed via `@jlceda/pro-api-types`.
- Coordinates inside EasyEDA APIs are typically in **mil**; the frontend converts to **mm** (`MIL_TO_MM = 0.0254`) before sending to the backend.
- Storage keys used: `pdn-results`, `pdn-results-images`.
- MessageBus channels used: `pdn-config-*`, `pdn-results-*`, `padne-results-ready`, `pdn-reanalyze`, `pdn-results-close`, `pdn-service-check-*`.
- ESLint uses `@antfu/eslint-config` with tabs, single quotes, and semicolons. VS Code settings disable Prettier and enable ESLint fix-on-save for most file types.

## Release checklist

1. Update `version` in `extension.json`.
2. Add entry to `CHANGELOG.md`.
3. Run `npm run build` locally to verify.
4. Push to `main`; CI will create the GitHub Release and `.eext` artifacts.
