import {
  EasyEDA_Track,
  EasyEDA_Via,
  EasyEDA_Pad,
  EasyEDA_CopperPour,
  EasyEDA_PcbData,
} from './types';

/**
 * extract.ts - PCB 数据提取模块
 * 使用 EasyEDA Pro API 从当前打开的 PCB 中提取原始数据
 * 坐标保持 EasyEDA 内部单位（mil），不做转换，由后端处理
 */
export class PcbExtractor {

  /** 前端诊断日志，供外部读取后注入到诊断弹窗 */
  readonly diagnostics: string[] = [];

  private diag(line: string) {
    this.diagnostics.push(line);
  }

  /** 并发控制器，限制同时进行的 API 调用数 */
  private async parallelMap<T, R>(
    items: T[],
    fn: (item: T) => Promise<R>,
    concurrency: number,
    onBatchDone?: (done: number) => void,
  ): Promise<R[]> {
    const results: R[] = [];
    let next = 0;
    let doneCount = 0;

    const runNext = async (): Promise<void> => {
      while (next < items.length) {
        const idx = next++;
        try {
          results[idx] = await fn(items[idx]);
        } catch {
          results[idx] = undefined as any;
        }
        doneCount++;
        onBatchDone?.(doneCount);
      }
    };

    const workers = Array.from(
      { length: Math.min(concurrency, items.length) },
      () => runNext(),
    );
    await Promise.all(workers);
    return results;
  }

