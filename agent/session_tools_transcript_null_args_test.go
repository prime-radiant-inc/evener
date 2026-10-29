package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/identifier"
)

// strictSchemaPlaceholders are argument shapes from models whose strict tool
// schemas make them emit every key: zero or the string "null" for a field they
// mean to leave unset. Each must read exactly like omitting the field.
var strictSchemaPlaceholders = []struct {
	name string
	args map[string]any
}{
	{"offset zero", map[string]any{"offset_bytes": float64(0)}},
	{"context zero", map[string]any{"context_lines": float64(0)}},
	{"output_match null string", map[string]any{"output_match": "null"}},
	{"output_match empty", map[string]any{"output_match": ""}},
	{"output_match null string with context zero", map[string]any{"output_match": "null", "context_lines": float64(0)}},
	{"format null string", map[string]any{"format": "null"}},
	{"range null string", map[string]any{"range": "null"}},
	{"expand_turn null string", map[string]any{"expand_turn": "null"}},
	{"offset null string", map[string]any{"offset_bytes": "null"}},
	{"context null string", map[string]any{"context_lines": "null"}},
	{"every key", map[string]any{
		"format": "null", "range": "null", "expand_turn": "null",
		"offset_bytes": float64(0), "output_match": "null", "context_lines": float64(0),
	}},
}

func readTranscriptRegistry(t *testing.T, deps *toolDeps) *tool.Registry {
	t.Helper()
	reg := tool.NewRegistry()
	if err := reg.Register(readTranscriptTool(deps)); err != nil {
		t.Fatalf("register read_transcript: %v", err)
	}
	return reg
}

func TestReadTranscriptSessionRefTreatsStrictSchemaPlaceholdersAsAbsent(t *testing.T) {
	dir := newBucket(t)
	const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
	writeFindSession(t, dir, findMetaSpec{id: sessionID}, "semantic turn")
	deps := &toolDeps{stateDir: dir, sessionID: sessionID}
	reg := readTranscriptRegistry(t, deps)

	omitted, err := execReadTranscript(context.Background(), deps, map[string]any{})
	if err != nil {
		t.Fatalf("omitted options: %v", err)
	}
	registryOmitted := executeReadTranscriptFromRegistry(t, reg, "omitted", map[string]any{})
	if registryOmitted.IsError {
		t.Fatalf("registry omitted options: %s", registryOmitted.Output)
	}
	for _, tc := range strictSchemaPlaceholders {
		t.Run("exec "+tc.name, func(t *testing.T) {
			got, err := execReadTranscript(context.Background(), deps, tc.args)
			if err != nil {
				t.Fatalf("execReadTranscript(%v): %v", tc.args, err)
			}
			if !sameReadResult(omitted, got) {
				t.Fatalf("placeholders changed the read:\nomitted=%#v\ngot=%#v", omitted, got)
			}
		})
		t.Run("registry "+tc.name, func(t *testing.T) {
			got := executeReadTranscriptFromRegistry(t, reg, "placeholder", tc.args)
			if got.IsError {
				t.Fatalf("registry rejected %v: %s", tc.args, got.Output)
			}
			if !sameReadResult(registryOmitted.FullOutput, got.FullOutput) {
				t.Fatalf("placeholders changed the read:\nomitted=%s\ngot=%s", registryOmitted.FullOutput, got.FullOutput)
			}
		})
	}
}

