// Package view renders box.Item slices as human-facing ASCII/ANSI projections.
//
// Views are human-facing per sop.md §9: they NEVER emit JSON. The three
// perspectives shipped in R0.7.3 are List (flat table), Kanban (grouped
// columns) and Timeline (chronological). Each Renderer is invoked by the
// `box view` / `box rotate` CLI commands; tests exercise the renderers
// directly through MapTo.
package view

import (
	"errors"
	"fmt"
	"strings"

	"github.com/windborneos/box-model/box"
)

// newMermaidReplacer returns the strings.Replacer used by mermaidEscape. Kept
// at package level to avoid re-allocating on every call.
func newMermaidReplacer() *strings.Replacer {
	return strings.NewReplacer(
		`"`, "#quot;",
		"[", "#91;",
		"]", "#93;",
		"|", "#124;",
	)
}

// Perspective is the user-visible name of a view shape.
type Perspective string

const (
	List     Perspective = "list"
	Kanban   Perspective = "kanban"
	Timeline Perspective = "timeline"
	Graph    Perspective = "graph"
	Tree     Perspective = "tree"
	Mind     Perspective = "mind"
	Matrix   Perspective = "matrix"
)

// Axis is the grouping or ordering dimension. Each Perspective has a default
// Axis (kanban→status, timeline→stored_at, list→ignored) and a permitted set;
// passing an Axis outside the permitted set returns ErrInvalidAxis.
type Axis string

const (
	AxisStatus   Axis = "status"
	AxisKind     Axis = "kind"
	AxisStoredAt Axis = "stored_at"
	AxisScope    Axis = "scope"
	AxisTopic    Axis = "topic"
	AxisPriority Axis = "priority"
	// AxisRelation is the natural axis for the Graph perspective. Used by
	// `box rotate --axis=relation` to pick the graph renderer.
	AxisRelation Axis = "relation"
)

// RenderOptions tunes a single render call. Color is only honoured when the
// caller has determined stdout is a TTY (see box/cli stdoutIsTTY). Width=0
// means "use the default 80".
type RenderOptions struct {
	Axis  Axis
	Color bool
	Width int
}

// Renderer is the interface implemented by every per-perspective renderer.
type Renderer interface {
	Render(items []box.Item, opts RenderOptions) (string, error)
}

// ErrUnknownPerspective is returned by MapTo when the requested name does not
// match a known Perspective.
var ErrUnknownPerspective = errors.New("unknown perspective")

// ErrInvalidAxis is returned by a Renderer when RenderOptions.Axis is not one
// of the perspective's permitted axes.
var ErrInvalidAxis = errors.New("invalid axis for perspective")

// MapTo returns the Renderer for a Perspective, or ErrUnknownPerspective if
// the name is not recognised. Renderer implementations are unexported; only
// the interface is exposed to callers.
func MapTo(p Perspective) (Renderer, error) {
	switch p {
	case List:
		return &listRenderer{}, nil
	case Kanban:
		return &kanbanRenderer{}, nil
	case Timeline:
		return &timelineRenderer{}, nil
	case Graph:
		return &graphRenderer{}, nil
	case Tree:
		return &treeRenderer{}, nil
	case Mind:
		return &mindRenderer{}, nil
	case Matrix:
		return &matrixRenderer{}, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownPerspective, p)
	}
}

// firstSymbolValue returns the Value of the first Symbol on it whose Kind
// matches; returns "" if none.
func firstSymbolValue(it box.Item, kind box.SymbolKind) string {
	for _, s := range it.Symbols {
		if s.Kind == kind {
			return s.Value
		}
	}
	return ""
}

// allSymbolValues returns every Value on it whose Kind matches, in order.
func allSymbolValues(it box.Item, kind box.SymbolKind) []string {
	var out []string
	for _, s := range it.Symbols {
		if s.Kind == kind {
			out = append(out, s.Value)
		}
	}
	return out
}

// truncate returns s shortened to max chars, with no ellipsis (callers know
// the column width and assume hard truncation).
func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// shortID returns the last n characters of the item ID; if shorter, returns
// the full ID.
func shortID(id string, n int) string {
	if len(id) <= n {
		return id
	}
	return id[len(id)-n:]
}

// mermaidEscape escapes characters that conflict with mermaid label syntax.
// Mermaid label syntax treats "[]|" as structural; we replace each conflicting
// rune with the corresponding HTML-entity-style escape that mermaid renders
// literally inside node labels.
func mermaidEscape(s string) string {
	r := newMermaidReplacer()
	return r.Replace(s)
}

// mermaidNodeID returns a stable mermaid-safe node identifier derived from an
// item id. Mermaid node IDs are alphanumeric/underscore; we replace anything
// else with `_` and prefix with `n_` so a numeric-only id still parses.
func mermaidNodeID(itemID string) string {
	out := make([]byte, 0, len(itemID)+2)
	out = append(out, 'n', '_')
	for i := 0; i < len(itemID); i++ {
		c := itemID[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '_':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}
