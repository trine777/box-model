package box

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

type Store interface {
	CreateBox(context.Context, Box) (Box, error)
	GetBox(context.Context, string) (Box, error)
	GetBoxByKey(ctx context.Context, key string) (Box, error)
	SealBox(context.Context, string) error
	CountItems(context.Context, string) (int, error)
	InsertItem(context.Context, Item) (Item, error)
	Browse(context.Context, string, BrowseFilter) ([]Item, error)
	GetItem(context.Context, string) (Item, error)
	RecordConsume(context.Context, ConsumeLog) error
	MarkConsumed(context.Context, string) error
	ReplaceItem(ctx context.Context, prevItemID string, newItem Item) (Item, error)
	UpdateLabels(ctx context.Context, itemID string, labels map[string]string) (Item, error)
	DeleteItem(ctx context.Context, itemID string) (Item, error)
	ListConsumes(ctx context.Context, itemID string) ([]ConsumeLog, error)
	Trace(ctx context.Context, boxID string, query SymbolQuery) ([]Item, error)
	Neighbors(ctx context.Context, itemID string, hops int) (Subgraph, error)
	// SetItemSymbols overwrites the Symbols field of an item in place. It
	// does NOT open a new revision (mirrors UpdateLabels semantics). Caller
	// authorization is enforced at the Service layer.
	SetItemSymbols(ctx context.Context, itemID string, symbols []Symbol) (Item, error)
	// AppendTrace appends one TraceStep to the task's append-only trace log.
	// The implementation is responsible for assigning step.Step (= current
	// length) and persisting the entry. taskID must refer to an existing
	// item; semantics on non-task items are undefined (Service enforces
	// kind=task before calling).
	AppendTrace(ctx context.Context, taskID string, step TraceStep) error
	// ListTrace returns the full trace history for a task in append order.
	// Returns an empty (non-nil) slice when no trace exists yet.
	ListTrace(ctx context.Context, taskID string) ([]TraceStep, error)
	// ListBoxes returns every Box matching the filter (AND across populated
	// dimensions). An empty BoxFilter matches every box. Caller-scoping is
	// NOT enforced here — that is the Service layer's job (R5.1 D#4).
	ListBoxes(ctx context.Context, filter BoxFilter) ([]Box, error)
	// UpdateBoxLabels overwrites a box's Labels map in place — same semantics
	// as UpdateLabels does for items: no new version, no immutability shim.
	// Bumps Box.Version so external watchers can detect the change.
	// Caller authorisation is enforced at the Service layer (R6 sphere work).
	UpdateBoxLabels(ctx context.Context, boxID string, labels map[string]string) (Box, error)
	// UpdateBoxSymbols overwrites a box's Symbols in place (R13). Bumps
	// Version. Caller authorisation enforced at the Service layer.
	UpdateBoxSymbols(ctx context.Context, boxID string, symbols []Symbol) (Box, error)
}

type MemoryStore struct {
	mu          sync.RWMutex
	boxes       map[string]Box
	items       map[string]Item
	byBox       map[string][]string
	byIdem      map[string]string
	consumes    []ConsumeLog
	byTaskTrace map[string][]TraceStep // task_id → ordered TraceSteps
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		boxes:       map[string]Box{},
		items:       map[string]Item{},
		byBox:       map[string][]string{},
		byIdem:      map[string]string{},
		byTaskTrace: map[string][]TraceStep{},
	}
}

func (s *MemoryStore) CreateBox(_ context.Context, b Box) (Box, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.boxes {
		if existing.Key == b.Key && existing.Version == b.Version {
			return Box{}, ErrConflict
		}
	}
	s.boxes[b.ID] = b
	return b, nil
}

func (s *MemoryStore) GetBox(_ context.Context, id string) (Box, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.boxes[id]
	if !ok {
		return Box{}, ErrNotFound
	}
	return b, nil
}

// GetBoxByKey returns the Box with the given public key. When multiple
// versions exist under the same key (rare in single-operator use), the box
// with the highest Version is returned. Returns ErrNotFound if no box matches.
func (s *MemoryStore) GetBoxByKey(_ context.Context, key string) (Box, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var (
		best  Box
		found bool
	)
	for _, b := range s.boxes {
		if b.Key != key {
			continue
		}
		if !found || b.Version > best.Version {
			best = b
			found = true
		}
	}
	if !found {
		return Box{}, ErrNotFound
	}
	return best, nil
}

func (s *MemoryStore) SealBox(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.boxes[id]
	if !ok {
		return ErrNotFound
	}
	if b.Status != "active" {
		return ErrConflict
	}
	b.Status = "sealed"
	s.boxes[id] = b
	return nil
}

