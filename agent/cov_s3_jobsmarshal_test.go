package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func s3cov_strptr(s string) *string { return &s }

func TestS3Cov_MarshalBoundedDelegateResult(t *testing.T) {
	t.Parallel()

	t.Run("fits without bounding", func(t *testing.T) {
		t.Parallel()
		out := delegateToolResult{JobID: "J1", Type: "delegate", Status: "completed", Output: s3cov_strptr("short")}
		got, err := marshalBoundedDelegateResult(out, 0)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, `"output":"short"`) {
			t.Fatalf("got %s", got)
		}
	})

	t.Run("truncates output tail to fit", func(t *testing.T) {
		t.Parallel()
		big := strings.Repeat("x", 4000)
		out := delegateToolResult{JobID: "J1", Type: "delegate", Status: "completed", Output: s3cov_strptr(big)}
		// A small-but-not-tiny cap forces the binary-search output-limit path to
		// keep a bounded tail rather than dropping the whole field.
		got, err := marshalBoundedDelegateResult(out, 400)
		if err != nil {
			t.Fatal(err)
		}
		var parsed delegateToolResult
		if err := json.Unmarshal([]byte(got), &parsed); err != nil {
			t.Fatalf("result not valid JSON: %v (%s)", err, got)
		}
		if parsed.Output == nil {
			t.Fatal("expected a bounded output, not a dropped one")
		}
		if len(*parsed.Output) >= len(big) {
			t.Fatalf("output not truncated: %d", len(*parsed.Output))
		}
		if len(got) > 400 {
			t.Fatalf("result %d exceeds cap 400", len(got))
		}
	})

	t.Run("drops structured result when it alone busts the cap", func(t *testing.T) {
		t.Parallel()
		bigStruct := map[string]any{"blob": strings.Repeat("z", 4000)}
		out := delegateToolResult{
			JobID:            "J1",
			Type:             "delegate",
			Status:           "completed",
			Output:           s3cov_strptr("small"),
			StructuredResult: bigStruct,
		}
		// The metadata envelope fits ~300 chars, but the structured result does
		// not — forcing the empty-output fallback and then the structured drop.
		got, err := marshalBoundedDelegateResult(out, 300)
		if err != nil {
			t.Fatal(err)
		}
		var parsed delegateToolResult
		if err := json.Unmarshal([]byte(got), &parsed); err != nil {
			t.Fatalf("not valid JSON: %v (%s)", err, got)
		}
		if parsed.StructuredResult != nil {
			t.Fatalf("expected structured result dropped, got %v", parsed.StructuredResult)
		}
		if parsed.StructuredResultReason == "" {
			t.Fatal("expected a structured-result-dropped reason")
		}
		if len(got) > 300 {
			t.Fatalf("result %d exceeds cap 300", len(got))
		}
	})
}

func TestS3Cov_WatchSendArg(t *testing.T) {
	t.Parallel()

	t.Run("absent send is nil", func(t *testing.T) {
		t.Parallel()
		got, err := watchSendArg(map[string]any{})
		if err != nil || got != nil {
			t.Fatalf("got %v, %v", got, err)
		}
	})

	t.Run("send not an object errors", func(t *testing.T) {
		t.Parallel()
		if _, err := watchSendArg(map[string]any{"send": "nope"}); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("empty send is nil", func(t *testing.T) {
		t.Parallel()
		got, err := watchSendArg(map[string]any{"send": map[string]any{}})
		if err != nil || got != nil {
			t.Fatalf("got %v, %v", got, err)
		}
	})

	t.Run("missing to errors", func(t *testing.T) {
		t.Parallel()
		if _, err := watchSendArg(map[string]any{"send": map[string]any{"message": "hi"}}); err == nil {
			t.Fatal("expected error for missing send.to")
		}
	})

	t.Run("valid send", func(t *testing.T) {
		t.Parallel()
		got, err := watchSendArg(map[string]any{"send": map[string]any{
			"to":              "child-1",
			"message":         "ping",
			"include_excerpt": true,
		}})
		if err != nil || got == nil {
			t.Fatalf("got %v, %v", got, err)
		}
		if got.To != "child-1" || got.Message != "ping" || !got.IncludeExcerpt {
			t.Fatalf("parsed wrong: %+v", got)
		}
	})
}

func TestS3Cov_IsEmptyWatchSend(t *testing.T) {
	t.Parallel()
	if !isEmptyWatchSend(map[string]any{}) {
		t.Fatal("empty map should be empty send")
	}
	if isEmptyWatchSend(map[string]any{"to": "x"}) {
		t.Fatal("to set should be non-empty")
	}
}

func TestS3Cov_StringArrayArg(t *testing.T) {
	t.Parallel()
	if got, err := stringArrayArg(map[string]any{}, "k"); err != nil || got != nil {
		t.Fatalf("absent: %v %v", got, err)
	}
	if _, err := stringArrayArg(map[string]any{"k": "notarray"}, "k"); err == nil {
		t.Fatal("expected array error")
	}
	if _, err := stringArrayArg(map[string]any{"k": []any{"a", 1}}, "k"); err == nil {
		t.Fatal("expected string-values error")
	}
	got, err := stringArrayArg(map[string]any{"k": []any{"a", "b"}}, "k")
	if err != nil || len(got) != 2 || got[1] != "b" {
		t.Fatalf("valid: %v %v", got, err)
	}
}