  /** 提取网络信息（焊盘、过孔、层名），不提��几何（走线/铺铜由 Gerber 提供） */
  async extractNetworkInfo(onProgress?: (percent: number) => void): Promise<EasyEDA_PcbData> {
    const tracks: EasyEDA_Track[] = [];
    const vias: EasyEDA_Via[] = [];
    const pads: EasyEDA_Pad[] = [];
    const copperPours: EasyEDA_CopperPour[] = [];
    const padKeySet = new Set<string>();

    onProgress?.(5);
    const layerNames = await this.extractLayerNames();

    // 提取过孔、焊盘和走线
    onProgress?.(15);
    const [bulkVias, bulkPads, bulkTracks] = await Promise.all([
      (async () => { try { return await eda.pcb_PrimitiveVia.getAll(); } catch { return null; } })(),
      (async () => { try { return await eda.pcb_PrimitivePad.getAll(); } catch { return null; } })(),
      (async () => {
        const apis = [
          { name: 'pcb_PrimitiveLine', fn: () => eda.pcb_PrimitiveLine?.getAll() },  // ✓ 已验证有效
          { name: 'pcb_PrimitiveTrace', fn: () => eda.pcb_PrimitiveTrace?.getAll() },
          { name: 'pcb_PrimitiveSegment', fn: () => eda.pcb_PrimitiveSegment?.getAll() },
          { name: 'pcb_PrimitiveTrack', fn: () => eda.pcb_PrimitiveTrack?.getAll() },
          { name: 'pcb_Track', fn: () => eda.pcb_Track?.getAll() },
          { name: 'pcb_Tracks', fn: () => eda.pcb_Tracks?.getAll() },
          { name: 'pcb_Trace', fn: () => eda.pcb_Trace?.getAll() },
          { name: 'pcb_Traces', fn: () => eda.pcb_Traces?.getAll() },
          { name: 'pcb_Segment', fn: () => eda.pcb_Segment?.getAll() },
          { name: 'pcb_Segments', fn: () => eda.pcb_Segments?.getAll() },
          { name: 'pcb_Line', fn: () => eda.pcb_Line?.getAll() },
          { name: 'pcb_Lines', fn: () => eda.pcb_Lines?.getAll() },
        ];

        for (const api of apis) {
          try {
            this.diag(`尝试走线API: ${api.name}...`);
            const result = await api.fn();
            if (result && Array.isArray(result) && result.length > 0) {
              this.diag(`✓ ${api.name} 返回 ${result.length} 条走线`);
              return result;
            } else if (result && Array.isArray(result)) {
              this.diag(`  ${api.name} 存在但返回空数组`);
            } else if (result === null) {
              this.diag(`  ${api.name} 返回 null`);
            } else if (result === undefined) {
              this.diag(`  ${api.name} API 不存在`);
            }
          } catch (e) {
            this.diag(`  ${api.name} 调用异常: ${e}`);
          }
        }

        this.diag(`所有走线API均失败或无数据`);
        return null;
      })(),
    ]);

    if (bulkVias != null) {
      for (const via of bulkVias) {
        const net = this.getNetFromPrimitive(via);
        if (!net) continue;
        const v = this.extractVia(via, net);
        if (v) vias.push(v);
      }
    }

    // 提取走线数据（用于电流容量检查）
    if (bulkTracks != null) {
      this.diag(`bulkTracks 返回: ${bulkTracks.length} 条`);
      for (const track of bulkTracks) {
        const net = this.getNetFromPrimitive(track);
        if (!net) {
          this.diag(`跳过无网络走线`);
          continue;
        }
        const t = this.extractTrack(track, net);
        if (t) tracks.push(t);
      }
      this.diag(`提取走线: ${tracks.length} 条`);
      // 显示前几条走线信息用于调试
      for (let i = 0; i < Math.min(3, tracks.length); i++) {
        const t = tracks[i];
        this.diag(`  走线[${i}]: net=${t.net}, width=${t.width}, layer=${t.layer}`);
      }
    } else {
      this.diag(`bulkTracks 为 null，API 调用可能失败`);
    }
    if (bulkPads != null) {
      for (const pad of bulkPads) {
        const p = this.extractPad(pad);
        if (p && p.net) {
          const key = `${p.net}|${p.x.toFixed(2)}|${p.y.toFixed(2)}`;
          if (!padKeySet.has(key)) { pads.push(p); padKeySet.add(key); }
        }
      }
    }
    onProgress?.(40);

    // Phase 2: 器件焊盘（同 extractAll）
    try {
      const components = await eda.pcb_PrimitiveComponent.getAll();
      const posToNetMap = new Map<string, string>();
      for (const p of pads) {
        posToNetMap.set(`${p.x.toFixed(2)}_${p.y.toFixed(2)}`, p.net);
      }

      const compMeta = components.map(comp => ({
        refDes: typeof comp.getState_Designator === 'function'
          ? comp.getState_Designator() : undefined,
        deviceName: typeof comp.getState_OtherProperty === 'function'
          ? (comp.getState_OtherProperty()?.['Device'] as string | undefined) : undefined,
        compId: comp.getState_PrimitiveId?.(),
      })).filter(c => c.compId);

      const compPinResults = await this.parallelMap(
        compMeta,
        async ({ compId, refDes, deviceName }) => {
          try {
            const pins = await eda.pcb_PrimitiveComponent.getAllPinsByPrimitiveId(compId!);
            if (!pins) return [];
            const compPads: EasyEDA_Pad[] = [];
            for (const pin of pins) {
              let fallbackNet2: string | undefined;
              try {
                const pinX = pin.getState_X?.();
                const pinY = pin.getState_Y?.();
                if (pinX != null && pinY != null) {
                  fallbackNet2 = posToNetMap.get(`${Number(pinX).toFixed(2)}_${Number(pinY).toFixed(2)}`);
                }
              } catch {}
              const pad = this.extractPad(pin, refDes, deviceName, fallbackNet2);
              if (pad) compPads.push(pad);
            }
            return compPads;
          } catch { return []; }
        },
        8,
        (done) => {
          onProgress?.(40 + Math.round((done / compMeta.length) * 30));
        },
      );

      for (const compPads of compPinResults) {
        if (!compPads) continue;
        for (const pad of compPads) {
          const key = `${pad.net}|${pad.x.toFixed(2)}|${pad.y.toFixed(2)}`;
          if (!padKeySet.has(key)) {
            pads.push(pad);
            padKeySet.add(key);
          } else if (pad.ref_des) {
            const existingIdx = pads.findIndex(p =>
              `${p.net}|${p.x.toFixed(2)}|${p.y.toFixed(2)}` === key && !p.ref_des
            );
            if (existingIdx >= 0) {
              pads[existingIdx].ref_des = pad.ref_des;
              pads[existingIdx].device_name = pad.device_name;
              if (pads[existingIdx].pad_number === '?' && pad.pad_number !== '?') {
                pads[existingIdx].pad_number = pad.pad_number;
              }
            }
          }
        }
      }
    } catch {}

    onProgress?.(80);

    // Gerber 流水线需要所有铜层：内层通过过孔连接，不一定有直接焊盘。
    // 不过滤，全部保留。
    const filteredLayerNames: Record<number, string> = {};
    for (const [id, name] of Object.entries(layerNames)) {
      filteredLayerNames[Number(id)] = name;
    }
    const filteredOuterLayerIds = this.detectOuterLayers(filteredLayerNames);

    onProgress?.(100);

    this.diag(`extractNetworkInfo: pads=${pads.length}, vias=${vias.length}, layers=${Object.keys(filteredLayerNames).length}`);

    return {
      tracks,
      vias,
      pads,
      copperPours,
      layerNames: filteredLayerNames,
      outerLayerIds: filteredOuterLayerIds,
    };
  }

