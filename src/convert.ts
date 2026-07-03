import {
  EasyEDA_PcbData,
  EasyEDA_Pad,
  EasyEDA_Via,
  SerializedSolution,
  SolutionData,
  PdnConfig,
} from './types';

/**
 * convert.ts - PDN Data Conversion Module
 *
 * Gerber-based pipeline: geometry comes from Gerber files,
 * this module only builds declarative config (vias, pads, sources, loads)
 * and handles solution deserialization for display.
 */
export class PcbDataConverter {

  readonly diagnostics: string[] = [];

  private diag(line: string) {
    this.diagnostics.push(line);
  }

  private readonly COPPER_CONDUCTIVITY = 5.95e4;
  private readonly DEFAULT_COPPER_THICKNESS = 0.035;
  private readonly MIL_TO_MM = 0.0254;

  // ============================================================
  // Gerber Config Builder
  // ============================================================

  /**
   * Build declarative config JSON for the Python backend.
   * The backend handles all geometry (from Gerber), via punching,
   * and network construction from this config.
   */
  buildGerberConfig(
    easyedaData: EasyEDA_PcbData,
    config: PdnConfig,
  ): Record<string, any> {
    const layerNames = easyedaData.layerNames;
    const analysisNets = new Set<string>();
    for (const rail of config.rails) {
      analysisNets.add(rail.net);
      if (rail.gnd_net) analysisNets.add(rail.gnd_net);
    }

    // Sort layers in physical stack order: Top(1) → Inner(15+) → Bottom(2)
    const sortedLayerEntries = Object.entries(layerNames).sort((a, b) => {
      const idA = Number(a[0]), idB = Number(b[0]);
      const posA = idA === 2 ? 1e6 : idA;
      const posB = idB === 2 ? 1e6 : idB;
      return posA - posB;
    });
    const sortedLayerNames = sortedLayerEntries.map(([, name]) => name);

    // ── Layer configs ──
    const layerConfigs: Record<string, any>[] = [];
    const layerCuThickness: Record<string, number> = {};
    for (const [id, name] of sortedLayerEntries) {
      const thickness = config.layerCuThickness?.[Number(id)] ?? this.DEFAULT_COPPER_THICKNESS;
      layerConfigs.push({
        name,
        conductance: this.COPPER_CONDUCTIVITY * thickness,
        thickness,
        layer_id: Number(id),
      });
      layerCuThickness[name] = thickness;
    }
    // ── Helper: Parse blind/buried via type to determine layer range ──
    const parseViaLayerRange = (viaType: string | undefined): string[] | null => {
      if (!viaType) return null;  // Through-hole via (all layers)

      // Format examples from EasyEDA:
      // "Blind: Top-Layer2" → connects Top Layer to Layer 2
      // "Buried: Layer2-Layer5" → connects Layer 2 to Layer 5
      // "Blind: Layer8-Bottom" → connects Layer 8 to Bottom

      const blindMatch = viaType.match(/Blind:\s*(.+?)\s*-\s*(.+)/);
      const buriedMatch = !blindMatch && viaType.match(/Buried:\s*(.+?)\s*-\s*(.+)/);

      if (blindMatch || buriedMatch) {
        const startLayer = blindMatch ? blindMatch[1] : buriedMatch![1];
        const endLayer = blindMatch ? blindMatch[2] : buriedMatch![2];

        // Find the indices of these layers in the sorted layer names
        const startIndex = sortedLayerNames.findIndex(name =>
          name.toLowerCase().includes(startLayer.toLowerCase()) ||
          startLayer.toLowerCase().includes(name.toLowerCase().replace(' layer', ''))
        );
        const endIndex = sortedLayerNames.findIndex(name =>
          name.toLowerCase().includes(endLayer.toLowerCase()) ||
          endLayer.toLowerCase().includes(name.toLowerCase().replace(' layer', ''))
        );

        if (startIndex >= 0 && endIndex >= 0) {
          // Return all layers from start to end (inclusive)
          const rangeStart = Math.min(startIndex, endIndex);
          const rangeEnd = Math.max(startIndex, endIndex);
          return sortedLayerNames.slice(rangeStart, rangeEnd + 1);
        }
      }

      // If we can't parse the type, fall back to all layers
      return null;
    };

    // ── Via specs ──
    const vias: Record<string, any>[] = [];
    for (const via of easyedaData.vias) {
      if (!analysisNets.has(via.net)) continue;
      const vx = via.x * this.MIL_TO_MM;
      const vy = via.y * this.MIL_TO_MM;
      const holeD = via.hole_diameter * this.MIL_TO_MM;

      // Determine which layers this via connects to based on via_type
      const layerRange = parseViaLayerRange(via.via_type);
      const viaLayerNames = layerRange || sortedLayerNames;  // Fall back to all layers for through-hole

      vias.push({
        x: vx,
        y: vy,
        hole_diameter: holeD,
        diameter: via.diameter * this.MIL_TO_MM,
        net: via.net,
        layer_names: viaLayerNames,
        via_type: via.via_type,  // Pass through for debugging
      });
    }

    // ── Pad specs ──
    const pads: Record<string, any>[] = [];
    for (const pad of easyedaData.pads) {
      if (!analysisNets.has(pad.net)) continue;
      const layerName = pad.layer != null ? layerNames[pad.layer] : undefined;
      pads.push({
        ref_des: pad.ref_des ?? '',
        pad_number: pad.pad_number ?? '?',
        net: pad.net,
        x: pad.x * this.MIL_TO_MM,
        y: pad.y * this.MIL_TO_MM,
        layer: layerName,
        is_tht: pad.layer == null || pad.hole_diameter > 0,
      });
    }

    // ── Sources and loads from PdnConfig ──
    const gndNet = config.rails.find(r => r.gnd_net)?.gnd_net ?? '';
    const sources: Record<string, any>[] = [];
    const loads: Record<string, any>[] = [];

    for (const rail of config.rails) {
      const railGndNet = rail.gnd_net ?? gndNet;

      // Source pads: VCC pads (positive terminal)
      const sourcePads: Record<string, any>[] = [];
      // Source GND pads (negative terminal)
      const sourceGndPads: Record<string, any>[] = [];
      for (const src of rail.sources) {
        for (const pad of src.pads) {
          sourcePads.push({
            ref_des: src.ref_des,
            x: pad.x * this.MIL_TO_MM,
            y: pad.y * this.MIL_TO_MM,
            layer: pad.layer ?? '',
            net: rail.net,
            is_tht: !pad.layer,
          });
        }
        for (const pad of (src.gnd_pads ?? [])) {
          sourceGndPads.push({
            ref_des: src.ref_des,
            x: pad.x * this.MIL_TO_MM,
            y: pad.y * this.MIL_TO_MM,
            layer: pad.layer ?? '',
            net: railGndNet,
            is_tht: !pad.layer,
          });
        }
      }
      if (sourcePads.length > 0) {
        sources.push({
          net: rail.net,
          voltage: rail.voltage,
          gnd_net: railGndNet,
          pads: sourcePads,
          gnd_pads: sourceGndPads,
        });
      }

      // Load pads: VCC pads (CS "from" terminal)
      const loadPads: Record<string, any>[] = [];
      // Load GND pads (CS "to" terminal)
      const loadGndPads: Record<string, any>[] = [];
      for (const loadConfig of rail.loads) {
        for (const pad of loadConfig.pads) {
          loadPads.push({
            ref_des: loadConfig.ref_des,
            x: pad.x * this.MIL_TO_MM,
            y: pad.y * this.MIL_TO_MM,
            layer: pad.layer ?? '',
            net: rail.net,
            is_tht: !pad.layer,
          });
        }
        for (const pad of (loadConfig.gnd_pads ?? [])) {
          loadGndPads.push({
            ref_des: loadConfig.ref_des,
            x: pad.x * this.MIL_TO_MM,
            y: pad.y * this.MIL_TO_MM,
            layer: pad.layer ?? '',
            net: railGndNet,
            is_tht: !pad.layer,
          });
        }
      }
      if (loadPads.length > 0) {
        const totalCurrent = rail.loads.reduce((s, l) => s + l.current, 0);
        loads.push({
          net: rail.net,
          current: totalCurrent,
          gnd_net: railGndNet,
          pads: loadPads,
          gnd_pads: loadGndPads,
        });
      }
    }

    // ── EasyEDA bounding box (for coordinate transform to Gerber coords) ──
    // Must use ALL pads/vias from the PCB, not just analyzed-net ones,
    // because the backend aligns centers with Gerber geometry.
    // Using only analyzed pads produces a shifted center if those pads
    // are clustered in a corner of the board.
    let easyedaBounds: Record<string, number> | undefined;
    const allPads: Array<{ x: number; y: number }> = [
      ...easyedaData.pads.map((p: EasyEDA_Pad) => ({ x: p.x * this.MIL_TO_MM, y: p.y * this.MIL_TO_MM })),
      ...easyedaData.vias.map((v: EasyEDA_Via) => ({ x: v.x * this.MIL_TO_MM, y: v.y * this.MIL_TO_MM })),
    ];
    if (allPads.length > 0) {
      const xs = allPads.map((p: any) => p.x);
      const ys = allPads.map((p: any) => p.y);
      easyedaBounds = {
        minX: Math.min(...xs),
        minY: Math.min(...ys),
        maxX: Math.max(...xs),
        maxY: Math.max(...ys),
      };
    }

    this.diag(`buildGerberConfig: ${layerConfigs.length} layers, ${vias.length} vias, `
      + `${pads.length} pads, ${sources.length} sources, ${loads.length} loads, ${easyedaData.tracks.length} tracks`);

    // 转换走线数据为后端格式（EasyEDA 宽度单位是 mil，需要转换为 mm）
    const tracksData = easyedaData.tracks.map(t => ({
      net: t.net,
      x1: t.x1 * this.MIL_TO_MM,
      y1: t.y1 * this.MIL_TO_MM,
      x2: t.x2 * this.MIL_TO_MM,
      y2: t.y2 * this.MIL_TO_MM,
      width: t.width * this.MIL_TO_MM,  // mil → mm 转换
      layer: t.layer,
    }));
    this.diag(`走线数据转换: ${tracksData.length} 条，第一条: net=${tracksData[0]?.net}, width=${tracksData[0]?.width}mm (原始=${easyedaData.tracks[0]?.width}mil), layer=${tracksData[0]?.layer}`);

    return {
      layers: layerConfigs,
      vias,
      pads,
      sources,
      loads,
      tracks: tracksData,
      gnd_net: gndNet,
      easyeda_bounds: easyedaBounds,
      layer_cu_thickness: layerCuThickness,
      temp_rise: config.tempRise ?? 10,  // 默认 10°C 温升
      project_name: easyedaData.pads[0]?.net ?? 'pdn-project',
      generate_images: false,
    };
  }

