
import math
import numpy as np
import shapely.geometry

from dataclasses import dataclass, field
from typing import Optional, Iterable, Iterator, Protocol, List, Tuple, Dict, Set

_HAS_CDT = False
try:
    import condeltri as _cdt
    _HAS_CDT = True
except ImportError:
    pass


IndexType = int


class HasIndex(Protocol):
    i: IndexType


@dataclass(frozen=True)
class Vector:
    dx: float
    dy: float

    def dot(self, other: "Vector") -> float:
        return self.dx * other.dx + self.dy * other.dy

    def __add__(self, other: "Vector") -> "Vector":
        if not isinstance(other, Vector):
            raise TypeError("Addition is only defined for Vectors")
        return Vector(self.dx + other.dx, self.dy + other.dy)

    def __mul__(self, scalar: float) -> "Vector":
        return Vector(self.dx * scalar, self.dy * scalar)

    def __rmul__(self, scalar: float) -> "Vector":
        return self.__mul__(scalar)

    def __neg__(self) -> "Vector":
        return Vector(-self.dx, -self.dy)

    def __xor__(self, other: "Vector") -> float:
        return self.dx * other.dy - self.dy * other.dx

    def __abs__(self) -> float:
        return math.sqrt(self.dx ** 2 + self.dy ** 2)


@dataclass(frozen=True)
class Point:
    x: float
    y: float

    def distance(self, other: "Point") -> float:
        return math.sqrt((self.x - other.x) ** 2 + (self.y - other.y) ** 2)

    def __sub__(self, other: "Point") -> Vector:
        if not isinstance(other, Point):
            raise TypeError("Subtraction is only defined for Points")
        return Vector(self.x - other.x, self.y - other.y)

    def to_shapely(self) -> shapely.geometry.Point:
        return shapely.geometry.Point(self.x, self.y)


@dataclass(eq=False, repr=False)
class Vertex:
    p: Point
    out: Optional["HalfEdge"] = None
    i: IndexType = field(default=IndexType(0))

    def orbit(self) -> Iterator["HalfEdge"]:
        edge = self.out
        if edge is None:
            return
        while True:
            yield edge
            if edge.twin is None or edge.twin.next is None:
                break
            edge = edge.twin.next
            if edge == self.out:
                break


@dataclass(eq=False, repr=False)
class HalfEdge:
    origin: Vertex
    twin: Optional["HalfEdge"] = None
    next: Optional["HalfEdge"] = None
    prev: Optional["HalfEdge"] = None
    face: Optional["Face"] = None
    i: IndexType = field(default=IndexType(0))

    @property
    def is_boundary(self) -> bool:
        return self.face.is_boundary

    @staticmethod
    def connect(e1: "HalfEdge", e2: "HalfEdge") -> None:
        e1.next = e2
        e2.prev = e1

    def walk(self) -> Iterator["HalfEdge"]:
        edge = self
        while True:
            yield edge
            edge = edge.next
            if edge == self:
                break

    def cotan(self) -> float:
        if self.twin is None or self.next is None or self.next.next is None:
            return 0.
        if self.twin.next is None or self.twin.next.next is None:
            return 0.
        vertex_i = self.origin
        vertex_k = self.twin.origin
        ratio = 0.
        for other in [self.next.next, self.twin.next.next]:
            if other.next is None or other.next.face is None or other.next.face.is_boundary:
                continue
            vi = vertex_i.p - other.origin.p
            vk = vertex_k.p - other.origin.p
            cross = vi ^ vk
            # Clamp near-zero cross instead of skipping:
            # thin triangles should contribute large cotangent (high conductance),
            # not be silently zeroed out
            if abs(cross) < 1e-12:
                cross = 1e-12 if cross >= 0 else -1e-12
            ratio += abs(vi.dot(vk) / cross) / 2
        return ratio


@dataclass(eq=False)
class Face:
    edge: Optional[HalfEdge] = None
    is_boundary: bool = False
    i: IndexType = field(default=IndexType(0))

    @property
    def edges(self):
        edge = self.edge
        while True:
            yield edge
            edge = edge.next
            if edge == self.edge:
                break

    @property
    def vertices(self):
        for edge in self.edges:
            yield edge.origin

    @property
    def centroid(self) -> Point:
        x_sum = 0.0
        y_sum = 0.0
        count = 0
        for vertex in self.vertices:
            x_sum += vertex.p.x
            y_sum += vertex.p.y
            count += 1
        return Point(x_sum / count, y_sum / count)

    @property
    def area(self) -> float:
        area = 0.0
        for edge in self.edges:
            p1 = edge.origin.p
            p2 = edge.next.origin.p
            area += (p1.x * p2.y - p2.x * p1.y)
        return 0.5 * abs(area)


class IndexStore:
    def __init__(self):
        self._idx_to_obj: list = []

    @property
    def next_index(self) -> IndexType:
        return len(self._idx_to_obj)

    def add(self, obj) -> None:
        obj.i = self.next_index
        self._idx_to_obj.append(obj)

    def to_index(self, obj) -> IndexType:
        return obj.i

    def to_object(self, idx: int) -> HasIndex:
        return self._idx_to_obj[int(idx)]

    def __len__(self) -> int:
        return len(self._idx_to_obj)

    def __iter__(self) -> Iterator:
        return iter(self._idx_to_obj)

    def __contains__(self, obj) -> bool:
        return 0 <= obj.i < len(self._idx_to_obj) and self._idx_to_obj[obj.i] is obj

    def items(self) -> Iterator[Tuple[IndexType, HasIndex]]:
        for idx, obj in enumerate(self._idx_to_obj):
            yield idx, obj


