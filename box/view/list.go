package view

import (
	"fmt"
	"strings"

	"github.com/windborneos/box-model/box"
)

// listRenderer emits a plain ASCII table. opts.Axis is ignored (lists have
// no grouping); opts.Color is ignored (list is intentionally the most spartan
// view per spec).
type listRenderer struct{}

// Render produces a header + one line per item. Columns:
//
//	ID(last 20) | Rev | Kind | Status | Scope | Topic | Storage(40)
//
// Status takes the first SymStatus.Value ("-" if none). Scope/Topic
// concatenate every matching value with commas (truncated to 16 chars).
// StorageURI is truncated to 40 chars.
func (r *listRenderer) Render(items []box.Item, opts RenderOptions) (string, error) {
	header := "ID                   | Rev | Kind     | Status | Scope            | Topic            | Storage"
	sep := strings.Repeat("-", len(header))
	var b strings.Builder
	b.WriteString(header)
	b.WriteByte('\n')
	b.WriteString(sep)
	b.WriteByte('\n')
	for _, it := range items {
		id := shortID(it.ID, 20)
		status := firstSymbolValue(it, box.SymStatus)
		if status == "" {
			status = "-"
		}
		scope := truncate(strings.Join(allSymbolValues(it, box.SymScope), ","), 16)
		topic := truncate(strings.Join(allSymbolValues(it, box.SymTopic), ","), 16)
		if scope == "" {
			scope = "-"
		}
		if topic == "" {
			topic = "-"
		}
		fmt.Fprintf(&b, "%-20s | %3d | %-8s | %-6s | %-16s | %-16s | %s\n",
			id, it.Revision, truncate(it.Kind, 8), status, scope, topic,
			truncate(it.StorageURI, 40))
	}
	return b.String(), nil
}
