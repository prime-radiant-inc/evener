package interactiveartifacts

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestLargeSavedResultAndWholeEntryPagination(t *testing.T) {
	s, hash, _ := setupStore(t, StoreOptions{})
	ctx := context.Background()
	title := strings.Repeat("<", (1<<20)-1024)
	source := strings.Repeat("<", (1<<20)-1024)
	// Raw legal input deliberately avoids optional HTML escaping.
	raw := []byte(`{"mutationId":"large","title":"` + title + `","summary":"s","html":"` + source + `"}`)
	receipt, err := s.Publish(ctx, hash, raw, PublicationOrigin{})
	requireNoError(t, err)
	state := `{"v":"` + strings.Repeat("x", (256<<10)-16) + `"}`
	_, err = s.SaveState(ctx, hash, saveJSON(receipt.ArtifactID, "state", 1, 1, state))
	requireNoError(t, err)
	result := readState(t, s, hash, receipt.ArtifactID)
	encoded, err := json.Marshal(result)
	requireNoError(t, err)
	if len(encoded) <= 2<<20 {
		t.Fatal("fixture did not exceed request limit")
	}
	requireNoError(t, ValidateResult("artifact_read", encoded))
	for i := range 4 {
		_, err = s.Publish(ctx, hash, []byte(fmt.Sprintf(`{"mutationId":"list-%d","title":%q,"summary":"s","html":"ok"}`, i, title)), PublicationOrigin{})
		requireNoError(t, err)
	}
	cursor := ""
	seen := map[string]bool{}
	pages := 0
	for {
		args, _ := json.Marshal(ListRequest{Limit: 100, Cursor: cursor})
		page, err := s.List(ctx, hash, args)
		requireNoError(t, err)
		wire, _ := json.Marshal(page)
		if len(wire) > 16<<20-4096 {
			t.Fatalf("unbounded page: %d bytes", len(wire))
		}
		for _, item := range page.Artifacts {
			if seen[item.ArtifactID] || item.Title != title {
				t.Fatal("lost/duplicated/truncated entry")
			}
			seen[item.ArtifactID] = true
		}
		pages++
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != 5 || pages < 2 {
		t.Fatalf("pagination: %d entries / %d pages", len(seen), pages)
	}
}