class Mesh:
    def __init__(self):
        self.vertices = IndexStore()
        self.halfedges = IndexStore()
        self.faces = IndexStore()
        self.boundaries = IndexStore()
        self._edge_map: Dict[Tuple[int, int], HalfEdge] = {}

    def make_vertex(self, p: Point) -> Vertex:
        v = Vertex(p)
        self.vertices.add(v)
        return v

    def connect_vertices(self, v1: Vertex, v2: Vertex) -> HalfEdge:
        key12 = (self.vertices.to_index(v1), self.vertices.to_index(v2))
        key21 = (key12[1], key12[0])

        if key12 in self._edge_map:
            return self._edge_map[key12]

        e12 = HalfEdge(v1)
        self.halfedges.add(e12)
        e21 = HalfEdge(v2)
        self.halfedges.add(e21)
        e12.twin = e21
        e21.twin = e12

        self._edge_map[key12] = e12
        self._edge_map[key21] = e21

        if v1.out is None:
            v1.out = e12
        if v2.out is None:
            v2.out = e21

        return e12

    def repair_topology(self) -> None:
        """Ensure all half-edges have valid twin, prev, and next pointers.
        Missing prev/next links create broken orbit/cotan chains that cause
        zero diagonal entries in the Laplacian matrix."""
        # Step 1: Ensure every half-edge has a twin
        for hedge in list(self.halfedges):
            if hedge.twin is None:
                # Create an isolated twin half-edge
                twin = HalfEdge(hedge.origin)
                self.halfedges.add(twin)
                hedge.twin = twin
                twin.twin = hedge
                # Give the twin a boundary face
                bface = Face(is_boundary=True)
                self.boundaries.add(bface)
                twin.face = bface
                bface.edge = twin

        # Step 2: Ensure prev/next consistency for each face
        # For interior faces: check that the half-edge loop is closed
        for face in self.faces:
            hedges_in_face = list(face.edges)
            for i in range(len(hedges_in_face)):
                h = hedges_in_face[i]
                h_next = hedges_in_face[(i + 1) % len(hedges_in_face)]
                if h.next is None or h.next != h_next:
                    h.next = h_next
                if h_next.prev is None or h_next.prev != h:
                    h_next.prev = h

        # Step 3: For boundary faces, link prev/next
        for bface in self.boundaries:
            hedges_in_face = []
            h = bface.edge
            if h is None:
                continue
            visited = set()
            cur = h
            while cur is not None and cur not in visited:
                visited.add(cur)
                hedges_in_face.append(cur)
                cur = cur.next
            for i in range(len(hedges_in_face)):
                hi = hedges_in_face[i]
                hi_next = hedges_in_face[(i + 1) % len(hedges_in_face)]
                if hi.next is None:
                    hi.next = hi_next
                if hi_next.prev is None:
                    hi_next.prev = hi

        # Step 4: Ensure every vertex has an outgoing half-edge
        for vertex in self.vertices:
            if vertex.out is None:
                # Find any half-edge originating from this vertex
                for hedge in self.halfedges:
                    if hedge.origin == vertex:
                        vertex.out = hedge
                        break

    def euler_characteristic(self) -> int:
        return len(self.vertices) - len(self.halfedges) // 2 + len(self.faces)

    @classmethod
    def from_triangle_soup(cls, points: List[Point],
                           triangles: List[Tuple[int, int, int]]) -> "Mesh":
        mesh = cls()
        vertices = [mesh.make_vertex(p) for p in points]

        for tri in triangles:
            assert len(tri) == 3
            p1, p2, p3 = points[tri[0]], points[tri[1]], points[tri[2]]
            area = abs((p2.x - p1.x) * (p3.y - p1.y) - (p3.x - p1.x) * (p2.y - p1.y))
            if area < 1e-12:
                continue
            v1, v2, v3 = [vertices[i] for i in tri]

            vertex_edge_pairs = [(v1, v2), (v2, v3), (v3, v1)]
            face = Face()
            mesh.faces.add(face)
            current_hedges = []
            for u, v in vertex_edge_pairs:
                hedge = mesh.connect_vertices(u, v)
                u.out = hedge
                face.edge = hedge
                hedge.face = face
                current_hedges.append(hedge)

            for h1, h2 in zip(current_hedges, current_hedges[1:] + [current_hedges[0]]):
                HalfEdge.connect(h1, h2)

        # Collect boundary half-edges (those without a face assigned)
        boundary_hedges = set()
        # Use a list per vertex so we can handle non-manifold cases
        vertex_to_boundary_hedges: Dict[Vertex, list] = {}
        for hedge in mesh.halfedges:
            if hedge.face is not None:
                continue
            boundary_hedges.add(hedge)
            if hedge.origin not in vertex_to_boundary_hedges:
                vertex_to_boundary_hedges[hedge.origin] = []
            vertex_to_boundary_hedges[hedge.origin].append(hedge)

        # Walk boundary loops
        while boundary_hedges:
            hedge = boundary_hedges.pop()

            face = Face(is_boundary=True)
            mesh.boundaries.add(face)
            face.edge = hedge
            hedge.face = face

            hedge_prev = hedge
            while True:
                vertex_next = hedge_prev.twin.origin
                candidates = vertex_to_boundary_hedges.get(vertex_next, [])
                # Find the boundary half-edge whose origin is vertex_next
                # and whose twin.origin matches hedge_prev.origin
                # This picks the correct outgoing edge for manifold meshes
                # and makes a best-effort choice for non-manifold cases
                hedge_next = None
                for cand in candidates:
                    if cand in boundary_hedges and cand.twin.origin == hedge_prev.origin:
                        hedge_next = cand
                        break
                # Fallback: any boundary half-edge from this vertex
                if hedge_next is None:
                    for cand in candidates:
                        if cand in boundary_hedges:
                            hedge_next = cand
                            break

                if hedge_next is None:
                    break

                boundary_hedges.remove(hedge_next)
                HalfEdge.connect(hedge_prev, hedge_next)
                hedge_next.face = face
                hedge_prev = hedge_next

            HalfEdge.connect(hedge_prev, hedge)

        # Repair: ensure all remaining faceless half-edges get a boundary face
        # (handles any edges that weren't picked up by the walk above)
        for hedge in mesh.halfedges:
            if hedge.face is None:
                bface = Face(is_boundary=True)
                mesh.boundaries.add(bface)
                bface.edge = hedge
                hedge.face = bface

        mesh.repair_topology()
        return mesh