  // ============================================================
  // 私有方法：层信息
  // ============================================================

  private async extractLayerNames(): Promise<Record<number, string>> {
    const layerNames: Record<number, string> = {};
    try {
      const allLayers = await eda.pcb_Layer.getAllLayers();
      for (const layer of allLayers) {
        const id = layer.id as number;
        const isCu = this.isCopperLayerId(id);
        if (isCu) {
          layerNames[id] = layer.name;
        }
      }
    } catch (e) {}
    return layerNames;
  }

  private isCopperLayerId(id: number): boolean {
    return id === 1 || id === 2 || (id >= 15 && id <= 44);
  }

  private detectOuterLayers(layerNames: Record<number, string>): Set<number> {
    const ids = Object.keys(layerNames).map(Number).sort((a, b) => a - b);
    const outer = new Set<number>();
    if (ids.length >= 2) {
      outer.add(ids[0]);
      outer.add(ids[ids.length - 1]);
    } else {
      ids.forEach(id => outer.add(id));
    }
    return outer;
  }

  // ============================================================
  // 私有方法：单图元提取
  // ============================================================

  private getNetFromPrimitive(primitive: any): string | undefined {
    try {
      const netObj = primitive.getState_Net?.() ?? primitive.getState_NetName?.();
      if (typeof netObj === 'string') return netObj.trim() || undefined;
      if (netObj && typeof netObj === 'object') {
        if (typeof netObj.name === 'string') return netObj.name.trim() || undefined;
        if (typeof netObj.getName === 'function') {
          const n = netObj.getName();
          return n?.trim() || undefined;
        }
      }
    } catch {}
    return undefined;
  }

  private extractTrack(primitive: any, netName: string): EasyEDA_Track | null {
    try {
      const x1 = primitive.getState_StartX();
      const y1 = primitive.getState_StartY();
      const x2 = primitive.getState_EndX();
      const y2 = primitive.getState_EndY();
      const width = primitive.getState_LineWidth();
      const layer = primitive.getState_Layer();
      if (x1 === null || y1 === null || x2 === null || y2 === null) return null;
      return { net: netName, x1, y1, x2, y2, width: width || 0.254, layer: layer || 1 };
    } catch { return null; }
  }

