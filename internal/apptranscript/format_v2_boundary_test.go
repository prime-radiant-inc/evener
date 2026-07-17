package apptranscript

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/appwire"
)

func requireTurnsFromFile(t testing.TB, path string, maxLineBytes int, project EntryProjector) []appwire.Turn {
	t.Helper()
	turns, err := TurnsFromFile(path, maxLineBytes, project)
	if err != nil {
		t.Fatalf("TurnsFromFile: %v", err)
	}
	return turns
}

func TestSemanticReadersRejectUnknownTranscriptFields(t *testing.T) {
	header := `{"kind":"header","format_version":2}`
	tests := []struct {
		name string
		body string
	}{
		{"header", `{"kind":"header","format_version":2,"unknown":true}` + "\n"},
		{"entry", header + "\n" + `{"kind":"entry","seq":0,"turn":{},"unknown":true}` + "\n"},
		{"turn", header + "\n" + `{"kind":"entry","seq":0,"turn":{"unknown":true}}` + "\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "transcript.jsonl")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := ScanPrelude(path, 1<<20); err == nil {
				t.Fatal("ScanPrelude accepted an unknown field")
			}
			if turns, err := TurnsFromFile(path, 1<<20, nil); err == nil || turns != nil {
				t.Fatalf("TurnsFromFile = (%v, %v), want no turns and an error", turns, err)
			}
			if turns, cursor, err := NewTurnCache().LatestFromFile(path, 1<<20, 1, boundedTestProjector); err == nil || turns != nil || cursor != "" {
				t.Fatalf("LatestFromFile = (%v, %q, %v), want no turns and an error", turns, cursor, err)
			}
		})
	}
}

func TestSemanticFullAndIndexedReadersShareLineFraming(t *testing.T) {
	header := `{"kind":"header","format_version":2}`
	entry := `{"kind":"entry","seq":0,"turn":{"kind":"USER_INPUT"}}`
	maxLineBytes := max(len(header), len(entry))

	t.Run("exact max complete accepted and arbitrary tail discarded", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "transcript.jsonl")
		body := header + "\n" + entry + "\n" + strings.Repeat("x", maxLineBytes*1000)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ScanPrelude(path, maxLineBytes); err != nil {
			t.Fatalf("ScanPrelude: %v", err)
		}
		if _, err := TurnsFromFile(path, maxLineBytes, nil); err != nil {
			t.Fatalf("TurnsFromFile: %v", err)
		}
		if _, _, err := NewTurnCache().LatestFromFile(path, maxLineBytes, 1, boundedTestProjector); err != nil {
			t.Fatalf("LatestFromFile: %v", err)
		}
	})

	t.Run("max plus one complete rejected", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "transcript.jsonl")
		body := header + "\n" + strings.Repeat("x", maxLineBytes+1) + "\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ScanPrelude(path, maxLineBytes); err == nil {
			t.Fatal("ScanPrelude accepted a max+1 complete record")
		}
		if _, _, err := NewTurnCache().LatestFromFile(path, maxLineBytes, 1, boundedTestProjector); err == nil {
			t.Fatal("LatestFromFile accepted a max+1 complete record")
		}
	})
}

func requireLatestFromFile(t testing.TB, cache *TurnCache, path string, maxLineBytes, limit int, project BoundedEntryProjector) ([]appwire.Turn, string) {
	t.Helper()
	turns, cursor, err := cache.LatestFromFile(path, maxLineBytes, limit, project)
	if err != nil {
		t.Fatalf("LatestFromFile: %v", err)
	}
	return turns, cursor
}

func requirePageFromFile(t testing.TB, cache *TurnCache, path string, maxLineBytes int, cursor string, limit int, project BoundedEntryProjector) FilePage {
	t.Helper()
	page, err := cache.PageFromFile(path, maxLineBytes, cursor, limit, project)
	if err != nil {
		t.Fatalf("PageFromFile: %v", err)
	}
	return page
}

func requireTurnCountFromFile(t testing.TB, cache *TurnCache, path string, maxLineBytes int, project BoundedEntryProjector) int {
	t.Helper()
	count, err := cache.TurnCountFromFile(path, maxLineBytes, project)
	if err != nil {
		t.Fatalf("TurnCountFromFile: %v", err)
	}
	return count
}

func TestSemanticReadersRejectUnsupportedTranscriptFormat(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"version one", `{"kind":"header","format_version":1}` + "\n"},
		{"missing version", `{"kind":"header"}` + "\n"},
		{"mixed api call", `{"kind":"header","format_version":2}` + "\n" + `{"kind":"api_call"}` + "\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "transcript.jsonl")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			if _, err := ScanPrelude(path, 1<<20); !errors.Is(err, transcript.ErrUnsupportedFormat) {
				t.Fatalf("ScanPrelude error = %v, want ErrUnsupportedFormat", err)
			}
			if turns, err := TurnsFromFile(path, 1<<20, nil); !errors.Is(err, transcript.ErrUnsupportedFormat) || turns != nil {
				t.Fatalf("TurnsFromFile = (%v, %v), want nil ErrUnsupportedFormat", turns, err)
			}
		})
	}
}

