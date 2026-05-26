package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/windborneos/box-model/box"
	"github.com/windborneos/box-model/box/view"
)

// cmdView renders a box's items as a human-facing ASCII (optionally ANSI)
// projection. Per sop.md §9 the output is NEVER JSON.
//
// Usage:
//
//	box view <box-key> [--as=list|kanban|timeline] [--axis=...]
//	                   [--limit=N] [--include-history] [--kind=...]
//	                   [--label k=v]* [--ref k=v]*
//	                   [--stdin]
//
// --stdin reads a JSON []box.Item from stdin instead of calling
// Service.Browse, so views can be piped from `box trace`.
//
// Exit codes: 0 ok; 2 validation; 4 not found.
func (rc *rootContext) cmdView() int {
	fs := flag.NewFlagSet("view", flag.ContinueOnError)
	fs.SetOutput(rc.stderr)
	var (
		as         = fs.String("as", "list", "perspective: list|kanban|timeline|graph|tree|mind|matrix")
		axis       = fs.String("axis", "", "axis override; empty = perspective default")
		kind       = fs.String("kind", "", "filter by item.Kind")
		limit      = fs.Int("limit", 200, "max results")
		incHistory = fs.Bool("include-history", false, "include non-latest items")
		useStdin   = fs.Bool("stdin", false, "read JSON []Item from stdin instead of Browse")
		root       = fs.String("root", "", "override storage root")
		caller     = fs.String("caller", "", "override caller identity")
		labels     stringMap
		refs       stringMap
	)
	fs.Var(&labels, "label", "k=v label (repeatable)")
	fs.Var(&refs, "ref", "k=v source_ref (repeatable)")
	pos, flagArgs := splitArgs(rc.args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}

	items, code := rc.collectViewItems(*useStdin, pos, *root, *caller, *kind, *limit, *incHistory, &labels, &refs)
	if code != 0 {
		return code
	}
	return rc.renderToStdout(view.Perspective(*as), view.Axis(*axis), items)
}

// cmdRotate is a verb-shaped alias for cmdView that infers the Perspective
// from --axis:
//
//	--axis=status|kind|scope|topic|priority → kanban
//	--axis=stored_at                        → timeline
//
// --axis is required.
func (rc *rootContext) cmdRotate() int {
	fs := flag.NewFlagSet("rotate", flag.ContinueOnError)
	fs.SetOutput(rc.stderr)
	var (
		axis       = fs.String("axis", "", "axis: status|kind|stored_at|scope|topic|priority|relation (required)")
		kind       = fs.String("kind", "", "filter by item.Kind")
		limit      = fs.Int("limit", 200, "max results")
		incHistory = fs.Bool("include-history", false, "include non-latest items")
		useStdin   = fs.Bool("stdin", false, "read JSON []Item from stdin instead of Browse")
		root       = fs.String("root", "", "override storage root")
		caller     = fs.String("caller", "", "override caller identity")
		labels     stringMap
		refs       stringMap
	)
	fs.Var(&labels, "label", "k=v label (repeatable)")
	fs.Var(&refs, "ref", "k=v source_ref (repeatable)")
	pos, flagArgs := splitArgs(rc.args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if *axis == "" {
		fmt.Fprintln(rc.stderr, "Error: rotate requires --axis")
		return 2
	}
	p, ok := perspectiveForAxis(view.Axis(*axis))
	if !ok {
		fmt.Fprintf(rc.stderr, "Error: unknown --axis %q\n", *axis)
		return 2
	}

	items, code := rc.collectViewItems(*useStdin, pos, *root, *caller, *kind, *limit, *incHistory, &labels, &refs)
	if code != 0 {
		return code
	}
	return rc.renderToStdout(p, view.Axis(*axis), items)
}

// perspectiveForAxis maps an axis to its natural perspective. Returns false
// for unknown axes.
//
// `axis=relation` is a R0.7.4 addition — it picks the graph renderer. Tree
// and mind are NOT inferrable from any axis; users must request them
// explicitly with --as=tree / --as=mind on the `view` command.
func perspectiveForAxis(a view.Axis) (view.Perspective, bool) {
	switch a {
	case view.AxisStatus, view.AxisKind, view.AxisScope, view.AxisTopic, view.AxisPriority:
		return view.Kanban, true
	case view.AxisStoredAt:
		return view.Timeline, true
	case view.AxisRelation:
		return view.Graph, true
	}
	return "", false
}

// collectViewItems loads the items to render — either from stdin (when
// useStdin) or by calling Service.Browse. On error it writes to stderr and
// returns a non-zero exit code.
func (rc *rootContext) collectViewItems(useStdin bool, pos []string, root, caller, kind string, limit int, incHistory bool, labels, refs *stringMap) ([]box.Item, int) {
	if useStdin {
		data, err := io.ReadAll(rc.stdin)
		if err != nil {
			fmt.Fprintf(rc.stderr, "Error: %s\n", err.Error())
			return nil, 1
		}
		var items []box.Item
		if err := json.Unmarshal(data, &items); err != nil {
			fmt.Fprintf(rc.stderr, "Error: invalid --stdin JSON: %s\n", err.Error())
			return nil, 2
		}
		return items, 0
	}

	if len(pos) < 1 {
		fmt.Fprintln(rc.stderr, "Error: view/rotate requires <key> when --stdin not set")
		return nil, 2
	}
	key := pos[0]
	labelMap, ok := labels.toMap(rc.stderr)
	if !ok {
		return nil, 2
	}
	refMap, ok := refs.toMap(rc.stderr)
	if !ok {
		return nil, 2
	}
	svc, code := rc.openService(root)
	if code != 0 {
		return nil, code
	}
	ctx := context.Background()
	callerID := rc.resolveCallerExplicit(caller)
	b, err := resolveBoxByKey(ctx, svc, callerID, key)
	if err != nil {
		return nil, mapErr(err, rc.stderr)
	}
	filter := box.BrowseFilter{
		Kind:           kind,
		SourceRef:      refMap,
		Labels:         labelMap,
		Limit:          limit,
		IncludeHistory: incHistory,
	}
	items, err := svc.Browse(ctx, b.ID, filter)
	if err != nil {
		return nil, mapErr(err, rc.stderr)
	}
	return items, 0
}

// renderToStdout dispatches to the Renderer and writes the result to stdout.
// The renderer error is mapped to exit code 2 for invalid-axis, 1 otherwise.
func (rc *rootContext) renderToStdout(p view.Perspective, axis view.Axis, items []box.Item) int {
	r, err := view.MapTo(p)
	if err != nil {
		fmt.Fprintf(rc.stderr, "Error: %s\n", err.Error())
		return 2
	}
	opts := view.RenderOptions{
		Axis:  axis,
		Color: stdoutIsTTY(rc.stdout) && rc.env("BOX_NO_COLOR") != "1",
	}
	out, err := r.Render(items, opts)
	if err != nil {
		fmt.Fprintf(rc.stderr, "Error: %s\n", err.Error())
		if isInvalidAxis(err) {
			return 2
		}
		return 1
	}
	if _, err := io.WriteString(rc.stdout, out); err != nil {
		fmt.Fprintf(rc.stderr, "Error: %s\n", err.Error())
		return 1
	}
	return 0
}

// isInvalidAxis is a thin errors.Is wrapper so renderToStdout can return the
// documented exit code 2 for axis-validation problems without importing
// errors twice.
func isInvalidAxis(err error) bool {
	for e := err; e != nil; {
		if e == view.ErrInvalidAxis {
			return true
		}
		un, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = un.Unwrap()
	}
	return false
}