  private extractVia(primitive: any, netName: string): EasyEDA_Via | null {
    try {
      const x = primitive.getState_X();
      const y = primitive.getState_Y();
      const diameter = primitive.getState_Diameter();
      const holeDiameter = primitive.getState_HoleDiameter();
      if (x === null || y === null) return null;

      // Extract blind/buried via type information
      let viaType: string | undefined;
      try {
        viaType = primitive.getState_DesignRuleBlindViaName?.();
      } catch {}

      return {
        net: netName,
        x,
        y,
        diameter: diameter || 0.6,
        hole_diameter: holeDiameter || 0.3,
        via_type: viaType  // Will be used to determine which layers the via connects to
      };
    } catch { return null; }
  }

  private extractPad(primitive: any, refDes?: string, deviceName?: string, fallbackNet?: string): EasyEDA_Pad | null {
    try {
      const x = primitive.getState_X();
      const y = primitive.getState_Y();
      const padNumber = primitive.getState_PadNumber();
      const padShape = primitive.getState_Pad();
      const padW = Array.isArray(padShape) && typeof padShape[1] === 'number' ? padShape[1] : 0;
      const padH = Array.isArray(padShape) && typeof padShape[2] === 'number' ? padShape[2] : 0;
      let holeD = 0;
      try {
        const hole = primitive.getState_Hole();
        if (Array.isArray(hole) && typeof hole[1] === 'number') holeD = hole[1];
      } catch {}
      const rawLayer = typeof primitive.getState_Layer === 'function'
        ? primitive.getState_Layer() as number : undefined;
      const layer = rawLayer === 12 ? undefined : rawLayer;

      let netName = fallbackNet;
      if (!netName) {
        try {
          const netObj = primitive.getState_Net?.() ?? primitive.getState_NetName?.();
          if (typeof netObj === 'string') netName = netObj;
          else if (netObj && typeof netObj.name === 'string') netName = netObj.name;
          else if (netObj && typeof netObj.getName === 'function') netName = netObj.getName();
        } catch {}
      }

      if (x === null || y === null || !netName) return null;
      return {
        net: netName, x, y,
        pad_number: padNumber || '?',
        width: padW || 0.6, height: padH || 0.6,
        hole_diameter: holeD,
        layer: layer !== undefined ? layer : undefined,
        ref_des: refDes || undefined,
        device_name: deviceName || undefined,
      };
    } catch { return null; }
  }

  // ============================================================
  // 私有方法：铺铜提取（统一 Fill + Pour）
  // ============================================================

