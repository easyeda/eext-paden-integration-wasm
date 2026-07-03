
"""
main.py - padne PDN Analysis HTTP Server (Gerber-based pipeline)

Gerber ZIP + declarative config JSON ��� FEM solve → JSON response.

Pipeline:
  1. Parse Gerber → Layer objects (Shapely geometry)
  2. Extract board outline, clip copper to outline
  3. Compute coordinate transform (EasyEDA mm → Gerber coords)
  4. Punch via anti-pad holes in copper
  5. Build via resistor networks (distributed boundary-point model)
  6. Build user networks (voltage sources, current loads)
  7. solver.solve()
  8. Serialize response
"""

import gc
import itertools
import json
import logging
import math
import os
import pathlib
import tempfile
import zipfile
from dataclasses import dataclass, field

import numpy as np
import shapely.geometry
import shapely.ops
import shapely.affinity

try:
    import psutil
    _HAS_PSUTIL = True
except ImportError:
    _HAS_PSUTIL = False

# PyGerber version detection and imports
_PYGERBER_VERSION = None  # "new" for >=2.4, "old" for <2.0
_HAS_PYGERBER = False

# Old version global imports (for backward compatibility)
pygerber_gerber_api = None
pygerber_vm = None

try:
    # Try new version API (pygerber >= 2.4)
    from pygerber.gerberx3.api.v2 import GerberFile
    from pygerber.gerberx3.parser2.parser2 import Parser2, Parser2Options
    from pygerber.gerberx3.tokenizer.tokenizer import Tokenizer
    _PYGERBER_VERSION = "new"
    _HAS_PYGERBER = True
except ImportError:
    try:
        # Try old version API (pygerber < 2.0)
        import pygerber.gerber.api as pygerber_gerber_api
        import pygerber.vm as pygerber_vm
        _PYGERBER_VERSION = "old"
        _HAS_PYGERBER = True
    except ImportError:
        _PYGERBER_VERSION = None
        _HAS_PYGERBER = False

# Import solver with preference for enhanced version
try:
    from . import problem, mesh_pure as mesh
    try:
        from . import solver_enhanced as solver
    except ImportError:
        from . import solver
except ImportError:
    import problem
    import mesh_pure as mesh
    try:
        import solver_enhanced as solver
    except ImportError:
        import solver

log = logging.getLogger(__name__)


# ============================================================
# Diagnostics Collector (returned to frontend in error popup)
# ============================================================

class DiagCollector:
    def __init__(self):
        self.lines = []

    def info(self, msg):
        self.lines.append(f"[INFO] {msg}")
        log.info(msg)

    def warn(self, msg):
        self.lines.append(f"[WARN] {msg}")
        log.warning(msg)

    def error(self, msg):
        self.lines.append(f"[ERROR] {msg}")
        log.error(msg)


# ============================================================
# Pydantic Output Models
# ============================================================

import uvicorn
from fastapi import FastAPI, UploadFile, File, Form
from fastapi.middleware.cors import CORSMiddleware
from pydantic import BaseModel


class MeshTriangleOutput(BaseModel):
    vertices: list[int]


class MeshOutput(BaseModel):
    vertices: list[list[float]]
    triangles: list[MeshTriangleOutput]
    potentials: list[float]
    power_densities: list[float]
    current_densities: list[list[float]] = []


class DisconnectedMeshOutput(BaseModel):
    vertices: list[list[float]]
    triangles: list[MeshTriangleOutput]


class LayerSolutionOutput(BaseModel):
    layer_name: str
    meshes: list[MeshOutput]
    disconnected_meshes: list[DisconnectedMeshOutput]


class SolverInfoOutput(BaseModel):
    ground_node_current: float
    residual_norm: float


class CurrentCheckOutput(BaseModel):
    network_name: str
    layer_name: str
    calculated_current: float
    max_allowed_current: float
    utilization: float
    is_exceeded: bool
    trace_width_mm: float
    copper_oz: float
    temp_rise: float
    message: str


class AnalyzeResponse(BaseModel):
    success: bool
    message: str | None = None
    layer_solutions: list[LayerSolutionOutput] = []
    solver_info: SolverInfoOutput | None = None
    connection_points: dict[str, list[dict]] = {}
    layer_boundaries: dict[str, list[dict]] = {}
    diagnostics: list[str] = []
    current_warnings: list[CurrentCheckOutput] = []


# ============================================================
# Gerber Parsing
# ============================================================

def _fix_gerber_fsla(gbr_path, d=None):
    """Expand FSLA format if coordinates exceed the declared digit count.

    pygerber raises PackedCoordinateTooLongError when a coordinate has more
    digits than the FSLA format allows (e.g., 9-digit coordinate with format
    2.6 which only allows 8 digits).  This happens on large PCBs whose
    coordinates need more integer digits than the EDA tool declared.

    Fix: scan the file for max coordinate length and rewrite the FSLA header
    with enough integer digits to accommodate all coordinates.
    """
    import re

    try:
        content = gbr_path.read_text(encoding="utf-8", errors="ignore")
    except Exception:
        return

    # Find FSLA format: %FSLAX26Y26*%  → X integer=2, decimal=6
    m = re.search(r'%FSLAX(\d)(\d)Y(\d)(\d)\*%', content)

    # Find all coordinate values (X/Y followed by digits in draw commands)
    coords = re.findall(r'[XY]([+-]?\d+)', content)
    if not coords:
        return

    max_len = max(len(c.lstrip('+-')) for c in coords)

    if m:
        # Existing FSLA header - check if expansion needed
        x_int, x_dec = int(m.group(1)), int(m.group(2))
        y_int, y_dec = int(m.group(3)), int(m.group(4))
        expected_len = x_int + x_dec  # same for X and Y
        if max_len <= expected_len:
            return
        extra = max_len - expected_len
        new_x_int = x_int + extra
        new_y_int = y_int + extra
        old_fsla = m.group(0)
        new_fsla = f'%FSLAX{new_x_int}{x_dec}Y{new_y_int}{y_dec}*%'
        content = content.replace(old_fsla, new_fsla, 1)
    else:
        # No FSLA header - insert one based on actual coordinates
        # Default to 6 decimal places (industry standard), calculate integer digits needed
        decimal_places = 6
        # Calculate integer digits needed: max_len - decimal_places
        integer_digits = max_len - decimal_places
        if integer_digits < 2:
            integer_digits = 2  # Minimum 2 integer digits
        new_fsla = f'%FSLAX{integer_digits}{decimal_places}Y{integer_digits}{decimal_places}*%'
        # Insert FSLA header at the beginning (after MO command if present)
        mo_match = re.search(r'%MO(nn|MM)\*%', content)
        if mo_match:
            insert_pos = mo_match.end()
        else:
            insert_pos = 0
        content = content[:insert_pos] + new_fsla + '*%' + '\n' + content[insert_pos:]

    try:
        gbr_path.write_text(content, encoding="utf-8")
        if d:
            if m:
                d.info(f"Gerber '{gbr_path.name}': FSLA expanded "
                       f'X{x_int}{x_dec}Y{y_int}{y_dec} → '
                       f'X{new_x_int}{x_dec}Y{new_y_int}{y_dec} '
                       f'(max coord length {max_len} > {expected_len})')
            else:
                d.info(f"Gerber '{gbr_path.name}': FSLA inserted "
                       f'X{integer_digits}{decimal_places}Y{integer_digits}{decimal_places} '
                       f'(max coord length {max_len})')
    except Exception:
        pass


def _gerber_to_shapely(gbr_path, d=None):
    """Parse a Gerber file → Shapely MultiPolygon."""
    if not _HAS_PYGERBER:
        raise RuntimeError("pygerber not installed")

    # Common checks
    try:
        content = gbr_path.read_text(encoding="utf-8", errors="ignore")
        file_size = len(content)
        if d: d.info(f"Gerber '{gbr_path.name}': {file_size} bytes")
        if file_size < 10:
            if d: d.warn(f"Gerber '{gbr_path.name}': file too small")
            return None
    except Exception:
        pass

    # Fix FSLA format if coordinates are too long for the declared format.
    # EasyEDA can produce coordinates exceeding the FSLA integer digit count
    # (e.g., 9-digit coords with format 2.6 which only allows 8 digits).
    _fix_gerber_fsla(gbr_path, d)

    # Re-read content after potential FSLA fix
    try:
        source_code = gbr_path.read_text(encoding="utf-8", errors="ignore")
    except Exception:
        return None

    # Dispatch to version-specific implementation
    if _PYGERBER_VERSION == "new":
        return _gerber_to_shapely_new(gbr_path, d, source_code)
    else:
        return _gerber_to_shapely_old(gbr_path, d, source_code)


def _gerber_to_shapely_new(gbr_path, d=None, source_code=None):
    """Parse a Gerber file → Shapely MultiPolygon using pygerber >= 2.4."""
    from pygerber.gerberx3.api.v2 import GerberFile
    from pygerber.gerber.parser import Parser2, Parser2Options
    from pygerber.gerber.tokenizer import Tokenizer
    from pygerber.vm.shapely import ShapelyVirtualMachine
    import shapely.geometry as sh_geo

    def _angle_length_to_segment_count(angle_length):
        return int(abs(angle_length) * 0.4 + 10)

    geometry = None
    try:
        if source_code is None:
            source_code = gbr_path.read_text(encoding="utf-8", errors="ignore")

        # Parse Gerber file using low-level API
        tokens = Tokenizer().tokenize(source_code)
        parser = Parser2(Parser2Options(
            on_update_drawing_state_error="ignore"
        ))
        command_buffer = parser.parse(tokens)

        # Use ShapelyVirtualMachine to render
        vm = ShapelyVirtualMachine(
            angle_length_to_segment_count=_angle_length_to_segment_count,
        )

        skip_count = 0
        for command in command_buffer:
            try:
                command.visit(vm)
            except Exception as e:
                skip_count += 1
                if d and skip_count <= 5:
                    d.debug(f"Skipped command: {e}")

        # Extract geometry
        main_layer = vm._layers.get(vm.MAIN_LAYER_ID)
        all_polys = []
        if hasattr(main_layer, 'shape') and main_layer.shape:
            all_polys.extend(main_layer.shape)

        if not all_polys:
            if d: d.warn(f"Gerber '{gbr_path.name}': extracted 0 polygons")
            return None

        geometry = sh_geo.MultiPolygon(all_polys)
        if d: d.info(f"Gerber '{gbr_path.name}': extracted {len(all_polys)} polygons (skipped {skip_count} commands)")

    except Exception as e:
        if d: d.error(f"Gerber '{gbr_path.name}': parsing failed: {type(e).__name__}: {e}")
        return None

    # Post-processing (same as old version)
    if geometry is None:
        return None
    return _post_process_geometry(geometry, gbr_path, d)


def _gerber_to_shapely_old(gbr_path, d=None, source_code=None):
    """Parse a Gerber file → Shapely MultiPolygon using pygerber < 2.0.
    source_code parameter is ignored (kept for API compatibility)."""
    try:
        gerber_data = pygerber_gerber_api.GerberFile.from_file(str(gbr_path))
    except Exception as e:
        if d: d.warn(f"Gerber read failed '{gbr_path.name}': {e}")
        return None

    # Check if gerber data has any draws before rendering
    try:
        rvmc = gerber_data._get_rvmc()
    except AssertionError:
        if d: d.warn(f"Gerber '{gbr_path.name}': _get_rvmc AssertionError, trying alternate render")
        rvmc = None
    except Exception as e:
        if d: d.warn(f"Gerber '{gbr_path.name}': _get_rvmc failed: {type(e).__name__}: {e}")
        rvmc = None

    def _angle_length_to_segment_count(angle_length):
        return int(abs(angle_length) * 0.4 + 10)

    geometry = None

    # Attempt 1: standard pygerber render
    try:
        if rvmc is not None:
            result = pygerber_vm.render(
                rvmc,
                backend="shapely",
                angle_length_to_segment_count=_angle_length_to_segment_count,
            )
        else:
            result = pygerber_vm.render(
                gerber_data,
                backend="shapely",
                angle_length_to_segment_count=_angle_length_to_segment_count,
            )
        geometry = result.shape
    except AssertionError:
        if d: d.info(f"Gerber '{gbr_path.name}': standard render AssertionError, trying manual VM extraction")
    except Exception as e:
        if d: d.info(f"Gerber '{gbr_path.name}': standard render failed ({type(e).__name__}: {e}), trying manual VM extraction")

    # Attempt 2: bypass pygerber's assert — run VM manually, skip failing commands
    if geometry is None and rvmc is not None:
        try:
            from pygerber.vm.shapely import ShapelyVirtualMachine, ShapelyEagerLayer
            import shapely.geometry as _sh_geo
            vm = ShapelyVirtualMachine(
                angle_length_to_segment_count=_angle_length_to_segment_count,
            )
            skip_count = 0
            for command in rvmc.commands:
                try:
                    command.visit(vm)
                except (AssertionError, Exception):
                    skip_count += 1
            # Collect shapes from MAIN layer only (same as standard render)
            main_layer = vm._layers.get(vm.MAIN_LAYER_ID)
            all_polys = []
            if isinstance(main_layer, ShapelyEagerLayer) and main_layer.shape:
                all_polys.extend(main_layer.shape)
            if all_polys:
                geometry = _sh_geo.MultiPolygon(all_polys)
                if d: d.info(f"Gerber '{gbr_path.name}': manual VM extraction got {len(all_polys)} polygons (skipped {skip_count} commands)")
            elif skip_count > 0:
                if d: d.warn(f"Gerber '{gbr_path.name}': manual VM extraction got 0 polygons, skipped {skip_count}/{len(rvmc.commands)} commands")
        except Exception as e:
            if d: d.warn(f"Gerber '{gbr_path.name}': manual VM extraction failed: {type(e).__name__}: {e}")

    # Post-processing (common to both versions)
    if geometry is None:
        return None
    return _post_process_geometry(geometry, gbr_path, d)


