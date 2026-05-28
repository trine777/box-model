// Command box-mcp exposes Box Model's Service façade as an MCP (Model Context
// Protocol) server over stdio so LLM clients (Claude Desktop, mcp-cli, etc.)
// can speak directly to a local Box repository.
//
// Architecture invariants:
//   - The MCP SDK (github.com/modelcontextprotocol/go-sdk) is imported ONLY
//     from this command. Box core (box/, box/cli/, box/view/, box/obs/) stays
//     stdlib-only.
//   - Every tool handler routes through box.Service. The raw Store is never
//     touched here, mirroring CLI D#10.
//   - Human-facing tools (view, rotate) are NOT exposed — they violate the
//     LLM-friendly invariant of returning structured JSON only.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/windborneos/box-model/box"
	"github.com/windborneos/box-model/box/obs"
)

func main() {
	cfg, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(2)
	}
	if err := run(context.Background(), cfg, os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

// config holds the parsed command-line / env state for one box-mcp invocation.
type config struct {
	// boxHome overrides the storage root (otherwise BOX_HOME env, else ~/.box).
	boxHome string
	// owner is the default caller_id when a request omits it. Empty falls back
	// to BOX_CALLER env, then to the resolved box owner per handler.
	owner string
	// disableObs disables the obs.MemObserver wiring (mainly for tests).
	disableObs bool
	// httpAddr, when non-empty (or $PORT set), serves Streamable-HTTP MCP at
	// the given address instead of stdio. BOX_API_TOKEN is then required for
	// Bearer auth — the server refuses to start unauthenticated.
	httpAddr string
}

func parseFlags(args []string) (config, error) {
	fs := flag.NewFlagSet("box-mcp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var cfg config
	fs.StringVar(&cfg.boxHome, "box-home", "", "override storage root (else $BOX_HOME, else ~/.box)")
	fs.StringVar(&cfg.owner, "owner", "", "default caller identity for tool calls")
	fs.BoolVar(&cfg.disableObs, "no-obs", false, "disable observability wiring")
	fs.StringVar(&cfg.httpAddr, "http", "", "serve over Streamable-HTTP at this addr (e.g. :8080); else stdio. $PORT env also taken.")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if cfg.httpAddr == "" {
		if port := os.Getenv("PORT"); port != "" {
			cfg.httpAddr = ":" + port
		}
	}
	return cfg, nil
}

// run wires up the Service, builds the MCP server with 28 tools (17 base +
// 5 R0.10 task tools + 4 R0.13.1 program-track tools + 2 self-describing
// tools: box_manual / box_legend_all) and serves either over stdio (default)
// or Streamable-HTTP (when --http or $PORT is set). Split out from main for
// testability.
func run(ctx context.Context, cfg config, stdin io.Reader, stdout, stderr io.Writer) error {
	svc, _, err := buildService(ctx, cfg, stderr)
	if err != nil {
		return err
	}
	srv := buildServer(svc, cfg)
	if cfg.httpAddr != "" {
		caller := cfg.owner
		if caller == "" {
			caller = os.Getenv("BOX_CALLER")
		}
		return runHTTP(ctx, cfg, srv, svc, caller, stderr)
	}
	transport := &mcp.IOTransport{
		Reader: io.NopCloser(stdin),
		Writer: nopCloseWriter{stdout},
	}
	return srv.Run(ctx, transport)
}

// nopCloseWriter adapts an io.Writer to io.WriteCloser by implementing a
// no-op Close. Mirrors the SDK's internal helper so we can run over arbitrary
// (test-injected) writers.
type nopCloseWriter struct{ io.Writer }

func (nopCloseWriter) Close() error { return nil }

// buildService opens the FileStore, builds the obs.MemObserver (unless
// disabled), constructs the Service and runs EnsureSymbolBootstrap. Returns
// the Service and the observer so callers can plug in alternative test
// transports.
func buildService(ctx context.Context, cfg config, stderr io.Writer) (*box.Service, obs.Observer, error) {
	root, err := resolveRoot(cfg.boxHome)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve root: %w", err)
	}
	st, err := box.OpenFileStore(root)
	if err != nil {
		return nil, nil, fmt.Errorf("open store: %w", err)
	}
	var observer obs.Observer = obs.NoopObserver{}
	if !cfg.disableObs {
		observer = obs.NewMemObserver(stderr, slog.LevelInfo)
		st.SetObserver(observer)
	}
	svc := box.NewService(st, box.WithObserver(observer))
	if err := svc.EnsureSymbolBootstrap(ctx); err != nil {
		return nil, nil, fmt.Errorf("symbol bootstrap: %w", err)
	}
	return svc, observer, nil
}

// resolveRoot mirrors box/cli's rootResolution: --box-home > $BOX_HOME > ~/.box.
func resolveRoot(boxHome string) (string, error) {
	if boxHome != "" {
		return boxHome, nil
	}
	if v := os.Getenv("BOX_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".box"), nil
}

// handlers is the live state shared by every tool handler — the Service plus
// the resolved default caller id (computed once at server start).
type handlers struct {
	svc     *box.Service
	caller  string // may be ""
	boxHome string // resolved storage root (for blob GC and download routes)
}

// buildServer constructs an MCP server with every exposed Box tool registered.
//
// Tool naming convention: "box_<verb>". The 26 tools registered here mirror
// the Service surface 1:1: the original 17 (CreateBox / GetBoxByKey /
// SealBox / Summary / Store / ReplaceItem / UpdateLabels / MergeLabels /
// RemoveLabels / DeleteItem / Consume / GetItem / Browse / ListConsumes /
// Trace / LegendOf / Neighbors), the R0.10 task surface (CreateTask /
// SetTaskStatus / AppendTaskTrace / ListTaskTrace / GetTask) and the
// R0.13.1 program-track surface (TaskStart / TaskFinish / TaskAbort /
// TaskTokenStatus).
//
// Human-facing surfaces (view / rotate / legend_all / list_boxes) are not
// exposed.
func buildServer(svc *box.Service, cfg config) *mcp.Server {
	caller := cfg.owner
	if caller == "" {
		caller = os.Getenv("BOX_CALLER")
	}
	root, _ := resolveRoot(cfg.boxHome)
	h := &handlers{svc: svc, caller: caller, boxHome: root}
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "box-mcp",
		Version: "0.10.0",
	}, nil)
	registerTools(srv, h)
	return srv
}

