package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/windborneos/box-model/box"
)

func main() {
	ctx := context.Background()
	svc := box.NewService(box.NewMemoryStore())

	b, err := svc.CreateBox(ctx, box.CreateBoxRequest{
		Key:       "demo",
		OwnerType: "standalone",
		OwnerID:   "demo-owner",
	})
	if err != nil {
		log.Fatal(err)
	}

	_, err = svc.Store(ctx, "demo-owner", b.ID, box.StoreRequest{
		IdemKey:    "doc-1/v1",
		Kind:       "document",
		SourceType: "external",
		SourceRef:  map[string]string{"url": "https://example.com/spec"},
		Labels: map[string]string{
			"__op:area_id": "demo",
			"__sem:topic": "box-model",
		},
		LocationID: "loc-box-model",
		StorageURI: "blob://sha256:demo",
		Format:     "json",
		Content:    json.RawMessage(`{"title":"Box Model","body":"A multidimensional index."}`),
	})
	if err != nil {
		log.Fatal(err)
	}

	items, err := svc.Browse(ctx, b.ID, box.BrowseFilter{
		Labels: map[string]string{"__sem:topic": "box-model"},
	})
	if err != nil {
		log.Fatal(err)
	}

	out, _ := json.MarshalIndent(map[string]any{"items": items}, "", "  ")
	fmt.Println(string(out))
}
