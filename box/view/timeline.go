package view

import (
	"fmt"
	"sort"
	"strings"

	"github.com/windborneos/box-model/box"
)

// timelineRenderer sorts items by StoredAt desc and prints one event per line.
// Only AxisStoredAt is permitted.
type timelineRenderer struct{}

// Render emits one line per item, timestamp gutter on the left.
//
//	YYYY-MM-DD HH:MM ●─ <id-last-8> [<kind>] [<status-or-->] <storage-40>
//
// Items sharing the same calendar day reuse a blank timestamp gutter so the
// eye can follow runs of events.
func (r *timelineRenderer) Render(items []box.Item, opts RenderOptions) (string, error) {
	axis := opts.Axis
	if axis == "" {
		axis = AxisStoredAt
	}
	if axis != AxisStoredAt {
		return "", fmt.Errorf("%w: timeline only supports stored_at", ErrInvalidAxis)
	}

	sorted := make([]box.Item, len(items))
	copy(sorted, items)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].StoredAt.After(sorted[j].StoredAt)
	})

	var b strings.Builder
	prevDay := ""
	for _, it := range sorted {
		stamp := it.StoredAt.Format("2006-01-02 15:04")
		day := it.StoredAt.Format("2006-01-02")
		// Compact runs on same calendar day: blank the date portion.
		visible := stamp
		if day == prevDay {
			visible = "                " // 16 spaces, same width as "YYYY-MM-DD HH:MM"
		}
		prevDay = day
		status := firstSymbolValue(it, box.SymStatus)
		if status == "" {
			status = "-"
		}
		kind := it.Kind
		if kind == "" {
			kind = "-"
		}
		bullet := "●"
		if opts.Color {
			if c := statusColor(status); c != "" {
				bullet = c + "●" + ansiReset
			}
			visible = ansiGray + visible + ansiReset
		}
		fmt.Fprintf(&b, "%s %s─ %s [%s] [%s] %s\n",
			visible, bullet,
			shortID(it.ID, 8), kind, status,
			truncate(it.StorageURI, 40))
	}
	return b.String(), nil
}