// schemaWithAnyContent derives the input schema for T via reflection, then
// rewrites the "content" property from the default Go-` + "`interface{}`" + `
// rendering (which marshals to bare `true`, the JSON-Schema sentinel for
// "anything") into an empty schema object (`{}`). Both forms are
// semantically equivalent — "any JSON value" — but Claude Code's Zod-based
// validator rejects the boolean-as-schema form and accepts the empty-object
// form. Used only for box_store / box_replace_item.
func schemaWithAnyContent[T any]() *jsonschema.Schema {
	s, err := jsonschema.ForType(reflect.TypeFor[T](), &jsonschema.ForOptions{})
	if err != nil {
		panic(fmt.Sprintf("schema gen: %v", err))
	}
	if s.Properties != nil {
		if _, ok := s.Properties["content"]; ok {
			// A bare `*Schema{}` marshals as `true` (the schema-go library
			// treats empty as the JSON-Schema boolean-true sentinel). We need
			// a non-empty object so Zod accepts it, so we plant a Description.
			s.Properties["content"] = &jsonschema.Schema{
				Description: "arbitrary JSON content (object, array, scalar, or null)",
			}
		}
	}
	return s
}

// registerTools registers every box_* tool on srv. The split keeps buildServer
// readable and the grep-friendly tool list co-located.
func registerTools(srv *mcp.Server, h *handlers) {
	mcp.AddTool(srv, &mcp.Tool{Name: "box_create_box", Description: "Create a new box."}, h.handleCreateBox)
	mcp.AddTool(srv, &mcp.Tool{Name: "box_get_box_by_key", Description: "Resolve a box by its key."}, h.handleGetBoxByKey)
	mcp.AddTool(srv, &mcp.Tool{Name: "box_seal_box", Description: "Seal a box (no further writes)."}, h.handleSealBox)
	mcp.AddTool(srv, &mcp.Tool{Name: "box_summary", Description: "Summarize a box."}, h.handleSummary)
	mcp.AddTool(srv, &mcp.Tool{Name: "box_store", Description: "Store an item into a box.", InputSchema: schemaWithAnyContent[storeInput]()}, h.handleStore)
	mcp.AddTool(srv, &mcp.Tool{Name: "box_replace_item", Description: "Open a new revision of an item.", InputSchema: schemaWithAnyContent[replaceItemInput]()}, h.handleReplaceItem)
	mcp.AddTool(srv, &mcp.Tool{Name: "box_update_labels", Description: "Replace labels on an item."}, h.handleUpdateLabels)
	mcp.AddTool(srv, &mcp.Tool{Name: "box_merge_labels", Description: "Merge a patch into an item's labels."}, h.handleMergeLabels)
	mcp.AddTool(srv, &mcp.Tool{Name: "box_remove_labels", Description: "Remove keys from an item's labels."}, h.handleRemoveLabels)
	mcp.AddTool(srv, &mcp.Tool{Name: "box_delete_item", Description: "Soft-delete an item."}, h.handleDeleteItem)
	mcp.AddTool(srv, &mcp.Tool{Name: "box_consume", Description: "Record a consume audit on an item."}, h.handleConsume)
	mcp.AddTool(srv, &mcp.Tool{Name: "box_show", Description: "Fetch an item by id (pure read)."}, h.handleShow)
	mcp.AddTool(srv, &mcp.Tool{Name: "box_browse", Description: "List items in a box."}, h.handleBrowse)
	mcp.AddTool(srv, &mcp.Tool{Name: "box_list_consumes", Description: "List the consume audit log for an item."}, h.handleListConsumes)
	mcp.AddTool(srv, &mcp.Tool{Name: "box_trace", Description: "Query items by Symbol dimensions."}, h.handleTrace)
	mcp.AddTool(srv, &mcp.Tool{Name: "box_legend", Description: "Show the documentation entry for a Symbol literal."}, h.handleLegend)
	mcp.AddTool(srv, &mcp.Tool{Name: "box_neighbors", Description: "Print the hop-bounded relation subgraph."}, h.handleNeighbors)
	// R0.10 task surface — Box is dumb storage; agent runs PassCriteria.Query.
	mcp.AddTool(srv, &mcp.Tool{Name: "box_set_item_symbols", Description: "Replace an item's symbol set (works on any kind; R0.13.2 supersedes box_set_task_status)."}, h.handleSetItemSymbols)
	mcp.AddTool(srv, &mcp.Tool{Name: "box_append_event", Description: "Append one TraceStep to any item's event log (R0.13.2: no kind=task gate)."}, h.handleAppendEvent)
	mcp.AddTool(srv, &mcp.Tool{Name: "box_list_events", Description: "List the item's full event history (R0.13.2: works on any item)."}, h.handleListEvents)
	// R0.13.1 程辙层 (program-track layer) — Box stores; agent decides.
	mcp.AddTool(srv, &mcp.Tool{Name: "box_task_start", Description: "Open a YiCheng (program-track) session; returns the task plus a session token."}, h.handleTaskStart)
	mcp.AddTool(srv, &mcp.Tool{Name: "box_task_finish", Description: "Close a YiCheng session with a ✓ task_finish event."}, h.handleTaskFinish)
	mcp.AddTool(srv, &mcp.Tool{Name: "box_task_abort", Description: "Close a YiCheng session with a ✗ task_abort event (no rollback)."}, h.handleTaskAbort)
	mcp.AddTool(srv, &mcp.Tool{Name: "box_task_token_status", Description: "Pure read: report whether a session token is still live."}, h.handleTaskTokenStatus)
	// R0.19 blob consistency — orphan sweep + missing-ref alert.
	mcp.AddTool(srv, &mcp.Tool{Name: "box_gc_blobs", Description: "Scan items vs disk blobs; report orphans + missing refs; dry_run defaults true."}, h.handleGCBlobs)
	// R0.22 observability snapshot — exposes the in-memory counters/timers.
	mcp.AddTool(srv, &mcp.Tool{Name: "box_observability", Description: "Snapshot of in-memory counters + timers (compact stats). Optional name_prefix filter."}, h.handleObservability)
	// R4.1 self-describing tools — for fresh agents discovering box-mcp.
	mcp.AddTool(srv, &mcp.Tool{Name: "box_manual", Description: "Return the box-mcp traffic manual (markdown): symbols, 程辙 flow, all tools and example calls."}, h.handleManual)
	mcp.AddTool(srv, &mcp.Tool{Name: "box_legend_all", Description: "Return all 25 native symbol legend entries (kind/status/relation/priority) in one call."}, h.handleLegendAll)
	// R5.1 cross-box geo-globe view.
	mcp.AddTool(srv, &mcp.Tool{Name: "box_overview", Description: "Overview of all boxes (geo-globe model: axis × zoom × filter)."}, h.handleOverview)
}

