package box

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	ErrNotFound       = errors.New("not found")
	ErrConflict       = errors.New("conflict")
	ErrForbidden      = errors.New("forbidden")
	ErrValidation     = errors.New("validation")
	ErrNotImplemented = errors.New("not implemented")
)

type Box struct {
	ID            string            `json:"id"`
	Key           string            `json:"key"`
	Version       int               `json:"version"`
	OwnerType     string            `json:"owner_type"`
	OwnerID       string            `json:"owner_id"`
	StoragePolicy StoragePolicy     `json:"storage_policy"`
	Status        string            `json:"status"`
	CreatedAt     time.Time         `json:"created_at"`
	Labels        map[string]string `json:"labels,omitempty"`
}

type StoragePolicy struct {
	AllowedFormats  []string `json:"allowed_formats"`
	MaxItems        int      `json:"max_items"`
	MaxContentBytes int      `json:"max_content_bytes,omitempty"`
}

type Item struct {
	ID           string            `json:"id"`
	BoxID        string            `json:"box_id"`
	IdemKey      string            `json:"idem_key"`
	Kind         string            `json:"kind"`
	SourceType   string            `json:"source_type"`
	SourceRef    map[string]string `json:"source_ref"`
	Labels       map[string]string `json:"labels"`
	LocationID   string            `json:"location_id,omitempty"`
	StorageURI   string            `json:"storage_uri"`
	Format       string            `json:"format"`
	Content      json.RawMessage   `json:"content,omitempty"`
	ContentHash  string            `json:"content_hash"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	Status       string            `json:"status"`
	StoredBy     string            `json:"stored_by,omitempty"`
	StoredAt     time.Time         `json:"stored_at"`
	RevisionOf   string            `json:"revision_of,omitempty"`
	Revision     int               `json:"revision"`
	IsLatest     bool              `json:"is_latest"`
	SupersededAt *time.Time        `json:"superseded_at,omitempty"`
	Symbols      []Symbol          `json:"symbols,omitempty"`
}

type ConsumeLog struct {
	ID           string    `json:"id"`
	ItemID       string    `json:"item_id"`
	ConsumerType string    `json:"consumer_type"`
	ConsumerID   string    `json:"consumer_id"`
	Purpose      string    `json:"purpose,omitempty"`
	ConsumedAt   time.Time `json:"consumed_at"`
}

type CreateBoxRequest struct {
	Key           string
	OwnerType     string
	OwnerID       string
	StoragePolicy StoragePolicy
	Labels        map[string]string
}

// CreateTaskRequest is the schema for Service.CreateTask (R0.10 v2).
//
// Box validates the schema only — it does NOT interpret Source/Goal/
// PassCriteria semantically (invariant #10). The agent is responsible for
// deciding when the task is done and flipping the status via SetItemSymbols.
//
// Validation rules:
//   - Intent must be non-empty.
//   - Source may be empty (greenfield/creation tasks).
//   - Goal must have ≥1 entry; each entry is passed through ValidateSymbol
//     (note: ValidateSymbol — the per-entry check — not ValidateSymbols, so
//     a Goal entry may carry only a status / scope / etc. without an
//     accompanying SymKind).
//   - PassCriteria is required (see PassCriteria validation below).
//   - NailChain entries (if any) must be non-empty strings; Box does not
//     check whether the referenced nail exists — that is the agent's job.
//     R0.10 v1 line-up; v2 prefers NailDag (NailChain stays for compat).
//   - NailDag (R0.10 v2): when non-empty, each NailDagNode.ID must be unique,
//     each DependsOn entry must reference an existing node ID, and the graph
//     must be acyclic. Box validates structure only — DAG execution (topo
//     sort, branch concurrency, joins) is the agent's job.
type CreateTaskRequest struct {
	Intent       string          // required, non-empty
	Source       []Symbol        // optional (empty for greenfield tasks)
	Goal         []Symbol        // required, ≥1 entry, each ValidateSymbol-clean
	PassCriteria json.RawMessage // R0.13.2: opaque JSON; Box no longer validates shape. nil = unset.
	NailChain    []string        // R0.10 v1: optional; each entry must be non-empty
	NailDag      []NailDagNode   // R0.10 v2: optional; structurally validated
}

// NailDagNode is one node in a task's nail DAG (R0.10 v2). It replaces the
// linear v1 NailChain []string with a depends-on graph that can express
// branch + join shapes.
//
// Box validates structural invariants only:
//   - ID is unique within the DAG (across the slice)
//   - DependsOn references existing IDs in the same DAG
//   - the overall graph is acyclic
//
// Box never runs a topological sort or schedules nodes — that belongs to the
// agent (invariant #10). BranchID is a free-form grouping tag the agent
// chooses to keep parallel branches identifiable in the trace.
type NailDagNode struct {
	ID        string   `json:"id"`
	NailRef   string   `json:"nail_ref"`
	DependsOn []string `json:"depends_on,omitempty"`
	BranchID  string   `json:"branch_id,omitempty"`
}

// PassCriteria was a typed schema in R0.10–R0.13.1 (Kind whitelist:
// exists/absent/all_match/count_eq/compound + Operator/SubCriteria).
// R0.13.2 removed the schema enforcement to honour invariant #10 — Box
// never knew how to *run* a query, but it still policed *which* queries
// were stateable, which was a soft form of knowing about task semantics.
//
// Today PassCriteria lives as opaque json.RawMessage on CreateTaskRequest.
// The struct is preserved only as a documentation hint for callers who
// wish to send the canonical shape (e.g. existing trace replayers); Box
// itself no longer references it.
type PassCriteria struct {
	Kind   string      `json:"kind,omitempty"`
	Query  SymbolQuery `json:"query,omitempty"`
	Arg    int         `json:"arg,omitempty"`
	Reason string      `json:"reason,omitempty"`

	Operator    string         `json:"operator,omitempty"`
	SubCriteria []PassCriteria `json:"sub_criteria,omitempty"`
}

// TraceStep is one append-only event in a task's trace.jsonl.
//
// Step is assigned by Service.AppendTaskTrace (the current trace length); the
// caller may leave it as zero. AppendedAt is also set by the service.
//
// Op is a free-text label (box verb name like "store" or arbitrary like
// "llm_call"). NailRef is an optional pointer back to the originating
// NailForge nail step (e.g. "database_engine_forge/a1") — Box never
// dereferences it.
//
// R0.10 v2 additions:
//   - NodeID/BranchID let parallel-branch traces be regrouped client-side.
//   - Step is no longer dropped on zero — step=0 is a meaningful first entry
//     and is always emitted on the wire (custom MarshalJSON). The json tag
//     still says omitempty so MCP's auto-schema treats it as optional input.
type TraceStep struct {
	// Step is assigned by the store (= current trace length); callers may
	// leave it zero. The wire encoding always includes it (MarshalJSON).
	Step     int             `json:"step,omitempty"`
	NailRef  string          `json:"nail_ref,omitempty"`
	NodeID   string          `json:"node_id,omitempty"`   // R0.10 v2: corresponds to NailDagNode.ID
	BranchID string          `json:"branch_id,omitempty"` // R0.10 v2: branch grouping
	Op       string          `json:"op"`
	Args     json.RawMessage `json:"args,omitempty"`
	Result   json.RawMessage `json:"result,omitempty"`
	Error    string          `json:"error,omitempty"`
	// AppendedAt is set by Service.AppendTaskTrace; callers do not pass it.
	AppendedAt time.Time `json:"appended_at,omitempty"`
}

// MarshalJSON ensures Step is always emitted (R0.10 v2: D#16), including the
// step=0 first entry, while keeping the struct tag as "step,omitempty" so
// auto-generated input schemas treat callers' missing step as valid.
func (t TraceStep) MarshalJSON() ([]byte, error) {
	// Use an inline flat struct (NOT the embedded-alias trick — embedding
	// promotes both the outer "step" and the embedded "step", giving JSON
	// two keys) so Step is always written exactly once with its real value.
	return json.Marshal(struct {
		Step       int             `json:"step"`
		NailRef    string          `json:"nail_ref,omitempty"`
		NodeID     string          `json:"node_id,omitempty"`
		BranchID   string          `json:"branch_id,omitempty"`
		Op         string          `json:"op"`
		Args       json.RawMessage `json:"args,omitempty"`
		Result     json.RawMessage `json:"result,omitempty"`
		Error      string          `json:"error,omitempty"`
		AppendedAt time.Time       `json:"appended_at,omitempty"`
	}{
		Step:       t.Step,
		NailRef:    t.NailRef,
		NodeID:     t.NodeID,
		BranchID:   t.BranchID,
		Op:         t.Op,
		Args:       t.Args,
		Result:     t.Result,
		Error:      t.Error,
		AppendedAt: t.AppendedAt,
	})
}

// YiCheng (一程) is one in-flight task session in the program-track layer
// (R0.13.1). It lives only in process memory (sync.Map) — a restart wipes
// the session table by design (see invariant #11). The on-disk task trace
// remains intact; only the live token bindings disappear.
//
// YiCheng is not a transaction — it has no rollback, no isolation, no
// distributed-commit semantics. It is a path-ledger session: a token
// identifies one execution path so writes can opt-in to auto-trace under it.
type YiCheng struct {
	TaskID    string    `json:"task_id"`
	CallerID  string    `json:"caller_id"`
	CreatedAt time.Time `json:"created_at"`
}

// R0.13.2 removed the PassCriteria schema validator (passCriteriaKinds /
// passCriteriaOperators / passCriteriaMaxDepth / validatePassCriteria /
// validatePassCriteriaDepth). Box never knew how to *run* a pass_criteria;
// the surface-level whitelist of Kind values was the last vestige of Box
// knowing about task semantics, which softly contradicted invariant #10.
//
// Today the request carries pass_criteria as opaque json.RawMessage; the
// only check is "must be valid JSON" (enforced by json.Marshal at the
// callsite). Agents may invent any predicate shape they want.

// validateGoalSymbols is the Goal-specific symbol validator. Unlike
// ValidateSymbols (which insists on ≥1 SymKind), goals may describe pure
// status/topic targets (e.g. [{kind:status, value:"✓"}]).
func validateGoalSymbols(goal []Symbol) error {
	if len(goal) == 0 {
		return fmt.Errorf("%w: goal must contain at least one symbol", ErrValidation)
	}
	for _, s := range goal {
		if err := ValidateSymbol(s); err != nil {
			return err
		}
	}
	return nil
}

// validateNailChain enforces the per-entry non-empty rule. It does NOT verify
// that the referenced nail exists — that is the agent / NailForge concern.
func validateNailChain(chain []string) error {
	for i, entry := range chain {
		if entry == "" {
			return fmt.Errorf("%w: nail_chain[%d] is empty", ErrValidation, i)
		}
	}
	return nil
}

// validateNailDag enforces the R0.10 v2 structural rules:
//   - every NailDagNode.ID is non-empty AND unique within the slice
//   - every NailDagNode.NailRef is non-empty
//   - every DependsOn entry references an existing node ID in the same slice
//   - the depends-on graph is acyclic
//
// Box does not topologically sort, schedule, or invoke any nodes (invariant
// #10). A nil/empty slice is accepted — NailDag is optional alongside the
// v1 NailChain.
func validateNailDag(dag []NailDagNode) error {
	if len(dag) == 0 {
		return nil
	}
	ids := make(map[string]int, len(dag))
	for i, n := range dag {
		if n.ID == "" {
			return fmt.Errorf("%w: nail_dag[%d].id is empty", ErrValidation, i)
		}
		if _, dup := ids[n.ID]; dup {
			return fmt.Errorf("%w: nail_dag duplicate node id %q", ErrValidation, n.ID)
		}
		ids[n.ID] = i
		if n.NailRef == "" {
			return fmt.Errorf("%w: nail_dag[%d].nail_ref is empty", ErrValidation, i)
		}
	}
	for i, n := range dag {
		for j, dep := range n.DependsOn {
			if dep == "" {
				return fmt.Errorf("%w: nail_dag[%d].depends_on[%d] is empty", ErrValidation, i, j)
			}
			if _, ok := ids[dep]; !ok {
				return fmt.Errorf("%w: nail_dag node %q depends_on unknown id %q", ErrValidation, n.ID, dep)
			}
			if dep == n.ID {
				return fmt.Errorf("%w: nail_dag node %q depends_on itself (cycle)", ErrValidation, n.ID)
			}
		}
	}
	// Cycle detection — DFS with white/gray/black coloring (iterative-safe at
	// this scale: typical DAGs are <50 nodes).
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(dag))
	for _, n := range dag {
		color[n.ID] = white
	}
	byID := make(map[string]NailDagNode, len(dag))
	for _, n := range dag {
		byID[n.ID] = n
	}
	var visit func(id string) error
	visit = func(id string) error {
		switch color[id] {
		case gray:
			return fmt.Errorf("%w: nail_dag contains a cycle (through node %q)", ErrValidation, id)
		case black:
			return nil
		}
		color[id] = gray
		for _, dep := range byID[id].DependsOn {
			if err := visit(dep); err != nil {
				return err
			}
		}
		color[id] = black
		return nil
	}
	for _, n := range dag {
		if err := visit(n.ID); err != nil {
			return err
		}
	}
	return nil
}

type StoreRequest struct {
	IdemKey    string
	Kind       string
	SourceType string
	SourceRef  map[string]string
	Labels     map[string]string
	LocationID string
	StorageURI string
	Format     string
	Content    json.RawMessage
	Metadata   map[string]string
	StoredBy   string
	Symbols    []Symbol
}

type ConsumeOptions struct {
	Purpose      string
	MarkConsumed bool
	ConsumerType string
}

type BrowseFilter struct {
	Kind           string
	SourceRef      map[string]string
	Labels         map[string]string
	LocationIDs    []string
	Since          *time.Time
	Until          *time.Time
	Limit          int
	Offset         int
	IncludeHistory bool
	OnlyHistory    bool
}

// SymbolQuery is a filter for Trace. Kind/Value lists are OR-within and
// AND-across (the matching symbol must satisfy every populated dimension).
// BoxScope is unused — Trace's boxKey parameter selects the scope ("" =
// across all boxes). Kept as a forward-compat hint; harmless when empty.
type SymbolQuery struct {
	Kind     []SymbolKind `json:"kind,omitempty"`
	Value    []string     `json:"value,omitempty"`
	Ref      string       `json:"ref,omitempty"`
	BoxScope string       `json:"box_scope,omitempty"`
}

// Subgraph is the AI-friendly JSON shape returned by Service.Neighbors.
type Subgraph struct {
	Center string    `json:"center"`
	Nodes  []NodeRef `json:"nodes"`
	Edges  []EdgeRef `json:"edges"`
}

// NodeRef is a single node in a Subgraph. Kind is the Item.Kind (the DB
// classifier, distinct from the SymKind routing symbol).
type NodeRef struct {
	ItemID   string `json:"item_id"`
	BoxID    string `json:"box_id"`
	Kind     string `json:"kind,omitempty"`
	KindSym  string `json:"kind_sym,omitempty"`
	Status   string `json:"status,omitempty"`
	Distance int    `json:"distance"`
}

// EdgeRef is one directed SymRelation edge in a Subgraph.
type EdgeRef struct {
	From string `json:"from"`
	To   string `json:"to"`
	Rel  string `json:"rel"`
}

type Summary struct {
	BoxID          string         `json:"box_id"`
	TotalItems     int            `json:"total_items"`
	ByKind         map[string]int `json:"by_kind"`
	BySourceType   map[string]int `json:"by_source_type"`
	LatestStoredAt *time.Time     `json:"latest_stored_at,omitempty"`
}

// BoxFilter is the orthogonal filter dimension on Overview (R5.1). All three
// fields AND-combine: a Box must match every populated dimension to land in
// the result. An empty BoxFilter matches every (caller-owned) box.
type BoxFilter struct {
	Owner  string            `json:"owner,omitempty"`
	Status string            `json:"status,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
}

