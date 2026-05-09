package box

import (
	"context"
	"encoding/json"
	"fmt"
)

type Service struct {
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

func (s *Service) CreateBox(ctx context.Context, req CreateBoxRequest) (Box, error) {
	if req.Key == "" {
		return Box{}, fmt.Errorf("%w: key is required", ErrValidation)
	}
	if req.OwnerType == "" {
		req.OwnerType = "standalone"
	}
	if req.OwnerID == "" {
		req.OwnerID = "anonymous"
	}
	if req.StoragePolicy.MaxItems == 0 {
		req.StoragePolicy = DefaultPolicy()
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
}

func (s *Service) SealBox(ctx context.Context, callerID, boxID string) error {
	b, err := s.store.GetBox(ctx, boxID)
	if err != nil {
		return err
	}
	if b.OwnerID != callerID {
		return ErrForbidden
	}
	return s.store.SealBox(ctx, boxID)
}

func (s *Service) Store(ctx context.Context, callerID, boxID string, req StoreRequest) (Item, error) {
	b, err := s.store.GetBox(ctx, boxID)
	if err != nil {
		return Item{}, err
	}
	if b.Status != "active" {
		return Item{}, ErrConflict
	}
	if b.OwnerID != callerID {
		return Item{}, ErrForbidden
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
	})
}

func (s *Service) Browse(ctx context.Context, boxID string, filter BrowseFilter) ([]Item, error) {
	if err := ValidateLabels(filter.Labels); err != nil {
		return nil, err
	}
	return s.store.Browse(ctx, boxID, filter)
}

func (s *Service) GetItem(ctx context.Context, callerID, itemID string) (Item, error) {
	item, err := s.store.GetItem(ctx, itemID)
	if err != nil {
		return Item{}, err
	}
	_ = s.store.RecordConsume(ctx, ConsumeLog{
		ID:           NewID("consume_"),
		ItemID:       itemID,
		ConsumerType: "user",
		ConsumerID:   callerID,
		ConsumedAt:   nowUTC(),
	})
	_ = s.store.MarkConsumed(ctx, itemID)
	return item, nil
}

func (s *Service) Summary(ctx context.Context, boxID string) (Summary, error) {
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