def _post_process_geometry(geometry, gbr_path, d=None):
    """Post-process shapely geometry: union, clean, orient."""
    if geometry is None:
        if d: d.warn(f"Gerber '{gbr_path.name}': returned None")
        return None
    if geometry.is_empty:
        if d: d.warn(f"Gerber '{gbr_path.name}': empty geometry (type={geometry.geom_type})")
        return None

    if d: d.info(f"Gerber '{gbr_path.name}': raw type={geometry.geom_type}, bounds={geometry.bounds}")

    # NOTE: Do NOT flip Y axis here.
    # pygerber produces Y-down geometry (negative Y values), which has the
    # same Y direction as EasyEDA (Y-down, positive values).  The
    # coordinate transform (_compute_coordinate_transform) aligns the two
    # via center-to-center offset.  A Y-flip would invert the direction
    # and break the alignment, causing pads to miss the copper geometry.

    geometry = shapely.unary_union([geometry])
    geometry = geometry.buffer(1e-4).buffer(-1e-4)
    geometry = geometry.simplify(tolerance=1e-4, preserve_topology=True)
    geometry = shapely.remove_repeated_points(geometry, tolerance=1e-8)

    # Fix polygon orientation: ensure exterior rings are counter-clockwise.
    # Without the Y-flip, pygerber's Y-down geometry may produce clockwise
    # rings, which Shapely interprets as holes. This causes outline clipping
    # to return empty intersections.
    if hasattr(geometry, 'geoms'):
        fixed = []
        for g in geometry.geoms:
            if g.geom_type == 'Polygon':
                fixed.append(shapely.geometry.polygon.orient(g, 1.0))
            else:
                fixed.append(g)
        geometry = shapely.geometry.MultiPolygon(fixed)
    elif geometry.geom_type == 'Polygon':
        geometry = shapely.geometry.polygon.orient(geometry, 1.0)

    if geometry.geom_type == "Polygon":
        geometry = shapely.geometry.MultiPolygon([geometry])
    elif geometry.geom_type != "MultiPolygon":
        if d: d.warn(f"Gerber '{gbr_path.name}': unexpected type {geometry.geom_type}")
        return None

    return geometry


def _match_gerber_filename(layer_name, filename):
    """Match a Gerber filename to an EasyEDA layer name."""
    import re
    fn_lower = filename.lower()
    name_lower = layer_name.lower()

    if name_lower in fn_lower:
        return True

    # Normalize: remove spaces/underscores/hyphens so "Inner Layer 1"
    # matches "gerber_innerlayer1.g1"
    name_norm = re.sub(r'[\s_\-]', '', name_lower)
    fn_norm = re.sub(r'[\s_\-]', '', fn_lower)
    if name_norm in fn_norm:
        return True

    mapping = {
        "top": ["top", "f.cu", "fcu", "toplayer"],
        "bottom": ["bottom", "b.cu", "bcu", "bottomlayer"],
    }
    for key, patterns in mapping.items():
        if key in name_lower:
            return any(p in fn_lower for p in patterns)
    return False


def _build_layers_from_gerbers(zip_bytes, layer_configs, d=None):
    """Parse Gerber ZIP → list of problem.Layer objects."""
    layers = []

    with tempfile.TemporaryDirectory() as tmpdir:
        zip_path = pathlib.Path(tmpdir) / "gerber.zip"
        zip_path.write_bytes(zip_bytes)

        with zipfile.ZipFile(str(zip_path), "r") as zf:
            zf.extractall(tmpdir)
            if d: d.info(f"ZIP contents: {zf.namelist()}")

        gerber_files = []
        _std_exts = {".gbr", ".gtl", ".gbl", ".gbo", ".gbs", ".gko", ".gml"}
        for p in pathlib.Path(tmpdir).rglob("*"):
            sfx = p.suffix.lower()
            # Standard extensions OR inner-layer extensions (.g1, .g2, ..., .g10, .g44, etc.)
            if sfx in _std_exts or (len(sfx) > 2 and sfx.startswith(".g") and sfx[2:].isdigit()):
                gerber_files.append(p)
            elif p.is_file() and p.suffix == "" and p.name not in ("gerber.zip",):
                try:
                    head = p.read_text(encoding="utf-8", errors="ignore")[:100]
                    if "%FSLA" in head or "G04" in head or "%ADD" in head:
                        gerber_files.append(p)
                except Exception:
                    pass

        if d: d.info(f"Gerber files found: {len(gerber_files)}")
        for gf in gerber_files:
            if d: d.info(f"  {gf.name}")

        matched_gerber_files = set()
        for lc in layer_configs:
            layer_name = lc["name"]
            conductance = lc.get("conductance", 1.0)
            layer_id = lc.get("layer_id")

            matched_file = None

            # Pass 1: name-based matching
            for gf in gerber_files:
                if gf in matched_gerber_files:
                    continue
                if _match_gerber_filename(layer_name, gf.name):
                    matched_file = gf
                    break

            # Pass 2: layer_id based matching for inner copper layers
            # EasyEDA: ID 15 = 1st inner → .g1, ID 16 = 2nd inner → .g2, etc.
            if matched_file is None and layer_id is not None and layer_id >= 15:
                inner_idx = layer_id - 14
                target_ext = f".g{inner_idx}"
                for gf in gerber_files:
                    if gf in matched_gerber_files:
                        continue
                    if gf.suffix.lower() == target_ext:
                        matched_file = gf
                        if d: d.info(f"Layer '{layer_name}' (ID={layer_id}): matched by extension {target_ext} → {gf.name}")
                        break

            if matched_file is None:
                continue

            matched_gerber_files.add(matched_file)
            geometry = _gerber_to_shapely(matched_file, d)
            if geometry is None or geometry.is_empty:
                if d: d.warn(f"Layer '{layer_name}': Gerber parse result empty")
                continue

            if d: d.info(f"Layer '{layer_name}': {len(geometry.geoms)} polygons from Gerber")

            layer = problem.Layer(
                shape=geometry,
                name=layer_name,
                conductance=conductance,
            )
            layers.append(layer)

    if d: d.info(f"Valid layers: {len(layers)}")
    return layers


# ============================================================
# Board Outline
# ============================================================

def _extract_board_outline_from_gerbers(zip_bytes, d=None):
    """Extract board outline from Gerber ZIP."""
    if not _HAS_PYGERBER:
        return None

    with tempfile.TemporaryDirectory() as tmpdir:
        zip_path = pathlib.Path(tmpdir) / "gerber.zip"
        zip_path.write_bytes(zip_bytes)

        with zipfile.ZipFile(str(zip_path), "r") as zf:
            zf.extractall(tmpdir)

        outline_keywords = [
            ".gko", ".gml", "outline", "edge", "board", "margin",
            "profile", "dimension",
        ]

        for p in pathlib.Path(tmpdir).rglob("*"):
            if not p.is_file():
                continue
            name_lower = p.name.lower()
            if not any(kw in name_lower for kw in outline_keywords):
                continue
            try:
                geometry = _gerber_to_shapely(p, d)
                if geometry and not geometry.is_empty:
                    if d: d.info(f"Board outline from '{p.name}': {len(geometry.geoms)} polygons")
                    return geometry
            except Exception as e:
                if d: d.warn(f"Outline parse failed '{p.name}': {e}")

    return None


def _clip_layers_with_outline(layers, outline, d=None):
    """Clip each layer's geometry with the board outline."""
    if outline.geom_type in ("LineString", "MultiLineString"):
        if d: d.warn("Outline is lines not polygons, buffering")
        outline = outline.buffer(0.01)
        if outline.is_empty:
            if d: d.warn("Could not convert outline to polygon, skipping")
            return

    if isinstance(outline, shapely.geometry.Polygon):
        outline = shapely.geometry.MultiPolygon([outline])
    elif not isinstance(outline, shapely.geometry.MultiPolygon):
        polys = [g for g in getattr(outline, 'geoms', [])
                 if isinstance(g, shapely.geometry.Polygon)]
        if polys:
            outline = shapely.geometry.MultiPolygon(polys)
        else:
            if d: d.warn(f"Outline has no polygons ({outline.geom_type}), skipping")
            return

    if outline.is_empty:
        if d: d.warn("Outline empty, skipping")
        return

    # Board outline Gerbers (.GKO/.GML) draw the board edge as a closed
    # line stroke.  This produces a thin frame polygon whose filled area
    # is only the stroke width.  For clipping copper layers to the board
    # area we need a filled polygon covering the board interior.
    #
    # Step 1: strip interior rings (holes) from outline polygons.
    filled = []
    for g in outline.geoms:
        if g.geom_type == 'Polygon':
            filled.append(shapely.geometry.Polygon(g.exterior.coords))
    if filled:
        outline = shapely.geometry.MultiPolygon(filled)
        if d: d.info(f"Stripped outline interior rings → {len(filled)} polygon(s)")

    # Step 2: detect thin frame outlines (area << bounding box area).
    # A thin frame means the outline is a line stroke, not a filled board
    # area.  In this case, use the bounding box rectangle for clipping.
    outline_area = outline.area
    ob = outline.bounds  # (minx, miny, maxx, maxy)
    bbox_area = (ob[2] - ob[0]) * (ob[3] - ob[1])
    if bbox_area > 0 and outline_area / bbox_area < 0.5:
        if d: d.info(f"Outline is thin frame (area={outline_area:.2f}, bbox={bbox_area:.2f}, "
                     f"ratio={outline_area/bbox_area:.4f}), using bounding box for clipping")
        outline = shapely.geometry.MultiPolygon([
            shapely.geometry.Polygon([(ob[0], ob[1]), (ob[2], ob[1]),
                                      (ob[2], ob[3]), (ob[0], ob[3])])
        ])

    outline_bounds = outline.bounds
    for layer in layers:
        layer_bounds = layer.shape.bounds
        orig_area = layer.shape.area
        if d: d.info(f"Clipping '{layer.name}': layer={layer_bounds}, outline={outline_bounds}")
        clipped = layer.shape.intersection(outline)
        if clipped.is_empty:
            if d: d.warn(f"Layer '{layer.name}': empty after clipping, keeping original")
            continue
        if isinstance(clipped, shapely.geometry.Polygon):
            clipped = shapely.geometry.MultiPolygon([clipped])
        elif not isinstance(clipped, shapely.geometry.MultiPolygon):
            polys = [g for g in clipped.geoms if isinstance(g, shapely.geometry.Polygon)]
            if polys:
                clipped = shapely.geometry.MultiPolygon(polys)
            else:
                if d: d.warn(f"Layer '{layer.name}': no polygons after clipping, keeping original")
                continue
        # Safety check: if clipping removes >90% of the copper area,
        # the outline is likely wrong — keep the original geometry.
        clipped_area = clipped.area
        if orig_area > 0 and clipped_area / orig_area < 0.1:
            if d: d.warn(f"Layer '{layer.name}': clipping removed {100*(1-clipped_area/orig_area):.1f}% "
                         f"of copper ({clipped_area:.1f}/{orig_area:.1f} mm²), keeping original")
            continue
        object.__setattr__(layer, 'shape', clipped)
        object.__setattr__(layer, 'geoms', tuple(clipped.geoms))
        if d: d.info(f"Layer '{layer.name}': clipped OK ({len(clipped.geoms)} polygons)")


