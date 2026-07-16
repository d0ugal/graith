// Package capabilities is the source of truth for which graith capabilities
// each frontend surface (CLI, iOS, macOS) supports.
//
// The manifest lives in capabilities.json and is embedded here. The docs page
// at website/content/docs/capabilities.md renders a table from this manifest
// inside a generated region; TestDocMatchesManifest fails if the two drift
// apart, so the doc can never silently disagree with the manifest. Regenerate
// the doc with `go test ./internal/capabilities -run TestDocMatchesManifest
// -update`.
package capabilities

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
)

// requiredSurfaces is the fixed set of frontends this package models. The
// Capability struct has one field per surface, so the manifest must declare
// exactly these — no more, no fewer — or a surface's field would go
// unvalidated (or a declared surface would have nowhere to read its state).
var requiredSurfaces = []string{"cli", "ios", "macos"}

//go:embed capabilities.json
var manifestJSON []byte

// Markers bracket the generated matrix region in the docs page. Everything
// between them is owned by the generator; prose outside them is hand-written
// and preserved.
const (
	BeginMarker = "<!-- BEGIN GENERATED CAPABILITY MATRIX -->"
	EndMarker   = "<!-- END GENERATED CAPABILITY MATRIX -->"
)

// Surface is one frontend that clients the daemon.
type Surface struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// State is a possible support level for a capability on a surface.
type State struct {
	ID          string `json:"id"`
	Symbol      string `json:"symbol"`
	Description string `json:"description"`
}

// Capability is one unit of behaviour and its support state per surface.
type Capability struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Name     string `json:"name"`
	CLI      string `json:"cli"`
	IOS      string `json:"ios"`
	MacOS    string `json:"macos"`
	Notes    string `json:"notes,omitempty"`
}

// state returns the capability's state for the given surface ID.
func (c Capability) state(surfaceID string) (string, bool) {
	switch surfaceID {
	case "cli":
		return c.CLI, true
	case "ios":
		return c.IOS, true
	case "macos":
		return c.MacOS, true
	default:
		return "", false
	}
}

// Manifest is the whole capability matrix.
type Manifest struct {
	Version      int          `json:"version"`
	Surfaces     []Surface    `json:"surfaces"`
	States       []State      `json:"states"`
	Capabilities []Capability `json:"capabilities"`
}

// Load parses and validates the embedded manifest.
func Load() (*Manifest, error) {
	return parse(manifestJSON)
}

func parse(data []byte) (*Manifest, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("decode capability manifest: %w", err)
	}
	// Reject trailing data after the first JSON value: the manifest is the
	// source of truth, so "valid object plus garbage" must fail, not silently
	// parse the leading object.
	if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
		return nil, errors.New("decode capability manifest: unexpected trailing data after JSON value")
	}

	if err := m.Validate(); err != nil {
		return nil, err
	}

	return &m, nil
}

// checkDisplay rejects a display string that would break the generated
// Markdown — a table-cell pipe or a newline. The manifest is trusted, but the
// renderer is a source-of-truth generator and a stray `|` should fail loudly
// rather than silently corrupt a row.
func checkDisplay(field, val string) error {
	if strings.ContainsAny(val, "|\n\r") {
		return fmt.Errorf("%s contains a Markdown-breaking character (| or newline): %q", field, val)
	}

	return nil
}