// UpdateBoxLabels swaps the Labels map in place and bumps Box.Version. Sealed
// boxes are still mutable here — labels are metadata about the container, not
// content; sphere reassignment of an archived box is a legitimate operation.
// Authorisation is the Service layer's job.
func (s *MemoryStore) UpdateBoxLabels(_ context.Context, id string, labels map[string]string) (Box, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.boxes[id]
	if !ok {
		return Box{}, ErrNotFound
	}
	b.Labels = labels
	b.Version++
	s.boxes[id] = b
	return b, nil
}

func (s *MemoryStore) UpdateBoxSymbols(_ context.Context, id string, symbols []Symbol) (Box, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.boxes[id]
	if !ok {
		return Box{}, ErrNotFound
	}
	b.Symbols = symbols
	b.Version++
	s.boxes[id] = b
	return b, nil
}

func (s *MemoryStore) CountItems(_ context.Context, boxID string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byBox[boxID]), nil
}

func (s *MemoryStore) InsertItem(_ context.Context, item Item) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existingID, ok := s.byIdem[item.IdemKey]; ok {
		return s.items[existingID], nil
	}
	s.items[item.ID] = item
	s.byBox[item.BoxID] = append(s.byBox[item.BoxID], item.ID)
	s.byIdem[item.IdemKey] = item.ID
	return item, nil
}

func (s *MemoryStore) Browse(_ context.Context, boxID string, f BrowseFilter) ([]Item, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := s.byBox[boxID]
	out := make([]Item, 0, len(ids))
	for _, id := range ids {
		item := s.items[id]
		if item.Status == "deleted" {
			continue
		}
		if f.OnlyHistory {
			if item.IsLatest {
				continue
			}
		} else if !f.IncludeHistory {
			if !item.IsLatest {
				continue
			}
		}
		if !matches(item, f) {
			continue
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].StoredAt.After(out[j].StoredAt)
	})
	if f.Offset > len(out) {
		return []Item{}, nil
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	end := f.Offset + limit
	if end > len(out) {
		end = len(out)
	}
	return out[f.Offset:end], nil
}

func (s *MemoryStore) GetItem(_ context.Context, id string) (Item, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.items[id]
	if !ok || item.Status == "deleted" || item.Status == "expired" {
		return Item{}, ErrNotFound
	}
	return item, nil
}

func (s *MemoryStore) RecordConsume(_ context.Context, log ConsumeLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.consumes = append(s.consumes, log)
	return nil
}

func (s *MemoryStore) MarkConsumed(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[id]
	if !ok {
		return ErrNotFound
	}
	if item.Status == "available" {
		item.Status = "consumed"
		s.items[id] = item
	}
	return nil
}

// ReplaceItem flips prev to superseded/IsLatest=false and inserts newItem in
// one mutex-protected critical section so external Browse readers can never
// observe a state where prev has been flipped but the new item is not yet
// visible (and vice versa).
func (s *MemoryStore) ReplaceItem(_ context.Context, prevItemID string, newItem Item) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, ok := s.items[prevItemID]
	if !ok {
		return Item{}, ErrNotFound
	}
	if !prev.IsLatest {
		return Item{}, ErrConflict
	}
	if existingID, ok := s.byIdem[newItem.IdemKey]; ok && existingID != prevItemID {
		return Item{}, fmt.Errorf("%w: idem_key %q already used by another item", ErrConflict, newItem.IdemKey)
	}
	now := nowUTC()
	prev.IsLatest = false
	prev.Status = "superseded"
	prev.SupersededAt = &now
	s.items[prev.ID] = prev

	s.items[newItem.ID] = newItem
	s.byBox[newItem.BoxID] = append(s.byBox[newItem.BoxID], newItem.ID)
	s.byIdem[newItem.IdemKey] = newItem.ID
	return newItem, nil
}

// UpdateLabels overwrites labels on an existing item atomically. It does not
// touch revision metadata, storage_uri, content, or content_hash.
func (s *MemoryStore) UpdateLabels(_ context.Context, itemID string, labels map[string]string) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[itemID]
	if !ok {
		return Item{}, ErrNotFound
	}
	item.Labels = cloneMap(labels)
	s.items[itemID] = item
	return item, nil
}

// DeleteItem soft-deletes an item by flipping Status=deleted, IsLatest=false.
// Returns ErrNotFound if the item does not exist; ErrConflict if already deleted.
//
// Releases item.IdemKey from the byIdem index so a subsequent InsertItem with
// the same idem can create a NEW item (D#9). The item.IdemKey field on the
// stored item is intentionally preserved for audit.
func (s *MemoryStore) DeleteItem(_ context.Context, itemID string) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[itemID]
	if !ok {
		return Item{}, ErrNotFound
	}
	if item.Status == "deleted" {
		return Item{}, ErrConflict
	}
	item.Status = "deleted"
	item.IsLatest = false
	s.items[itemID] = item
	if item.IdemKey != "" {
		// Only release if the index currently points at this item — defensive
		// against a future state where two items shared a key historically.
		if cur, ok := s.byIdem[item.IdemKey]; ok && cur == itemID {
			delete(s.byIdem, item.IdemKey)
		}
	}
	return item, nil
}

