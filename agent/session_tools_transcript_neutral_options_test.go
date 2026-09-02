package agent

import (
	"context"
	"encoding/json"
	"maps"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
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
		materializedArgs := map[string]any{
			"transcript_ref": ref,
			"range":          "",
			"expand_turn":    float64(0),
			"format":         "markdown",
			"output_match":   "",
			"context_lines":  float64(0),
		}
		normalized, _ := normalizeRetainedReadArgsForRepair(materializedArgs)
		materialized, err := execReadTranscript(deps, normalized)
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
		materializedArgs := map[string]any{
			"transcript_ref": ref,
			"range":          nil,
			"expand_turn":    nil,
			"output_match":   "",
			"context_lines":  float64(0),
		}
		normalized, _ := normalizeRetainedReadArgsForRepair(materializedArgs)
		materialized, err := execReadTranscript(deps, normalized)
		if err != nil {
			t.Fatalf("materialized defaults: %v", err)
		}
		if !sameReadResult(omitted, materialized) {
			t.Fatalf("materialized artifact defaults changed result:\nomitted=%#v\nmaterialized=%#v", omitted, materialized)
		}
	})
}

func TestSessionExecToolRepairsMaterializedRetainedReadDefaults(t *testing.T) {
	t.Run("job defaults reach the registered executor", func(t *testing.T) {
		stateDir := t.TempDir()
		sess := newSession(t, withConfig(SessionConfig{
			StateDir:         stateDir,
			MaxSubagentDepth: 1,
			NoProjectPrompts: true,
			testOnly: testConfig{
				skipGitSnapshot:     true,
				minimalSystemPrompt: true,
				noSyncJobStore:      true,
			},
		}))
		jobID := identifier.MustNewJobID(sess.ID())
		seedLocalJobRecord(t, stateDir, sess.ID(), jobID, "/decoy", "ready\n", maxJobOutputRetentionBytes, true, int64(len("ready\n")), nil)
		repairedCh := drainRepairedEvents(sess)

		result := execReadTranscriptThroughSession(t, sess, "job-defaults", map[string]any{
			"transcript_ref": "job:" + jobID,
			"range":          "",
			"expand_turn":    float64(0),
			"format":         "markdown",
			"output_match":   "",
			"context_lines":  float64(0),
		})
		if result.IsError {
			t.Fatalf("registered job read failed: %s", result.Output)
		}
		nullableResult := execReadTranscriptThroughSession(t, sess, "job-null-defaults", map[string]any{
			"transcript_ref": "job:" + jobID,
			"range":          nil,
			"expand_turn":    nil,
			"format":         nil,
		})
		if nullableResult.IsError {
			t.Fatalf("registered nullable job read failed: %s", nullableResult.Output)
		}
		repaired := closeAndDrainRepairedEvents(sess, repairedCh)
		assertReadRepairTelemetry(t, repaired, "job-defaults", "range", "expand_turn", "format", "output_match", "context_lines")
		assertReadRepairTelemetry(t, repaired, "job-null-defaults", "range", "expand_turn", "format")
	})

	t.Run("artifact defaults reach the registered executor while format remains explicit", func(t *testing.T) {
		sess := newArtifactTestRoot(t)
		ref, err := sess.artifactStore.Put([]byte("ready\n"))
		if err != nil {
			t.Fatalf("put artifact: %v", err)
		}
		repairedCh := drainRepairedEvents(sess)
		result := execReadTranscriptThroughSession(t, sess, "artifact-defaults", map[string]any{
			"transcript_ref": ref,
			"range":          nil,
			"expand_turn":    nil,
			"output_match":   "",
			"context_lines":  float64(0),
		})
		if result.IsError {
			t.Fatalf("registered artifact read failed: %s", result.Output)
		}
		formatResult := execReadTranscriptThroughSession(t, sess, "artifact-null-format", map[string]any{"transcript_ref": ref, "format": nil})
		if !formatResult.IsError || !strings.Contains(formatResult.Output, "format is not supported for artifact") {
			t.Fatalf("artifact explicit null format = %#v, want artifact format rejection", formatResult)
		}
		assertReadRepairTelemetry(t, closeAndDrainRepairedEvents(sess, repairedCh), "artifact-defaults", "range", "expand_turn", "output_match", "context_lines")
	})
}