// Validate checks the manifest is internally consistent: known surfaces and
// states, unique capability IDs, and every capability carrying a valid state
// for every surface.
func (m *Manifest) Validate() error {
	if m.Version != 1 {
		return fmt.Errorf("unsupported manifest version %d (want 1)", m.Version)
	}

	if len(m.Surfaces) == 0 {
		return errors.New("manifest has no surfaces")
	}

	if len(m.States) == 0 {
		return errors.New("manifest has no states")
	}

	if len(m.Capabilities) == 0 {
		return errors.New("manifest has no capabilities")
	}

	validStates := map[string]bool{}
	seenSymbol := map[string]string{}

	for _, s := range m.States {
		if s.ID == "" {
			return errors.New("state has empty id")
		}

		if s.Symbol == "" {
			return fmt.Errorf("state %q has empty symbol", s.ID)
		}

		if other, dup := seenSymbol[s.Symbol]; dup {
			return fmt.Errorf("states %q and %q share symbol %q (ambiguous in the matrix)", other, s.ID, s.Symbol)
		}

		seenSymbol[s.Symbol] = s.ID
		if s.Description == "" {
			return fmt.Errorf("state %q has empty description", s.ID)
		}

		if err := checkDisplay(fmt.Sprintf("state %q symbol", s.ID), s.Symbol); err != nil {
			return err
		}

		if err := checkDisplay(fmt.Sprintf("state %q description", s.ID), s.Description); err != nil {
			return err
		}

		if validStates[s.ID] {
			return fmt.Errorf("duplicate state id %q", s.ID)
		}

		validStates[s.ID] = true
	}

	seenSurface := map[string]bool{}

	for _, s := range m.Surfaces {
		if s.ID == "" {
			return errors.New("surface has empty id")
		}

		if s.Name == "" {
			return fmt.Errorf("surface %q has empty name", s.ID)
		}

		if err := checkDisplay(fmt.Sprintf("surface %q name", s.ID), s.Name); err != nil {
			return err
		}

		if seenSurface[s.ID] {
			return fmt.Errorf("duplicate surface id %q", s.ID)
		}

		seenSurface[s.ID] = true
	}
	// The struct models exactly these surfaces; the manifest must too, so a
	// surface field cannot go silently unvalidated.
	for _, id := range requiredSurfaces {
		if !seenSurface[id] {
			return fmt.Errorf("missing required surface %q", id)
		}
	}

	for id := range seenSurface {
		if !slices.Contains(requiredSurfaces, id) {
			return fmt.Errorf("unknown surface id %q (want %v)", id, requiredSurfaces)
		}
	}

	seenCap := map[string]bool{}

	for _, c := range m.Capabilities {
		if c.ID == "" {
			return errors.New("capability has empty id")
		}

		if seenCap[c.ID] {
			return fmt.Errorf("duplicate capability id %q", c.ID)
		}

		seenCap[c.ID] = true
		if c.Category == "" {
			return fmt.Errorf("capability %q has empty category", c.ID)
		}

		if c.Name == "" {
			return fmt.Errorf("capability %q has empty name", c.ID)
		}

		if err := checkDisplay(fmt.Sprintf("capability %q category", c.ID), c.Category); err != nil {
			return err
		}

		if err := checkDisplay(fmt.Sprintf("capability %q name", c.ID), c.Name); err != nil {
			return err
		}

		if err := checkDisplay(fmt.Sprintf("capability %q notes", c.ID), c.Notes); err != nil {
			return err
		}

		for _, s := range m.Surfaces {
			st, ok := c.state(s.ID)
			if !ok {
				return fmt.Errorf("capability %q: no field for surface %q", c.ID, s.ID)
			}

			if !validStates[st] {
				return fmt.Errorf("capability %q: invalid state %q for surface %q", c.ID, st, s.ID)
			}
		}
	}

	return nil
}

// symbolFor returns the display symbol for a state ID.
func (m *Manifest) symbolFor(stateID string) string {
	for _, s := range m.States {
		if s.ID == stateID {
			return s.Symbol
		}
	}

	return stateID
}

// Categories returns the capability categories in first-seen order.
func (m *Manifest) Categories() []string {
	var order []string

	seen := map[string]bool{}
	for _, c := range m.Capabilities {
		if !seen[c.Category] {
			seen[c.Category] = true
			order = append(order, c.Category)
		}
	}

	return order
}

// MatrixMarkdown renders the capability matrix as Markdown: a legend, then one
// table per category. Deterministic output so it can be diffed against the
// committed doc.
func (m *Manifest) MatrixMarkdown() string {
	var b strings.Builder

	b.WriteString("### Legend\n\n")
	b.WriteString("| State | Meaning |\n")
	b.WriteString("|-------|---------|\n")

	for _, s := range m.States {
		fmt.Fprintf(&b, "| %s `%s` | %s |\n", s.Symbol, s.ID, s.Description)
	}

	for _, cat := range m.Categories() {
		fmt.Fprintf(&b, "\n### %s\n\n", cat)
		b.WriteString("| Capability |")

		for _, s := range m.Surfaces {
			fmt.Fprintf(&b, " %s |", s.Name)
		}

		b.WriteString("\n|------------|")

		for range m.Surfaces {
			b.WriteString(":---:|")
		}

		b.WriteString("\n")

		caps := m.capabilitiesIn(cat)
		// Number noted capabilities sequentially within the category so
		// footnote references stay unique when a category has more than one.
		noteNum := map[string]int{}

		var notes []Capability

		for _, c := range caps {
			if c.Notes != "" {
				notes = append(notes, c)
				noteNum[c.ID] = len(notes)
			}
		}

		for _, c := range caps {
			name := c.Name
			if n, ok := noteNum[c.ID]; ok {
				name = fmt.Sprintf("%s <sup>%d</sup>", name, n)
			}

			fmt.Fprintf(&b, "| %s |", name)

			for _, s := range m.Surfaces {
				st, _ := c.state(s.ID)
				fmt.Fprintf(&b, " %s |", m.symbolFor(st))
			}

			b.WriteString("\n")
		}

		if len(notes) > 0 {
			b.WriteString("\n")

			for i, c := range notes {
				fmt.Fprintf(&b, "<sup>%d</sup> %s: %s\n", i+1, c.Name, c.Notes)
			}
		}
	}

	return b.String()
}