// OverviewRequest is the R5.1 box_overview input (geo-globe model).
//
//   - Axis chooses the rotation: "owner" | "status" | "label:<key>".
//   - Zoom chooses the granularity: 0 = histogram only (map[string]int),
//     1 = one BoxGlyph per box (each group's Boxes slice is capped at 10).
//     Zoom=2 (drill into a single box) is deferred to R6.
//   - Filter limits the population (orthogonal to axis).
type OverviewRequest struct {
	Axis   string    `json:"axis"`
	Zoom   int       `json:"zoom,omitempty"`
	Filter BoxFilter `json:"filter,omitempty"`
}

// Overview is the R5.1 box_overview response.
//
// Zoom=0: Groups is map[string]int (the histogram). Groups field is left nil.
// Zoom=1: Groups is []OverviewGroup (one entry per axis bucket, each carrying
// up to 10 BoxGlyph entries). Histogram is left nil.
//
// Total is the populated-box count after Filter is applied AND caller-scoping
// is enforced (non-caller-owned boxes never count).
type Overview struct {
	Axis      string           `json:"axis"`
	Zoom      int              `json:"zoom"`
	Total     int              `json:"total"`
	Histogram map[string]int   `json:"histogram,omitempty"`
	Groups    []OverviewGroup  `json:"groups,omitempty"`
}

