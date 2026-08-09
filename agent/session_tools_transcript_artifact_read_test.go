package agent

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/artifactstore"
)

func artifactTranscriptFixture(t *testing.T, output string) (*toolDeps, string) {
	t.Helper()
	store, err := artifactstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("new artifact store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ref, err := store.Put([]byte(output))
	if err != nil {
		t.Fatalf("put artifact: %v", err)
	}
	s := &Session{artifactStore: store, profile: NewOpenAIProfile("gpt-5.2")}
	return newToolDeps(s), ref
}

func execRead(t *testing.T, deps *toolDeps, args map[string]any) map[string]any {
	t.Helper()
	value, err := execReadTranscript(deps, args)
	if err != nil {
		t.Fatalf("execReadTranscript(%v): %v", args, err)
	}
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal read result: %v", err)
	}
	return decodeReadEnvelope(t, b)
}

func requireExactKeys(t *testing.T, value map[string]any, want ...string) {
	t.Helper()
	got := make(map[string]bool, len(value))
	for key := range value {
		got[key] = true
	}
	wantSet := make(map[string]bool, len(want))
	for _, key := range want {
		wantSet[key] = true
	}
	if !reflect.DeepEqual(got, wantSet) {
		t.Fatalf("keys = %v, want exactly %v", got, wantSet)
	}
}

func requirePage(t *testing.T, envelope map[string]any, offset int64, data string) {
	t.Helper()
	if envelope["representation"] != "raw_bytes" || envelope["content_type"] != "text/plain" {
		t.Fatalf("page representation = %#v", envelope)
	}
	page, ok := envelope["page"].(map[string]any)
	if !ok {
		t.Fatalf("page = %T, want object", envelope["page"])
	}
	requireExactKeys(t, page, "offset_bytes", "bytes_returned", "total_bytes", "encoding", "data")
	if page["offset_bytes"] != float64(offset) || page["bytes_returned"] != float64(len(data)) || page["data"] != data || page["encoding"] != "utf8" {
		t.Fatalf("page = %#v, want offset=%d data=%q", page, offset, data)
	}
}

func requireMatch(t *testing.T, envelope map[string]any, offset int64, before []string, line string, after []string) {
	t.Helper()
	matches, ok := envelope["matches"].([]any)
	if !ok || len(matches) != 1 {
		t.Fatalf("matches = %#v, want one", envelope["matches"])
	}
	match := matches[0].(map[string]any)
	requireExactKeys(t, match, "line_start_byte", "before", "line", "after")
	if match["line_start_byte"] != float64(offset) || match["line"] != line || fmt.Sprint(match["before"]) != fmt.Sprint(stringsToAny(before)) || fmt.Sprint(match["after"]) != fmt.Sprint(stringsToAny(after)) {
		t.Fatalf("match = %#v, want offset=%d before=%v line=%q after=%v", match, offset, before, line, after)
	}
}

func stringsToAny(values []string) []any {
	out := make([]any, len(values))
	for i := range values {
		out[i] = values[i]
	}
	return out
}

func TestReadTranscriptArtifactPageAndSearch(t *testing.T) {
	deps, ref := artifactTranscriptFixture(t, "zero\nneedle\nend\n")
	page := execRead(t, deps, map[string]any{"transcript_ref": ref})
	requireExactKeys(t, page, "transcript_ref", "representation", "content_type", "page", "retained_start_bytes")
	requirePage(t, page, 0, "zero\nneedle\nend\n")
	if page["transcript_ref"] != ref || page["retained_start_bytes"] != float64(0) {
		t.Fatalf("artifact page = %#v", page)
	}
	if _, ok := page["job_status"]; ok {
		t.Fatalf("artifact page carries job_status: %#v", page)
	}

	search := execRead(t, deps, map[string]any{
		"transcript_ref": ref, "output_match": "needle", "context_lines": float64(1),
	})
	requireExactKeys(t, search,
		"transcript_ref", "output_match", "context_lines", "offset_bytes",
		"retained_start_bytes", "total_bytes", "search_complete",
		"skipped_partial_prefix", "matches",
	)
	if search["transcript_ref"] != ref || search["output_match"] != "needle" || search["context_lines"] != float64(1) || search["search_complete"] != true || search["total_bytes"] != float64(len("zero\nneedle\nend\n")) {
		t.Fatalf("artifact search = %#v", search)
	}
	requireMatch(t, search, 5, []string{"zero"}, "needle", []string{"end"})
	if _, ok := search["job_status"]; ok {
		t.Fatalf("artifact search carries job_status: %#v", search)
	}
}