# ============================================================
# Via Processing
# ============================================================

@dataclass(frozen=True)
class ViaSpec:
    point: shapely.geometry.Point
    drill_diameter: float
    layer_names: list[str]
    net: str = ""
    via_type: str = ""  # "", "Blind: Top-Layer2", "Buried: Layer2-Layer5", etc.
    shape: shapely.geometry.Polygon = field(init=False)

    def __post_init__(self):
        radius = self.drill_diameter / 2
        # 使用更多分段以保持圆形精度：小孔24段，大孔32段
        quad_segs = 24 if radius < 0.5 else 32
        shape = shapely.geometry.Point(self.point).buffer(radius, quad_segs=quad_segs)
        object.__setattr__(self, 'shape', shape)

    def compute_resistance(self, length, plating_thickness, conductivity):
        outer_r = self.drill_diameter / 2
        inner_r = outer_r - plating_thickness
        if inner_r <= 0 or conductivity <= 0:
            return 1e-3
        return length / (conductivity * math.pi * (outer_r ** 2 - inner_r ** 2))


# Global counter for via fallback statistics
_via_fallback_stats = {
    'total_vias': 0,
    'vias_with_fallback': 0,
    'fallback_by_net': {},
    'fallback_by_layer': {},
    'via_types': {},  # Track via types: 'through', 'blind', 'buried', 'unknown'
}


# Per-layer offset tracking
_layer_offset_vectors = {}  # {layer_name: [(dx, dy), ...]}

def _reset_via_fallback_stats():
    """Reset via fallback statistics counters."""
    global _via_fallback_stats, _layer_offset_vectors
    _via_fallback_stats = {
        'total_vias': 0,
        'vias_with_fallback': 0,
        'fallback_by_net': {},
        'fallback_by_layer': {},
        'via_types': {},
    }
    _layer_offset_vectors = {}


def _get_via_fallback_summary(d=None):
    """Log summary of via fallback statistics."""
    global _via_fallback_stats
    if _via_fallback_stats['total_vias'] == 0:
        return

    total = _via_fallback_stats['total_vias']
    with_fallback = _via_fallback_stats['vias_with_fallback']
    pct = 100 * with_fallback / total if total > 0 else 0

    if d:
        d.info(f"Via connection summary: {with_fallback}/{total} ({pct:.1f}%) used fallback")

        # Report via types (通孔/盲孔/埋孔)
        if _via_fallback_stats['via_types']:
            d.info("Via types:")
            for via_type, count in sorted(_via_fallback_stats['via_types'].items(),
                                          key=lambda x: -x[1]):
                type_name = {
                    'through': '通孔',
                    'blind': '盲孔',
                    'buried': '埋孔',
                    'unknown': '未知'
                }.get(via_type, via_type)
                d.info(f"  {type_name}: {count} via(s)")

        # Report by network
        if _via_fallback_stats['fallback_by_net']:
            d.info("Fallbacks by network:")
            for net, count in sorted(_via_fallback_stats['fallback_by_net'].items()):
                d.info(f"  {net}: {count} via(s)")

        # Report by layer
        if _via_fallback_stats['fallback_by_layer']:
            d.info("Fallbacks by layer:")
            for layer, count in sorted(_via_fallback_stats['fallback_by_layer'].items(),
                                       key=lambda x: -x[1]):
                d.info(f"  {layer}: {count} connection(s)")

        # Analyze and report layer offsets
        global _layer_offset_vectors
        if _layer_offset_vectors and d:
            d.info("Layer offset analysis (average fallback vectors):")
            for layer_name, offsets in sorted(_layer_offset_vectors.items()):
                if len(offsets) >= 3:  # Only report layers with multiple samples
                    avg_dx = sum(o[0] for o in offsets) / len(offsets)
                    avg_dy = sum(o[1] for o in offsets) / len(offsets)
                    avg_dist = sum(math.sqrt(o[0]**2 + o[1]**2) for o in offsets) / len(offsets)
                    std_dev = math.sqrt(sum((math.sqrt(o[0]**2 + o[1]**2) - avg_dist)**2 for o in offsets) / len(offsets))
                    d.info(f"  {layer_name}: offset=({avg_dx:+.4f}, {avg_dy:+.4f})mm, "
                           f"avg_dist={avg_dist:.4f}mm, std={std_dev:.4f}mm, n={len(offsets)}")

    return _via_fallback_stats


def _extract_via_specs_from_config(vias_data, layer_dict, transform=None):
    """Build ViaSpec list from frontend config."""
    specs = []
    for v in vias_data:
        x, y = v["x"], v["y"]
        if transform:
            sx, sy, ox, oy = transform
            x = x * sx + ox
            y = y * sy + oy
        point = shapely.geometry.Point(x, y)
        layer_names = v.get("layer_names", [])
        # Only include layers that exist in our Gerber data
        valid_layers = [n for n in layer_names if n in layer_dict]
        if not valid_layers:
            continue
        specs.append(ViaSpec(
            point=point,
            drill_diameter=v["hole_diameter"],
            layer_names=valid_layers,
            net=v.get("net", ""),
            via_type=v.get("via_type", ""),
        ))
    return specs


def _punch_via_holes(layers, via_specs):
    """Subtract via drill holes from layer geometry.

    Only merge holes that actually overlap - keep closely-spaced but
    non-overlapping holes separate to preserve copper between them.
    """
    if not via_specs:
        return
    for layer in layers:
        layer_holes = [(vs, vs.shape) for vs in via_specs if layer.name in vs.layer_names]
        if not layer_holes:
            continue

        # 智能合并：只合并真正重叠的孔
        # 使用基于图的聚类算法，将重叠的孔分组
        merged_groups = []
        used = set()

        for i, (via_i, hole_i) in enumerate(layer_holes):
            if i in used:
                continue

            # 找到所有与当前孔重叠的孔
            group = [hole_i]
            used.add(i)

            for j, (via_j, hole_j) in enumerate(layer_holes):
                if j <= i or j in used:
                    continue

                # 检查是否重叠（距离 < 孔径之和的某个比例）
                # 如果两孔距离小于孔半径，视为重叠
                dist = hole_i.distance(hole_j)
                radius_i = via_i.drill_diameter / 2
                radius_j = via_j.drill_diameter / 2
                min_radius = min(radius_i, radius_j)

                # 如果距离 < 最小孔半径，说明两孔重叠或非常接近
                if dist < min_radius:
                    group.append(hole_j)
                    used.add(j)

            # 如果组内有多个孔，合并它们
            if len(group) > 1:
                merged_group = shapely.unary_union(group)
                merged_groups.append(merged_group)
            else:
                merged_groups.append(hole_i)

        # 用合并后的孔组打铜皮
        if merged_groups:
            union_holes = shapely.unary_union(merged_groups)
            new_shape = layer.shape.difference(union_holes)
            if new_shape.is_empty:
                continue
            if isinstance(new_shape, shapely.geometry.Polygon):
                new_shape = shapely.geometry.MultiPolygon([new_shape])
            elif not isinstance(new_shape, shapely.geometry.MultiPolygon):
                polys = [g for g in new_shape.geoms if isinstance(g, shapely.geometry.Polygon)]
                new_shape = shapely.geometry.MultiPolygon(polys) if polys else layer.shape
            object.__setattr__(layer, 'shape', new_shape)
            object.__setattr__(layer, 'geoms', tuple(new_shape.geoms))


def _build_stackup_from_config(config_data, layers):
    """Build stackup thickness list from config."""
    layer_cu_thickness = config_data.get("layer_cu_thickness", {})
    stackup = []
    for layer in layers:
        t = layer_cu_thickness.get(layer.name, 0.035)
        stackup.append(t)
    return stackup


def _process_via_spec(via_spec, layer_dict, stackup, d=None):
    """Build resistor networks for a via (center-point model).

    Args:
        via_spec: ViaSpec with point, drill_diameter, layer_names, net, via_type
        layer_dict: Dict mapping layer names to problem.Layer objects
        stackup: List of copper thicknesses per layer
        d: Optional DiagCollector for diagnostic logging

    Returns:
        List of problem.Network objects representing via resistances
    """
    global _via_fallback_stats
    networks = []
    via_center = via_spec.point
    via_layers = [(i, layer_dict[n]) for i, n in enumerate(via_spec.layer_names)
                  if n in layer_dict]

    # Filter out layers that don't have copper for this network (no intersection with via center)
    # This avoids connecting to other networks' copper on layers where this network doesn't exist
    original_layer_count = len(via_layers)
    via_layers = [(i, layer) for i, layer in via_layers
                  if layer.shape.intersects(via_center)]
    filtered_count = original_layer_count - len(via_layers)

    if d and filtered_count > 0:
        d.info(f"Via '{via_spec.net}' @ ({via_center.x:.3f},{via_center.y:.3f}): "
               f"filtered {filtered_count} layer(s) with no copper for this network "
               f"({len(via_layers)} remaining)")

    if len(via_layers) < 2:
        if d:
            d.warn(f"Via '{via_spec.net}' @ ({via_center.x:.3f},{via_center.y:.3f}): "
                   f"only {len(via_layers)} layer(s) available after filtering, skipped")
        return networks

    # Update total via counter
    _via_fallback_stats['total_vias'] += 1

    # Classify via type (通孔/盲孔/埋孔)
    via_type = 'unknown'
    if via_spec.via_type:
        vt_lower = via_spec.via_type.lower()
        if 'blind' in vt_lower:
            via_type = 'blind'
        elif 'buried' in vt_lower:
            via_type = 'buried'
        else:
            via_type = 'through'
    else:
        # Empty via_type means through-hole via (connects all layers)
        via_type = 'through'
    _via_fallback_stats['via_types'][via_type] = \
        _via_fallback_stats['via_types'].get(via_type, 0) + 1

    # Build via info for diagnostics
    via_info = {
        'net': via_spec.net,
        'x': via_center.x,
        'y': via_center.y,
        'drill': via_spec.drill_diameter,
        'type': via_type,
    }

    # Track fallback usage for this via
    fallback_layers = []
    direct_layers = []
    via_used_fallback = False

    # Use via center as the single connection point on each layer
    for pair_i in range(len(via_layers) - 1):
        idx_a, layer_a = via_layers[pair_i]
        idx_b, layer_b = via_layers[pair_i + 1]
        thickness_a = stackup[idx_a] if idx_a < len(stackup) else 0.035
        thickness_b = stackup[idx_b] if idx_b < len(stackup) else 0.035
        length = (thickness_a + thickness_b) / 2
        plating = 0.025  # mm typical plating thickness
        total_resistance = via_spec.compute_resistance(length, plating, 5.95e4)

        # FEM point: nearest point on copper to via center (on annular ring)
        # Display point: via center (for visualization)
        nearest_a, used_fallback_a = _find_nearest_point_on_layer(
            via_center, layer_a, d, via_info)
        nearest_b, used_fallback_b = _find_nearest_point_on_layer(
            via_center, layer_b, d, via_info)

        # Track which layers used fallback
        if used_fallback_a:
            fallback_layers.append(layer_a.name)
            # Update global stats
            _via_fallback_stats['fallback_by_layer'][layer_a.name] = \
                _via_fallback_stats['fallback_by_layer'].get(layer_a.name, 0) + 1
            via_used_fallback = True
        else:
            direct_layers.append(layer_a.name)

        if used_fallback_b:
            fallback_layers.append(layer_b.name)
            # Update global stats
            _via_fallback_stats['fallback_by_layer'][layer_b.name] = \
                _via_fallback_stats['fallback_by_layer'].get(layer_b.name, 0) + 1
            via_used_fallback = True
        else:
            direct_layers.append(layer_b.name)

        # Only create via networks if the via actually lands on copper on both layers
        # Skip if either layer used fallback (no copper for this network)
        if used_fallback_a or used_fallback_b:
            if d:
                d.info(f"Via '{via_spec.net}' @ ({via_center.x:.3f},{via_center.y:.3f}): "
                       f"Skipping {layer_a.name} <-> {layer_b.name} due to fallback "
                       f"(a_fallback={used_fallback_a}, b_fallback={used_fallback_b})")
            continue

        conn_a = problem.Connection(layer=layer_a, point=nearest_a)
        conn_b = problem.Connection(layer=layer_b, point=nearest_b)
        connections = [conn_a, conn_b]
        elements = [problem.Resistor(
            a=conn_a.node_id,
            b=conn_b.node_id,
            resistance=total_resistance,
        )]

        if elements:
            networks.append(problem.Network(
                connections=connections,
                elements=elements,
            ))

    # Update global fallback stats for this via's network
    if via_used_fallback:
        _via_fallback_stats['vias_with_fallback'] += 1
        _via_fallback_stats['fallback_by_net'][via_spec.net] = \
            _via_fallback_stats['fallback_by_net'].get(via_spec.net, 0) + 1

    # Log summary for this via if any fallback was used
    if fallback_layers and d:
        type_cn = {'through': '通孔', 'blind': '盲孔', 'buried': '埋孔', 'unknown': '未知'}.get(via_type, via_type)
        d.warn(f"Via '{via_spec.net}' @ ({via_center.x:.3f},{via_center.y:.3f}): "
               f"type={type_cn}, fallback on {len(fallback_layers)} layer(s): {fallback_layers}")

    return networks


