package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// A tight character limit must leave the digest's omission marker and
// offset_bytes hint inside the registry's budget. A fixed-size digest lets the
// registry keep its own head and tail and cut the marker out of the middle.
func TestShellToolDigestFitsAConstrainedCharLimit(t *testing.T) {
	t.Parallel()
	full := strings.Repeat("status line ..............................\n", 300)
	path := filepath.Join(t.TempDir(), "big.log")
	if err := os.WriteFile(path, []byte(full), 0o600); err != nil {
		t.Fatalf("write output fixture: %v", err)
	}
	cfg := minimalTestSessionConfig(t)
	cfg.ToolOutputLimits = map[string]schema.ToolOutputLimit{
		"shell": {MaxChars: 800, MaxLines: 512, Strategy: schema.TruncHeadTail},
	}
	s := newSession(t, withConfig(cfg))
	args, err := json.Marshal(map[string]string{"command": "cat -- " + shellSingleQuote(path)})
	if err != nil {
		t.Fatalf("marshal shell args: %v", err)
	}
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "digest-chars-800",
		Name:      "shell",
		Arguments: args,
	})
	if res.IsError {
		t.Fatalf("shell returned error: %s", res.Output)
	}
	var out shellToolResult
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal shell result: %v (output: %s)", err, res.Output)
	}
	if out.Output == nil {
		t.Fatal("large output result carries no inline digest")
	}
	if got := len([]rune(res.Output)); got > 800 {
		t.Fatalf("rendered result has %d characters, want at most 800", got)
	}
	if !strings.Contains(*out.Output, "offset_bytes=") {
		t.Fatalf("digest lost its offset_bytes hint under the 800-character limit:\n%s", *out.Output)
	}
	// The registry must not have re-truncated the digest.
	if res.Output != formatShellResult(out) {
		t.Fatalf("registry changed the rendered shell result:\nwant:\n%s\ngot:\n%s", formatShellResult(out), res.Output)
	}
}

// A tight line limit must do the same: shrink the previews until the marker and
// footer fit, rather than let the registry cut the marker away.
func TestShellToolDigestFitsAConstrainedLineLimit(t *testing.T) {
	t.Parallel()
	// A build log of 43-byte lines: the 512-byte head alone is 11 lines, so a
	// one-sided shrink would satisfy the 10-line budget by dropping the tail and
	// its final diagnostic. Both ends must survive.
	const first = "BUILD FIRST: compiling package alpha .......\n"
	const last = "BUILD LAST: linking binary omega ..........\n"
	var log strings.Builder
	log.WriteString(first)
	for i := 1; i < 299; i++ {
		fmt.Fprintf(&log, "build step %03d: compiling package ..... ok\n", i)
	}
	log.WriteString(last)
	full := log.String()
	path := filepath.Join(t.TempDir(), "build.log")
	if err := os.WriteFile(path, []byte(full), 0o600); err != nil {
		t.Fatalf("write output fixture: %v", err)
	}
	cfg := minimalTestSessionConfig(t)
	cfg.ToolOutputLimits = map[string]schema.ToolOutputLimit{
		"shell": {MaxChars: 100_000, MaxLines: 10, Strategy: schema.TruncHeadTail},
	}
	s := newSession(t, withConfig(cfg))
	if got := s.reg.Get("shell").Limit.MaxLines; got != 10 {
		t.Fatalf("shell line limit = %d, want 10", got)
	}
	args, err := json.Marshal(map[string]string{"command": "cat -- " + shellSingleQuote(path)})
	if err != nil {
		t.Fatalf("marshal shell args: %v", err)
	}
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "digest-lines-10",
		Name:      "shell",
		Arguments: args,
	})
	if res.IsError {
		t.Fatalf("shell returned error: %s", res.Output)
	}
	var out shellToolResult
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal shell result: %v (output: %s)", err, res.Output)
	}
	if out.Output == nil {
		t.Fatal("large output result carries no inline digest")
	}
	if got := strings.Count(res.Output, "\n") + 1; got > 10 {
		t.Fatalf("rendered result has %d lines, want at most 10", got)
	}
	if !strings.HasPrefix(*out.Output, "BUILD FIRST:") {
		t.Fatalf("digest dropped the first line:\n%s", *out.Output)
	}
	if !strings.Contains(*out.Output, "BUILD LAST:") {
		t.Fatalf("digest dropped the last diagnostic line:\n%s", *out.Output)
	}
	if !strings.Contains(*out.Output, "offset_bytes=") {
		t.Fatalf("digest lost its offset_bytes hint under the 10-line limit:\n%s", *out.Output)
	}
	// The registry must not have re-truncated the digest.
	if res.Output != formatShellResult(out) {
		t.Fatalf("registry changed the rendered shell result:\nwant:\n%s\ngot:\n%s", formatShellResult(out), res.Output)
	}
}
