package box

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/windborneos/box-model/box/obs"
)

// FileStore is a Store implementation that persists Boxes and Items as JSON
// files under a root directory. Layout:
//
//	<root>/boxes/<box_key>/box.json
//	<root>/boxes/<box_key>/items/<item_id>.json
//	<root>/boxes/<box_key>/items/.pending-<uuid>.json   (journal, transient)
//	<root>/boxes/<box_key>/consumes.jsonl
//
// Concurrency: a single sync.Mutex serializes all writes. Reads also take the
// lock to keep the implementation simple (KISS — one-person company).
//
// Single-file writes go through writeFileAtomic (tmp + rename). Multi-file
// operations (ReplaceItem) go through a journal: write a .pending-*.json file,
// fsync it, apply, then remove the journal. On Open, any leftover .pending-*
// files are replayed before the on-disk state is loaded into the in-memory
// indexes.
type FileStore struct {
	root string
	mu   sync.Mutex

	boxes      map[string]Box      // box_id → Box
	items      map[string]Item     // item_id → Item
	byBox      map[string][]string // box_id → []item_id
	byIdem     map[string]string   // idem_key → item_id
	boxKeyToID map[string]string   // box_key → box_id

	// obs is the in-process observer used to emit FileStore-level metrics
	// (open duration, journal replays, IO errors). Defaults to NoopObserver.
	// Wire a real observer via FileStore.SetObserver.
	obs obs.Observer
}

// SetObserver installs an obs.Observer on this FileStore. Subsequent
// operations emit metrics and log records via o. A nil argument is ignored.
//
// Note: OpenFileStore runs before this hook can fire, so the "store.open.*"
// metrics emitted during Open will land on the NoopObserver. Wire observability
// at the CLI level if you care about Open timings.
func (s *FileStore) SetObserver(o obs.Observer) {
	if o == nil {
		return
	}
	s.mu.Lock()
	s.obs = o
	s.mu.Unlock()
}

// journalEntry is the on-disk representation of an in-flight multi-file
// operation. It is intentionally unexported.
type journalEntry struct {
	Kind      string      `json:"kind"`
	BoxKey    string      `json:"box_key"`
	PrevID    string      `json:"prev_id,omitempty"`
	PrevAfter *Item       `json:"prev_after,omitempty"`
	NewItem   *Item       `json:"new_item,omitempty"`
	ItemID    string      `json:"item_id,omitempty"`
	ItemAfter *Item       `json:"item_after,omitempty"`
	Log       *ConsumeLog `json:"log,omitempty"`
}

// OpenFileStore opens the FileStore rooted at the given directory. The
// directory tree is created if absent. Any leftover journal files are replayed
// before in-memory indexes are rebuilt from disk.
func OpenFileStore(root string) (*FileStore, error) {
	if root == "" {
		return nil, fmt.Errorf("%w: root is required", ErrValidation)
	}
	if err := os.MkdirAll(filepath.Join(root, "boxes"), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir root: %w", err)
	}
	s := &FileStore{
		root:       root,
		boxes:      map[string]Box{},
		items:      map[string]Item{},
		byBox:      map[string][]string{},
		byIdem:     map[string]string{},
		boxKeyToID: map[string]string{},
		obs:        obs.NoopObserver{},
	}
	start := time.Now()
	s.obs.Inc("store.open.attempt", nil)
	if err := s.replayAndLoad(); err != nil {
		return nil, err
	}
	if err := s.scanOrphanTracesAtStartup(); err != nil {
		return nil, fmt.Errorf("orphan scan: %w", err)
	}
	s.obs.Observe("store.open.duration_ms", float64(time.Since(start).Milliseconds()), nil)
	s.obs.Inc("store.open.success", nil)
	return s, nil
}