# ============================================================
# Network Building (Voltage Sources, Current Loads)
# ============================================================

def _find_nearest_point_on_layer(point, layer, d=None, via_info=None):
    """Find the nearest point on layer to the given point.

    Returns:
        (point, used_fallback): where used_fallback=True if nearest_points was used
    """
    if layer.shape.intersects(point):
        if d and via_info:
            via_type_label = via_info.get('type', 'unknown')
            type_cn = {'through': '通孔', 'blind': '盲孔', 'buried': '埋孔'}.get(via_type_label, via_type_label)
            d.info(f"Via '{via_info['net']}' @ ({via_info['x']:.3f},{via_info['y']:.3f}) "
                   f"layer '{layer.name}': direct intersection (type={type_cn})")
        return point, False
    _, nearest = shapely.ops.nearest_points(point, layer.shape)
    dist = point.distance(nearest) if hasattr(nearest, 'distance') else float('inf')

    # Check if fallback was used (distance > 0)
    used_fallback = dist > 1e-6
    if used_fallback and d and via_info:
        # Record offset vector for this layer
        global _layer_offset_vectors
        if layer.name not in _layer_offset_vectors:
            _layer_offset_vectors[layer.name] = []
        offset_x = nearest.x - point.x
        offset_y = nearest.y - point.y
        _layer_offset_vectors[layer.name].append((offset_x, offset_y))

        via_type_label = via_info.get('type', 'unknown')
        type_cn = {'through': '通孔', 'blind': '盲孔', 'buried': '埋孔'}.get(via_type_label, via_type_label)
        d.warn(f"Via '{via_info['net']}' @ ({via_info['x']:.3f},{via_info['y']:.3f}) "
               f"layer '{layer.name}': FALLBACK used (dist={dist:.4f}mm, type={type_cn})")
    return nearest, used_fallback


# ============================================================
# Current Capacity Checking
# ============================================================


def _estimate_min_trace_width(layer):
    """从铜皮几何估算最小走线宽度。

    通过计算铜皮在各个方向上的最小"瓶颈"宽度来估算。
    对于简单走线，这近似于走线宽度；对于复杂铜皮，
    返回最窄处的宽度。

    Args:
        layer: problem.Layer 对象

    Returns:
        float: 估算的最小走线宽度，如果无法分析返回默认值 0.2mm
    """
    try:
        import shapely.geometry as geom
        import numpy as np

        if not layer.shape or layer.shape.is_empty:
            return 0.2  # 默认值

        # 获取所有多边形
        geoms = layer.geoms if hasattr(layer, 'geoms') else [layer.shape]

        min_width = float('inf')
        samples_analyzed = 0

        # 采样分析：从每个多边形内部采样点，计算到边界的最小距离
        # 这个距离的两倍就是该点处的"局部宽度"
        for poly in geoms:
            if poly.is_empty or poly.area < 1e-6:
                continue

            try:
                # 对于每个多边形，采样内部点
                # 使用均匀网格采样
                minx, miny, maxx, maxy = poly.bounds
                dx = maxx - minx
                dy = maxy - miny

                # 根据面积决定采样密度
                area = poly.area
                if area < 1:
                    n_samples = 10
                elif area < 10:
                    n_samples = 20
                elif area < 50:
                    n_samples = 40
                else:
                    n_samples = 80

                # 生成网格采样点
                nx = int(np.sqrt(n_samples * dx / (dy + 1e-6))) + 1
                ny = int(n_samples / nx) + 1

                x_step = dx / nx if nx > 0 else dx
                y_step = dy / ny if ny > 0 else dy

                for i in range(nx + 1):
                    for j in range(ny + 1):
                        x = minx + i * x_step
                        y = miny + j * y_step
                        pt = geom.Point(x, y)

                        # 只处理在多边形内部的点
                        if poly.contains(pt) or poly.touches(pt):
                            # 计算到边界的最小距离
                            # 使用 buffer(0) 清理几何可能产生的小孔
                            exterior = poly.exterior
                            if exterior:
                                # 距离外边界最近的点的距离
                                d = exterior.distance(pt)
                                if d > 0 and d < min_width:
                                    min_width = d
                                    samples_analyzed += 1

            except Exception:
                continue

        # 局部宽度的两倍就是走线宽度（从中心到边界是半径）
        if min_width != float('inf') and min_width > 0:
            estimated_width = 2 * min_width
            # 限制在合理范围内 [0.05mm, 10mm]
            estimated_width = max(0.05, min(10, estimated_width))
            return estimated_width

        return 0.2  # 默认值

    except Exception:
        return 0.2  # 默认值


def _check_current_capacities(solution, config_data, layer_dict, d=None):
    """检查各网络各层的电流容量是否超限。

    Args:
        solution: solver.Solution 对象
        config_data: 配置数据
        layer_dict: 层字典 {name: problem.Layer}
        d: DiagCollector (可选)

    Returns:
        list[CurrentCheckOutput]: 电流检查结果列表
    """
    try:
        from . import calculation
    except ImportError:
        try:
            import calculation
        except ImportError:
            if d:
                d.error("calculation module not available")
            return []

    warnings = []

    # 获取配置
    layer_cu_thickness = config_data.get("layer_cu_thickness", {})
    temp_rise = config_data.get("temp_rise", 10.0)  # 默认 10°C 温升
    sources = config_data.get("sources", [])
    loads = config_data.get("loads", [])

    # 获取走线数据（如果有）
    tracks_data = config_data.get("tracks", [])
    if d:
        d.info(f"Got {len(tracks_data)} tracks from EasyEDA")
        if len(tracks_data) > 0:
            d.info(f"First track: net={tracks_data[0].get('net')}, width={tracks_data[0].get('width')}, layer={tracks_data[0].get('layer')}")
        # 显示所有走线的网络
        track_nets = set(t.get('net', 'UNKNOWN') for t in tracks_data)
        d.info(f"Track networks: {list(track_nets)}")

    # 构建网络到电流的映射
    network_currents = {}
    for load in loads:
        net = load.get("net", "")
        current = load.get("current", 0)
        if net and current > 0:
            # 同一网络的多个负载电流累加
            network_currents[net] = network_currents.get(net, 0) + current

    if not network_currents:
        if d:
            d.warn("No load currents configured, skipping current capacity check")
        return warnings

    if d:
        d.info(f"Current capacity check: {len(network_currents)} networks, temp_rise={temp_rise}°C")

    # 构建层ID到层名的映射
    # 从 solution.problem.layers 获取层信息
    # 注意：前端传递的 layer_id 是数字，但这里我们需要层名
    # 我们直接使用 layer_dict 中的层名
    layer_name_to_id = {}
    # 尝试从 config_data 获取层信息
    layer_configs = config_data.get("layers", [])
    for lc in layer_configs:
        layer_name = lc.get("name")
        layer_id = lc.get("layer_id")
        if layer_name and layer_id is not None:
            layer_name_to_id[layer_name] = layer_id

    # 计算每个网络在每层的最小走线宽度
    # 格式: {net_name: {layer_name: min_width_mm}}
    network_min_widths = {}
    for track in tracks_data:
        net = track.get("net", "")
        if not net or net not in network_currents:
            continue
        layer_id = track.get("layer")
        # ���过 layer_id 找到对应的层名
        layer_name = None
        for lname, lid in layer_name_to_id.items():
            if lid == layer_id:
                layer_name = lname
                break
        if not layer_name or layer_name not in layer_dict:
            continue
        width = track.get("width", 0)
        if width <= 0:
            continue

        if net not in network_min_widths:
            network_min_widths[net] = {}
        if layer_name not in network_min_widths[net]:
            network_min_widths[net][layer_name] = width
        else:
            network_min_widths[net][layer_name] = min(network_min_widths[net][layer_name], width)

    if d and network_min_widths:
        d.info(f"Extracted track widths for {len(network_min_widths)} networks")

    # 对每个网络、每个层进行检查
    # 只检查该网络实际存在的层
    trace_width_summary = []

    # 先统计每个网络在哪些层存在
    # {net_name: set(layer_names)}
    network_layers = {}
    for net, widths_by_layer in network_min_widths.items():
        network_layers[net] = set(widths_by_layer.keys())

    # 添加几何分析的结果（检查网络在哪些层有铜皮）
    for net_name in network_currents.keys():
        if net_name not in network_layers:
            network_layers[net_name] = set()
        for layer_name, layer in layer_dict.items():
            if layer.shape.area >= 1e-6:
                # 检查该网络是否在此层有连接（通过连接点判断）
                has_connection = False
                for network in solution.problem.networks:
                    for conn in network.connections:
                        # 检查连接点是否在该层且与该网络相关
                        if conn.layer.name == layer_name:
                            # 简化判断：假设该层有铜皮，网络就可能存在
                            has_connection = True
                            break
                    if has_connection:
                        break
                if has_connection:
                    network_layers[net_name].add(layer_name)

    if d:
        d.info(f"Network layers: {[(n, list(ls)) for n, ls in network_layers.items()]}")

    for layer_name, layer in layer_dict.items():
        # 获取该层铜厚
        cu_mm = layer_cu_thickness.get(layer_name, 0.035)
        cu_oz = cu_mm / calculation.OZ_TO_MM

        # 判断是否为外层
        is_outer = any(kw in layer_name.lower() for kw in ["top", "bottom"])

        # 获取该层所有网络的最小走线宽度
        layer_min_width = None
        for net, widths_by_layer in network_min_widths.items():
            if layer_name in widths_by_layer:
                w = widths_by_layer[layer_name]
                if layer_min_width is None or w < layer_min_width:
                    layer_min_width = w

        # 如果没有走线数据，使用几何估算
        if layer_min_width is None:
            layer_min_width = _estimate_min_trace_width(layer)
            width_source = "几何估算"
        else:
            width_source = "走线提取"

        # 只显示有网络的层的线宽摘要
        layer_has_networks = False
        for net_name in network_currents.keys():
            if layer_name in network_layers.get(net_name, set()):
                layer_has_networks = True
                break

        if layer_has_networks:
            trace_width_summary.append(
                f"  {layer_name} ({'外层' if is_outer else '内层'}, {cu_oz:.2f}oz铜): 最小线宽 ≈ {layer_min_width:.3f}mm ({width_source})"
            )

        if d and layer_has_networks:
            d.info(f"Layer '{layer_name}': trace_width≈{layer_min_width:.3f}mm ({width_source}), cu={cu_oz:.2f}oz, outer={is_outer}")

        # 检查该层上每个网络的电流（只检查该网络存在的层）
        for net_name, current in network_currents.items():
            # 跳过该网络不存在的层
            if layer_name not in network_layers.get(net_name, set()):
                continue

            # 检查该层是否有铜皮
            if layer.shape.area < 1e-6:
                continue

            # 使用该网络在此层的实际走线宽度，如果没有则使用层的最小宽度
            trace_width = layer_min_width
            if net_name in network_min_widths and layer_name in network_min_widths[net_name]:
                trace_width = network_min_widths[net_name][layer_name]

            try:
                result = calculation.check_current_capacity(
                    network_name=net_name,
                    layer_name=layer_name,
                    calculated_current=current,
                    trace_width_mm=trace_width,
                    copper_thickness_mm=cu_mm,
                    temp_rise=temp_rise,
                    is_outer_layer=is_outer,
                    safety_margin=1.0,  # 100% 利用率算超限
                )

                # 只报告有问题的情况（超限或接近上限）
                if result.is_exceeded or result.utilization > 0.8:
                    warnings.append(CurrentCheckOutput(**result.to_dict()))

                    if d:
                        if result.is_exceeded:
                            d.error(result.message)
                        elif result.utilization > 0.8:
                            d.warn(result.message)

            except Exception as e:
                if d:
                    d.warn(f"Current check failed for {net_name}@{layer_name}: {e}")

    # 将走线宽度摘要作为第一个警告条目（特殊标记）
    if trace_width_summary:
        summary_warning = CurrentCheckOutput(
            network_name="[线宽摘要]",
            layer_name="全部",
            calculated_current=0,
            max_allowed_current=0,
            utilization=0,
            is_exceeded=False,
            trace_width_mm=0,
            copper_oz=0,
            temp_rise=temp_rise,
            message="\n".join(trace_width_summary),
        )
        # 将摘要插入到 warnings 的开头
        warnings.insert(0, summary_warning)

    if warnings:
        exceeded = [w for w in warnings if w.is_exceeded]
        if d:
            d.info(f"Current capacity check complete: {len(warnings)} warnings, {len(exceeded)} exceeded")

    return warnings