// ----- input schemas ------------------------------------------------------

// Each tool gets a dedicated struct so the SDK can derive a JSON schema. The
// field names mirror the Service surface and the architect-mandated naming
// table; tags use jsonschema descriptions for client UI.

type createBoxInput struct {
	Key       string             `json:"key" jsonschema:"the public key of the box (required)"`
	OwnerType string             `json:"owner_type,omitempty"`
	OwnerID   string             `json:"owner_id,omitempty"`
	Policy    *box.StoragePolicy `json:"storage_policy,omitempty"`
	Labels    map[string]string  `json:"labels,omitempty"`
}

type getBoxByKeyInput struct {
	Key string `json:"key" jsonschema:"the public key of the box (required)"`
}

type boxIDInput struct {
	BoxID string `json:"box_id" jsonschema:"the box id (required)"`
}

type storeInput struct {
	BoxID      string            `json:"box_id" jsonschema:"target box id (required)"`
	Kind       string            `json:"kind" jsonschema:"item kind (required)"`
	SourceType string            `json:"source_type" jsonschema:"source type (required)"`
	StorageURI string            `json:"storage_uri" jsonschema:"storage uri (required)"`
	IdemKey    string            `json:"idem_key,omitempty"`
	SourceRef  map[string]string `json:"source_ref,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	LocationID string            `json:"location_id,omitempty"`
	Format     string            `json:"format,omitempty"`
	// Content is any JSON value; we capture it as RawMessage at unmarshal time
	// (the field is typed `any` so the inferred schema accepts arbitrary
	// JSON, not the byte-array shape json.RawMessage would otherwise produce).
	Content  any               `json:"content,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Symbols  []box.Symbol      `json:"symbols,omitempty"`
}

type replaceItemInput struct {
	PrevItemID string            `json:"prev_item_id" jsonschema:"id of the item to replace (required)"`
	Kind       string            `json:"kind,omitempty"`
	SourceType string            `json:"source_type,omitempty"`
	StorageURI string            `json:"storage_uri,omitempty"`
	IdemKey    string            `json:"idem_key,omitempty"`
	SourceRef  map[string]string `json:"source_ref,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	LocationID string            `json:"location_id,omitempty"`
	Format     string            `json:"format,omitempty"`
	Content    any               `json:"content,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	Symbols    []box.Symbol      `json:"symbols,omitempty"`
}