@dataclass
class ZeroForm:
    mesh: Mesh
    values: list = field(init=False, repr=False)

    def __post_init__(self):
        self.values = [0.0] * len(self.mesh.vertices)

    def __getitem__(self, vertex: Vertex) -> float:
        if vertex not in self.mesh.vertices:
            raise KeyError("Vertex not in mesh")
        return self.values[vertex.i]

    def __setitem__(self, vertex: Vertex, value: float) -> None:
        if vertex not in self.mesh.vertices:
            raise KeyError("Vertex not in mesh")
        self.values[vertex.i] = value

    def __add__(self, other: "ZeroForm") -> "ZeroForm":
        if self.mesh is not other.mesh:
            raise ValueError("Cannot add ZeroForms on different meshes")
        result = ZeroForm(self.mesh)
        result.values = [a + b for a, b in zip(self.values, other.values)]
        return result

    def __sub__(self, other: "ZeroForm") -> "ZeroForm":
        if self.mesh is not other.mesh:
            raise ValueError("Cannot subtract ZeroForms on different meshes")
        result = ZeroForm(self.mesh)
        result.values = [a - b for a, b in zip(self.values, other.values)]
        return result

    def __mul__(self, scalar: float) -> "ZeroForm":
        result = ZeroForm(self.mesh)
        result.values = [v * scalar for v in self.values]
        return result

    def __rmul__(self, scalar: float) -> "ZeroForm":
        return self.__mul__(scalar)

    def __truediv__(self, scalar: float) -> "ZeroForm":
        if scalar == 0:
            raise ZeroDivisionError("Cannot divide ZeroForm by zero")
        result = ZeroForm(self.mesh)
        result.values = [v / scalar for v in self.values]
        return result

    def __neg__(self) -> "ZeroForm":
        result = ZeroForm(self.mesh)
        result.values = [-v for v in self.values]
        return result

    def d(self) -> "OneForm":
        one_form = OneForm(self.mesh)
        for hedge in self.mesh.halfedges:
            target_value = self.values[hedge.twin.origin.i]
            source_value = self.values[hedge.origin.i]
            one_form.values[hedge.i] = target_value - source_value
        return one_form


@dataclass
class OneForm:
    mesh: Mesh
    values: list = field(init=False, repr=False)

    def __post_init__(self):
        self.values = [0.0] * len(self.mesh.halfedges)

    def __getitem__(self, hedge: HalfEdge) -> float:
        if hedge not in self.mesh.halfedges:
            raise KeyError("HalfEdge not in mesh")
        return self.values[hedge.i]

    def __setitem__(self, hedge: HalfEdge, value: float) -> None:
        if hedge not in self.mesh.halfedges:
            raise KeyError("HalfEdge not in mesh")
        self.values[hedge.i] = value
        self.values[hedge.twin.i] = -value

    def __add__(self, other: "OneForm") -> "OneForm":
        if self.mesh is not other.mesh:
            raise ValueError("Cannot add OneForms on different meshes")
        result = OneForm(self.mesh)
        result.values = [a + b for a, b in zip(self.values, other.values)]
        return result

    def __sub__(self, other: "OneForm") -> "OneForm":
        if self.mesh is not other.mesh:
            raise ValueError("Cannot subtract OneForms on different meshes")
        result = OneForm(self.mesh)
        result.values = [a - b for a, b in zip(self.values, other.values)]
        return result

    def __mul__(self, scalar: float) -> "OneForm":
        result = OneForm(self.mesh)
        result.values = [v * scalar for v in self.values]
        return result

    def __rmul__(self, scalar: float) -> "OneForm":
        return self.__mul__(scalar)

    def __truediv__(self, scalar: float) -> "OneForm":
        if scalar == 0:
            raise ZeroDivisionError("Cannot divide OneForm by zero")
        result = OneForm(self.mesh)
        result.values = [v / scalar for v in self.values]
        return result

    def __neg__(self) -> "OneForm":
        result = OneForm(self.mesh)
        result.values = [-v for v in self.values]
        return result