func TestReadTranscriptSessionRefStillRefusesContradictoryOptions(t *testing.T) {
	dir := newBucket(t)
	const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
	writeFindSession(t, dir, findMetaSpec{id: sessionID}, "semantic turn")
	deps := &toolDeps{stateDir: dir, sessionID: sessionID}
	reg := readTranscriptRegistry(t, deps)

	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"real offset without expand_turn", map[string]any{"offset_bytes": float64(5)}, "offset_bytes requires expand_turn"},
		{"real offset beside placeholders", map[string]any{"offset_bytes": float64(5), "output_match": "null", "context_lines": float64(0)}, "offset_bytes requires expand_turn"},
		{"real context without output_match", map[string]any{"context_lines": float64(3)}, "context_lines requires output_match"},
		{"real output_match on a session ref", map[string]any{"output_match": "ready"}, "output_match applies only"},
	} {
		t.Run("exec "+tc.name, func(t *testing.T) {
			_, err := execReadTranscript(context.Background(), deps, tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
		t.Run("registry "+tc.name, func(t *testing.T) {
			got := executeReadTranscriptFromRegistry(t, reg, "contradiction", tc.args)
			if !got.IsError || !strings.Contains(got.Output, tc.want) {
				t.Fatalf("result = %#v, want error containing %q", got, tc.want)
			}
		})
	}
}

func TestReadTranscriptJobRefTreatsNullStringsAsAbsent(t *testing.T) {
	stateDir := t.TempDir()
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	output := "ready\nvalue is null\n"
	seedLocalJobRecord(t, stateDir, owner, jobID, "/decoy", output, maxJobOutputRetentionBytes, true, int64(len(output)), nil)
	deps := &toolDeps{stateDir: stateDir, sessionID: owner}
	ref := "job:" + jobID
	omitted, err := execReadTranscript(context.Background(), deps, map[string]any{"transcript_ref": ref})
	if err != nil {
		t.Fatalf("omitted options: %v", err)
	}
	got, err := execReadTranscript(context.Background(), deps, map[string]any{
		"transcript_ref": ref, "format": "null", "range": "null", "expand_turn": "null",
		"offset_bytes": "null", "context_lines": "null",
	})
	if err != nil {
		t.Fatalf("null strings: %v", err)
	}
	if !sameReadResult(omitted, got) {
		t.Fatalf("null strings changed the job read:\nomitted=%#v\ngot=%#v", omitted, got)
	}
}

// On a job or artifact ref output_match is a regular expression, and "null" is
// a legitimate pattern that must search, with or without context.
func TestReadTranscriptRetainedRefSearchesLiteralNull(t *testing.T) {
	stateDir := t.TempDir()
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	output := "ready\nvalue is null\n"
	seedLocalJobRecord(t, stateDir, owner, jobID, "/decoy", output, maxJobOutputRetentionBytes, true, int64(len(output)), nil)
	deps := &toolDeps{stateDir: stateDir, sessionID: owner}
	for _, args := range []map[string]any{
		{"transcript_ref": "job:" + jobID, "output_match": "null"},
		{"transcript_ref": "job:" + jobID, "output_match": "NULL", "context_lines": float64(1)},
	} {
		got, err := execReadTranscript(context.Background(), deps, args)
		if err != nil {
			t.Fatalf("search %v: %v", args, err)
		}
		encoded, _ := json.Marshal(got)
		if !strings.Contains(string(encoded), `"search_complete"`) {
			t.Fatalf("search %v did not run a search: %s", args, encoded)
		}
	}
}

// A read_transcript call whose context is already cancelled stops before it
// returns transcript content, for every transcript format.
func TestReadTranscriptToolHonorsCancelledContext(t *testing.T) {
	dir := newBucket(t)
	const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
	writeFindSession(t, dir, findMetaSpec{id: sessionID}, "semantic turn")
	registered := readTranscriptTool(&toolDeps{stateDir: dir, sessionID: sessionID})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	for _, format := range []string{"markdown", "outline", "jsonl"} {
		t.Run(format, func(t *testing.T) {
			_, err := registered.Exec(ctx, nil, map[string]any{"format": format})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Exec error = %v, want context.Canceled", err)
			}
		})
	}
}

// A jsonl read cancelled after its first scan stops in the second pass that
// projects the requested range.
func TestReadTranscriptRawSecondPassHonorsCancellation(t *testing.T) {
	dir := newBucket(t)
	const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
	writeFindSession(t, dir, findMetaSpec{id: sessionID}, "semantic turn")
	path := transcriptPath(dir, sessionID)

	ctx, cancel := context.WithCancel(t.Context())
	original := readRawLinesForRange
	readRawLinesForRange = func(ctx context.Context, path, root string, start, end int) (string, int, int, bool, error) {
		cancel()
		return original(ctx, path, root, start, end)
	}
	t.Cleanup(func() { readRawLinesForRange = original })

	if _, err := readRaw(ctx, path, dir, "local:"+sessionID, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("readRaw error = %v, want context.Canceled", err)
	}
}
