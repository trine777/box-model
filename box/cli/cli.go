// Package cli implements the `box` command-line interface as a library so it
// can be exercised end-to-end from tests without spawning processes.
//
// The single entrypoint is Run, which takes the full argv slice (including the
// program name at argv[0]), three IO streams, an environment-lookup function,
// and a Store factory. main wraps it with the real os.* values; tests inject
// in-memory equivalents.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/windborneos/box-model/box"
	"github.com/windborneos/box-model/box/obs"
)

// splitArgs partitions args into (positional, flagArgs). Positional arguments
// are those that appear before any flag-style token (starting with '-'). This
// keeps the user-facing CLI shape "box <cmd> <positional...> --flag..." while
// still letting us use stdlib flag for the flag tail.
func splitArgs(args []string) (positional, flags []string) {
	for i, a := range args {
		if strings.HasPrefix(a, "-") {
			return args[:i], args[i:]
		}
	}
	return args, nil
}

// stringMap collects repeatable "--label k=v" / "--ref k=v" flags. The first
// '=' is the separator; an empty value (e.g. "c=") is allowed. The same name
// can be repeated.
type stringMap struct {
	pairs []string
}

func (s *stringMap) String() string  { return strings.Join(s.pairs, ",") }
func (s *stringMap) Set(v string) error {
	s.pairs = append(s.pairs, v)
	return nil
}

func (s *stringMap) toMap(stderr io.Writer) (map[string]string, bool) {
	out := map[string]string{}
	for _, p := range s.pairs {
		i := strings.IndexByte(p, '=')
		if i < 0 {
			fmt.Fprintf(stderr, "Error: expected key=value, got %q\n", p)
			return nil, false
		}
		out[p[:i]] = p[i+1:]
	}
	return out, true
}

// stringSlice collects a repeatable flag's raw values (used for --location).
type stringSlice struct {
	values []string
}

func (s *stringSlice) String() string { return strings.Join(s.values, ",") }
func (s *stringSlice) Set(v string) error {
	s.values = append(s.values, v)
	return nil
}

type rootContext struct {
	args         []string
	stdin        io.Reader
	stdout       io.Writer
	stderr       io.Writer
	env          func(string) string
	storeFactory func(string) (box.Store, error)

	// observer is the active obs.Observer for this invocation; populated
	// lazily by ensureObserver(). logFile/snapPath are bookkeeping for the
	// deferred close + snapshot-merge that runs at the end of Run().
	observer obs.Observer
	logFile  *os.File
	snapPath string
}

// Run executes a single CLI invocation.
//
//	args:         full os.Args (incl. argv[0])
//	stdin/stdout/stderr: usually os.Stdin/os.Stdout/os.Stderr; tests inject buffers
//	env:          function to look up env vars; usually os.Getenv. Tests pass a
//	              map-backed func.
//	storeFactory: function that builds the Store given root. Defaults to
//	              OpenFileStore. Tests inject a factory returning a shared
//	              MemoryStore for fast in-memory tests.
//
// Returns the process exit code; the caller (main) is responsible for
// os.Exit(code).
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer,
	env func(string) string,
	storeFactory func(root string) (box.Store, error)) int {
	if env == nil {
		env = os.Getenv
	}
	if storeFactory == nil {
		storeFactory = defaultStoreFactory
	}
	if len(args) < 2 {
		return cmdHelp(stdout)
	}
	cmd := args[1]
	rest := args[2:]
	rc := &rootContext{
		args:         rest,
		stdin:        stdin,
		stdout:       stdout,
		stderr:       stderr,
		env:          env,
		storeFactory: storeFactory,
	}
	// Close the log file at the end of every invocation and merge live
	// metrics into the persisted snapshot so `box stats` can read them
	// later. Failures here are best-effort (observability never blocks
	// the CLI's return code).
	defer rc.flushObserver()

	switch cmd {
	case "init":
		return rc.cmdInit()
	case "store":
		return rc.cmdStore()
	case "browse":
		return rc.cmdBrowse()
	case "show":
		return rc.cmdShow()
	case "replace":
		return rc.cmdReplace()
	case "tag":
		return rc.cmdTag()
	case "delete":
		return rc.cmdDelete()
	case "consume":
		return rc.cmdConsume()
	case "summary":
		return rc.cmdSummary()
	case "seal":
		return rc.cmdSeal()
	case "stats":
		return rc.cmdStats()
	case "logs":
		return rc.cmdLogs()
	case "trace":
		return rc.cmdTrace()
	case "legend":
		return rc.cmdLegend()
	case "neighbors":
		return rc.cmdNeighbors()
	case "view":
		return rc.cmdView()
	case "rotate":
		return rc.cmdRotate()
	case "task_create":
		return rc.cmdTaskCreate()
	case "task_status":
		return rc.cmdTaskStatus()
	case "task_trace":
		return rc.cmdTaskTrace()
	case "task_list_trace":
		return rc.cmdTaskListTrace()
	case "task_show":
		return rc.cmdTaskShow()
	case "help", "-h", "--help":
		return cmdHelp(stdout)
	default:
		fmt.Fprintf(stderr, "Error: unknown command %q\n", cmd)
		return 2
	}
}

// ensureObserver lazily constructs the per-invocation observer on first use
// so commands that don't touch the Service (stats, logs) skip the file open.
func (rc *rootContext) ensureObserver() obs.Observer {
	if rc.observer != nil {
		return rc.observer
	}
	o, f, snapPath := rc.buildObserver()
	rc.observer = o
	rc.logFile = f
	rc.snapPath = snapPath
	return rc.observer
}

// flushObserver is called by Run's defer. It (a) merges in-memory metrics
// into the persisted snapshot if we have a MemObserver, and (b) closes the
// log file. All errors are swallowed — observability is best-effort.
func (rc *rootContext) flushObserver() {
	if mo, ok := rc.observer.(*obs.MemObserver); ok && rc.snapPath != "" {
		_ = mergeAndPersist(mo, rc.snapPath)
	}
	if rc.logFile != nil {
		_ = rc.logFile.Close()
	}
}

func defaultStoreFactory(root string) (box.Store, error) {
	return box.OpenFileStore(root)
}

func cmdHelp(stdout io.Writer) int {
	fmt.Fprintln(stdout, `box — a multidimensional content box.

Usage:
  box <command> [args] [flags]

Commands:
  init     <key>           Create a new box.
  store    <key>           Store an item into a box.
  browse   <key>           List items in a box.
  show     <item_id>       Show a single item (pure read; never consumes).
  replace  <prev_item_id>  Create a new revision of an item.
  tag      <item_id>       Replace the labels on an item.
  delete   <item_id>       Soft-delete an item.
  consume  <item_id>       Record a consume audit and optionally mark consumed.
  summary  <key>           Summarize a box.
  seal     <key>           Seal a box (no further writes).
  stats                    Print accumulated metrics counters/timers.
  logs                     Tail structured JSON log records.
  trace                    Query items by Symbol dimensions (JSON output).
  legend   <symbol_token>  Show the documentation entry for a Symbol literal.
  neighbors <item_id>      Print the hop-bounded relation subgraph (JSON).
  view     <key>           Render a box as list/kanban/timeline/graph/tree/mind/matrix (ASCII or mermaid; no JSON).
  rotate   <key>           Render via --axis (status/kind/stored_at/relation/...); alias of view.
  task_create <key>        Create a task item with intent/goal/pass_criteria/nail_chain.
  task_status <task_id>    Set task status by writing symbols [T, <status>] in place.
  task_trace <task_id>     Append one TraceStep (JSON) to the task's trace log.
  task_list_trace <task_id> List the task's full trace history (JSON).
  task_show <task_id>      Show the task item (kind=task) JSON.
  help                     Show this help.

Global flags:
  --root=PATH    Override the box home (defaults to $BOX_HOME or ~/.box).
  --caller=ID    Override the caller identity (defaults to $BOX_CALLER).

Output:
  Most commands emit JSON to stdout. Use --format=id for ID-only output,
  or --format=table where supported.`)
	return 0
}

// caller / root resolution -------------------------------------------------

