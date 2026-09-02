package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/identifier"
)

// TestReadTranscriptNeutralMaterializedRetainedOptions covers the provider shape
// observed in 034HvTCI5LrwbM2ZZpBMqN. The semantically empty options must select
// the same retained-read mode and return the same result as their omission.
func TestReadTranscriptNeutralMaterializedRetainedOptions(t *testing.T) {
	t.Run("job default markdown", func(t *testing.T) {
		stateDir := t.TempDir()
		owner := identifier.MustNewSessionID()
		jobID := identifier.MustNewJobID(owner)
		seedLocalJobRecord(t, stateDir, owner, jobID, "/decoy", "ready\n", maxJobOutputRetentionBytes, true, int64(len("ready\n")), nil)
		deps := &toolDeps{stateDir: stateDir, sessionID: owner}
		ref := "job:" + jobID

		omitted, err := execReadTranscript(deps, map[string]any{"transcript_ref": ref})
		if err != nil {
			t.Fatalf("omitted options: %v", err)
		}
		materialized, err := execReadTranscript(deps, map[string]any{
			"transcript_ref": ref,
			"range":          "",
			"expand_turn":    float64(0),
			"format":         "markdown",
			"output_match":   "",
			"context_lines":  float64(0),
		})
		if err != nil {
			t.Fatalf("materialized defaults: %v", err)
		}
		if !sameReadResult(omitted, materialized) {
			t.Fatalf("materialized job defaults changed result:\nomitted=%#v\nmaterialized=%#v", omitted, materialized)
		}
	})

	t.Run("artifact default page", func(t *testing.T) {
		deps, ref := artifactTranscriptFixture(t, "ready\n")
		omitted, err := execReadTranscript(deps, map[string]any{"transcript_ref": ref})
		if err != nil {
			t.Fatalf("omitted options: %v", err)
		}
		materialized, err := execReadTranscript(deps, map[string]any{
			"transcript_ref": ref,
			"range":          nil,
			"expand_turn":    nil,
			"output_match":   "",
			"context_lines":  float64(0),
		})
		if err != nil {
			t.Fatalf("materialized defaults: %v", err)
		}
		if !sameReadResult(omitted, materialized) {
			t.Fatalf("materialized artifact defaults changed result:\nomitted=%#v\nmaterialized=%#v", omitted, materialized)
		}
	})
}

func TestNormalizeRetainedReadArgsNeutralValues(t *testing.T) {
	fields := []string{"range", "expand_turn", "format", "output_match", "context_lines"}
	tests := []struct {
		name        string
		ref         string
		args        map[string]any
		wantPresent []string
	}{
		{name: "session absent", ref: "current", args: map[string]any{}},
		{name: "session null", ref: "current", args: map[string]any{"range": nil, "expand_turn": nil, "format": nil, "output_match": nil, "context_lines": nil}, wantPresent: fields},
		{name: "session empty and defaults", ref: "current", args: map[string]any{"range": "", "expand_turn": float64(0), "format": "markdown", "output_match": "", "context_lines": float64(0)}, wantPresent: fields},
		{name: "session meaningful", ref: "current", args: map[string]any{"range": "1-2", "expand_turn": float64(1), "format": "outline", "output_match": "match", "context_lines": float64(1)}, wantPresent: fields},
		{name: "job absent", ref: "job:abc", args: map[string]any{}},
		{name: "job null", ref: "job:abc", args: map[string]any{"range": nil, "expand_turn": nil, "format": nil, "output_match": nil, "context_lines": nil}},
		{name: "job empty and defaults", ref: "job:abc", args: map[string]any{"range": "", "expand_turn": float64(0), "format": "markdown", "output_match": "", "context_lines": float64(0)}},
		{name: "job meaningful", ref: "job:abc", args: map[string]any{"range": "1-2", "expand_turn": float64(1), "format": "outline", "output_match": "match", "context_lines": float64(1)}, wantPresent: fields},
		{name: "artifact absent", ref: "artifact:abc", args: map[string]any{}},
		{name: "artifact null", ref: "artifact:abc", args: map[string]any{"range": nil, "expand_turn": nil, "format": nil, "output_match": nil, "context_lines": nil}, wantPresent: []string{"format"}},
		{name: "artifact empty and defaults", ref: "artifact:abc", args: map[string]any{"range": "", "expand_turn": float64(0), "format": "markdown", "output_match": "", "context_lines": float64(0)}, wantPresent: []string{"format"}},
		{name: "artifact meaningful", ref: "artifact:abc", args: map[string]any{"range": "1-2", "expand_turn": float64(1), "format": "outline", "output_match": "match", "context_lines": float64(1)}, wantPresent: fields},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			normalized := normalizeRetainedReadArgs(tc.args, tc.ref)
			want := make(map[string]bool, len(tc.wantPresent))
			for _, name := range tc.wantPresent {
				want[name] = true
			}
			for _, name := range fields {
				_, got := normalized[name]
				if got != want[name] {
					t.Fatalf("%s present = %t, want %t; normalized=%#v", name, got, want[name], normalized)
				}
			}
		})
	}
}

func TestNormalizeRetainedReadArgsPreservesExplicitZeroOffset(t *testing.T) {
	for _, ref := range []string{"job:abc", "artifact:abc"} {
		t.Run(ref, func(t *testing.T) {
			normalized := normalizeRetainedReadArgs(map[string]any{"transcript_ref": ref, "offset_bytes": float64(0)}, ref)
			if _, present := normalized["offset_bytes"]; !present {
				t.Fatal("normalization removed explicit offset_bytes=0")
			}
			_, operation, err := parseRetainedReadArgs(normalized)
			if err != nil || operation != retainedReadPage {
				t.Fatalf("offset_bytes=0 parsed as operation=%d err=%v, want retained page", operation, err)
			}
		})
	}
}

func TestRetainedReadIncompatibleArgsDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind string
		args map[string]any
		want []string
	}{
		{
			name: "job reports every meaningful incompatibility",
			kind: "job",
			args: map[string]any{"range": "1-2", "expand_turn": float64(1), "format": "outline"},
			want: []string{`range="1-2"`, "expand_turn=1", `format="outline"`, `{"transcript_ref":"job:<job_id>"}`},
		},
		{
			name: "artifact reports every meaningful incompatibility",
			kind: "artifact",
			args: map[string]any{"range": "1-2", "expand_turn": float64(1), "format": "markdown"},
			want: []string{`range="1-2"`, "expand_turn=1", `format="markdown"`, `{"transcript_ref":"artifact:<id>"}`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if tc.kind == "job" {
				err = validateJobReadArgs(tc.args, retainedReadDefault)
			} else {
				err = validateArtifactReadArgs(tc.args, retainedReadDefault)
			}
			if err == nil {
				t.Fatal("validation succeeded, want diagnostic error")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %q, want %q", err, want)
				}
			}
		})
	}
}

func TestArtifactExplicitFormatsRemainRejected(t *testing.T) {
	for _, format := range []any{nil, "", "markdown", "outline"} {
		if err := validateArtifactReadArgs(map[string]any{"format": format}, retainedReadDefault); err == nil {
			t.Fatalf("format %#v was accepted for artifact ref", format)
		}
	}
}

func sameReadResult(a, b any) bool {
	encodedA, err := json.Marshal(a)
	if err != nil {
		return false
	}
	encodedB, err := json.Marshal(b)
	if err != nil {
		return false
	}
	var decodedA, decodedB any
	if json.Unmarshal(encodedA, &decodedA) != nil || json.Unmarshal(encodedB, &decodedB) != nil {
		return false
	}
	return reflect.DeepEqual(decodedA, decodedB)
}