// contentToRaw re-marshals an `any` Content value into a json.RawMessage so it
// can flow into box.StoreRequest. Returns nil for a nil content (Service
// defaults to JSON null).
func contentToRaw(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("%w: content marshal: %v", box.ErrValidation, err)
	}
	return b, nil
}

type updateLabelsInput struct {
	ItemID       string            `json:"item_id" jsonschema:"item id (required)"`
	Labels       map[string]string `json:"labels" jsonschema:"full label replacement (required)"`
	AllowHistory bool              `json:"allow_history,omitempty"`
}

type mergeLabelsInput struct {
	ItemID       string            `json:"item_id" jsonschema:"item id (required)"`
	Patch        map[string]string `json:"patch" jsonschema:"label patch (required)"`
	AllowHistory bool              `json:"allow_history,omitempty"`
}

type removeLabelsInput struct {
	ItemID       string   `json:"item_id" jsonschema:"item id (required)"`
	Keys         []string `json:"keys" jsonschema:"label keys to remove (required)"`
	AllowHistory bool     `json:"allow_history,omitempty"`
}

type itemIDInput struct {
	ItemID string `json:"item_id" jsonschema:"item id (required)"`
}

type consumeInput struct {
	ItemID       string `json:"item_id" jsonschema:"item id (required)"`
	MarkConsumed *bool  `json:"mark_consumed,omitempty"`
	Purpose      string `json:"purpose,omitempty"`
	ConsumerType string `json:"consumer_type,omitempty"`
}