// scanOrphanTracesAtStartup walks every task item's trace.jsonl. Any task
// whose trace contains a "task_start" but whose tail is neither "task_finish"
// nor "task_abort" gets one synthetic "orphan_by_crash" event appended (and
// the in-memory status symbol flipped to ?). This honours SPEC §4 崩归
// (crash-recovery v0.1) — without it, a process restart would leave the
// trace silently truncated mid-execution.
//
// Runs under the FileStore mutex via the call sites it uses; safe to invoke
// from OpenFileStore exactly once before SetObserver fires.
func (s *FileStore) scanOrphanTracesAtStartup() error {
	now := nowUTC()
	for _, item := range s.items {
		if item.Kind != "task" || !item.IsLatest || item.Status == "deleted" {
			continue
		}
		boxRec, ok := s.boxes[item.BoxID]
		if !ok {
			continue
		}
		path := s.taskTracePath(boxRec.Key, item.ID)
		trace, err := readTraceFile(path)
		if err != nil {
			return err
		}
		if len(trace) == 0 {
			continue
		}
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
		last := trace[len(trace)-1]
		if last.Op == "task_finish" || last.Op == "task_abort" || last.Op == "orphan_by_crash" {
			continue
		}
		// Append the synthetic orphan event directly to the file. We cannot
		// route through AppendTrace because that grabs s.mu (which we already
		// hold? No — replayAndLoad releases as it goes; but OpenFileStore is
		// single-threaded). To stay safe, inline the writer.
		argPayload, _ := json.Marshal(map[string]any{
			"reason": "孤程_由_崩裂",
			"at":     now,
		})
		step := TraceStep{
			Step:       len(trace),
			Op:         "orphan_by_crash",
			Args:       argPayload,
			Error:      "孤程_由_崩裂",
			AppendedAt: now,
		}
		line, err := json.Marshal(step)
		if err != nil {
			return err
		}
		line = append(line, '\n')
		if err := os.MkdirAll(s.tasksDir(boxRec.Key), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		if _, err := f.Write(line); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		// Flip the in-memory symbol to ? so a downstream observer can see
		// it without scanning the trace.
		newSyms := []Symbol{
			{Kind: SymKind, Value: "T"},
			{Kind: SymStatus, Value: "?"},
		}
		item.Symbols = cloneSymbols(newSyms)
		s.items[item.ID] = item
		if err := s.writeItem(boxRec.Key, item); err != nil {
			return err
		}
		s.obs.Inc("store.orphan_scan.marked", nil)
		s.obs.LogWarn("orphan task marked", "op", "OpenFileStore", "task_id", item.ID, "box_key", boxRec.Key)
	}
	return nil
}

// readTraceFile decodes a trace.jsonl into []TraceStep without taking the
// FileStore mutex (caller is OpenFileStore, single-threaded). Missing file =
// empty slice, no error.
func readTraceFile(path string) ([]TraceStep, error) {
	out := make([]TraceStep, 0)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	for {
		var step TraceStep
		if err := dec.Decode(&step); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		out = append(out, step)
	}
	return out, nil
}

// Close releases any resources. The current implementation does not hold open
// file handles between calls, so this is a no-op kept for forward compatibility.
func (s *FileStore) Close() error {
	return nil
}

// --- paths -----------------------------------------------------------------

func (s *FileStore) boxDir(key string) string {
	return filepath.Join(s.root, "boxes", key)
}

func (s *FileStore) boxJSON(key string) string {
	return filepath.Join(s.boxDir(key), "box.json")
}

func (s *FileStore) itemsDir(key string) string {
	return filepath.Join(s.boxDir(key), "items")
}

func (s *FileStore) itemPath(key, itemID string) string {
	return filepath.Join(s.itemsDir(key), itemID+".json")
}

func (s *FileStore) consumesPath(key string) string {
	return filepath.Join(s.boxDir(key), "consumes.jsonl")
}

func (s *FileStore) journalPath(key string) string {
	return filepath.Join(s.itemsDir(key), ".pending-"+newJournalID()+".json")
}

func newJournalID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// --- atomic write helpers --------------------------------------------------

// writeFileAtomic writes data to path via a temp file in the same directory
// and an atomic rename. fsync is performed on the temp file before rename to
// ensure the data is durable.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// --- load / replay ---------------------------------------------------------

func (s *FileStore) replayAndLoad() error {
	boxesDir := filepath.Join(s.root, "boxes")
	entries, err := os.ReadDir(boxesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	// First pass: replay journals so on-disk state is coherent.
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if err := s.replayJournals(e.Name()); err != nil {
			return fmt.Errorf("replay %s: %w", e.Name(), err)
		}
	}
	// Second pass: load boxes and items.
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if err := s.loadBox(e.Name()); err != nil {
			return fmt.Errorf("load %s: %w", e.Name(), err)
		}
	}
	return nil
}