@dataclass
class TwoForm:
    mesh: Mesh
    values: list = field(init=False, repr=False)

    def __post_init__(self):
        self.values = [0.0] * len(self.mesh.faces)

    def __getitem__(self, face: Face) -> float:
        if face not in self.mesh.faces and face not in self.mesh.boundaries:
            raise KeyError("Face not in mesh")
        if face in self.mesh.boundaries:
            return 0.0
        return self.values[face.i]

    def __setitem__(self, face: Face, value: float) -> None:
        if face not in self.mesh.faces:
            raise KeyError("Face not in mesh.faces")
        self.values[face.i] = value

    def __add__(self, other: "TwoForm") -> "TwoForm":
        if self.mesh is not other.mesh:
            raise ValueError("Cannot add TwoForms on different meshes")
        result = TwoForm(self.mesh)
        result.values = [a + b for a, b in zip(self.values, other.values)]
        return result

    def __sub__(self, other: "TwoForm") -> "TwoForm":
        if self.mesh is not other.mesh:
            raise ValueError("Cannot subtract TwoForms on different meshes")
        result = TwoForm(self.mesh)
        result.values = [a - b for a, b in zip(self.values, other.values)]
        return result

    def __mul__(self, scalar: float) -> "TwoForm":
        result = TwoForm(self.mesh)
        result.values = [v * scalar for v in self.values]
        return result

    def __rmul__(self, scalar: float) -> "TwoForm":
        return self.__mul__(scalar)

    def __truediv__(self, scalar: float) -> "TwoForm":
        if scalar == 0:
            raise ZeroDivisionError("Cannot divide TwoForm by zero")
        result = TwoForm(self.mesh)
        result.values = [v / scalar for v in self.values]
        return result

    def __neg__(self) -> "TwoForm":
        result = TwoForm(self.mesh)
        result.values = [-v for v in self.values]
        return result


class MeshingException(RuntimeError):
    pass


