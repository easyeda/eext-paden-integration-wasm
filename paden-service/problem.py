"""
problem.py - Data structures for PCB PDN analysis problems.

This module defines the core data structures that are passed to the mesher
and FEM solver. It represents:
- Copper layers (Layer)
- Electrical networks (Network)
- Lumped elements (Resistor, VoltageSource, CurrentSource, VoltageRegulator)
- The complete problem definition (Problem)

All classes are frozen dataclasses for immutability and hashability.
"""

from __future__ import annotations

import shapely.geometry
from dataclasses import dataclass, field
from typing import Any


@dataclass(frozen=True, slots=True)
class Layer:
    """
    Represents a single copper layer of the input circuit board.

    Attributes:
        shape: MultiPolygon representing all copper regions on this layer.
        name: Human-readable layer name (e.g., "Top", "Bottom", "Inner Layer 1").
        conductance: Conductance in Siemens (conductivity * thickness).

    The individual polygons are cached in the .geoms field to avoid expensive
    Shapely copying when accessing them repeatedly.
    """
    shape: shapely.geometry.MultiPolygon
    name: str
    conductance: float  # Siemens = conductivity [S/mm] * thickness [mm]

    # Cached tuple of individual polygons, extracted from shape
    geoms: tuple[shapely.geometry.Polygon, ...] = field(init=False, repr=False)

    def __post_init__(self) -> None:
        # Extract individual polygons from MultiPolygon and cache them.
        # This avoids expensive Shapely copying on repeated .geoms access.
        object.__setattr__(self, 'geoms', tuple(self.shape.geoms))

    def __hash__(self) -> int:
        # Custom hash since Layer contains unhashable Shapely objects
        return hash((id(self.shape), self.name, self.conductance))


@dataclass(frozen=True, slots=True)
class NodeID:
    """
    Opaque identifier for a node in the electrical network.

    NodeIDs are used to reference specific connection points in the network,
    such as pad connections or via terminals. They are compared by identity
    (object id) rather than value, making them truly opaque.
    """
    def __eq__(self, other: object) -> bool:
        # Force identity comparison, not value comparison.
        # This prevents all instances from being considered equal.
        return self is other

    def __hash__(self) -> int:
        # Hash based on identity, consistent with __eq__
        return id(self)


@dataclass(frozen=True, slots=True)
class Connection:
    """
    Represents a connection between an internal node of the Network and
    a point on a copper layer.

    Attributes:
        layer: The Layer this connection is on.
        point: The shapely Point location on the layer.
        node_id: The NodeID for this connection (auto-generated if not provided).
    """
    layer: Layer
    point: shapely.geometry.Point
    node_id: NodeID = field(default_factory=NodeID)


@dataclass(frozen=True)
class BaseLumped:
    """
    Base class for all lumped elements in the network.

    Lumped elements are discrete components (resistors, sources, etc.)
    that connect two or more nodes in the network.
    """
    def __post_init__(self) -> None:
        if not self.terminals:
            raise AssertionError("Lumped elements must have terminals")

    @property
    def terminals(self) -> list[NodeID]:
        """Return the list of NodeIDs connected to this element."""
        ...

    @property
    def is_source(self) -> bool:
        """Return True if this element is a voltage/current source."""
        return False

    @property
    def extra_variable_count(self) -> int:
        """
        Return the number of extra variables needed for this element.

        Most elements need 0, but voltage sources need 1 extra variable
        for the current through the source.
        """
        return 0


@dataclass(frozen=True, slots=True)
class Network:
    """
    Represents an electrical network consisting of connections and elements.

    A Network groups together:
    - Connections: points where the network connects to copper layers
    - Elements: lumped elements (resistors, sources, etc.) between nodes

    Attributes:
        connections: List of Connection objects.
        elements: List of BaseLumped elements.
        nodes: Mapping from NodeID to integer index (computed in __post_init__).
        has_source: True if any element is a voltage/current source.

    The network is validated during construction:
    - All element terminals must be NodeID instances
    - All connections must connect to at least one element
    """
    connections: list[Connection]
    elements: list[BaseLumped]
    nodes: dict[NodeID, int] = field(init=False, repr=False)
    has_source: bool = field(init=False, repr=False)

    def __post_init__(self) -> None:
        # Collect all unique nodes from elements
        node_set: set[NodeID] = set()
        for element in self.elements:
            for terminal in element.terminals:
                if not isinstance(terminal, NodeID):
                    raise TypeError(f"Terminal must be a NodeID, got {type(terminal)}")
                node_set.add(terminal)

        # If no elements, there should be no connections either
        if not node_set and self.connections:
            raise ValueError("Network with connections must have at least one element")

        # Validate connections: each must connect to a node in this network
        for connection in self.connections:
            if connection.node_id not in node_set:
                raise ValueError(
                    f"Connection node {connection.node_id} not found in any element terminal. "
                    "Floating connections are not allowed."
                )

        # Build node-to-index mapping
        keys = list(node_set)
        nodes = {key: i for i, key in enumerate(keys)}
        object.__setattr__(self, "nodes", nodes)

        # Check if network contains any source elements
        has_source = any(element.is_source for element in self.elements)
        object.__setattr__(self, "has_source", has_source)


