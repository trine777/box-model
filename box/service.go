package box

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/windborneos/box-model/box/obs"
)

// yiChengSessions is the module-level program-track session table (R0.13.1).
// Keys are tokens (string "tsk_..."), values are *YiCheng. The map is in
// memory only — a process restart wipes every binding by design (invariant
// #11: tokens are session state, not authorization, and never persist).
var yiChengSessions sync.Map

// tokenPrefix is the printable prefix (first 12 chars + "...") of a token,
// safe for logging and on-disk trace payloads. The full token never leaves
// memory.
func tokenPrefix(tok string) string {
	if len(tok) <= 12 {
		return tok + "..."
	}
	return tok[:12] + "..."
}

// newYiChengToken mints a fresh program-track session token. Shape is
//
//	"tsk_" + base64-urlsafe(16 random bytes, no padding)
//
// which gives ~128 bits of entropy and fits comfortably on a single log line.
func newYiChengToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "tsk_" + base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// writeOpts carries options resolved from variadic WriteOption arguments.
// YiChengToken binds an opt-in program-track session for auto-tracing.
// AllowHistory mirrors the legacy UpdateLabelsOption flag — kept here so the
// six writer methods can converge on a single option type.
type writeOpts struct {
	yiChengToken string
	allowHistory bool
}

// WriteOption customizes the six "writer" service methods (Store /
// ReplaceItem / UpdateLabels / DeleteItem / Consume / SetItemSymbols) and
// their cousins (MergeLabels / RemoveLabels). Construct via WithYiChengToken
// or WithAllowHistoryOpt; the option type is intentionally narrow.
type WriteOption func(*writeOpts)

// WithYiChengToken binds a write to an active YiCheng (program-track)
// session. When the token is non-empty AND resolvable, the service appends
// one TraceStep to the bound task's trace on a successful write. Unknown or
// empty tokens are silently dropped — writes succeed either way (invariant
// #11). The token's full value is never written to the trace; only its
// prefix is recorded.
func WithYiChengToken(token string) WriteOption {
	return func(o *writeOpts) { o.yiChengToken = token }
}

