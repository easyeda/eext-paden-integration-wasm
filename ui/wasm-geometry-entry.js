/**
 * Bundler entry for the geometry/Gerber bridge.
 *
 * This file is bundled into dist/wasm-geometry-bridge.js as an IIFE.
 * It inlines tracespace and earcut, and relies on window.Clipper2ZFactory
 * having been set by ui/wasm-host.html before this bundle executes.
 */

import { parse } from '@tracespace/parser';
import { plot } from '@tracespace/plotter';
import earcut from 'earcut';

// The bridge reads these globals and exposes window.padenGeometry.
import './wasm-geometry-bridge.js';

window.tracespaceParser = { parse };
window.tracespacePlotter = { plot };
window.earcut = earcut;