  // ============================================================
  // Solution Deserialization (for display)
  // ============================================================

  deserializeSolution(solution: SerializedSolution, layerNames: string[]): SolutionData {
    return {
      layerSolutions: solution.layer_solutions.map((ls, i) => ({
        layerName: ls.layer_name ?? layerNames[i] ?? `Layer ${i}`,
        meshes: ls.meshes.map(m => ({
          vertices: m.vertices.map(p => ({ x: p[0], y: p[1] })),
          triangles: m.triangles.map(t => [t.vertices[0], t.vertices[1], t.vertices[2]]),
          potentials: m.potentials,
          powerDensities: m.power_densities,
          currentDensities: m.current_densities || [],
        })),
        disconnectedMeshes: ls.disconnected_meshes.map(m => ({
          vertices: m.vertices.map(p => ({ x: p[0], y: p[1] })),
          triangles: m.triangles.map(t => [t.vertices[0], t.vertices[1], t.vertices[2]]),
          potentials: [],
          powerDensities: [],
        })),
      })),
      solverInfo: {
        groundNodeCurrent: solution.solver_info.ground_node_current,
        residualNorm: solution.solver_info.residual_norm,
      },
      diagnostics: solution.diagnostics,
      currentWarnings: solution.current_warnings,
    };
  }
}