func TestSemanticReadersIgnoreInvalidUnreadableAndSentinelSiblingAPILog(t *testing.T) {
	valid := `{"kind":"header","format_version":2}` + "\n" +
		`{"kind":"entry","seq":0,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[{"kind":"text","text":"semantic sentinel"}]}}}` + "\n"
	tests := []struct {
		name     string
		contents []byte
		mode     os.FileMode
	}{
		{name: "invalid", contents: []byte("not-json\n"), mode: 0o600},
		{name: "unreadable", contents: []byte(`{"kind":"api_attempt","request":"api sentinel"}` + "\n"), mode: 0o000},
		{name: "sentinel", contents: []byte(`{"kind":"api_attempt","request":"api sentinel"}` + "\n"), mode: 0o600},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "session.transcript.jsonl")
			if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
				t.Fatal(err)
			}
			apiPath := filepath.Join(dir, "session.api.jsonl")
			if err := os.WriteFile(apiPath, tt.contents, tt.mode); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(apiPath, 0o600) })

			if _, err := ScanPrelude(path, 1<<20); err != nil {
				t.Fatalf("ScanPrelude consulted sibling API log: %v", err)
			}
			if _, err := TurnsFromFile(path, 1<<20, nil); err != nil {
				t.Fatalf("TurnsFromFile consulted sibling API log: %v", err)
			}
		})
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "session.transcript.jsonl")
	if err := os.WriteFile(path, []byte(`{"kind":"header","format_version":1}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.api.jsonl"), []byte(`{"kind":"api_attempt","response":"sentinel"}`+"\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := TurnsFromFile(path, 1<<20, nil); !errors.Is(err, transcript.ErrUnsupportedFormat) {
		t.Fatalf("TurnsFromFile error = %v, want transcript ErrUnsupportedFormat independent of sibling API log", err)
	}
}

func TestTurnCacheRejectsUnsupportedTranscriptWithoutStaleState(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"version one", `{"kind":"header","format_version":1}` + "\n"},
		{"mixed api call", `{"kind":"header","format_version":2}` + "\n" + `{"kind":"api_call","seq":1}` + "\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			valid := `{"kind":"header","format_version":2}` + "\n" +
				`{"kind":"entry","seq":1,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[{"kind":"text","text":"valid"}]},"timestamp":"2026-07-16T00:00:00Z"}}` + "\n"

			for _, bounded := range []bool{false, true} {
				t.Run(map[bool]string{false: "full", true: "bounded"}[bounded], func(t *testing.T) {
					path := filepath.Join(t.TempDir(), "transcript.jsonl")
					if err := os.WriteFile(path, []byte(valid), 0o644); err != nil {
						t.Fatal(err)
					}
					cache := NewTurnCache()
					if bounded {
						if _, _, err := cache.LatestFromFile(path, 1<<20, 1, boundedTestProjector); err != nil {
							t.Fatalf("prime bounded cache: %v", err)
						}
					} else if _, err := cache.TurnsFromFile(path, 1<<20, sequentialTestProjector()); err != nil {
						t.Fatalf("prime full cache: %v", err)
					}
					indexPath := path + ".appwire-index.json"
					journalPath := indexPath + ".journal"
					if err := os.WriteFile(journalPath, []byte("stale"), 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
						t.Fatal(err)
					}

					if bounded {
						turns, cursor, err := cache.LatestFromFile(path, 1<<20, 1, boundedTestProjector)
						if !errors.Is(err, transcript.ErrUnsupportedFormat) || turns != nil || cursor != "" {
							t.Fatalf("LatestFromFile = (%v, %q, %v), want nil empty ErrUnsupportedFormat", turns, cursor, err)
						}
					} else {
						turns, err := cache.TurnsFromFile(path, 1<<20, sequentialTestProjector())
						if !errors.Is(err, transcript.ErrUnsupportedFormat) || turns != nil {
							t.Fatalf("TurnsFromFile = (%v, %v), want nil ErrUnsupportedFormat", turns, err)
						}
					}
					if _, ok := cache.entries[path]; ok {
						t.Fatal("unsupported transcript retained a cache entry")
					}
					for _, stalePath := range []string{indexPath, journalPath} {
						if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
							t.Fatalf("stale cache artifact %s remains: %v", stalePath, err)
						}
					}
				})
			}
		})
	}
}

func TestBoundedReadersRequireACompleteV2Header(t *testing.T) {
	t.Run("empty transcript", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "transcript.jsonl")
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}

		turns, cursor, err := NewTurnCache().LatestFromFile(path, 1<<20, 1, boundedTestProjector)
		if !errors.Is(err, transcript.ErrUnsupportedFormat) || turns != nil || cursor != "" {
			t.Fatalf("LatestFromFile = (%v, %q, %v), want nil empty ErrUnsupportedFormat", turns, cursor, err)
		}

		page, err := NewTurnCache().PageFromFile(path, 1<<20, "", 1, boundedTestProjector)
		if !errors.Is(err, transcript.ErrUnsupportedFormat) || page.Turns != nil || page.NextCursor != "" {
			t.Fatalf("PageFromFile = (%+v, %v), want empty ErrUnsupportedFormat", page, err)
		}

		count, err := NewTurnCache().TurnCountFromFile(path, 1<<20, boundedTestProjector)
		if !errors.Is(err, transcript.ErrUnsupportedFormat) || count != 0 {
			t.Fatalf("TurnCountFromFile = (%d, %v), want zero ErrUnsupportedFormat", count, err)
		}
	})

	t.Run("header only v2 transcript", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "transcript.jsonl")
		if err := os.WriteFile(path, []byte(`{"kind":"header","format_version":2}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		cache := NewTurnCache()
		turns, cursor := requireLatestFromFile(t, cache, path, 1<<20, 1, boundedTestProjector)
		if turns != nil || cursor != "" {
			t.Fatalf("LatestFromFile = (%v, %q), want nil empty", turns, cursor)
		}
		page := requirePageFromFile(t, cache, path, 1<<20, "", 1, boundedTestProjector)
		if page.Turns != nil || page.NextCursor != "" {
			t.Fatalf("PageFromFile = %+v, want empty", page)
		}
		if count := requireTurnCountFromFile(t, cache, path, 1<<20, boundedTestProjector); count != 0 {
			t.Fatalf("TurnCountFromFile = %d, want zero", count)
		}
	})
}
