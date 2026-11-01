package schema

import (
	"path/filepath"
	"testing"
)

func TestSessionLayoutPaths(t *testing.T) {
	state := filepath.Join("state", "dir")
	sessions := filepath.Join(state, "sessions")
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"sessions dir", SessionsDir(state), sessions},
		{"transcript", SessionTranscriptPath(state, "sess_a"), filepath.Join(sessions, "sess_a.transcript.jsonl")},
		{"meta", SessionMetaPath(state, "sess_a"), filepath.Join(sessions, "sess_a.meta.json")},
		{"api log", SessionAPILogPath(state, "sess_a"), filepath.Join(sessions, "sess_a.api.jsonl")},
		{"event log", SessionLogPath(state, "sess_a"), filepath.Join(sessions, "sess_a.log.jsonl")},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

func TestSessionArtifactSuffixesCoverEveryFlatFile(t *testing.T) {
	have := map[string]bool{}
	for _, s := range SessionArtifactSuffixes {
		have[s] = true
	}
	for _, p := range []string{
		SessionTranscriptPath("s", "x"), SessionMetaPath("s", "x"),
		SessionAPILogPath("s", "x"), SessionLogPath("s", "x"),
		SessionMetaPath("s", "x") + ".lock",
	} {
		base := filepath.Base(p)
		found := false
		for suffix := range have {
			if len(base) > len(suffix) && base[len(base)-len(suffix):] == suffix {
				found = true
			}
		}
		if !found {
			t.Errorf("%s has no suffix in SessionArtifactSuffixes", base)
		}
	}
}