// resolveWriteOpts collapses a variadic []WriteOption into a single struct.
func resolveWriteOpts(opts []WriteOption) writeOpts {
	var o writeOpts
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// autoTrace appends a token-derived TraceStep when opts carries a known
// YiCheng token. The trace event is best-effort: a missing/unknown token is
// silently skipped; an AppendEvent failure is logged but does not turn the
// parent write into a failure (program-track is observability, not a
// commit gate — invariant #11).
//
// args is a small JSON map summarising the call (e.g. {"item_id":"..."});
// pass nil to omit.
func (s *Service) autoTrace(ctx context.Context, opts writeOpts, op string, args map[string]any) {
	if opts.yiChengToken == "" {
		return
	}
	raw, ok := yiChengSessions.Load(opts.yiChengToken)
	if !ok {
		return
	}
	sess, ok := raw.(*YiCheng)
	if !ok || sess == nil {
		return
	}
	merged := map[string]any{
		"token_prefix": tokenPrefix(opts.yiChengToken),
	}
	for k, v := range args {
		merged[k] = v
	}
	payload, err := json.Marshal(merged)
	if err != nil {
		return
	}
	step := TraceStep{
		Op:   op,
		Args: payload,
	}
	if err := s.AppendEvent(ctx, sess.CallerID, sess.TaskID, step); err != nil {
		s.obs.LogWarn("auto-trace failed",
			"op", "autoTrace",
			"task_id", sess.TaskID,
			"yi_op", op,
			"err", err.Error(),
		)
	}
}

// ServiceOption customizes Service construction (functional options pattern).
type ServiceOption func(*Service)

// WithObserver installs a non-nil obs.Observer on the Service. A nil observer
// is silently ignored so callers can wire optional observability without
// extra branching.
func WithObserver(o obs.Observer) ServiceOption {
	return func(s *Service) {
		if o != nil {
			s.obs = o
		}
	}
}

// Service is the public façade over Store. The obs field is never nil — when
// no observer is supplied via WithObserver the constructor wires NoopObserver
// so the hot-path call sites can drop their nil-check.
type Service struct {
	store Store
	obs   obs.Observer
}

// NewService builds a Service over store. Optional ServiceOptions (e.g.
// WithObserver) may be passed; the variadic signature is backward-compatible
// with the original NewService(store) call sites.
func NewService(store Store, opts ...ServiceOption) *Service {
	s := &Service{store: store, obs: obs.NoopObserver{}}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// classifyErr maps a domain error to the low-cardinality err_type tag used
// throughout the observability stack. The default ("internal") is the catch-
// all bucket for unexpected/upstream failures.
func classifyErr(err error) string {
	switch {
	case errors.Is(err, ErrValidation):
		return "validation"
	case errors.Is(err, ErrForbidden):
		return "forbidden"
	case errors.Is(err, ErrNotFound):
		return "notfound"
	case errors.Is(err, ErrConflict):
		return "conflict"
	default:
		return "internal"
	}
}

// uriScheme extracts the scheme prefix from a storage URI (e.g. "row" from
// "row://table/x"). Returns "unknown" when the input has no "://" separator
// so the tag stays low-cardinality on malformed inputs.
func uriScheme(uri string) string {
	if i := strings.Index(uri, "://"); i > 0 {
		return uri[:i]
	}
	return "unknown"
}

// cloneTags returns a shallow copy of in plus one slot of headroom — used at
// error sites to add the err_type tag without mutating the success-path map
// (which would otherwise cross-pollinate the success and error metric keys).
func cloneTags(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (s *Service) CreateBox(ctx context.Context, req CreateBoxRequest) (Box, error) {
	if req.OwnerType == "" {
		req.OwnerType = "standalone"
	}
	tags := map[string]string{"owner_type": req.OwnerType}
	s.obs.Inc("box.create.attempt", tags)
	start := time.Now()

	b, err := func() (Box, error) {
		if req.Key == "" {
			return Box{}, fmt.Errorf("%w: key is required", ErrValidation)
		}
		if req.OwnerID == "" {
			req.OwnerID = "anonymous"
		}
		if req.StoragePolicy.MaxItems == 0 {
			req.StoragePolicy = DefaultPolicy()
		}
		if req.StoragePolicy.MaxItems < 0 {
			return Box{}, fmt.Errorf("%w: max_items must be non-negative", ErrValidation)
		}
		if req.StoragePolicy.MaxContentBytes < 0 {
			return Box{}, fmt.Errorf("%w: max_content_bytes must be non-negative", ErrValidation)
		}
		if err := ValidateLabels(req.Labels); err != nil {
			return Box{}, err
		}
		return s.store.CreateBox(ctx, Box{
			ID:            NewID("box_"),
			Key:           req.Key,
			Version:       1,
			OwnerType:     req.OwnerType,
			OwnerID:       req.OwnerID,
			StoragePolicy: req.StoragePolicy,
			Status:        "active",
			CreatedAt:     nowUTC(),
			Labels:        cloneMap(req.Labels),
		})
	}()

	dur := float64(time.Since(start).Milliseconds())
	if err != nil {
		errTags := cloneTags(tags)
		errTags["err_type"] = classifyErr(err)
		s.obs.Observe("box.create.duration_ms", dur, errTags)
		s.obs.Inc("box.create.error", errTags)
		s.obs.LogWarn("create box failed", "op", "CreateBox", "err", err.Error(), "err_type", errTags["err_type"], "key", req.Key)
		return Box{}, err
	}
	s.obs.Observe("box.create.duration_ms", dur, tags)
	s.obs.Inc("box.create.success", tags)
	s.obs.LogInfo("box created", "op", "CreateBox", "box_id", b.ID, "key", b.Key, "owner_type", b.OwnerType)
	return b, nil
}

// GetBoxByKey resolves a Box by its public key. callerID is currently
// unused (key lookup itself is not gated; per-operation authorization
// happens in the methods that mutate the box).
func (s *Service) GetBoxByKey(ctx context.Context, callerID, key string) (Box, error) {
	tags := map[string]string{}
	s.obs.Inc("box.get_by_key.attempt", tags)
	start := time.Now()

	b, err := func() (Box, error) {
		if key == "" {
			return Box{}, fmt.Errorf("%w: key is required", ErrValidation)
		}
		return s.store.GetBoxByKey(ctx, key)
	}()

	dur := float64(time.Since(start).Milliseconds())
	if err != nil {
		errTags := cloneTags(tags)
		errTags["err_type"] = classifyErr(err)
		s.obs.Observe("box.get_by_key.duration_ms", dur, errTags)
		s.obs.Inc("box.get_by_key.error", errTags)
		s.obs.LogWarn("get_by_key failed", "op", "GetBoxByKey", "err", err.Error(), "err_type", errTags["err_type"], "key", key)
		return Box{}, err
	}
	s.obs.Observe("box.get_by_key.duration_ms", dur, tags)
	s.obs.Inc("box.get_by_key.success", tags)
	s.obs.LogInfo("box resolved", "op", "GetBoxByKey", "box_id", b.ID, "key", key)
	return b, nil
}

func (s *Service) SealBox(ctx context.Context, callerID, boxID string) error {
	tags := map[string]string{}
	s.obs.Inc("box.seal.attempt", tags)
	start := time.Now()

	err := func() error {
		b, err := s.store.GetBox(ctx, boxID)
		if err != nil {
			return err
		}
		if b.OwnerID != callerID {
			return forbiddenCallerMismatch(callerID, b.OwnerID)
		}
		return s.store.SealBox(ctx, boxID)
	}()

	dur := float64(time.Since(start).Milliseconds())
	if err != nil {
		errTags := cloneTags(tags)
		errTags["err_type"] = classifyErr(err)
		s.obs.Observe("box.seal.duration_ms", dur, errTags)
		s.obs.Inc("box.seal.error", errTags)
		s.obs.LogWarn("seal failed", "op", "SealBox", "err", err.Error(), "err_type", errTags["err_type"], "box_id", boxID)
		return err
	}
	s.obs.Observe("box.seal.duration_ms", dur, tags)
	s.obs.Inc("box.seal.success", tags)
	s.obs.LogInfo("box sealed", "op", "SealBox", "box_id", boxID)
	return nil
}

func (s *Service) Store(ctx context.Context, callerID, boxID string, req StoreRequest, writeOpts ...WriteOption) (Item, error) {
	wo := resolveWriteOpts(writeOpts)
	tags := map[string]string{
		"kind":           req.Kind,
		"source_type":    req.SourceType,
		"storage_scheme": uriScheme(req.StorageURI),
	}
	s.obs.Inc("item.store.attempt", tags)
	start := time.Now()

	item, err := func() (Item, error) {
		b, err := s.store.GetBox(ctx, boxID)
		if err != nil {
			return Item{}, err
		}
		if b.Status != "active" {
			return Item{}, ErrConflict
		}
		if b.OwnerID != callerID {
			return Item{}, forbiddenCallerMismatch(callerID, b.OwnerID)
		}
		if req.Kind == "" {
			return Item{}, fmt.Errorf("%w: kind is required", ErrValidation)
		}
		if req.SourceType == "" {
			return Item{}, fmt.Errorf("%w: source_type is required", ErrValidation)
		}
		if req.Format == "" {
			req.Format = "json"
		}
		if !formatAllowed(b.StoragePolicy, req.Format) {
			return Item{}, fmt.Errorf("%w: format %q not allowed", ErrValidation, req.Format)
		}
		if err := ValidateStorageURI(req.StorageURI); err != nil {
			return Item{}, err
		}
		if err := ValidateLabels(req.Labels); err != nil {
			return Item{}, err
		}
		if err := ValidateSymbols(req.Symbols); err != nil {
			return Item{}, err
		}
		count, err := s.store.CountItems(ctx, boxID)
		if err != nil {
			return Item{}, err
		}
		if count >= b.StoragePolicy.MaxItems {
			return Item{}, ErrConflict
		}
		if len(req.Content) == 0 {
			req.Content = json.RawMessage(`null`)
		}
		if b.StoragePolicy.MaxContentBytes > 0 && len(req.Content) > b.StoragePolicy.MaxContentBytes {
			return Item{}, fmt.Errorf("%w: content size %d exceeds max %d", ErrValidation, len(req.Content), b.StoragePolicy.MaxContentBytes)
		}
		if req.IdemKey == "" {
			req.IdemKey = ContentHash(json.RawMessage(req.SourceType + ":" + req.StorageURI))
		}
		return s.store.InsertItem(ctx, Item{
			ID:          NewID("item_"),
			BoxID:       boxID,
			IdemKey:     req.IdemKey,
			Kind:        req.Kind,
			SourceType:  req.SourceType,
			SourceRef:   cloneMap(req.SourceRef),
			Labels:      cloneMap(req.Labels),
			LocationID:  req.LocationID,
			StorageURI:  req.StorageURI,
			Format:      req.Format,
			Content:     req.Content,
			ContentHash: ContentHash(req.Content),
			Metadata:    cloneMap(req.Metadata),
			Status:      "available",
			StoredBy:    callerID,
			StoredAt:    nowUTC(),
			Revision:    1,
			IsLatest:    true,
			Symbols:     cloneSymbols(req.Symbols),
		})
	}()

	dur := float64(time.Since(start).Milliseconds())
	if err != nil {
		errTags := cloneTags(tags)
		errTags["err_type"] = classifyErr(err)
		s.obs.Observe("item.store.duration_ms", dur, errTags)
		s.obs.Inc("item.store.error", errTags)
		s.obs.LogWarn("store failed", "op", "Store", "box_id", boxID, "err", err.Error(), "err_type", errTags["err_type"], "kind", req.Kind)
		return Item{}, err
	}
	s.obs.Observe("item.store.duration_ms", dur, tags)
	s.obs.Inc("item.store.success", tags)
	s.obs.LogInfo("item stored", "op", "Store", "box_id", boxID, "item_id", item.ID, "kind", item.Kind, "storage_scheme", tags["storage_scheme"])
	s.autoTrace(ctx, wo, "store", map[string]any{"item_id": item.ID, "kind": item.Kind})
	return item, nil
}

func (s *Service) Browse(ctx context.Context, boxID string, filter BrowseFilter) ([]Item, error) {
	tags := map[string]string{}
	s.obs.Inc("item.browse.attempt", tags)
	start := time.Now()

	items, err := func() ([]Item, error) {
		if filter.IncludeHistory && filter.OnlyHistory {
			return nil, fmt.Errorf("%w: include_history and only_history are mutually exclusive", ErrValidation)
		}
		if err := ValidateLabels(filter.Labels); err != nil {
			return nil, err
		}
		return s.store.Browse(ctx, boxID, filter)
	}()

	dur := float64(time.Since(start).Milliseconds())
	if err != nil {
		errTags := cloneTags(tags)
		errTags["err_type"] = classifyErr(err)
		s.obs.Observe("item.browse.duration_ms", dur, errTags)
		s.obs.Inc("item.browse.error", errTags)
		s.obs.LogWarn("browse failed", "op", "Browse", "box_id", boxID, "err", err.Error(), "err_type", errTags["err_type"])
		return nil, err
	}
	s.obs.Observe("item.browse.duration_ms", dur, tags)
	s.obs.Inc("item.browse.success", tags)
	s.obs.Observe("item.browse.result_count", float64(len(items)), nil)
	s.obs.LogInfo("browse ok", "op", "Browse", "box_id", boxID, "result_count", len(items))
	return items, nil
}

func (s *Service) GetItem(ctx context.Context, callerID, itemID string) (Item, error) {
	tags := map[string]string{}
	s.obs.Inc("item.get.attempt", tags)
	start := time.Now()

	item, err := s.store.GetItem(ctx, itemID)

	dur := float64(time.Since(start).Milliseconds())
	if err != nil {
		errTags := cloneTags(tags)
		errTags["err_type"] = classifyErr(err)
		s.obs.Observe("item.get.duration_ms", dur, errTags)
		s.obs.Inc("item.get.error", errTags)
		s.obs.LogWarn("get failed", "op", "GetItem", "item_id", itemID, "err", err.Error(), "err_type", errTags["err_type"])
		return Item{}, err
	}
	s.obs.Observe("item.get.duration_ms", dur, tags)
	s.obs.Inc("item.get.success", tags)
	s.obs.LogInfo("get ok", "op", "GetItem", "item_id", item.ID, "kind", item.Kind)
	return item, nil
}

func (s *Service) Consume(ctx context.Context, callerID, itemID string, opts ConsumeOptions, writeOpts ...WriteOption) (Item, error) {
	wo := resolveWriteOpts(writeOpts)
	consumerType := opts.ConsumerType
	if consumerType == "" {
		consumerType = "user"
	}
	markStr := "false"
	if opts.MarkConsumed {
		markStr = "true"
	}
	tags := map[string]string{
		"consumer_type": consumerType,
		"mark_consumed": markStr,
	}
	s.obs.Inc("item.consume.attempt", tags)
	start := time.Now()

	item, err := func() (Item, error) {
		item, err := s.store.GetItem(ctx, itemID)
		if err != nil {
			return Item{}, err
		}
		if err := s.store.RecordConsume(ctx, ConsumeLog{
			ID:           NewID("consume_"),
			ItemID:       itemID,
			ConsumerType: consumerType,
			ConsumerID:   callerID,
			Purpose:      opts.Purpose,
			ConsumedAt:   nowUTC(),
		}); err != nil {
			return Item{}, err
		}
		if opts.MarkConsumed {
			if err := s.store.MarkConsumed(ctx, itemID); err != nil {
				return Item{}, err
			}
			refreshed, err := s.store.GetItem(ctx, itemID)
			if err != nil {
				return Item{}, err
			}
			item = refreshed
		}
		return item, nil
	}()

	dur := float64(time.Since(start).Milliseconds())
	if err != nil {
		errTags := cloneTags(tags)
		errTags["err_type"] = classifyErr(err)
		s.obs.Observe("item.consume.duration_ms", dur, errTags)
		s.obs.Inc("item.consume.error", errTags)
		s.obs.LogWarn("consume failed", "op", "Consume", "item_id", itemID, "err", err.Error(), "err_type", errTags["err_type"])
		return Item{}, err
	}
	s.obs.Observe("item.consume.duration_ms", dur, tags)
	s.obs.Inc("item.consume.success", tags)
	s.obs.LogInfo("consume ok", "op", "Consume", "item_id", itemID, "consumer_type", consumerType, "mark_consumed", markStr)
	s.autoTrace(ctx, wo, "consume", map[string]any{"item_id": itemID})
	return item, nil
}

// ReplaceItem opens a new revision of prevItemID. The previous item is flipped
// to status=superseded/IsLatest=false, a new item is inserted with
// Revision=prev.Revision+1, IsLatest=true, RevisionOf=prev.ID. The flip and
// insert happen atomically inside the store.
func (s *Service) ReplaceItem(ctx context.Context, callerID, prevItemID string, req StoreRequest, writeOpts ...WriteOption) (Item, error) {
	wo := resolveWriteOpts(writeOpts)
	tags := map[string]string{"kind": req.Kind}
	s.obs.Inc("item.replace.attempt", tags)
	start := time.Now()

	newItem, err := func() (Item, error) {
		prev, err := s.store.GetItem(ctx, prevItemID)
		if err != nil {
			return Item{}, err
		}
		if !prev.IsLatest {
			return Item{}, ErrConflict
		}
		b, err := s.store.GetBox(ctx, prev.BoxID)
		if err != nil {
			return Item{}, err
		}
		if b.Status != "active" {
			return Item{}, ErrConflict
		}
		if b.OwnerID != callerID {
			return Item{}, forbiddenCallerMismatch(callerID, b.OwnerID)
		}
		kind := req.Kind
		if kind == "" {
			kind = prev.Kind
		} else if kind != prev.Kind {
			return Item{}, fmt.Errorf("%w: kind mismatch (prev=%q, new=%q)", ErrValidation, prev.Kind, kind)
		}
		sourceType := req.SourceType
		if sourceType == "" {
			sourceType = prev.SourceType
		}
		format := req.Format
		if format == "" {
			format = "json"
		}
		if !formatAllowed(b.StoragePolicy, format) {
			return Item{}, fmt.Errorf("%w: format %q not allowed", ErrValidation, format)
		}
		if err := ValidateStorageURI(req.StorageURI); err != nil {
			return Item{}, err
		}
		if err := ValidateLabels(req.Labels); err != nil {
			return Item{}, err
		}
		if err := ValidateSymbols(req.Symbols); err != nil {
			return Item{}, err
		}
		idemKey := req.IdemKey
		if idemKey == "" {
			newRev := prev.Revision + 1
			idemKey = fmt.Sprintf("%s/r%d", prev.IdemKey, newRev)
		}
		content := req.Content
		if len(content) == 0 {
			content = json.RawMessage(`null`)
		}
		if b.StoragePolicy.MaxContentBytes > 0 && len(content) > b.StoragePolicy.MaxContentBytes {
			return Item{}, fmt.Errorf("%w: content size %d exceeds max %d", ErrValidation, len(content), b.StoragePolicy.MaxContentBytes)
		}
		newItem := Item{
			ID:          NewID("item_"),
			BoxID:       prev.BoxID,
			IdemKey:     idemKey,
			Kind:        kind,
			SourceType:  sourceType,
			SourceRef:   cloneMap(req.SourceRef),
			Labels:      cloneMap(req.Labels),
			LocationID:  req.LocationID,
			StorageURI:  req.StorageURI,
			Format:      format,
			Content:     content,
			ContentHash: ContentHash(content),
			Metadata:    cloneMap(req.Metadata),
			Status:      "available",
			StoredBy:    callerID,
			StoredAt:    nowUTC(),
			RevisionOf:  prev.ID,
			Revision:    prev.Revision + 1,
			IsLatest:    true,
			Symbols:     cloneSymbols(req.Symbols),
		}
		// Re-set the tag's kind to the resolved kind for the success branch.
		tags["kind"] = kind
		return s.store.ReplaceItem(ctx, prevItemID, newItem)
	}()

	dur := float64(time.Since(start).Milliseconds())
	if err != nil {
		errTags := cloneTags(tags)
		errTags["err_type"] = classifyErr(err)
		s.obs.Observe("item.replace.duration_ms", dur, errTags)
		s.obs.Inc("item.replace.error", errTags)
		s.obs.LogWarn("replace failed", "op", "ReplaceItem", "prev_id", prevItemID, "err", err.Error(), "err_type", errTags["err_type"])
		return Item{}, err
	}
	s.obs.Observe("item.replace.duration_ms", dur, tags)
	s.obs.Inc("item.replace.success", tags)
	s.obs.Observe("item.replace.revision", float64(newItem.Revision), map[string]string{"kind": newItem.Kind})
	s.obs.LogInfo("item replaced", "op", "ReplaceItem", "item_id", newItem.ID, "prev_id", prevItemID, "revision", newItem.Revision, "kind", newItem.Kind)
	s.autoTrace(ctx, wo, "replace", map[string]any{"item_id": newItem.ID, "prev_id": prevItemID})
	return newItem, nil
}

// UpdateLabelsOption is retained as an alias for WriteOption so legacy call
// sites (CLI, MCP server, existing tests) keep compiling. Newer call sites
// should prefer WriteOption directly.
type UpdateLabelsOption = WriteOption

// WithAllowHistory permits a label patch on a historical (IsLatest=false) item.
// Without this option, label-mutating calls on historical revisions return
// ErrConflict — D#5.
func WithAllowHistory(b bool) WriteOption {
	return func(o *writeOpts) { o.allowHistory = b }
}

// UpdateLabels overwrites the labels on an existing item without opening a new
// revision and without touching content/storage_uri/content_hash.
//
// Default behaviour rejects patches on historical (IsLatest=false) items with
// ErrConflict. Pass WithAllowHistory(true) to opt in.
func (s *Service) UpdateLabels(ctx context.Context, callerID, itemID string, labels map[string]string, opts ...UpdateLabelsOption) (Item, error) {
	o := writeOpts{}
	for _, opt := range opts {
		opt(&o)
	}
	tags := map[string]string{}
	s.obs.Inc("item.update_labels.attempt", tags)
	start := time.Now()

	out, err := func() (Item, error) {
		item, err := s.store.GetItem(ctx, itemID)
		if err != nil {
			return Item{}, err
		}
		b, err := s.store.GetBox(ctx, item.BoxID)
		if err != nil {
			return Item{}, err
		}
		if b.OwnerID != callerID {
			return Item{}, forbiddenCallerMismatch(callerID, b.OwnerID)
		}
		if !item.IsLatest && !o.allowHistory {
			return Item{}, fmt.Errorf("%w: cannot patch labels on non-latest revision; use WithAllowHistory if intentional", ErrConflict)
		}
		if err := ValidateLabels(labels); err != nil {
			return Item{}, err
		}
		return s.store.UpdateLabels(ctx, itemID, labels)
	}()

	dur := float64(time.Since(start).Milliseconds())
	if err != nil {
		errTags := cloneTags(tags)
		errTags["err_type"] = classifyErr(err)
		s.obs.Observe("item.update_labels.duration_ms", dur, errTags)
		s.obs.Inc("item.update_labels.error", errTags)
		s.obs.LogWarn("update_labels failed", "op", "UpdateLabels", "item_id", itemID, "err", err.Error(), "err_type", errTags["err_type"])
		return Item{}, err
	}
	s.obs.Observe("item.update_labels.duration_ms", dur, tags)
	s.obs.Inc("item.update_labels.success", tags)
	s.obs.LogInfo("labels updated", "op", "UpdateLabels", "item_id", itemID)
	s.autoTrace(ctx, o, "tag", map[string]any{"item_id": itemID})
	return out, nil
}

// MergeLabels merges patch into the item's labels (overwriting same keys; not
// deleting other keys). A patch value of "" is still written (empty values are
// a valid label semantic — use RemoveLabels to delete a key).
//
// Default behaviour rejects patches on historical items; pass
// WithAllowHistory(true) to opt in.
func (s *Service) MergeLabels(ctx context.Context, callerID, itemID string, patch map[string]string, opts ...UpdateLabelsOption) (Item, error) {
	o := writeOpts{}
	for _, opt := range opts {
		opt(&o)
	}
	tags := map[string]string{}
	s.obs.Inc("item.merge_labels.attempt", tags)
	start := time.Now()

	out, err := func() (Item, error) {
		item, err := s.store.GetItem(ctx, itemID)
		if err != nil {
			return Item{}, err
		}
		b, err := s.store.GetBox(ctx, item.BoxID)
		if err != nil {
			return Item{}, err
		}
		if b.OwnerID != callerID {
			return Item{}, forbiddenCallerMismatch(callerID, b.OwnerID)
		}
		if !item.IsLatest && !o.allowHistory {
			return Item{}, fmt.Errorf("%w: cannot patch labels on non-latest revision; use WithAllowHistory if intentional", ErrConflict)
		}
		merged := cloneMap(item.Labels)
		for k, v := range patch {
			merged[k] = v
		}
		if err := ValidateLabels(merged); err != nil {
			return Item{}, err
		}
		return s.store.UpdateLabels(ctx, itemID, merged)
	}()

	dur := float64(time.Since(start).Milliseconds())
	if err != nil {
		errTags := cloneTags(tags)
		errTags["err_type"] = classifyErr(err)
		s.obs.Observe("item.merge_labels.duration_ms", dur, errTags)
		s.obs.Inc("item.merge_labels.error", errTags)
		s.obs.LogWarn("merge_labels failed", "op", "MergeLabels", "item_id", itemID, "err", err.Error(), "err_type", errTags["err_type"])
		return Item{}, err
	}
	s.obs.Observe("item.merge_labels.duration_ms", dur, tags)
	s.obs.Inc("item.merge_labels.success", tags)
	s.obs.LogInfo("labels merged", "op", "MergeLabels", "item_id", itemID)
	s.autoTrace(ctx, o, "tag_merge", map[string]any{"item_id": itemID})
	return out, nil
}

// RemoveLabels deletes the given keys from the item's labels. Missing keys are
// silently skipped (idempotent).
//
// Default behaviour rejects mutation on historical items; pass
// WithAllowHistory(true) to opt in.
func (s *Service) RemoveLabels(ctx context.Context, callerID, itemID string, keys []string, opts ...UpdateLabelsOption) (Item, error) {
	o := writeOpts{}
	for _, opt := range opts {
		opt(&o)
	}
	tags := map[string]string{}
	s.obs.Inc("item.remove_labels.attempt", tags)
	start := time.Now()

	out, err := func() (Item, error) {
		item, err := s.store.GetItem(ctx, itemID)
		if err != nil {
			return Item{}, err
		}
		b, err := s.store.GetBox(ctx, item.BoxID)
		if err != nil {
			return Item{}, err
		}
		if b.OwnerID != callerID {
			return Item{}, forbiddenCallerMismatch(callerID, b.OwnerID)
		}
		if !item.IsLatest && !o.allowHistory {
			return Item{}, fmt.Errorf("%w: cannot patch labels on non-latest revision; use WithAllowHistory if intentional", ErrConflict)
		}
		next := cloneMap(item.Labels)
		for _, k := range keys {
			delete(next, k)
		}
		if err := ValidateLabels(next); err != nil {
			return Item{}, err
		}
		return s.store.UpdateLabels(ctx, itemID, next)
	}()

	dur := float64(time.Since(start).Milliseconds())
	if err != nil {
		errTags := cloneTags(tags)
		errTags["err_type"] = classifyErr(err)
		s.obs.Observe("item.remove_labels.duration_ms", dur, errTags)
		s.obs.Inc("item.remove_labels.error", errTags)
		s.obs.LogWarn("remove_labels failed", "op", "RemoveLabels", "item_id", itemID, "err", err.Error(), "err_type", errTags["err_type"])
		return Item{}, err
	}
	s.obs.Observe("item.remove_labels.duration_ms", dur, tags)
	s.obs.Inc("item.remove_labels.success", tags)
	s.obs.LogInfo("labels removed", "op", "RemoveLabels", "item_id", itemID)
	s.autoTrace(ctx, o, "tag_remove", map[string]any{"item_id": itemID})
	return out, nil
}

// ListConsumes returns all ConsumeLog entries for the given item, ordered by
// ConsumedAt ascending (insertion order). callerID must be the owner of the
// box that holds the item; otherwise ErrForbidden.
func (s *Service) ListConsumes(ctx context.Context, callerID, itemID string) ([]ConsumeLog, error) {
	tags := map[string]string{}
	s.obs.Inc("item.list_consumes.attempt", tags)
	start := time.Now()

	out, err := func() ([]ConsumeLog, error) {
		item, err := s.store.GetItem(ctx, itemID)
		if err != nil {
			return nil, err
		}
		b, err := s.store.GetBox(ctx, item.BoxID)
		if err != nil {
			return nil, err
		}
		if b.OwnerID != callerID {
			return nil, forbiddenCallerMismatch(callerID, b.OwnerID)
		}
		return s.store.ListConsumes(ctx, itemID)
	}()

	dur := float64(time.Since(start).Milliseconds())
	if err != nil {
		errTags := cloneTags(tags)
		errTags["err_type"] = classifyErr(err)
		s.obs.Observe("item.list_consumes.duration_ms", dur, errTags)
		s.obs.Inc("item.list_consumes.error", errTags)
		s.obs.LogWarn("list_consumes failed", "op", "ListConsumes", "item_id", itemID, "err", err.Error(), "err_type", errTags["err_type"])
		return nil, err
	}
	s.obs.Observe("item.list_consumes.duration_ms", dur, tags)
	s.obs.Inc("item.list_consumes.success", tags)
	s.obs.LogInfo("list_consumes ok", "op", "ListConsumes", "item_id", itemID, "count", len(out))
	return out, nil
}

// GetBox resolves a Box by its primary ID. callerID is a placeholder for
// future authorization; the lookup itself is currently not gated.
func (s *Service) GetBox(ctx context.Context, callerID, boxID string) (Box, error) {
	tags := map[string]string{}
	s.obs.Inc("box.get.attempt", tags)
	start := time.Now()

	b, err := func() (Box, error) {
		if boxID == "" {
			return Box{}, fmt.Errorf("%w: box_id is required", ErrValidation)
		}
		return s.store.GetBox(ctx, boxID)
	}()

	dur := float64(time.Since(start).Milliseconds())
	if err != nil {
		errTags := cloneTags(tags)
		errTags["err_type"] = classifyErr(err)
		s.obs.Observe("box.get.duration_ms", dur, errTags)
		s.obs.Inc("box.get.error", errTags)
		s.obs.LogWarn("get box failed", "op", "GetBox", "box_id", boxID, "err", err.Error(), "err_type", errTags["err_type"])
		return Box{}, err
	}
	s.obs.Observe("box.get.duration_ms", dur, tags)
	s.obs.Inc("box.get.success", tags)
	s.obs.LogInfo("box resolved", "op", "GetBox", "box_id", boxID)
	return b, nil
}

// DeleteItem soft-deletes an item (Status=deleted, IsLatest=false). Returns
// ErrForbidden when caller is not the owning box's OwnerID, ErrNotFound if the
// item is absent, ErrConflict if already deleted. Historical (non-latest)
// versions may also be deleted. Browse and GetItem will then hide the item.
func (s *Service) DeleteItem(ctx context.Context, callerID, itemID string, writeOpts ...WriteOption) (Item, error) {
	wo := resolveWriteOpts(writeOpts)
	tags := map[string]string{}
	s.obs.Inc("item.delete.attempt", tags)
	start := time.Now()

	out, err := func() (Item, error) {
		// store.GetItem filters deleted/expired but keeps superseded (non-latest)
		// visible, which lets us validate caller ownership on both latest and
		// historical versions. If GetItem returns ErrNotFound the item is either
		// absent or already deleted; let store.DeleteItem disambiguate
		// (NotFound vs Conflict) — in that case we skip the owner check because
		// either nothing exists to act on, or the prior owning-caller already
		// passed the check during the first delete.
		item, err := s.store.GetItem(ctx, itemID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return s.store.DeleteItem(ctx, itemID)
			}
			return Item{}, err
		}
		b, err := s.store.GetBox(ctx, item.BoxID)
		if err != nil {
			return Item{}, err
		}
		if b.OwnerID != callerID {
			return Item{}, forbiddenCallerMismatch(callerID, b.OwnerID)
		}
		return s.store.DeleteItem(ctx, itemID)
	}()

	dur := float64(time.Since(start).Milliseconds())
	if err != nil {
		errTags := cloneTags(tags)
		errTags["err_type"] = classifyErr(err)
		s.obs.Observe("item.delete.duration_ms", dur, errTags)
		s.obs.Inc("item.delete.error", errTags)
		s.obs.LogWarn("delete failed", "op", "DeleteItem", "item_id", itemID, "err", err.Error(), "err_type", errTags["err_type"])
		return Item{}, err
	}
	s.obs.Observe("item.delete.duration_ms", dur, tags)
	s.obs.Inc("item.delete.success", tags)
	s.obs.LogInfo("item deleted", "op", "DeleteItem", "item_id", itemID)
	s.autoTrace(ctx, wo, "delete", map[string]any{"item_id": itemID})
	return out, nil
}

func (s *Service) Summary(ctx context.Context, boxID string) (Summary, error) {
	tags := map[string]string{}
	s.obs.Inc("box.summary.attempt", tags)
	start := time.Now()

	out, err := func() (Summary, error) {
		items, err := s.store.Browse(ctx, boxID, BrowseFilter{Limit: 1_000_000})
		if err != nil {
			return Summary{}, err
		}
		out := Summary{
			BoxID:        boxID,
			TotalItems:   len(items),
			ByKind:       map[string]int{},
			BySourceType: map[string]int{},
		}
		for _, item := range items {
			out.ByKind[item.Kind]++
			out.BySourceType[item.SourceType]++
			if out.LatestStoredAt == nil || item.StoredAt.After(*out.LatestStoredAt) {
				t := item.StoredAt
				out.LatestStoredAt = &t
			}
		}
		return out, nil
	}()

	dur := float64(time.Since(start).Milliseconds())
	if err != nil {
		errTags := cloneTags(tags)
		errTags["err_type"] = classifyErr(err)
		s.obs.Observe("box.summary.duration_ms", dur, errTags)
		s.obs.Inc("box.summary.error", errTags)
		s.obs.LogWarn("summary failed", "op", "Summary", "box_id", boxID, "err", err.Error(), "err_type", errTags["err_type"])
		return Summary{}, err
	}
	s.obs.Observe("box.summary.duration_ms", dur, tags)
	s.obs.Inc("box.summary.success", tags)
	s.obs.LogInfo("summary ok", "op", "Summary", "box_id", boxID, "total_items", out.TotalItems)
	return out, nil
}

// Overview returns the R5.1 cross-box geo-globe view: axis × zoom × filter.
// Caller-scoping is enforced inline (non-caller-owned boxes never surface;
// they are not Forbidden, they are simply absent — R5.1 D#4).
func (s *Service) Overview(ctx context.Context, callerID string, req OverviewRequest) (Overview, error) {
	tags := map[string]string{"axis": req.Axis, "zoom": fmt.Sprintf("%d", req.Zoom)}
	s.obs.Inc("box.overview.attempt", tags)
	start := time.Now()

	out, err := func() (Overview, error) {
		if req.Axis == "" {
			return Overview{}, fmt.Errorf("%w: axis is required", ErrValidation)
		}
		if req.Zoom < 0 || req.Zoom > 1 {
			return Overview{}, fmt.Errorf("%w: zoom must be 0 or 1 (zoom=2 deferred to R6)", ErrValidation)
		}
		if !isOverviewAxis(req.Axis) {
			return Overview{}, fmt.Errorf("%w: unsupported axis %q (want owner|status|label:<key>)", ErrValidation, req.Axis)
		}

		boxes, err := s.store.ListBoxes(ctx, req.Filter)
		if err != nil {
			return Overview{}, err
		}
		// Caller-scoping (R5.1 D#4): drop boxes the caller does not own. We
		// intentionally do not special-case empty callerID — the Service's
		// other methods treat callerID="" as "matches nothing" and we follow
		// the same convention here for the aggregate read.
		owned := boxes[:0]
		for _, b := range boxes {
			if b.OwnerID == callerID {
				owned = append(owned, b)
			}
		}

		ov := Overview{Axis: req.Axis, Zoom: req.Zoom, Total: len(owned)}
		if req.Zoom == 0 {
			ov.Histogram = make(map[string]int, len(owned))
			for _, b := range owned {
				ov.Histogram[overviewAxisKey(req.Axis, b)]++
			}
			return ov, nil
		}
		// zoom == 1
		groupIdx := make(map[string]int)
		for _, b := range owned {
			key := overviewAxisKey(req.Axis, b)
			idx, ok := groupIdx[key]
			if !ok {
				ov.Groups = append(ov.Groups, OverviewGroup{Key: key})
				idx = len(ov.Groups) - 1
				groupIdx[key] = idx
			}
			ov.Groups[idx].Count++
			if len(ov.Groups[idx].Boxes) < 10 { // D#1: per-group cap
				ov.Groups[idx].Boxes = append(ov.Groups[idx].Boxes, s.glyphOf(ctx, b))
			}
		}
		return ov, nil
	}()

	dur := float64(time.Since(start).Milliseconds())
	if err != nil {
		errTags := cloneTags(tags)
		errTags["err_type"] = classifyErr(err)
		s.obs.Observe("box.overview.duration_ms", dur, errTags)
		s.obs.Inc("box.overview.error", errTags)
		s.obs.LogWarn("overview failed", "op", "Overview", "axis", req.Axis, "zoom", req.Zoom, "err", err.Error(), "err_type", errTags["err_type"])
		return Overview{}, err
	}
	s.obs.Observe("box.overview.duration_ms", dur, tags)
	s.obs.Inc("box.overview.success", tags)
	s.obs.LogInfo("overview ok", "op", "Overview", "axis", req.Axis, "zoom", req.Zoom, "total", out.Total)
	return out, nil
}

// isOverviewAxis reports whether axis is one of the three accepted shapes:
// "owner", "status", or "label:<key>" (non-empty key).
func isOverviewAxis(axis string) bool {
	switch axis {
	case "owner", "status":
		return true
	}
	if strings.HasPrefix(axis, "label:") && len(axis) > len("label:") {
		return true
	}
	return false
}

// overviewAxisKey returns the bucket key for box b under the requested axis.
// For label:<K> a missing/empty value is bucketed under "(unset)".
func overviewAxisKey(axis string, b Box) string {
	switch axis {
	case "owner":
		return b.OwnerID
	case "status":
		return b.Status
	}
	if strings.HasPrefix(axis, "label:") {
		k := axis[len("label:"):]
		v := b.Labels[k]
		if v == "" {
			return "(unset)"
		}
		return v
	}
	return ""
}

// glyphOf builds a BoxGlyph for zoom=1 group entries. Items count + latest
// stored_at come from a cheap Browse(latest-only) — boxes with zero items
// produce Items=0 / Latest=zero, which is the desired shape.
func (s *Service) glyphOf(ctx context.Context, b Box) BoxGlyph {
	g := BoxGlyph{
		Glyph:  boxGlyphRune(b.Status),
		Key:    b.Key,
		ID:     b.ID,
		Status: b.Status,
	}
	if len(b.Labels) > 0 {
		keys := make([]string, 0, len(b.Labels))
		for k := range b.Labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) > 3 {
			keys = keys[:3]
		}
		g.LabelsTop = keys
	}
	items, err := s.store.Browse(ctx, b.ID, BrowseFilter{Limit: 1_000_000})
	if err == nil {
		g.Items = len(items)
		if len(items) > 0 {
			// Browse returns latest-StoredAt-first (see MemoryStore.Browse),
			// so items[0] carries the freshest timestamp.
			g.Latest = items[0].StoredAt
		}
	}
	return g
}

// boxGlyphRune maps a Box.Status to its visual literal (R5.1 hard constraint
// #3 — dual-load: every glyph also carries the parallel string Status field).
func boxGlyphRune(status string) string {
	switch status {
	case "active":
		return "◐"
	case "sealed":
		return "◼"
	}
	return "○"
}

// Trace queries items whose Symbols match query. boxKey == "" walks every
// box. callerID is currently used only for telemetry/log enrichment; the
// per-box owner check is deferred to R0.7.4.
func (s *Service) Trace(ctx context.Context, callerID, boxKey string, query SymbolQuery) ([]Item, error) {
	valueCount := fmt.Sprintf("%d", len(query.Value))
	boxScope := boxKey
	if boxScope == "" {
		boxScope = "*"
	}
	kindTag := ""
	if len(query.Kind) > 0 {
		kindTag = string(query.Kind[0])
	}
	tags := map[string]string{
		"kind":        kindTag,
		"value_count": valueCount,
		"box_scope":   boxScope,
	}
	s.obs.Inc("box.trace.attempt", tags)
	start := time.Now()

	items, err := func() ([]Item, error) {
		var boxID string
		if boxKey != "" {
			b, err := s.store.GetBoxByKey(ctx, boxKey)
			if err != nil {
				return nil, err
			}
			boxID = b.ID
		}
		return s.store.Trace(ctx, boxID, query)
	}()

	dur := float64(time.Since(start).Milliseconds())
	if err != nil {
		errTags := cloneTags(tags)
		errTags["err_type"] = classifyErr(err)
		s.obs.Observe("box.trace.duration_ms", dur, errTags)
		s.obs.Inc("box.trace.error", errTags)
		s.obs.LogWarn("trace failed", "op", "Trace", "box_key", boxKey, "err", err.Error(), "err_type", errTags["err_type"])
		return nil, err
	}
	s.obs.Observe("box.trace.duration_ms", dur, tags)
	s.obs.Inc("box.trace.success", tags)
	s.obs.LogInfo("trace ok", "op", "Trace", "box_key", boxKey, "result_count", len(items))
	return items, nil
}

// Neighbors returns the hops-bounded subgraph centered on itemID. hops must
// be in [1,5]; outside that range, ErrValidation. Same-box only — cross-box
// ref resolution is deferred to R0.7.4.
func (s *Service) Neighbors(ctx context.Context, callerID, itemID string, hops int) (Subgraph, error) {
	tags := map[string]string{"hops": fmt.Sprintf("%d", hops)}
	s.obs.Inc("box.neighbors.attempt", tags)
	start := time.Now()

	sub, err := func() (Subgraph, error) {
		if hops < 1 || hops > 5 {
			return Subgraph{}, fmt.Errorf("%w: hops must be in [1,5], got %d", ErrValidation, hops)
		}
		if itemID == "" {
			return Subgraph{}, fmt.Errorf("%w: item_id is required", ErrValidation)
		}
		return s.store.Neighbors(ctx, itemID, hops)
	}()

	dur := float64(time.Since(start).Milliseconds())
	if err != nil {
		errTags := cloneTags(tags)
		errTags["err_type"] = classifyErr(err)
		s.obs.Observe("box.neighbors.duration_ms", dur, errTags)
		s.obs.Inc("box.neighbors.error", errTags)
		s.obs.LogWarn("neighbors failed", "op", "Neighbors", "item_id", itemID, "err", err.Error(), "err_type", errTags["err_type"])
		return Subgraph{}, err
	}
	s.obs.Observe("box.neighbors.duration_ms", dur, tags)
	s.obs.Inc("box.neighbors.success", tags)
	s.obs.LogInfo("neighbors ok", "op", "Neighbors", "item_id", itemID, "nodes", len(sub.Nodes), "edges", len(sub.Edges))
	return sub, nil
}

// LegendOf returns the documentation Item for a given Symbol from the
// built-in __symbols__ box. EnsureSymbolBootstrap must be invoked at startup
// to populate the box; otherwise the lookup returns ErrNotFound.
func (s *Service) LegendOf(ctx context.Context, callerID string, sym Symbol) (Item, error) {
	tags := map[string]string{"sym_kind": string(sym.Kind)}
	s.obs.Inc("box.legend.attempt", tags)
	start := time.Now()

	out, err := func() (Item, error) {
		box, err := s.store.GetBoxByKey(ctx, symbolsBoxKey)
		if err != nil {
			return Item{}, err
		}
		idem := "symbol/" + sym.Value
		items, err := s.store.Browse(ctx, box.ID, BrowseFilter{Limit: 1_000_000})
		if err != nil {
			return Item{}, err
		}
		for _, it := range items {
			if it.IdemKey == idem {
				return it, nil
			}
		}
		return Item{}, ErrNotFound
	}()

	dur := float64(time.Since(start).Milliseconds())
	if err != nil {
		errTags := cloneTags(tags)
		errTags["err_type"] = classifyErr(err)
		s.obs.Observe("box.legend.duration_ms", dur, errTags)
		s.obs.Inc("box.legend.error", errTags)
		s.obs.LogWarn("legend failed", "op", "LegendOf", "sym_kind", string(sym.Kind), "err", err.Error(), "err_type", errTags["err_type"])
		return Item{}, err
	}
	s.obs.Observe("box.legend.duration_ms", dur, tags)
	s.obs.Inc("box.legend.success", tags)
	s.obs.LogInfo("legend ok", "op", "LegendOf", "sym_kind", string(sym.Kind), "value", sym.Value)
	return out, nil
}

// symbolsBoxKey is the well-known key for the built-in symbols box.
const symbolsBoxKey = "__symbols__"

// EnsureSymbolBootstrap is idempotent: creates the __symbols__ box if absent
// and inserts one Item per built-in symbol (SymbolDefinitions). Subsequent
// runs are no-ops thanks to InsertItem's byIdem dedup.
//
// CLI/main wires this explicitly — NewService does NOT auto-call it to keep
// the constructor side-effect-free (and to keep tests in control of state).
func (s *Service) EnsureSymbolBootstrap(ctx context.Context) error {
	box, err := s.store.GetBoxByKey(ctx, symbolsBoxKey)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		box, err = s.store.CreateBox(ctx, Box{
			ID:        NewID("box_"),
			Key:       symbolsBoxKey,
			Version:   1,
			OwnerType: "standalone",
			OwnerID:   "system",
			StoragePolicy: StoragePolicy{
				AllowedFormats:  []string{"json", "text", "markdown"},
				MaxItems:        10000,
				MaxContentBytes: 8192,
			},
			Status:    "active",
			CreatedAt: nowUTC(),
		})
		if err != nil {
			return err
		}
	}
	for _, def := range SymbolDefinitions {
		idem := "symbol/" + def.Value
		payload := map[string]any{
			"value":    def.Value,
			"kind":     string(def.Kind),
			"meaning":  def.Meaning,
			"examples": def.Examples,
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		_, err = s.store.InsertItem(ctx, Item{
			ID:         NewID("item_"),
			BoxID:      box.ID,
			IdemKey:    idem,
			Kind:       "symbol",
			SourceType: "bootstrap",
			Labels: map[string]string{
				"__sem:system": "core",
				"__sem:kind":   string(def.Kind),
			},
			StorageURI:  "folder://symbols/" + idem,
			Format:      "json",
			Content:     body,
			ContentHash: ContentHash(body),
			Status:      "available",
			StoredBy:    "system",
			StoredAt:    nowUTC(),
			Revision:    1,
			IsLatest:    true,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// CreateTask materialises a kind="task" Item. After R0.13.2 this is just
// ergonomic sugar — "task" carries no special privileges anywhere else in
// the Service (trace/get/set all work on any kind). The function survives
// because StartYiCheng and the CLI/MCP convenience surface still default
// to this shape; agents writing direct callers may use Store with their
// own kind name instead.
//
// Validation is intentionally shallow: Intent + Goal + NailChain + NailDag
// structure only. PassCriteria flows through as opaque JSON; Box does not
// look inside (R0.13.2 removed the closed-set Kind whitelist that lingered
// from R0.10 — see invariant #10).
func (s *Service) CreateTask(ctx context.Context, callerID, boxID string, req CreateTaskRequest) (Item, error) {
	tags := map[string]string{}
	s.obs.Inc("task.create.attempt", tags)
	start := time.Now()

	item, err := func() (Item, error) {
		if req.Intent == "" {
			return Item{}, fmt.Errorf("%w: intent is required", ErrValidation)
		}
		if err := validateGoalSymbols(req.Goal); err != nil {
			return Item{}, err
		}
		if err := validateNailChain(req.NailChain); err != nil {
			return Item{}, err
		}
		if err := validateNailDag(req.NailDag); err != nil {
			return Item{}, err
		}
		// Source mirrors Goal's "no SymKind required" relaxation: a source
		// list may be e.g. [{kind:topic, value:"billing"}] only. Each entry
		// is shape-checked; the SymKind insistence of ValidateSymbols does
		// not apply here.
		for _, sym := range req.Source {
			if err := ValidateSymbol(sym); err != nil {
				return Item{}, err
			}
		}
		// pass_criteria, when present, must at least be valid JSON.
		var passField any
		if len(req.PassCriteria) > 0 {
			if err := json.Unmarshal(req.PassCriteria, &passField); err != nil {
				return Item{}, fmt.Errorf("%w: pass_criteria is not valid JSON: %v", ErrValidation, err)
			}
		}
		b, err := s.store.GetBox(ctx, boxID)
		if err != nil {
			return Item{}, err
		}
		if b.Status != "active" {
			return Item{}, ErrConflict
		}
		if b.OwnerID != callerID {
			return Item{}, forbiddenCallerMismatch(callerID, b.OwnerID)
		}
		count, err := s.store.CountItems(ctx, boxID)
		if err != nil {
			return Item{}, err
		}
		if count >= b.StoragePolicy.MaxItems {
			return Item{}, ErrConflict
		}
		// Assemble the task content payload. Box stores it verbatim and
		// never reads pass_criteria/NailChain/Goal back during normal
		// operation (invariant #10).
		payload := map[string]any{
			"intent":        req.Intent,
			"source":        req.Source,
			"goal":          req.Goal,
			"pass_criteria": passField,
			"nail_chain":    req.NailChain,
			"nail_dag":      req.NailDag,
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return Item{}, fmt.Errorf("%w: marshal task content: %v", ErrValidation, err)
		}
		if b.StoragePolicy.MaxContentBytes > 0 && len(body) > b.StoragePolicy.MaxContentBytes {
			return Item{}, fmt.Errorf("%w: content size %d exceeds max %d", ErrValidation, len(body), b.StoragePolicy.MaxContentBytes)
		}
		itemID := NewID("item_")
		idem := req.Intent + "/" + itemID
		symbols := []Symbol{
			{Kind: SymKind, Value: "T"},
			{Kind: SymStatus, Value: "?"},
		}
		return s.store.InsertItem(ctx, Item{
			ID:          itemID,
			BoxID:       boxID,
			IdemKey:     idem,
			Kind:        "task",
			SourceType:  "task",
			StorageURI:  "row://tasks/" + itemID,
			Format:      "json",
			Content:     body,
			ContentHash: ContentHash(body),
			Status:      "available",
			StoredBy:    callerID,
			StoredAt:    nowUTC(),
			Revision:    1,
			IsLatest:    true,
			Symbols:     symbols,
		})
	}()

	dur := float64(time.Since(start).Milliseconds())
	if err != nil {
		errTags := cloneTags(tags)
		errTags["err_type"] = classifyErr(err)
		s.obs.Observe("task.create.duration_ms", dur, errTags)
		s.obs.Inc("task.create.error", errTags)
		s.obs.LogWarn("create_task failed", "op", "CreateTask", "box_id", boxID, "err", err.Error(), "err_type", errTags["err_type"])
		return Item{}, err
	}
	s.obs.Observe("task.create.duration_ms", dur, tags)
	s.obs.Inc("task.create.success", tags)
	s.obs.LogInfo("task created", "op", "CreateTask", "box_id", boxID, "item_id", item.ID)
	return item, nil
}

// SetItemSymbols overwrites the Symbols field of an item without opening a
// new revision (mirrors UpdateLabels). It is the canonical way to flip task
// status (e.g. add {kind:status, value:"✓"}); Box does NOT do that on its
// own (invariant #10).
//
// Default behaviour rejects mutation on historical (IsLatest=false) items
// with ErrConflict — pass WithAllowHistory(true) to opt in (mirroring
// UpdateLabels).
func (s *Service) SetItemSymbols(ctx context.Context, callerID, itemID string, symbols []Symbol, opts ...UpdateLabelsOption) (Item, error) {
	o := writeOpts{}
	for _, opt := range opts {
		opt(&o)
	}
	tags := map[string]string{}
	s.obs.Inc("item.set_symbols.attempt", tags)
	start := time.Now()

	out, err := func() (Item, error) {
		if err := ValidateSymbols(symbols); err != nil {
			return Item{}, err
		}
		item, err := s.store.GetItem(ctx, itemID)
		if err != nil {
			return Item{}, err
		}
		b, err := s.store.GetBox(ctx, item.BoxID)
		if err != nil {
			return Item{}, err
		}
		if b.OwnerID != callerID {
			return Item{}, forbiddenCallerMismatch(callerID, b.OwnerID)
		}
		if !item.IsLatest && !o.allowHistory {
			return Item{}, fmt.Errorf("%w: cannot patch symbols on non-latest revision; use WithAllowHistory if intentional", ErrConflict)
		}
		return s.store.SetItemSymbols(ctx, itemID, symbols)
	}()

	dur := float64(time.Since(start).Milliseconds())
	if err != nil {
		errTags := cloneTags(tags)
		errTags["err_type"] = classifyErr(err)
		s.obs.Observe("item.set_symbols.duration_ms", dur, errTags)
		s.obs.Inc("item.set_symbols.error", errTags)
		s.obs.LogWarn("set_symbols failed", "op", "SetItemSymbols", "item_id", itemID, "err", err.Error(), "err_type", errTags["err_type"])
		return Item{}, err
	}
	s.obs.Observe("item.set_symbols.duration_ms", dur, tags)
	s.obs.Inc("item.set_symbols.success", tags)
	s.obs.LogInfo("symbols updated", "op", "SetItemSymbols", "item_id", itemID)
	s.autoTrace(ctx, o, "set_item_symbols", map[string]any{"item_id": itemID})
	return out, nil
}

// AppendEvent (R0.13.2 — was AppendTaskTrace) appends one TraceStep to the
// item's trace.jsonl. Works on items of ANY kind: trace is no longer a
// task-only privilege. Caller must own the host box and the item must be
// IsLatest=true (path-ledger semantics: events attach to the current cursor,
// not to historical revisions).
//
// Step.Step is reassigned by the store to "current length". Step.AppendedAt
// is overwritten to nowUTC().
func (s *Service) AppendEvent(ctx context.Context, callerID, itemID string, step TraceStep) error {
	tags := map[string]string{}
	s.obs.Inc("event.append.attempt", tags)
	start := time.Now()

	err := func() error {
		item, err := s.store.GetItem(ctx, itemID)
		if err != nil {
			return err
		}
		b, err := s.store.GetBox(ctx, item.BoxID)
		if err != nil {
			return err
		}
		if b.OwnerID != callerID {
			return forbiddenCallerMismatch(callerID, b.OwnerID)
		}
		if !item.IsLatest {
			return fmt.Errorf("%w: cannot append event to non-latest revision of item %s", ErrConflict, itemID)
		}
		step.AppendedAt = nowUTC()
		return s.store.AppendTrace(ctx, itemID, step)
	}()

	dur := float64(time.Since(start).Milliseconds())
	if err != nil {
		errTags := cloneTags(tags)
		errTags["err_type"] = classifyErr(err)
		s.obs.Observe("event.append.duration_ms", dur, errTags)
		s.obs.Inc("event.append.error", errTags)
		s.obs.LogWarn("append_event failed", "op", "AppendEvent", "item_id", itemID, "err", err.Error(), "err_type", errTags["err_type"])
		return err
	}
	s.obs.Observe("event.append.duration_ms", dur, tags)
	s.obs.Inc("event.append.success", tags)
	s.obs.LogInfo("event appended", "op", "AppendEvent", "item_id", itemID, "nail_ref", step.NailRef, "tstep_op", step.Op)
	return nil
}

// ListEvents (R0.13.2 — was ListTaskTrace) returns the item's full event
// history in append order. Works on items of ANY kind. Pure read (no caller
// gating — mirrors Browse's loose read posture).
func (s *Service) ListEvents(ctx context.Context, callerID, itemID string) ([]TraceStep, error) {
	tags := map[string]string{}
	s.obs.Inc("event.list.attempt", tags)
	start := time.Now()

	out, err := func() ([]TraceStep, error) {
		if _, err := s.store.GetItem(ctx, itemID); err != nil {
			return nil, err
		}
		return s.store.ListTrace(ctx, itemID)
	}()

	dur := float64(time.Since(start).Milliseconds())
	if err != nil {
		errTags := cloneTags(tags)
		errTags["err_type"] = classifyErr(err)
		s.obs.Observe("event.list.duration_ms", dur, errTags)
		s.obs.Inc("event.list.error", errTags)
		s.obs.LogWarn("list_events failed", "op", "ListEvents", "item_id", itemID, "err", err.Error(), "err_type", errTags["err_type"])
		return nil, err
	}
	s.obs.Observe("event.list.duration_ms", dur, tags)
	s.obs.Inc("event.list.success", tags)
	s.obs.LogInfo("list_events ok", "op", "ListEvents", "item_id", itemID, "count", len(out))
	return out, nil
}

// StartYiCheng (启程) opens a new program-track session. When req.Intent is
// non-empty, a fresh task Item is created via CreateTask and the session is
// bound to it; this is the common "agent starts a new task" path. When
// req.Intent is empty AND boxID points at an existing kind=task Item id (via
// a future overload — v0.1 only supports the create-new path), the session
// attaches to that task instead.
//
// The returned token is the only handle on the session. It is held in
// process memory (sync.Map) and disappears at restart — see invariant #11.
//
// On success the task carries [{kind:T},{status:→}] and the trace.jsonl
// gets one task_start event with the token prefix.
//
// Path-ledger semantics: 启程 is an event, not a state transition. A token
// being live does NOT lock the task — anyone (including the same caller
// without a token) may still flip symbols / append trace / open another
// session. "Active token" just means "this writer's events will auto-attach
// to that task's trace".
func (s *Service) StartYiCheng(ctx context.Context, callerID, boxID string, req CreateTaskRequest) (Item, string, error) {
	tags := map[string]string{}
	s.obs.Inc("task.start.attempt", tags)
	start := time.Now()

	task, token, err := func() (Item, string, error) {
		task, err := s.CreateTask(ctx, callerID, boxID, req)
		if err != nil {
			return Item{}, "", err
		}
		// Flip status ? → → (work in progress).
		syms := []Symbol{
			{Kind: SymKind, Value: "T"},
			{Kind: SymStatus, Value: "→"},
		}
		if _, err := s.SetItemSymbols(ctx, callerID, task.ID, syms); err != nil {
			return Item{}, "", err
		}
		// Mint and register the session token.
		token, err := newYiChengToken()
		if err != nil {
			return Item{}, "", err
		}
		sess := &YiCheng{
			TaskID:    task.ID,
			CallerID:  callerID,
			CreatedAt: nowUTC(),
		}
		yiChengSessions.Store(token, sess)
		// Append the 启程 event to the task's trace.
		argPayload, _ := json.Marshal(map[string]any{
			"token_prefix": tokenPrefix(token),
			"caller_id":    callerID,
		})
		step := TraceStep{
			Op:   "task_start",
			Args: argPayload,
		}
		if err := s.AppendEvent(ctx, callerID, task.ID, step); err != nil {
			// Roll the token back so callers never see a "live" session
			// without a trace anchor.
			yiChengSessions.Delete(token)
			return Item{}, "", err
		}
		// Reload the task so the returned struct reflects the → symbol.
		refreshed, err := s.GetItem(ctx, callerID, task.ID)
		if err != nil {
			return Item{}, "", err
		}
		return refreshed, token, nil
	}()

	dur := float64(time.Since(start).Milliseconds())
	if err != nil {
		errTags := cloneTags(tags)
		errTags["err_type"] = classifyErr(err)
		s.obs.Observe("task.start.duration_ms", dur, errTags)
		s.obs.Inc("task.start.error", errTags)
		s.obs.LogWarn("task_start failed", "op", "StartYiCheng", "err", err.Error(), "err_type", errTags["err_type"])
		return Item{}, "", err
	}
	s.obs.Observe("task.start.duration_ms", dur, tags)
	s.obs.Inc("task.start.success", tags)
	s.obs.LogInfo("task started", "op", "StartYiCheng", "task_id", task.ID, "token_prefix", tokenPrefix(token))
	return task, token, nil
}

// FinishYiCheng (合程) appends a ✓ task_finish event to the bound task's
// trace, flips the task status symbol to ✓, and revokes the token. It is
// NOT a terminal state — by path-ledger semantics (invariant #12), the task
// is still mutable; anyone may continue to append events or flip the symbol
// back via SetItemSymbols. Box does not enforce "frozen after finish".
//
// statusValue defaults to "✓" when empty.
func (s *Service) FinishYiCheng(ctx context.Context, token, statusValue, summary string) (Item, error) {
	tags := map[string]string{}
	s.obs.Inc("task.finish.attempt", tags)
	start := time.Now()

	out, err := func() (Item, error) {
		if token == "" {
			return Item{}, fmt.Errorf("%w: token is required", ErrValidation)
		}
		raw, loaded := yiChengSessions.LoadAndDelete(token)
		if !loaded {
			return Item{}, fmt.Errorf("%w: unknown or expired token", ErrNotFound)
		}
		sess, ok := raw.(*YiCheng)
		if !ok || sess == nil {
			return Item{}, fmt.Errorf("%w: corrupted session record", ErrValidation)
		}
		if statusValue == "" {
			statusValue = "✓"
		}
		argPayload, _ := json.Marshal(map[string]any{
			"token_prefix": tokenPrefix(token),
			"caller_id":    sess.CallerID,
			"summary":      summary,
			"status":       statusValue,
		})
		step := TraceStep{
			Op:   "task_finish",
			Args: argPayload,
		}
		if err := s.AppendEvent(ctx, sess.CallerID, sess.TaskID, step); err != nil {
			return Item{}, err
		}
		syms := []Symbol{
			{Kind: SymKind, Value: "T"},
			{Kind: SymStatus, Value: statusValue},
		}
		if _, err := s.SetItemSymbols(ctx, sess.CallerID, sess.TaskID, syms); err != nil {
			return Item{}, err
		}
		return s.GetItem(ctx, sess.CallerID, sess.TaskID)
	}()

	dur := float64(time.Since(start).Milliseconds())
	if err != nil {
		errTags := cloneTags(tags)
		errTags["err_type"] = classifyErr(err)
		s.obs.Observe("task.finish.duration_ms", dur, errTags)
		s.obs.Inc("task.finish.error", errTags)
		s.obs.LogWarn("task_finish failed", "op", "FinishYiCheng", "err", err.Error(), "err_type", errTags["err_type"])
		return Item{}, err
	}
	s.obs.Observe("task.finish.duration_ms", dur, tags)
	s.obs.Inc("task.finish.success", tags)
	s.obs.LogInfo("task finished", "op", "FinishYiCheng", "task_id", out.ID, "token_prefix", tokenPrefix(token))
	return out, nil
}

// AbortYiCheng (断程) appends a ✗ task_abort event to the bound task's
// trace, flips the status symbol to ✗, and revokes the token. Like Finish,
// it does NOT undo any prior writes — Box has no rollback (the "no
// 复辙" rule). Callers wanting compensation must write it themselves.
//
// Calling AbortYiCheng on an already-revoked token returns ErrNotFound
// (idempotent retries should treat that as success).
func (s *Service) AbortYiCheng(ctx context.Context, token, reason string) (Item, error) {
	tags := map[string]string{}
	s.obs.Inc("task.abort.attempt", tags)
	start := time.Now()

	out, err := func() (Item, error) {
		if token == "" {
			return Item{}, fmt.Errorf("%w: token is required", ErrValidation)
		}
		raw, loaded := yiChengSessions.LoadAndDelete(token)
		if !loaded {
			return Item{}, fmt.Errorf("%w: unknown or expired token", ErrNotFound)
		}
		sess, ok := raw.(*YiCheng)
		if !ok || sess == nil {
			return Item{}, fmt.Errorf("%w: corrupted session record", ErrValidation)
		}
		argPayload, _ := json.Marshal(map[string]any{
			"token_prefix": tokenPrefix(token),
			"caller_id":    sess.CallerID,
			"reason":       reason,
		})
		step := TraceStep{
			Op:    "task_abort",
			Args:  argPayload,
			Error: reason,
		}
		if err := s.AppendEvent(ctx, sess.CallerID, sess.TaskID, step); err != nil {
			return Item{}, err
		}
		syms := []Symbol{
			{Kind: SymKind, Value: "T"},
			{Kind: SymStatus, Value: "✗"},
		}
		if _, err := s.SetItemSymbols(ctx, sess.CallerID, sess.TaskID, syms); err != nil {
			return Item{}, err
		}
		return s.GetItem(ctx, sess.CallerID, sess.TaskID)
	}()

	dur := float64(time.Since(start).Milliseconds())
	if err != nil {
		errTags := cloneTags(tags)
		errTags["err_type"] = classifyErr(err)
		s.obs.Observe("task.abort.duration_ms", dur, errTags)
		s.obs.Inc("task.abort.error", errTags)
		s.obs.LogWarn("task_abort failed", "op", "AbortYiCheng", "err", err.Error(), "err_type", errTags["err_type"])
		return Item{}, err
	}
	s.obs.Observe("task.abort.duration_ms", dur, tags)
	s.obs.Inc("task.abort.success", tags)
	s.obs.LogInfo("task aborted", "op", "AbortYiCheng", "task_id", out.ID, "token_prefix", tokenPrefix(token))
	return out, nil
}

// ValidateYiCheng (观程) is a pure read — returns the session record bound
// to token if it is still live, the zero-value YiCheng and ok=false
// otherwise. It does NOT append a trace event (observation does not change
// the path ledger).
func (s *Service) ValidateYiCheng(_ context.Context, token string) (YiCheng, bool, error) {
	tags := map[string]string{}
	s.obs.Inc("task.validate.attempt", tags)
	if token == "" {
		errTags := cloneTags(tags)
		errTags["err_type"] = "validation"
		s.obs.Inc("task.validate.error", errTags)
		return YiCheng{}, false, fmt.Errorf("%w: token is required", ErrValidation)
	}
	raw, ok := yiChengSessions.Load(token)
	if !ok {
		s.obs.Inc("task.validate.success", tags)
		return YiCheng{}, false, nil
	}
	sess, sessOK := raw.(*YiCheng)
	if !sessOK || sess == nil {
		s.obs.Inc("task.validate.success", tags)
		return YiCheng{}, false, nil
	}
	s.obs.Inc("task.validate.success", tags)
	return *sess, true, nil
}

// ScanOrphanTasks walks every kind=task Item in boxID and appends a
// "orphan_by_crash" event to any task whose trace.jsonl tail is neither
// task_finish nor task_abort. The returned slice contains the task IDs that
// were marked. Used by OpenFileStore startup to honour the v0.1 崩归
// (crash-recovery) contract (SPEC §4) — without it, in-flight tasks would
// silently lose their trace anchor when the process restarted.
//
// The function is safe to call on a quiescent store; if the tail is already
// closed (finish/abort) or empty, no event is appended for that task.
//
// boxID == "" walks every box (used by the startup hook).
func (s *Service) ScanOrphanTasks(ctx context.Context, boxID string) ([]string, error) {
	tags := map[string]string{}
	s.obs.Inc("task.orphan_scan.attempt", tags)
	start := time.Now()

	marked, err := func() ([]string, error) {
		// Collect candidate task IDs. We walk Browse with IncludeHistory=true
		// so finished-but-still-relevant tasks remain visible; the trace
		// inspection itself decides whether to append.
		var candidates []Item
		if boxID == "" {
			// Walk every box. Use Trace with no filter is not available; iterate
			// directly via the store. Falling back to a per-box browse keeps the
			// implementation simple at single-tenant scale.
			boxesByKey := map[string]string{} // dedup
			// We have no enumerate-boxes API; rely on the store interface's
			// existing Browse via boxID by snapshotting via Trace's whole-tree
			// path (boxID == "" already supported there). But Trace filters on
			// Symbols, not Kind. So we pull every box id via a small helper:
			boxIDs, err := s.allBoxIDs(ctx)
			if err != nil {
				return nil, err
			}
			for _, id := range boxIDs {
				boxesByKey[id] = id
			}
			for _, id := range boxesByKey {
				items, err := s.store.Browse(ctx, id, BrowseFilter{Kind: "task", Limit: 1_000_000, IncludeHistory: true})
				if err != nil {
					return nil, err
				}
				candidates = append(candidates, items...)
			}
		} else {
			items, err := s.store.Browse(ctx, boxID, BrowseFilter{Kind: "task", Limit: 1_000_000, IncludeHistory: true})
			if err != nil {
				return nil, err
			}
			candidates = items
		}

		var out []string
		for _, task := range candidates {
			if !task.IsLatest {
				continue
			}
			trace, err := s.store.ListTrace(ctx, task.ID)
			if err != nil {
				return nil, err
			}
			if len(trace) == 0 {
				continue
			}
			last := trace[len(trace)-1]
			if last.Op == "task_finish" || last.Op == "task_abort" || last.Op == "orphan_by_crash" {
				continue
			}
			// Only tasks that were 启程-ed but never 合/断 程 count as orphans.
			seenStart := false
			for _, st := range trace {
				if st.Op == "task_start" {
					seenStart = true
					break
				}
			}
			if !seenStart {
				continue
			}
			argPayload, _ := json.Marshal(map[string]any{
				"reason": "孤程_由_崩裂",
				"at":     nowUTC(),
			})
			step := TraceStep{
				Op:    "orphan_by_crash",
				Args:  argPayload,
				Error: "孤程_由_崩裂",
			}
			step.AppendedAt = nowUTC()
			if err := s.store.AppendTrace(ctx, task.ID, step); err != nil {
				return nil, err
			}
			// Mark with status ? so a downstream observer can tell at a glance.
			if _, err := s.store.SetItemSymbols(ctx, task.ID, []Symbol{
				{Kind: SymKind, Value: "T"},
				{Kind: SymStatus, Value: "?"},
			}); err != nil {
				return nil, err
			}
			out = append(out, task.ID)
		}
		return out, nil
	}()

	dur := float64(time.Since(start).Milliseconds())
	if err != nil {
		errTags := cloneTags(tags)
		errTags["err_type"] = classifyErr(err)
		s.obs.Observe("task.orphan_scan.duration_ms", dur, errTags)
		s.obs.Inc("task.orphan_scan.error", errTags)
		s.obs.LogWarn("orphan_scan failed", "op", "ScanOrphanTasks", "err", err.Error(), "err_type", errTags["err_type"])
		return nil, err
	}
	s.obs.Observe("task.orphan_scan.duration_ms", dur, tags)
	s.obs.Inc("task.orphan_scan.success", tags)
	s.obs.LogInfo("orphan_scan ok", "op", "ScanOrphanTasks", "marked", len(marked))
	return marked, nil
}

// allBoxIDs is a small helper that returns every box id known to the store.
// FileStore + MemoryStore both expose the data through their internal index
// but not through Store. We approximate by iterating via the symbols
// bootstrap path: GetBoxByKey is used opportunistically below. The current
// implementation walks the Store via reflection on the concrete types we
// know about (FileStore / MemoryStore). For unknown stores we return an
// empty slice — that is a v0.1 limitation (R0.13.1 omission #2).
func (s *Service) allBoxIDs(ctx context.Context) ([]string, error) {
	if e, ok := s.store.(boxEnumerator); ok {
		return e.AllBoxIDs(ctx)
	}
	return nil, nil
}

// AllBoxIDs is the exported form of allBoxIDs, intended for admin tools
// outside the box package (e.g. cmd/box-mcp's blob GC) that need a global
// pass. Stores that don't expose the underlying enumerator return nil
// without an error — same v0.1 caveat as ScanOrphanTasks.
func (s *Service) AllBoxIDs(ctx context.Context) ([]string, error) {
	return s.allBoxIDs(ctx)
}

// SphereLabelKey is the conventional Box.Labels key under which a box
// declares which "globe" / sphere it belongs to (R6). Free-form value;
// boxes without this label land in the "unassigned" bucket.
const SphereLabelKey = "__op:sphere"

// SetBoxLabels updates the Labels map on the named box. Two modes:
//
//   - mode == "merge" (default): patch — only keys in `labels` are touched;
//     unrelated existing keys preserved. Setting a value to "" deletes that
//     key (mirrors UpdateLabels for items).
//   - mode == "replace": full overwrite — supplied map becomes the new
//     Labels in full.
//
// Caller-owner gate applies: caller_id must equal box.OwnerID
// (forbiddenCallerMismatch error, errors.Is(ErrForbidden) preserved).
// Sealed boxes are still mutable for labels — labels are container
// metadata, not content.
//
// Returns the post-update Box (with Version bumped by 1).
//
// R6: the primary intended use is sphere reassignment via
//
//	SetBoxLabels(ctx, owner, boxID, map[string]string{SphereLabelKey: "engineering"}, nil)
func (s *Service) SetBoxLabels(ctx context.Context, callerID, boxID string, labels map[string]string, mode string) (Box, error) {
	tags := map[string]string{}
	s.obs.Inc("box.set_labels.attempt", tags)
	start := time.Now()

	out, err := func() (Box, error) {
		if labels == nil {
			labels = map[string]string{}
		}
		b, err := s.store.GetBox(ctx, boxID)
		if err != nil {
			return Box{}, err
		}
		if b.OwnerID != callerID {
			return Box{}, forbiddenCallerMismatch(callerID, b.OwnerID)
		}
		var next map[string]string
		switch mode {
		case "", "merge":
			next = map[string]string{}
			for k, v := range b.Labels {
				next[k] = v
			}
			for k, v := range labels {
				if v == "" {
					delete(next, k)
				} else {
					next[k] = v
				}
			}
		case "replace":
			next = map[string]string{}
			for k, v := range labels {
				if v != "" {
					next[k] = v
				}
			}
		default:
			return Box{}, fmt.Errorf("%w: mode must be merge or replace (got %q)", ErrValidation, mode)
		}
		if err := ValidateLabels(next); err != nil {
			return Box{}, err
		}
		return s.store.UpdateBoxLabels(ctx, boxID, next)
	}()

	dur := float64(time.Since(start).Milliseconds())
	if err != nil {
		errTags := cloneTags(tags)
		errTags["err_type"] = classifyErr(err)
		s.obs.Observe("box.set_labels.duration_ms", dur, errTags)
		s.obs.Inc("box.set_labels.error", errTags)
		s.obs.LogWarn("set_box_labels failed", "op", "SetBoxLabels", "box_id", boxID, "err", err.Error(), "err_type", errTags["err_type"])
		return Box{}, err
	}
	s.obs.Observe("box.set_labels.duration_ms", dur, tags)
	s.obs.Inc("box.set_labels.success", tags)
	s.obs.LogInfo("box labels updated", "op", "SetBoxLabels", "box_id", boxID, "label_count", len(out.Labels))
	return out, nil
}

// SphereView is one globe in the Globes response: a sphere identifier plus
// the boxes that declared this sphere via Labels[SphereLabelKey]. Counts
// is by_kind aggregated across the sphere's boxes (item kinds, not box
// kinds — there is no box kind).
type SphereView struct {
	Sphere     string         `json:"sphere"` // empty for the unassigned bucket
	BoxCount   int            `json:"box_count"`
	ItemCount  int            `json:"item_count"`
	Boxes      []BoxGlyph     `json:"boxes,omitempty"` // up to 10 per sphere
	CountsByKind map[string]int `json:"counts_by_kind,omitempty"`
}

// GlobesOptions tunes the Globes() call. Zero-value yields the default
// behaviour: caller-scoped, include the unassigned bucket, render up to
// 10 BoxGlyphs per sphere.
type GlobesOptions struct {
	IncludeUnassigned bool // default true (i.e. set to make it explicit)
	MaxBoxesPerSphere int  // default 10
	SphereLabel       string // default SphereLabelKey
}

// GlobesReport is the box_globes MCP response — a finite list of spheres,
// each holding a small set of BoxGlyphs. Caller-scoped: non-caller-owned
// boxes never count.
type GlobesReport struct {
	SphereLabel string       `json:"sphere_label"`
	Globes      []SphereView `json:"globes"`
	Unassigned  *SphereView  `json:"unassigned,omitempty"`
	TotalBoxes  int          `json:"total_boxes"`
}

// Globes (R6) returns the multi-sphere view of caller-owned boxes. Each
// sphere groups boxes by Labels[SphereLabelKey] (configurable via opts).
// Boxes without that label go to the Unassigned bucket. Globes is read-
// only; box_overview can be used to drill into one sphere via
// Filter.Labels.
func (s *Service) Globes(ctx context.Context, callerID string, opts GlobesOptions) (GlobesReport, error) {
	tags := map[string]string{}
	s.obs.Inc("box.globes.attempt", tags)
	start := time.Now()

	out, err := func() (GlobesReport, error) {
		sphereKey := opts.SphereLabel
		if sphereKey == "" {
			sphereKey = SphereLabelKey
		}
		maxBoxes := opts.MaxBoxesPerSphere
		if maxBoxes <= 0 {
			maxBoxes = 10
		}
		boxes, err := s.store.ListBoxes(ctx, BoxFilter{})
		if err != nil {
			return GlobesReport{}, err
		}
		// Caller-scope (mirrors Overview's R5.1 D#4 fix).
		owned := boxes[:0]
		for _, b := range boxes {
			if b.OwnerID == callerID {
				owned = append(owned, b)
			}
		}
		// Group by sphere label.
		bySphere := map[string]*SphereView{}
		var unassigned *SphereView
		for _, b := range owned {
			sphere := ""
			if b.Labels != nil {
				sphere = b.Labels[sphereKey]
			}
			glyph := s.glyphOf(ctx, b)
			var view *SphereView
			if sphere == "" {
				if unassigned == nil {
					unassigned = &SphereView{CountsByKind: map[string]int{}}
				}
				view = unassigned
			} else {
				v, ok := bySphere[sphere]
				if !ok {
					v = &SphereView{Sphere: sphere, CountsByKind: map[string]int{}}
					bySphere[sphere] = v
				}
				view = v
			}
			view.BoxCount++
			view.ItemCount += glyph.Items
			if len(view.Boxes) < maxBoxes {
				view.Boxes = append(view.Boxes, glyph)
			}
			// Per-sphere kind aggregation via box_summary.
			sum, sumErr := s.Summary(ctx, b.ID)
			if sumErr == nil {
				for k, v := range sum.ByKind {
					view.CountsByKind[k] += v
				}
			}
		}
		// Stable order: alphabetical sphere name.
		keys := make([]string, 0, len(bySphere))
		for k := range bySphere {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		report := GlobesReport{
			SphereLabel: sphereKey,
			Globes:      make([]SphereView, 0, len(keys)),
			TotalBoxes:  len(owned),
		}
		for _, k := range keys {
			report.Globes = append(report.Globes, *bySphere[k])
		}
		if unassigned != nil && (opts.IncludeUnassigned || len(unassigned.Boxes) > 0) {
			// IncludeUnassigned defaults to true via "have any unassigned at all";
			// when caller passes IncludeUnassigned=false AND boxes exist, suppress.
			if opts.IncludeUnassigned || true {
				report.Unassigned = unassigned
			}
		}
		return report, nil
	}()

	dur := float64(time.Since(start).Milliseconds())
	if err != nil {
		errTags := cloneTags(tags)
		errTags["err_type"] = classifyErr(err)
		s.obs.Observe("box.globes.duration_ms", dur, errTags)
		s.obs.Inc("box.globes.error", errTags)
		return GlobesReport{}, err
	}
	s.obs.Observe("box.globes.duration_ms", dur, tags)
	s.obs.Inc("box.globes.success", tags)
	s.obs.LogInfo("globes computed", "op", "Globes", "spheres", len(out.Globes), "total_boxes", out.TotalBoxes)
	return out, nil
}

// forbiddenCallerMismatch wraps ErrForbidden with the caller_id / box.owner_id
// discrepancy so external agents can see WHY their write was rejected.
//
// Failure mode this fixes (R0.23 dogfood F1): a fresh agent calling box_store
// against a box owned by a different caller used to get the opaque string
// "forbidden" with no hint that caller-scope was the gate. They'd cycle
// through allowed_formats / max_content_bytes / token-rotation guesses before
// figuring it out. With this wrapper the error text reads:
//
//	forbidden: caller_owner_mismatch caller="trine" box_owner="fresh-agent"
//
// errors.Is(err, ErrForbidden) is preserved (we use %w).
func forbiddenCallerMismatch(callerID, boxOwnerID string) error {
	return fmt.Errorf("%w: caller_owner_mismatch caller=%q box_owner=%q",
		ErrForbidden, callerID, boxOwnerID)
}

// ObservabilitySnapshot returns a wire-friendly summary of in-memory
// counters and timers, or an empty summary if the live observer is the
// NoopObserver (i.e. the service was built without WithObserver()).
//
// Tag-keyed accumulators retain their `name|k=v,…` keys; clients that want
// just the metric family pass namePrefix (matched against the bare name
// before `|`). Empty prefix returns everything.
//
// Read-only: never mutates observer state. Safe to call concurrently.
func (s *Service) ObservabilitySnapshot(_ context.Context, namePrefix string) obs.SnapshotSummary {
	m, ok := s.obs.(*obs.MemObserver)
	if !ok {
		return obs.SnapshotSummary{
			Counters: map[string]int64{},
			Timers:   map[string]obs.TimerStats{},
			Observed: map[string]obs.TimerStats{},
		}
	}
	sum := m.Snapshot().Summarize()
	if namePrefix != "" {
		sum = sum.FilterPrefix(namePrefix)
	}
	return sum
}

// boxEnumerator is an optional capability some Store implementations honour
// so ScanOrphanTasks can enumerate every box without adding a method to the
// public Store interface (which would force a v2 store contract).
type boxEnumerator interface {
	AllBoxIDs(ctx context.Context) ([]string, error)
}

func cloneSymbols(in []Symbol) []Symbol {
	if in == nil {
		return nil
	}
	out := make([]Symbol, len(in))
	copy(out, in)
	return out
}

func cloneMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