func TestReadTranscriptCompositeInvalidJobReportsParseAndModeFields(t *testing.T) {
	stateDir := t.TempDir()
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	seedLocalJobRecord(t, stateDir, owner, jobID, "/decoy", "ready\n", maxJobOutputRetentionBytes, true, int64(len("ready\n")), nil)
	_, err := execReadTranscript(&toolDeps{stateDir: stateDir, sessionID: owner}, map[string]any{
		"transcript_ref": "job:" + jobID,
		"range":          "1-2",
		"expand_turn":    float64(1),
		"format":         "outline",
		"context_lines":  float64(-1),
	})
	if err == nil {
		t.Fatal("composite invalid job read succeeded")
	}
	for _, want := range []string{`range="1-2"`, "expand_turn=1", `format="outline"`, "context_lines=-1", `{"transcript_ref":"job:<job_id>"}`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want %q", err, want)
		}
	}
}

func TestSessionExecToolNormalizesCoercedRetainedReadDefaults(t *testing.T) {
	t.Run("job", func(t *testing.T) {
		stateDir := t.TempDir()
		sess := newSession(t, withConfig(SessionConfig{
			StateDir:         stateDir,
			MaxSubagentDepth: 1,
			NoProjectPrompts: true,
			testOnly: testConfig{
				skipGitSnapshot:     true,
				minimalSystemPrompt: true,
				noSyncJobStore:      true,
			},
		}))
		jobID := identifier.MustNewJobID(sess.ID())
		seedLocalJobRecord(t, stateDir, sess.ID(), jobID, "/decoy", "ready\n", maxJobOutputRetentionBytes, true, int64(len("ready\n")), nil)
		ref := "job:" + jobID
		repairedCh := drainRepairedEvents(sess)

		defaults := execReadTranscriptThroughSession(t, sess, "job-string-zeros", map[string]any{"transcript_ref": ref, "expand_turn": "0", "output_match": "", "context_lines": "0"})
		if defaults.IsError {
			t.Fatalf("stringified job defaults failed: %s", defaults.Output)
		}
		search := execReadTranscriptThroughSession(t, sess, "job-string-search-context", map[string]any{"transcript_ref": ref, "output_match": "ready", "context_lines": "1"})
		if search.IsError {
			t.Fatalf("stringified job search context failed: %s", search.Output)
		}
		meaningful := execReadTranscriptThroughSession(t, sess, "job-string-nonzero-expand", map[string]any{"transcript_ref": ref, "expand_turn": "1"})
		if !meaningful.IsError {
			t.Fatal("nonzero job expand_turn succeeded")
		}
		assertReadRepairChanges(t, closeAndDrainRepairedEvents(sess, repairedCh), "job-string-zeros", "coerce_type:expand_turn:", "coerce_type:context_lines:", "normalize_default:output_match:", "normalize_default:expand_turn:", "normalize_default:context_lines:")
	})

	t.Run("artifact", func(t *testing.T) {
		sess := newArtifactTestRoot(t)
		ref, err := sess.artifactStore.Put([]byte("ready\n"))
		if err != nil {
			t.Fatalf("put artifact: %v", err)
		}
		repairedCh := drainRepairedEvents(sess)

		defaults := execReadTranscriptThroughSession(t, sess, "artifact-string-zeros", map[string]any{"transcript_ref": ref, "expand_turn": "0", "output_match": "", "context_lines": "0"})
		if defaults.IsError {
			t.Fatalf("stringified artifact defaults failed: %s", defaults.Output)
		}
		search := execReadTranscriptThroughSession(t, sess, "artifact-string-search-context", map[string]any{"transcript_ref": ref, "output_match": "ready", "context_lines": "1"})
		if search.IsError {
			t.Fatalf("stringified artifact search context failed: %s", search.Output)
		}
		meaningful := execReadTranscriptThroughSession(t, sess, "artifact-string-nonzero-expand", map[string]any{"transcript_ref": ref, "expand_turn": "1"})
		if !meaningful.IsError {
			t.Fatal("nonzero artifact expand_turn succeeded")
		}
		assertReadRepairChanges(t, closeAndDrainRepairedEvents(sess, repairedCh), "artifact-string-zeros", "coerce_type:expand_turn:", "coerce_type:context_lines:", "normalize_default:output_match:", "normalize_default:expand_turn:", "normalize_default:context_lines:")
	})
}