type browseInput struct {
	BoxID          string            `json:"box_id" jsonschema:"target box id (required)"`
	Kind           string            `json:"kind,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
	SourceRef      map[string]string `json:"source_ref,omitempty"`
	LocationIDs    []string          `json:"location_ids,omitempty"`
	Limit          int               `json:"limit,omitempty"`
	Offset         int               `json:"offset,omitempty"`
	IncludeHistory bool              `json:"include_history,omitempty"`
	OnlyHistory    bool              `json:"only_history,omitempty"`
}

type traceInput struct {
	BoxKey string             `json:"box_key,omitempty"`
	Kind   []box.SymbolKind   `json:"kind,omitempty"`
	Value  []string           `json:"value,omitempty"`
	Ref    string             `json:"ref,omitempty"`
}

type legendInput struct {
	Kind  box.SymbolKind `json:"kind" jsonschema:"symbol kind: kind|status|relation|scope|topic|priority|domain (required)"`
	Value string         `json:"value" jsonschema:"symbol literal value (required)"`
	Ref   string         `json:"ref,omitempty"`
}

type neighborsInput struct {
	ItemID string `json:"item_id" jsonschema:"item id (required)"`
	Hops   int    `json:"hops,omitempty" jsonschema:"BFS hop limit; defaults to 1; range [1,5]"`
}

// overviewInput is the R5.1 box_overview MCP input schema.
//
//	axis   — "owner" | "status" | "label:<key>"        (required)
//	zoom   — 0 (histogram) or 1 (one BoxGlyph per box) (default 0)
//	filter — orthogonal owner/status/labels limiter    (optional)
type overviewInput struct {
	Axis   string         `json:"axis" jsonschema:"axis: owner|status|label:<key> (required)"`
	Zoom   int            `json:"zoom,omitempty" jsonschema:"granularity; zero=histogram, one=one glyph per box (default zero)"`
	Filter *box.BoxFilter `json:"filter,omitempty"`
}

// ----- handlers ----------------------------------------------------------
//
// Each handler:
//   1. Resolves caller_id via resolveCaller (defaultCaller / per-item-or-box
//      owner fallback).
//   2. Calls the matching Service method.
//   3. Returns the result as an `any` (the SDK marshals it to
//      StructuredContent + TextContent automatically).
//
// All handlers use Out=any so the SDK's auto-derived output schema validation
// is skipped — box.Item / box.Box etc. have non-omitempty maps that marshal
// to JSON null on a zero value, which would otherwise fail the inferred
// "type:object" check. Input schemas are still validated against the typed
// Input struct, which is what we want.
//
// Errors are returned as-is from box.Service; the SDK wraps them into an
// IsError=true CallToolResult whose text content preserves the
// "validation:" / "not found:" / "conflict:" / "forbidden:" sentinel prefix
// so clients can parse intent.

// resolveCaller picks the caller id for one tool call. Resolution order:
//   - the cfg-supplied default (--owner / $BOX_CALLER) if non-empty
//   - the owner of the box identified by itemID > boxID > boxKey
//
// Mirrors the CLI's behaviour so a single-tenant install can omit caller_id
// from every request. Returns ErrValidation if no fallback can be derived.
func (h *handlers) resolveCaller(ctx context.Context, itemID, boxID, boxKey string) (string, error) {
	if h.caller != "" {
		return h.caller, nil
	}
	if itemID != "" {
		item, err := h.svc.GetItem(ctx, "", itemID)
		if err != nil {
			return "", err
		}
		b, err := h.svc.GetBox(ctx, "", item.BoxID)
		if err != nil {
			return "", err
		}
		return b.OwnerID, nil
	}
	if boxID != "" {
		b, err := h.svc.GetBox(ctx, "", boxID)
		if err != nil {
			return "", err
		}
		return b.OwnerID, nil
	}
	if boxKey != "" {
		b, err := h.svc.GetBoxByKey(ctx, "", boxKey)
		if err != nil {
			return "", err
		}
		return b.OwnerID, nil
	}
	return "", fmt.Errorf("%w: cannot resolve caller (set --owner or BOX_CALLER, or include box/item context)", box.ErrValidation)
}

func (h *handlers) handleCreateBox(ctx context.Context, _ *mcp.CallToolRequest, in createBoxInput) (*mcp.CallToolResult, any, error) {
	req := box.CreateBoxRequest{
		Key:       in.Key,
		OwnerType: in.OwnerType,
		OwnerID:   in.OwnerID,
		Labels:    in.Labels,
	}
	if in.Policy != nil {
		req.StoragePolicy = *in.Policy
	}
	if req.OwnerID == "" {
		req.OwnerID = h.caller
	}
	b, err := h.svc.CreateBox(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	return nil, b, nil
}

func (h *handlers) handleGetBoxByKey(ctx context.Context, _ *mcp.CallToolRequest, in getBoxByKeyInput) (*mcp.CallToolResult, any, error) {
	b, err := h.svc.GetBoxByKey(ctx, h.caller, in.Key)
	if err != nil {
		return nil, nil, err
	}
	return nil, b, nil
}

func (h *handlers) handleSealBox(ctx context.Context, _ *mcp.CallToolRequest, in boxIDInput) (*mcp.CallToolResult, any, error) {
	caller, err := h.resolveCaller(ctx, "", in.BoxID, "")
	if err != nil {
		return nil, nil, err
	}
	if err := h.svc.SealBox(ctx, caller, in.BoxID); err != nil {
		return nil, nil, err
	}
	b, err := h.svc.GetBox(ctx, caller, in.BoxID)
	if err != nil {
		return nil, nil, err
	}
	return nil, b, nil
}

func (h *handlers) handleSummary(ctx context.Context, _ *mcp.CallToolRequest, in boxIDInput) (*mcp.CallToolResult, any, error) {
	s, err := h.svc.Summary(ctx, in.BoxID)
	if err != nil {
		return nil, nil, err
	}
	return nil, s, nil
}

func (h *handlers) handleStore(ctx context.Context, _ *mcp.CallToolRequest, in storeInput) (*mcp.CallToolResult, any, error) {
	caller, err := h.resolveCaller(ctx, "", in.BoxID, "")
	if err != nil {
		return nil, nil, err
	}
	content, err := contentToRaw(in.Content)
	if err != nil {
		return nil, nil, err
	}
	item, err := h.svc.Store(ctx, caller, in.BoxID, box.StoreRequest{
		IdemKey:    in.IdemKey,
		Kind:       in.Kind,
		SourceType: in.SourceType,
		SourceRef:  in.SourceRef,
		Labels:     in.Labels,
		LocationID: in.LocationID,
		StorageURI: in.StorageURI,
		Format:     in.Format,
		Content:    content,
		Metadata:   in.Metadata,
		StoredBy:   caller,
		Symbols:    in.Symbols,
	})
	if err != nil {
		return nil, nil, err
	}
	return nil, item, nil
}

func (h *handlers) handleReplaceItem(ctx context.Context, _ *mcp.CallToolRequest, in replaceItemInput) (*mcp.CallToolResult, any, error) {
	caller, err := h.resolveCaller(ctx, in.PrevItemID, "", "")
	if err != nil {
		return nil, nil, err
	}
	content, err := contentToRaw(in.Content)
	if err != nil {
		return nil, nil, err
	}
	item, err := h.svc.ReplaceItem(ctx, caller, in.PrevItemID, box.StoreRequest{
		IdemKey:    in.IdemKey,
		Kind:       in.Kind,
		SourceType: in.SourceType,
		SourceRef:  in.SourceRef,
		Labels:     in.Labels,
		LocationID: in.LocationID,
		StorageURI: in.StorageURI,
		Format:     in.Format,
		Content:    content,
		Metadata:   in.Metadata,
		StoredBy:   caller,
		Symbols:    in.Symbols,
	})
	if err != nil {
		return nil, nil, err
	}
	return nil, item, nil
}

func (h *handlers) handleUpdateLabels(ctx context.Context, _ *mcp.CallToolRequest, in updateLabelsInput) (*mcp.CallToolResult, any, error) {
	caller, err := h.resolveCaller(ctx, in.ItemID, "", "")
	if err != nil {
		return nil, nil, err
	}
	var opts []box.UpdateLabelsOption
	if in.AllowHistory {
		opts = append(opts, box.WithAllowHistory(true))
	}
	item, err := h.svc.UpdateLabels(ctx, caller, in.ItemID, in.Labels, opts...)
	if err != nil {
		return nil, nil, err
	}
	return nil, item, nil
}

func (h *handlers) handleMergeLabels(ctx context.Context, _ *mcp.CallToolRequest, in mergeLabelsInput) (*mcp.CallToolResult, any, error) {
	caller, err := h.resolveCaller(ctx, in.ItemID, "", "")
	if err != nil {
		return nil, nil, err
	}
	var opts []box.UpdateLabelsOption
	if in.AllowHistory {
		opts = append(opts, box.WithAllowHistory(true))
	}
	item, err := h.svc.MergeLabels(ctx, caller, in.ItemID, in.Patch, opts...)
	if err != nil {
		return nil, nil, err
	}
	return nil, item, nil
}

func (h *handlers) handleRemoveLabels(ctx context.Context, _ *mcp.CallToolRequest, in removeLabelsInput) (*mcp.CallToolResult, any, error) {
	caller, err := h.resolveCaller(ctx, in.ItemID, "", "")
	if err != nil {
		return nil, nil, err
	}
	var opts []box.UpdateLabelsOption
	if in.AllowHistory {
		opts = append(opts, box.WithAllowHistory(true))
	}
	item, err := h.svc.RemoveLabels(ctx, caller, in.ItemID, in.Keys, opts...)
	if err != nil {
		return nil, nil, err
	}
	return nil, item, nil
}

func (h *handlers) handleDeleteItem(ctx context.Context, _ *mcp.CallToolRequest, in itemIDInput) (*mcp.CallToolResult, any, error) {
	caller, err := h.resolveCaller(ctx, in.ItemID, "", "")
	if err != nil {
		return nil, nil, err
	}
	item, err := h.svc.DeleteItem(ctx, caller, in.ItemID)
	if err != nil {
		return nil, nil, err
	}
	return nil, item, nil
}

func (h *handlers) handleConsume(ctx context.Context, _ *mcp.CallToolRequest, in consumeInput) (*mcp.CallToolResult, any, error) {
	caller, err := h.resolveCaller(ctx, in.ItemID, "", "")
	if err != nil {
		return nil, nil, err
	}
	// mark_consumed defaults to true (per spec) when the field is absent.
	mark := true
	if in.MarkConsumed != nil {
		mark = *in.MarkConsumed
	}
	consumer := in.ConsumerType
	if consumer == "" {
		consumer = "agent"
	}
	item, err := h.svc.Consume(ctx, caller, in.ItemID, box.ConsumeOptions{
		Purpose:      in.Purpose,
		MarkConsumed: mark,
		ConsumerType: consumer,
	})
	if err != nil {
		return nil, nil, err
	}
	return nil, item, nil
}

func (h *handlers) handleShow(ctx context.Context, _ *mcp.CallToolRequest, in itemIDInput) (*mcp.CallToolResult, any, error) {
	item, err := h.svc.GetItem(ctx, h.caller, in.ItemID)
	if err != nil {
		return nil, nil, err
	}
	return nil, item, nil
}

// browseOutput wraps the slice so the structured payload is a JSON object.
type browseOutput struct {
	Items []box.Item `json:"items"`
}

func (h *handlers) handleBrowse(ctx context.Context, _ *mcp.CallToolRequest, in browseInput) (*mcp.CallToolResult, any, error) {
	items, err := h.svc.Browse(ctx, in.BoxID, box.BrowseFilter{
		Kind:           in.Kind,
		SourceRef:      in.SourceRef,
		Labels:         in.Labels,
		LocationIDs:    in.LocationIDs,
		Limit:          in.Limit,
		Offset:         in.Offset,
		IncludeHistory: in.IncludeHistory,
		OnlyHistory:    in.OnlyHistory,
	})
	if err != nil {
		return nil, nil, err
	}
	return nil, browseOutput{Items: items}, nil
}

type listConsumesOutput struct {
	Logs []box.ConsumeLog `json:"logs"`
}

func (h *handlers) handleListConsumes(ctx context.Context, _ *mcp.CallToolRequest, in itemIDInput) (*mcp.CallToolResult, any, error) {
	caller, err := h.resolveCaller(ctx, in.ItemID, "", "")
	if err != nil {
		return nil, nil, err
	}
	logs, err := h.svc.ListConsumes(ctx, caller, in.ItemID)
	if err != nil {
		return nil, nil, err
	}
	return nil, listConsumesOutput{Logs: logs}, nil
}

type traceOutput struct {
	Items []box.Item `json:"items"`
}

func (h *handlers) handleTrace(ctx context.Context, _ *mcp.CallToolRequest, in traceInput) (*mcp.CallToolResult, any, error) {
	items, err := h.svc.Trace(ctx, h.caller, in.BoxKey, box.SymbolQuery{
		Kind:  in.Kind,
		Value: in.Value,
		Ref:   in.Ref,
	})
	if err != nil {
		return nil, nil, err
	}
	return nil, traceOutput{Items: items}, nil
}

func (h *handlers) handleLegend(ctx context.Context, _ *mcp.CallToolRequest, in legendInput) (*mcp.CallToolResult, any, error) {
	item, err := h.svc.LegendOf(ctx, h.caller, box.Symbol{
		Kind:  in.Kind,
		Value: in.Value,
		Ref:   in.Ref,
	})
	if err != nil {
		return nil, nil, err
	}
	return nil, item, nil
}

func (h *handlers) handleNeighbors(ctx context.Context, _ *mcp.CallToolRequest, in neighborsInput) (*mcp.CallToolResult, any, error) {
	hops := in.Hops
	if hops == 0 {
		hops = 1
	}
	sub, err := h.svc.Neighbors(ctx, h.caller, in.ItemID, hops)
	if err != nil {
		return nil, nil, err
	}
	return nil, sub, nil
}

// ----- R0.13.2 event tools (was R0.10 task tools) --------------------------
//
// R0.13.2 removed box_create_task / box_get_task / box_set_task_status from
// the MCP surface. Three reasons:
//   - box_create_task is sugar for box_store; agents writing direct calls
//     can pick their own kind name.
//   - box_get_task duplicated box_show with a kind=task gate. The gate was
//     a privilege Box did not need to enforce (invariant #10).
//   - box_set_task_status was a kind=T-anchored convenience; box_set_item_
//     symbols is the generalisation and replaces it.
//
// Trace tools work on any item, not just kind=task. Schemas use
// item_id (was task_id). The wire-name shift is box_append_task_trace →
// box_append_event and box_list_task_trace → box_list_events.

type appendEventInput struct {
	ItemID string        `json:"item_id" jsonschema:"item id (required)"`
	Step   box.TraceStep `json:"step" jsonschema:"the trace step (op required; nail_ref/args/result/error optional)"`
}

type listEventsInput struct {
	ItemID string `json:"item_id" jsonschema:"item id (required)"`
}

type listEventsOutput struct {
	Events []box.TraceStep `json:"events"`
}

type setItemSymbolsInput struct {
	ItemID       string       `json:"item_id" jsonschema:"item id (required)"`
	Symbols      []box.Symbol `json:"symbols" jsonschema:"replacement symbol set (required)"`
	AllowHistory bool         `json:"allow_history,omitempty"`
}

// decodePassCriteria flattens an arbitrary JSON value sent over MCP into
// the json.RawMessage CreateTaskRequest expects. Box does not interpret it
// further (invariant #10 — R0.13.2 removed the kind whitelist).
func decodePassCriteria(raw any) (json.RawMessage, error) {
	if raw == nil {
		return nil, nil
	}
	buf, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: pass_criteria marshal: %v", box.ErrValidation, err)
	}
	return buf, nil
}

func (h *handlers) handleAppendEvent(ctx context.Context, _ *mcp.CallToolRequest, in appendEventInput) (*mcp.CallToolResult, any, error) {
	caller, err := h.resolveCaller(ctx, in.ItemID, "", "")
	if err != nil {
		return nil, nil, err
	}
	if err := h.svc.AppendEvent(ctx, caller, in.ItemID, in.Step); err != nil {
		return nil, nil, err
	}
	events, err := h.svc.ListEvents(ctx, caller, in.ItemID)
	if err != nil {
		return nil, nil, err
	}
	return nil, listEventsOutput{Events: events}, nil
}

func (h *handlers) handleListEvents(ctx context.Context, _ *mcp.CallToolRequest, in listEventsInput) (*mcp.CallToolResult, any, error) {
	events, err := h.svc.ListEvents(ctx, h.caller, in.ItemID)
	if err != nil {
		return nil, nil, err
	}
	return nil, listEventsOutput{Events: events}, nil
}

func (h *handlers) handleSetItemSymbols(ctx context.Context, _ *mcp.CallToolRequest, in setItemSymbolsInput) (*mcp.CallToolResult, any, error) {
	caller, err := h.resolveCaller(ctx, in.ItemID, "", "")
	if err != nil {
		return nil, nil, err
	}
	var opts []box.UpdateLabelsOption
	if in.AllowHistory {
		opts = append(opts, box.WithAllowHistory(true))
	}
	item, err := h.svc.SetItemSymbols(ctx, caller, in.ItemID, in.Symbols, opts...)
	if err != nil {
		return nil, nil, err
	}
	return nil, item, nil
}

// ----- R0.13.1 程辙层 (program-track) tools -------------------------------

type taskStartInput struct {
	BoxID        string            `json:"box_id" jsonschema:"target box id (required)"`
	Intent       string            `json:"intent" jsonschema:"task intent string (required)"`
	Source       []box.Symbol      `json:"source,omitempty"`
	Goal         []box.Symbol      `json:"goal" jsonschema:"goal symbols (at least one; required)"`
	PassCriteria any               `json:"pass_criteria" jsonschema:"pass criteria JSON object; Box never executes Query (compound supported via {kind:compound,operator,sub_criteria})"`
	NailChain    []string          `json:"nail_chain,omitempty"`
	NailDag      []box.NailDagNode `json:"nail_dag,omitempty"`
}

type taskStartOutput struct {
	Task  box.Item `json:"task"`
	Token string   `json:"token"`
}

type taskFinishInput struct {
	Token   string `json:"token" jsonschema:"YiCheng session token (required)"`
	Status  string `json:"status,omitempty" jsonschema:"final status symbol (default ✓)"`
	Summary string `json:"summary,omitempty"`
}

type taskAbortInput struct {
	Token  string `json:"token" jsonschema:"YiCheng session token (required)"`
	Reason string `json:"reason,omitempty"`
}

type taskTokenStatusInput struct {
	Token string `json:"token" jsonschema:"YiCheng session token (required)"`
}

type taskTokenStatusOutput struct {
	Active  bool         `json:"active"`
	Session *box.YiCheng `json:"session,omitempty"`
}

func (h *handlers) handleTaskStart(ctx context.Context, _ *mcp.CallToolRequest, in taskStartInput) (*mcp.CallToolResult, any, error) {
	caller, err := h.resolveCaller(ctx, "", in.BoxID, "")
	if err != nil {
		return nil, nil, err
	}
	pc, err := decodePassCriteria(in.PassCriteria)
	if err != nil {
		return nil, nil, err
	}
	req := box.CreateTaskRequest{
		Intent:       in.Intent,
		Source:       in.Source,
		Goal:         in.Goal,
		PassCriteria: pc,
		NailChain:    in.NailChain,
		NailDag:      in.NailDag,
	}
	task, token, err := h.svc.StartYiCheng(ctx, caller, in.BoxID, req)
	if err != nil {
		return nil, nil, err
	}
	return nil, taskStartOutput{Task: task, Token: token}, nil
}

func (h *handlers) handleTaskFinish(ctx context.Context, _ *mcp.CallToolRequest, in taskFinishInput) (*mcp.CallToolResult, any, error) {
	item, err := h.svc.FinishYiCheng(ctx, in.Token, in.Status, in.Summary)
	if err != nil {
		return nil, nil, err
	}
	return nil, item, nil
}

func (h *handlers) handleTaskAbort(ctx context.Context, _ *mcp.CallToolRequest, in taskAbortInput) (*mcp.CallToolResult, any, error) {
	item, err := h.svc.AbortYiCheng(ctx, in.Token, in.Reason)
	if err != nil {
		return nil, nil, err
	}
	return nil, item, nil
}

func (h *handlers) handleTaskTokenStatus(ctx context.Context, _ *mcp.CallToolRequest, in taskTokenStatusInput) (*mcp.CallToolResult, any, error) {
	sess, ok, err := h.svc.ValidateYiCheng(ctx, in.Token)
	if err != nil {
		return nil, nil, err
	}
	out := taskTokenStatusOutput{Active: ok}
	if ok {
		s := sess
		out.Session = &s
	}
	return nil, out, nil
}

// ----- R0.19 blob GC -------------------------------------------------------

type gcBlobsInput struct {
	DryRun         *bool `json:"dry_run,omitempty" jsonschema:"if true (default), report only — do not delete"`
	OlderThanSec   int   `json:"older_than_seconds,omitempty" jsonschema:"orphans newer than this are spared (default 86400)"`
}

// ----- R0.22 observability ------------------------------------------------

type observabilityInput struct {
	NamePrefix string `json:"name_prefix,omitempty" jsonschema:"optional metric-name prefix filter (e.g. 'box.create' returns only matches)"`
}

// handleObservability surfaces the in-memory MemObserver state as a
// wire-friendly SnapshotSummary. Read-only.
func (h *handlers) handleObservability(ctx context.Context, _ *mcp.CallToolRequest, in observabilityInput) (*mcp.CallToolResult, any, error) {
	return nil, h.svc.ObservabilitySnapshot(ctx, in.NamePrefix), nil
}

// handleGCBlobs runs one GC pass over the blob root configured for this
// server. Defaults are conservative (dry_run=true, older=24h) so an
// accidental call without args never deletes anything.
func (h *handlers) handleGCBlobs(ctx context.Context, _ *mcp.CallToolRequest, in gcBlobsInput) (*mcp.CallToolResult, any, error) {
	dry := true
	if in.DryRun != nil {
		dry = *in.DryRun
	}
	older := 24 * 3600
	if in.OlderThanSec > 0 {
		older = in.OlderThanSec
	}
	root, err := blobRoot(h.boxHome)
	if err != nil {
		return nil, nil, err
	}
	rep, err := runBlobGC(ctx, h.svc, root, gcOptions{
		DryRun:    dry,
		OlderThan: time.Duration(older) * time.Second,
	})
	if err != nil {
		return nil, nil, err
	}
	return nil, rep, nil
}

// handleOverview wires box_overview to Service.Overview. The handler does not
// fall back to box-owner caller resolution — overview is a cross-box read
// and requires an explicit --owner / $BOX_CALLER so caller-scoping is
// well-defined (R5.1 D#4).
func (h *handlers) handleOverview(ctx context.Context, _ *mcp.CallToolRequest, in overviewInput) (*mcp.CallToolResult, any, error) {
	var filter box.BoxFilter
	if in.Filter != nil {
		filter = *in.Filter
	}
	req := box.OverviewRequest{
		Axis:   in.Axis,
		Zoom:   in.Zoom,
		Filter: filter,
	}
	ov, err := h.svc.Overview(ctx, h.caller, req)
	if err != nil {
		return nil, nil, err
	}
	return nil, ov, nil
}
