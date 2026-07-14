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
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

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
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("decode capability manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate checks the manifest is internally consistent: known surfaces and
// states, unique capability IDs, and every capability carrying a valid state
// for every surface.
func (m *Manifest) Validate() error {
	if m.Version != 1 {
		return fmt.Errorf("unsupported manifest version %d (want 1)", m.Version)
	}
	if len(m.Surfaces) == 0 {
		return fmt.Errorf("manifest has no surfaces")
	}
	if len(m.States) == 0 {
		return fmt.Errorf("manifest has no states")
	}
	if len(m.Capabilities) == 0 {
		return fmt.Errorf("manifest has no capabilities")
	}

	validStates := map[string]bool{}
	for _, s := range m.States {
		if s.ID == "" {
			return fmt.Errorf("state has empty id")
		}
		if s.Symbol == "" {
			return fmt.Errorf("state %q has empty symbol", s.ID)
		}
		if validStates[s.ID] {
			return fmt.Errorf("duplicate state id %q", s.ID)
		}
		validStates[s.ID] = true
	}

	seenSurface := map[string]bool{}
	for _, s := range m.Surfaces {
		if s.ID == "" {
			return fmt.Errorf("surface has empty id")
		}
		if seenSurface[s.ID] {
			return fmt.Errorf("duplicate surface id %q", s.ID)
		}
		seenSurface[s.ID] = true
	}

	seenCap := map[string]bool{}
	for _, c := range m.Capabilities {
		if c.ID == "" {
			return fmt.Errorf("capability has empty id")
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
		for _, c := range caps {
			name := c.Name
			if c.Notes != "" {
				name = fmt.Sprintf("%s <sup>1</sup>", name)
			}
			fmt.Fprintf(&b, "| %s |", name)
			for _, s := range m.Surfaces {
				st, _ := c.state(s.ID)
				fmt.Fprintf(&b, " %s |", m.symbolFor(st))
			}
			b.WriteString("\n")
		}

		// Footnotes for any noted capabilities in this category, stable order.
		var notes []Capability
		for _, c := range caps {
			if c.Notes != "" {
				notes = append(notes, c)
			}
		}
		if len(notes) > 0 {
			b.WriteString("\n")
			for _, c := range notes {
				fmt.Fprintf(&b, "<sup>1</sup> %s: %s\n", c.Name, c.Notes)
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

// ReplaceRegion returns doc with the text between BeginMarker and EndMarker
// replaced by the freshly rendered matrix. It errors if either marker is
// missing or they are out of order, so a malformed doc fails loudly rather
// than being silently rewritten.
func (m *Manifest) ReplaceRegion(doc string) (string, error) {
	begin := strings.Index(doc, BeginMarker)
	if begin < 0 {
		return "", fmt.Errorf("begin marker %q not found in doc", BeginMarker)
	}
	end := strings.Index(doc, EndMarker)
	if end < 0 {
		return "", fmt.Errorf("end marker %q not found in doc", EndMarker)
	}
	if end < begin {
		return "", fmt.Errorf("end marker appears before begin marker")
	}
	before := doc[:begin+len(BeginMarker)]
	after := doc[end:]
	return before + "\n\n" + m.MatrixMarkdown() + "\n" + after, nil
}

// ExtractRegion returns the trimmed content between the two markers.
func ExtractRegion(doc string) (string, error) {
	begin := strings.Index(doc, BeginMarker)
	if begin < 0 {
		return "", fmt.Errorf("begin marker %q not found in doc", BeginMarker)
	}
	end := strings.Index(doc, EndMarker)
	if end < 0 {
		return "", fmt.Errorf("end marker %q not found in doc", EndMarker)
	}
	if end < begin {
		return "", fmt.Errorf("end marker appears before begin marker")
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
