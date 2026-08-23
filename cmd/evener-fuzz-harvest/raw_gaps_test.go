package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/llm/apilog"
)

// TestHarvestSSEOpenAIProviderFallback covers the "openai" provider case in
// providerTargetDirsForAttempt's fallback switch (line 153): when the endpoint
// family is unknown and the body doesn't match any shape, the provider name
// "openai" routes to both OpenAI responses and chat completions dirs.
func TestHarvestSSEOpenAIProviderFallback(t *testing.T) {
	d := t.TempDir()
	out := t.TempDir()
	r := newRunner(out, NewEmitter(false, 32768), nil)
	san := &Sanitizer{}
	api := filepath.Join(d, "api.jsonl")
	// Use an SSE body with unknown endpoint family and provider "openai".
	// bodyShapeTargetDirs won't match because "data: {}" is SSE (handled by
	// the SSE path), but providerTargetDirsForAttempt is called before the
	// SSE check — wait, no: providerTargetDirsForAttempt is called inside the
	// SSE loop to get dirs. The body must look like SSE to reach the loop that
	// calls providerTargetDirsForAttempt.
	entry := canonicalAPIAttemptLineFor(t, "openai", "", apilog.AttemptSuccess, []byte("data: {}\n\n"))
	mustHarvestWrite(t, api, entry+"\n")
	harvestSSE(r, san, []string{api})
	// The SSE stat should have scanned at least one body.
	if r.stat("sse").scanned == 0 {
		t.Fatalf("expected at least one SSE scan, got stats=%v", r.stats)
	}
}

// TestHarvestSSEMissingFile covers the os.Open error path (line 29) where
// the API log file does not exist.
func TestHarvestSSEMissingFile(t *testing.T) {
	out := t.TempDir()
	r := newRunner(out, NewEmitter(false, 32768), nil)
	san := &Sanitizer{}
	// Pass a non-existent path — harvestSSE should skip it without panicking.
	harvestSSE(r, san, []string{filepath.Join(t.TempDir(), "missing.jsonl")})
	// No crash is the assertion.
}

// TestHarvestSSEBadJSON covers the decoder error path (lines 34-35) where
// the API log contains invalid JSON.
func TestHarvestSSEBadJSON(t *testing.T) {
	d := t.TempDir()
	out := t.TempDir()
	r := newRunner(out, NewEmitter(false, 32768), nil)
	san := &Sanitizer{}
	api := filepath.Join(d, "api.jsonl")
	mustHarvestWrite(t, api, "not valid json\n")
	harvestSSE(r, san, []string{api})
	// The decoder should break on the bad line without panicking.
}

// TestHarvestSSEEmptyBody covers the decode-body error / empty-body path
// (lines 42-43).
func TestHarvestSSEEmptyBody(t *testing.T) {
	d := t.TempDir()
	out := t.TempDir()
	r := newRunner(out, NewEmitter(false, 32768), nil)
	san := &Sanitizer{}
	api := filepath.Join(d, "api.jsonl")
	// A valid attempt record with an empty response body.
	entry := canonicalAPIAttemptLineFor(t, "test", "", apilog.AttemptSuccess, []byte(""))
	mustHarvestWrite(t, api, entry+"\n")
	harvestSSE(r, san, []string{api})
}

// silence unused imports in case of build differences
var _ = os.Stat
var _ = strings.Contains