// ListConsumes returns all ConsumeLog entries for itemID in insertion
// (chronological) order. Returns an empty (non-nil) slice when no logs exist.
func (s *MemoryStore) ListConsumes(_ context.Context, itemID string) ([]ConsumeLog, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ConsumeLog, 0)
	for _, c := range s.consumes {
		if c.ItemID == itemID {
			out = append(out, c)
		}
	}
	return out, nil
}

// Trace returns latest, non-deleted items whose Symbols satisfy the query.
// boxID == "" selects across every box. Sorted StoredAt-desc.
func (s *MemoryStore) Trace(_ context.Context, boxID string, query SymbolQuery) ([]Item, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return traceItems(s.items, s.byBox, boxID, query), nil
}

// Neighbors returns the hops-bounded subgraph centered on itemID.
// Out-edges come from item.Symbols (SymRelation entries with Ref). In-edges
// are computed by a linear scan — fine at 1500-item scale (KISS).
func (s *MemoryStore) Neighbors(_ context.Context, itemID string, hops int) (Subgraph, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return buildNeighbors(s.items, itemID, hops)
}

func matches(item Item, f BrowseFilter) bool {
	if f.Kind != "" && item.Kind != f.Kind {
		return false
	}
	if f.Since != nil && item.StoredAt.Before(*f.Since) {
		return false
	}
	if f.Until != nil && item.StoredAt.After(*f.Until) {
		return false
	}
	if len(f.LocationIDs) > 0 {
		found := false
		for _, loc := range f.LocationIDs {
			if item.LocationID == loc {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for k, v := range f.SourceRef {
		if item.SourceRef[k] != v {
			return false
		}
	}
	for k, v := range f.Labels {
		if item.Labels[k] != v {
			return false
		}
	}
	return true
}

func nowUTC() time.Time {
	return time.Now().UTC().Round(0)
}

// SetItemSymbols overwrites the Symbols slice on an existing item without
// touching revision metadata, labels, content, or storage_uri. Mirrors
// UpdateLabels' "in place" semantics.
func (s *MemoryStore) SetItemSymbols(_ context.Context, itemID string, symbols []Symbol) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[itemID]
	if !ok {
		return Item{}, ErrNotFound
	}
	item.Symbols = cloneSymbols(symbols)
	s.items[itemID] = item
	return item, nil
}

// AppendTrace appends a step to the per-task trace slice. Step.Step is
// reassigned to the current length so the on-disk and in-memory orderings
// always agree even if the caller passed a stale index.
func (s *MemoryStore) AppendTrace(_ context.Context, taskID string, step TraceStep) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	step.Step = len(s.byTaskTrace[taskID])
	s.byTaskTrace[taskID] = append(s.byTaskTrace[taskID], step)
	return nil
}

// ListTrace returns a defensive copy of the task's trace slice; the empty
// slice is non-nil so callers can rely on JSON-encoding to "[]".
func (s *MemoryStore) ListTrace(_ context.Context, taskID string) ([]TraceStep, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src := s.byTaskTrace[taskID]
	out := make([]TraceStep, len(src))
	copy(out, src)
	return out, nil
}

// ListBoxes returns boxes that match every populated BoxFilter dimension.
// An empty filter returns every box (in unspecified order). Caller-scoping is
// the Service layer's responsibility.
func (s *MemoryStore) ListBoxes(_ context.Context, filter BoxFilter) ([]Box, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Box, 0, len(s.boxes))
	for _, b := range s.boxes {
		if !matchesBoxFilter(b, filter) {
			continue
		}
		out = append(out, b)
	}
	return out, nil
}

// matchesBoxFilter is the shared predicate for ListBoxes (memory + file).
func matchesBoxFilter(b Box, f BoxFilter) bool {
	if f.Owner != "" && b.OwnerID != f.Owner {
		return false
	}
	if f.Status != "" && b.Status != f.Status {
		return false
	}
	for k, v := range f.Labels {
		if b.Labels[k] != v {
			return false
		}
	}
	return true
}

// AllBoxIDs satisfies the optional boxEnumerator capability used by
// Service.ScanOrphanTasks. Returns a snapshot — callers should not assume
// the result remains valid past the next mutation.
func (s *MemoryStore) AllBoxIDs(_ context.Context) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.boxes))
	for id := range s.boxes {
		out = append(out, id)
	}
	return out, nil
}