func (s *FileStore) replayJournals(boxKey string) error {
	itemsDir := s.itemsDir(boxKey)
	entries, err := os.ReadDir(itemsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, ".pending-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		// Finding a pending journal means the process crashed mid-replace last
		// time. Emit a warn-level signal so operators notice.
		s.obs.Inc("store.journal.replay", nil)
		s.obs.LogWarn("replaying journal", "op", "OpenFileStore", "box_key", boxKey, "journal", name)
		jpath := filepath.Join(itemsDir, name)
		data, err := os.ReadFile(jpath)
		if err != nil {
			s.obs.Inc("store.journal.apply_error", nil)
			s.obs.LogError("read journal failed", err, "box_key", boxKey, "journal", name)
			return err
		}
		var entry journalEntry
		if err := json.Unmarshal(data, &entry); err != nil {
			// Malformed journal — refuse to silently drop; surface the error
			// so the operator can investigate.
			s.obs.Inc("store.journal.apply_error", nil)
			s.obs.LogError("invalid journal", err, "box_key", boxKey, "journal", name)
			return fmt.Errorf("invalid journal %s: %w", name, err)
		}
		if err := s.applyJournalToDisk(entry); err != nil {
			s.obs.Inc("store.journal.apply_error", nil)
			s.obs.LogError("apply journal failed", err, "box_key", boxKey, "journal", name)
			return fmt.Errorf("apply journal %s: %w", name, err)
		}
		if err := os.Remove(jpath); err != nil {
			s.obs.Inc("store.write.error", nil)
			s.obs.LogError("remove journal failed", err, "box_key", boxKey, "journal", name)
			return fmt.Errorf("remove journal %s: %w", name, err)
		}
	}
	return nil
}

// applyJournalToDisk writes the post-image item files described by the journal.
// It only writes to the filesystem; the in-memory index is rebuilt by
// loadBox() after replay completes. It is idempotent (safe to re-run).
func (s *FileStore) applyJournalToDisk(e journalEntry) error {
	switch e.Kind {
	case "replace_item":
		if e.PrevAfter != nil {
			path := s.itemPath(e.BoxKey, e.PrevAfter.ID)
			data, err := json.MarshalIndent(e.PrevAfter, "", "  ")
			if err != nil {
				return err
			}
			if err := writeFileAtomic(path, data); err != nil {
				return err
			}
		}
		if e.NewItem != nil {
			path := s.itemPath(e.BoxKey, e.NewItem.ID)
			data, err := json.MarshalIndent(e.NewItem, "", "  ")
			if err != nil {
				return err
			}
			if err := writeFileAtomic(path, data); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown journal kind %q", e.Kind)
	}
}

func (s *FileStore) loadBox(boxKey string) error {
	bjson := s.boxJSON(boxKey)
	data, err := os.ReadFile(bjson)
	if err != nil {
		if os.IsNotExist(err) {
			// directory without a box.json — skip
			return nil
		}
		return err
	}
	var b Box
	if err := json.Unmarshal(data, &b); err != nil {
		return fmt.Errorf("unmarshal box %s: %w", boxKey, err)
	}
	s.boxes[b.ID] = b
	s.boxKeyToID[b.Key] = b.ID

	itemsDir := s.itemsDir(boxKey)
	entries, err := os.ReadDir(itemsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// Skip transient artifacts:
		//   - .tmp files (incomplete writes)
		//   - .pending-*.json (journals; should have been replayed)
		if strings.HasSuffix(name, ".tmp") {
			continue
		}
		if strings.HasPrefix(name, ".pending-") {
			continue
		}
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		ipath := filepath.Join(itemsDir, name)
		idata, err := os.ReadFile(ipath)
		if err != nil {
			return err
		}
		var item Item
		if err := json.Unmarshal(idata, &item); err != nil {
			return fmt.Errorf("unmarshal item %s: %w", name, err)
		}
		s.items[item.ID] = item
		s.byBox[item.BoxID] = append(s.byBox[item.BoxID], item.ID)
		// Skip deleted items when rebuilding the idem index so a previously
		// released idem_key (D#9) does not get re-occupied on reopen.
		if item.IdemKey != "" && item.Status != "deleted" {
			s.byIdem[item.IdemKey] = item.ID
		}
	}
	return nil
}

// --- Store interface -------------------------------------------------------

func (s *FileStore) CreateBox(_ context.Context, b Box) (Box, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.boxes {
		if existing.Key == b.Key && existing.Version == b.Version {
			return Box{}, ErrConflict
		}
	}
	if err := os.MkdirAll(s.itemsDir(b.Key), 0o755); err != nil {
		return Box{}, err
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return Box{}, err
	}
	if err := writeFileAtomic(s.boxJSON(b.Key), data); err != nil {
		return Box{}, err
	}
	s.boxes[b.ID] = b
	s.boxKeyToID[b.Key] = b.ID
	return b, nil
}

func (s *FileStore) GetBox(_ context.Context, id string) (Box, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.boxes[id]
	if !ok {
		return Box{}, ErrNotFound
	}
	return b, nil
}

// GetBoxByKey returns the Box with the given public key. FileStore maintains a
// box_key → box_id index that assumes a single Box per key (matching
// CreateBox's conflict check semantics). Returns ErrNotFound if no box matches.
func (s *FileStore) GetBoxByKey(_ context.Context, key string) (Box, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.boxKeyToID[key]
	if !ok {
		return Box{}, ErrNotFound
	}
	b, ok := s.boxes[id]
	if !ok {
		return Box{}, ErrNotFound
	}
	return b, nil
}

func (s *FileStore) SealBox(_ context.Context, id string) error {
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
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(s.boxJSON(b.Key), data); err != nil {
		return err
	}
	s.boxes[id] = b
	return nil
}

// UpdateBoxLabels swaps Box.Labels and bumps Version, then persists box.json
// atomically (temp file + rename). Sealed boxes are still mutable here —
// sphere reassignment of an archived box is legitimate metadata work.
func (s *FileStore) UpdateBoxLabels(_ context.Context, id string, labels map[string]string) (Box, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.boxes[id]
	if !ok {
		return Box{}, ErrNotFound
	}
	b.Labels = labels
	b.Version++
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return Box{}, err
	}
	if err := writeFileAtomic(s.boxJSON(b.Key), data); err != nil {
		return Box{}, err
	}
	s.boxes[id] = b
	return b, nil
}

