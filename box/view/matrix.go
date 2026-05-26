// Package view: matrix renderer.
//
// Matrix is a 2-D summary table: SymKind on rows (D/R/Q/H/T/M/F/O/A/X plus a
// "(none)" row for items with no SymKind) × SymStatus on columns (?/→/✓/✗/~/◯
// plus a "(none)" column). Each cell shows the item count; empty cells render
// as "." (not 0) so the eye can pick them out. A "Total" column trails every
// row and a "Total" row trails the body; the grand total sits at the bottom
// right.
//
// Matrix is a 2-D view, so it is NEVER reachable via `box rotate --axis=...`
// (axes are one-dimensional). Users must request it explicitly with
// `box view <key> --as=matrix`. The renderer ignores RenderOptions.Axis (it
// does not return ErrInvalidAxis for any value).
package view

import (
	"fmt"
	"strings"

	"github.com/windborneos/box-model/box"
)

// matrixRenderer emits a fixed kind × status table. The axes are closed sets
// (the whitelists `kindOrder` and `statusOrder` from kanban.go) with an extra
// "(none)" row/column for items missing the relevant symbol.
type matrixRenderer struct{}

// Column widths used by the renderer.
//
//   labelWidth — width of the row-label gutter (e.g. "R" or "(none)")
//   cellWidth  — width of each data cell, including the "Total" cell
const (
	matrixLabelWidth = 14
	matrixCellWidth  = 5
)

// noneBucket is the synthetic label for items lacking a SymKind or SymStatus.
const noneBucket = "(none)"

// Render writes the matrix as ASCII. RenderOptions.Axis is ignored — matrix
// is 2-D by definition. RenderOptions.Color / Width are also not honoured
// (the table is fixed-shape; colourising the kind/status header literals
// could be added later if needed).
func (r *matrixRenderer) Render(items []box.Item, opts RenderOptions) (string, error) {
	_ = opts

	rows := append(append([]string{}, kindOrder...), noneBucket)
	cols := append(append([]string{}, statusOrder...), noneBucket)

	// Tally counts. Items whose SymKind / SymStatus is outside the whitelist
	// fall through to the (none) bucket too — same rule kanban applies for
	// its inList check.
	cell := make(map[[2]string]int, len(rows)*len(cols))
	for _, it := range items {
		k := firstSymbolValue(it, box.SymKind)
		if k == "" || !inList(k, kindOrder) {
			k = noneBucket
		}
		s := firstSymbolValue(it, box.SymStatus)
		if s == "" || !inList(s, statusOrder) {
			s = noneBucket
		}
		cell[[2]string{k, s}]++
	}

	var b strings.Builder

	// Leading blank line, then header row: label gutter + each col header
	// padded to cellWidth, then "Total".
	b.WriteByte('\n')
	b.WriteString(strings.Repeat(" ", matrixLabelWidth))
	for _, c := range cols {
		writeCell(&b, c)
	}
	writeCell(&b, "Total")
	b.WriteByte('\n')

	// Body: one row per rows entry. Empty cells get ".".
	grandTotal := 0
	colTotals := make([]int, len(cols))
	for _, row := range rows {
		writeLabel(&b, row)
		rowTotal := 0
		for ci, c := range cols {
			n := cell[[2]string{row, c}]
			if n == 0 {
				writeCell(&b, ".")
			} else {
				writeCell(&b, fmt.Sprintf("%d", n))
			}
			rowTotal += n
			colTotals[ci] += n
		}
		if rowTotal == 0 {
			writeCell(&b, ".")
		} else {
			writeCell(&b, fmt.Sprintf("%d", rowTotal))
		}
		grandTotal += rowTotal
		b.WriteByte('\n')
	}

	// Divider: label gutter + cellWidth * (len(cols)+1).
	dividerLen := matrixLabelWidth + matrixCellWidth*(len(cols)+1)
	b.WriteString(strings.Repeat("─", dividerLen))
	b.WriteByte('\n')

	// Total row.
	writeLabel(&b, "Total")
	for _, ct := range colTotals {
		if ct == 0 {
			writeCell(&b, ".")
		} else {
			writeCell(&b, fmt.Sprintf("%d", ct))
		}
	}
	if grandTotal == 0 {
		writeCell(&b, ".")
	} else {
		writeCell(&b, fmt.Sprintf("%d", grandTotal))
	}
	b.WriteByte('\n')

	// Trailing blank line.
	b.WriteByte('\n')

	return b.String(), nil
}

// writeLabel left-aligns s into the matrixLabelWidth gutter.
func writeLabel(b *strings.Builder, s string) {
	b.WriteString(padRight(s, matrixLabelWidth))
}

// writeCell left-aligns s into a matrixCellWidth column.
func writeCell(b *strings.Builder, s string) {
	b.WriteString(padRight(s, matrixCellWidth))
}

// padRight returns s padded on the right with spaces to exactly w runes. If
// s is wider than w, it is returned unchanged.
func padRight(s string, w int) string {
	n := runeLen(s)
	if n >= w {
		return s
	}
	return s + strings.Repeat(" ", w-n)
}
