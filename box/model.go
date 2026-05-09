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
	ErrNotFound   = errors.New("not found")
	ErrConflict   = errors.New("conflict")
	ErrForbidden  = errors.New("forbidden")
	ErrValidation = errors.New("validation")
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
	AllowedFormats []string `json:"allowed_formats"`
	MaxItems       int      `json:"max_items"`
}

type Item struct {
	ID          string            `json:"id"`
	BoxID       string            `json:"box_id"`
	IdemKey     string            `json:"idem_key"`
	Kind        string            `json:"kind"`
	SourceType  string            `json:"source_type"`
	SourceRef   map[string]string `json:"source_ref"`
	Labels      map[string]string `json:"labels"`
	LocationID  string            `json:"location_id,omitempty"`
	StorageURI  string            `json:"storage_uri"`
	Format      string            `json:"format"`
	Content     json.RawMessage   `json:"content,omitempty"`
	ContentHash string            `json:"content_hash"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Status      string            `json:"status"`
	StoredBy    string            `json:"stored_by,omitempty"`
	StoredAt    time.Time         `json:"stored_at"`
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
}

type BrowseFilter struct {
	Kind        string
	SourceRef   map[string]string
	Labels      map[string]string
	LocationIDs []string
	Since       *time.Time
	Until       *time.Time
	Limit       int
	Offset      int
}

type Summary struct {
	BoxID          string         `json:"box_id"`
	TotalItems     int            `json:"total_items"`
	ByKind         map[string]int `json:"by_kind"`
	BySourceType   map[string]int `json:"by_source_type"`
	LatestStoredAt *time.Time     `json:"latest_stored_at,omitempty"`
}

func DefaultPolicy() StoragePolicy {
	return StoragePolicy{AllowedFormats: []string{"json", "markdown", "text"}, MaxItems: 1000}
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