func (s *FileStore) UpdateBoxSymbols(_ context.Context, id string, symbols []Symbol) (Box, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.boxes[id]
	if !ok {
		return Box{}, ErrNotFound
	}
	b.Symbols = symbols
	b.Version++
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return Box{}, err
	}
	if err := writeFileAtomic(s.boxJSON(b.Key), data); err != nil {
		return Box{}, err
	}
	s.boxes[id] = b
	return b, nil
}

func (s *FileStore) CountItems(_ context.Context, boxID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byBox[boxID]), nil
}

func (s *FileStore) InsertItem(_ context.Context, item Item) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existingID, ok := s.byIdem[item.IdemKey]; ok {
		return s.items[existingID], nil
	}
	b, ok := s.boxes[item.BoxID]
	if !ok {
		return Item{}, ErrNotFound
	}
	if err := s.writeItem(b.Key, item); err != nil {
		return Item{}, err
	}
	s.items[item.ID] = item
	s.byBox[item.BoxID] = append(s.byBox[item.BoxID], item.ID)
	if item.IdemKey != "" {
		s.byIdem[item.IdemKey] = item.ID
	}
	return item, nil
}

func (s *FileStore) Browse(_ context.Context, boxID string, f BrowseFilter) ([]Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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

func (s *FileStore) GetItem(_ context.Context, id string) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[id]
	if !ok || item.Status == "deleted" || item.Status == "expired" {
		return Item{}, ErrNotFound
	}
	return item, nil
}