// capabilitiesIn returns the capabilities for a category in manifest order.
func (m *Manifest) capabilitiesIn(category string) []Capability {
	var out []Capability

	for _, c := range m.Capabilities {
		if c.Category == category {
			out = append(out, c)
		}
	}

	return out
}

// markerBounds locates the single generated region in doc, returning the byte
// offset of the begin marker and of the end marker. It requires exactly one of
// each marker in the correct order; a missing, duplicated, or out-of-order
// marker is an error. Enforcing a single pair is what makes the drift check
// sound — two marker pairs would let a stale second region survive both
// extraction and replacement while CI stayed green.
func markerBounds(doc string) (begin, end int, err error) {
	begin = strings.Index(doc, BeginMarker)
	if begin < 0 {
		return 0, 0, fmt.Errorf("begin marker %q not found in doc", BeginMarker)
	}

	if strings.LastIndex(doc, BeginMarker) != begin {
		return 0, 0, fmt.Errorf("multiple begin markers %q found in doc", BeginMarker)
	}

	end = strings.Index(doc, EndMarker)
	if end < 0 {
		return 0, 0, fmt.Errorf("end marker %q not found in doc", EndMarker)
	}

	if strings.LastIndex(doc, EndMarker) != end {
		return 0, 0, fmt.Errorf("multiple end markers %q found in doc", EndMarker)
	}

	if end < begin {
		return 0, 0, errors.New("end marker appears before begin marker")
	}

	return begin, end, nil
}

// ReplaceRegion returns doc with the text between BeginMarker and EndMarker
// replaced by the freshly rendered matrix. It errors if the markers are
// missing, duplicated, or out of order, so a malformed doc fails loudly rather
// than being silently rewritten.
func (m *Manifest) ReplaceRegion(doc string) (string, error) {
	begin, end, err := markerBounds(doc)
	if err != nil {
		return "", err
	}

	before := doc[:begin+len(BeginMarker)]
	after := doc[end:]

	return before + "\n\n" + m.MatrixMarkdown() + "\n" + after, nil
}

// ExtractRegion returns the trimmed content between the two markers.
func ExtractRegion(doc string) (string, error) {
	begin, end, err := markerBounds(doc)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(doc[begin+len(BeginMarker) : end]), nil
}

// Coverage reports, per surface, how many capabilities are in each state.
// Handy for a quick "how far behind is macOS" read; sorted by surface order.
func (m *Manifest) Coverage() map[string]map[string]int {
	out := map[string]map[string]int{}

	for _, s := range m.Surfaces {
		counts := map[string]int{}

		for _, c := range m.Capabilities {
			st, _ := c.state(s.ID)
			counts[st]++
		}

		out[s.ID] = counts
	}

	return out
}

// GUICapability is one capability projected for the GUI conformance check: its
// id and its state on the two GUI surfaces. The CLI column is deliberately
// dropped — the Swift check only reasons about iOS/macOS, and coupling the GUI
// fixture to CLI churn would make it stale for no reason.
type GUICapability struct {
	ID    string `json:"id"`
	IOS   string `json:"ios"`
	MacOS string `json:"macos"`
}

// GUIFixture is the language-neutral projection the Swift GraithSessionKit
// conformance test reads (issue #1149). It mirrors the #1129/#1144 protocol
// fixture: the Go manifest is the source of truth, this is committed under
// gui/ so SwiftPM can bundle it, and a staleness test keeps the two in step.
type GUIFixture struct {
	Version      int             `json:"version"`
	Capabilities []GUICapability `json:"capabilities"`
}

// GUIFixture projects the manifest to the GUI-facing fixture, in manifest order.
func (m *Manifest) GUIFixture() GUIFixture {
	caps := make([]GUICapability, 0, len(m.Capabilities))
	for _, c := range m.Capabilities {
		caps = append(caps, GUICapability{ID: c.ID, IOS: c.IOS, MacOS: c.MacOS})
	}

	return GUIFixture{Version: m.Version, Capabilities: caps}
}

// MarshalGUIFixture renders the GUI fixture as indented JSON with a trailing
// newline, so the committed file is diff-friendly and byte-stable.
func (m *Manifest) MarshalGUIFixture() ([]byte, error) {
	out, err := json.MarshalIndent(m.GUIFixture(), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal gui capability fixture: %w", err)
	}

	return append(out, '\n'), nil
}

// SurfaceIDs returns surface IDs in manifest order.
func (m *Manifest) SurfaceIDs() []string {
	ids := make([]string, 0, len(m.Surfaces))
	for _, s := range m.Surfaces {
		ids = append(ids, s.ID)
	}

	return ids
}

// StateIDs returns state IDs sorted, for stable iteration in callers.
func (m *Manifest) StateIDs() []string {
	ids := make([]string, 0, len(m.States))
	for _, s := range m.States {
		ids = append(ids, s.ID)
	}

	sort.Strings(ids)

	return ids
}
