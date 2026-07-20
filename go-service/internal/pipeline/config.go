// Package pipeline implements the Gerber-to-solution analysis pipeline.
package pipeline

// LayerConfig describes a copper layer from the frontend.
type LayerConfig struct {
	Name        string  `json:"name"`
	Conductance float64 `json:"conductance"`
	LayerID     int     `json:"layer_id"`
}

// Pad describes a component pad.
type Pad struct {
	X            float64 `json:"x"`
	Y            float64 `json:"y"`
	Layer        string  `json:"layer"`
	Net          string  `json:"net"`
	IsTHT        bool    `json:"is_tht"`
	HoleDiameter float64 `json:"hole_diameter"`
}

// Via describes a via.
type Via struct {
	X            float64  `json:"x"`
	Y            float64  `json:"y"`
	HoleDiameter float64  `json:"hole_diameter"`
	LayerNames   []string `json:"layer_names"`
	Net          string   `json:"net"`
	ViaType      string   `json:"via_type"`
}

// Source describes a voltage source.
type Source struct {
	Net     string  `json:"net"`
	Voltage float64 `json:"voltage"`
	GndNet  string  `json:"gnd_net"`
	Pads    []Pad   `json:"pads"`
	GndPads []Pad   `json:"gnd_pads"`
	RefDes  string  `json:"ref_des"`
}

// Load describes a current load.
type Load struct {
	Net     string  `json:"net"`
	Current float64 `json:"current"`
	GndNet  string  `json:"gnd_net"`
	Pads    []Pad   `json:"pads"`
	GndPads []Pad   `json:"gnd_pads"`
	RefDes  string  `json:"ref_des"`
}

// Track describes a PCB track.
type Track struct {
	Net   string  `json:"net"`
	Width float64 `json:"width"`
	Layer int     `json:"layer"`
	X1    float64 `json:"x1"`
	Y1    float64 `json:"y1"`
	X2    float64 `json:"x2"`
	Y2    float64 `json:"y2"`
}

// Bounds describes the EasyEDA canvas bounds.
type Bounds struct {
	MinX float64 `json:"minX"`
	MinY float64 `json:"minY"`
	MaxX float64 `json:"maxX"`
	MaxY float64 `json:"maxY"`
}

// Config is the full frontend configuration.
type Config struct {
	ProjectName      string                   `json:"project_name"`
	Layers           []LayerConfig            `json:"layers"`
	Vias             []Via                    `json:"vias"`
	Pads             []Pad                    `json:"pads"`
	Sources          []Source                 `json:"sources"`
	Loads            []Load                   `json:"loads"`
	Tracks           []Track                  `json:"tracks"`
	Rails            []map[string]interface{} `json:"rails"`
	GndNet           string                   `json:"gnd_net"`
	TempRise         float64                  `json:"temp_rise"`
	LayerCuThickness map[string]float64       `json:"layer_cu_thickness"`
	EasyEDABounds    *Bounds                  `json:"easyeda_bounds"`
}

// EffectiveConductance returns the configured conductance or a default.
func (lc *LayerConfig) EffectiveConductance() float64 {
	if lc.Conductance == 0 {
		return 1.0
	}
	return lc.Conductance
}