  private async extractCopperPours(copperPours: EasyEDA_CopperPour[]): Promise<void> {
    this.diag('\n' + '='.repeat(20) + ' 铜皮提取诊断 ' + '='.repeat(20));

    // ---- Phase 1：静态填充 Fill ----
    try {
      const fills = await eda.pcb_PrimitiveFill.getAll();
      this.diag(`Fill.getAll() 返回 ${fills?.length ?? 0} 个`);
      for (const fill of fills) {
        const net = fill.getState_Net();
        if (!net || net.trim() === '') continue;
        const layer = fill.getState_Layer() as number;
        const polygon = fill.getState_ComplexPolygon();
        const rawSrc = polygon?.getSource();
        if (rawSrc) {
          const preview = JSON.stringify(rawSrc).substring(0, 500);
          this.diag(`  Fill source preview: ${preview}`);
        }
        const { exterior: fillExt, holes: fillHoles } = this.parseComplexPolygonRings(rawSrc);
        this.addCopperPoly(copperPours, net, layer, fillExt, fillHoles, true, 1, 'Fill');
      }
    } catch (e) { this.diag(`!! Fill 提取失败: ${e}`); }

    // ---- Phase 2：覆铜 Pour → Poured ----
    const pourInfoMap = new Map<string, { net: string; layer: number }>();
    try {
      const pours = await eda.pcb_PrimitivePour.getAll();
      this.diag(`Pour.getAll() 返回 ${pours?.length ?? 0} 个`);
      for (const pour of pours) {
        const net = pour.getState_Net();
        const layer = pour.getState_Layer() as number;
        const pid = pour.getState_PrimitiveId();
        this.diag(`  Pour: pid=${pid}, net="${net}", layer=${layer}`);
        if (pid && net) pourInfoMap.set(pid, { net, layer });
      }
    } catch (e) { this.diag(`!! Pour 提取失败: ${e}`); }

    try {
      const poureds = await eda.pcb_PrimitivePoured.getAll();
      this.diag(`Poured.getAll() 返回 ${poureds?.length ?? 0} 个`);

      for (const poured of poureds) {
        const pourPid = poured.getState_PourPrimitiveId();
        const info = pourInfoMap.get(pourPid);
        if (!info || !info.net || info.net.trim() === '') {
          this.diag(`  Poured: pourPid=${pourPid}, 无匹配 Pour 信息`);
          continue;
        }
        const { net, layer } = info;

        try {
          const pourFills = poured.getState_PourFills();
          this.diag(`  Poured: pourPid=${pourPid}, net="${net}", layer=${layer}, pourFills=${pourFills ? (Array.isArray(pourFills) ? pourFills.length : '非数组') : 'null'}`);
          if (!pourFills || !Array.isArray(pourFills)) continue;

          if (pourFills.length === 0) {
            this.diag(`  pourFills 为空，回退到 Pour 边框`);
            const pourObj = await eda.pcb_PrimitivePour.get(pourPid);
            if (pourObj) {
              const polygon = pourObj.getState_ComplexPolygon();
              const pourSrc = polygon?.getSource();
              if (pourSrc) this.diag(`  Pour边框 source preview: ${JSON.stringify(pourSrc).substring(0, 500)}`);
              const { exterior: pourExt, holes: pourHoles } = this.parseComplexPolygonRings(pourSrc);
              this.addCopperPoly(copperPours, net, layer, pourExt, pourHoles, false, 1, 'Pour边框');
            } else {
              this.diag(`!! 回退: Pour.get(${pourPid}) 返回 null`);
            }
            continue;
          }

          let totalVerts = 0;
          for (let fi = 0; fi < pourFills.length; fi++) {
            const { exterior, holes, scale } = this.parsePourFill(pourFills[fi], fi === 0);
            totalVerts += this.addCopperPoly(copperPours, net, layer, exterior, holes, false, scale);
          }
          this.diag(`  Poured(${pourPid}): ${pourFills.length} pourFill → ${totalVerts} 总顶点`);
        } catch (e) { this.diag(`!! Poured(${pourPid}) 处理失败: ${e}`); }
      }
    } catch (e) { this.diag(`!! Poured.getAll() 失败: ${e}`); }

    this.diag(`最终提取铜皮总数: ${copperPours.length}`);
    if (copperPours.length > 0) {
      const byNetLayer: Record<string, number> = {};
      for (const cp of copperPours) {
        const key = `${cp.net}@L${cp.layer}`;
        byNetLayer[key] = (byNetLayer[key] ?? 0) + 1;
      }
      this.diag(`按网络+层分布: ${JSON.stringify(byNetLayer)}`);
    }
  }

  /**
   * 缩放并添加一个铜皮多边形，返回顶点数（0 表示跳过）
   * scale=1: ComplexPolygon 源（Fill、Pour 边框），坐标已是 mil
   * scale=10: Pour fill path 源，坐标是 1/10 mil，需要 ×10
   */
  private addCopperPoly(
    out: EasyEDA_CopperPour[], net: string, layer: number,
    exterior: Array<{ x: number; y: number }>,
    holes: Array<Array<{ x: number; y: number }>>,
    isFill: boolean, scale: number, tag?: string,
  ): number {
    if (exterior.length < 3) return 0;
    const rawVerts = exterior.map(v => ({ x: v.x * scale, y: v.y * scale }));
    const rawHoles = holes.map(h => h.map(v => ({ x: v.x * scale, y: v.y * scale })));
    if (tag) {
      this.diag(`  ${tag}: net="${net}", layer=${layer}, ${exterior.length} 顶点, scale=${scale} → (${rawVerts[0].x.toFixed(1)}, ${rawVerts[0].y.toFixed(1)})`);
    }
    out.push({ net, layer, vertices: rawVerts, holes: rawHoles, is_fill: isFill });
    return rawVerts.length;
  }

