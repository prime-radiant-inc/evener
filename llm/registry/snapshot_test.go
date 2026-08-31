package registry

import (
	"testing"
	"time"
)

func TestEmbeddedSnapshot_ConvertsAndCarriesMeta(t *testing.T) {
	raw, meta, err := EmbeddedSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	provs, err := FromModelsDev(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(provs) < 150 {
		t.Fatalf("embedded snapshot has %d providers; expected the full models.dev catalog", len(provs))
	}
	found := false
	for _, p := range provs {
		if p.ID == "groq" {
			found = true
		}
	}
	if !found {
		t.Fatal("groq missing from the embedded snapshot")
	}
	if meta.FetchedAt.IsZero() || meta.FetchedAt.After(time.Now().Add(24*time.Hour)) {
		t.Fatalf("meta.FetchedAt = %v", meta.FetchedAt)
	}
	if meta.Source != "https://models.dev/api.json" {
		t.Fatalf("meta.Source = %q", meta.Source)
	}
}

func TestParseMeta(t *testing.T) {
	m, err := ParseMeta([]byte(`{"fetched_at":"2026-08-28T22:03:00Z","etag":"W/\"abc\"","source":"https://models.dev/api.json"}`))
	if err != nil {
		t.Fatal(err)
	}
	if m.Etag != `W/"abc"` || m.FetchedAt.Year() != 2026 {
		t.Fatalf("ParseMeta = %+v", m)
	}
	if _, err := ParseMeta([]byte(`{"fetched_at":"not a time"}`)); err == nil {
		t.Fatal("bad fetched_at must error")
	}
}