func TestReadTranscriptArtifactPageContinuationAndFixedEncoding(t *testing.T) {
	output := strings.Repeat("a", retainedOutputPageBytes) + "tail"
	deps, ref := artifactTranscriptFixture(t, output)
	first := execRead(t, deps, map[string]any{"transcript_ref": ref})
	requireExactKeys(t, first, "transcript_ref", "representation", "content_type", "page", "retained_start_bytes", "continuation")
	requirePage(t, first, 0, output[:retainedOutputPageBytes])
	continuation := first["continuation"].(map[string]any)
	requireExactKeys(t, continuation, "offset_bytes")
	if continuation["offset_bytes"] != float64(retainedOutputPageBytes) {
		t.Fatalf("continuation = %#v", continuation)
	}
	second := execRead(t, deps, map[string]any{"transcript_ref": ref, "offset_bytes": continuation["offset_bytes"]})
	requirePage(t, second, retainedOutputPageBytes, "tail")
}

func TestReadTranscriptRetainedValidation(t *testing.T) {
	deps, ref := artifactTranscriptFixture(t, "line\n")
	unknownRef := "artifact:" + strings.Repeat("0", 32)
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "context without match", args: map[string]any{"transcript_ref": ref, "context_lines": float64(1)}, want: "invalid_request: context_lines requires output_match"},
		{name: "negative context", args: map[string]any{"transcript_ref": ref, "output_match": "line", "context_lines": float64(-1)}, want: "invalid_request: context_lines must be between 0 and 10"},
		{name: "too much context", args: map[string]any{"transcript_ref": ref, "output_match": "line", "context_lines": float64(11)}, want: "invalid_request: context_lines must be between 0 and 10"},
		{name: "invalid re2", args: map[string]any{"transcript_ref": ref, "output_match": "["}, want: "invalid_request: output_match"},
		{name: "session search", args: map[string]any{"transcript_ref": "current", "output_match": "line"}, want: "invalid_request: output_match applies only to job: and artifact: refs"},
		{name: "artifact format", args: map[string]any{"transcript_ref": ref, "format": "markdown"}, want: "invalid_request: format is not supported for artifact: refs"},
		{name: "artifact range", args: map[string]any{"transcript_ref": ref, "range": "1-2"}, want: "invalid_request: range applies only to session transcript refs"},
		{name: "artifact expansion", args: map[string]any{"transcript_ref": ref, "expand_turn": float64(0)}, want: "invalid_request: expand_turn applies only to session transcript refs"},
		{name: "artifact beyond eof", args: map[string]any{"transcript_ref": ref, "offset_bytes": float64(6)}, want: "invalid_request: offset_bytes 6 is beyond EOF 5; valid byte interval is [0,5]"},
		{name: "malformed artifact", args: map[string]any{"transcript_ref": "artifact:not-valid"}, want: "invalid_request: artifact transcript_ref must be a valid artifact:<id>"},
		{name: "expired artifact", args: map[string]any{"transcript_ref": unknownRef}, want: "artifact_expired:"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := execReadTranscript(deps, tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestReadTranscriptArtifactExpiredWhenNoStoreIsAvailable(t *testing.T) {
	ref := "artifact:" + strings.Repeat("a", 32)
	_, err := execReadTranscript(nil, map[string]any{"transcript_ref": ref})
	if err == nil || !strings.Contains(err.Error(), "artifact_expired:") {
		t.Fatalf("nil dependency error = %v, want artifact_expired", err)
	}
}
