package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	const first = "STATUS FIRST: boot .\n"
	diagnostic := "FINAL DIAGNOSTIC: " + strings.Repeat("D", 251)
	var log strings.Builder
	log.WriteString(first)
	for i := 1; i < 450; i++ {
		fmt.Fprintf(&log, "status %03d ........\n", i)
	}
	log.WriteString(diagnostic + "\n")
	if got, want := len(diagnostic)+1, 270; got != want {
		t.Fatalf("diagnostic line is %d bytes, want %d", got, want)
	}
	full := log.String()
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
	if !strings.HasPrefix(*out.Output, "STATUS FIRST:") {
		t.Fatalf("digest dropped the first line under the 800-character limit:\n%s", *out.Output)
	}
	if !strings.Contains(*out.Output, diagnostic) {
		t.Fatalf("digest dropped the 270-byte final diagnostic under the 800-character limit:\n%s", *out.Output)
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

// A line limit is a line budget, not a byte budget: short status lines must not
// crowd out a long final diagnostic line when the byte previews shrink.
func TestShellToolDigestLineLimitKeepsLongFinalDiagnostic(t *testing.T) {
	t.Parallel()
	// 20-byte status lines and a 180-byte newline-terminated diagnostic. At a
	// 10-line limit the old byte-halving loop dropped the tail at 128 bytes,
	// which discards a 180-byte line whole.
	diagnostic := "FINAL DIAGNOSTIC: " + strings.Repeat("E", 161)
	var log strings.Builder
	log.WriteString("STATUS FIRST: boot ........\n")
	for i := 1; i < 500; i++ {
		fmt.Fprintf(&log, "status %03d ........\n", i)
	}
	log.WriteString(diagnostic + "\n")
	if got, want := len(diagnostic)+1, 180; got != want {
		t.Fatalf("diagnostic line is %d bytes, want %d", got, want)
	}
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
	args, err := json.Marshal(map[string]string{"command": "cat -- " + shellSingleQuote(path)})
	if err != nil {
		t.Fatalf("marshal shell args: %v", err)
	}
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "digest-long-final",
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
	if !strings.HasPrefix(*out.Output, "STATUS FIRST:") {
		t.Fatalf("digest dropped the first line:\n%s", *out.Output)
	}
	if !strings.Contains(*out.Output, "FINAL DIAGNOSTIC:") {
		t.Fatalf("digest dropped the long final diagnostic line:\n%s", *out.Output)
	}
	if !strings.Contains(*out.Output, "offset_bytes=") {
		t.Fatalf("digest lost its offset_bytes hint under the 10-line limit:\n%s", *out.Output)
	}
	marker := regexp.MustCompile(`omitted \[(\d+),(\d+)\); read more with read_transcript\(offset_bytes=(\d+)\)`).FindStringSubmatch(*out.Output)
	if marker == nil {
		t.Fatalf("digest marker lacks its omitted lifetime range:\n%s", *out.Output)
	}
	if marker[1] != marker[3] {
		t.Fatalf("offset_bytes=%s does not point at the first omitted byte %s:\n%s", marker[3], marker[1], *out.Output)
	}
	if marker[2] <= marker[1] {
		t.Fatalf("omitted range [%s,%s) is empty:\n%s", marker[1], marker[2], *out.Output)
	}
	if res.Output != formatShellResult(out) {
		t.Fatalf("registry changed the rendered shell result:\nwant:\n%s\ngot:\n%s", formatShellResult(out), res.Output)
	}
}

// Under MaxLines: 1, the omission marker and job footer share one line so
// neither the continuation offset nor the exit/job footer is stripped.
func TestShellToolDigestOneLineLimitKeepsMarkerAndFooterTogether(t *testing.T) {
	t.Parallel()
	full := strings.Repeat("line output ...........................\n", 300)
	path := filepath.Join(t.TempDir(), "big.log")
	if err := os.WriteFile(path, []byte(full), 0o600); err != nil {
		t.Fatalf("write output fixture: %v", err)
	}
	cfg := minimalTestSessionConfig(t)
	cfg.ToolOutputLimits = map[string]schema.ToolOutputLimit{
		"shell": {MaxChars: 800, MaxLines: 1, Strategy: schema.TruncHeadTail},
	}
	s := newSession(t, withConfig(cfg))
	args, err := json.Marshal(map[string]string{"command": "cat -- " + shellSingleQuote(path)})
	if err != nil {
		t.Fatalf("marshal shell args: %v", err)
	}
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "digest-lines-1",
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
	if strings.Contains(res.Output, "\n") {
		t.Fatalf("one-line limit rendered multiple lines:\n%s", res.Output)
	}
	for _, want := range []string{"omitted [0,", "offset_bytes=0", "exit 0", "job:" + out.JobID} {
		if !strings.Contains(res.Output, want) {
			t.Fatalf("one-line result missing %q:\n%s", want, res.Output)
		}
	}
}