// RecordConsume appends a single ConsumeLog as one JSON line to the box's
// consumes.jsonl. Each call opens, appends, fsyncs, and closes — the append is
// itself atomic but not coupled with MarkConsumed (see D#4 in ROADMAP).
func (s *FileStore) RecordConsume(_ context.Context, log ConsumeLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.items[log.ItemID]
	if !ok {
		return ErrNotFound
	}
	boxRec, ok := s.boxes[b.BoxID]
	if !ok {
		return ErrNotFound
	}
	if err := os.MkdirAll(s.boxDir(boxRec.Key), 0o755); err != nil {
		return err
	}
	line, err := json.Marshal(log)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	f, err := os.OpenFile(s.consumesPath(boxRec.Key), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(line); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func (s *FileStore) MarkConsumed(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[id]
	if !ok {
		return ErrNotFound
	}
	if item.Status != "available" {
		return nil
	}
	b, ok := s.boxes[item.BoxID]
	if !ok {
		return ErrNotFound
	}
	item.Status = "consumed"
	if err := s.writeItem(b.Key, item); err != nil {
		return err
	}
	s.items[id] = item
	return nil
}

// ReplaceItem flips prev → superseded/IsLatest=false and inserts newItem with
// IsLatest=true. The two file writes happen through a journal so a crash
// between them leaves a recoverable state (replayed at next OpenFileStore).
func (s *FileStore) ReplaceItem(_ context.Context, prevItemID string, newItem Item) (Item, error) {
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
	b, ok := s.boxes[prev.BoxID]
	if !ok {
		return Item{}, ErrNotFound
	}
	now := nowUTC()
	prevAfter := prev
	prevAfter.IsLatest = false
	prevAfter.Status = "superseded"
	prevAfter.SupersededAt = &now

	entry := journalEntry{
		Kind:      "replace_item",
		BoxKey:    b.Key,
		PrevID:    prev.ID,
		PrevAfter: &prevAfter,
		NewItem:   &newItem,
	}
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return Item{}, err
	}
	jpath := s.journalPath(b.Key)
	if err := writeFileAtomic(jpath, data); err != nil {
		return Item{}, err
	}
	// Apply the journal to disk, then drop it.
	if err := s.applyJournalToDisk(entry); err != nil {
		return Item{}, err
	}
	if err := os.Remove(jpath); err != nil {
		return Item{}, err
	}
	// Update in-memory indexes.
	s.items[prev.ID] = prevAfter
	s.items[newItem.ID] = newItem
	s.byBox[newItem.BoxID] = append(s.byBox[newItem.BoxID], newItem.ID)
	if newItem.IdemKey != "" {
		s.byIdem[newItem.IdemKey] = newItem.ID
	}
	return newItem, nil
}

func (s *FileStore) UpdateLabels(_ context.Context, itemID string, labels map[string]string) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[itemID]
	if !ok {
		return Item{}, ErrNotFound
	}
	b, ok := s.boxes[item.BoxID]
	if !ok {
		return Item{}, ErrNotFound
	}
	item.Labels = cloneMap(labels)
	if err := s.writeItem(b.Key, item); err != nil {
		return Item{}, err
	}
	s.items[itemID] = item
	return item, nil
}

// DeleteItem soft-deletes an item on disk (item file remains for audit; the
// status field flips to "deleted" and IsLatest to false). Releases the item's
// IdemKey from the in-memory byIdem index so a subsequent InsertItem with the
// same idem creates a NEW item (D#9). The on-disk IdemKey field is preserved.
func (s *FileStore) DeleteItem(_ context.Context, itemID string) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[itemID]
	if !ok {
		return Item{}, ErrNotFound
	}
	if item.Status == "deleted" {
		return Item{}, ErrConflict
	}
	b, ok := s.boxes[item.BoxID]
	if !ok {
		return Item{}, ErrNotFound
	}
	item.Status = "deleted"
	item.IsLatest = false
	if err := s.writeItem(b.Key, item); err != nil {
		return Item{}, err
	}
	s.items[itemID] = item
	if item.IdemKey != "" {
		if cur, ok := s.byIdem[item.IdemKey]; ok && cur == itemID {
			delete(s.byIdem, item.IdemKey)
		}
	}
	return item, nil
}

// ListConsumes returns all ConsumeLog entries for itemID by scanning the
// owning box's consumes.jsonl. Order is the on-disk (chronological) order.
// Returns an empty (non-nil) slice when the file does not exist.
func (s *FileStore) ListConsumes(_ context.Context, itemID string) ([]ConsumeLog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[itemID]
	if !ok {
		return nil, ErrNotFound
	}
	boxRec, ok := s.boxes[item.BoxID]
	if !ok {
		return nil, ErrNotFound
	}
	path := s.consumesPath(boxRec.Key)
	out := make([]ConsumeLog, 0)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	for {
		var c ConsumeLog
		if err := dec.Decode(&c); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if c.ItemID == itemID {
			out = append(out, c)
		}
	}
	return out, nil
}

// Trace mirrors MemoryStore.Trace; FileStore keeps an in-memory index that
// is the authoritative source for symbol queries (Symbols are persisted in
// item files and reloaded on open).
func (s *FileStore) Trace(_ context.Context, boxID string, query SymbolQuery) ([]Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return traceItems(s.items, s.byBox, boxID, query), nil
}

// Neighbors mirrors MemoryStore.Neighbors against the in-memory index.
func (s *FileStore) Neighbors(_ context.Context, itemID string, hops int) (Subgraph, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return buildNeighbors(s.items, itemID, hops)
}