func execReadTranscriptThroughSession(t *testing.T, sess *Session, id string, args map[string]any) tool.ExecResult {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return sess.execTool(context.Background(), llm.ToolCallData{ID: id, Name: "read_transcript", Arguments: encoded}, "")
}

func closeAndDrainRepairedEvents(sess *Session, repairedCh <-chan []events.ToolCallRepairedData) []events.ToolCallRepairedData {
	sess.Close()
	return <-repairedCh
}

func assertReadRepairTelemetry(t *testing.T, repaired []events.ToolCallRepairedData, callID string, fields ...string) {
	t.Helper()
	for _, event := range repaired {
		if event.ToolName != "read_transcript" || event.CallID != callID {
			continue
		}
		if len(event.Changes) != len(fields) {
			t.Fatalf("repair changes = %v, want one per field %v", event.Changes, fields)
		}
		for _, field := range fields {
			found := false
			for _, change := range event.Changes {
				if strings.HasPrefix(change, "normalize_default:"+field+":") {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("repair changes = %v, want normalized %q", event.Changes, field)
			}
		}
		return
	}
	t.Fatalf("no read_transcript repair event for %q: %+v", callID, repaired)
}

func assertReadRepairChanges(t *testing.T, repaired []events.ToolCallRepairedData, callID string, want ...string) {
	t.Helper()
	for _, event := range repaired {
		if event.ToolName != "read_transcript" || event.CallID != callID {
			continue
		}
		if len(event.Changes) != len(want) {
			t.Fatalf("repair changes = %v, want one change per %v", event.Changes, want)
		}
		for _, prefix := range want {
			count := 0
			for _, change := range event.Changes {
				if strings.HasPrefix(change, prefix) {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("repair changes = %v, want exactly one %q", event.Changes, prefix)
			}
		}
		return
	}
	t.Fatalf("no read_transcript repair event for %q: %+v", callID, repaired)
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
		{name: "job null", ref: "job:abc", args: map[string]any{"range": nil, "expand_turn": nil, "format": nil, "output_match": nil, "context_lines": nil}, wantPresent: []string{"output_match"}},
		{name: "job empty and defaults", ref: "job:abc", args: map[string]any{"range": "", "expand_turn": float64(0), "format": "markdown", "output_match": "", "context_lines": float64(0)}},
		{name: "job meaningful", ref: "job:abc", args: map[string]any{"range": "1-2", "expand_turn": float64(1), "format": "outline", "output_match": "match", "context_lines": float64(1)}, wantPresent: fields},
		{name: "artifact absent", ref: "artifact:abc", args: map[string]any{}},
		{name: "artifact null", ref: "artifact:abc", args: map[string]any{"range": nil, "expand_turn": nil, "format": nil, "output_match": nil, "context_lines": nil}, wantPresent: []string{"format", "output_match"}},
		{name: "artifact empty and defaults", ref: "artifact:abc", args: map[string]any{"range": "", "expand_turn": float64(0), "format": "markdown", "output_match": "", "context_lines": float64(0)}, wantPresent: []string{"format"}},
		{name: "artifact meaningful", ref: "artifact:abc", args: map[string]any{"range": "1-2", "expand_turn": float64(1), "format": "outline", "output_match": "match", "context_lines": float64(1)}, wantPresent: fields},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			normalized, _ := normalizeRetainedReadArgsForRepair(mapWithTranscriptRef(tc.args, tc.ref))
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
			normalized, _ := normalizeRetainedReadArgsForRepair(map[string]any{"transcript_ref": ref, "offset_bytes": float64(0)})
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

func mapWithTranscriptRef(args map[string]any, ref string) map[string]any {
	withRef := make(map[string]any, len(args)+1)
	maps.Copy(withRef, args)
	withRef["transcript_ref"] = ref
	return withRef
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
