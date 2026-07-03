package worktree

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf8"
)

// FuzzSidecarJSONRoundtrip drives Sidecar's JSON encoding with arbitrary
// field values (bypassing the codec's file I/O entirely) and checks
// marshal->unmarshal identity: every field the fuzzer sets must survive a
// round trip unchanged. This is the wire-format half of the codec contract;
// FuzzReadSidecar below covers the "arbitrary bytes on disk" half.
func FuzzSidecarJSONRoundtrip(f *testing.F) {
	seeds := []Sidecar{
		{},
		{
			Name:            "feature/foo",
			Branch:          "feature/foo",
			BaseSHA:         "abc123",
			MergeTarget:     "main",
			OriginalRoot:    "/home/jesse/git/prime-radiant/serf",
			CreatorSession:  "01HXYZ",
			DelegateID:      "dlg_01HXYZ",
			WorktreeRemoved: true,
			TipSHAAtRemoval: "def456",
			CreatedAt:       "2026-07-03T12:00:00Z",
		},
		{Name: "\x00 weird\tbytes\n\"quote\"\\slash", CreatedAt: "not-really-rfc3339"},
	}
	for _, sc := range seeds {
		f.Add(sc.Name, sc.Branch, sc.BaseSHA, sc.MergeTarget, sc.OriginalRoot,
			sc.CreatorSession, sc.DelegateID, sc.WorktreeRemoved, sc.TipSHAAtRemoval, sc.CreatedAt)
	}

	f.Fuzz(func(t *testing.T, name, branch, baseSHA, mergeTarget, originalRoot,
		creatorSession, delegateID string, worktreeRemoved bool, tipSHAAtRemoval, createdAt string) {
		// encoding/json replaces invalid UTF-8 with U+FFFD on marshal (Go
		// strings are arbitrary byte sequences; JSON strings are not) — that
		// substitution is documented json.Marshal behavior, not a codec bug,
		// so identity only holds for fields the fuzzer happened to generate
		// as valid UTF-8.
		for _, s := range []string{name, branch, baseSHA, mergeTarget, originalRoot, creatorSession, delegateID, tipSHAAtRemoval, createdAt} {
			if !utf8.ValidString(s) {
				return
			}
		}
		sc := Sidecar{
			Name:            name,
			Branch:          branch,
			BaseSHA:         baseSHA,
			MergeTarget:     mergeTarget,
			OriginalRoot:    originalRoot,
			CreatorSession:  creatorSession,
			DelegateID:      delegateID,
			WorktreeRemoved: worktreeRemoved,
			TipSHAAtRemoval: tipSHAAtRemoval,
			CreatedAt:       createdAt,
		}
		raw, err := json.Marshal(sc)
		if err != nil {
			t.Fatalf("json.Marshal(%+v): %v", sc, err)
		}
		var got Sidecar
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("json.Unmarshal(%s): %v", raw, err)
		}
		if got != sc {
			t.Fatalf("round trip mismatch: marshal(%+v) = %s, unmarshal -> %+v", sc, raw, got)
		}
	})
}

// FuzzReadSidecar writes arbitrary bytes to a sidecar-shaped path and drives
// ReadSidecar against them: whatever the bytes are, ReadSidecar must never
// panic — an error is the correct outcome for anything that isn't valid
// Sidecar JSON.
func FuzzReadSidecar(f *testing.F) {
	seeds := [][]byte{
		nil,
		{},
		[]byte("{}"),
		[]byte(`{"name":"a"}`),
		[]byte("not json at all"),
		[]byte(`{"name": `),
		[]byte(`{"worktree_removed": "not-a-bool"}`),
		[]byte("\x00\x01\x02\xff\xfe"),
		[]byte(`{"name":"a","branch":"a","base_sha":"x","original_root":"/r","creator_session":"s","created_at":"2026-01-01T00:00:00Z"}`),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, content []byte) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "a.json"), content, 0o644); err != nil {
			t.Fatalf("os.WriteFile: %v", err)
		}
		_, _ = ReadSidecar(dir, "a") // must not panic; error is fine
	})
}