  /** 解析单个 pourFill 为外环 + 孔洞，同时返回缩放系数 */
  private parsePourFill(fill: any, logDetail: boolean): {
    exterior: Array<{ x: number; y: number }>;
    holes: Array<Array<{ x: number; y: number }>>;
    scale: number;
  } {
    let exterior: Array<{ x: number; y: number }> = [];
    let holes: Array<Array<{ x: number; y: number }>> = [];
    let scale = 10; // 默认：fill.path 源坐标为 1/10 mil

    if (fill && fill.path) {
      let src = fill.path.getSource();
      if (logDetail && src) this.diag(`  pourFill.path source preview: ${JSON.stringify(src).substring(0, 500)}`);
      if (Array.isArray(src)) {
        while (src.length === 1 && Array.isArray(src[0])) src = src[0];
        if (src.length > 0 && Array.isArray(src[0])) {
          for (let si = 0; si < src.length; si++) {
            if (!Array.isArray(src[si])) continue;
            const subParsed = this.parsePolygonVertices(src[si]);
            if (si === 0) {
              exterior = subParsed;
            } else {
              if (subParsed.length >= 3) holes.push(subParsed);
            }
          }
        } else {
          exterior = this.parsePolygonVertices(src);
        }
      }
    } else {
      // 回退路径：parsePourFillVertices 可能通过 getSource() 拿到 mil 坐标
      scale = 1;
      exterior = this.parsePourFillVertices(fill);
      if (logDetail) {
        const fillKeys = fill ? Object.keys(fill).join(',') : 'null';
        this.diag(`  pourFill: 无 path, scale=1, parsePourFillVertices → ${exterior.length} 顶点, keys=${fillKeys}`);
      }
    }

    // 2 顶点 + lineWidth → 矩形
    if (exterior.length === 2 && fill?.lineWidth > 0) {
      const [p1, p2] = exterior;
      const dx = p2.x - p1.x, dy = p2.y - p1.y;
      const len = Math.sqrt(dx * dx + dy * dy);
      if (len > 1e-10) {
        const nx = -dy / len, ny = dx / len, halfW = fill.lineWidth / 2;
        exterior = [
          { x: p1.x + nx * halfW, y: p1.y + ny * halfW },
          { x: p2.x + nx * halfW, y: p2.y + ny * halfW },
          { x: p2.x - nx * halfW, y: p2.y - ny * halfW },
          { x: p1.x - nx * halfW, y: p1.y - ny * halfW },
        ];
      }
    }

    return { exterior, holes, scale };
  }

  // ============================================================
  // 私有方法：多边形解析
  // ============================================================

  /**
   * 解析 ComplexPolygon 源数据为外环 + 孔洞。
   * source 可能是：
   *  - 单层扁平命令数组 [cmd, x, y, ...]        → 无孔
   *  - 嵌套数组 [[ring0], [ring1], ...]          → ring0=外环, 其余=孔
   */
  private parseComplexPolygonRings(source: any): {
    exterior: Array<{ x: number; y: number }>;
    holes: Array<Array<{ x: number; y: number }>>;
  } {
    if (!source) return { exterior: [], holes: [] };
    const arr = Array.isArray(source) ? source : [];

    // 嵌套数组：第一个子数组是外环，其余是孔洞（复合孔）
    if (arr.length > 0 && Array.isArray(arr[0])) {
      let exterior: Array<{ x: number; y: number }> = [];
      const holes: Array<Array<{ x: number; y: number }>> = [];
      for (let i = 0; i < arr.length; i++) {
        if (!Array.isArray(arr[i])) continue;
        const parsed = this.parsePolygonVertices(arr[i]);
        if (i === 0) {
          exterior = parsed;
        } else if (parsed.length >= 3) {
          holes.push(parsed);
        }
      }
      return { exterior, holes };
    }

    // 单层扁平数组：只有外环
    return { exterior: this.parsePolygonVertices(source), holes: [] };
  }

