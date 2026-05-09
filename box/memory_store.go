package box

import (
	"context"
	"sort"
	"sync"
	"time"
)

type Store interface {
	CreateBox(context.Context, Box) (Box, error)
	GetBox(context.Context, string) (Box, error)
	SealBox(context.Context, string) error
	CountItems(context.Context, string) (int, error)
	InsertItem(context.Context, Item) (Item, error)
	Browse(context.Context, string, BrowseFilter) ([]Item, error)
	GetItem(context.Context, string) (Item, error)
	RecordConsume(context.Context, ConsumeLog) error
	MarkConsumed(context.Context, string) error
}

type MemoryStore struct {
	mu       sync.RWMutex
	boxes    map[string]Box
	items    map[string]Item
	byBox    map[string][]string
	byIdem   map[string]string
	consumes []ConsumeLog
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		boxes:  map[string]Box{},
		items:  map[string]Item{},
		byBox:  map[string][]string{},
		byIdem: map[string]string{},
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
