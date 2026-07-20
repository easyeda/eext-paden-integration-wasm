// Package problem defines the core data structures for PCB PDN analysis.
package problem

import (
	"fmt"

	"github.com/easyeda/eext-paden-integration/go-service/internal/geometry"
)

// Layer represents a single copper layer of the input circuit board.
type Layer struct {
	Shape       geometry.MultiPolygon
	NetLabels   []string // inferred net for each polygon in Shape; empty = unknown
	Name        string
	Conductance float64  // Siemens = conductivity [S/mm] * thickness [mm]
}

// Bounds returns the bounding box of all polygons in the layer.
func (l *Layer) Bounds() geometry.Box {
	if len(l.Shape) == 0 {
		return geometry.Box{}
	}
	b := l.Shape[0].Bounds()
	for i := 1; i < len(l.Shape); i++ {
		bi := l.Shape[i].Bounds()
		if bi.MinX < b.MinX {
			b.MinX = bi.MinX
		}
		if bi.MinY < b.MinY {
			b.MinY = bi.MinY
		}
		if bi.MaxX > b.MaxX {
			b.MaxX = bi.MaxX
		}
		if bi.MaxY > b.MaxY {
			b.MaxY = bi.MaxY
		}
	}
	return b
}

// Area returns the total area of all polygons in the layer.
func (l *Layer) Area() float64 {
	var area float64
	for _, poly := range l.Shape {
		for i, ring := range poly {
			a := ring.Area()
			if i == 0 {
				area += a
			} else {
				area -= a
			}
		}
	}
	return area
}

// NodeID is an opaque identifier for a node in the electrical network.
type NodeID struct {
	id int
}

var nodeIDCounter int

// NewNodeID creates a new unique NodeID.
func NewNodeID() *NodeID {
	nodeIDCounter++
	return &NodeID{id: nodeIDCounter}
}

// ResetNodeIDCounter resets the node ID generator. Used between analyses.
func ResetNodeIDCounter() {
	nodeIDCounter = 0
}

// Connection represents a connection between an internal node and a point on a copper layer.
type Connection struct {
	Layer  *Layer
	Point  geometry.Point
	NodeID *NodeID
}

// NewConnection creates a connection with a fresh NodeID.
func NewConnection(layer *Layer, point geometry.Point) *Connection {
	return &Connection{
		Layer:  layer,
		Point:  point,
		NodeID: NewNodeID(),
	}
}

// LumpedElement is the interface for all lumped elements in the network.
type LumpedElement interface {
	Terminals() []*NodeID
	IsSource() bool
	ExtraVariableCount() int
}

// Resistor is a resistor between two nodes.
type Resistor struct {
	A, B       *NodeID
	Resistance float64
}

func (r *Resistor) Terminals() []*NodeID    { return []*NodeID{r.A, r.B} }
func (r *Resistor) IsSource() bool          { return false }
func (r *Resistor) ExtraVariableCount() int { return 0 }

// VoltageSource is an ideal voltage source between two nodes.
type VoltageSource struct {
	P, N    *NodeID
	Voltage float64
}

func (v *VoltageSource) Terminals() []*NodeID    { return []*NodeID{v.P, v.N} }
func (v *VoltageSource) IsSource() bool          { return true }
func (v *VoltageSource) ExtraVariableCount() int { return 1 }

// CurrentSource is an ideal current source between two nodes.
type CurrentSource struct {
	F, T    *NodeID
	Current float64
}

func (c *CurrentSource) Terminals() []*NodeID    { return []*NodeID{c.F, c.T} }
func (c *CurrentSource) IsSource() bool          { return true }
func (c *CurrentSource) ExtraVariableCount() int { return 0 }

// VoltageRegulator models a regulator with sense terminals.
type VoltageRegulator struct {
	VP, VN  *NodeID
	SF, ST  *NodeID
	Voltage float64
	Gain    float64
}

func (v *VoltageRegulator) Terminals() []*NodeID    { return []*NodeID{v.VP, v.VN, v.SF, v.ST} }
func (v *VoltageRegulator) IsSource() bool          { return true }
func (v *VoltageRegulator) ExtraVariableCount() int { return 1 }

// Network represents an electrical network of connections and elements.
type Network struct {
	Connections []*Connection
	Elements    []LumpedElement
	Nodes       map[*NodeID]int // computed
	HasSource   bool            // computed
}

// NewNetwork validates and builds a network.
func NewNetwork(connections []*Connection, elements []LumpedElement) (*Network, error) {
	nodeSet := make(map[*NodeID]struct{})
	for _, elem := range elements {
		for _, terminal := range elem.Terminals() {
			if terminal == nil {
				return nil, fmt.Errorf("terminal must not be nil")
			}
			nodeSet[terminal] = struct{}{}
		}
	}
	if len(nodeSet) == 0 && len(connections) > 0 {
		return nil, fmt.Errorf("network with connections must have at least one element")
	}
	for _, conn := range connections {
		if _, ok := nodeSet[conn.NodeID]; !ok {
			return nil, fmt.Errorf("floating connection not allowed")
		}
	}
	nodes := make(map[*NodeID]int, len(nodeSet))
	i := 0
	for node := range nodeSet {
		nodes[node] = i
		i++
	}
	hasSource := false
	for _, elem := range elements {
		if elem.IsSource() {
			hasSource = true
			break
		}
	}
	return &Network{
		Connections: connections,
		Elements:    elements,
		Nodes:       nodes,
		HasSource:   hasSource,
	}, nil
}

// Problem is the complete PCB PDN analysis problem.
type Problem struct {
	Layers      []*Layer
	Networks    []*Network
	ProjectName string
}