  private parsePolygonVertices(source: any): Array<{ x: number; y: number }> {
    if (!source) return [];
    const arr: any[] = Array.isArray(source) ? source : [];
    const vertices: Array<{ x: number; y: number }> = [];
    let i = 0;

    while (i < arr.length) {
      const token = arr[i];

      if (token === 'R') {
        const x = arr[i + 1], y = arr[i + 2], w = arr[i + 3], h = arr[i + 4];
        if (typeof x === 'number' && typeof y === 'number' && typeof w === 'number' && typeof h === 'number') {
          vertices.push({ x, y }, { x: x + w, y }, { x: x + w, y: y + h }, { x, y: y + h });
        }
        i += 7;
      } else if (token === 'CIRCLE') {
        const cx = arr[i + 1], cy = arr[i + 2], r = arr[i + 3];
        if (typeof cx === 'number' && typeof cy === 'number' && typeof r === 'number') {
          // 曲率自适应采样：根据半径决定采样点数
          // 小圆(r<1mm)用24点，中圆(1-5mm)用32点，大圆(>5mm)用48点
          // 这样既保持圆形精度，又避免超大圆的过多采样
          const nSamples = r < 40 ? 24 : (r < 200 ? 32 : 48);
          for (let k = 0; k < nSamples; k++) {
            const angle = (k / nSamples) * 2 * Math.PI;
            vertices.push({ x: cx + r * Math.cos(angle), y: cy + r * Math.sin(angle) });
          }
        }
        i += 4;
      } else if (token === 'L') {
        i += 1;
      } else if (token === 'ARC' || token === 'CARC') {
        const sweepAngle = arr[i + 1];
        const endX = arr[i + 2], endY = arr[i + 3];
        if (typeof sweepAngle === 'number' && typeof endX === 'number' && typeof endY === 'number') {
          const last = vertices.length > 0 ? vertices[vertices.length - 1] : { x: 0, y: 0 };
          const isCounterArc = token === 'CARC';
          vertices.push(...this.interpolateArc(last.x, last.y, sweepAngle, endX, endY, isCounterArc));
        }
        i += 4;
      } else if (token === 'C') {
        const cp1x = arr[i + 1], cp1y = arr[i + 2];
        const cp2x = arr[i + 3], cp2y = arr[i + 4];
        const endX = arr[i + 5], endY = arr[i + 6];
        if (typeof cp1x === 'number' && typeof cp1y === 'number' &&
            typeof cp2x === 'number' && typeof cp2y === 'number' &&
            typeof endX === 'number' && typeof endY === 'number') {
          const last = vertices.length > 0 ? vertices[vertices.length - 1] : { x: 0, y: 0 };
          vertices.push(...this.interpolateCubicBezier(last.x, last.y, cp1x, cp1y, cp2x, cp2y, endX, endY));
        }
        i += 7;
      } else if (typeof token === 'number') {
        const y = arr[i + 1];
        if (typeof y === 'number') {
          vertices.push({ x: token, y });
          i += 2;
        } else {
          i += 1;
        }
      } else {
        i += 1;
      }
    }

    return vertices;
  }