// OverviewGroup is one axis bucket at zoom=1. Boxes is capped at 10 — the
// design rejects long []Box lists as a response shape (R5.1 hard constraint #1).
type OverviewGroup struct {
	Key   string     `json:"key"`
	Count int        `json:"count"`
	Boxes []BoxGlyph `json:"boxes,omitempty"`
}

// BoxGlyph is the machine+human dual-load box descriptor (R5.1 hard constraint
// #3). Glyph is the visual literal ("◐" active, "◼" sealed, etc.); Status is
// the parallel string field; both are populated.
type BoxGlyph struct {
	Glyph     string     `json:"glyph"`
	Key       string     `json:"key"`
	ID        string     `json:"id"`
	Status    string     `json:"status"`
	Items     int        `json:"items"`
	LabelsTop []string   `json:"labels_top,omitempty"`
	// Latest is a *time.Time so an empty box (no items stored yet) marshals
	// as `null` instead of Go's zero "0001-01-01T00:00:00Z". H1 from R6
	// dogfood: subagent flagged the zero-value leak as readability noise.
	Latest *time.Time `json:"latest,omitempty"`
}

func DefaultPolicy() StoragePolicy {
	return StoragePolicy{
		AllowedFormats:  []string{"json", "markdown", "text"},
		MaxItems:        1000,
		MaxContentBytes: 256 * 1024,
	}
}

func NewID(prefix string) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

func ContentHash(content json.RawMessage) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

var labelNameRe = regexp.MustCompile(`^[A-Za-z0-9_:.@/-]+$`)

func ValidateLabels(labels map[string]string) error {
	for k, v := range labels {
		if !labelNameRe.MatchString(k) {
			return fmt.Errorf("%w: invalid label key %q", ErrValidation, k)
		}
		if len(v) > 1024 {
			return fmt.Errorf("%w: label %q exceeds 1KB", ErrValidation, k)
		}
	}
	return nil
}

func ValidateStorageURI(uri string) error {
	if uri == "" {
		return fmt.Errorf("%w: storage_uri is required", ErrValidation)
	}
	allowed := []string{"row://", "blob://", "folder://", "repo://", "s3://", "ipfs://", "collection://"}
	for _, prefix := range allowed {
		if strings.HasPrefix(uri, prefix) {
			return nil
		}
	}
	return fmt.Errorf("%w: unsupported storage_uri scheme", ErrValidation)
}

func formatAllowed(policy StoragePolicy, format string) bool {
	for _, f := range policy.AllowedFormats {
		if f == format {
			return true
		}
	}
	return false
}
