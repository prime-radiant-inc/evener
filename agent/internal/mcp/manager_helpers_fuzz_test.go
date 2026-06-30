package mcp

import (
	"os"
	"sort"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// FuzzMergeEnv drives mergeEnv — the process-environment override merge used when
// launching a stdio MCP server. Input is an arbitrary set of override key/value
// pairs. mergeEnv reads the real os.Environ() but the asserted invariants hold
// regardless of its contents, so no environment stubbing is needed (and the
// fuzzer never launches a server).
//
// Oracles (never bare no-panic):
//   - Every override key appears EXACTLY once in the result, carrying its value.
//   - No result entry keyed by an override key survives from the base
//     environment (overrides replace, never duplicate).
//   - Every result entry is a "key=value" string (the exec.Cmd env contract).
//   - The SET of result entries is deterministic within the process (the slice
//     ORDER is not: env-var order is insignificant and mergeEnv appends the
//     overrides in Go map-iteration order).
//
// Env-var names cannot contain '=', so override keys carrying one are skipped —
// honoring the real key contract rather than feeding mergeEnv invalid input.
func FuzzMergeEnv(f *testing.F) {
	f.Add("PATH", "/custom/bin", "FOO", "bar")
	f.Add("", "", "", "")
	f.Add("A=B", "weird=val", "HOME", "")
	f.Add("dup", "1", "dup", "2") // last write wins in the map

	f.Fuzz(func(t *testing.T, k1, v1, k2, v2 string) {
		extra := map[string]string{}
		if k1 != "" && !strings.Contains(k1, "=") {
			extra[k1] = v1
		}
		if k2 != "" && !strings.Contains(k2, "=") {
			extra[k2] = v2
		}

		got := mergeEnv(extra)

		// Each result entry is key=value; build the multiset of keys.
		keyCounts := map[string]int{}
		for _, e := range got {
			key, val, ok := strings.Cut(e, "=")
			if !ok {
				t.Fatalf("env entry has no '=': %q", e)
			}
			keyCounts[key]++
			if want, isOverride := extra[key]; isOverride && val != want {
				t.Fatalf("override key %q has value %q, want %q", key, val, want)
			}
		}

		for key := range extra {
			if keyCounts[key] != 1 {
				t.Fatalf("override key %q appears %d times, want 1", key, keyCounts[key])
			}
		}

		got2 := mergeEnv(extra)
		if !sameSet(got, got2) {
			t.Fatalf("mergeEnv result set not deterministic")
		}
		_ = os.Environ // documents the real boundary mergeEnv consults
	})
}

// sameSet reports whether a and b contain the same entries, ignoring order.
func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string{}, a...)
	bs := append([]string{}, b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

// FuzzMCPResultToString drives mcpResultToString — the converter from an MCP
// CallToolResult into the string returned as a tool result. Input is a list of
// text parts, a flag injecting an opaque (non-text) content part to exercise the
// JSON-marshal default branch, and the IsError flag.
//
// Oracles (never bare no-panic):
//   - A nil result converts to "".
//   - An IsError result ALWAYS carries the "[MCP Error] " prefix.
//   - A text-only, non-error result equals exactly strings.Join(texts, "\n").
//   - Conversion is deterministic.
func FuzzMCPResultToString(f *testing.F) {
	f.Add("hello", "world", false, false)
	f.Add("only one", "", true, false)
	f.Add("", "", false, true)
	f.Add("line", "img", true, true)

	f.Fuzz(func(t *testing.T, a, b string, withImage, isError bool) {
		if s := mcpResultToString(nil); s != "" {
			t.Fatalf("nil result converted to %q, want empty", s)
		}

		texts := []string{a, b}
		content := []mcpsdk.Content{
			&mcpsdk.TextContent{Text: a},
			&mcpsdk.TextContent{Text: b},
		}
		if withImage {
			content = append(content, &mcpsdk.ImageContent{Data: []byte("xyz"), MIMEType: "image/png"})
		}
		result := &mcpsdk.CallToolResult{Content: content, IsError: isError}

		got := mcpResultToString(result)

		if isError && !strings.HasPrefix(got, "[MCP Error] ") {
			t.Fatalf("IsError result lacks prefix: %q", got)
		}
		if !withImage && !isError {
			if want := strings.Join(texts, "\n"); got != want {
				t.Fatalf("text-only conversion = %q, want %q", got, want)
			}
		}
		if again := mcpResultToString(result); again != got {
			t.Fatalf("mcpResultToString not deterministic")
		}
	})
}