  /** 从起点到终点按 sweepAngle（度）插值弧线 */
  private interpolateArc(
    startX: number, startY: number,
    sweepAngle: number, endX: number, endY: number,
    isCounterArc: boolean,
  ): Array<{ x: number; y: number }> {
    const dx = endX - startX, dy = endY - startY;
    const chord = Math.sqrt(dx * dx + dy * dy);

    const absAngle = Math.abs(sweepAngle);
    if (chord < 1e-10 || absAngle < 0.01 || absAngle > 359.99) {
      return [{ x: endX, y: endY }];
    }

    const halfAngleRad = absAngle * Math.PI / 360;
    const sinHalf = Math.sin(halfAngleRad);
    if (sinHalf < 1e-10) return [{ x: endX, y: endY }];

    const r = chord / (2 * sinHalf);
    const h = r * Math.cos(halfAngleRad);
    const mx = (startX + endX) / 2, my = (startY + endY) / 2;
    const px = -dy / chord, py = dx / chord;

    // CARC 反转方向
    const effectiveAngle = isCounterArc ? -sweepAngle : sweepAngle;
    const sign = effectiveAngle >= 0 ? 1 : -1;
    const cx = mx + sign * h * px;
    const cy = my + sign * h * py;

    const startAngle = Math.atan2(startY - cy, startX - cx);
    const sweepRad = absAngle * Math.PI / 180;
    const sweep = effectiveAngle >= 0 ? sweepRad : -sweepRad;

    // 曲率自适应采样：圆弧至少8段，确保小角度圆弧也有足够精度
    const nSeg = Math.min(48, Math.max(8, Math.ceil(absAngle / 4)));
    const pts: Array<{ x: number; y: number }> = [];
    for (let k = 1; k <= nSeg; k++) {
      const angle = startAngle + sweep * (k / nSeg);
      pts.push({ x: cx + r * Math.cos(angle), y: cy + r * Math.sin(angle) });
    }
    // 强制最后一个点精确落在 (endX, endY)，保证路径不断裂
    if (pts.length > 0) {
      pts[pts.length - 1] = { x: endX, y: endY };
    }
    return pts;
  }

  private interpolateCubicBezier(
    startX: number, startY: number,
    cp1x: number, cp1y: number,
    cp2x: number, cp2y: number,
    endX: number, endY: number,
  ): Array<{ x: number; y: number }> {
    const nSeg = 16;
    const pts: Array<{ x: number; y: number }> = [];
    for (let k = 1; k <= nSeg; k++) {
      const t = k / nSeg;
      const mt = 1 - t;
      pts.push({
        x: mt * mt * mt * startX + 3 * mt * mt * t * cp1x + 3 * mt * t * t * cp2x + t * t * t * endX,
        y: mt * mt * mt * startY + 3 * mt * mt * t * cp1y + 3 * mt * t * t * cp2y + t * t * t * endY,
      });
    }
    if (pts.length > 0) {
      pts[pts.length - 1] = { x: endX, y: endY };
    }
    return pts;
  }

  private parsePourFillVertices(fill: any): Array<{ x: number; y: number }> {
    if (!fill) return [];
    const vertices: Array<{ x: number; y: number }> = [];

    if (Array.isArray(fill)) {
      for (const item of fill) {
        if (item && typeof item.x === 'number' && typeof item.y === 'number') {
          vertices.push({ x: item.x, y: item.y });
        } else if (Array.isArray(item) && item.length >= 2 && typeof item[0] === 'number') {
          vertices.push({ x: item[0], y: item[1] });
        }
      }
      if (vertices.length === 0 && fill.length >= 4 && typeof fill[0] === 'number') {
        for (let i = 0; i + 1 < fill.length; i += 2) {
          if (typeof fill[i] === 'number' && typeof fill[i + 1] === 'number') {
            vertices.push({ x: fill[i], y: fill[i + 1] });
          }
        }
      }
      if (vertices.length === 0) return this.parsePolygonVertices(fill);
    }

    if (vertices.length === 0 && fill.getSource) return this.parsePolygonVertices(fill.getSource());
    if (vertices.length === 0 && fill.points) return this.parsePourFillVertices(fill.points);

    return vertices;
  }

}