# ============================================================
# Short Circuit Detection
# ============================================================

def _detect_short_circuits(ground_current, config_data, layer_dict, d=None):
    """Detect potential short circuits when ground current is abnormally high.

    Args:
        ground_current: The ground node current from solver
        config_data: Configuration data containing via and network info
        layer_dict: Dictionary of layer names to Layer objects
        d: Optional DiagCollector for diagnostic logging
    """
    if ground_current is None or d is None:
        return

    # Threshold for "abnormally high" ground current
    # For typical PCBs, ground current should be < 0.001A (near zero)
    # Values > 0.1A indicate significant leakage/short
    HIGH_CURRENT_THRESHOLD = 0.1

    abs_current = abs(ground_current)
    if abs_current < HIGH_CURRENT_THRESHOLD:
        return

    d.warn(f"⚠️  高地电流检测: {abs_current:.6f}A (阈值: {HIGH_CURRENT_THRESHOLD}A)")
    d.warn("   可能存在VCC与GND之间的短路连接！")

    # Analyze vias to find potential short circuits
    vias_data = config_data.get("vias", [])
    gnd_net = config_data.get("gnd_net", "")
    rails = config_data.get("rails", [])
    sources = config_data.get("sources", [])

    # Get all VCC network names (non-GND networks)
    vcc_nets = set()
    for rail in rails:
        vcc_nets.add(rail.get("net", ""))
    # Also get from sources (fallback)
    for src in sources:
        vcc_nets.add(src.get("net", ""))
    if gnd_net:
        vcc_nets.discard(gnd_net)

    if not vias_data:
        d.warn("   此设计没有过孔，短路可能来自其他原因")
        return

    d.warn(f"   分析 {len(vias_data)} 个过孔以定位潜在短路点...")

    # For each layer, determine which network "owns" it based on via connections
    layer_network_ownership = {}  # {layer_name: {net: count}}
    for layer_name in layer_dict.keys():
        layer_network_ownership[layer_name] = {}

    # Count via connections per layer per network
    via_layer_connections = []  # [(via_net, layer_name, x, y), ...]

    for v in vias_data:
        via_net = v.get("net", "")
        x, y = v["x"], v["y"]
        layer_names = v.get("layer_names", [])

        for layer_name in layer_names:
            if layer_name in layer_dict:
                layer_network_ownership[layer_name][via_net] = \
                    layer_network_ownership[layer_name].get(via_net, 0) + 1
                via_layer_connections.append((via_net, layer_name, x, y))

    # Determine primary network for each layer
    layer_primary_net = {}
    for layer_name, net_counts in layer_network_ownership.items():
        if net_counts:
            primary_net = max(net_counts.items(), key=lambda x: x[1])[0]
            layer_primary_net[layer_name] = primary_net

    # Find and report suspicious vias (VCC via on GND-dominant layer, or vice versa)
    # Only check vias that are clearly power/ground networks to reduce false positives
    suspicious_vias = []

    for via_net, layer_name, x, y in via_layer_connections:
        primary_net = layer_primary_net.get(layer_name)
        if not primary_net:
            continue

        # Skip if via network is neither GND nor a known VCC network
        # This ignores signal vias and other non-power networks
        is_via_gnd = via_net == gnd_net or (gnd_net and via_net == gnd_net)
        is_via_vcc = via_net in vcc_nets
        is_layer_gnd = primary_net == gnd_net or (gnd_net and primary_net == gnd_net)
        is_layer_vcc = primary_net in vcc_nets

        # Only flag as suspicious if both via and layer are clearly power/ground networks
        # and they don't match (e.g., VCC via on GND layer, or vice versa)
        if not (is_via_gnd or is_via_vcc):
            continue  # Skip signal vias or unknown networks
        if not (is_layer_gnd or is_layer_vcc):
            continue  # Skip layers with unknown primary network

        # Check mismatch
        if (is_via_gnd and is_layer_vcc) or (is_via_vcc and is_layer_gnd):
            suspicious_vias.append({
                'via_net': via_net,
                'layer': layer_name,
                'layer_primary_net': primary_net,
                'x': x,
                'y': y
            })

    if suspicious_vias:
        d.warn(f"   发现 {len(suspicious_vias)} 个可疑过孔连接 (网络不匹配):")
        # Group by location to avoid duplicates
        seen_locations = set()
        for sv in suspicious_vias[:20]:  # Limit to first 20
            loc_key = (round(sv['x'], 3), round(sv['y'], 3))
            if loc_key in seen_locations:
                continue
            seen_locations.add(loc_key)
            d.warn(f"   过孔 '{sv['via_net']}' @ ({sv['x']:.3f}, {sv['y']:.3f}): "
                   f"连接到层 '{sv['layer']}' (主网络: {sv['layer_primary_net']})")
        if len(suspicious_vias) > 20:
            d.warn(f"   ... 还有 {len(suspicious_vias) - 20} 个可疑过孔")
    else:
        d.warn("   未发现明显的过孔网络不匹配")
        d.warn("   短路可能来自铜皮重叠或Gerber导出问题")

    # ── 铜皮网络分布分析 ──
    d.warn("")
    d.warn("   铜皮网络分布 (基于过孔/焊盘连接):")
    for layer_name in sorted(layer_dict.keys()):
        if layer_name not in layer_network_ownership:
            continue
        net_counts = layer_network_ownership[layer_name]
        if not net_counts:
            d.warn(f"   层 '{layer_name}': 无连接数据")
            continue

        total = sum(net_counts.values())
        # Sort networks by connection count
        sorted_nets = sorted(net_counts.items(), key=lambda x: -x[1])
        net_str = ", ".join([f"{net}({cnt}/{total})" for net, cnt in sorted_nets[:5]])
        if len(sorted_nets) > 5:
            net_str += f" +{len(sorted_nets)-5}个其他网络"

        # Check if this layer has mixed power networks
        has_vcc = any(net in vcc_nets for net in net_counts.keys())
        has_gnd = (net_counts.get(gnd_net, 0) > 0) if gnd_net else False

        status = ""
        if has_vcc and has_gnd:
            status = " ⚠️ 混合电源网络层"
        elif has_vcc:
            status = " (电源层)"
        elif has_gnd:
            status = " (地层)"

        d.warn(f"   层 '{layer_name}': {net_str}{status}")

    # ── 铜皮重叠检测 (基于连接点推断) ──
    d.warn("")
    d.warn("   铜皮重叠检测:")
    pads_data = config_data.get("pads", [])
    connection_points = {}  # {layer_name: {net: [points]}}
    for pad in pads_data:
        pad_net = pad.get("net", "")
        pad_layer = pad.get("layer", "")
        if pad_net and pad_layer:
            if pad_layer not in connection_points:
                connection_points[pad_layer] = {}
            if pad_net not in connection_points[pad_layer]:
                connection_points[pad_layer][pad_net] = []
            connection_points[pad_layer][pad_net].append((pad["x"], pad["y"]))

    # Check for layers with both VCC and GND connection points close together
    import math
    spatial_conflicts = []
    for layer_name, net_points in connection_points.items():
        if layer_name not in layer_dict:
            continue
        layer_obj = layer_dict[layer_name]

        # Get VCC and GND points for this layer
        vcc_points = []
        for vcc_net in vcc_nets:
            vcc_points.extend(net_points.get(vcc_net, []))
        gnd_points = net_points.get(gnd_net, []) if gnd_net else []

        if not vcc_points or not gnd_points:
            continue

        # Check if any VCC point is very close to a GND point (< 1mm)
        # This could indicate copper pour overlap
        for vx, vy in vcc_points[:50]:  # Limit check to first 50 points
            vpt = shapely.geometry.Point(vx, vy)
            for gx, gy in gnd_points[:50]:
                gpt = shapely.geometry.Point(gx, gy)
                dist = vpt.distance(gpt)
                if dist < 1.0:  # Within 1mm
                    spatial_conflicts.append({
                        'layer': layer_name,
                        'vcc_point': (vx, vy),
                        'gnd_point': (gx, gy),
                        'distance': dist
                    })
                    break  # One conflict per VCC point is enough
            if len(spatial_conflicts) >= 5:
                break  # Limit total conflicts reported
        if len(spatial_conflicts) >= 5:
            break

    if spatial_conflicts:
        d.warn(f"   发现 {len(spatial_conflicts)} 个疑似铜皮重叠:")
        for sc in spatial_conflicts[:5]:
            d.warn(f"   层 '{sc['layer']}': VCC点({sc['vcc_point'][0]:.2f},{sc['vcc_point'][1]:.2f}) "
                   f"距GND点({sc['gnd_point'][0]:.2f},{sc['gnd_point'][1]:.2f}) 仅{sc['distance']*1000:.1f}mm")
    else:
        d.warn("   未发现明显的电源-地连接点空间冲突")

    # ── 焊盘网络检查（基于空间位置推断）──
    d.warn("")
    d.warn("   焊盘网络检查 (基于空间位置):")
    sources = config_data.get("sources", [])
    loads = config_data.get("loads", [])

    # Helper: Infer network at a specific location based on nearby connection points
    def infer_network_at_location(x, y, layer_name, search_radius=2.0):
        """Infer which network dominates at a specific location.
        Returns: (dominant_net, confidence) where confidence is ratio of dominant/total
        """
        if layer_name not in layer_dict:
            return None, 0

        layer_obj = layer_dict[layer_name]
        pt = shapely.geometry.Point(x, y)

        # Check if point is within copper
        if not layer_obj.shape.intersects(pt):
            return None, 0  # Not on copper

        # Count nearby vias and pads by network
        nearby_net_counts = {}
        for via_net, v_layer, vx, vy in via_layer_connections:
            if v_layer != layer_name:
                continue
            dist = ((vx - x)**2 + (vy - y)**2)**0.5
            if dist <= search_radius:
                nearby_net_counts[via_net] = nearby_net_counts.get(via_net, 0) + 1

        # Also count pads
        all_pads = pads_data if 'pads_data' in dir() else []
        for pad in all_pads:
            pad_layer = pad.get("layer", "")
            if pad_layer != layer_name:
                continue
            pad_net = pad.get("net", "")
            if not pad_net:
                continue
            px, py = pad["x"], pad["y"]
            dist = ((px - x)**2 + (py - y)**2)**0.5
            if dist <= search_radius:
                nearby_net_counts[pad_net] = nearby_net_counts.get(pad_net, 0) + 1

        if not nearby_net_counts:
            return "UNKNOWN", 0

        total = sum(nearby_net_counts.values())
        dominant_net = max(nearby_net_counts.items(), key=lambda x: x[1])
        confidence = dominant_net[1] / total if total > 0 else 0

        return dominant_net[0], confidence

    # Get all pads for reference
    pads_data = config_data.get("pads", [])

    # Check source pads
    for src in sources:
        src_net = src.get("net", "")
        src_gnd_net = src.get("gnd_net", gnd_net)
        src_pads = src.get("pads", [])
        src_gnd_pads = src.get("gnd_pads", [])

        d.warn(f"   电源源 '{src.get('ref_des', 'Unknown')}' ({src_net} @ {src.get('voltage', 0)}V):")

        # Check VCC pads - are they on VCC copper or GND copper?
        vcc_on_gnd = []
        vcc_on_vcc = []
        vcc_no_copper = []

        for pad in src_pads[:20]:  # Limit to first 20
            pad_layer = pad.get("layer", "")
            pad_x, pad_y = pad["x"], pad["y"]

            if not pad_layer or pad_layer not in layer_dict:
                vcc_no_copper.append((pad_x, pad_y, pad_layer))
                continue

            inferred_net, confidence = infer_network_at_location(pad_x, pad_y, pad_layer)

            if inferred_net is None:
                vcc_no_copper.append((pad_x, pad_y, pad_layer))
            elif inferred_net == gnd_net or (gnd_net and "GND" in inferred_net.upper()):
                vcc_on_gnd.append((pad_x, pad_y, pad_layer, inferred_net, confidence))
            elif inferred_net == src_net:
                vcc_on_vcc.append((pad_x, pad_y, pad_layer, confidence))

        # Report VCC pads
        if vcc_on_gnd:
            d.warn(f"     ⚠️ VCC焊盘落在GND铜皮区域 ({len(vcc_on_gnd)} 个):")
            for x, y, layer, net, conf in vcc_on_gnd[:5]:
                d.warn(f"       @ ({x:.2f},{y:.2f}) 層'{layer}' 推断网络:{net} (置信度:{conf:.1%})")
            if len(vcc_on_gnd) > 5:
                d.warn(f"       ... 还有 {len(vcc_on_gnd) - 5} 个")

        if vcc_no_copper:
            d.warn(f"     ⚠️ VCC焊盘未落在铜皮上 ({len(vcc_no_copper)} 个):")
            for x, y, layer in vcc_no_copper[:3]:
                d.warn(f"       @ ({x:.2f},{y:.2f}) 層'{layer or '未知'}'")
            if len(vcc_no_copper) > 3:
                d.warn(f"       ... 还有 {len(vcc_no_copper) - 3} 个")

        if vcc_on_vcc:
            d.warn(f"     ✓ VCC焊盘正常 ({len(vcc_on_vcc)} 个，落在VCC铜皮上)")
        elif not vcc_on_gnd and not vcc_no_copper:
            d.warn(f"     ✓ VCC焊盘正常 ({len(src_pads)} 个)")

        # Check GND pads
        gnd_on_vcc = []
        gnd_on_gnd = []

        for pad in src_gnd_pads[:10]:
            pad_layer = pad.get("layer", "")
            pad_x, pad_y = pad["x"], pad["y"]

            if not pad_layer or pad_layer not in layer_dict:
                continue

            inferred_net, confidence = infer_network_at_location(pad_x, pad_y, pad_layer)

            if inferred_net in vcc_nets:
                gnd_on_vcc.append((pad_x, pad_y, pad_layer, inferred_net))
            elif inferred_net == gnd_net or (gnd_net and "GND" in inferred_net.upper()):
                gnd_on_gnd.append((pad_x, pad_y, pad_layer, confidence))

        if gnd_on_vcc:
            d.warn(f"     ⚠️ GND焊盘落在VCC铜皮区域 ({len(gnd_on_vcc)} 个):")
            for x, y, layer, net in gnd_on_vcc[:3]:
                d.warn(f"       @ ({x:.2f},{y:.2f}) 層'{layer}' 推断网络:{net}")
            if len(gnd_on_vcc) > 3:
                d.warn(f"       ... 还有 {len(gnd_on_vcc) - 3} 个")
        else:
            d.warn(f"     ✓ GND焊盘正常 ({len(src_gnd_pads)} 个)")

    # Check load pads
    for load in loads:
        load_net = load.get("net", "")
        load_pads = load.get("pads", [])

        d.warn(f"   负载 '{load.get('ref_des', 'Unknown')}' ({load_net} @ {load.get('current', 0)}A):")

        load_on_gnd = []
        load_on_vcc = []

        for pad in load_pads[:10]:
            pad_layer = pad.get("layer", "")
            pad_x, pad_y = pad["x"], pad["y"]

            if not pad_layer or pad_layer not in layer_dict:
                continue

            inferred_net, confidence = infer_network_at_location(pad_x, pad_y, pad_layer)

            if inferred_net is None:
                continue
            elif inferred_net == gnd_net or (gnd_net and "GND" in inferred_net.upper()):
                load_on_gnd.append((pad_x, pad_y, pad_layer, inferred_net, confidence))
            elif inferred_net == load_net:
                load_on_vcc.append((pad_x, pad_y, pad_layer, confidence))

        if load_on_gnd:
            d.warn(f"     ⚠️ 电源焊盘落在GND铜皮区域 ({len(load_on_gnd)} 个):")
            for x, y, layer, net, conf in load_on_gnd[:3]:
                d.warn(f"       @ ({x:.2f},{y:.2f}) 層'{layer}' 推断网络:{net} (置信度:{conf:.1%})")
            if len(load_on_gnd) > 3:
                d.warn(f"       ... 还有 {len(load_on_gnd) - 3} 个")
        elif load_on_vcc:
            d.warn(f"     ✓ 电源焊盘正常 ({len(load_on_vcc)} 个，落在VCC铜皮上)")
        else:
            d.warn(f"     ✓ 电源焊盘正常 ({len(load_pads)} 个)")

    # ── VCC 过孔在各层的连接检查 ──
    d.warn("")
    d.warn("   VCC过孔铜皮连接检查:")
    for vcc_net in vcc_nets:
        # Get VCC vias
        vcc_vias = []
        for v in vias_data:
            if v.get("net", "") == vcc_net:
                vcc_vias.append(v)
        if not vcc_vias:
            continue

        d.warn(f"   网络 '{vcc_net}' ({len(vcc_vias)} 个过孔):")

        # Check each via on each layer
        for layer_name, layer_obj in layer_dict.items():
            connected_count = 0
            not_on_copper = []

            for v in vcc_vias[:5]:  # Check first 5 vias
                x, y = v["x"], v["y"]
                pt = shapely.geometry.Point(x, y)
                layer_names = v.get("layer_names", [])

                if layer_name not in layer_names:
                    continue  # Via doesn't connect to this layer

                if layer_obj.shape.intersects(pt):
                    connected_count += 1
                else:
                    not_on_copper.append((x, y))

            total_vias_on_layer = sum(1 for v in vcc_vias[:5] if layer_name in v.get("layer_names", []))
            if total_vias_on_layer > 0:
                ratio = connected_count / total_vias_on_layer if total_vias_on_layer > 0 else 0
                status = "✓" if ratio >= 0.8 else "⚠️" if ratio >= 0.5 else "❌"
                d.warn(f"     層 '{layer_name}': {status} {connected_count}/{total_vias_on_layer} 过孔在铜���上 ({ratio:.0%})")
                if not_on_copper:
                    for x, y in not_on_copper[:2]:
                        d.warn(f"       过孔 @ ({x:.2f},{y:.2f}) 不在铜皮上")


