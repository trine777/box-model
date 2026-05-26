package view

import (
	"fmt"
	"sort"
	"strings"

	"github.com/windborneos/box-model/box"
)

// kanbanRenderer groups items into columns by the first matching Symbol of
// the configured Axis kind. AxisStoredAt is rejected (continuous values
// don't bucket).
type kanbanRenderer struct{}

// Whitelists/orderings for closed-set axes.
var (
	statusOrder   = []string{"?", "→", "✓", "✗", "~", "◯"}
	kindOrder     = []string{"D", "R", "Q", "H", "T", "M", "F", "O", "A", "X"}
	priorityOrder = []string{"*", "**", "***"}
)

// ANSI escape codes; emitted only when opts.Color is true.
const (
	ansiReset    = "\x1b[0m"
	ansiGray     = "\x1b[90m"
	ansiYellow   = "\x1b[33m"
	ansiGreen    = "\x1b[32m"
	ansiRed      = "\x1b[31m"
	ansiMagenta  = "\x1b[35m"
	ansiDimGray  = "\x1b[2;90m"
)

// statusColor returns the ANSI prefix for a status literal, or "" if unknown.
func statusColor(v string) string {
	switch v {
	case "?":
		return ansiGray
	case "→":
		return ansiYellow
	case "✓":
		return ansiGreen
	case "✗":
		return ansiRed
	case "~":
		return ansiMagenta
	case "◯":
		return ansiDimGray
	}
	return ""
}

// Render emits a multi-column board. Default axis is status.
func (r *kanbanRenderer) Render(items []box.Item, opts RenderOptions) (string, error) {
	axis := opts.Axis
	if axis == "" {
		axis = AxisStatus
	}
	symKind, allowed, order, err := kanbanAxisSpec(axis)
	if err != nil {
		return "", err
	}

	// Determine the column order. For closed-set axes we use the predefined
	// order plus "(none)" at the end for items lacking the symbol. For open
	// sets (scope/topic) we sort observed values alphabetically.
	columns := []string{}
	colItems := map[string][]box.Item{}
	noneCol := "(none)"

	if order != nil {
		// Closed set: pre-seed each known column so it appears even if empty.
		columns = append([]string{}, order...)
	}
	seen := map[string]bool{}
	for _, c := range columns {
		seen[c] = true
	}
	for _, it := range items {
		v := firstSymbolValue(it, symKind)
		if v == "" || (order != nil && !inList(v, order)) {
			if !seen[noneCol] {
				columns = append(columns, noneCol)
				seen[noneCol] = true
			}
			colItems[noneCol] = append(colItems[noneCol], it)
			continue
		}
		if !seen[v] {
			columns = append(columns, v)
			seen[v] = true
		}
		colItems[v] = append(colItems[v], it)
	}

	if order == nil {
		// Open-set axis: sort the discovered values alphabetically, then
		// append (none) (if present) at the very end.
		open := []string{}
		hadNone := false
		for _, c := range columns {
			if c == noneCol {
				hadNone = true
				continue
			}
			open = append(open, c)
		}
		sort.Strings(open)
		columns = open
		if hadNone {
			columns = append(columns, noneCol)
		}
	}
	_ = allowed // axis whitelist already enforced via kanbanAxisSpec

	// Column width: divide width evenly, floor at 20.
	totalWidth := opts.Width
	if totalWidth <= 0 {
		totalWidth = 80
	}
	nCols := len(columns)
	if nCols == 0 {
		return "", nil
	}
	colWidth := (totalWidth - (nCols + 1)) / nCols
	if colWidth < 20 {
		colWidth = 20
	}

	var b strings.Builder
	// Top border.
	writeBorder(&b, nCols, colWidth, "┌", "┬", "┐")
	// Header row.
	b.WriteString("│")
	for _, c := range columns {
		hdr := c
		if opts.Color {
			if col := statusColor(c); col != "" {
				hdr = col + c + ansiReset
			}
		}
		// pad to colWidth (account for ANSI invisible characters by padding raw).
		b.WriteString(" ")
		b.WriteString(hdr)
		pad := colWidth - 1 - runeLen(c)
		if pad < 0 {
			pad = 0
		}
		b.WriteString(strings.Repeat(" ", pad))
		b.WriteString("│")
	}
	b.WriteByte('\n')
	writeBorder(&b, nCols, colWidth, "├", "┼", "┤")

	// Body: emit one row per max-depth card across columns.
	maxRows := 0
	for _, c := range columns {
		if n := len(colItems[c]); n > maxRows {
			maxRows = n
		}
	}
	for row := 0; row < maxRows; row++ {
		b.WriteString("│")
		for _, c := range columns {
			cell := ""
			if row < len(colItems[c]) {
				it := colItems[c][row]
				cell = fmt.Sprintf("%s %s %s",
					shortID(it.ID, 8),
					truncate(it.Kind, 6),
					firstSymbolValue(it, box.SymStatus))
				cell = strings.TrimSpace(cell)
			}
			b.WriteString(" ")
			b.WriteString(truncate(cell, colWidth-2))
			pad := colWidth - 1 - runeLen(truncate(cell, colWidth-2))
			if pad < 0 {
				pad = 0
			}
			b.WriteString(strings.Repeat(" ", pad))
			b.WriteString("│")
		}
		b.WriteByte('\n')
	}
	writeBorder(&b, nCols, colWidth, "└", "┴", "┘")
	return b.String(), nil
}

// kanbanAxisSpec validates the axis and returns (symbolKind, axisAllowed,
// columnOrderOrNil). For open-set axes (scope/topic) order==nil signals
// data-driven ordering.
func kanbanAxisSpec(a Axis) (box.SymbolKind, bool, []string, error) {
	switch a {
	case AxisStatus:
		return box.SymStatus, true, statusOrder, nil
	case AxisKind:
		return box.SymKind, true, kindOrder, nil
	case AxisPriority:
		return box.SymPriority, true, priorityOrder, nil
	case AxisScope:
		return box.SymScope, true, nil, nil
	case AxisTopic:
		return box.SymTopic, true, nil, nil
	case AxisStoredAt:
		return "", false, nil, fmt.Errorf("%w: kanban does not support stored_at", ErrInvalidAxis)
	default:
		return "", false, nil, fmt.Errorf("%w: %q", ErrInvalidAxis, a)
	}
}

// inList reports whether v appears in xs.
func inList(v string, xs []string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// runeLen returns the rune count of s (so multi-byte glyphs like → don't
// over-pad the column).
func runeLen(s string) int {
	return len([]rune(s))
}

// writeBorder draws one border row using the given corner/intersection runes.
func writeBorder(b *strings.Builder, nCols, colWidth int, left, mid, right string) {
	for i := 0; i < nCols; i++ {
		if i == 0 {
			b.WriteString(left)
		} else {
			b.WriteString(mid)
		}
		b.WriteString(strings.Repeat("─", colWidth))
	}
	b.WriteString(right)
	b.WriteByte('\n')
}