// writeItem persists a single item file atomically.
func (s *FileStore) writeItem(boxKey string, item Item) error {
	data, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.itemPath(boxKey, item.ID), data)
}

// tasksDir returns the directory that holds <task_id>.trace.jsonl files for
// a given box key. Mirrors the items/ + consumes.jsonl convention.
func (s *FileStore) tasksDir(boxKey string) string {
	return filepath.Join(s.boxDir(boxKey), "tasks")
}

// taskTracePath returns the absolute path to <task_id>.trace.jsonl. It does
// not create the parent directory — callers must MkdirAll before writing.
func (s *FileStore) taskTracePath(boxKey, taskID string) string {
	return filepath.Join(s.tasksDir(boxKey), taskID+".trace.jsonl")
}

// SetItemSymbols rewrites the on-disk item file with new Symbols.
func (s *FileStore) SetItemSymbols(_ context.Context, itemID string, symbols []Symbol) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[itemID]
	if !ok {
		return Item{}, ErrNotFound
	}
	b, ok := s.boxes[item.BoxID]
	if !ok {
		return Item{}, ErrNotFound
	}
	item.Symbols = cloneSymbols(symbols)
	if err := s.writeItem(b.Key, item); err != nil {
		return Item{}, err
	}
	s.items[itemID] = item
	return item, nil
}

// AppendTrace appends a single TraceStep as one JSON line to the per-task
// trace file. Open is O_APPEND|O_CREATE|O_WRONLY with fsync, mirroring
// RecordConsume's append + sync pattern.
//
// Step.Step is rewritten to the count of pre-existing lines so the on-disk
// numbering stays canonical even if the caller passed a stale index.
func (s *FileStore) AppendTrace(_ context.Context, taskID string, step TraceStep) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[taskID]
	if !ok {
		return ErrNotFound
	}
	boxRec, ok := s.boxes[item.BoxID]
	if !ok {
		return ErrNotFound
	}
	tdir := s.tasksDir(boxRec.Key)
	if err := os.MkdirAll(tdir, 0o755); err != nil {
		return err
	}
	path := s.taskTracePath(boxRec.Key, taskID)
	// Compute step number from the existing file size by counting lines —
	// scanning the file keeps the implementation stateless across processes.
	existing, err := countTraceLines(path)
	if err != nil {
		return err
	}
	step.Step = existing
	line, err := json.Marshal(step)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(line); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// ListTrace reads the per-task trace.jsonl into []TraceStep. A missing file
// returns the empty slice (no error) so callers can treat "no trace yet" as
// "empty list".
func (s *FileStore) ListTrace(_ context.Context, taskID string) ([]TraceStep, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[taskID]
	if !ok {
		return nil, ErrNotFound
	}
	boxRec, ok := s.boxes[item.BoxID]
	if !ok {
		return nil, ErrNotFound
	}
	path := s.taskTracePath(boxRec.Key, taskID)
	out := make([]TraceStep, 0)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	for {
		var step TraceStep
		if err := dec.Decode(&step); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		out = append(out, step)
	}
	return out, nil
}

// countTraceLines returns the count of newline-terminated JSON lines in a
// trace file. A non-existent file counts as zero. Caller already holds the
// FileStore mutex.
func countTraceLines(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	defer f.Close()
	n := 0
	dec := json.NewDecoder(f)
	for {
		var step TraceStep
		if err := dec.Decode(&step); err != nil {
			if err == io.EOF {
				return n, nil
			}
			return 0, err
		}
		n++
	}
}

// ListBoxes returns boxes that match every populated BoxFilter dimension.
// An empty filter returns every box. Caller-scoping is the Service layer's
// responsibility (R5.1 D#4).
func (s *FileStore) ListBoxes(_ context.Context, filter BoxFilter) ([]Box, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Box, 0, len(s.boxes))
	for _, b := range s.boxes {
		if !matchesBoxFilter(b, filter) {
			continue
		}
		out = append(out, b)
	}
	return out, nil
}

// AllBoxIDs satisfies the optional boxEnumerator capability used by
// Service.ScanOrphanTasks.
func (s *FileStore) AllBoxIDs(_ context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.boxes))
	for id := range s.boxes {
		out = append(out, id)
	}
	return out, nil
}

// compile-time check that FileStore satisfies Store.
var _ Store = (*FileStore)(nil)