def _build_user_networks(config_data, layer_dict, transform=None, d=None):
    """Build VoltageSource and CurrentSource networks from frontend config."""
    sources = config_data.get("sources", [])
    loads = config_data.get("loads", [])
    gnd_net = config_data.get("gnd_net", "")
    pads_data = config_data.get("pads", [])
    networks = []

    def _limit_gnd_pads(gnd_pads, reference_pads, location_name, d=None):
        """Limit GND pads to nearest 20 for numerical stability."""
        _MAX_GND = 20
        if len(gnd_pads) > _MAX_GND and reference_pads:
            cx = sum(p.get("x", 0) for p in reference_pads) / len(reference_pads)
            cy = sum(p.get("y", 0) for p in reference_pads) / len(reference_pads)
            gnd_pads.sort(key=lambda p: (p.get("x", 0) - cx)**2 + (p.get("y", 0) - cy)**2)
            total = len(gnd_pads)
            gnd_pads = gnd_pads[:_MAX_GND]
            if d:
                d.info(f"Limited GND pads to {_MAX_GND}/{total} near {location_name} for numerical stability")
        return gnd_pads

    def _transform_pt(x, y):
        if transform:
            sx, sy, ox, oy = transform
            return x * sx + ox, y * sy + oy
        return x, y

    def _connect_tht_all_layers(x, y):
        """THT pad: connect to layers where pad actually lands on copper.

        Only connects when the pad point actually intersects the copper.
        No tolerance/fallback to avoid false connections to nearby networks.
        """
        conns = []
        seen = set()
        tx, ty = _transform_pt(x, y)
        pt = shapely.geometry.Point(tx, ty)
        for lname, layer in layer_dict.items():
            # Only connect if point is within copper (exact intersection)
            if layer.shape.intersects(pt):
                key = (id(layer), round(pt.x, 6), round(pt.y, 6))
                if key not in seen:
                    seen.add(key)
                    conns.append(problem.Connection(layer=layer, point=pt))
        return conns

    def _connect_single_layer(layer_name, x, y):
        """SMD pad: connect to one layer."""
        layer = layer_dict.get(layer_name)
        if layer is None:
            if d: d.warn(f"Layer '{layer_name}' not in Gerber layers {list(layer_dict.keys())}")
            return []
        tx, ty = _transform_pt(x, y)
        pt = shapely.geometry.Point(tx, ty)
        nearest, _ = _find_nearest_point_on_layer(pt, layer)  # Handle tuple return
        return [problem.Connection(layer=layer, point=nearest)]

    def _connect_pad(pad_info):
        """Connect a pad (THT or SMD) to appropriate layers."""
        layer_name = pad_info.get("layer") or ""
        is_tht = pad_info.get("is_tht", False)
        x, y = pad_info["x"], pad_info["y"]
        if is_tht:
            return _connect_tht_all_layers(x, y)
        if not layer_name:
            return []
        return _connect_single_layer(layer_name, x, y)

    def _conn_key(c):
        return (id(c.layer), round(c.point.x, 6), round(c.point.y, 6))

    # ── Build combined VS+CS network per net ──
    # Both VS and CS reference the same GND net pads.  Building separate
    # glue-VS (V=0) for each creates duplicate matrix rows → singular system.
    # Fix: merge each VS+CS pair that shares the same GND net into ONE network
    # with a single set of GND glue-VS.
    for src in sources:
        net = src.get("net", "")
        voltage = src.get("voltage", 3.3)
        src_gnd_net = src.get("gnd_net", gnd_net)
        src_pads = src.get("pads", [])
        src_gnd_pads = src.get("gnd_pads", [])

        # Source VCC pads → positive terminal
        p_connections = []
        for sp in src_pads:
            conns = _connect_pad(sp)
            p_connections.extend(conns)

        # Source GND pads → negative terminal (primary)
        # Fallback to global GND pad list if source has no gnd_pads
        if src_gnd_pads:
            gnd_pads = src_gnd_pads
        else:
            gnd_pads = [p for p in pads_data if p.get("net") == src_gnd_net]
            gnd_pads = _limit_gnd_pads(gnd_pads, src_pads, f"source '{net}'", d)
        n_connections = []
        for gp in gnd_pads:
            conns = _connect_pad(gp)
            n_connections.extend(conns)

        if not p_connections or not n_connections:
            if d: d.warn(f"Source '{net}': p={len(p_connections)}, n={len(n_connections)}, skipped")
            continue

        p0 = p_connections[0]
        n0 = n_connections[0]

        if _conn_key(p0) == _conn_key(n0):
            if d: d.warn(f"Source '{net}': main VS p==n, skipped")
            continue

        all_conns = [p0, n0]
        elements = [problem.VoltageSource(p=p0.node_id, n=n0.node_id, voltage=voltage)]
        seen_keys = {_conn_key(p0), _conn_key(n0)}

        # Glue VS for extra source pads
        for pc in p_connections[1:]:
            k = _conn_key(pc)
            if k in seen_keys:
                continue
            seen_keys.add(k)
            elements.append(problem.VoltageSource(p=pc.node_id, n=p0.node_id, voltage=0.0))
            all_conns.append(pc)

        # Glue VS for GND pads (only once!)
        n0_key = _conn_key(n0)
        for nc in n_connections[1:]:
            k = _conn_key(nc)
            if k in seen_keys:
                continue
            seen_keys.add(k)
            elements.append(problem.VoltageSource(p=nc.node_id, n=n0.node_id, voltage=0.0))
            all_conns.append(nc)

        # Find matching CS (same net / same GND net)
        matched_load = None
        for load in loads:
            if load.get("net") == net and load.get("gnd_net", gnd_net) == src_gnd_net:
                matched_load = load
                break

        if matched_load:
            current = matched_load.get("current", 0.1)
            load_pads = matched_load.get("pads", [])
            load_gnd_pads = matched_load.get("gnd_pads", [])

            f_connections = []
            for lp in load_pads:
                conns = _connect_pad(lp)
                f_connections.extend(conns)

            # Load GND pads → CS "to" terminal (preferred over n0)
            t_connections = []
            for lp in load_gnd_pads:
                conns = _connect_pad(lp)
                t_connections.extend(conns)

            if f_connections:
                # CS: current flows from load VCC pad to load GND pad (or n0)
                cs_t = t_connections[0] if t_connections else n0
                elements.append(problem.CurrentSource(
                    f=f_connections[0].node_id,
                    t=cs_t.node_id,
                    current=current,
                ))
                f0_key = _conn_key(f_connections[0])
                if f0_key not in seen_keys:
                    seen_keys.add(f0_key)
                    all_conns.append(f_connections[0])
                cs_t_key = _conn_key(cs_t)
                if cs_t_key not in seen_keys:
                    seen_keys.add(cs_t_key)
                    all_conns.append(cs_t)

                # Glue VS for extra load VCC pads
                for fc in f_connections[1:]:
                    k = _conn_key(fc)
                    if k in seen_keys:
                        continue
                    seen_keys.add(k)
                    elements.append(problem.VoltageSource(
                        p=fc.node_id, n=f_connections[0].node_id, voltage=0.0))
                    all_conns.append(fc)

                # Glue VS for extra load GND pads → tie to cs_t
                for tc in t_connections[1:]:
                    k = _conn_key(tc)
                    if k in seen_keys:
                        continue
                    seen_keys.add(k)
                    elements.append(problem.VoltageSource(
                        p=tc.node_id, n=cs_t.node_id, voltage=0.0))
                    all_conns.append(tc)

                # Glue VS: tie load GND to source GND (cs_t → n0)
                if t_connections and _conn_key(cs_t) != _conn_key(n0):
                    elements.append(problem.VoltageSource(
                        p=cs_t.node_id, n=n0.node_id, voltage=0.0))

                if d: d.info(f"VS+CS '{net}': V={voltage}, I={current}A, "
                             f"p={len(p_connections)}, n={len(n_connections)}, "
                             f"f={len(f_connections)}, t={len(t_connections)}")
            else:
                if d: d.info(f"VS '{net}': V={voltage}, p={len(p_connections)}, n={len(n_connections)}")
        else:
            if d: d.info(f"VS '{net}': V={voltage}, p={len(p_connections)}, n={len(n_connections)}")

        networks.append(problem.Network(connections=all_conns, elements=elements))

    # ── Standalone Current Loads (no matching VS) ──
    matched_nets = {src.get("net") for src in sources}
    for load in loads:
        net = load.get("net", "")
        if net in matched_nets:
            continue  # already handled above
        current = load.get("current", 0.1)
        load_gnd_net = load.get("gnd_net", gnd_net)
        load_pads = load.get("pads", [])
        load_gnd_pads = load.get("gnd_pads", [])

        f_connections = []
        for lp in load_pads:
            conns = _connect_pad(lp)
            f_connections.extend(conns)

        # Load GND pads → CS "to" terminal (preferred)
        if load_gnd_pads:
            gnd_pads = load_gnd_pads
        else:
            gnd_pads = [p for p in pads_data if p.get("net") == load_gnd_net]
            gnd_pads = _limit_gnd_pads(gnd_pads, load_pads, f"load '{net}'", d)
        t_connections = []
        for gp in gnd_pads:
            conns = _connect_pad(gp)
            t_connections.extend(conns)

        if not f_connections or not t_connections:
            if d: d.warn(f"Load '{net}': f={len(f_connections)}, t={len(t_connections)}, skipped")
            continue

        elements = [problem.CurrentSource(
            f=f_connections[0].node_id,
            t=t_connections[0].node_id,
            current=current,
        )]
        seen_keys = {_conn_key(f_connections[0]), _conn_key(t_connections[0])}
        all_conns = [f_connections[0], t_connections[0]]
        for fc in f_connections[1:]:
            k = _conn_key(fc)
            if k in seen_keys:
                continue
            seen_keys.add(k)
            elements.append(problem.VoltageSource(p=fc.node_id, n=f_connections[0].node_id, voltage=0.0))
            all_conns.append(fc)
        for tc in t_connections[1:]:
            k = _conn_key(tc)
            if k in seen_keys:
                continue
            seen_keys.add(k)
            elements.append(problem.VoltageSource(p=tc.node_id, n=t_connections[0].node_id, voltage=0.0))
            all_conns.append(tc)

        networks.append(problem.Network(connections=all_conns, elements=elements))
        if d: d.info(f"CS '{net}': I={current}A, f={len(f_connections)}, t={len(t_connections)}")

    return networks