// resolveCallerExplicit returns the caller as overridden by --caller / $BOX_CALLER.
// If neither is set, returns "" — callers are then expected to fall back to the
// box owner (one-person-company self-call friendliness).
func (rc *rootContext) resolveCallerExplicit(callerFlag string) string {
	if callerFlag != "" {
		return callerFlag
	}
	return rc.env("BOX_CALLER")
}

// resolveRoot resolves the storage root: --root > $BOX_HOME > ~/.box.
func (rc *rootContext) resolveRoot(rootFlag string) (string, error) {
	if rootFlag != "" {
		return rootFlag, nil
	}
	if v := rc.env("BOX_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".box"), nil
}

// openService is a convenience: resolve root, open store, build service.
// The per-invocation Observer is wired into both the Service (for verb-level
// metrics) and, when supported, the FileStore (for journal-replay / IO error
// signals). FileStore.SetObserver is a no-op on the in-memory test factory.
//
// The Store itself is intentionally not returned — D#10. All CLI commands
// must go through the Service façade so caller/owner/history semantics are
// enforced uniformly. If a command needs a lookup the Service doesn't expose
// yet, expose it on the Service instead of grabbing the Store here.
func (rc *rootContext) openService(rootFlag string) (*box.Service, int) {
	root, err := rc.resolveRoot(rootFlag)
	if err != nil {
		fmt.Fprintf(rc.stderr, "Error: %s\n", err.Error())
		return nil, 1
	}
	st, err := rc.storeFactory(root)
	if err != nil {
		fmt.Fprintf(rc.stderr, "Error: %s\n", err.Error())
		return nil, 1
	}
	observer := rc.ensureObserver()
	if fs, ok := st.(*box.FileStore); ok {
		fs.SetObserver(observer)
	}
	return box.NewService(st, box.WithObserver(observer)), 0
}

// mapErr maps a domain error to the documented exit code, printing the
// message to stderr. Returns 0 for nil.
func mapErr(err error, stderr io.Writer) int {
	if err == nil {
		return 0
	}
	fmt.Fprintf(stderr, "Error: %s\n", err.Error())
	switch {
	case errors.Is(err, box.ErrValidation):
		return 2
	case errors.Is(err, box.ErrForbidden):
		return 3
	case errors.Is(err, box.ErrNotFound):
		return 4
	case errors.Is(err, box.ErrConflict):
		return 5
	default:
		return 1
	}
}

// content resolution -------------------------------------------------------

// loadContent reads --content STR|@file|- per the spec. Empty raw means
// "flag not supplied"; the service handles the empty case.
func (rc *rootContext) loadContent(raw string) (json.RawMessage, error) {
	if raw == "" {
		return nil, nil
	}
	switch {
	case raw == "-":
		data, err := io.ReadAll(rc.stdin)
		if err != nil {
			return nil, err
		}
		return json.RawMessage(data), nil
	case strings.HasPrefix(raw, "@"):
		data, err := os.ReadFile(raw[1:])
		if err != nil {
			return nil, err
		}
		return json.RawMessage(data), nil
	default:
		return json.RawMessage(raw), nil
	}
}

// resolveBoxByKey resolves a box by its public key through the Service layer.
func resolveBoxByKey(ctx context.Context, svc *box.Service, callerID, key string) (box.Box, error) {
	return svc.GetBoxByKey(ctx, callerID, key)
}

// output helpers -----------------------------------------------------------

func writeJSON(w io.Writer, v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	_, err = w.Write([]byte("\n"))
	return err
}

func writeID(w io.Writer, id string) error {
	_, err := fmt.Fprintln(w, id)
	return err
}

func writeItem(w io.Writer, item box.Item, format string) error {
	switch format {
	case "id":
		return writeID(w, item.ID)
	default:
		return writeJSON(w, item)
	}
}

func writeBox(w io.Writer, b box.Box, format string) error {
	switch format {
	case "id":
		return writeID(w, b.ID)
	default:
		return writeJSON(w, b)
	}
}

// labelSummary returns a short ", "-joined "k=v" preview limited to 60 chars.
func labelSummary(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	out := strings.Join(parts, ", ")
	if len(out) > 60 {
		out = out[:57] + "..."
	}
	return out
}

func writeItemTable(w io.Writer, items []box.Item) error {
	fmt.Fprintln(w, "ID | Revision | Kind | Status | Labels")
	for _, it := range items {
		fmt.Fprintf(w, "%s | %d | %s | %s | %s\n",
			it.ID, it.Revision, it.Kind, it.Status, labelSummary(it.Labels))
	}
	return nil
}

// subcommands --------------------------------------------------------------

func (rc *rootContext) cmdInit() int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(rc.stderr)
	var (
		owner     = fs.String("owner", "", "owner id (required)")
		ownerType = fs.String("owner-type", "standalone", "owner type")
		root      = fs.String("root", "", "override storage root")
		caller    = fs.String("caller", "", "override caller identity")
		format    = fs.String("format", "json", "output format: json|id")
		labels    stringMap
	)
	fs.Var(&labels, "label", "k=v label (repeatable)")
	_ = caller // init: caller is the new box's owner; --caller is accepted but not used.
	pos, flagArgs := splitArgs(rc.args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(pos) < 1 {
		fmt.Fprintln(rc.stderr, "Error: init requires <key>")
		return 2
	}
	key := pos[0]
	if *owner == "" {
		fmt.Fprintln(rc.stderr, "Error: --owner is required")
		return 2
	}
	labelMap, ok := labels.toMap(rc.stderr)
	if !ok {
		return 2
	}
	svc, code := rc.openService(*root)
	if code != 0 {
		return code
	}
	b, err := svc.CreateBox(context.Background(), box.CreateBoxRequest{
		Key:       key,
		OwnerType: *ownerType,
		OwnerID:   *owner,
		Labels:    labelMap,
	})
	if err != nil {
		return mapErr(err, rc.stderr)
	}
	if err := writeBox(rc.stdout, b, *format); err != nil {
		fmt.Fprintf(rc.stderr, "Error: %s\n", err.Error())
		return 1
	}
	return 0
}

func (rc *rootContext) cmdStore() int {
	fs := flag.NewFlagSet("store", flag.ContinueOnError)
	fs.SetOutput(rc.stderr)
	var (
		kind       = fs.String("kind", "", "item kind (required)")
		source     = fs.String("source", "", "source type (required)")
		storage    = fs.String("storage", "", "storage uri (required)")
		idem       = fs.String("idem", "", "idempotency key")
		format     = fs.String("format", "json", "output format: json|id")
		itemFormat = fs.String("item-format", "", "Item.Format (e.g. json|markdown|text); defaults to 'json'")
		locID      = fs.String("location-id", "", "location id")
		content    = fs.String("content", "", "content: STR | @file | -")
		root       = fs.String("root", "", "override storage root")
		caller     = fs.String("caller", "", "override caller identity")
		labels     stringMap
		refs       stringMap
		// R0.7.2 — Symbol flags. --notation is mutually exclusive with the
		// explicit --kind-sym / --status / --scope / ... block.
		kindSym    = fs.String("kind-sym", "", "Symbol kind literal (D/R/Q/...)")
		statusSym  = fs.String("status", "", "Symbol status (?/→/✓/✗/~/◯ or open|wip|done|rejected|blocked|archived)")
		prioSym    = fs.String("priority", "", "Symbol priority (* / ** / *** or low|med|high)")
		notation   = fs.String("notation", "", "SLP literal string; mutually exclusive with explicit symbol flags")
		scopes     stringSlice
		topics     stringSlice
		domains    stringSlice
		dependsOn  stringSlice
		supersedes stringSlice
		refines    stringSlice
		similarTo  stringSlice
		altTo      stringSlice
		hasPart    stringSlice
	)
	fs.Var(&labels, "label", "k=v label (repeatable)")
	fs.Var(&refs, "ref", "k=v source_ref (repeatable)")
	fs.Var(&scopes, "scope", "Symbol scope (repeatable)")
	fs.Var(&topics, "topic", "Symbol topic (repeatable)")
	fs.Var(&domains, "domain", "Symbol domain ns:value (repeatable)")
	fs.Var(&dependsOn, "depends-on", "Relation: depends-on <item_id> (repeatable)")
	fs.Var(&supersedes, "supersedes", "Relation: supersedes <item_id> (repeatable)")
	fs.Var(&refines, "refines", "Relation: refines <item_id> (repeatable)")
	fs.Var(&similarTo, "similar-to", "Relation: similar-to <item_id> (repeatable)")
	fs.Var(&altTo, "alt-to", "Relation: alternative-to <item_id> (repeatable)")
	fs.Var(&hasPart, "has-part", "Relation: has-part <item_id> (repeatable)")
	pos, flagArgs := splitArgs(rc.args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(pos) < 1 {
		fmt.Fprintln(rc.stderr, "Error: store requires <key>")
		return 2
	}
	key := pos[0]
	labelMap, ok := labels.toMap(rc.stderr)
	if !ok {
		return 2
	}
	refMap, ok := refs.toMap(rc.stderr)
	if !ok {
		return 2
	}
	contentBytes, err := rc.loadContent(*content)
	if err != nil {
		fmt.Fprintf(rc.stderr, "Error: %s\n", err.Error())
		return 1
	}
	syms, code := collectSymbolsForStore(rc.stderr, symbolFlags{
		notation:   *notation,
		kindSym:    *kindSym,
		statusSym:  *statusSym,
		prioSym:    *prioSym,
		scopes:     scopes.values,
		topics:     topics.values,
		domains:    domains.values,
		dependsOn:  dependsOn.values,
		supersedes: supersedes.values,
		refines:    refines.values,
		similarTo:  similarTo.values,
		altTo:      altTo.values,
		hasPart:    hasPart.values,
	})
	if code != 0 {
		return code
	}

	svc, code := rc.openService(*root)
	if code != 0 {
		return code
	}
	ctx := context.Background()
	callerID := rc.resolveCallerExplicit(*caller)
	b, err := resolveBoxByKey(ctx, svc, callerID, key)
	if err != nil {
		return mapErr(err, rc.stderr)
	}
	if callerID == "" {
		callerID = b.OwnerID
	}
	if callerID == "" {
		fmt.Fprintln(rc.stderr, "Error: cannot resolve caller (set --caller or BOX_CALLER)")
		return 2
	}
	item, err := svc.Store(ctx, callerID, b.ID, box.StoreRequest{
		IdemKey:    *idem,
		Kind:       *kind,
		SourceType: *source,
		SourceRef:  refMap,
		Labels:     labelMap,
		LocationID: *locID,
		StorageURI: *storage,
		Format:     *itemFormat,
		Content:    contentBytes,
		StoredBy:   callerID,
		Symbols:    syms,
	})
	if err != nil {
		return mapErr(err, rc.stderr)
	}
	if err := writeItem(rc.stdout, item, *format); err != nil {
		fmt.Fprintf(rc.stderr, "Error: %s\n", err.Error())
		return 1
	}
	return 0
}

func (rc *rootContext) cmdBrowse() int {
	fs := flag.NewFlagSet("browse", flag.ContinueOnError)
	fs.SetOutput(rc.stderr)
	var (
		kind        = fs.String("kind", "", "filter by kind")
		limit       = fs.Int("limit", 50, "max results")
		offset     = fs.Int("offset", 0, "offset")
		incHistory = fs.Bool("include-history", false, "include non-latest items")
		onlyHist   = fs.Bool("only-history", false, "only non-latest items")
		format     = fs.String("format", "json", "output format: json|table|id")
		root       = fs.String("root", "", "override storage root")
		caller     = fs.String("caller", "", "override caller identity")
		labels     stringMap
		refs       stringMap
		locations  stringSlice
	)
	fs.Var(&labels, "label", "k=v label (repeatable)")
	fs.Var(&refs, "ref", "k=v source_ref (repeatable)")
	fs.Var(&locations, "location", "location id (repeatable)")
	pos, flagArgs := splitArgs(rc.args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(pos) < 1 {
		fmt.Fprintln(rc.stderr, "Error: browse requires <key>")
		return 2
	}
	key := pos[0]
	labelMap, ok := labels.toMap(rc.stderr)
	if !ok {
		return 2
	}
	refMap, ok := refs.toMap(rc.stderr)
	if !ok {
		return 2
	}

	svc, code := rc.openService(*root)
	if code != 0 {
		return code
	}
	ctx := context.Background()
	callerID := rc.resolveCallerExplicit(*caller)
	b, err := resolveBoxByKey(ctx, svc, callerID, key)
	if err != nil {
		return mapErr(err, rc.stderr)
	}
	filter := box.BrowseFilter{
		Kind:           *kind,
		SourceRef:      refMap,
		Labels:         labelMap,
		LocationIDs:    locations.values,
		Limit:          *limit,
		Offset:         *offset,
		IncludeHistory: *incHistory,
		OnlyHistory:    *onlyHist,
	}
	items, err := svc.Browse(ctx, b.ID, filter)
	if err != nil {
		return mapErr(err, rc.stderr)
	}
	switch *format {
	case "id":
		for _, it := range items {
			if err := writeID(rc.stdout, it.ID); err != nil {
				fmt.Fprintf(rc.stderr, "Error: %s\n", err.Error())
				return 1
			}
		}
	case "table":
		if err := writeItemTable(rc.stdout, items); err != nil {
			fmt.Fprintf(rc.stderr, "Error: %s\n", err.Error())
			return 1
		}
	default:
		if err := writeJSON(rc.stdout, items); err != nil {
			fmt.Fprintf(rc.stderr, "Error: %s\n", err.Error())
			return 1
		}
	}
	return 0
}

func (rc *rootContext) cmdShow() int {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	fs.SetOutput(rc.stderr)
	var (
		format = fs.String("format", "json", "output format: json|id")
		root   = fs.String("root", "", "override storage root")
		caller = fs.String("caller", "", "override caller identity")
	)
	_ = caller
	pos, flagArgs := splitArgs(rc.args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(pos) < 1 {
		fmt.Fprintln(rc.stderr, "Error: show requires <item_id>")
		return 2
	}
	itemID := pos[0]
	svc, code := rc.openService(*root)
	if code != 0 {
		return code
	}
	// GetItem currently ignores caller; pass empty string. (Pure read.)
	item, err := svc.GetItem(context.Background(), "", itemID)
	if err != nil {
		return mapErr(err, rc.stderr)
	}
	if err := writeItem(rc.stdout, item, *format); err != nil {
		fmt.Fprintf(rc.stderr, "Error: %s\n", err.Error())
		return 1
	}
	return 0
}

func (rc *rootContext) cmdReplace() int {
	fs := flag.NewFlagSet("replace", flag.ContinueOnError)
	fs.SetOutput(rc.stderr)
	var (
		kind       = fs.String("kind", "", "item kind (defaults to prev)")
		storage    = fs.String("storage", "", "storage uri (required)")
		idem       = fs.String("idem", "", "idempotency key")
		format     = fs.String("format", "json", "output format: json|id")
		itemFormat = fs.String("item-format", "", "Item.Format")
		locID      = fs.String("location-id", "", "location id")
		content    = fs.String("content", "", "content: STR | @file | -")
		root       = fs.String("root", "", "override storage root")
		caller     = fs.String("caller", "", "override caller identity")
		labels     stringMap
		refs       stringMap
		kindSym    = fs.String("kind-sym", "", "Symbol kind literal (D/R/Q/...)")
		statusSym  = fs.String("status", "", "Symbol status")
		prioSym    = fs.String("priority", "", "Symbol priority")
		notation   = fs.String("notation", "", "SLP literal string")
		scopes     stringSlice
		topics     stringSlice
		domains    stringSlice
		dependsOn  stringSlice
		supersedes stringSlice
		refines    stringSlice
		similarTo  stringSlice
		altTo      stringSlice
		hasPart    stringSlice
	)
	fs.Var(&labels, "label", "k=v label (repeatable)")
	fs.Var(&refs, "ref", "k=v source_ref (repeatable)")
	fs.Var(&scopes, "scope", "Symbol scope (repeatable)")
	fs.Var(&topics, "topic", "Symbol topic (repeatable)")
	fs.Var(&domains, "domain", "Symbol domain ns:value (repeatable)")
	fs.Var(&dependsOn, "depends-on", "Relation: depends-on <item_id> (repeatable)")
	fs.Var(&supersedes, "supersedes", "Relation: supersedes <item_id> (repeatable)")
	fs.Var(&refines, "refines", "Relation: refines <item_id> (repeatable)")
	fs.Var(&similarTo, "similar-to", "Relation: similar-to <item_id> (repeatable)")
	fs.Var(&altTo, "alt-to", "Relation: alternative-to <item_id> (repeatable)")
	fs.Var(&hasPart, "has-part", "Relation: has-part <item_id> (repeatable)")
	pos, flagArgs := splitArgs(rc.args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(pos) < 1 {
		fmt.Fprintln(rc.stderr, "Error: replace requires <prev_item_id>")
		return 2
	}
	prevID := pos[0]
	labelMap, ok := labels.toMap(rc.stderr)
	if !ok {
		return 2
	}
	refMap, ok := refs.toMap(rc.stderr)
	if !ok {
		return 2
	}
	contentBytes, err := rc.loadContent(*content)
	if err != nil {
		fmt.Fprintf(rc.stderr, "Error: %s\n", err.Error())
		return 1
	}
	syms, code := collectSymbolsForStore(rc.stderr, symbolFlags{
		notation:   *notation,
		kindSym:    *kindSym,
		statusSym:  *statusSym,
		prioSym:    *prioSym,
		scopes:     scopes.values,
		topics:     topics.values,
		domains:    domains.values,
		dependsOn:  dependsOn.values,
		supersedes: supersedes.values,
		refines:    refines.values,
		similarTo:  similarTo.values,
		altTo:      altTo.values,
		hasPart:    hasPart.values,
	})
	if code != 0 {
		return code
	}
	svc, scode := rc.openService(*root)
	if scode != 0 {
		return scode
	}
	ctx := context.Background()
	// Resolve caller via the item → box owner chain.
	callerID := rc.resolveCallerExplicit(*caller)
	if callerID == "" {
		prev, err := svc.GetItem(ctx, "", prevID)
		if err != nil {
			return mapErr(err, rc.stderr)
		}
		b, err := svc.GetBox(ctx, "", prev.BoxID)
		if err != nil {
			return mapErr(err, rc.stderr)
		}
		callerID = b.OwnerID
	}
	if callerID == "" {
		fmt.Fprintln(rc.stderr, "Error: cannot resolve caller (set --caller or BOX_CALLER)")
		return 2
	}
	item, err := svc.ReplaceItem(ctx, callerID, prevID, box.StoreRequest{
		IdemKey:    *idem,
		Kind:       *kind,
		SourceRef:  refMap,
		Labels:     labelMap,
		LocationID: *locID,
		StorageURI: *storage,
		Format:     *itemFormat,
		Content:    contentBytes,
		StoredBy:   callerID,
		Symbols:    syms,
	})
	if err != nil {
		return mapErr(err, rc.stderr)
	}
	if err := writeItem(rc.stdout, item, *format); err != nil {
		fmt.Fprintf(rc.stderr, "Error: %s\n", err.Error())
		return 1
	}
	return 0
}

func (rc *rootContext) cmdTag() int {
	fs := flag.NewFlagSet("tag", flag.ContinueOnError)
	fs.SetOutput(rc.stderr)
	var (
		format = fs.String("format", "json", "output format: json|id")
		root   = fs.String("root", "", "override storage root")
		caller = fs.String("caller", "", "override caller identity")
		labels stringMap
	)
	fs.Var(&labels, "label", "k=v label (repeatable; full replacement)")
	pos, flagArgs := splitArgs(rc.args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(pos) < 1 {
		fmt.Fprintln(rc.stderr, "Error: tag requires <item_id>")
		return 2
	}
	itemID := pos[0]
	labelMap, ok := labels.toMap(rc.stderr)
	if !ok {
		return 2
	}
	svc, code := rc.openService(*root)
	if code != 0 {
		return code
	}
	ctx := context.Background()
	callerID := rc.resolveCallerExplicit(*caller)
	if callerID == "" {
		item, err := svc.GetItem(ctx, "", itemID)
		if err != nil {
			return mapErr(err, rc.stderr)
		}
		b, err := svc.GetBox(ctx, "", item.BoxID)
		if err != nil {
			return mapErr(err, rc.stderr)
		}
		callerID = b.OwnerID
	}
	if callerID == "" {
		fmt.Fprintln(rc.stderr, "Error: cannot resolve caller (set --caller or BOX_CALLER)")
		return 2
	}
	item, err := svc.UpdateLabels(ctx, callerID, itemID, labelMap)
	if err != nil {
		return mapErr(err, rc.stderr)
	}
	if err := writeItem(rc.stdout, item, *format); err != nil {
		fmt.Fprintf(rc.stderr, "Error: %s\n", err.Error())
		return 1
	}
	return 0
}

func (rc *rootContext) cmdDelete() int {
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	fs.SetOutput(rc.stderr)
	var (
		format = fs.String("format", "json", "output format: json|id")
		root   = fs.String("root", "", "override storage root")
		caller = fs.String("caller", "", "override caller identity")
	)
	pos, flagArgs := splitArgs(rc.args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(pos) < 1 {
		fmt.Fprintln(rc.stderr, "Error: delete requires <item_id>")
		return 2
	}
	itemID := pos[0]
	svc, code := rc.openService(*root)
	if code != 0 {
		return code
	}
	ctx := context.Background()
	callerID := rc.resolveCallerExplicit(*caller)
	if callerID == "" {
		item, err := svc.GetItem(ctx, "", itemID)
		if err == nil {
			b, err := svc.GetBox(ctx, "", item.BoxID)
			if err == nil {
				callerID = b.OwnerID
			}
		}
	}
	if callerID == "" {
		fmt.Fprintln(rc.stderr, "Error: cannot resolve caller (set --caller or BOX_CALLER)")
		return 2
	}
	item, err := svc.DeleteItem(ctx, callerID, itemID)
	if err != nil {
		return mapErr(err, rc.stderr)
	}
	if err := writeItem(rc.stdout, item, *format); err != nil {
		fmt.Fprintf(rc.stderr, "Error: %s\n", err.Error())
		return 1
	}
	return 0
}

func (rc *rootContext) cmdConsume() int {
	fs := flag.NewFlagSet("consume", flag.ContinueOnError)
	fs.SetOutput(rc.stderr)
	var (
		mark      = fs.Bool("mark", false, "mark the item as consumed (default if neither flag set)")
		auditOnly = fs.Bool("audit-only", false, "record an audit entry without marking")
		purpose   = fs.String("purpose", "", "purpose of consume")
		format    = fs.String("format", "json", "output format: json|id")
		root      = fs.String("root", "", "override storage root")
		caller    = fs.String("caller", "", "override caller identity")
	)
	pos, flagArgs := splitArgs(rc.args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(pos) < 1 {
		fmt.Fprintln(rc.stderr, "Error: consume requires <item_id>")
		return 2
	}
	if *mark && *auditOnly {
		fmt.Fprintln(rc.stderr, "Error: --mark and --audit-only are mutually exclusive")
		return 2
	}
	if !*mark && !*auditOnly {
		*mark = true
	}
	itemID := pos[0]
	svc, code := rc.openService(*root)
	if code != 0 {
		return code
	}
	ctx := context.Background()
	callerID := rc.resolveCallerExplicit(*caller)
	if callerID == "" {
		item, err := svc.GetItem(ctx, "", itemID)
		if err == nil {
			b, err := svc.GetBox(ctx, "", item.BoxID)
			if err == nil {
				callerID = b.OwnerID
			}
		}
	}
	if callerID == "" {
		fmt.Fprintln(rc.stderr, "Error: cannot resolve caller (set --caller or BOX_CALLER)")
		return 2
	}
	item, err := svc.Consume(ctx, callerID, itemID, box.ConsumeOptions{
		Purpose:      *purpose,
		MarkConsumed: *mark,
	})
	if err != nil {
		return mapErr(err, rc.stderr)
	}
	if err := writeItem(rc.stdout, item, *format); err != nil {
		fmt.Fprintf(rc.stderr, "Error: %s\n", err.Error())
		return 1
	}
	return 0
}

func (rc *rootContext) cmdSummary() int {
	fs := flag.NewFlagSet("summary", flag.ContinueOnError)
	fs.SetOutput(rc.stderr)
	var (
		format = fs.String("format", "json", "output format: json|table")
		root   = fs.String("root", "", "override storage root")
	)
	pos, flagArgs := splitArgs(rc.args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(pos) < 1 {
		fmt.Fprintln(rc.stderr, "Error: summary requires <key>")
		return 2
	}
	key := pos[0]
	svc, code := rc.openService(*root)
	if code != 0 {
		return code
	}
	ctx := context.Background()
	b, err := resolveBoxByKey(ctx, svc, "", key)
	if err != nil {
		return mapErr(err, rc.stderr)
	}
	s, err := svc.Summary(ctx, b.ID)
	if err != nil {
		return mapErr(err, rc.stderr)
	}
	switch *format {
	case "table":
		fmt.Fprintf(rc.stdout, "Box %s (%s): %d items\n", b.Key, b.ID, s.TotalItems)
		kinds := make([]string, 0, len(s.ByKind))
		for k := range s.ByKind {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		for _, k := range kinds {
			fmt.Fprintf(rc.stdout, "  %s: %d\n", k, s.ByKind[k])
		}
	default:
		if err := writeJSON(rc.stdout, s); err != nil {
			fmt.Fprintf(rc.stderr, "Error: %s\n", err.Error())
			return 1
		}
	}
	return 0
}

func (rc *rootContext) cmdSeal() int {
	fs := flag.NewFlagSet("seal", flag.ContinueOnError)
	fs.SetOutput(rc.stderr)
	var (
		root   = fs.String("root", "", "override storage root")
		caller = fs.String("caller", "", "override caller identity")
	)
	pos, flagArgs := splitArgs(rc.args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(pos) < 1 {
		fmt.Fprintln(rc.stderr, "Error: seal requires <key>")
		return 2
	}
	key := pos[0]
	svc, code := rc.openService(*root)
	if code != 0 {
		return code
	}
	ctx := context.Background()
	callerID := rc.resolveCallerExplicit(*caller)
	b, err := resolveBoxByKey(ctx, svc, callerID, key)
	if err != nil {
		return mapErr(err, rc.stderr)
	}
	if callerID == "" {
		callerID = b.OwnerID
	}
	if callerID == "" {
		fmt.Fprintln(rc.stderr, "Error: cannot resolve caller (set --caller or BOX_CALLER)")
		return 2
	}
	if err := svc.SealBox(ctx, callerID, b.ID); err != nil {
		return mapErr(err, rc.stderr)
	}
	// Re-fetch to print the sealed box.
	sealed, err := svc.GetBox(ctx, callerID, b.ID)
	if err != nil {
		return mapErr(err, rc.stderr)
	}
	if err := writeJSON(rc.stdout, sealed); err != nil {
		fmt.Fprintf(rc.stderr, "Error: %s\n", err.Error())
		return 1
	}
	return 0
}

// cmdStats prints accumulated metrics from the persisted snapshot.
// The snapshot is updated at the end of every CLI invocation via
// flushObserver → mergeAndPersist, so successive runs accrue cross-process
// totals. Use --name to filter by metric prefix; --reset truncates the
// snapshot after printing.
func (rc *rootContext) cmdStats() int {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	fs.SetOutput(rc.stderr)
	var (
		name  = fs.String("name", "", "filter by metric name prefix")
		reset = fs.Bool("reset", false, "clear snapshot after printing")
	)
	_, flagArgs := splitArgs(rc.args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	paths := rc.resolveObserverPaths()
	snap, err := loadSnapshot(paths.snapshotPath)
	if err != nil {
		fmt.Fprintf(rc.stderr, "Error: %s\n", err.Error())
		return 1
	}
	if len(snap.Counters) == 0 && len(snap.Timers) == 0 && len(snap.Observed) == 0 {
		fmt.Fprintln(rc.stdout, "no metrics yet")
		return 0
	}
	printSnapshot(rc.stdout, snap, *name)
	if *reset {
		// Best-effort overwrite with an empty snapshot.
		empty := persistedSnapshot{
			Counters: map[string]int64{},
			Timers:   map[string]aggSamples{},
			Observed: map[string]aggSamples{},
		}
		if data, err := json.MarshalIndent(empty, "", "  "); err == nil {
			_ = os.WriteFile(paths.snapshotPath, data, 0o644)
		}
	}
	return 0
}

// cmdLogs tails the structured JSON log file. Supports filtering by minimum
// level, op (case-sensitive match), and relative duration via --since.
func (rc *rootContext) cmdLogs() int {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(rc.stderr)
	var (
		tail  = fs.Int("tail", 50, "max lines to print (from the tail)")
		level = fs.String("level", "", "minimum level: debug|info|warn|error")
		op    = fs.String("op", "", "filter by op field")
		since = fs.String("since", "", "relative duration like 1h, 24h, 7d")
	)
	_, flagArgs := splitArgs(rc.args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	paths := rc.resolveObserverPaths()
	dur, err := parseSinceDuration(*since)
	if err != nil {
		fmt.Fprintf(rc.stderr, "Error: %s\n", err.Error())
		return 2
	}
	// Flush any pending log writes from this same invocation before reading.
	if rc.logFile != nil {
		_ = rc.logFile.Sync()
	}
	if err := tailLogFile(rc.stdout, paths.logPath, *tail, *level, *op, dur); err != nil {
		fmt.Fprintf(rc.stderr, "Error: %s\n", err.Error())
		return 1
	}
	return 0
}

// symbolFlags is the bag of Symbol-related flag values collected by
// cmdStore and cmdReplace. collectSymbolsForStore converts it into a
// []box.Symbol (or returns a non-zero exit code on validation error).
type symbolFlags struct {
	notation   string
	kindSym    string
	statusSym  string
	prioSym    string
	scopes     []string
	topics     []string
	domains    []string
	dependsOn  []string
	supersedes []string
	refines    []string
	similarTo  []string
	altTo      []string
	hasPart    []string
}

// collectSymbolsForStore turns CLI flag state into a slice of Symbols
// suitable for StoreRequest.Symbols. If --notation is set it takes precedence
// (and any explicit Symbol flag becomes an ErrValidation per R0.7.2 §2).
//
// Returns (symbols, exitCode). exitCode 0 == success.
func collectSymbolsForStore(stderr io.Writer, sf symbolFlags) ([]box.Symbol, int) {
	hasExplicit := sf.kindSym != "" || sf.statusSym != "" || sf.prioSym != "" ||
		len(sf.scopes) > 0 || len(sf.topics) > 0 || len(sf.domains) > 0 ||
		len(sf.dependsOn) > 0 || len(sf.supersedes) > 0 || len(sf.refines) > 0 ||
		len(sf.similarTo) > 0 || len(sf.altTo) > 0 || len(sf.hasPart) > 0
	if sf.notation != "" && hasExplicit {
		fmt.Fprintln(stderr, "Error: --notation is mutually exclusive with explicit symbol flags")
		return nil, 2
	}
	if sf.notation != "" {
		syms, err := ParseNotation(sf.notation)
		if err != nil {
			fmt.Fprintf(stderr, "Error: %s\n", err.Error())
			return nil, 2
		}
		return syms, 0
	}
	if !hasExplicit {
		// No symbol flags supplied — keep backward compat (nil = no symbols).
		return nil, 0
	}
	var syms []box.Symbol
	if sf.kindSym != "" {
		syms = append(syms, box.Symbol{Kind: box.SymKind, Value: sf.kindSym})
	}
	if sf.statusSym != "" {
		v, ok := normalizeStatus(sf.statusSym)
		if !ok {
			fmt.Fprintf(stderr, "Error: unknown --status %q\n", sf.statusSym)
			return nil, 2
		}
		syms = append(syms, box.Symbol{Kind: box.SymStatus, Value: v})
	}
	if sf.prioSym != "" {
		v, ok := normalizePriority(sf.prioSym)
		if !ok {
			fmt.Fprintf(stderr, "Error: unknown --priority %q\n", sf.prioSym)
			return nil, 2
		}
		syms = append(syms, box.Symbol{Kind: box.SymPriority, Value: v})
	}
	for _, v := range sf.scopes {
		syms = append(syms, box.Symbol{Kind: box.SymScope, Value: v})
	}
	for _, v := range sf.topics {
		syms = append(syms, box.Symbol{Kind: box.SymTopic, Value: v})
	}
	for _, v := range sf.domains {
		syms = append(syms, box.Symbol{Kind: box.SymDomain, Value: v})
	}
	// Relation flags: each value is an item_id; the Symbol.Value is the
	// canonical relation literal.
	for _, ref := range sf.dependsOn {
		syms = append(syms, box.Symbol{Kind: box.SymRelation, Value: "&", Ref: ref})
	}
	for _, ref := range sf.supersedes {
		syms = append(syms, box.Symbol{Kind: box.SymRelation, Value: ">", Ref: ref})
	}
	for _, ref := range sf.refines {
		syms = append(syms, box.Symbol{Kind: box.SymRelation, Value: "<", Ref: ref})
	}
	for _, ref := range sf.similarTo {
		syms = append(syms, box.Symbol{Kind: box.SymRelation, Value: "≈", Ref: ref})
	}
	for _, ref := range sf.altTo {
		syms = append(syms, box.Symbol{Kind: box.SymRelation, Value: "|", Ref: ref})
	}
	for _, ref := range sf.hasPart {
		syms = append(syms, box.Symbol{Kind: box.SymRelation, Value: "⊃", Ref: ref})
	}
	return syms, 0
}

// statusAlias maps human-friendly --status / SLP shorthand to the canonical
// status literal accepted by ValidateSymbol. Identity entries make the table
// double as a whitelist for direct-literal input. Used by both cmdStore and
// cmdTrace (the symbol kinds map identically across the two surfaces).
var statusAlias = map[string]string{
	"open": "?", "wip": "→", "done": "✓",
	"rejected": "✗", "blocked": "~", "archived": "◯",
	"?": "?", "→": "→", "✓": "✓", "✗": "✗", "~": "~", "◯": "◯",
}

// priorityAlias maps shorthand to the canonical priority literal (* / ** / ***).
var priorityAlias = map[string]string{
	"low": "*", "med": "**", "high": "***",
	"*": "*", "**": "**", "***": "***",
}

// normalizeStatus resolves an alias to its canonical literal; the second
// return is false if v is not a recognised alias.
func normalizeStatus(v string) (string, bool) {
	out, ok := statusAlias[v]
	return out, ok
}

// normalizePriority resolves an alias to its canonical literal.
func normalizePriority(v string) (string, bool) {
	out, ok := priorityAlias[v]
	return out, ok
}

// cmdTrace queries items by Symbol dimensions. Output is always JSON to
// stdout (R0.7.3 will add human-friendly views as a separate command).
//
// Flag-to-SymbolKind mapping mirrors collectSymbolsForStore: each populated
// dimension contributes its SymbolKind to query.Kind and its values to
// query.Value. The R0.7.1 query semantics ("any matching symbol satisfies the
// query") apply — a fine-grained AND-across-dimensions filter is R0.7.4.
func (rc *rootContext) cmdTrace() int {
	fs := flag.NewFlagSet("trace", flag.ContinueOnError)
	fs.SetOutput(rc.stderr)
	var (
		boxFlag = fs.String("box", "", "restrict to one box by key; empty = all boxes")
		rel     = fs.String("rel", "", "relation literal (>, <, &, |, ≈, ⊃)")
		ref     = fs.String("ref", "", "ref item id (used with --rel)")
		root    = fs.String("root", "", "override storage root")
		caller  = fs.String("caller", "", "override caller identity")
		kinds   stringSlice
		statuses stringSlice
		scopes  stringSlice
		topics  stringSlice
		prios   stringSlice
		domains stringSlice
	)
	fs.Var(&kinds, "kind", "Symbol kind literal (repeatable)")
	fs.Var(&statuses, "status", "Symbol status literal or alias (repeatable)")
	fs.Var(&scopes, "scope", "Symbol scope value (repeatable)")
	fs.Var(&topics, "topic", "Symbol topic value (repeatable)")
	fs.Var(&prios, "priority", "Symbol priority literal or alias (repeatable)")
	fs.Var(&domains, "domain", "Symbol domain ns:value (repeatable)")
	_, flagArgs := splitArgs(rc.args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	svc, code := rc.openService(*root)
	if code != 0 {
		return code
	}
	ctx := context.Background()
	// Best-effort bootstrap: a missing __symbols__ box doesn't block trace.
	if err := svc.EnsureSymbolBootstrap(ctx); err != nil {
		fmt.Fprintf(rc.stderr, "warn: symbol bootstrap failed: %s\n", err.Error())
	}
	query, code := buildTraceQuery(rc.stderr, kinds.values, statuses.values, scopes.values, topics.values, prios.values, domains.values, *rel, *ref)
	if code != 0 {
		return code
	}
	callerID := rc.resolveCallerExplicit(*caller)
	items, err := svc.Trace(ctx, callerID, *boxFlag, query)
	if err != nil {
		return mapErr(err, rc.stderr)
	}
	if err := writeJSON(rc.stdout, items); err != nil {
		fmt.Fprintf(rc.stderr, "Error: %s\n", err.Error())
		return 1
	}
	return 0
}

// buildTraceQuery folds the flag-slice state into a SymbolQuery. Each
// populated dimension contributes (a) its SymbolKind to Kind and (b) its
// values (normalised where applicable) to Value. Relation is a singleton
// (Value=rel, Ref=ref).
func buildTraceQuery(stderr io.Writer, kinds, statuses, scopes, topics, prios, domains []string, rel, ref string) (box.SymbolQuery, int) {
	q := box.SymbolQuery{}
	if len(kinds) > 0 {
		q.Kind = append(q.Kind, box.SymKind)
		q.Value = append(q.Value, kinds...)
	}
	if len(statuses) > 0 {
		q.Kind = append(q.Kind, box.SymStatus)
		for _, raw := range statuses {
			v, ok := normalizeStatus(raw)
			if !ok {
				fmt.Fprintf(stderr, "Error: unknown --status %q\n", raw)
				return box.SymbolQuery{}, 2
			}
			q.Value = append(q.Value, v)
		}
	}
	if len(scopes) > 0 {
		q.Kind = append(q.Kind, box.SymScope)
		q.Value = append(q.Value, scopes...)
	}
	if len(topics) > 0 {
		q.Kind = append(q.Kind, box.SymTopic)
		q.Value = append(q.Value, topics...)
	}
	if len(prios) > 0 {
		q.Kind = append(q.Kind, box.SymPriority)
		for _, raw := range prios {
			v, ok := normalizePriority(raw)
			if !ok {
				fmt.Fprintf(stderr, "Error: unknown --priority %q\n", raw)
				return box.SymbolQuery{}, 2
			}
			q.Value = append(q.Value, v)
		}
	}
	if len(domains) > 0 {
		q.Kind = append(q.Kind, box.SymDomain)
		q.Value = append(q.Value, domains...)
	}
	if rel != "" {
		q.Kind = append(q.Kind, box.SymRelation)
		q.Value = append(q.Value, rel)
		q.Ref = ref
	}
	return q, 0
}

// cmdLegend looks up the documentation Item for a single Symbol literal.
// The single positional argument is the symbol token itself: a single-letter
// kind, a status sigil, a relation operator, a priority literal, or a
// "ns:value" domain string. We classify it here (not via ParseNotation,
// which would reject a bare relation operator).
func (rc *rootContext) cmdLegend() int {
	fs := flag.NewFlagSet("legend", flag.ContinueOnError)
	fs.SetOutput(rc.stderr)
	var (
		root   = fs.String("root", "", "override storage root")
		caller = fs.String("caller", "", "override caller identity")
	)
	pos, flagArgs := splitArgs(rc.args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(pos) < 1 {
		fmt.Fprintln(rc.stderr, "Error: legend requires <symbol_token>")
		return 2
	}
	tok := pos[0]
	sym, ok := classifyLegendToken(tok)
	if !ok {
		fmt.Fprintf(rc.stderr, "Error: %s: unrecognised symbol token %q\n", box.ErrValidation.Error(), tok)
		return 2
	}
	svc, code := rc.openService(*root)
	if code != 0 {
		return code
	}
	ctx := context.Background()
	if err := svc.EnsureSymbolBootstrap(ctx); err != nil {
		fmt.Fprintf(rc.stderr, "warn: symbol bootstrap failed: %s\n", err.Error())
	}
	callerID := rc.resolveCallerExplicit(*caller)
	item, err := svc.LegendOf(ctx, callerID, sym)
	if err != nil {
		return mapErr(err, rc.stderr)
	}
	if err := writeJSON(rc.stdout, item); err != nil {
		fmt.Fprintf(rc.stderr, "Error: %s\n", err.Error())
		return 1
	}
	return 0
}

// classifyLegendToken infers the SymbolKind from a bare token. Relation
// operators are returned with an empty Ref since legend queries describe the
// symbol itself, not a particular edge.
func classifyLegendToken(tok string) (box.Symbol, bool) {
	if len(tok) == 1 {
		switch tok {
		case "D", "R", "Q", "H", "T", "M", "F", "O", "A", "X":
			return box.Symbol{Kind: box.SymKind, Value: tok}, true
		case "?", "~":
			return box.Symbol{Kind: box.SymStatus, Value: tok}, true
		case "*":
			return box.Symbol{Kind: box.SymPriority, Value: tok}, true
		}
	}
	switch tok {
	case "→", "✓", "✗", "◯":
		return box.Symbol{Kind: box.SymStatus, Value: tok}, true
	case "**", "***":
		return box.Symbol{Kind: box.SymPriority, Value: tok}, true
	case ">", "<", "&", "|", "≈", "⊃":
		return box.Symbol{Kind: box.SymRelation, Value: tok}, true
	}
	if strings.Contains(tok, ":") {
		return box.Symbol{Kind: box.SymDomain, Value: tok}, true
	}
	return box.Symbol{}, false
}

// cmdNeighbors prints the hop-bounded subgraph of relation edges around
// the given item.
func (rc *rootContext) cmdNeighbors() int {
	fs := flag.NewFlagSet("neighbors", flag.ContinueOnError)
	fs.SetOutput(rc.stderr)
	var (
		hops   = fs.Int("hops", 1, "BFS hop limit (1-5)")
		root   = fs.String("root", "", "override storage root")
		caller = fs.String("caller", "", "override caller identity")
	)
	pos, flagArgs := splitArgs(rc.args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(pos) < 1 {
		fmt.Fprintln(rc.stderr, "Error: neighbors requires <item_id>")
		return 2
	}
	itemID := pos[0]
	svc, code := rc.openService(*root)
	if code != 0 {
		return code
	}
	ctx := context.Background()
	if err := svc.EnsureSymbolBootstrap(ctx); err != nil {
		fmt.Fprintf(rc.stderr, "warn: symbol bootstrap failed: %s\n", err.Error())
	}
	callerID := rc.resolveCallerExplicit(*caller)
	sub, err := svc.Neighbors(ctx, callerID, itemID, *hops)
	if err != nil {
		return mapErr(err, rc.stderr)
	}
	if err := writeJSON(rc.stdout, sub); err != nil {
		fmt.Fprintf(rc.stderr, "Error: %s\n", err.Error())
		return 1
	}
	return 0
}

// parseSinceDuration accepts an empty string (no filter) or a value parseable
// by time.ParseDuration, plus the convenience suffix "d" (days). Returns 0
// for no filter.
//
// --- R0.10 task commands -------------------------------------------------
//
// Five top-level commands surface the Service task API: task_create /
// task_status / task_trace / task_list_trace / task_show. They are top-level
// (rather than nested under a `task` parent) to match the existing flat
// dispatch in Run, and the underscore form mirrors the MCP tool naming
// (box_create_task / box_set_task_status / ...).

// cmdTaskCreate creates a task in the named box. The task body is rich
// (intent / goal / pass_criteria / nail_chain) but Box treats every field
// as opaque schema — invariant #10 (the agent runs pass_criteria.query, not
// Box).
func (rc *rootContext) cmdTaskCreate() int {
	fs := flag.NewFlagSet("task_create", flag.ContinueOnError)
	fs.SetOutput(rc.stderr)
	var (
		intent    = fs.String("intent", "", "task intent (required)")
		passKind  = fs.String("pass-kind", "", "pass criteria kind: exists|absent|all_match|count_eq (required)")
		passQuery = fs.String("pass-query", "", "pass criteria query as JSON (e.g. {\"kind\":[\"kind\"],\"value\":[\"R\"]})")
		passArg   = fs.Int("pass-arg", 0, "pass criteria arg (count_eq only)")
		passReas  = fs.String("pass-reason", "", "free-text rationale for the pass criteria (required)")
		root      = fs.String("root", "", "override storage root")
		caller    = fs.String("caller", "", "override caller identity")
		format    = fs.String("format", "json", "output format: json|id")
		goalSyms  stringSlice
		srcSyms   stringSlice
		nails     stringSlice
	)
	fs.Var(&goalSyms, "goal-sym", "goal symbol in SLP notation (repeatable; required ≥1)")
	fs.Var(&srcSyms, "source-sym", "source symbol in SLP notation (repeatable; may be empty)")
	fs.Var(&nails, "nail", "nail_chain entry (repeatable)")
	pos, flagArgs := splitArgs(rc.args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(pos) < 1 {
		fmt.Fprintln(rc.stderr, "Error: task_create requires <key>")
		return 2
	}
	key := pos[0]
	if *intent == "" {
		fmt.Fprintln(rc.stderr, "Error: --intent is required")
		return 2
	}
	if *passKind == "" {
		fmt.Fprintln(rc.stderr, "Error: --pass-kind is required")
		return 2
	}
	if *passReas == "" {
		fmt.Fprintln(rc.stderr, "Error: --pass-reason is required")
		return 2
	}
	goal, code := parseTaskSymbols(rc.stderr, goalSyms.values)
	if code != 0 {
		return code
	}
	source, code := parseTaskSymbols(rc.stderr, srcSyms.values)
	if code != 0 {
		return code
	}
	var query box.SymbolQuery
	if *passQuery != "" {
		if err := json.Unmarshal([]byte(*passQuery), &query); err != nil {
			fmt.Fprintf(rc.stderr, "Error: --pass-query JSON: %s\n", err.Error())
			return 2
		}
	}
	svc, code := rc.openService(*root)
	if code != 0 {
		return code
	}
	ctx := context.Background()
	callerID := rc.resolveCallerExplicit(*caller)
	b, err := resolveBoxByKey(ctx, svc, callerID, key)
	if err != nil {
		return mapErr(err, rc.stderr)
	}
	if callerID == "" {
		callerID = b.OwnerID
	}
	if callerID == "" {
		fmt.Fprintln(rc.stderr, "Error: cannot resolve caller (set --caller or BOX_CALLER)")
		return 2
	}
	item, err := svc.CreateTask(ctx, callerID, b.ID, box.CreateTaskRequest{
		Intent: *intent,
		Source: source,
		Goal:   goal,
		PassCriteria: box.PassCriteria{
			Kind:   *passKind,
			Query:  query,
			Arg:    *passArg,
			Reason: *passReas,
		},
		NailChain: nails.values,
	})
	if err != nil {
		return mapErr(err, rc.stderr)
	}
	if err := writeItem(rc.stdout, item, *format); err != nil {
		fmt.Fprintf(rc.stderr, "Error: %s\n", err.Error())
		return 1
	}
	return 0
}

// parseTaskSymbols parses a slice of SLP-notation strings (one per repeated
// flag) into a flat []box.Symbol. Empty input returns (nil, 0). Each entry
// is parsed by ParseNotation so the full Symbol grammar is available
// (e.g. "R ✓ *", "T → ⊃ item_abc").
func parseTaskSymbols(stderr io.Writer, raws []string) ([]box.Symbol, int) {
	if len(raws) == 0 {
		return nil, 0
	}
	var out []box.Symbol
	for _, raw := range raws {
		syms, err := ParseNotation(raw)
		if err != nil {
			fmt.Fprintf(stderr, "Error: --goal/source-sym %q: %s\n", raw, err.Error())
			return nil, 2
		}
		out = append(out, syms...)
	}
	return out, 0
}

// cmdTaskStatus flips a task's status symbol in place (does NOT open a new
// revision). Internally it calls SetItemSymbols with [T, <status>].
func (rc *rootContext) cmdTaskStatus() int {
	fs := flag.NewFlagSet("task_status", flag.ContinueOnError)
	fs.SetOutput(rc.stderr)
	var (
		status       = fs.String("status", "", "new status: open|wip|done|rejected|blocked|archived (required)")
		allowHistory = fs.Bool("allow-history", false, "allow patching a non-latest revision")
		root         = fs.String("root", "", "override storage root")
		caller       = fs.String("caller", "", "override caller identity")
		format       = fs.String("format", "json", "output format: json|id")
	)
	pos, flagArgs := splitArgs(rc.args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(pos) < 1 {
		fmt.Fprintln(rc.stderr, "Error: task_status requires <task_id>")
		return 2
	}
	taskID := pos[0]
	if *status == "" {
		fmt.Fprintln(rc.stderr, "Error: --status is required")
		return 2
	}
	v, ok := normalizeStatus(*status)
	if !ok {
		fmt.Fprintf(rc.stderr, "Error: unknown --status %q\n", *status)
		return 2
	}
	svc, code := rc.openService(*root)
	if code != 0 {
		return code
	}
	ctx := context.Background()
	callerID := rc.resolveCallerExplicit(*caller)
	if callerID == "" {
		item, err := svc.GetItem(ctx, "", taskID)
		if err != nil {
			return mapErr(err, rc.stderr)
		}
		b, err := svc.GetBox(ctx, "", item.BoxID)
		if err != nil {
			return mapErr(err, rc.stderr)
		}
		callerID = b.OwnerID
	}
	if callerID == "" {
		fmt.Fprintln(rc.stderr, "Error: cannot resolve caller (set --caller or BOX_CALLER)")
		return 2
	}
	syms := []box.Symbol{
		{Kind: box.SymKind, Value: "T"},
		{Kind: box.SymStatus, Value: v},
	}
	var opts []box.UpdateLabelsOption
	if *allowHistory {
		opts = append(opts, box.WithAllowHistory(true))
	}
	item, err := svc.SetItemSymbols(ctx, callerID, taskID, syms, opts...)
	if err != nil {
		return mapErr(err, rc.stderr)
	}
	if err := writeItem(rc.stdout, item, *format); err != nil {
		fmt.Fprintf(rc.stderr, "Error: %s\n", err.Error())
		return 1
	}
	return 0
}

// cmdTaskTrace appends one TraceStep (caller-supplied JSON) to the task's
// trace.jsonl. step.Step is overwritten by the store (= current length); the
// caller's value is ignored.
func (rc *rootContext) cmdTaskTrace() int {
	fs := flag.NewFlagSet("task_trace", flag.ContinueOnError)
	fs.SetOutput(rc.stderr)
	var (
		stepJSON = fs.String("step", "", "TraceStep JSON (e.g. {\"op\":\"x\",\"nail_ref\":\"y\"}) (required)")
		root     = fs.String("root", "", "override storage root")
		caller   = fs.String("caller", "", "override caller identity")
	)
	pos, flagArgs := splitArgs(rc.args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(pos) < 1 {
		fmt.Fprintln(rc.stderr, "Error: task_trace requires <task_id>")
		return 2
	}
	taskID := pos[0]
	if *stepJSON == "" {
		fmt.Fprintln(rc.stderr, "Error: --step is required")
		return 2
	}
	var step box.TraceStep
	if err := json.Unmarshal([]byte(*stepJSON), &step); err != nil {
		fmt.Fprintf(rc.stderr, "Error: --step JSON: %s\n", err.Error())
		return 2
	}
	svc, code := rc.openService(*root)
	if code != 0 {
		return code
	}
	ctx := context.Background()
	callerID := rc.resolveCallerExplicit(*caller)
	if callerID == "" {
		item, err := svc.GetItem(ctx, "", taskID)
		if err != nil {
			return mapErr(err, rc.stderr)
		}
		b, err := svc.GetBox(ctx, "", item.BoxID)
		if err != nil {
			return mapErr(err, rc.stderr)
		}
		callerID = b.OwnerID
	}
	if callerID == "" {
		fmt.Fprintln(rc.stderr, "Error: cannot resolve caller (set --caller or BOX_CALLER)")
		return 2
	}
	if err := svc.AppendTaskTrace(ctx, callerID, taskID, step); err != nil {
		return mapErr(err, rc.stderr)
	}
	// Print the final list — the test surface and CLI ergonomics agree.
	trace, err := svc.ListTaskTrace(ctx, callerID, taskID)
	if err != nil {
		return mapErr(err, rc.stderr)
	}
	if err := writeJSON(rc.stdout, trace); err != nil {
		fmt.Fprintf(rc.stderr, "Error: %s\n", err.Error())
		return 1
	}
	return 0
}

// cmdTaskListTrace prints the task's full trace history as a JSON array.
func (rc *rootContext) cmdTaskListTrace() int {
	fs := flag.NewFlagSet("task_list_trace", flag.ContinueOnError)
	fs.SetOutput(rc.stderr)
	var (
		root   = fs.String("root", "", "override storage root")
		caller = fs.String("caller", "", "override caller identity")
	)
	_ = caller
	pos, flagArgs := splitArgs(rc.args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(pos) < 1 {
		fmt.Fprintln(rc.stderr, "Error: task_list_trace requires <task_id>")
		return 2
	}
	taskID := pos[0]
	svc, code := rc.openService(*root)
	if code != 0 {
		return code
	}
	ctx := context.Background()
	trace, err := svc.ListTaskTrace(ctx, rc.resolveCallerExplicit(*caller), taskID)
	if err != nil {
		return mapErr(err, rc.stderr)
	}
	if err := writeJSON(rc.stdout, trace); err != nil {
		fmt.Fprintf(rc.stderr, "Error: %s\n", err.Error())
		return 1
	}
	return 0
}

// cmdTaskShow is a thin wrapper over GetItem that additionally enforces
// kind=="task" so the user gets an early ErrValidation instead of confusing
// downstream output.
func (rc *rootContext) cmdTaskShow() int {
	fs := flag.NewFlagSet("task_show", flag.ContinueOnError)
	fs.SetOutput(rc.stderr)
	var (
		root   = fs.String("root", "", "override storage root")
		caller = fs.String("caller", "", "override caller identity")
		format = fs.String("format", "json", "output format: json|id")
	)
	_ = caller
	pos, flagArgs := splitArgs(rc.args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(pos) < 1 {
		fmt.Fprintln(rc.stderr, "Error: task_show requires <task_id>")
		return 2
	}
	taskID := pos[0]
	svc, code := rc.openService(*root)
	if code != 0 {
		return code
	}
	ctx := context.Background()
	item, err := svc.GetItem(ctx, "", taskID)
	if err != nil {
		return mapErr(err, rc.stderr)
	}
	if item.Kind != "task" {
		fmt.Fprintf(rc.stderr, "Error: %s: item %s is kind=%q, not task\n", box.ErrValidation.Error(), taskID, item.Kind)
		return 2
	}
	if err := writeItem(rc.stdout, item, *format); err != nil {
		fmt.Fprintf(rc.stderr, "Error: %s\n", err.Error())
		return 1
	}
	return 0
}

// Stdlib's time.ParseDuration only understands h/m/s/ms/us/ns, so "Nd" is
// expanded to "N*24h" by parsing the numeric portion as an integer multiplier
// of 24 hours.
func parseSinceDuration(in string) (time.Duration, error) {
	if in == "" {
		return 0, nil
	}
	if strings.HasSuffix(in, "d") {
		num := strings.TrimSuffix(in, "d")
		if num == "" {
			return 0, fmt.Errorf("invalid duration %q", in)
		}
		hours, err := time.ParseDuration(num + "h")
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q: %w", in, err)
		}
		return hours * 24, nil
	}
	return time.ParseDuration(in)
}