class Mesher:
    @dataclass(frozen=True)
    class Config:
        minimum_angle: float = 20.0
        maximum_size: float = 1.2  # 优化：从 0.6 增加到 1.2mm，减少约50%网格顶点
        variable_density_min_distance: float = 0.5
        variable_density_max_distance: float = 5.0  # 优化：从 3.0 增加到 5.0mm
        variable_size_maximum_factor: float = 5.0  # 优化：从 3.0 增加到 5.0，允许更大的网格变化
        distance_map_quantization: float = 1.0

        RELAXED = None

        @property
        def is_variable_density(self) -> bool:
            return self.variable_size_maximum_factor != 1.0

    def __init__(self, config: Optional["Mesher.Config"] = None):
        self.config = config if config is not None else Mesher.Config()

    def poly_to_mesh(self, poly: shapely.geometry.Polygon,
                     seed_points: List[Point] = []) -> Mesh:
        if poly.is_empty or poly.area < 1e-10:
            return Mesh()

        if self.config.maximum_size <= 0:
            return self._earcut_triangulate(poly)

        # Fix self-intersections from Gerber→Shapely conversion without
        # morphological change.  buffer(0) only fixes topology; the previous
        # buffer(0.005).buffer(-0.005) morphological opening could sever
        # narrow bridges in complex copper pours on large PCBs.
        cleaned = poly.buffer(0)
        if not cleaned.is_empty and cleaned.area > 1e-10:
            if cleaned.geom_type == 'Polygon':
                poly = cleaned
            elif cleaned.geom_type == 'MultiPolygon':
                # Keep ALL fragments whose area is at least 1% of the original
                # — taking only the largest polygon can discard significant
                # copper regions on complex layers.
                min_area = poly.area * 0.01
                keep = [g for g in cleaned.geoms if g.area >= min_area]
                if len(keep) == 1:
                    poly = keep[0]
                elif keep:
                    poly = max(keep, key=lambda g: g.area)

        # Simplify boundary to reduce vertex count and smooth out
        # micro-jaggedness from arc approximation in Gerber data.
        # Use smaller tolerance for complex PCBs to preserve edge details
        simpl_tol = max(0.005, self.config.maximum_size * 0.02)
        simplified = poly.simplify(simpl_tol, preserve_topology=True)
        if not simplified.is_empty and simplified.area > 1e-10:
            if simplified.geom_type == 'Polygon':
                poly = simplified
            elif simplified.geom_type == 'MultiPolygon':
                min_area = poly.area * 0.01
                keep = [g for g in simplified.geoms if g.area >= min_area]
                if len(keep) == 1:
                    poly = keep[0]
                elif keep:
                    poly = max(keep, key=lambda g: g.area)

        # For complex polygons (many boundary vertices), simplify to avoid
        # exceeding MAX_VERTICES and falling back to earcut which produces
        # degenerate elongated triangles.
        # 优化：降低边界顶点限制以减少内存使用，从 12000 降到 6000
        MAX_BOUNDARY_VERTS = 6000
        poly = self._simplify_if_needed(poly, MAX_BOUNDARY_VERTS)

        if _HAS_CDT:
            try:
                return self._cdt_triangulate(poly, seed_points)
            except Exception:
                pass

        try:
            return self._adaptive_triangulate(poly, seed_points)
        except Exception:
            return self._earcut_triangulate(poly)

    @staticmethod
    def _simplify_if_needed(poly, max_verts):
        """Simplify polygon boundary if vertex count exceeds max_verts.

        Uses binary search to find the minimum tolerance that reduces vertices
        to below max_verts, preserving as much boundary detail as possible.
        """
        n = len(poly.exterior.coords)
        for interior in poly.interiors:
            n += len(interior.coords)
        if n <= max_verts:
            return poly

        import math
        bounds = poly.bounds
        diag = math.hypot(bounds[2] - bounds[0], bounds[3] - bounds[1])

        # Binary search for optimal tolerance
        # Lower bound: very small tolerance (minimal simplification)
        # Upper bound: large tolerance (aggressive simplification)
        low_tol = diag * 0.0001
        high_tol = diag * 0.01  # 1% of diagonal

        best_result = poly

        # Binary search with more iterations for precision
        for _ in range(12):
            mid_tol = (low_tol + high_tol) / 2
            simplified = poly.simplify(mid_tol, preserve_topology=True)

            if simplified.is_empty or simplified.geom_type not in ('Polygon', 'MultiPolygon'):
                low_tol = mid_tol  # Tolerance too high, try lower
                continue

            if isinstance(simplified, shapely.geometry.MultiPolygon):
                simplified = max(simplified.geoms, key=lambda g: g.area)

            n2 = len(simplified.exterior.coords)
            for interior in simplified.interiors:
                n2 += len(interior.coords)

            if n2 <= max_verts:
                # This tolerance works, try to preserve more detail by going lower
                best_result = simplified
                high_tol = mid_tol
            else:
                # Still too many vertices, need higher tolerance
                low_tol = mid_tol

        return best_result

    @staticmethod
    def _enforce_min_edge(poly, min_len):
        """Merge consecutive boundary vertices closer than min_len.

        This directly removes the close-together vertex pairs that cause
        starburst degenerate triangles.  Unlike simplify() which only
        considers shape error, this enforces a hard minimum spacing
        between adjacent boundary vertices.
        """
        def _ring(coords, closed):
            """Filter a coordinate ring to enforce minimum edge length."""
            if len(coords) < 3:
                return list(coords)
            pts = [coords[0]]
            for i in range(1, len(coords)):
                dx = coords[i][0] - pts[-1][0]
                dy = coords[i][1] - pts[-1][1]
                d = math.hypot(dx, dy)
                # Keep the last point (closing vertex) regardless
                is_closing = (i == len(coords) - 1 and closed)
                if is_closing:
                    dx0 = coords[i][0] - pts[0][0]
                    dy0 = coords[i][1] - pts[0][1]
                    if math.hypot(dx0, dy0) < min_len and len(pts) > 2:
                        # Closing vertex too close to first — drop it,
                        # we'll re-close below
                        pass
                    else:
                        pts.append(coords[i])
                elif d >= min_len:
                    pts.append(coords[i])
            if closed:
                # Ensure ring is properly closed
                dx = pts[-1][0] - pts[0][0]
                dy = pts[-1][1] - pts[0][1]
                if math.hypot(dx, dy) > 1e-12:
                    pts.append(pts[0])
            return pts

        ext = _ring(poly.exterior.coords, closed=True)
        if len(ext) < 4:
            return poly
        holes = []
        for interior in poly.interiors:
            h = _ring(interior.coords, closed=True)
            if len(h) >= 4:
                holes.append(h)
        try:
            result = shapely.geometry.Polygon(ext, holes)
            if not result.is_valid:
                result = result.buffer(0)
            if result.is_empty or result.area < 1e-10:
                return poly
            return result
        except Exception:
            return poly

    def _refine_mesh(self, poly, mesh, min_angle_deg=20, max_iter=8):
        """Split thin triangles by inserting midpoints on longest edges.

        Converts the half-edge mesh → point/triangle arrays, iteratively
        finds triangles whose minimum angle < min_angle_deg, inserts the
        midpoint of the longest edge as a new Steiner point, re-runs
        Delaunay, and filters by polygon containment.  The mesh stays
        complete (no holes) because every removed thin triangle is
        replaced by smaller, better-shaped ones.
        """
        from scipy.spatial import Delaunay as ScipyDelaunay
        from shapely.prepared import prep

        # Safety: skip refinement for very large meshes to avoid timeout
        if len(mesh.vertices) > 80000:
            return mesh

        prepared = prep(poly)
        cos_thresh = math.cos(math.radians(min_angle_deg))

        # Extract points and triangles from half-edge mesh
        pts = [(v.p.x, v.p.y) for v in mesh.vertices]
        tris = []
        for face in mesh.faces:
            verts = list(face.vertices)
            if len(verts) == 3:
                tris.append((verts[0].i, verts[1].i, verts[2].i))
        if not tris:
            return mesh

        for _iter in range(max_iter):
            # Find thin triangles and collect Steiner points
            new_pts = []
            for tri in tris:
                p0, p1, p2 = pts[tri[0]], pts[tri[1]], pts[tri[2]]
                is_thin = False
                # Check each angle
                corners = [(p0, p1, p2), (p1, p2, p0), (p2, p0, p1)]
                for a, b, c in corners:
                    ab = math.hypot(b[0]-a[0], b[1]-a[1])
                    ac = math.hypot(c[0]-a[0], c[1]-a[1])
                    if ab < 1e-12 or ac < 1e-12:
                        continue
                    cos_a = ((b[0]-a[0])*(c[0]-a[0])+(b[1]-a[1])*(c[1]-a[1]))/(ab*ac)
                    if cos_a > cos_thresh:
                        # Insert midpoint of longest edge
                        edges = [
                            (math.hypot(p1[0]-p0[0], p1[1]-p0[1]), p0, p1),
                            (math.hypot(p2[0]-p1[0], p2[1]-p1[1]), p1, p2),
                            (math.hypot(p0[0]-p2[0], p0[1]-p2[1]), p2, p0),
                        ]
                        _, pa, pb = max(edges)
                        mid = ((pa[0]+pb[0])/2, (pa[1]+pb[1])/2)
                        new_pts.append(mid)
                        is_thin = True
                        break

            if not new_pts:
                break  # All triangles meet quality criterion

            # Add new Steiner points (deduplicated)
            seen = {(round(p[0], 3), round(p[1], 3)) for p in pts}
            added = 0
            for p in new_pts:
                key = (round(p[0], 3), round(p[1], 3))
                if key not in seen:
                    seen.add(key)
                    # Only add points inside the polygon
                    if prepared.contains(shapely.geometry.Point(p[0], p[1])):
                        pts.append(p)
                        added += 1
            if added == 0:
                break

            # Re-triangulate
            arr = np.array(pts)
            delaunay = ScipyDelaunay(arr)
            tris = []
            for s in delaunay.simplices:
                cx = arr[s, 0].mean()
                cy = arr[s, 1].mean()
                if prepared.contains(shapely.geometry.Point(float(cx), float(cy))):
                    a, b, c = int(s[0]), int(s[1]), int(s[2])
                    if a != b and b != c and c != a:
                        tris.append((a, b, c))

        # Rebuild half-edge mesh from refined triangles
        mesh_points = [Point(p[0], p[1]) for p in pts]
        return Mesh.from_triangle_soup(mesh_points, tris)

    def _cdt_triangulate(self, poly, seed_points):
        from scipy.spatial import KDTree
        from shapely.prepared import prep

        area = poly.area
        min_size = self.config.maximum_size
        max_size = min_size  # uniform mesh — no variable density
        min_dist = self.config.variable_density_min_distance
        max_dist = self.config.variable_density_max_distance
        MAX_VERTICES = max(50000, int(area / (min_size * min_size) * 1.5))
        MAX_VERTICES = min(MAX_VERTICES, 1000000)

        has_seeds = len(seed_points) > 0
        seed_tree = KDTree([(p.x, p.y) for p in seed_points]) if has_seeds else None

        def target_one(x, y):
            if not has_seeds:
                return max_size
            d = seed_tree.query([[x, y]])[0][0]
            t = min(1.0, max(0.0, (d - min_dist) / (max_dist - min_dist)))
            return min_size + t * (max_size - min_size)

        def subdivide(ax, ay, bx, by):
            length = math.hypot(bx - ax, by - ay)
            if length < 1e-12:
                return [(ax, ay)]
            mx, my = (ax + bx) / 2, (ay + by) / 2
            ts = target_one(mx, my)
            if length <= ts * 1.5:
                return [(ax, ay)]
            return subdivide(ax, ay, mx, my) + subdivide(mx, my, bx, by)

        # --- Collect densified boundary points per ring ---
        rings_pts = []  # list of list of (x, y) per ring
        for ring in [poly.exterior] + list(poly.interiors):
            coords = list(ring.coords)[:-1]
            n = len(coords)
            ring_pts = []
            for i in range(n):
                x1, y1 = coords[i]
                x2, y2 = coords[(i + 1) % n]
                ring_pts.extend(subdivide(x1, y1, x2, y2))
            rings_pts.append(ring_pts)

        # --- Deduplicate all points, build segment list ---
        seen = {}
        pts = []

        def add(x, y):
            key = (round(x, 3), round(y, 3))
            if key not in seen:
                seen[key] = len(pts)
                pts.append((float(x), float(y)))
            return seen[key]

        segments = []
        for ring_pts in rings_pts:
            indices = [add(x, y) for x, y in ring_pts]
            n = len(indices)
            for i in range(n):
                a, b = indices[i], indices[(i + 1) % n]
                if a != b:
                    segments.append((a, b))

        # --- Interior adaptive grid ---
        bounds = poly.bounds
        spacing = min_size
        if area / (spacing * spacing) > MAX_VERTICES * 0.6:
            spacing = math.sqrt(area / (MAX_VERTICES * 0.6))

        xs = np.arange(bounds[0], bounds[2] + spacing, spacing)
        ys = np.arange(bounds[1], bounds[3] + spacing, spacing)
        xx, yy = np.meshgrid(xs, ys, indexing='ij')
        xf, yf = xx.ravel(), yy.ravel()

        if has_seeds:
            dists = seed_tree.query(np.column_stack([xf, yf]))[0]
            t_arr = np.clip((dists - min_dist) / (max_dist - min_dist), 0.0, 1.0)
            targets = min_size + t_arr * (max_size - min_size)
            levels = np.maximum(1, np.round(targets / spacing)).astype(int)
            n_x, n_y = len(xs), len(ys)
            iix, iiy = np.meshgrid(np.arange(n_x), np.arange(n_y), indexing='ij')
            keep = (iix.ravel() % levels == 0) & (iiy.ravel() % levels == 0)
            cx, cy = xf[keep], yf[keep]
        else:
            cx, cy = xf, yf

        prepared = prep(poly)
        interior = [
            (float(x), float(y))
            for x, y in zip(cx, cy)
            if prepared.contains(shapely.geometry.Point(float(x), float(y)))
        ]

        for x, y in interior:
            add(x, y)
        for p in seed_points:
            add(p.x, p.y)

        if len(pts) > MAX_VERTICES or len(pts) < 3:
            raise MeshingException("Too many or too few vertices for CDT")

        # --- Build CDT ---
        cdt_verts = [_cdt.V2d(x, y) for x, y in pts]
        cdt_edges = [_cdt.Edge(a, b) for a, b in segments]

        t = _cdt.Triangulation(
            _cdt.VertexInsertionOrder.AS_PROVIDED,
            _cdt.IntersectingConstraintEdges.NOT_ALLOWED,
            1e-6
        )
        t.insert_vertices(cdt_verts)
        if cdt_edges:
            t.insert_edges(cdt_edges)
        t.erase_super_triangle()

        # --- Filter triangles by centroid containment ---
        verts_arr = np.array(pts)
        valid = []
        used_set = set()
        for tri in t.triangles:
            vi = tri.vertices
            cx = (verts_arr[vi[0], 0] + verts_arr[vi[1], 0] + verts_arr[vi[2], 0]) / 3
            cy = (verts_arr[vi[0], 1] + verts_arr[vi[1], 1] + verts_arr[vi[2], 1]) / 3
            if prepared.contains(shapely.geometry.Point(float(cx), float(cy))):
                a, b, c = int(vi[0]), int(vi[1]), int(vi[2])
                if a != b and b != c and c != a:
                    valid.append((a, b, c))
                    used_set.update([a, b, c])

        if not valid:
            raise MeshingException("CDT produced no valid triangles")

        # Remap to remove unused vertices
        used_list = sorted(used_set)
        old_to_new = {old: new for new, old in enumerate(used_list)}
        final_pts = [pts[i] for i in used_list]
        final_tri = [(old_to_new[a], old_to_new[b], old_to_new[c]) for a, b, c in valid]

        # Verify seed points survived
        if has_seeds:
            seed_keys = {(round(p.x, 3), round(p.y, 3)) for p in seed_points}
            final_keys = {(round(x, 3), round(y, 3)) for x, y in final_pts}
            if not seed_keys.issubset(final_keys):
                raise MeshingException("Seed points lost during CDT")

        mesh_points = [Point(x, y) for x, y in final_pts]
        return Mesh.from_triangle_soup(mesh_points, final_tri)

    def _adaptive_triangulate(self, poly, seed_points):
        from scipy.spatial import Delaunay, KDTree

        area = poly.area
        min_size = self.config.maximum_size
        max_size = min_size  # uniform mesh
        min_dist = self.config.variable_density_min_distance
        max_dist = self.config.variable_density_max_distance
        MAX_VERTICES = max(50000, int(area / (min_size * min_size) * 1.5))
        MAX_VERTICES = min(MAX_VERTICES, 1000000)

        has_seeds = len(seed_points) > 0
        seed_tree = KDTree([(p.x, p.y) for p in seed_points]) if has_seeds else None

        def target_vec(xx, yy):
            if not has_seeds:
                return np.full(len(xx), max_size, dtype=float)
            dists = seed_tree.query(np.column_stack([xx, yy]))[0]
            t = np.clip((dists - min_dist) / (max_dist - min_dist), 0.0, 1.0)
            return min_size + t * (max_size - min_size)

        def target_one(x, y):
            if not has_seeds:
                return max_size
            d = seed_tree.query([[x, y]])[0][0]
            t = min(1.0, max(0.0, (d - min_dist) / (max_dist - min_dist)))
            return min_size + t * (max_size - min_size)

        # Densify boundary by recursive edge subdivision
        def subdivide(ax, ay, bx, by):
            length = math.hypot(bx - ax, by - ay)
            if length < 1e-12:
                return [(ax, ay)]
            mx, my = (ax + bx) / 2, (ay + by) / 2
            ts = target_one(mx, my)
            if length <= ts * 1.5:
                return [(ax, ay)]
            return subdivide(ax, ay, mx, my) + subdivide(mx, my, bx, by)

        boundary = []
        for ring in [poly.exterior] + list(poly.interiors):
            coords = list(ring.coords)[:-1]
            n = len(coords)
            for i in range(n):
                x1, y1 = coords[i]
                x2, y2 = coords[(i + 1) % n]
                boundary.extend(subdivide(x1, y1, x2, y2))

        # Interior adaptive grid
        bounds = poly.bounds
        spacing = min_size
        if area / (spacing * spacing) > MAX_VERTICES * 0.6:
            spacing = math.sqrt(area / (MAX_VERTICES * 0.6))

        xs = np.arange(bounds[0], bounds[2] + spacing, spacing)
        ys = np.arange(bounds[1], bounds[3] + spacing, spacing)
        n_x, n_y = len(xs), len(ys)

        xx, yy = np.meshgrid(xs, ys, indexing='ij')
        xf, yf = xx.ravel(), yy.ravel()

        targets = target_vec(xf, yf)
        levels = np.maximum(1, np.round(targets / spacing)).astype(int)

        iix, iiy = np.meshgrid(np.arange(n_x), np.arange(n_y), indexing='ij')
        keep = (iix.ravel() % levels == 0) & (iiy.ravel() % levels == 0)

        cx, cy = xf[keep], yf[keep]

        from shapely.prepared import prep
        prepared = prep(poly)

        interior = [
            (float(x), float(y))
            for x, y in zip(cx, cy)
            if prepared.contains(shapely.geometry.Point(float(x), float(y)))
        ]

        # Merge boundary + interior + seeds, deduplicate
        seen = {}
        pts = []

        def add(x, y):
            key = (round(x, 3), round(y, 3))
            if key not in seen:
                seen[key] = len(pts)
                pts.append((float(x), float(y)))

        for x, y in boundary:
            add(x, y)
        for x, y in interior:
            add(x, y)
        for p in seed_points:
            add(p.x, p.y)

        if len(pts) > MAX_VERTICES or len(pts) < 3:
            return self._earcut_triangulate(poly)

        arr = np.array(pts)
        tri = Delaunay(arr)

        valid = []
        used_set = set()
        for s in tri.simplices:
            centroid = shapely.geometry.Point(
                float(arr[s, 0].mean()), float(arr[s, 1].mean())
            )
            if prepared.contains(centroid):
                a, b, c = int(s[0]), int(s[1]), int(s[2])
                if a != b and b != c and c != a:
                    area = abs(
                        (pts[b][0] - pts[a][0]) * (pts[c][1] - pts[a][1])
                        - (pts[c][0] - pts[a][0]) * (pts[b][1] - pts[a][1])
                    )
                    if area > 1e-10:
                        valid.append((a, b, c))
                        used_set.update([a, b, c])

        if not valid:
            return self._earcut_triangulate(poly)

        # Remap to remove isolated vertices
        used_list = sorted(used_set)
        old_to_new = {old: new for new, old in enumerate(used_list)}
        final_pts = [pts[i] for i in used_list]
        final_tri = [(old_to_new[a], old_to_new[b], old_to_new[c]) for a, b, c in valid]

        # Verify seed points survived the clipping
        if has_seeds:
            seed_keys = {(round(p.x, 3), round(p.y, 3)) for p in seed_points}
            final_keys = {(round(x, 3), round(y, 3)) for x, y in final_pts}
            if not seed_keys.issubset(final_keys):
                return self._earcut_triangulate(poly)

        mesh_points = [Point(x, y) for x, y in final_pts]
        return Mesh.from_triangle_soup(mesh_points, final_tri)

    def _earcut_triangulate(self, poly):
        import trimesh

        try:
            vertices, faces = trimesh.creation.triangulate_polygon(poly, engine='earcut')
        except Exception:
            try:
                vertices, faces = trimesh.creation.triangulate_polygon(
                    poly.buffer(0), engine='earcut'
                )
            except Exception:
                return Mesh()

        if len(faces) == 0:
            return Mesh()

        unique_map = {}
        remap = {}
        unique_verts = []
        for i in range(len(vertices)):
            key = (round(float(vertices[i][0]), 4), round(float(vertices[i][1]), 4))
            if key in unique_map:
                remap[i] = unique_map[key]
            else:
                new_idx = len(unique_verts)
                unique_map[key] = new_idx
                unique_verts.append(vertices[i])
                remap[i] = new_idx

        triangles = []
        for face in faces:
            tri = tuple(remap[int(face[i])] for i in range(3))
            if tri[0] != tri[1] and tri[1] != tri[2] and tri[2] != tri[0]:
                triangles.append(tri)

        if not triangles:
            return Mesh()

        mesh_points = [Point(float(v[0]), float(v[1])) for v in unique_verts]
        mesh = Mesh.from_triangle_soup(mesh_points, triangles)

        # Laplacian smoothing: move each interior vertex to the average
        # of its neighbors.  This rounds out degenerate spikes without
        # removing triangles (keeps the mesh complete).
        for _ in range(3):
            new_pos = {}
            for vertex in mesh.vertices:
                # Only smooth interior vertices (those not on the boundary)
                if vertex.out is None:
                    continue
                is_boundary = False
                neighbors = []
                for edge in vertex.orbit():
                    if edge.is_boundary:
                        is_boundary = True
                        break
                    neighbors.append(edge.twin.origin)
                if is_boundary or not neighbors:
                    continue
                nx = sum(n.p.x for n in neighbors) / len(neighbors)
                ny = sum(n.p.y for n in neighbors) / len(neighbors)
                new_pos[vertex.i] = (nx, ny)
            for vi, (nx, ny) in new_pos.items():
                mesh.vertices.to_object(vi).p = Point(nx, ny)

        return mesh


Mesher.Config.RELAXED = Mesher.Config(
    minimum_angle=5.0,
    maximum_size=0,
    variable_size_maximum_factor=1.0
)