# ============================================================
# Coordinate Transform
# ============================================================

def _compute_coordinate_transform(config_data, layers, d=None):
    """Compute EasyEDA mm → Gerber coordinate transform.

    EasyEDA pads/vias are in mm (converted from mil), Gerber geometry is in mm.
    They should be the same scale but may have different origins.
    We align centers to compute offset only (scale=1).
    """
    easyeda_bounds = config_data.get("easyeda_bounds")
    if not easyeda_bounds or not layers:
        return None

    all_geoms = []
    for layer in layers:
        all_geoms.extend(layer.geoms)
    if not all_geoms:
        return None

    union = shapely.union_all(all_geoms)
    gb = union.bounds  # (minx, miny, maxx, maxy)

    # Use center-to-center offset (no scale)
    easyeda_cx = (easyeda_bounds["minX"] + easyeda_bounds["maxX"]) / 2
    easyeda_cy = (easyeda_bounds["minY"] + easyeda_bounds["maxY"]) / 2
    gerber_cx = (gb[0] + gb[2]) / 2
    gerber_cy = (gb[1] + gb[3]) / 2

    ox = gerber_cx - easyeda_cx
    oy = gerber_cy - easyeda_cy

    if d:
        d.info(f"EasyEDA bounds: X=[{easyeda_bounds['minX']:.2f},{easyeda_bounds['maxX']:.2f}] "
               f"Y=[{easyeda_bounds['minY']:.2f},{easyeda_bounds['maxY']:.2f}]")
        d.info(f"Gerber bounds: X=[{gb[0]:.2f},{gb[2]:.2f}] Y=[{gb[1]:.2f},{gb[3]:.2f}]")
        d.info(f"Centers: easyeda=({easyeda_cx:.2f},{easyeda_cy:.2f}), gerber=({gerber_cx:.2f},{gerber_cy:.2f})")

    return (1.0, 1.0, ox, oy)


# ============================================================
# Serialization
# ============================================================

def _to_easyeda(x, y, transform, flip_y=False):
    """Gerber → EasyEDA: inverse of the coordinate transform."""
    sx, sy, ox, oy = transform
    ey = (y - oy) / sy
    if flip_y:
        ey = -ey
    return (x - ox) / sx, ey


def serialize_compact_mesh(cm: solver.CompactMesh, transform=None):
    """Serialize a CompactMesh to vertices + triangles for JSON output."""
    xy = cm.vertex_xy
    vertices = []
    for i in range(len(xy)):
        x, y = float(xy[i, 0]), float(xy[i, 1])
        if transform:
            x, y = _to_easyeda(x, y, transform, flip_y=False)
        vertices.append([x, y])
    triangles = []
    for tri in cm.triangles:
        triangles.append({"vertices": [int(tri[0]), int(tri[1]), int(tri[2])]})
    return vertices, triangles


def serialize_solution(solution, diagnostics=None, transform=None, current_warnings=None):
    layer_solutions = []
    for i, layer_sol in enumerate(solution.layer_solutions):
        layer_name = solution.problem.layers[i].name if i < len(solution.problem.layers) else f"Layer_{i}"
        mesh_outputs = []
        has_cd = bool(layer_sol.current_densities)
        for mi, (cm, pot, pd) in enumerate(zip(
            layer_sol.compact_meshes,
            layer_sol.potentials,
            layer_sol.power_densities if layer_sol.power_densities else [None] * len(layer_sol.compact_meshes),
        )):
            vertices, triangles = serialize_compact_mesh(cm, transform)
            potentials = pot.tolist() if pot is not None else []
            power_densities = pd.tolist() if pd is not None else []
            current_densities = layer_sol.current_densities[mi] if has_cd and mi < len(layer_sol.current_densities) else []
            mesh_outputs.append(MeshOutput(
                vertices=vertices,
                triangles=[MeshTriangleOutput(**t) for t in triangles],
                potentials=potentials,
                power_densities=power_densities,
                current_densities=current_densities,
            ))

        disconnected_outputs = []
        for dcm in layer_sol.disconnected_compact:
            vertices, triangles = serialize_compact_mesh(dcm, transform)
            disconnected_outputs.append(DisconnectedMeshOutput(
                vertices=vertices,
                triangles=[MeshTriangleOutput(**t) for t in triangles],
            ))

        layer_solutions.append(LayerSolutionOutput(
            layer_name=layer_name,
            meshes=mesh_outputs,
            disconnected_meshes=disconnected_outputs,
        ))

    solver_info = SolverInfoOutput(
        ground_node_current=solution.solver_info.ground_node_current,
        residual_norm=solution.solver_info.residual_norm,
    )

    connection_points = {}
    _cp_min_x, _cp_max_x = float('inf'), float('-inf')
    _cp_min_y, _cp_max_y = float('inf'), float('-inf')
    for network in solution.problem.networks:
        for conn in network.connections:
            lname = conn.layer.name
            if lname not in connection_points:
                connection_points[lname] = []
            dp = conn.point
            raw_x, raw_y = float(dp.x), float(dp.y)
            px, py = raw_x, raw_y
            if transform:
                px, py = _to_easyeda(raw_x, raw_y, transform)
            _cp_min_x = min(_cp_min_x, px); _cp_max_x = max(_cp_max_x, px)
            _cp_min_y = min(_cp_min_y, py); _cp_max_y = max(_cp_max_y, py)
            connection_points[lname].append({
                'x': px,
                'y': py,
                'is_source': network.has_source,
            })
    log.info(f"Connection points (after transform): X=[{_cp_min_x:.2f},{_cp_max_x:.2f}] Y=[{_cp_min_y:.2f},{_cp_max_y:.2f}]")

    layer_boundaries = {}
    for layer_i, layer in enumerate(solution.problem.layers):
        layer_sol = solution.layer_solutions[layer_i]

        # Streaming processing: extract and transform one mesh at a time
        # This keeps memory usage low by releasing data after each mesh
        polygons = []
        mesh_extraction_failed = False

        try:
            # Process connected meshes - one at a time
            if hasattr(layer_sol, 'compact_meshes'):
                for mesh_idx, cm in enumerate(layer_sol.compact_meshes):
                    # Extract boundaries for this single mesh
                    mesh_polys = cm.extract_boundaries()

                    # Immediately transform and save results
                    for poly in mesh_polys:
                        exterior = poly['exterior']
                        holes = poly['holes']
                        if transform:
                            exterior = [list(_to_easyeda(x, y, transform, flip_y=False))
                                        for x, y in exterior]
                            holes = [[list(_to_easyeda(x, y, transform, flip_y=False))
                                      for x, y in hole] for hole in holes]
                        polygons.append({'exterior': exterior, 'holes': holes})

                    # Release memory immediately after processing this mesh
                    del mesh_polys

            # Process disconnected meshes - one at a time
            if hasattr(layer_sol, 'disconnected_compact'):
                for cm in layer_sol.disconnected_compact:
                    mesh_polys = cm.extract_boundaries()

                    for poly in mesh_polys:
                        exterior = poly['exterior']
                        holes = poly['holes']
                        if transform:
                            exterior = [list(_to_easyeda(x, y, transform, flip_y=False))
                                        for x, y in exterior]
                            holes = [[list(_to_easyeda(x, y, transform, flip_y=False))
                                      for x, y in hole] for hole in holes]
                        polygons.append({'exterior': exterior, 'holes': holes})

                    del mesh_polys

        except Exception as e:
            log.debug(f"Mesh boundary extraction failed: {e}")
            mesh_extraction_failed = True

        # Fallback: use original geometries if mesh extraction failed
        if mesh_extraction_failed or not polygons:
            if hasattr(solution, 'original_geometries') and solution.original_geometries:
                geoms = solution.original_geometries[layer_i]
            else:
                geoms = layer.geoms

            for geom in geoms:
                def _pt(c):
                    cx, cy = float(c[0]), float(c[1])
                    if transform:
                        cx, cy = _to_easyeda(cx, cy, transform, flip_y=False)
                    return [cx, cy]
                exterior = [_pt(c) for c in geom.exterior.coords]
                holes = [[_pt(c) for c in interior.coords]
                         for interior in geom.interiors]
                polygons.append({'exterior': exterior, 'holes': holes})

        layer_boundaries[layer.name] = polygons

    return AnalyzeResponse(
        success=True,
        message="求解完成",
        layer_solutions=layer_solutions,
        solver_info=solver_info,
        connection_points=connection_points,
        layer_boundaries=layer_boundaries,
        diagnostics=diagnostics or [],
        current_warnings=current_warnings or [],
    )


