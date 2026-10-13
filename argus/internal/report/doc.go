// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

// Package report draws a report's PDF from a Doc that is already worked out: the server gathers the
// uptime, problems and sensor history and formats every figure, and this package only lays it out
// (page 1 with the summary, then a section per host with its problems and its sensors' charts).
package report

import (
	"image"
	"time"
)

// Tone colours a figure the way the app does.
type Tone int

const (
	Plain Tone = iota
	Good
	Warn
	Err
	Muted
)

// Doc is one report, ready to draw.
type Doc struct {
	Title  string // "Monthly availability, site1"
	Period string // "1 to 30 September 2026"
	// The lines at the top right of page 1: what it covers, when it was made, who it is for.
	Scope string
	Made  string
	For   string
	Logo  image.Image // nil: no logo

	Tiles []Tile // the figures across page 1 (uptime, under target, downtime, problems)

	Hosts     []HostRow
	HostsMore string // "6 more hosts at 100 %, no problems": the rows left out of the table
	Sensors   []SensorRow
	Footnote  string

	Sections []Section // one per host, in order
}

// Tile is one of page 1's headline figures.
type Tile struct {
	Label, Value, Sub string
	Tone              Tone
}

// HostRow is a line of page 1's hosts table.
type HostRow struct {
	Name, Group, Uptime, Downtime, Problems, Longest string
	UptimeTone                                       Tone
	Under                                            bool // under the uptime target: the row is tinted
}

// SensorRow is a line of page 1's sensors table.
type SensorRow struct {
	Name, Host, Min, Avg, Max, P95, InWarn, InErr string
	WarnTone, ErrTone                             Tone
}

// Section is a host's part of the report.
type Section struct {
	Name, Sub  string // "sw-core", "site1/Network - UniFi switch - 10.0.0.2"
	Uptime     string // "" when the report doesn't cover its uptime (only some of its sensors were picked)
	UptimeTone Tone
	Days       []Day // the strip under the header, a cell per day (or per week on a long period)
	// Problems is nil when the section doesn't cover them; NoProblems is said when it does and
	// there were none.
	Problems     []Problem
	ProblemsMore string // "12 more problems": the rows left out
	NoProblems   string
	Charts       []Chart
}

// Day is a cell of a host's strip.
type Day struct{ Tone Tone }

// Problem is a row of a host's problems table.
type Problem struct {
	Started, Lasted, Name, Sensor, Ack string
	Tone                               Tone // the severity dot
}

// ChartKind says how a sensor is drawn.
type ChartKind int

const (
	LineChart   ChartKind = iota // values over time
	UpDownChart                  // an up/down sensor: a step line, the time it was down shaded
)

// Chart is one sensor's chart, full width.
type Chart struct {
	Title string
	Stats []Stat // at the right of the title: "min 17.7 Mbps  avg ..."
	Kind  ChartKind

	From, To time.Time
	Series   []Series
	// Thresholds are drawn as dashed lines with a tag in the axis gutter; a single-series chart is
	// coloured by them too (the stretch past a threshold takes its colour).
	Thresholds []Threshold
	Below      bool // lower is worse: past a threshold means under it

	Label    func(v float64) string // an axis or threshold value, with its units
	Base1024 bool                   // the axis steps in powers of 1024 (bytes)
	// Ceil is as high as the values can go (100 for a percentage), 0 for no limit: the axis stops
	// there rather than at a round number past it.
	Ceil float64

	// Down is the time it was down: an UpDownChart's red bars, or a LineChart's shading (a group's
	// availability channel).
	Down    []Span
	Foot    string // under the chart: "in warning 1 h 50 min, in error 0"
	Loc     *time.Location
	Compact bool // a close-up of an outage: a lower plot
}

// Stat is a label and its value.
type Stat struct{ Label, Value string }

// Series is one channel of a chart. Points are in time order; a gap of more than two steps breaks
// the line.
type Series struct {
	Name   string
	Points []Point
}

// Point is a value at a time.
type Point struct {
	T time.Time
	V float64
}

// Threshold is a line across a chart.
type Threshold struct {
	V    float64
	Tone Tone // Warn or Err
}

// Span is a stretch of time.
type Span struct{ From, To time.Time }
