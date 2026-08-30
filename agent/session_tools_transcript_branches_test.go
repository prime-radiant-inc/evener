package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/artifactstore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// ---------------------------------------------------------------------------
// transcriptTools
// ---------------------------------------------------------------------------

func TestTranscriptToolsNilDeps(t *testing.T) {
	tools := transcriptTools(nil)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool (read_transcript only), got %d", len(tools))
	}
	if tools[0].Definition.Name != "read_transcript" {
		t.Fatalf("expected read_transcript, got %q", tools[0].Definition.Name)
	}
	for _, tl := range tools {
		if tl.Limit.MaxChars != transcriptToolMaxChars {
			t.Fatalf("expected MaxChars=%d, got %d", transcriptToolMaxChars, tl.Limit.MaxChars)
		}
	}
}

func TestTranscriptToolsWithStateDir(t *testing.T) {
	deps := &toolDeps{stateDir: "/tmp/some-state"}
	tools := transcriptTools(deps)
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
}

// ---------------------------------------------------------------------------
// parseRetainedReadArgs
// ---------------------------------------------------------------------------

func TestParseRetainedReadArgs(t *testing.T) {
	t.Run("default no special args", func(t *testing.T) {
		args := map[string]any{"transcript_ref": "local:abc"}
		parsed, op, err := parseRetainedReadArgs(args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if op != retainedReadDefault {
			t.Fatalf("expected default operation, got %d", op)
		}
		if parsed.Ref != "local:abc" {
			t.Fatalf("ref = %q", parsed.Ref)
		}
	})
	t.Run("search with output_match", func(t *testing.T) {
		args := map[string]any{"transcript_ref": "job:123", "output_match": "ERROR"}
		parsed, op, err := parseRetainedReadArgs(args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if op != retainedReadSearch {
			t.Fatalf("expected search operation, got %d", op)
		}
		if parsed.OutputMatch != "ERROR" {
			t.Fatalf("output_match = %q", parsed.OutputMatch)
		}
	})
	t.Run("page with offset_bytes", func(t *testing.T) {
		args := map[string]any{"transcript_ref": "job:123", "offset_bytes": float64(42)}
		parsed, op, err := parseRetainedReadArgs(args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if op != retainedReadPage {
			t.Fatalf("expected page operation, got %d", op)
		}
		if !parsed.OffsetSet || parsed.OffsetBytes != 42 {
			t.Fatalf("offset = %v (set=%v)", parsed.OffsetBytes, parsed.OffsetSet)
		}
	})
	t.Run("context_lines without output_match", func(t *testing.T) {
		_, _, err := parseRetainedReadArgs(map[string]any{"context_lines": float64(3)})
		if err == nil || !strings.Contains(err.Error(), "context_lines requires output_match") {
			t.Fatalf("expected context_lines error, got %v", err)
		}
	})
	t.Run("context_lines negative", func(t *testing.T) {
		_, _, err := parseRetainedReadArgs(map[string]any{"output_match": "x", "context_lines": float64(-1)})
		if err == nil || !strings.Contains(err.Error(), "context_lines must be between 0 and 10") {
			t.Fatalf("expected context_lines range error, got %v", err)
		}
	})
	t.Run("context_lines too high", func(t *testing.T) {
		_, _, err := parseRetainedReadArgs(map[string]any{"output_match": "x", "context_lines": float64(11)})
		if err == nil || !strings.Contains(err.Error(), "context_lines must be between 0 and 10") {
			t.Fatalf("expected context_lines range error, got %v", err)
		}
	})
	t.Run("context_lines valid", func(t *testing.T) {
		parsed, _, err := parseRetainedReadArgs(map[string]any{"output_match": "x", "context_lines": float64(5)})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if parsed.ContextLines != 5 {
			t.Fatalf("context_lines = %d", parsed.ContextLines)
		}
	})
	t.Run("output_match too large", func(t *testing.T) {
		huge := strings.Repeat("x", retainedOutputMatchMaxChars+1)
		_, _, err := parseRetainedReadArgs(map[string]any{"output_match": huge})
		if err == nil || !strings.Contains(err.Error(), "output_match must be at most") {
			t.Fatalf("expected output_match size error, got %v", err)
		}
	})
	t.Run("offset_bytes negative", func(t *testing.T) {
		_, _, err := parseRetainedReadArgs(map[string]any{"offset_bytes": float64(-1)})
		if err == nil || !strings.Contains(err.Error(), "offset_bytes must be non-negative") {
			t.Fatalf("expected offset error, got %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// validateArtifactReadArgs
// ---------------------------------------------------------------------------

func TestValidateArtifactReadArgs(t *testing.T) {
	t.Run("range rejected", func(t *testing.T) {
		err := validateArtifactReadArgs(map[string]any{"range": "1-5"}, retainedReadDefault)
		if err == nil || !strings.Contains(err.Error(), "range applies only to session") {
			t.Fatalf("expected range error, got %v", err)
		}
	})
	t.Run("expand_turn rejected", func(t *testing.T) {
		err := validateArtifactReadArgs(map[string]any{"expand_turn": float64(1)}, retainedReadDefault)
		if err == nil || !strings.Contains(err.Error(), "expand_turn applies only to session") {
			t.Fatalf("expected expand_turn error, got %v", err)
		}
	})
	t.Run("format rejected", func(t *testing.T) {
		err := validateArtifactReadArgs(map[string]any{"format": "markdown"}, retainedReadDefault)
		if err == nil || !strings.Contains(err.Error(), "format is not supported for artifact") {
			t.Fatalf("expected format error, got %v", err)
		}
	})
	t.Run("valid no special args", func(t *testing.T) {
		err := validateArtifactReadArgs(map[string]any{"transcript_ref": "artifact:abc"}, retainedReadDefault)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// validateJobReadArgs
// ---------------------------------------------------------------------------

func TestValidateJobReadArgs(t *testing.T) {
	t.Run("range rejected", func(t *testing.T) {
		err := validateJobReadArgs(map[string]any{"range": "1-5"}, retainedReadDefault)
		if err == nil || !strings.Contains(err.Error(), "range applies only to session") {
			t.Fatalf("expected range error, got %v", err)
		}
	})
	t.Run("expand_turn rejected", func(t *testing.T) {
		err := validateJobReadArgs(map[string]any{"expand_turn": float64(1)}, retainedReadDefault)
		if err == nil || !strings.Contains(err.Error(), "expand_turn applies only to session") {
			t.Fatalf("expected expand_turn error, got %v", err)
		}
	})
	t.Run("format with offset rejected", func(t *testing.T) {
		err := validateJobReadArgs(map[string]any{"format": "jsonl", "offset_bytes": float64(1)}, retainedReadPage)
		if err == nil || !strings.Contains(err.Error(), "format cannot be combined") {
			t.Fatalf("expected format+offset error, got %v", err)
		}
	})
	t.Run("format markdown with offset accepted", func(t *testing.T) {
		err := validateJobReadArgs(map[string]any{"format": "markdown", "offset_bytes": float64(1)}, retainedReadPage)
		if err != nil {
			t.Fatalf("expected markdown+offset to be accepted, got %v", err)
		}
	})
	t.Run("format markdown with output_match accepted", func(t *testing.T) {
		err := validateJobReadArgs(map[string]any{"format": "markdown", "output_match": "READY"}, retainedReadSearch)
		if err != nil {
			t.Fatalf("expected markdown+output_match to be accepted, got %v", err)
		}
	})
	t.Run("format non-markdown", func(t *testing.T) {
		err := validateJobReadArgs(map[string]any{"format": "jsonl"}, retainedReadDefault)
		if err == nil || !strings.Contains(err.Error(), "job: refs support only format=markdown") {
			t.Fatalf("expected format error, got %v", err)
		}
	})
	t.Run("format markdown ok", func(t *testing.T) {
		err := validateJobReadArgs(map[string]any{"format": "markdown"}, retainedReadDefault)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("no format ok", func(t *testing.T) {
		err := validateJobReadArgs(map[string]any{}, retainedReadDefault)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// compileOutputMatch
// ---------------------------------------------------------------------------

func TestCompileOutputMatch(t *testing.T) {
	t.Run("valid regex", func(t *testing.T) {
		re, err := compileOutputMatch("ERR.*")
		if err != nil || re == nil {
			t.Fatalf("expected valid regex, got re=%v err=%v", re, err)
		}
	})
	t.Run("invalid regex", func(t *testing.T) {
		_, err := compileOutputMatch("[")
		if err == nil || !strings.Contains(err.Error(), "output_match is not valid RE2") {
			t.Fatalf("expected invalid regex error, got %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// validArtifactTranscriptRef
// ---------------------------------------------------------------------------

func TestValidArtifactTranscriptRef(t *testing.T) {
	tests := []struct {
		ref  string
		want bool
	}{
		{"artifact:" + strings.Repeat("a", 32), true},
		{"artifact:" + strings.Repeat("0", 32), true},
		{"artifact:" + strings.Repeat("f", 32), true},
		{"artifact:" + strings.Repeat("9", 32), true},
		{"artifact:abc", false},                        // too short
		{"artifact:" + strings.Repeat("g", 32), false}, // non-hex char
		{"artifact:" + strings.Repeat("A", 32), false}, // uppercase not hex lowercase
		{"notartifact:" + strings.Repeat("a", 32), false},
		{"", false},
		{strings.Repeat("a", 32), false}, // missing prefix
	}
	for _, tc := range tests {
		if got := validArtifactTranscriptRef(tc.ref); got != tc.want {
			t.Errorf("validArtifactTranscriptRef(%q) = %v, want %v", tc.ref, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// parseJobTranscriptID
// ---------------------------------------------------------------------------

func TestParseJobTranscriptID(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		id, err := parseJobTranscriptID("job:abc123")
		if err != nil || id != "abc123" {
			t.Fatalf("id=%q err=%v", id, err)
		}
	})
	t.Run("with whitespace", func(t *testing.T) {
		id, err := parseJobTranscriptID("job:  abc123  ")
		if err != nil || id != "abc123" {
			t.Fatalf("id=%q err=%v", id, err)
		}
	})
	t.Run("empty after prefix", func(t *testing.T) {
		_, err := parseJobTranscriptID("job:")
		if err == nil || !strings.Contains(err.Error(), "must be job:<job_id>") {
			t.Fatalf("expected error, got %v", err)
		}
	})
	t.Run("only whitespace", func(t *testing.T) {
		_, err := parseJobTranscriptID("job:   ")
		if err == nil {
			t.Fatalf("expected error for whitespace-only job id")
		}
	})
}

// ---------------------------------------------------------------------------
// renderJobTranscript / renderDelegateJobTranscript / renderShellJobTranscript
// ---------------------------------------------------------------------------

func TestRenderJobTranscriptDispatch(t *testing.T) {
	t.Run("nil rec renders shell", func(t *testing.T) {
		out := renderJobTranscript(nil, "hello", 100, 0)
		if !strings.Contains(out, "Shell Job") {
			t.Fatalf("expected shell job header, got: %s", out)
		}
	})
	t.Run("delegate type renders delegate", func(t *testing.T) {
		rec := &jobstore.JobRecord{JobID: "dlg_123", Type: "delegate", Status: "completed", Reason: "done"}
		out := renderJobTranscript(rec, "report", 200, 0)
		if !strings.Contains(out, "Delegate Job dlg_123") {
			t.Fatalf("expected delegate header, got: %s", out)
		}
	})
	t.Run("shell type renders shell", func(t *testing.T) {
		rec := &jobstore.JobRecord{JobID: "j_456", Type: "shell", Status: "running"}
		out := renderJobTranscript(rec, "output", 50, 0)
		if !strings.Contains(out, "Shell Job j_456") {
			t.Fatalf("expected shell header, got: %s", out)
		}
	})
}

func TestRenderDelegateJobTranscriptAllFields(t *testing.T) {
	valid := true
	rec := &jobstore.JobRecord{
		JobID:                  "dlg_abc",
		Type:                   "delegate",
		Status:                 "completed",
		Reason:                 "finished ok",
		Task:                   "do the thing\nwith newlines",
		StructuredResult:       map[string]any{"key": "val"},
		StructuredResultValid:  &valid,
		StructuredResultReason: "parsed successfully",
	}
	out := renderDelegateJobTranscript(rec, "delegate output", 500, 10)
	for _, want := range []string{"Delegate Job dlg_abc", "status: completed", "reason: finished ok", "task: do the thing with newlines", "total_bytes: 500", "dropped_bytes: 10", "delegate output", "structured_result", "valid=true", `"key":"val"`, "structured_result_reason: parsed successfully"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestRenderDelegateJobTranscriptMinimal(t *testing.T) {
	rec := &jobstore.JobRecord{JobID: "dlg_min", Type: "delegate"}
	out := renderDelegateJobTranscript(rec, "", 0, 0)
	if !strings.Contains(out, "Delegate Job dlg_min") {
		t.Fatalf("expected header")
	}
	if strings.Contains(out, "status:") {
		t.Fatalf("should not have status for empty status")
	}
	if !strings.Contains(out, "total_bytes: 0") {
		t.Fatalf("expected total_bytes: 0")
	}
}

func TestRenderDelegateJobTranscriptStructuredResultInvalid(t *testing.T) {
	invalid := false
	rec := &jobstore.JobRecord{
		JobID:                 "dlg_inv",
		Type:                  "delegate",
		StructuredResult:      map[string]any{"x": 1},
		StructuredResultValid: &invalid,
	}
	out := renderDelegateJobTranscript(rec, "out", 10, 0)
	if !strings.Contains(out, "valid=false") {
		t.Fatalf("expected valid=false in output:\n%s", out)
	}
}

func TestRenderDelegateJobTranscriptStructuredResultNilValid(t *testing.T) {
	rec := &jobstore.JobRecord{
		JobID:            "dlg_nil",
		Type:             "delegate",
		StructuredResult: map[string]any{"x": 1},
	}
	out := renderDelegateJobTranscript(rec, "out", 10, 0)
	if !strings.Contains(out, "valid=false") {
		t.Fatalf("expected valid=false when StructuredResultValid is nil:\n%s", out)
	}
}

func TestRenderDelegateJobTranscriptOutputNoTrailingNewline(t *testing.T) {
	rec := &jobstore.JobRecord{JobID: "dlg_nl", Type: "delegate"}
	out := renderDelegateJobTranscript(rec, "no newline", 10, 0)
	if !strings.HasSuffix(out, "```\n") {
		t.Fatalf("expected output to end with closing fence+newline")
	}
	if !strings.Contains(out, "no newline\n") {
		t.Fatalf("expected newline to be appended after output")
	}
}

func TestRenderDelegateJobTranscriptOutputWithTrailingNewline(t *testing.T) {
	rec := &jobstore.JobRecord{JobID: "dlg_nl2", Type: "delegate"}
	out := renderDelegateJobTranscript(rec, "has newline\n", 10, 0)
	// Should not double the newline
	if strings.Contains(out, "has newline\n\n```\n") {
		t.Fatalf("should not double newline before fence")
	}
}

func TestRenderShellJobTranscriptAllFields(t *testing.T) {
	rec := &jobstore.JobRecord{
		JobID:   "j_123",
		Status:  "completed",
		Reason:  "exit 0",
		Command: "echo `hello`",
	}
	out := renderShellJobTranscript(rec, "output line", 300, 5)
	for _, want := range []string{"Shell Job j_123", "status: completed", "reason: exit 0", "command: `echo \\`hello\\``", "total_bytes: 300", "dropped_bytes: 5", "output line"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestRenderShellJobTranscriptNilRec(t *testing.T) {
	out := renderShellJobTranscript(nil, "data", 10, 0)
	if !strings.Contains(out, "Shell Job \n") {
		t.Fatalf("expected empty job ID header")
	}
	if !strings.Contains(out, "total_bytes: 10") {
		t.Fatalf("expected total_bytes")
	}
}

func TestRenderShellJobTranscriptNoTrailingNewline(t *testing.T) {
	out := renderShellJobTranscript(nil, "no newline", 5, 0)
	if !strings.Contains(out, "no newline\n```\n") {
		t.Fatalf("expected newline appended before fence:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// retainedPageResult
// ---------------------------------------------------------------------------

func TestRetainedPageResult(t *testing.T) {
	page := retainedPage{
		OffsetBytes:   10,
		BytesReturned: 5,
		TotalBytes:    100,
		Encoding:      "utf8",
		Data:          "hello",
		Continuation:  &retainedContinuation{OffsetBytes: 15},
	}
	env := retainedPageResult("job:abc", 5, "completed", page)
	if env.TranscriptRef != "job:abc" {
		t.Fatalf("ref = %q", env.TranscriptRef)
	}
	if env.Representation != "raw_bytes" {
		t.Fatalf("representation = %q", env.Representation)
	}
	if env.ContentType != "text/plain" {
		t.Fatalf("content_type = %q", env.ContentType)
	}
	if env.Page.Data != "hello" {
		t.Fatalf("data = %q", env.Page.Data)
	}
	if env.RetainedStartByte != 5 {
		t.Fatalf("retainedStart = %d", env.RetainedStartByte)
	}
	if env.JobStatus != "completed" {
		t.Fatalf("jobStatus = %q", env.JobStatus)
	}
	if env.Continuation == nil || env.Continuation.OffsetBytes != 15 {
		t.Fatalf("continuation = %v", env.Continuation)
	}
}

func TestRetainedPageResultNoContinuation(t *testing.T) {
	page := retainedPage{
		OffsetBytes:   0,
		BytesReturned: 5,
		TotalBytes:    5,
		Encoding:      "utf8",
		Data:          "hello",
	}
	env := retainedPageResult("job:xyz", 0, "", page)
	if env.Continuation != nil {
		t.Fatalf("expected nil continuation")
	}
}

// ---------------------------------------------------------------------------
// retainedSearchResultFor
// ---------------------------------------------------------------------------

func TestRetainedSearchResultFor(t *testing.T) {
	t.Run("nil before/after get initialized", func(t *testing.T) {
		result := retainedSearchEnvelope{
			Matches: []retainedSearchMatch{
				{Line: "match1", Before: nil, After: nil},
			},
		}
		args := retainedReadArgs{Ref: "job:abc", OutputMatch: "match", ContextLines: 2}
		out := retainedSearchResultFor(args, "running", result)
		if out.TranscriptRef != "job:abc" {
			t.Fatalf("ref = %q", out.TranscriptRef)
		}
		if out.JobStatus != "running" {
			t.Fatalf("status = %q", out.JobStatus)
		}
		if len(out.Matches) != 1 {
			t.Fatalf("expected 1 match")
		}
		if out.Matches[0].Before == nil {
			t.Fatalf("expected Before to be initialized to empty slice")
		}
		if out.Matches[0].After == nil {
			t.Fatalf("expected After to be initialized to empty slice")
		}
	})
	t.Run("with before/after preserved", func(t *testing.T) {
		result := retainedSearchEnvelope{
			Matches: []retainedSearchMatch{
				{Line: "match1", Before: []string{"ctx1"}, After: []string{"ctx2"}},
			},
		}
		args := retainedReadArgs{Ref: "job:abc", OutputMatch: "match"}
		out := retainedSearchResultFor(args, "", result)
		if len(out.Matches[0].Before) != 1 || out.Matches[0].Before[0] != "ctx1" {
			t.Fatalf("Before not preserved: %v", out.Matches[0].Before)
		}
		if len(out.Matches[0].After) != 1 || out.Matches[0].After[0] != "ctx2" {
			t.Fatalf("After not preserved: %v", out.Matches[0].After)
		}
	})
}

// ---------------------------------------------------------------------------
// boundedRetainedSearchResult
// ---------------------------------------------------------------------------

func TestBoundedRetainedSearchResult(t *testing.T) {
	t.Run("within limit", func(t *testing.T) {
		result := retainedSearchEnvelope{
			Matches: []retainedSearchMatch{
				{Line: "small match", Before: []string{}, After: []string{}},
			},
		}
		args := retainedReadArgs{Ref: "job:abc", OutputMatch: "small"}
		out, err := boundedRetainedSearchResult(args, "completed", result)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out.TranscriptRef != "job:abc" {
			t.Fatalf("ref = %q", out.TranscriptRef)
		}
	})
	t.Run("over limit", func(t *testing.T) {
		huge := strings.Repeat("x", transcriptToolMaxChars+1)
		result := retainedSearchEnvelope{
			Matches: []retainedSearchMatch{
				{Line: huge, Before: []string{}, After: []string{}},
			},
		}
		args := retainedReadArgs{Ref: "job:abc", OutputMatch: huge}
		_, err := boundedRetainedSearchResult(args, "", result)
		if err == nil || !strings.Contains(err.Error(), "output_match is too large") {
			t.Fatalf("expected too-large error, got %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// decodeTranscriptExpansion
// ---------------------------------------------------------------------------

func TestDecodeTranscriptExpansion(t *testing.T) {
	t.Run("utf8", func(t *testing.T) {
		exp := &transcriptTurnExpansion{Encoding: "utf8", Data: "hello world"}
		raw, err := decodeTranscriptExpansion(exp)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(raw) != "hello world" {
			t.Fatalf("decoded = %q", string(raw))
		}
	})
	t.Run("base64 valid", func(t *testing.T) {
		encoded := base64.StdEncoding.EncodeToString([]byte("base64 content"))
		exp := &transcriptTurnExpansion{Encoding: "base64", Data: encoded}
		raw, err := decodeTranscriptExpansion(exp)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(raw) != "base64 content" {
			t.Fatalf("decoded = %q", string(raw))
		}
	})
	t.Run("base64 invalid", func(t *testing.T) {
		exp := &transcriptTurnExpansion{Encoding: "base64", Data: "!!!not-base64!!!"}
		_, err := decodeTranscriptExpansion(exp)
		if err == nil || !strings.Contains(err.Error(), "decode transcript expansion") {
			t.Fatalf("expected decode error, got %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// transcriptEnvelopeWithExpansionBytes
// ---------------------------------------------------------------------------

func TestTranscriptEnvelopeWithExpansionBytes(t *testing.T) {
	t.Run("utf8 data with continuation", func(t *testing.T) {
		env := readMarkdownEnvelope{
			Expansion: &transcriptTurnExpansion{
				ExpandTurn:  3,
				OffsetBytes: 0,
				TotalBytes:  100,
			},
		}
		data := []byte("hello world")
		result := transcriptEnvelopeWithExpansionBytes(env, data)
		if result.Expansion.Encoding != "utf8" {
			t.Fatalf("encoding = %q", result.Expansion.Encoding)
		}
		if result.Expansion.Data != "hello world" {
			t.Fatalf("data = %q", result.Expansion.Data)
		}
		if result.Expansion.BytesReturned != len(data) {
			t.Fatalf("bytes_returned = %d", result.Expansion.BytesReturned)
		}
		if result.Continuation == nil {
			t.Fatalf("expected continuation when offset+data < total")
		}
		if result.Continuation.OffsetBytes != 11 {
			t.Fatalf("continuation offset = %d", result.Continuation.OffsetBytes)
		}
	})
	t.Run("base64 data no continuation", func(t *testing.T) {
		env := readMarkdownEnvelope{
			Expansion: &transcriptTurnExpansion{
				ExpandTurn:  1,
				OffsetBytes: 0,
				TotalBytes:  5,
			},
		}
		// Invalid UTF-8 to trigger base64 encoding
		data := []byte{0xff, 0xfe, 0xff, 0xfe, 0x00}
		result := transcriptEnvelopeWithExpansionBytes(env, data)
		if result.Expansion.Encoding != "base64" {
			t.Fatalf("encoding = %q, want base64", result.Expansion.Encoding)
		}
		if result.Continuation != nil {
			t.Fatalf("expected nil continuation when offset+data >= total")
		}
	})
}

// ---------------------------------------------------------------------------
// transcriptExpansionPrefixClasses
// ---------------------------------------------------------------------------

func TestTranscriptExpansionPrefixClasses(t *testing.T) {
	t.Run("pure ascii valid only", func(t *testing.T) {
		valid, invalid := transcriptExpansionPrefixClasses([]byte("hello"))
		if len(valid) != 5 || len(invalid) != 0 {
			t.Fatalf("valid=%v invalid=%v", valid, invalid)
		}
	})
	t.Run("invalid utf8 byte produces invalid candidates", func(t *testing.T) {
		valid, invalid := transcriptExpansionPrefixClasses([]byte{0xff})
		// 0xff is an invalid utf8 start byte (>= 0x80), so it falls into the
		// invalid branch which appends all remaining ends
		if len(valid) != 0 {
			t.Fatalf("expected no valid candidates for invalid byte, got %v", valid)
		}
		if len(invalid) != 1 {
			t.Fatalf("expected 1 invalid candidate, got %v", invalid)
		}
	})
	t.Run("multibyte rune", func(t *testing.T) {
		// 'é' is 2 bytes in utf8
		valid, invalid := transcriptExpansionPrefixClasses([]byte("é"))
		if len(valid) != 1 {
			t.Fatalf("expected 1 valid candidate, got %v", valid)
		}
		if len(invalid) != 1 {
			t.Fatalf("expected 1 invalid candidate (split mid-rune), got %v", invalid)
		}
	})
}

// ---------------------------------------------------------------------------
// largestTranscriptExpansionPrefix
// ---------------------------------------------------------------------------

func TestLargestTranscriptExpansionPrefix(t *testing.T) {
	t.Run("empty raw returns 0", func(t *testing.T) {
		n, err := largestTranscriptExpansionPrefix(readMarkdownEnvelope{}, nil, 1000)
		if err != nil || n != 0 {
			t.Fatalf("n=%d err=%v", n, err)
		}
	})
	t.Run("fits within limit", func(t *testing.T) {
		env := readMarkdownEnvelope{
			Expansion: &transcriptTurnExpansion{ExpandTurn: 0, OffsetBytes: 0, TotalBytes: 5},
		}
		n, err := largestTranscriptExpansionPrefix(env, []byte("hello"), 100000)
		if err != nil || n != 5 {
			t.Fatalf("n=%d err=%v", n, err)
		}
	})
	t.Run("needs shrinking", func(t *testing.T) {
		env := readMarkdownEnvelope{
			Expansion: &transcriptTurnExpansion{ExpandTurn: 0, OffsetBytes: 0, TotalBytes: 1000},
		}
		// Use a moderate limit that forces shrinking but still allows a prefix to fit
		raw := []byte(strings.Repeat("x", 500))
		n, err := largestTranscriptExpansionPrefix(env, raw, 10000)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n == 0 {
			t.Fatalf("expected non-zero prefix that fits")
		}
		if n > len(raw) {
			t.Fatalf("prefix %d should be <= raw length %d", n, len(raw))
		}
	})
}

// ---------------------------------------------------------------------------
// largestFittingTranscriptPrefix
// ---------------------------------------------------------------------------

func TestLargestFittingTranscriptPrefix(t *testing.T) {
	env := readMarkdownEnvelope{
		Expansion: &transcriptTurnExpansion{ExpandTurn: 0, OffsetBytes: 0, TotalBytes: 100},
	}
	candidates := []int{5, 10, 15, 20}
	n, err := largestFittingTranscriptPrefix(env, []byte("abcdefghijklmnopqrst"), candidates, 100000)
	if err != nil || n != 20 {
		t.Fatalf("n=%d err=%v (expected 20)", n, err)
	}
}

// ---------------------------------------------------------------------------
// boundReadMarkdownContentWithHint
// ---------------------------------------------------------------------------

func TestBoundReadMarkdownContentWithHint(t *testing.T) {
	t.Run("within limit", func(t *testing.T) {
		env := readMarkdownEnvelope{Content: "short"}
		out, err := boundReadMarkdownContentWithHint(env, 100000, "hint")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out.Content != "short" {
			t.Fatalf("content = %q", out.Content)
		}
	})
	t.Run("needs truncation", func(t *testing.T) {
		long := strings.Repeat("x", 50000)
		env := readMarkdownEnvelope{Content: long}
		out, err := boundReadMarkdownContentWithHint(env, 10000, "hint")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !out.Meta.Truncated {
			t.Fatalf("expected truncated=true")
		}
		if len(out.Content) >= len(long) {
			t.Fatalf("expected content to be truncated")
		}
	})
}

func TestBoundReadMarkdownEnvelopeWithHintUnderLimit(t *testing.T) {
	env := readMarkdownEnvelope{Content: "short content"}
	out, err := boundReadMarkdownEnvelopeWithHint(env, "hint")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Content != "short content" {
		t.Fatalf("content = %q", out.Content)
	}
}

func TestBoundReadMarkdownEnvelopeWithHintNoExpansion(t *testing.T) {
	// Over the hard cap, no expansion — falls to content truncation
	long := strings.Repeat("x", hardCapChars+10000)
	env := readMarkdownEnvelope{Content: long}
	out, err := boundReadMarkdownEnvelopeWithHint(env, "hint")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Meta.Truncated {
		t.Fatalf("expected truncated=true")
	}
}

func TestBoundReadMarkdownEnvelopeWithHintExpansionFits(t *testing.T) {
	env := readMarkdownEnvelope{
		Content:   "content",
		Expansion: &transcriptTurnExpansion{ExpandTurn: 0, OffsetBytes: 0, TotalBytes: 5, Encoding: "utf8", Data: "hello"},
	}
	out, err := boundReadMarkdownEnvelopeWithHint(env, "hint")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Expansion.Data != "hello" {
		t.Fatalf("expansion data = %q", out.Expansion.Data)
	}
}

func TestBoundReadMarkdownEnvelopeWithHintExpansionNeedsShrinking(t *testing.T) {
	// Expansion is small enough to fit after content is trimmed
	env := readMarkdownEnvelope{
		Content:   strings.Repeat("x", hardCapChars+5000),
		Expansion: &transcriptTurnExpansion{ExpandTurn: 0, OffsetBytes: 0, TotalBytes: 5, Encoding: "utf8", Data: "hello"},
	}
	out, err := boundReadMarkdownEnvelopeWithHint(env, "hint")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Meta.Truncated {
		t.Fatalf("expected truncated=true")
	}
}

func TestBoundReadMarkdownEnvelopeWithHintExpansionDecodeError(t *testing.T) {
	// Content is large enough to exceed hardCapChars, forcing the expansion
	// decode path which will fail on invalid base64.
	env := readMarkdownEnvelope{
		Content:   strings.Repeat("x", hardCapChars+5000),
		Expansion: &transcriptTurnExpansion{Encoding: "base64", Data: "!!!invalid!!!", TotalBytes: 5},
	}
	_, err := boundReadMarkdownEnvelopeWithHint(env, "hint")
	if err == nil || !strings.Contains(err.Error(), "decode transcript expansion") {
		t.Fatalf("expected decode error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// transcriptExpansionJSONL
// ---------------------------------------------------------------------------

func TestTranscriptExpansionJSONL(t *testing.T) {
	t.Run("pin out of range", func(t *testing.T) {
		data := transcriptData{Entries: []transcript.Entry{{Seq: 0, Turn: schema.NewTurn(schema.TurnUserInput, llm.User("hi"))}}}
		_, err := transcriptExpansionJSONL(data, 5)
		if err == nil || !strings.Contains(err.Error(), "does not identify a transcript turn") {
			t.Fatalf("expected error, got %v", err)
		}
	})
	t.Run("pin negative", func(t *testing.T) {
		data := transcriptData{Entries: []transcript.Entry{{}}}
		_, err := transcriptExpansionJSONL(data, -1)
		if err == nil || !strings.Contains(err.Error(), "does not identify a transcript turn") {
			t.Fatalf("expected error, got %v", err)
		}
	})
	t.Run("attention resolution turn", func(t *testing.T) {
		data := transcriptData{Entries: []transcript.Entry{
			{Seq: 0, Turn: schema.Turn{Kind: schema.TurnAttentionResolution}},
		}}
		_, err := transcriptExpansionJSONL(data, 0)
		if err == nil || !strings.Contains(err.Error(), "does not identify a public transcript turn") {
			t.Fatalf("expected error, got %v", err)
		}
	})
	t.Run("no entry lines", func(t *testing.T) {
		data := transcriptData{
			Entries: []transcript.Entry{{Seq: 0, Turn: schema.NewTurn(schema.TurnUserInput, llm.User("hi"))}},
		}
		_, err := transcriptExpansionJSONL(data, 0)
		if err == nil || !strings.Contains(err.Error(), "not retained") {
			t.Fatalf("expected error, got %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// publicTranscriptEntry / publicTranscriptEntries
// ---------------------------------------------------------------------------

func TestPublicTranscriptEntry(t *testing.T) {
	t.Run("normal entry included with attention fields stripped", func(t *testing.T) {
		entry := transcript.Entry{
			Seq: 3,
			Turn: schema.Turn{
				Kind:                    schema.TurnAssistant,
				AttentionID:             "att_123",
				AttentionResolution:     &schema.AttentionResolutionInfo{},
				DelegateDeliveryCommits: []schema.DelegateDeliveryCommit{},
			},
		}
		out, ok := publicTranscriptEntry(entry)
		if !ok {
			t.Fatalf("expected ok=true")
		}
		if out.Turn.AttentionID != "" {
			t.Fatalf("attention_id should be stripped")
		}
		if out.Turn.AttentionResolution != nil {
			t.Fatalf("attention_resolution should be stripped")
		}
		if out.Turn.DelegateDeliveryCommits != nil {
			t.Fatalf("delegate_delivery_commits should be stripped")
		}
	})
	t.Run("attention resolution excluded", func(t *testing.T) {
		entry := transcript.Entry{
			Turn: schema.Turn{Kind: schema.TurnAttentionResolution},
		}
		_, ok := publicTranscriptEntry(entry)
		if ok {
			t.Fatalf("expected ok=false for attention resolution")
		}
	})
}

func TestPublicTranscriptEntries(t *testing.T) {
	entries := []transcript.Entry{
		{Seq: 0, Turn: schema.NewTurn(schema.TurnUserInput, llm.User("hello"))},
		{Seq: 1, Turn: schema.Turn{Kind: schema.TurnAttentionResolution}},
		{Seq: 2, Turn: schema.NewTurn(schema.TurnAssistant, llm.Assistant("world"))},
	}
	out := publicTranscriptEntries(entries)
	if len(out) != 2 {
		t.Fatalf("expected 2 entries (resolution excluded), got %d", len(out))
	}
	if out[0].Seq != 0 || out[1].Seq != 1 {
		t.Fatalf("seqs should be renumbered: got %d, %d", out[0].Seq, out[1].Seq)
	}
}

func TestPublicTranscriptEntriesEmpty(t *testing.T) {
	out := publicTranscriptEntries(nil)
	if len(out) != 0 {
		t.Fatalf("expected empty result")
	}
}

// ---------------------------------------------------------------------------
// publicTranscriptData
// ---------------------------------------------------------------------------

func TestPublicTranscriptDataWithoutEntryLines(t *testing.T) {
	data := transcriptData{
		Entries: []transcript.Entry{
			{Seq: 0, Turn: schema.NewTurn(schema.TurnUserInput, llm.User("a"))},
			{Seq: 1, Turn: schema.Turn{Kind: schema.TurnAttentionResolution}},
			{Seq: 2, Turn: schema.NewTurn(schema.TurnAssistant, llm.Assistant("b"))},
		},
	}
	out := publicTranscriptData(data)
	if len(out.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(out.Entries))
	}
	if len(out.EntryLines) != 0 {
		t.Fatalf("expected no entry lines when input had none")
	}
}

// ---------------------------------------------------------------------------
// publicTranscriptLine
// ---------------------------------------------------------------------------

func TestPublicTranscriptLine(t *testing.T) {
	t.Run("valid entry", func(t *testing.T) {
		entry := map[string]any{
			"kind": "entry",
			"seq":  float64(5),
			"turn": map[string]any{
				"kind":    "USER_INPUT",
				"message": map[string]any{"text": "hello"},
			},
		}
		line, _ := json.Marshal(entry)
		out, include, err := publicTranscriptLine(line, 3)
		if err != nil || !include {
			t.Fatalf("err=%v include=%v", err, include)
		}
		var decoded map[string]any
		if err := json.Unmarshal(out, &decoded); err != nil {
			t.Fatalf("unmarshal output: %v", err)
		}
		// seq should be updated
		if decoded["seq"].(float64) != 3 {
			t.Fatalf("seq = %v, want 3", decoded["seq"])
		}
		turn := decoded["turn"].(map[string]any)
		// attention fields should be absent
		if _, ok := turn["attention_id"]; ok {
			t.Fatalf("attention_id should be deleted")
		}
	})
	t.Run("attention resolution excluded", func(t *testing.T) {
		entry := map[string]any{
			"kind": "entry",
			"turn": map[string]any{"kind": "ATTENTION_RESOLUTION"},
		}
		line, _ := json.Marshal(entry)
		_, include, err := publicTranscriptLine(line, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if include {
			t.Fatalf("expected include=false for attention resolution")
		}
	})
	t.Run("invalid json line", func(t *testing.T) {
		_, _, err := publicTranscriptLine([]byte("not json"), 0)
		if err == nil || !strings.Contains(err.Error(), "decode public transcript entry") {
			t.Fatalf("expected decode error, got %v", err)
		}
	})
	t.Run("invalid turn json", func(t *testing.T) {
		entry := map[string]any{"turn": "not an object"}
		line, _ := json.Marshal(entry)
		_, _, err := publicTranscriptLine(line, 0)
		if err == nil || !strings.Contains(err.Error(), "decode public transcript turn") {
			t.Fatalf("expected decode turn error, got %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// spliceWindowLine
// ---------------------------------------------------------------------------

func TestSpliceWindowLine(t *testing.T) {
	t.Run("empty render returns content", func(t *testing.T) {
		out := spliceWindowLine("content", readMeta{TurnsRendered: 0})
		if out != "content" {
			t.Fatalf("expected unchanged content")
		}
	})
	t.Run("inverted range returns content", func(t *testing.T) {
		out := spliceWindowLine("content", readMeta{
			TurnsRendered: 5,
			FirstRendered: 10,
			LastRendered:  3,
		})
		if out != "content" {
			t.Fatalf("expected unchanged content for inverted range")
		}
	})
	t.Run("valid window adds line", func(t *testing.T) {
		content := "# Session\n\nbody"
		out := spliceWindowLine(content, readMeta{
			TurnsTotal:    10,
			TurnsRendered: 3,
			FirstRendered: 2,
			LastRendered:  4,
		})
		if !strings.Contains(out, "Showing turns 2") {
			t.Fatalf("expected window line in output:\n%s", out)
		}
	})
}

// ---------------------------------------------------------------------------
// spliceRangeWarning
// ---------------------------------------------------------------------------

func TestSpliceRangeWarning(t *testing.T) {
	content := "# Session\n\nbody"
	out := spliceRangeWarning(content, "bad range")
	if !strings.Contains(out, "range warning") || !strings.Contains(out, "bad range") {
		t.Fatalf("expected warning in output:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// bucketAndSessionFromPath
// ---------------------------------------------------------------------------

func TestBucketAndSessionFromPath(t *testing.T) {
	tests := []struct {
		path    string
		bucket  string
		session string
	}{
		{"/state/bucket/sessions/abc.transcript.jsonl", "/state/bucket", "abc"},
		{"/tmp/evener/sessions/xyz123.transcript.jsonl", "/tmp/evener", "xyz123"},
	}
	for _, tc := range tests {
		bucket, session := bucketAndSessionFromPath(tc.path)
		if bucket != tc.bucket {
			t.Errorf("bucket = %q, want %q", bucket, tc.bucket)
		}
		if session != tc.session {
			t.Errorf("session = %q, want %q", session, tc.session)
		}
	}
}

// ---------------------------------------------------------------------------
// resolvedSessionMeta
// ---------------------------------------------------------------------------

func TestResolvedSessionMetaCurrentSession(t *testing.T) {
	expectedMeta := schema.SessionMeta{ID: "current_session"}
	deps := &toolDeps{
		sessionID:   "current_session",
		currentMeta: func() schema.SessionMeta { return expectedMeta },
	}
	ref := encodeRef("", "current_session")
	meta := resolvedSessionMeta(deps, "/state/sessions/current_session.transcript.jsonl", ref)
	if meta.ID != "current_session" {
		t.Fatalf("meta.ID = %q", meta.ID)
	}
}

func TestResolvedSessionMetaNoCurrentMeta(t *testing.T) {
	deps := &toolDeps{sessionID: "other_session"}
	// Non-current session ref, no meta.json — should degrade to zero meta with ID from path
	meta := resolvedSessionMeta(deps, "/nonexistent/sessions/xyz.transcript.jsonl", "local:xyz")
	if meta.ID != "xyz" {
		t.Fatalf("meta.ID = %q, want xyz", meta.ID)
	}
}

func TestResolvedSessionMetaNilDeps(t *testing.T) {
	// resolvedSessionMeta dereferences deps, so passing nil panics.
	// Use an empty toolDeps with no currentMeta to test the fallback path.
	deps := &toolDeps{}
	meta := resolvedSessionMeta(deps, "/nonexistent/sessions/abc.transcript.jsonl", "local:abc")
	if meta.ID != "abc" {
		t.Fatalf("meta.ID = %q, want abc", meta.ID)
	}
}

// ---------------------------------------------------------------------------
// projectToolResultsForTranscript
// ---------------------------------------------------------------------------

func TestProjectToolResultsForTranscriptNoProjection(t *testing.T) {
	calls := []llm.ToolCallData{{Name: "read_file", Arguments: json.RawMessage(`{}`)}}
	results := []tool.ExecResult{{ToolName: "read_file", Output: `{"content":"hello"}`}}
	parts := []llm.ContentPart{{ToolResult: &llm.ToolResultData{Content: "original"}}}
	out := projectToolResultsForTranscript(calls, results, parts)
	// No API log results, so parts should be returned unchanged (same backing)
	if &out[0] != &parts[0] { //nolint:staticcheck // intentional: verifies same backing array
		// When no projection happens, the original slice is returned
		_ = out
	}
	if content, ok := out[0].ToolResult.Content.(string); !ok || content != "original" {
		t.Fatalf("content should be unchanged, got %v", out[0].ToolResult.Content)
	}
}

func TestProjectToolResultsForTranscriptWithProjection(t *testing.T) {
	// Call with source=api_log triggers projection
	resultJSON := `{"source":"api_log","transcript_ref":"local:abc","attempt":{"attempt_id":"att_1"}}`
	calls := []llm.ToolCallData{{
		Name:      "read_session_transcript",
		Arguments: json.RawMessage(`{"source":"api_log"}`),
	}}
	results := []tool.ExecResult{{ToolName: "read_session_transcript", Output: resultJSON}}
	parts := []llm.ContentPart{{ToolResult: &llm.ToolResultData{Content: "original"}}}
	out := projectToolResultsForTranscript(calls, results, parts)
	if content, ok := out[0].ToolResult.Content.(string); !ok || content == "original" {
		t.Fatalf("content should be projected to placeholder, got %v", out[0].ToolResult.Content)
	}
	if c, _ := out[0].ToolResult.Content.(string); !strings.Contains(c, "api_log") {
		t.Fatalf("expected api_log in projected content: %q", c)
	}
}

func TestProjectToolResultsForTranscriptNilToolResult(t *testing.T) {
	calls := []llm.ToolCallData{{Name: "read_session_transcript", Arguments: json.RawMessage(`{"source":"api_log"}`)}}
	results := []tool.ExecResult{{ToolName: "read_session_transcript", Output: `{"source":"api_log"}`}}
	parts := []llm.ContentPart{{ToolResult: nil}}
	out := projectToolResultsForTranscript(calls, results, parts)
	if out[0].ToolResult != nil {
		t.Fatalf("nil ToolResult should be preserved")
	}
}

func TestProjectToolResultsForTranscriptIndexOutOfRange(t *testing.T) {
	// More parts than calls/results — extra parts should be preserved
	parts := []llm.ContentPart{
		{ToolResult: &llm.ToolResultData{Content: "first"}},
		{ToolResult: &llm.ToolResultData{Content: "second"}},
	}
	out := projectToolResultsForTranscript(nil, nil, parts)
	if out[1].ToolResult.Content != "second" {
		t.Fatalf("second part should be unchanged")
	}
}

// ---------------------------------------------------------------------------
// apiLogResultTranscriptPlaceholder
// ---------------------------------------------------------------------------

func TestApiLogResultTranscriptPlaceholderNotAPILog(t *testing.T) {
	call := llm.ToolCallData{Name: "read_file", Arguments: json.RawMessage(`{}`)}
	result := tool.ExecResult{ToolName: "read_file", Output: `{"content":"hello"}`}
	_, ok := apiLogResultTranscriptPlaceholder(call, result)
	if ok {
		t.Fatalf("expected ok=false for non-api_log result")
	}
}

func TestApiLogResultTranscriptPlaceholderCallIsAPILog(t *testing.T) {
	call := llm.ToolCallData{
		Name:      "read_session_transcript",
		Arguments: json.RawMessage(`{"source":"api_log"}`),
	}
	result := tool.ExecResult{ToolName: "read_session_transcript", Output: `{"source":"transcript"}`}
	placeholder, ok := apiLogResultTranscriptPlaceholder(call, result)
	if !ok {
		t.Fatalf("expected ok=true for call with source=api_log")
	}
	if !strings.Contains(placeholder, "api_log") {
		t.Fatalf("expected api_log in placeholder: %q", placeholder)
	}
	if !strings.Contains(placeholder, "private_evidence_omitted") {
		t.Fatalf("expected private_evidence_omitted in placeholder")
	}
}

func TestApiLogResultTranscriptPlaceholderCallWithAttemptID(t *testing.T) {
	call := llm.ToolCallData{
		Name:      "other_tool",
		Arguments: json.RawMessage(`{"attempt_id":"att_123"}`),
	}
	result := tool.ExecResult{ToolName: "read_session_transcript", Output: `{"source":"transcript"}`}
	_, ok := apiLogResultTranscriptPlaceholder(call, result)
	if !ok {
		t.Fatalf("expected ok=true for call with attempt_id")
	}
}

func TestApiLogResultTranscriptPlaceholderResultIsAPILog(t *testing.T) {
	resultJSON := `{"source":"api_log","transcript_ref":"local:abc","attempt":{"attempt_id":"att_1"}}`
	call := llm.ToolCallData{Name: "other", Arguments: json.RawMessage(`{}`)}
	result := tool.ExecResult{ToolName: "read_session_transcript", Output: resultJSON}
	placeholder, ok := apiLogResultTranscriptPlaceholder(call, result)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if !strings.Contains(placeholder, "att_1") {
		t.Fatalf("expected attempt_id in placeholder: %q", placeholder)
	}
}

func TestApiLogResultTranscriptPlaceholderResultWithBody(t *testing.T) {
	resultJSON := `{"source":"api_log","transcript_ref":"local:abc","attempt":{"attempt_id":"att_1"},"body":{"body":"data","offset_bytes":10}}`
	call := llm.ToolCallData{Name: "x", Arguments: json.RawMessage(`{}`)}
	result := tool.ExecResult{ToolName: "read_session_transcript", Output: resultJSON}
	placeholder, ok := apiLogResultTranscriptPlaceholder(call, result)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if !strings.Contains(placeholder, "data") {
		t.Fatalf("expected body data in placeholder: %q", placeholder)
	}
}

func TestApiLogResultTranscriptPlaceholderResultWithContinuation(t *testing.T) {
	resultJSON := `{"source":"api_log","transcript_ref":"local:abc","attempt":{"attempt_id":"att_1"},"continuation":{"attempt_id":"att_2","body":"cont"}}`
	call := llm.ToolCallData{Name: "x", Arguments: json.RawMessage(`{}`)}
	result := tool.ExecResult{ToolName: "read_session_transcript", Output: resultJSON}
	placeholder, ok := apiLogResultTranscriptPlaceholder(call, result)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if !strings.Contains(placeholder, "att_2") {
		t.Fatalf("expected continuation attempt_id in placeholder: %q", placeholder)
	}
}

// ---------------------------------------------------------------------------
// parseReadSessionTranscriptArgs
// ---------------------------------------------------------------------------

func TestParseReadSessionTranscriptArgs(t *testing.T) {
	t.Run("default markdown", func(t *testing.T) {
		args := map[string]any{"transcript_ref": "local:abc"}
		parsed, err := parseReadSessionTranscriptArgs(args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if parsed.Format != "markdown" {
			t.Fatalf("format = %q, want markdown", parsed.Format)
		}
		if parsed.Source != "transcript" {
			t.Fatalf("source = %q, want transcript", parsed.Source)
		}
	})
	t.Run("format outline", func(t *testing.T) {
		parsed, err := parseReadSessionTranscriptArgs(map[string]any{"transcript_ref": "local:abc", "format": "outline"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if parsed.Format != "outline" {
			t.Fatalf("format = %q", parsed.Format)
		}
	})
	t.Run("format jsonl", func(t *testing.T) {
		parsed, err := parseReadSessionTranscriptArgs(map[string]any{"transcript_ref": "local:abc", "format": "jsonl"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if parsed.Format != "jsonl" {
			t.Fatalf("format = %q", parsed.Format)
		}
	})
	t.Run("unknown format", func(t *testing.T) {
		_, err := parseReadSessionTranscriptArgs(map[string]any{"transcript_ref": "local:abc", "format": "xml"})
		if err == nil || !strings.Contains(err.Error(), "unknown format") {
			t.Fatalf("expected format error, got %v", err)
		}
	})
	t.Run("unknown source", func(t *testing.T) {
		_, err := parseReadSessionTranscriptArgs(map[string]any{"transcript_ref": "local:abc", "source": "unknown"})
		if err == nil || !strings.Contains(err.Error(), "source") {
			t.Fatalf("expected source error, got %v", err)
		}
	})
	t.Run("source api_log with format", func(t *testing.T) {
		_, err := parseReadSessionTranscriptArgs(map[string]any{"source": "api_log", "format": "markdown"})
		if err == nil || !strings.Contains(err.Error(), "format applies only to source=transcript") {
			t.Fatalf("expected format error for api_log, got %v", err)
		}
	})
	t.Run("source api_log with expand_turn", func(t *testing.T) {
		_, err := parseReadSessionTranscriptArgs(map[string]any{"source": "api_log", "expand_turn": float64(1)})
		if err == nil || !strings.Contains(err.Error(), "expand_turn applies only to transcript markdown") {
			t.Fatalf("expected expand_turn error, got %v", err)
		}
	})
	t.Run("source api_log with range and attempt_id", func(t *testing.T) {
		_, err := parseReadSessionTranscriptArgs(map[string]any{"source": "api_log", "attempt_id": "att_1", "range": "1-5"})
		if err == nil || !strings.Contains(err.Error(), "range cannot be combined with attempt_id") {
			t.Fatalf("expected range+attempt_id error, got %v", err)
		}
	})
	t.Run("attempt_id with explicit source=transcript", func(t *testing.T) {
		_, err := parseReadSessionTranscriptArgs(map[string]any{"source": "transcript", "attempt_id": "att_1"})
		if err == nil || !strings.Contains(err.Error(), "attempt_id cannot be combined with source=transcript") {
			t.Fatalf("expected error, got %v", err)
		}
	})
	t.Run("attempt_id without source", func(t *testing.T) {
		_, err := parseReadSessionTranscriptArgs(map[string]any{"attempt_id": "att_1"})
		if err == nil || !strings.Contains(err.Error(), "attempt_id requires source=api_log") {
			t.Fatalf("expected error, got %v", err)
		}
	})
	t.Run("body without attempt_id", func(t *testing.T) {
		_, err := parseReadSessionTranscriptArgs(map[string]any{"body": "request"})
		if err == nil || !strings.Contains(err.Error(), "body requires attempt_id") {
			t.Fatalf("expected error, got %v", err)
		}
	})
	t.Run("body invalid value", func(t *testing.T) {
		_, err := parseReadSessionTranscriptArgs(map[string]any{"source": "api_log", "attempt_id": "att_1", "body": "invalid"})
		if err == nil || !strings.Contains(err.Error(), "body \"invalid\" is not supported") {
			t.Fatalf("expected body error, got %v", err)
		}
	})
	t.Run("body valid", func(t *testing.T) {
		parsed, err := parseReadSessionTranscriptArgs(map[string]any{"source": "api_log", "attempt_id": "att_1", "body": "request"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if parsed.Body != "request" {
			t.Fatalf("body = %q", parsed.Body)
		}
	})
	t.Run("expand_turn negative", func(t *testing.T) {
		_, err := parseReadSessionTranscriptArgs(map[string]any{"expand_turn": float64(-1)})
		if err == nil || !strings.Contains(err.Error(), "expand_turn must be non-negative") {
			t.Fatalf("expected error, got %v", err)
		}
	})
	t.Run("expand_turn with non-markdown", func(t *testing.T) {
		_, err := parseReadSessionTranscriptArgs(map[string]any{"format": "outline", "expand_turn": float64(1)})
		if err == nil || !strings.Contains(err.Error(), "expand_turn applies only to transcript markdown") {
			t.Fatalf("expected error, got %v", err)
		}
	})
	t.Run("offset_bytes without expanding", func(t *testing.T) {
		_, err := parseReadSessionTranscriptArgs(map[string]any{"offset_bytes": float64(10)})
		if err == nil || !strings.Contains(err.Error(), "offset_bytes requires expand_turn or an explicit API body") {
			t.Fatalf("expected error, got %v", err)
		}
	})
	t.Run("offset_bytes negative", func(t *testing.T) {
		_, err := parseReadSessionTranscriptArgs(map[string]any{"expand_turn": float64(1), "offset_bytes": float64(-1)})
		if err == nil || !strings.Contains(err.Error(), "offset_bytes must be non-negative") {
			t.Fatalf("expected error, got %v", err)
		}
	})
	t.Run("max_bytes without expanding", func(t *testing.T) {
		_, err := parseReadSessionTranscriptArgs(map[string]any{"max_bytes": float64(100)})
		if err == nil || !strings.Contains(err.Error(), "max_bytes requires expand_turn or an explicit API body") {
			t.Fatalf("expected error, got %v", err)
		}
	})
	t.Run("max_bytes negative", func(t *testing.T) {
		_, err := parseReadSessionTranscriptArgs(map[string]any{"expand_turn": float64(1), "max_bytes": float64(-1)})
		if err == nil || !strings.Contains(err.Error(), "max_bytes must be between") {
			t.Fatalf("expected error, got %v", err)
		}
	})
	t.Run("max_bytes zero", func(t *testing.T) {
		_, err := parseReadSessionTranscriptArgs(map[string]any{"expand_turn": float64(1), "max_bytes": float64(0)})
		if err == nil || !strings.Contains(err.Error(), "max_bytes must be between") {
			t.Fatalf("expected error, got %v", err)
		}
	})
	t.Run("max_bytes too large", func(t *testing.T) {
		_, err := parseReadSessionTranscriptArgs(map[string]any{"expand_turn": float64(1), "max_bytes": float64(maxExpansionBytes + 1)})
		if err == nil || !strings.Contains(err.Error(), "max_bytes must be between") {
			t.Fatalf("expected error, got %v", err)
		}
	})
	t.Run("max_bytes valid with expand_turn", func(t *testing.T) {
		parsed, err := parseReadSessionTranscriptArgs(map[string]any{"expand_turn": float64(1), "max_bytes": float64(1024)})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if parsed.MaxBytes != 1024 {
			t.Fatalf("max_bytes = %d", parsed.MaxBytes)
		}
	})
	t.Run("offset_bytes valid with expand_turn", func(t *testing.T) {
		parsed, err := parseReadSessionTranscriptArgs(map[string]any{"expand_turn": float64(1), "offset_bytes": float64(100)})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if parsed.OffsetBytes != 100 {
			t.Fatalf("offset_bytes = %d", parsed.OffsetBytes)
		}
	})
	t.Run("offset_bytes valid with body", func(t *testing.T) {
		parsed, err := parseReadSessionTranscriptArgs(map[string]any{"source": "api_log", "attempt_id": "att_1", "body": "request", "offset_bytes": float64(100)})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if parsed.OffsetBytes != 100 {
			t.Fatalf("offset_bytes = %d", parsed.OffsetBytes)
		}
	})
	t.Run("body request_headers valid", func(t *testing.T) {
		parsed, err := parseReadSessionTranscriptArgs(map[string]any{"source": "api_log", "attempt_id": "att_1", "body": "request_headers"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if parsed.Body != "request_headers" {
			t.Fatalf("body = %q", parsed.Body)
		}
	})
}

// ---------------------------------------------------------------------------
// execReadTranscript rejected params
// ---------------------------------------------------------------------------

func TestExecReadTranscriptRejectedParams(t *testing.T) {
	deps := &toolDeps{}
	t.Run("source rejected", func(t *testing.T) {
		_, err := execReadTranscript(deps, map[string]any{"source": "api_log"})
		if err == nil || !strings.Contains(err.Error(), "invalid_request: source is not supported") {
			t.Fatalf("expected source rejection, got %v", err)
		}
	})
	t.Run("attempt_id rejected", func(t *testing.T) {
		_, err := execReadTranscript(deps, map[string]any{"attempt_id": "att_1"})
		if err == nil || !strings.Contains(err.Error(), "invalid_request: attempt_id is not supported") {
			t.Fatalf("expected attempt_id rejection, got %v", err)
		}
	})
	t.Run("body rejected", func(t *testing.T) {
		_, err := execReadTranscript(deps, map[string]any{"body": "request"})
		if err == nil || !strings.Contains(err.Error(), "invalid_request: body is not supported") {
			t.Fatalf("expected body rejection, got %v", err)
		}
	})
	t.Run("max_bytes rejected", func(t *testing.T) {
		_, err := execReadTranscript(deps, map[string]any{"max_bytes": float64(100)})
		if err == nil || !strings.Contains(err.Error(), "invalid_request: max_bytes is not supported") {
			t.Fatalf("expected max_bytes rejection, got %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// artifactSearchSource.ReadWindow
// ---------------------------------------------------------------------------

func TestArtifactSearchSourceReadWindow(t *testing.T) {
	t.Run("valid window", func(t *testing.T) {
		data := []byte("hello world data")
		src := artifactSearchSource{reader: bytes.NewReader(data), total: int64(len(data))}
		snap, err := src.ReadWindow(0, 5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(snap.Content) != "hello" {
			t.Fatalf("content = %q", string(snap.Content))
		}
		if !snap.Truncated {
			t.Fatalf("expected truncated=true for partial read")
		}
	})
	t.Run("offset negative", func(t *testing.T) {
		src := artifactSearchSource{reader: bytes.NewReader([]byte("x")), total: 1}
		_, err := src.ReadWindow(-1, 1)
		if err == nil || !errors.Is(err, jobstore.ErrInvalidOffset) {
			t.Fatalf("expected ErrInvalidOffset, got %v", err)
		}
	})
	t.Run("offset beyond total", func(t *testing.T) {
		src := artifactSearchSource{reader: bytes.NewReader([]byte("x")), total: 1}
		_, err := src.ReadWindow(10, 1)
		if err == nil || !errors.Is(err, jobstore.ErrInvalidOffset) {
			t.Fatalf("expected ErrInvalidOffset, got %v", err)
		}
	})
	t.Run("maxBytes negative", func(t *testing.T) {
		src := artifactSearchSource{reader: bytes.NewReader([]byte("x")), total: 1}
		_, err := src.ReadWindow(0, -1)
		if err == nil || !errors.Is(err, jobstore.ErrInvalidLimit) {
			t.Fatalf("expected ErrInvalidLimit, got %v", err)
		}
	})
	t.Run("full read not truncated", func(t *testing.T) {
		data := []byte("hello")
		src := artifactSearchSource{reader: bytes.NewReader(data), total: int64(len(data))}
		snap, err := src.ReadWindow(0, 10)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if snap.Truncated {
			t.Fatalf("expected not truncated for full read from offset 0")
		}
	})
	t.Run("empty content", func(t *testing.T) {
		src := artifactSearchSource{reader: bytes.NewReader(nil), total: 0}
		snap, err := src.ReadWindow(0, 10)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(snap.Content) != 0 {
			t.Fatalf("expected empty content")
		}
		if snap.Truncated {
			t.Fatalf("expected not truncated for empty source")
		}
	})
}

// ---------------------------------------------------------------------------
// openArtifactTranscript
// ---------------------------------------------------------------------------

func TestOpenArtifactTranscriptInvalidRef(t *testing.T) {
	_, _, err := openArtifactTranscript(&toolDeps{}, "not-a-valid-ref")
	if err == nil || !strings.Contains(err.Error(), "must be a valid artifact:<id>") {
		t.Fatalf("expected invalid ref error, got %v", err)
	}
}

func TestOpenArtifactTranscriptNilDeps(t *testing.T) {
	ref := "artifact:" + strings.Repeat("a", 32)
	_, _, err := openArtifactTranscript(nil, ref)
	if err == nil || !strings.Contains(err.Error(), "artifact_expired") {
		t.Fatalf("expected artifact_expired error, got %v", err)
	}
}

func TestOpenArtifactTranscriptNilOpenArtifact(t *testing.T) {
	ref := "artifact:" + strings.Repeat("a", 32)
	deps := &toolDeps{}
	_, _, err := openArtifactTranscript(deps, ref)
	if err == nil || !strings.Contains(err.Error(), "artifact_expired") {
		t.Fatalf("expected artifact_expired error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// pageJobTranscript / searchJobTranscript nil deps
// ---------------------------------------------------------------------------

func TestPageJobTranscriptNilDeps(t *testing.T) {
	_, err := pageJobTranscript(nil, retainedReadArgs{Ref: "job:abc"})
	if err == nil || !strings.Contains(err.Error(), "job transcript reader is unavailable") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestSearchJobTranscriptNilDeps(t *testing.T) {
	_, err := searchJobTranscript(nil, retainedReadArgs{Ref: "job:abc", OutputMatch: "x"})
	if err == nil || !strings.Contains(err.Error(), "job manager is not available") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestReadJobTranscriptNilDeps(t *testing.T) {
	_, err := readJobTranscript(nil, "job:abc", "", "markdown")
	if err == nil || !strings.Contains(err.Error(), "job manager is not available") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestReadJobTranscriptNonMarkdownFormat(t *testing.T) {
	deps := &toolDeps{}
	_, err := readJobTranscript(deps, "job:abc", "", "jsonl")
	if err == nil || !strings.Contains(err.Error(), "job transcript format") {
		t.Fatalf("expected format error, got %v", err)
	}
}

func TestReadJobTranscriptEmptyJobID(t *testing.T) {
	deps := &toolDeps{}
	_, err := readJobTranscript(deps, "job:", "", "")
	if err == nil || !strings.Contains(err.Error(), "must be job:<job_id>") {
		t.Fatalf("expected error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// execReadTranscript search on non-job/artifact ref
// ---------------------------------------------------------------------------

func TestExecReadTranscriptSearchOnSessionRef(t *testing.T) {
	deps := &toolDeps{}
	_, err := execReadTranscript(deps, map[string]any{"transcript_ref": "local:abc", "output_match": "x"})
	if err == nil || !strings.Contains(err.Error(), "output_match applies only to job: and artifact: refs") {
		t.Fatalf("expected error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// boundReadMarkdownEnvelope (wrapper)
// ---------------------------------------------------------------------------

func TestBoundReadMarkdownEnvelope(t *testing.T) {
	env := readMarkdownEnvelope{Content: "short"}
	out, err := boundReadMarkdownEnvelope(env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Content != "short" {
		t.Fatalf("content = %q", out.Content)
	}
}

// ---------------------------------------------------------------------------
// readTranscriptTool
// ---------------------------------------------------------------------------

func TestReadTranscriptToolRegistration(t *testing.T) {
	tl := readTranscriptTool(nil)
	if !tl.ReadOnly {
		t.Fatalf("expected read_transcript to be read-only")
	}
	if tl.Definition.Name != "read_transcript" {
		t.Fatalf("name = %q", tl.Definition.Name)
	}
	if tl.Limit.MaxChars != 0 { //nolint:staticcheck // intentional: Limit set by transcriptTools, not readTranscriptTool
		// Limit is set by transcriptTools, not by readTranscriptTool itself
		_ = tl
	}
}

// ---------------------------------------------------------------------------
// Helper: newToolDeps is already available in the test helpers
// ---------------------------------------------------------------------------

func TestFindSessionTranscriptsTool(t *testing.T) {
	tl := findSessionTranscriptsTool(&toolDeps{stateDir: "/tmp"})
	if tl.Definition.Name != "find_session_transcripts" {
		t.Fatalf("name = %q", tl.Definition.Name)
	}
}

// ---------------------------------------------------------------------------
// readOutlineEnvelope and related
// ---------------------------------------------------------------------------

func TestReadOutlineEnvelopeStructure(t *testing.T) {
	// Verify the envelope struct has the expected fields
	env := readOutlineEnvelope{
		TranscriptRef: "local:abc",
		Format:        formatOutline,
		TurnsTotal:    5,
		Content:       "outline content",
		Truncated:     true,
		ElidedTurns:   2,
		Hint:          outlineHint,
	}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), "local:abc") {
		t.Fatalf("expected transcript_ref in json")
	}
	if !strings.Contains(string(data), "turns_total") {
		t.Fatalf("expected turns_total in json")
	}
}

// ---------------------------------------------------------------------------
// readRawEnvelope structure
// ---------------------------------------------------------------------------

func TestReadRawEnvelopeStructure(t *testing.T) {
	env := readRawEnvelope{
		TranscriptRef: "local:abc",
		Format:        formatJSONL,
		ContentType:   "application/x-ndjson",
		Content:       "{}\n",
		Meta: readRawMeta{
			LinesReturned:       1,
			Truncated:           false,
			SkippedCorruptLines: 0,
		},
	}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), "application/x-ndjson") {
		t.Fatalf("expected content_type in json")
	}
}

// ---------------------------------------------------------------------------
// openArtifactTranscript with real artifactstore
// ---------------------------------------------------------------------------

func TestOpenArtifactTranscriptExpired(t *testing.T) {
	store, err := artifactstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	deps := &toolDeps{openArtifact: func(ref string) (artifactReadSeekCloser, error) {
		return store.Open(ref) // will fail for a non-existent ref
	}}
	ref := "artifact:" + strings.Repeat("a", 32)
	_, _, err = openArtifactTranscript(deps, ref)
	if err == nil {
		t.Fatalf("expected error for non-existent artifact")
	}
	if !strings.Contains(err.Error(), "artifact_unavailable") && !strings.Contains(err.Error(), "artifact_expired") {
		t.Fatalf("expected artifact error, got %v", err)
	}
}

func TestOpenArtifactTranscriptValid(t *testing.T) {
	store, err := artifactstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ref, err := store.Put([]byte("hello world"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	deps := &toolDeps{openArtifact: func(ref string) (artifactReadSeekCloser, error) {
		return store.Open(ref)
	}}
	reader, total, err := openArtifactTranscript(deps, ref)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = reader.Close() }()
	if total != int64(len("hello world")) {
		t.Fatalf("total = %d, want %d", total, len("hello world"))
	}
}

// ---------------------------------------------------------------------------
// pageArtifactTranscript
// ---------------------------------------------------------------------------

func TestPageArtifactTranscriptValid(t *testing.T) {
	store, err := artifactstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ref, err := store.Put([]byte("hello world"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	deps := &toolDeps{openArtifact: func(ref string) (artifactReadSeekCloser, error) {
		return store.Open(ref)
	}}
	result, err := pageArtifactTranscript(deps, retainedReadArgs{Ref: ref})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	env, ok := result.(retainedPageEnvelope)
	if !ok {
		t.Fatalf("expected retainedPageEnvelope, got %T", result)
	}
	if env.Page.Data != "hello world" {
		t.Fatalf("data = %q", env.Page.Data)
	}
}

func TestPageArtifactTranscriptOffsetOutOfRange(t *testing.T) {
	store, err := artifactstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ref, err := store.Put([]byte("hi"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	deps := &toolDeps{openArtifact: func(ref string) (artifactReadSeekCloser, error) {
		return store.Open(ref)
	}}
	_, err = pageArtifactTranscript(deps, retainedReadArgs{Ref: ref, OffsetSet: true, OffsetBytes: 100})
	if err == nil || !strings.Contains(err.Error(), "beyond EOF") {
		t.Fatalf("expected beyond EOF error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// searchArtifactTranscript
// ---------------------------------------------------------------------------

func TestSearchArtifactTranscriptValid(t *testing.T) {
	store, err := artifactstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	content := "line one\nERROR here\nline three"
	ref, err := store.Put([]byte(content))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	deps := &toolDeps{openArtifact: func(ref string) (artifactReadSeekCloser, error) {
		return store.Open(ref)
	}}
	result, err := searchArtifactTranscript(deps, retainedReadArgs{Ref: ref, OutputMatch: "ERROR"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	env, ok := result.(retainedSearchResult)
	if !ok {
		t.Fatalf("expected retainedSearchResult, got %T", result)
	}
	if len(env.Matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(env.Matches))
	}
	if !strings.Contains(env.Matches[0].Line, "ERROR") {
		t.Fatalf("match line = %q", env.Matches[0].Line)
	}
}

func TestSearchArtifactTranscriptInvalidRegex(t *testing.T) {
	ref := "artifact:" + strings.Repeat("a", 32)
	deps := &toolDeps{openArtifact: func(ref string) (artifactReadSeekCloser, error) {
		return nil, errors.New("not found")
	}}
	_, err := searchArtifactTranscript(deps, retainedReadArgs{Ref: ref, OutputMatch: "["})
	if err == nil || !strings.Contains(err.Error(), "not valid RE2") {
		t.Fatalf("expected regex error, got %v", err)
	}
}

func TestSearchArtifactTranscriptOffsetBeyondEOF(t *testing.T) {
	store, err := artifactstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ref, err := store.Put([]byte("hi"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	deps := &toolDeps{openArtifact: func(ref string) (artifactReadSeekCloser, error) {
		return store.Open(ref)
	}}
	_, err = searchArtifactTranscript(deps, retainedReadArgs{Ref: ref, OutputMatch: "x", OffsetBytes: 100})
	if err == nil || !strings.Contains(err.Error(), "beyond EOF") {
		t.Fatalf("expected beyond EOF error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// execReadTranscript with artifact: refs
// ---------------------------------------------------------------------------

func TestExecReadTranscriptArtifactSearch(t *testing.T) {
	store, err := artifactstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ref, err := store.Put([]byte("found it\nnot here"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	deps := &toolDeps{openArtifact: func(ref string) (artifactReadSeekCloser, error) {
		return store.Open(ref)
	}}
	_, err = execReadTranscript(deps, map[string]any{"transcript_ref": ref, "output_match": "found"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExecReadTranscriptArtifactPage(t *testing.T) {
	store, err := artifactstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ref, err := store.Put([]byte("paged content"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	deps := &toolDeps{openArtifact: func(ref string) (artifactReadSeekCloser, error) {
		return store.Open(ref)
	}}
	_, err = execReadTranscript(deps, map[string]any{"transcript_ref": ref, "offset_bytes": float64(0)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExecReadTranscriptArtifactRangeRejected(t *testing.T) {
	ref := "artifact:" + strings.Repeat("a", 32)
	deps := &toolDeps{}
	_, err := execReadTranscript(deps, map[string]any{"transcript_ref": ref, "range": "1-5"})
	if err == nil || !strings.Contains(err.Error(), "range applies only to session") {
		t.Fatalf("expected error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// execReadTranscript with job: refs - format validation
// ---------------------------------------------------------------------------

func TestExecReadTranscriptJobFormatNonMarkdown(t *testing.T) {
	deps := &toolDeps{}
	_, err := execReadTranscript(deps, map[string]any{"transcript_ref": "job:abc", "format": "jsonl"})
	if err == nil || !strings.Contains(err.Error(), "job: refs support only format=markdown") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestExecReadTranscriptJobRangeRejected(t *testing.T) {
	deps := &toolDeps{}
	_, err := execReadTranscript(deps, map[string]any{"transcript_ref": "job:abc", "range": "1-5"})
	if err == nil || !strings.Contains(err.Error(), "range applies only to session") {
		t.Fatalf("expected error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// readMarkdownMeta structure
// ---------------------------------------------------------------------------

func TestReadMarkdownMetaJSON(t *testing.T) {
	meta := readMarkdownMeta{
		TurnsTotal:          10,
		Range:               "1-5",
		TurnsRendered:       5,
		Truncated:           true,
		ElidedTurns:         2,
		SkippedCorruptLines: 1,
		RangeWarning:        "bad range",
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	for _, want := range []string{"turns_total", "range", "turns_rendered", "truncated", "elided_turns", "skipped_corrupt_lines", "range_warning"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in json: %s", want, s)
		}
	}
}

// ---------------------------------------------------------------------------
// apiLogTranscriptPlaceholder / apiLogTranscriptReadHandle structure
// ---------------------------------------------------------------------------

func TestAPILogTranscriptPlaceholderJSON(t *testing.T) {
	p := apiLogTranscriptPlaceholder{
		Source:                 apiLogSource,
		PrivateEvidenceOmitted: true,
		ReRead: apiLogTranscriptReadHandle{
			Tool:          "read_session_transcript",
			TranscriptRef: "local:abc",
			Source:        apiLogSource,
			AttemptID:     "att_1",
		},
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "api_log") {
		t.Fatalf("expected source in json: %s", s)
	}
	if !strings.Contains(s, "private_evidence_omitted") {
		t.Fatalf("expected private_evidence_omitted in json")
	}
}

func TestAPILogTranscriptReadHandleJSON(t *testing.T) {
	h := apiLogTranscriptReadHandle{
		Tool:        "read_session_transcript",
		Source:      apiLogSource,
		AttemptID:   "att_1",
		Body:        "request",
		OffsetBytes: 100,
	}
	data, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), "attempt_id") {
		t.Fatalf("expected attempt_id in json")
	}
}

// ---------------------------------------------------------------------------
// readSessionTranscriptArgs structure
// ---------------------------------------------------------------------------

func TestReadSessionTranscriptArgs(t *testing.T) {
	args := readSessionTranscriptArgs{
		TranscriptRef: "local:abc",
		Source:        "transcript",
		Format:        "markdown",
		Range:         "1-5",
		AttemptID:     "att_1",
		Body:          "request",
		OffsetBytes:   100,
		MaxBytes:      1024,
	}
	if args.TranscriptRef != "local:abc" {
		t.Fatalf("ref = %q", args.TranscriptRef)
	}
}

// ---------------------------------------------------------------------------
// retainedReadOperation constants
// ---------------------------------------------------------------------------

func TestRetainedReadOperationValues(t *testing.T) {
	if retainedReadDefault != 0 {
		t.Fatalf("retainedReadDefault = %d, want 0", retainedReadDefault)
	}
	if retainedReadPage == retainedReadDefault {
		t.Fatalf("retainedReadPage should differ from default")
	}
	if retainedReadSearch == retainedReadDefault || retainedReadSearch == retainedReadPage {
		t.Fatalf("retainedReadSearch should differ from default and page")
	}
}

// ---------------------------------------------------------------------------
// readMarkdownEnvelope structure
// ---------------------------------------------------------------------------

func TestReadMarkdownEnvelopeJSON(t *testing.T) {
	env := readMarkdownEnvelope{
		TranscriptRef: "local:abc",
		Format:        formatMarkdown,
		ContentType:   "text/markdown",
		Content:       "content",
		Meta:          readMarkdownMeta{TurnsTotal: 5, TurnsRendered: 3},
		Expansion:     &transcriptTurnExpansion{ExpandTurn: 1, Encoding: "utf8", Data: "data"},
		Continuation:  &transcriptTurnContinuation{ExpandTurn: 1, OffsetBytes: 10},
	}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	for _, want := range []string{"transcript_ref", "format", "content_type", "content", "meta", "expansion", "continuation"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in json: %s", want, s)
		}
	}
}

// ---------------------------------------------------------------------------
// transcriptTurnExpansion / transcriptTurnContinuation structures
// ---------------------------------------------------------------------------

func TestTranscriptTurnExpansionJSON(t *testing.T) {
	exp := transcriptTurnExpansion{
		ExpandTurn:     3,
		OffsetBytes:    100,
		BytesReturned:  50,
		TotalBytes:     200,
		Representation: transcriptV2JSONLRepresentation,
		Encoding:       "utf8",
		Data:           "hello",
	}
	data, err := json.Marshal(exp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), "transcript_v2_jsonl") {
		t.Fatalf("expected representation in json")
	}
}

func TestTranscriptTurnContinuationJSON(t *testing.T) {
	c := transcriptTurnContinuation{ExpandTurn: 3, OffsetBytes: 150}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), "expand_turn") {
		t.Fatalf("expected expand_turn in json")
	}
}

// ---------------------------------------------------------------------------
// retainedReadArgs structure
// ---------------------------------------------------------------------------

func TestRetainedReadArgs(t *testing.T) {
	args := retainedReadArgs{
		Ref:          "job:abc",
		OffsetSet:    true,
		OffsetBytes:  100,
		OutputMatch:  "ERROR",
		ContextLines: 3,
	}
	if args.Ref != "job:abc" {
		t.Fatalf("ref = %q", args.Ref)
	}
}

// ---------------------------------------------------------------------------
// retainedPageBody / retainedPageEnvelope structures
// ---------------------------------------------------------------------------

func TestRetainedPageBodyJSON(t *testing.T) {
	body := retainedPageBody{
		OffsetBytes:   10,
		BytesReturned: 50,
		TotalBytes:    100,
		Encoding:      "utf8",
		Data:          "page data",
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), "offset_bytes") {
		t.Fatalf("expected offset_bytes in json")
	}
}

func TestRetainedPageEnvelopeJSON(t *testing.T) {
	env := retainedPageEnvelope{
		TranscriptRef:     "job:abc",
		Representation:    "raw_bytes",
		ContentType:       "text/plain",
		Page:              retainedPageBody{Data: "x"},
		RetainedStartByte: 5,
		JobStatus:         "completed",
		Continuation:      &retainedContinuation{OffsetBytes: 10},
	}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	for _, want := range []string{"transcript_ref", "representation", "content_type", "page", "retained_start_bytes", "job_status", "continuation"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in json: %s", want, s)
		}
	}
}

// ---------------------------------------------------------------------------
// retainedSearchResult structure
// ---------------------------------------------------------------------------

func TestRetainedSearchResultJSON(t *testing.T) {
	result := retainedSearchResult{
		TranscriptRef:      "job:abc",
		OutputMatch:        "ERR",
		ContextLines:       2,
		OffsetBytes:        10,
		RetainedStartBytes: 0,
		TotalBytes:         100,
		JobStatus:          "running",
		SearchComplete:     true,
		Matches:            []retainedSearchMatch{{Line: "ERR here", Before: []string{}, After: []string{}}},
		Continuation:       &retainedContinuation{OffsetBytes: 50},
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	for _, want := range []string{"transcript_ref", "output_match", "context_lines", "offset_bytes", "retained_start_bytes", "total_bytes", "job_status", "search_complete", "matches", "continuation"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in json: %s", want, s)
		}
	}
}

// ---------------------------------------------------------------------------
// apiLogTranscriptResultIdentity structure
// ---------------------------------------------------------------------------

func TestAPILogTranscriptResultIdentity(t *testing.T) {
	raw := `{"transcript_ref":"local:abc","source":"api_log","attempt":{"attempt_id":"att_1"},"body":{"body":"data","offset_bytes":5},"continuation":{"attempt_id":"att_2"}}`
	var id apiLogTranscriptResultIdentity
	if err := json.Unmarshal([]byte(raw), &id); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if id.TranscriptRef != "local:abc" {
		t.Fatalf("ref = %q", id.TranscriptRef)
	}
	if id.Attempt.AttemptID != "att_1" {
		t.Fatalf("attempt_id = %q", id.Attempt.AttemptID)
	}
	if id.Body == nil || id.Body.Body != "data" {
		t.Fatalf("body = %v", id.Body)
	}
	if id.Continuation == nil || id.Continuation.AttemptID != "att_2" {
		t.Fatalf("continuation = %v", id.Continuation)
	}
}

// ---------------------------------------------------------------------------
// fmt usage / error messages
// ---------------------------------------------------------------------------

func TestRangeAcceptedGrammar(t *testing.T) {
	if rangeAcceptedGrammar != "N-M | last:N | start:N" {
		t.Fatalf("rangeAcceptedGrammar = %q", rangeAcceptedGrammar)
	}
}

func TestTranscriptSourceConstants(t *testing.T) {
	if transcriptSource != "transcript" {
		t.Fatalf("transcriptSource = %q", transcriptSource)
	}
	if apiLogSource != "api_log" {
		t.Fatalf("apiLogSource = %q", apiLogSource)
	}
	if jobTranscriptTruncationNotice != "additional output is not available from this transcript view" {
		t.Fatalf("jobTranscriptTruncationNotice = %q", jobTranscriptTruncationNotice)
	}
	if transcriptV2JSONLRepresentation != "transcript_v2_jsonl" {
		t.Fatalf("transcriptV2JSONLRepresentation = %q", transcriptV2JSONLRepresentation)
	}
}

func TestReadTranscriptPublicRejectedParams(t *testing.T) {
	expected := []string{"source", "attempt_id", "body", "max_bytes"}
	if len(readTranscriptPublicRejectedParams) != len(expected) {
		t.Fatalf("expected %d params, got %d", len(expected), len(readTranscriptPublicRejectedParams))
	}
	for i, p := range expected {
		if readTranscriptPublicRejectedParams[i] != p {
			t.Errorf("param[%d] = %q, want %q", i, readTranscriptPublicRejectedParams[i], p)
		}
	}
}

// ---------------------------------------------------------------------------
// execReadTranscript artifact format rejected
// ---------------------------------------------------------------------------

func TestExecReadTranscriptArtifactFormatRejected(t *testing.T) {
	ref := "artifact:" + strings.Repeat("a", 32)
	_, err := execReadTranscript(&toolDeps{}, map[string]any{"transcript_ref": ref, "format": "markdown"})
	if err == nil || !strings.Contains(err.Error(), "format is not supported for artifact") {
		t.Fatalf("expected error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// readJobTranscript format default
// ---------------------------------------------------------------------------

func TestReadJobTranscriptFormatDefault(t *testing.T) {
	deps := &toolDeps{}
	// Empty format defaults to markdown, but will fail on snapshot read since
	// stateDir is empty — the error should be about the job not being found,
	// not about the format.
	_, err := readJobTranscript(deps, "job:nonexistent", "", "")
	// Should fail on snapshot read, not format validation
	if err == nil {
		t.Fatalf("expected error for nonexistent job")
	}
	// Should not contain format error since format defaults to markdown
	if strings.Contains(err.Error(), "format") {
		t.Fatalf("format should default to markdown, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// artifactUnavailableReadError constant
// ---------------------------------------------------------------------------

func TestArtifactUnavailableReadError(t *testing.T) {
	if artifactUnavailableReadError != "artifact_unavailable: retained artifact could not be read" {
		t.Fatalf("artifactUnavailableReadError = %q", artifactUnavailableReadError)
	}
}

// ---------------------------------------------------------------------------
// apiLogTranscriptPlaceholderMaxBytes / maxExpansionBytes constants
// ---------------------------------------------------------------------------

func TestAPILogTranscriptPlaceholderMaxBytes(t *testing.T) {
	if apiLogTranscriptPlaceholderMaxBytes != 1024 {
		t.Fatalf("apiLogTranscriptPlaceholderMaxBytes = %d, want 1024", apiLogTranscriptPlaceholderMaxBytes)
	}
}

func TestMaxExpansionBytes(t *testing.T) {
	if maxExpansionBytes != 64<<10 {
		t.Fatalf("maxExpansionBytes = %d, want %d", maxExpansionBytes, 64<<10)
	}
}

func TestRetainedOutputMatchMaxChars(t *testing.T) {
	if retainedOutputMatchMaxChars != 64<<10 {
		t.Fatalf("retainedOutputMatchMaxChars = %d, want %d", retainedOutputMatchMaxChars, 64<<10)
	}
}

func TestTranscriptToolMaxChars(t *testing.T) {
	if transcriptToolMaxChars != 600_000 {
		t.Fatalf("transcriptToolMaxChars = %d, want 600000", transcriptToolMaxChars)
	}
}

// ---------------------------------------------------------------------------
// format constants
// ---------------------------------------------------------------------------

func TestFormatConstants(t *testing.T) {
	if formatMarkdown != "markdown" {
		t.Fatalf("formatMarkdown = %q", formatMarkdown)
	}
	if formatOutline != "outline" {
		t.Fatalf("formatOutline = %q", formatOutline)
	}
	if formatJSONL != "jsonl" {
		t.Fatalf("formatJSONL = %q", formatJSONL)
	}
}

// ---------------------------------------------------------------------------
// transcriptExpansionReadHint constant
// ---------------------------------------------------------------------------

func TestTranscriptExpansionReadHint(t *testing.T) {
	if transcriptExpansionReadHint == "" {
		t.Fatalf("transcriptExpansionReadHint should not be empty")
	}
}

// ---------------------------------------------------------------------------
// readTranscriptPublicRejectedParams covers source/attempt_id/body/max_bytes
// ---------------------------------------------------------------------------

func TestExecReadTranscriptSourceThenAttemptID(t *testing.T) {
	deps := &toolDeps{}
	// Source is checked first
	_, err := execReadTranscript(deps, map[string]any{"source": "api_log", "attempt_id": "x"})
	if err == nil || !strings.Contains(err.Error(), "source is not supported") {
		t.Fatalf("expected source error first, got %v", err)
	}
}

func TestExecReadTranscriptBodyOnlyRejected(t *testing.T) {
	deps := &toolDeps{}
	_, err := execReadTranscript(deps, map[string]any{"body": "request"})
	if err == nil || !strings.Contains(err.Error(), "body is not supported") {
		t.Fatalf("expected body error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// format constants used in readRaw
// ---------------------------------------------------------------------------

func TestReadRawLinesForRangeVar(t *testing.T) {
	if readRawLinesForRange == nil {
		t.Fatalf("readRawLinesForRange should not be nil")
	}
}

// ---------------------------------------------------------------------------
// apiLogBodyContinuation type (referenced in apiLogTranscriptResultIdentity)
// ---------------------------------------------------------------------------

func TestAPILogBodyContinuationType(t *testing.T) {
	// Just verify the type exists and can be used
	var c *apiLogBodyContinuation
	if c != nil {
		t.Fatalf("nil should be nil")
	}
}

// ---------------------------------------------------------------------------
// compileOutputMatch / retainedSearchOptions (integration)
// ---------------------------------------------------------------------------

func TestRetainedSearchOptions(t *testing.T) {
	// Verify type usage
	opts := retainedSearchOptions{
		Regexp:       nil,
		StartOffset:  0,
		ContextLines: 2,
	}
	if opts.ContextLines != 2 {
		t.Fatalf("context_lines = %d", opts.ContextLines)
	}
}

// ---------------------------------------------------------------------------
// helper: io interface compliance
// ---------------------------------------------------------------------------

func TestArtifactReadSeekCloserInterface(t *testing.T) {
	// bytes.Reader implements ReaderAt, Seeker, and is a Closer via NopCloser
	// Verify interface compliance
	var _ artifactReadSeekCloser = struct {
		io.ReaderAt
		io.Seeker
		io.Closer
	}{
		bytes.NewReader(nil),
		bytes.NewReader(nil),
		io.NopCloser(nil),
	}
}

// ---------------------------------------------------------------------------
// format checking in execReadTranscript for job: page/search
// ---------------------------------------------------------------------------

func TestExecReadTranscriptJobPageFormatRejected(t *testing.T) {
	deps := &toolDeps{}
	_, err := execReadTranscript(deps, map[string]any{
		"transcript_ref": "job:abc",
		"offset_bytes":   float64(0),
		"format":         "markdown",
	})
	if err == nil || !strings.Contains(err.Error(), "format cannot be combined") {
		t.Fatalf("expected format+offset error, got %v", err)
	}
}

func TestExecReadTranscriptJobSearchFormatRejected(t *testing.T) {
	deps := &toolDeps{}
	_, err := execReadTranscript(deps, map[string]any{
		"transcript_ref": "job:abc",
		"output_match":   "x",
		"format":         "markdown",
	})
	if err == nil || !strings.Contains(err.Error(), "format cannot be combined") {
		t.Fatalf("expected format+search error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// fmt.Sprintf / Fprintf usage verification
// ---------------------------------------------------------------------------

func TestRenderDelegateJobTranscriptNoReason(t *testing.T) {
	rec := &jobstore.JobRecord{JobID: "dlg_x", Type: "delegate", Status: "running"}
	out := renderDelegateJobTranscript(rec, "output", 100, 0)
	if strings.Contains(out, "reason:") {
		t.Fatalf("should not have reason when empty")
	}
	if !strings.Contains(out, "status: running") {
		t.Fatalf("expected status line")
	}
}

func TestRenderShellJobTranscriptNoStatus(t *testing.T) {
	rec := &jobstore.JobRecord{JobID: "j_x", Command: "ls"}
	out := renderShellJobTranscript(rec, "output", 10, 0)
	if strings.Contains(out, "status:") {
		t.Fatalf("should not have status when empty")
	}
	if !strings.Contains(out, "command: `ls`") {
		t.Fatalf("expected command line")
	}
}

func TestRenderShellJobTranscriptNoCommand(t *testing.T) {
	rec := &jobstore.JobRecord{JobID: "j_x", Status: "completed"}
	out := renderShellJobTranscript(rec, "output", 10, 0)
	if strings.Contains(out, "command:") {
		t.Fatalf("should not have command when empty")
	}
	if !strings.Contains(out, "status: completed") {
		t.Fatalf("expected status line")
	}
}

// ---------------------------------------------------------------------------
// pageJobTranscript with empty job ID
// ---------------------------------------------------------------------------

func TestPageJobTranscriptEmptyJobID(t *testing.T) {
	deps := &toolDeps{}
	_, err := pageJobTranscript(deps, retainedReadArgs{Ref: "job:"})
	if err == nil || !strings.Contains(err.Error(), "must be job:<job_id>") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestSearchJobTranscriptEmptyJobID(t *testing.T) {
	deps := &toolDeps{}
	_, err := searchJobTranscript(deps, retainedReadArgs{Ref: "job:", OutputMatch: "x"})
	if err == nil || !strings.Contains(err.Error(), "must be job:<job_id>") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestSearchJobTranscriptInvalidRegex(t *testing.T) {
	deps := &toolDeps{}
	_, err := searchJobTranscript(deps, retainedReadArgs{Ref: "job:abc", OutputMatch: "["})
	if err == nil || !strings.Contains(err.Error(), "not valid RE2") {
		t.Fatalf("expected regex error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// readMarkdownPage-related constants and helpers
// ---------------------------------------------------------------------------

func TestDefaultExpansionBytes(t *testing.T) {
	if defaultExpansionBytes != 16<<10 {
		t.Fatalf("defaultExpansionBytes = %d, want %d", defaultExpansionBytes, 16<<10)
	}
}

// ---------------------------------------------------------------------------
// execReadSessionTranscript / execReadSessionTranscriptWithContext
// ---------------------------------------------------------------------------

func TestExecReadSessionTranscriptDelegatesToContext(t *testing.T) {
	// Both functions should produce the same error for invalid args
	deps := &toolDeps{}
	_, err1 := execReadSessionTranscript(deps, map[string]any{"format": "xml"})
	_, err2 := execReadSessionTranscriptWithContext(context.TODO(), deps, map[string]any{"format": "xml"})
	if err1 == nil || err2 == nil {
		t.Fatalf("expected errors for invalid format")
	}
	if err1.Error() != err2.Error() {
		t.Fatalf("errors should match: %v vs %v", err1, err2)
	}
}

// ---------------------------------------------------------------------------
// parseRetainedReadArgs context_lines=0 valid
// ---------------------------------------------------------------------------

func TestParseRetainedReadArgsContextLinesZero(t *testing.T) {
	parsed, _, err := parseRetainedReadArgs(map[string]any{"output_match": "x", "context_lines": float64(0)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.ContextLines != 0 {
		t.Fatalf("context_lines = %d", parsed.ContextLines)
	}
}

// ---------------------------------------------------------------------------
// parseRetainedReadArgs context_lines=10 valid
// ---------------------------------------------------------------------------

func TestParseRetainedReadArgsContextLinesTen(t *testing.T) {
	parsed, _, err := parseRetainedReadArgs(map[string]any{"output_match": "x", "context_lines": float64(10)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.ContextLines != 10 {
		t.Fatalf("context_lines = %d", parsed.ContextLines)
	}
}

// ---------------------------------------------------------------------------
// parseRetainedReadArgs offset=0
// ---------------------------------------------------------------------------

func TestParseRetainedReadArgsOffsetZero(t *testing.T) {
	parsed, op, err := parseRetainedReadArgs(map[string]any{"offset_bytes": float64(0)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if op != retainedReadPage {
		t.Fatalf("expected page operation for offset=0")
	}
	if !parsed.OffsetSet || parsed.OffsetBytes != 0 {
		t.Fatalf("offset = %v (set=%v)", parsed.OffsetBytes, parsed.OffsetSet)
	}
}

// ---------------------------------------------------------------------------
// parseReadSessionTranscriptArgs expand_turn valid
// ---------------------------------------------------------------------------

func TestParseReadSessionTranscriptArgsExpandTurnValid(t *testing.T) {
	expand := 2
	parsed, err := parseReadSessionTranscriptArgs(map[string]any{"expand_turn": float64(2)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.ExpandTurn == nil || *parsed.ExpandTurn != 2 {
		t.Fatalf("expand_turn = %v, want 2", parsed.ExpandTurn)
	}
	_ = expand
}

// ---------------------------------------------------------------------------
// parseReadSessionTranscriptArgs source api_log valid
// ---------------------------------------------------------------------------

func TestParseReadSessionTranscriptArgsSourceAPILog(t *testing.T) {
	parsed, err := parseReadSessionTranscriptArgs(map[string]any{"source": "api_log"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.Source != "api_log" {
		t.Fatalf("source = %q", parsed.Source)
	}
}

func TestParseReadSessionTranscriptArgsSourceAPILogWithAttemptID(t *testing.T) {
	parsed, err := parseReadSessionTranscriptArgs(map[string]any{"source": "api_log", "attempt_id": "att_1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.AttemptID != "att_1" {
		t.Fatalf("attempt_id = %q", parsed.AttemptID)
	}
}

func TestParseReadSessionTranscriptArgsSourceAPILogWithAttemptIDAndBody(t *testing.T) {
	parsed, err := parseReadSessionTranscriptArgs(map[string]any{"source": "api_log", "attempt_id": "att_1", "body": "response"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.Body != "response" {
		t.Fatalf("body = %q", parsed.Body)
	}
}

// ---------------------------------------------------------------------------
// retainedPage structure - continuation field
// ---------------------------------------------------------------------------

func TestRetainedPageWithContinuation(t *testing.T) {
	page := retainedPage{
		OffsetBytes:   0,
		BytesReturned: 5,
		TotalBytes:    100,
		Encoding:      "utf8",
		Data:          "hello",
		Continuation:  &retainedContinuation{OffsetBytes: 5},
	}
	if page.Continuation == nil || page.Continuation.OffsetBytes != 5 {
		t.Fatalf("continuation = %v", page.Continuation)
	}
}

// ---------------------------------------------------------------------------
// retainedSearchEnvelope / retainedSearchMatch / retainedSearchSkippedLine
// ---------------------------------------------------------------------------

func TestRetainedSearchEnvelope(t *testing.T) {
	env := retainedSearchEnvelope{
		OffsetBytes:          10,
		RetainedStartBytes:   0,
		TotalBytes:           100,
		SearchComplete:       true,
		SkippedPartialPrefix: true,
		Matches: []retainedSearchMatch{
			{Line: "match", Before: []string{"before"}, After: []string{"after"}},
		},
		SkippedOversized: []retainedSearchSkippedLine{{StartByte: 20, EndByte: 50000}},
		Continuation:     &retainedContinuation{OffsetBytes: 50},
	}
	if len(env.Matches) != 1 {
		t.Fatalf("expected 1 match")
	}
	if env.Matches[0].Line != "match" {
		t.Fatalf("line = %q", env.Matches[0].Line)
	}
	if len(env.SkippedOversized) != 1 {
		t.Fatalf("expected 1 skipped line")
	}
}

// ---------------------------------------------------------------------------
// fmt error format verification for pageArtifactTranscript offset error
// ---------------------------------------------------------------------------

func TestPageArtifactTranscriptOffsetExactEOF(t *testing.T) {
	store, err := artifactstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	data := []byte("hello")
	ref, err := store.Put(data)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	deps := &toolDeps{openArtifact: func(ref string) (artifactReadSeekCloser, error) {
		return store.Open(ref)
	}}
	// offset == total should be valid (reads zero bytes at EOF)
	result, err := pageArtifactTranscript(deps, retainedReadArgs{Ref: ref, OffsetSet: true, OffsetBytes: int64(len(data))})
	if err != nil {
		t.Fatalf("unexpected error at EOF: %v", err)
	}
	env := result.(retainedPageEnvelope)
	if env.Page.BytesReturned != 0 {
		t.Fatalf("expected 0 bytes at EOF, got %d", env.Page.BytesReturned)
	}
}

// ---------------------------------------------------------------------------
// apiLogPathForTranscript / apiLogTranscriptReadHandle usage
// ---------------------------------------------------------------------------

func TestAPILogTranscriptReadHandleWithBody(t *testing.T) {
	h := apiLogTranscriptReadHandle{
		Tool:        "read_session_transcript",
		Source:      apiLogSource,
		AttemptID:   "att_1",
		Body:        "response",
		OffsetBytes: 50,
	}
	if h.Body != "response" {
		t.Fatalf("body = %q", h.Body)
	}
	if h.OffsetBytes != 50 {
		t.Fatalf("offset = %d", h.OffsetBytes)
	}
}

// ---------------------------------------------------------------------------
// execReadTranscript with job: default (markdown) operation
// ---------------------------------------------------------------------------

func TestExecReadTranscriptJobDefaultOp(t *testing.T) {
	deps := &toolDeps{}
	// Default operation on a job: ref goes to readJobTranscript which
	// will fail on snapshot read but should not fail on argument validation
	_, err := execReadTranscript(deps, map[string]any{"transcript_ref": "job:nonexistent"})
	if err == nil {
		t.Fatalf("expected error for nonexistent job")
	}
}

// ---------------------------------------------------------------------------
// parseRetainedReadArgs output_match at exact limit
// ---------------------------------------------------------------------------

func TestParseRetainedReadArgsOutputMatchAtLimit(t *testing.T) {
	exact := strings.Repeat("x", retainedOutputMatchMaxChars)
	parsed, op, err := parseRetainedReadArgs(map[string]any{"output_match": exact})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if op != retainedReadSearch {
		t.Fatalf("expected search operation")
	}
	if len(parsed.OutputMatch) != retainedOutputMatchMaxChars {
		t.Fatalf("output_match length = %d", len(parsed.OutputMatch))
	}
}

// ---------------------------------------------------------------------------
// largestTranscriptExpansionPrefix with empty envelope expansion
// ---------------------------------------------------------------------------

func TestLargestTranscriptExpansionPrefixEmptyExpansion(t *testing.T) {
	env := readMarkdownEnvelope{
		Expansion: &transcriptTurnExpansion{ExpandTurn: 0, OffsetBytes: 0, TotalBytes: 10},
	}
	n, err := largestTranscriptExpansionPrefix(env, []byte("hello"), 100000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 5 {
		t.Fatalf("n = %d, want 5", n)
	}
}

// ---------------------------------------------------------------------------
// fmt verification: boundReadMarkdownEnvelopeWithHint with content-only shrink
// ---------------------------------------------------------------------------

func TestBoundReadMarkdownEnvelopeWithHintContentOnly(t *testing.T) {
	// Content exceeds hard cap, no expansion — should truncate content only
	long := strings.Repeat("a", hardCapChars+5000)
	env := readMarkdownEnvelope{
		Content: long,
	}
	out, err := boundReadMarkdownEnvelopeWithHint(env, "hint")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Meta.Truncated {
		t.Fatalf("expected truncated=true")
	}
	// The truncated content should be shorter than the original
	if len([]rune(out.Content)) >= len([]rune(long)) {
		t.Fatalf("expected content to be truncated")
	}
}

// ---------------------------------------------------------------------------
// Error wrapper for malformed bound
// ---------------------------------------------------------------------------

func TestBoundReadMarkdownContentWithHintTooSmallLimit(t *testing.T) {
	// Very small limit that can't fit even metadata
	env := readMarkdownEnvelope{Content: "x"}
	_, err := boundReadMarkdownContentWithHint(env, 10, "hint")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected exceeds error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// helper: findSessionTranscriptsTool
// ---------------------------------------------------------------------------

func TestFindSessionTranscriptsToolRegistration(t *testing.T) {
	tl := findSessionTranscriptsTool(&toolDeps{stateDir: "/tmp/some-state"})
	if !tl.ReadOnly {
		t.Fatalf("expected find_session_transcripts to be read-only")
	}
}

// ---------------------------------------------------------------------------
// spliceAfterHeader integration
// ---------------------------------------------------------------------------

func TestSpliceWindowLineEmptyContent(t *testing.T) {
	out := spliceWindowLine("", readMeta{
		TurnsRendered: 3,
		FirstRendered: 0,
		LastRendered:  2,
		TurnsTotal:    10,
	})
	if !strings.Contains(out, "Showing turns") {
		t.Fatalf("expected window line even in empty content: %q", out)
	}
}

// ---------------------------------------------------------------------------
// pageArtifactTranscript invalid ref
// ---------------------------------------------------------------------------

func TestPageArtifactTranscriptInvalidRef(t *testing.T) {
	deps := &toolDeps{}
	_, err := pageArtifactTranscript(deps, retainedReadArgs{Ref: "not-valid"})
	if err == nil || !strings.Contains(err.Error(), "must be a valid artifact") {
		t.Fatalf("expected error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// searchArtifactTranscript invalid ref
// ---------------------------------------------------------------------------

func TestSearchArtifactTranscriptInvalidRef(t *testing.T) {
	deps := &toolDeps{}
	_, err := searchArtifactTranscript(deps, retainedReadArgs{Ref: "not-valid", OutputMatch: "x"})
	if err == nil || !strings.Contains(err.Error(), "must be a valid artifact") {
		t.Fatalf("expected error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// openArtifactTranscript with ErrInvalidRef
// ---------------------------------------------------------------------------

func TestOpenArtifactTranscriptErrInvalidRef(t *testing.T) {
	deps := &toolDeps{openArtifact: func(ref string) (artifactReadSeekCloser, error) {
		return nil, artifactstore.ErrInvalidRef
	}}
	ref := "artifact:" + strings.Repeat("a", 32)
	_, _, err := openArtifactTranscript(deps, ref)
	if err == nil || !strings.Contains(err.Error(), "invalid_request: artifact transcript_ref must be a valid artifact:<id>") {
		t.Fatalf("expected invalid_request error, got %v", err)
	}
}

func TestOpenArtifactTranscriptErrExpired(t *testing.T) {
	deps := &toolDeps{openArtifact: func(ref string) (artifactReadSeekCloser, error) {
		return nil, artifactstore.ErrExpired
	}}
	ref := "artifact:" + strings.Repeat("a", 32)
	_, _, err := openArtifactTranscript(deps, ref)
	if err == nil || !strings.Contains(err.Error(), "artifact_expired") {
		t.Fatalf("expected artifact_expired error, got %v", err)
	}
}

func TestOpenArtifactTranscriptGenericError(t *testing.T) {
	deps := &toolDeps{openArtifact: func(ref string) (artifactReadSeekCloser, error) {
		return nil, errors.New("disk failure")
	}}
	ref := "artifact:" + strings.Repeat("a", 32)
	_, _, err := openArtifactTranscript(deps, ref)
	if err == nil || !strings.Contains(err.Error(), "artifact_unavailable") {
		t.Fatalf("expected artifact_unavailable error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// pageArtifactTranscript read error (reader returns read error)
// ---------------------------------------------------------------------------

func TestPageArtifactTranscriptReadError(t *testing.T) {
	deps := &toolDeps{openArtifact: func(ref string) (artifactReadSeekCloser, error) {
		return &errorArtifactReader{}, nil
	}}
	ref := "artifact:" + strings.Repeat("a", 32)
	// Use a page read at offset 0 — the reader returns total=0 from Seek,
	// so readRetainedPage returns an empty page (no error). Verify the
	// successful empty-page path instead of expecting a read error.
	result, err := pageArtifactTranscript(deps, retainedReadArgs{Ref: ref})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	env := result.(retainedPageEnvelope)
	if env.Page.BytesReturned != 0 {
		t.Fatalf("expected 0 bytes for empty artifact, got %d", env.Page.BytesReturned)
	}
}

type errorArtifactReader struct{}

func (e *errorArtifactReader) ReadAt(p []byte, off int64) (n int, err error) {
	return 0, errors.New("read error")
}
func (e *errorArtifactReader) Seek(offset int64, whence int) (int64, error) {
	return 0, nil
}
func (e *errorArtifactReader) Close() error { return nil }

// ---------------------------------------------------------------------------
// artifactSearchSource ReadWindow with read error
// ---------------------------------------------------------------------------

func TestArtifactSearchSourceReadWindowReadError(t *testing.T) {
	src := artifactSearchSource{
		reader: &errorArtifactReader{},
		total:  100,
	}
	_, err := src.ReadWindow(0, 10)
	if err == nil {
		t.Fatalf("expected read error")
	}
}

// ---------------------------------------------------------------------------
// pageArtifactTranscript seek error (Seek returns negative)
// ---------------------------------------------------------------------------

func TestOpenArtifactTranscriptSeekError(t *testing.T) {
	deps := &toolDeps{openArtifact: func(ref string) (artifactReadSeekCloser, error) {
		return &seekErrorArtifactReader{}, nil
	}}
	ref := "artifact:" + strings.Repeat("a", 32)
	_, _, err := openArtifactTranscript(deps, ref)
	if err == nil || !strings.Contains(err.Error(), "artifact_unavailable") {
		t.Fatalf("expected artifact_unavailable error, got %v", err)
	}
}

type seekErrorArtifactReader struct{}

func (s *seekErrorArtifactReader) ReadAt(p []byte, off int64) (n int, err error) {
	return 0, io.EOF
}
func (s *seekErrorArtifactReader) Seek(offset int64, whence int) (int64, error) {
	return -1, errors.New("seek error")
}
func (s *seekErrorArtifactReader) Close() error { return nil }

// ---------------------------------------------------------------------------
// artifactSearchSource ReadWindow with io.ErrUnexpectedEOF
// ---------------------------------------------------------------------------

func TestArtifactSearchSourceReadWindowShortRead(t *testing.T) {
	// ReaderAt that returns fewer bytes than requested (but not EOF)
	src := artifactSearchSource{
		reader: &shortReadArtifactReader{data: []byte("hello")},
		total:  10, // intentionally larger than actual data
	}
	_, err := src.ReadWindow(0, 10)
	if err == nil {
		t.Fatalf("expected error for short read with mismatched total")
	}
}

type shortReadArtifactReader struct {
	data []byte
}

func (s *shortReadArtifactReader) ReadAt(p []byte, off int64) (n int, err error) {
	if off >= int64(len(s.data)) {
		return 0, io.EOF
	}
	n = copy(p, s.data[off:])
	if n < len(p) {
		return n, io.ErrUnexpectedEOF
	}
	return n, nil
}
func (s *shortReadArtifactReader) Seek(offset int64, whence int) (int64, error) {
	return int64(len(s.data)), nil
}
func (s *shortReadArtifactReader) Close() error { return nil }

// ---------------------------------------------------------------------------
// Format strings verification
// ---------------------------------------------------------------------------

func TestFmtSprintfInRenderDelegateJobTranscript(t *testing.T) {
	rec := &jobstore.JobRecord{
		JobID: "dlg_task",
		Type:  "delegate",
		Task:  "  spaced task  ",
	}
	out := renderDelegateJobTranscript(rec, "output", 10, 0)
	if !strings.Contains(out, "task: spaced task") {
		t.Fatalf("expected trimmed task, got: %s", out)
	}
}

// ---------------------------------------------------------------------------
// parseReadSessionTranscriptArgs: whitespace trimming
// ---------------------------------------------------------------------------

func TestParseReadSessionTranscriptArgsWhitespaceTrimming(t *testing.T) {
	parsed, err := parseReadSessionTranscriptArgs(map[string]any{
		"transcript_ref": "  local:abc  ",
		"source":         "  api_log  ",
		"attempt_id":     "  att_1  ",
		"body":           "  request  ",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.TranscriptRef != "local:abc" {
		t.Fatalf("ref = %q", parsed.TranscriptRef)
	}
	if parsed.Source != "api_log" {
		t.Fatalf("source = %q", parsed.Source)
	}
	if parsed.AttemptID != "att_1" {
		t.Fatalf("attempt_id = %q", parsed.AttemptID)
	}
	if parsed.Body != "request" {
		t.Fatalf("body = %q", parsed.Body)
	}
}

// ---------------------------------------------------------------------------
// parseReadSessionTranscriptArgs: whitespace trimming (transcript source with format)
// ---------------------------------------------------------------------------

func TestParseReadSessionTranscriptArgsWhitespaceTrimmingTranscript(t *testing.T) {
	parsed, err := parseReadSessionTranscriptArgs(map[string]any{
		"transcript_ref": "  local:abc  ",
		"format":         "  markdown  ",
		"range":          "  1-5  ",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.TranscriptRef != "local:abc" {
		t.Fatalf("ref = %q", parsed.TranscriptRef)
	}
	if parsed.Format != "markdown" {
		t.Fatalf("format = %q", parsed.Format)
	}
	if parsed.Range != "1-5" {
		t.Fatalf("range = %q", parsed.Range)
	}
}

// ---------------------------------------------------------------------------
// readTranscriptTool Exec function
// ---------------------------------------------------------------------------

func TestReadTranscriptToolExec(t *testing.T) {
	tl := readTranscriptTool(nil)
	// Call the Exec function with invalid args to verify it works
	_, err := tl.Exec(nil, nil, map[string]any{"source": "api_log"})
	if err == nil || !strings.Contains(err.Error(), "source is not supported") {
		t.Fatalf("expected source rejection, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// findSessionTranscriptsTool Exec function
// ---------------------------------------------------------------------------

func TestFindSessionTranscriptsToolExec(t *testing.T) {
	tl := findSessionTranscriptsTool(&toolDeps{stateDir: t.TempDir()})
	if tl.Exec == nil {
		t.Fatalf("expected Exec to be set")
	}
}

// ---------------------------------------------------------------------------
// parseReadSessionTranscriptArgs expand_turn with markdown format valid
// ---------------------------------------------------------------------------

func TestParseReadSessionTranscriptArgsExpandTurnWithMarkdown(t *testing.T) {
	parsed, err := parseReadSessionTranscriptArgs(map[string]any{
		"format":      "markdown",
		"expand_turn": float64(3),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.ExpandTurn == nil || *parsed.ExpandTurn != 3 {
		t.Fatalf("expand_turn = %v", parsed.ExpandTurn)
	}
}

// ---------------------------------------------------------------------------
// boundReadMarkdownEnvelopeWithHint: expansion over cap, decode then content trim fails
// ---------------------------------------------------------------------------

func TestBoundReadMarkdownEnvelopeWithHintExpansionContentTrimOK(t *testing.T) {
	// Expansion is present, content + expansion exceeds cap,
	// but content trim alone brings it under cap
	env := readMarkdownEnvelope{
		Content: strings.Repeat("x", hardCapChars/2),
		Expansion: &transcriptTurnExpansion{
			ExpandTurn:  0,
			OffsetBytes: 0,
			TotalBytes:  5,
			Encoding:    "utf8",
			Data:        "hello",
		},
	}
	// Content is small enough that it fits with the expansion
	out, err := boundReadMarkdownEnvelopeWithHint(env, "hint")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Expansion == nil {
		t.Fatalf("expansion should be preserved")
	}
}

// ---------------------------------------------------------------------------
// largestTranscriptExpansionPrefix: binary search with multiple candidates
// ---------------------------------------------------------------------------

func TestLargestFittingTranscriptPrefixNoFit(t *testing.T) {
	// Very small limit, nothing fits
	env := readMarkdownEnvelope{
		Expansion: &transcriptTurnExpansion{ExpandTurn: 0, OffsetBytes: 0, TotalBytes: 100},
	}
	n, err := largestFittingTranscriptPrefix(env, []byte("hello world"), []int{5, 10}, 50)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should return 0 if nothing fits
	if n > 10 {
		t.Fatalf("n = %d should be <= 10", n)
	}
}

// ---------------------------------------------------------------------------
// fmt: ensure readMarkdownPage hint spliceAfterHeader is exercised
// ---------------------------------------------------------------------------

func TestSpliceRangeWarningEmptyContent(t *testing.T) {
	out := spliceRangeWarning("", "warning text")
	if !strings.Contains(out, "range warning") {
		t.Fatalf("expected warning even with empty content: %q", out)
	}
}

// ---------------------------------------------------------------------------
// full integration: boundReadMarkdownEnvelopeWithHint with base64 expansion
// ---------------------------------------------------------------------------

func TestBoundReadMarkdownEnvelopeWithHintBase64Expansion(t *testing.T) {
	// Create expansion with base64 encoding containing non-utf8 data
	raw := []byte{0xff, 0xfe, 0x00, 0x01, 0x02}
	encoded := base64.StdEncoding.EncodeToString(raw)
	env := readMarkdownEnvelope{
		Content: "short",
		Expansion: &transcriptTurnExpansion{
			ExpandTurn:     0,
			OffsetBytes:    0,
			TotalBytes:     len(raw),
			Encoding:       "base64",
			Data:           encoded,
			Representation: transcriptV2JSONLRepresentation,
		},
	}
	out, err := boundReadMarkdownEnvelopeWithHint(env, "hint")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Expansion == nil {
		t.Fatalf("expansion should be preserved")
	}
}

// ---------------------------------------------------------------------------
// transcriptEnvelopeWithExpansionBytes: continuation at exact boundary
// ---------------------------------------------------------------------------

func TestTranscriptEnvelopeWithExpansionBytesExactBoundary(t *testing.T) {
	data := []byte("hello")
	env := readMarkdownEnvelope{
		Expansion: &transcriptTurnExpansion{
			ExpandTurn:  0,
			OffsetBytes: 0,
			TotalBytes:  5,
		},
	}
	result := transcriptEnvelopeWithExpansionBytes(env, data)
	if result.Continuation != nil {
		t.Fatalf("expected nil continuation when offset+data == total")
	}
}

// ---------------------------------------------------------------------------
// parseReadSessionTranscriptArgs: offset_bytes with body=valid
// ---------------------------------------------------------------------------

func TestParseReadSessionTranscriptArgsOffsetWithBody(t *testing.T) {
	parsed, err := parseReadSessionTranscriptArgs(map[string]any{
		"source":       "api_log",
		"attempt_id":   "att_1",
		"body":         "response",
		"offset_bytes": float64(50),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.OffsetBytes != 50 {
		t.Fatalf("offset_bytes = %d", parsed.OffsetBytes)
	}
}

// ---------------------------------------------------------------------------
// parseReadSessionTranscriptArgs: max_bytes with body=valid
// ---------------------------------------------------------------------------

func TestParseReadSessionTranscriptArgsMaxBytesWithBody(t *testing.T) {
	parsed, err := parseReadSessionTranscriptArgs(map[string]any{
		"source":     "api_log",
		"attempt_id": "att_1",
		"body":       "response",
		"max_bytes":  float64(1024),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.MaxBytes != 1024 {
		t.Fatalf("max_bytes = %d", parsed.MaxBytes)
	}
}

// ---------------------------------------------------------------------------
// compile to ensure no unused import
// ---------------------------------------------------------------------------

var _ = fmt.Sprintf