# ============================================================
# FastAPI Application
# ============================================================

app = FastAPI(title="padne PDN Analysis API", version="2.0.0")


# Increase multipart upload limit (default 1MB is too small for Gerber ZIPs)
# Starlette 1.0 has THREE layers of defaults, all 1MB:
#   1. Request.form(max_part_size=1MB)           ← entry point called by FastAPI
#   2. Request._get_form(max_part_size=1MB)      ← called by form()
#   3. MultiPartParser.__init__(max_part_size=1MB) ← actual parser
# Each layer explicitly passes its default to the next, so patching only one
# is ineffective — the caller's default overrides the callee's patched default.
def _patch_multipart_limit():
    from starlette.formparsers import MultiPartParser as _MPP
    from starlette.requests import Request as _Req
    _limit = 100 * 1024 * 1024  # 100 MB
    _MPP.max_part_size = _limit
    _orig_init = _MPP.__init__
    def _patched_init(self, headers, stream, *, max_files=1000, max_fields=1000, max_part_size=_limit):
        _orig_init(self, headers, stream, max_files=max_files, max_fields=max_fields, max_part_size=max_part_size)
    _MPP.__init__ = _patched_init
    _orig_get_form = _Req._get_form
    async def _patched_get_form(self, *, max_files=1000, max_fields=1000, max_part_size=_limit):
        return await _orig_get_form(self, max_files=max_files, max_fields=max_fields, max_part_size=max_part_size)
    _Req._get_form = _patched_get_form
    _orig_form = _Req.form
    def _patched_form(self, *, max_files=1000, max_fields=1000, max_part_size=_limit):
        return _orig_form(self, max_files=max_files, max_fields=max_fields, max_part_size=max_part_size)
    _Req.form = _patched_form

_patch_multipart_limit()

app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)


@app.get("/test")
async def health_check():
    return {"status": "ok"}


def _err_response(d, msg):
    return AnalyzeResponse(success=False, message=msg, diagnostics=d.lines)


@app.post("/analyze-gerber")
async def analyze_gerber(
    gerber: UploadFile = File(...),
    config: str = Form(...),
):
    """Analyze PDN from Gerber ZIP + declarative config JSON."""
    d = DiagCollector()

    try:
        if not _HAS_PYGERBER:
            return _err_response(d, "pygerber not installed")

        config_data = json.loads(config) if config else {}
        d.info(f"project={config_data.get('project_name')}, "
               f"layers={len(config_data.get('layers', []))}, "
               f"vias={len(config_data.get('vias', []))}, "
               f"pads={len(config_data.get('pads', []))}, "
               f"sources={len(config_data.get('sources', []))}, "
               f"loads={len(config_data.get('loads', []))}")

        # Log source/load pad details
        for i, src in enumerate(config_data.get("sources", [])):
            for j, sp in enumerate(src.get("pads", [])):
                d.info(f"  source[{i}].pad[{j}]: net='{sp.get('net','')}', layer='{sp.get('layer','')}', is_tht={sp.get('is_tht',False)}")
            for j, gp in enumerate(src.get("gnd_pads", [])):
                d.info(f"  source[{i}].gnd_pad[{j}]: net='{gp.get('net','')}', layer='{gp.get('layer','')}', is_tht={gp.get('is_tht',False)}")
        for i, ld in enumerate(config_data.get("loads", [])):
            for j, lp in enumerate(ld.get("pads", [])):
                d.info(f"  load[{i}].pad[{j}]: net='{lp.get('net','')}', layer='{lp.get('layer','')}', is_tht={lp.get('is_tht',False)}")
            for j, gp in enumerate(ld.get("gnd_pads", [])):
                d.info(f"  load[{i}].gnd_pad[{j}]: net='{gp.get('net','')}', layer='{gp.get('layer','')}', is_tht={gp.get('is_tht',False)}")

        zip_bytes = await gerber.read()
        if not zip_bytes:
            return _err_response(d, "Empty Gerber file")

        layer_configs = config_data.get("layers", [])
        if not layer_configs:
            return _err_response(d, "No layer configs")

        # 1. Parse Gerber → Layer objects
        d.info("Step 1: Parse Gerber files")
        layers = _build_layers_from_gerbers(zip_bytes, layer_configs, d)
        if not layers:
            return _err_response(d, "No valid copper layers from Gerber")
        layer_dict = {layer.name: layer for layer in layers}
        d.info(f"Valid layers: {list(layer_dict.keys())}")

        # 2. Board outline clipping
        d.info("Step 2: Board outline clipping")
        outline = _extract_board_outline_from_gerbers(zip_bytes, d)
        if outline:
            # Save layer shapes before clipping
            saved_shapes = [(id(l), l.shape, l.geoms) for l in layers]
            _clip_layers_with_outline(layers, outline, d)
            # Check if clipping destroyed any layer — if so, revert
            any_destroyed = any(l.shape.is_empty for l in layers)
            if any_destroyed:
                d.warn("Outline clipping destroyed layers, reverting to unclipped geometry")
                for lid, shape, geoms in saved_shapes:
                    for l in layers:
                        if id(l) == lid:
                            object.__setattr__(l, 'shape', shape)
                            object.__setattr__(l, 'geoms', geoms)
                            break
        else:
            d.info("No board outline found")

        # 3. Coordinate transform
        transform = _compute_coordinate_transform(config_data, layers, d)
        if transform:
            d.info(f"Transform: scale=({transform[0]:.4f},{transform[1]:.4f}), offset=({transform[2]:.2f},{transform[3]:.2f})")

        # 4. Build stackup
        stackup = _build_stackup_from_config(config_data, layers)

        # 5. Via specs (no geometric hole punching)
        # Via connections are modeled as resistor networks, so punching
        # drill holes into the copper geometry is unnecessary and harmful:
        # it fragments large copper pours into thousands of tiny polygons,
        # creates extremely complex boundaries, and causes the mesher to
        # produce degenerate elongated triangles or fall back to earcut.
        d.info("Step 3: Via specs (no geometric hole punching)")
        via_specs = _extract_via_specs_from_config(config_data.get("vias", []), layer_dict, transform)
        d.info(f"Via specs: {len(via_specs)}")

        # 6. Via resistor networks
        d.info("Step 4: Via resistor networks")
        _reset_via_fallback_stats()  # Reset statistics before processing

        # Extract user's selected network names from voltage sources
        sources = config_data.get("sources", [])
        selected_networks = {src.get("net", "") for src in sources if src.get("net", "")}
        d.info(f"User selected networks: {selected_networks}")

        # Show via network distribution
        via_net_counts = {}
        for vs in via_specs:
            net = vs.net if vs.net else "(no net)"
            via_net_counts[net] = via_net_counts.get(net, 0) + 1
        d.info(f"Via specs by network: {via_net_counts}")

        # Filter and process via networks
        via_networks = []
        skipped_vias = 0
        for vs in via_specs:
            # Only create via networks for vias belonging to user's selected networks
            # Match empty/missing network only if selected_networks contains empty string
            if vs.net and vs.net in selected_networks:
                via_networks.extend(_process_via_spec(vs, layer_dict, stackup, d))
            elif not vs.net and "" in selected_networks:
                # Process vias with no network name only if empty string is explicitly selected
                via_networks.extend(_process_via_spec(vs, layer_dict, stackup, d))
            else:
                # Skip vias from other networks
                skipped_vias += 1
                # Still track statistics for skipped vias (for diagnostics)
                _via_fallback_stats['total_vias'] += 1
                _via_fallback_stats['via_types']['through'] = \
                    _via_fallback_stats['via_types'].get('through', 0) + 1
        d.info(f"Via networks for selected networks: {len(via_networks)} (skipped {skipped_vias} vias from other networks)")

        # 6.5. Via connection diagnostics summary
        _get_via_fallback_summary(d)

        # 7. User networks (VS, CS)
        d.info("Step 5: User networks")
        user_networks = _build_user_networks(config_data, layer_dict, transform, d)
        d.info(f"User networks: {len(user_networks)}")

        all_networks = via_networks + user_networks
        if not all_networks:
            return _err_response(d, "No valid networks")

        # Filter out layers with no network connections
        connected_layer_ids = set()
        for net in all_networks:
            for conn in net.connections:
                connected_layer_ids.add(id(conn.layer))
        filtered_layers = [l for l in layers if id(l) in connected_layer_ids]
        if len(filtered_layers) < len(layers):
            removed = [l.name for l in layers if id(l) not in connected_layer_ids]
            d.info(f"Filtered layers: {len(filtered_layers)}/{len(layers)} (removed: {removed})")
        layers = filtered_layers
        if not layers:
            return _err_response(d, "No layers with network connections")

        # 8. Solve
        d.info("Step 6: Assemble + solve")
        prob = problem.Problem(
            layers=layers,
            networks=all_networks,
            project_name=config_data.get("project_name"),
        )
        d.info(f"Problem: {len(prob.layers)} layers, {len(prob.networks)} networks")
        gc.collect()

        try:
            # Capture solver logs into DiagCollector for frontend display
            class _DiagHandler(logging.Handler):
                def __init__(self, diag):
                    super().__init__()
                    self.diag = diag
                def emit(self, record):
                    msg = self.format(record)
                    if record.levelno >= logging.WARNING:
                        self.diag.warn(f"[solver] {msg}")
                    else:
                        self.diag.info(f"[solver] {msg}")

            _diag_handler = _DiagHandler(d)
            _solver_logger = logging.getLogger(solver.__name__)
            _solver_logger.addHandler(_diag_handler)
            _solver_logger.setLevel(logging.DEBUG)
            try:
                solution = solver.solve(prob)
            finally:
                _solver_logger.removeHandler(_diag_handler)
        except Exception as e:
            import traceback
            tb = traceback.format_exc()
            d.error(f"Solve failed: {e}\n{tb}")
            gc.collect()
            return _err_response(d, f"Solve failed: {e}")

        gni = solution.solver_info.ground_node_current
        rn = solution.solver_info.residual_norm
        if gni is None or rn is None or math.isnan(gni) or math.isnan(rn):
            gc.collect()
            return _err_response(d, f"Singular matrix (ground_current={gni}, residual={rn})")

        d.info(f"Solve OK: ground_current={gni:.6e}, residual={rn:.6e}")

        # 10. Short circuit detection (if ground current is abnormally high)
        _detect_short_circuits(gni, config_data, layer_dict, d)

        # 11. Current capacity checking
        current_warnings = _check_current_capacities(solution, config_data, layer_dict, d)

        # 9. Serialize
        result = serialize_solution(solution, diagnostics=d.lines, transform=transform, current_warnings=current_warnings)
        del solution
        gc.collect()
        return result

    except MemoryError as e:
        gc.collect()
        return _err_response(d, f"Out of memory: {e}")
    except Exception as e:
        gc.collect()
        d.error(f"Analysis failed: {e}")
        return _err_response(d, f"Analysis failed: {e}")


if __name__ == "__main__":
    import sys
    logging.basicConfig(level=logging.INFO)
    uvicorn.run("main:app", host="0.0.0.0", port=5000, reload=True)