@dataclass(frozen=True, slots=True)
class Resistor(BaseLumped):
    """
    A resistor between two nodes.

    Attributes:
        a: First node terminal.
        b: Second node terminal.
        resistance: Resistance in ohms (must be positive).
    """
    a: NodeID
    b: NodeID
    resistance: float

    def __post_init__(self) -> None:
        super().__post_init__()
        if self.resistance <= 0:
            raise ValueError(f"Resistance must be positive, got {self.resistance}")

    @property
    def terminals(self) -> list[NodeID]:
        return [self.a, self.b]


@dataclass(frozen=True, slots=True)
class VoltageSource(BaseLumped):
    """
    An ideal voltage source between two nodes.

    Attributes:
        p: Positive terminal node.
        n: Negative terminal node.
        voltage: Voltage in volts.

    The voltage source maintains V_p - V_n = voltage.
    Requires one extra variable for the current through the source.
    """
    p: NodeID
    n: NodeID
    voltage: float

    @property
    def terminals(self) -> list[NodeID]:
        return [self.p, self.n]

    @property
    def is_source(self) -> bool:
        return True

    @property
    def extra_variable_count(self) -> int:
        return 1


@dataclass(frozen=True, slots=True)
class CurrentSource(BaseLumped):
    """
    An ideal current source between two nodes.

    Attributes:
        f: "From" node (current flows from this node).
        t: "To" node (current flows to this node).
        current: Current in amperes.

    The current source forces a constant current from f to t.
    """
    f: NodeID
    t: NodeID
    current: float

    @property
    def terminals(self) -> list[NodeID]:
        return [self.f, self.t]

    @property
    def is_source(self) -> bool:
        return True


@dataclass(frozen=True, slots=True)
class VoltageRegulator(BaseLumped):
    """
    A voltage regulator with sense terminals.

    This models a regulator that maintains:
    V(v_p) - V(v_n) = voltage + gain * (V(s_f) - V(s_t))

    Attributes:
        v_p: Voltage positive sense terminal.
        v_n: Voltage negative sense terminal.
        s_f: Sense feedback "from" terminal.
        s_t: Sense feedback "to" terminal.
        voltage: Base output voltage.
        gain: Feedback gain (sense terminal contribution).

    Requires one extra variable for the regulator current.
    """
    v_p: NodeID
    v_n: NodeID
    s_f: NodeID
    s_t: NodeID
    voltage: float
    gain: float

    @property
    def terminals(self) -> list[NodeID]:
        return [self.v_p, self.v_n, self.s_f, self.s_t]

    @property
    def is_source(self) -> bool:
        return True

    @property
    def extra_variable_count(self) -> int:
        return 1


@dataclass(frozen=True, slots=True)
class Problem:
    """
    The complete PCB PDN analysis problem.

    A Problem contains:
    - All copper layers to analyze
    - All electrical networks (voltage sources, loads, via networks)
    - Optional project name for identification

    Attributes:
        layers: List of Layer objects representing copper layers.
        networks: List of Network objects representing electrical connections.
        project_name: Optional human-readable project identifier.

    This is the top-level data structure passed to the FEM solver.
    """
    layers: list[Layer]
    networks: list[Network]
    project_name: str | None = None


# Export public symbols
__all__ = [
    'Layer',
    'NodeID',
    'Connection',
    'BaseLumped',
    'Network',
    'Resistor',
    'VoltageSource',
    'CurrentSource',
    'VoltageRegulator',
    'Problem',
]
