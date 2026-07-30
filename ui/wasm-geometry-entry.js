/**
 * Bundler entry for the geometry bridge.
 *
 * This file is bundled into dist/wasm-geometry-bridge.js as an IIFE.
 * It inlines earcut and triangle-wasm, and relies on window.Clipper2ZFactory
 * having been set by ui/wasm-host.html before this bundle executes.
 */

import earcut from 'earcut';
import Triangle from 'triangle-wasm';

// The bridge reads these globals and exposes window.padenGeometry.
import './wasm-geometry-bridge.js';

window.earcut = earcut;
// Triangle's CommonJS module exports init/triangulate/makeIO/freeIO. The
// bridge calls these from cdtTriangulate().
window.Triangle = Triangle;
