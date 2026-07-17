package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/llm/apilog"
)

func canonicalAPIAttemptLine(t *testing.T, provider string, body []byte) string {
	return canonicalAPIAttemptLineFor(t, provider, "", apilog.AttemptSuccess, body)
}

func canonicalAPIAttemptLineFor(t *testing.T, provider, endpointFamily string, outcome apilog.AttemptOutcomeClass, body []byte) string {
	t.Helper()
	statusCode := 200
	if outcome == apilog.AttemptProviderReject {
		statusCode = 400
	}
	record := apilog.APIAttemptRecord{
		Kind:             "api_attempt",
		SchemaVersion:    1,
		AttemptID:        identifier.MustNewAPIAttemptID(),
		AttemptGroupID:   "ag_harvest",
		AttemptIndex:     1,
		Timestamp:        time.Unix(1, 0).UTC(),
		ProviderInstance: provider,
		RequestModel:     "test-model",
		Request: apilog.APIAttemptRequest{
			Method:         "POST",
			Endpoint:       "https://provider.test/v1/stream",
			EndpointFamily: endpointFamily,
			Body:           apilog.EncodeBody([]byte("{}")),
		},
		Response: &apilog.APIAttemptResponse{
			StatusCode: statusCode,
			Body:       apilog.EncodeBody(body),
		},
		Outcome: outcome,
	}
	line, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return string(line)
}

func TestHarvestProviderAliasesKeepDecoderSeeds(t *testing.T) {
	tests := []struct {
		name           string
		endpointFamily string
		body           string
		wantDir        string
	}{
		{
			name:           "Anthropic Messages",
			endpointFamily: "anthropic_messages",
			body:           "event: message_start\n" + `data: {"type":"message_start","message":{}}` + "\n\n",
			wantDir:        dirAnthropicStream,
		},
		{
			name:           "Google streamGenerateContent",
			endpointFamily: "google_stream_generate_content",
			body:           `data: {"candidates":[]}` + "\n\n",
			wantDir:        dirGeminiStream,
		},
		{
			name:           "OpenAI public Responses",
			endpointFamily: "openai_public",
			body:           `data: {"type":"response.completed","response":{}}` + "\n\n",
			wantDir:        dirOpenAIResponses,
		},
		{
			name:           "OpenAI Codex Responses",
			endpointFamily: "openai_codex",
			body:           `data: {"type":"response.completed","response":{}}` + "\n\n",
			wantDir:        dirOpenAIResponses,
		},
		{
			name:           "OpenAI Chat Completions",
			endpointFamily: "openai_chat_completions",
			body:           `data: {"object":"chat.completion.chunk","choices":[]}` + "\n\n",
			wantDir:        dirOpenAIChatComplete,
		},
		{
			name:           "OpenAI-compatible Chat Completions",
			endpointFamily: "openai_compatible_chat_completions",
			body:           `data: {"object":"chat.completion.chunk","choices":[]}` + "\n\n",
			wantDir:        dirOpenAICompatStream,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertHarvestedProviderDirs(t, tt.endpointFamily, []byte(tt.body), tt.wantDir)
		})
	}
}

func TestHarvestProviderAliasesRouteByBodyShapeWithoutEndpointFamily(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantDirs []string
	}{
		{
			name:     "Anthropic is unambiguous",
			body:     "event: message_start\n" + `data: {"type":"message_start","message":{}}` + "\n\n",
			wantDirs: []string{dirAnthropicStream},
		},
		{
			name:     "Google is unambiguous",
			body:     `data: {"candidates":[]}` + "\n\n",
			wantDirs: []string{dirGeminiStream},
		},
		{
			name:     "Responses is unambiguous",
			body:     `data: {"type":"response.completed","response":{}}` + "\n\n",
			wantDirs: []string{dirOpenAIResponses},
		},
		{
			name:     "Chat Completions fans out to both compatible decoders",
			body:     `data: {"object":"chat.completion.chunk","choices":[]}` + "\n\n",
			wantDirs: []string{dirOpenAIChatComplete, dirOpenAICompatStream},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertHarvestedProviderDirs(t, "", []byte(tt.body), tt.wantDirs...)
		})
	}
}

func assertHarvestedProviderDirs(t *testing.T, endpointFamily string, body []byte, wantDirs ...string) {
	t.Helper()
	out := t.TempDir()
	apiLog := filepath.Join(t.TempDir(), "attempt.api.jsonl")
	writeFile(t, apiLog, canonicalAPIAttemptLineFor(t, "configured-alias", endpointFamily, apilog.AttemptSuccess, body)+"\n")

	r := newRunner(out, NewEmitter(false, 32<<10), nil)
	harvestSSE(r, &Sanitizer{}, []string{apiLog})
	if got := countFiles(t, filepath.Join(out, dirParseSSE)); got != 1 {
		t.Fatalf("generic corpus seeds = %d, want 1", got)
	}

	want := make(map[string]bool, len(wantDirs))
	for _, dir := range wantDirs {
		want[dir] = true
	}
	for _, dir := range []string{
		dirOpenAIResponses,
		dirOpenAIChatComplete,
		dirAnthropicStream,
		dirGeminiStream,
		dirOpenAICompatStream,
	} {
		wantCount := 0
		if want[dir] {
			wantCount = 1
		}
		if got := countFiles(t, filepath.Join(out, dir)); got != wantCount {
			t.Fatalf("%s seeds = %d, want %d", dir, got, wantCount)
		}
	}
}

func TestHarvestNonSSEProviderErrors(t *testing.T) {
	errorBody := []byte(`{"error":{"type":"invalid_request_error","message":"invalid data: credential ` + plantedHarvestSecret + `"}}`)
	out := t.TempDir()
	apiLog := filepath.Join(t.TempDir(), "errors.api.jsonl")
	writeFile(t, apiLog, canonicalAPIAttemptLineFor(t, "configured-alias", "anthropic_messages", apilog.AttemptProviderReject, errorBody)+"\n")

	r := newRunner(out, NewEmitter(false, 32<<10), nil)
	harvestSSE(r, &Sanitizer{}, []string{apiLog})

	generic := readOnlyBytesSeed(t, filepath.Join(out, dirParseSSE))
	provider := readOnlyBytesSeed(t, filepath.Join(out, dirAnthropicStream))
	if !bytes.Equal(generic, provider) {
		t.Fatalf("generic seed %q differs from provider seed %q", generic, provider)
	}
	if bytes.Contains(generic, []byte(plantedHarvestSecret)) {
		t.Fatal("provider error seed retained a credential")
	}
	want, err := scrubJSON(errorBody)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(generic, want) {
		t.Fatalf("provider error seed = %q, want scrubbed JSON %q", generic, want)
	}

	oversizedOut := t.TempDir()
	oversized := newRunner(oversizedOut, NewEmitter(false, len(want)-1), nil)
	harvestSSE(oversized, &Sanitizer{}, []string{apiLog})
	if got := countFiles(t, filepath.Join(oversizedOut, dirParseSSE)); got != 0 {
		t.Fatalf("oversized generic seeds = %d, want 0", got)
	}
	if got := countFiles(t, filepath.Join(oversizedOut, dirAnthropicStream)); got != 0 {
		t.Fatalf("oversized provider seeds = %d, want 0", got)
	}
}

func readOnlyBytesSeed(t *testing.T, dir string) []byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("%s contains %d seeds, want 1", dir, len(entries))
	}
	raw, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 || lines[0] != "go test fuzz v1" {
		t.Fatalf("invalid bytes seed %q", raw)
	}
	literal := strings.TrimSuffix(strings.TrimPrefix(lines[1], "[]byte("), ")")
	decoded, err := strconv.Unquote(literal)
	if err != nil {
		t.Fatalf("decode bytes seed: %v", err)
	}
	return []byte(decoded)
}

func scenarioSplitSSEEvents(t *testing.T) {
	body := []byte("data: a\n\ndata: b\n\ndata: c\n\n")
	events := splitSSEEvents(body)
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3: %q", len(events), events)
	}
	// Reassembly is lossless.
	if got := bytes.Join(events, nil); !bytes.Equal(got, body) {
		t.Errorf("reassembled = %q, want %q", got, body)
	}
	// An unterminated trailing event is kept.
	tail := splitSSEEvents([]byte("data: a\n\ndata: tail"))
	if len(tail) != 2 || string(tail[1]) != "data: tail" {
		t.Errorf("trailing event = %q, want the unterminated remainder", tail)
	}
}

func scenarioSSESeedWindows(t *testing.T) {
	// A stream within the cap is a single window, unchanged.
	small := []byte("data: a\n\ndata: b\n\n")
	if w := sseSeedWindows(small, 1000); len(w) != 1 || !bytes.Equal(w[0], small) {
		t.Fatalf("small stream: got %d windows, want 1 unchanged", len(w))
	}

	// A large stream is packed into multiple windows of whole events, each within
	// the cap, and concatenating the windows reproduces the original.
	var big bytes.Buffer
	for i := 0; i < 50; i++ {
		big.WriteString("data: ")
		big.Write(bytes.Repeat([]byte("x"), 40))
		big.WriteString("\n\n")
	}
	windows := sseSeedWindows(big.Bytes(), 200)
	if len(windows) < 2 {
		t.Fatalf("expected the big stream to split into >=2 windows, got %d", len(windows))
	}
	var reassembled []byte
	for _, w := range windows {
		if len(w) > 200 {
			t.Errorf("window of %d bytes exceeds the 200 cap", len(w))
		}
		// Each window ends on the event boundary.
		if !bytes.HasSuffix(w, []byte("\n\n")) {
			t.Errorf("window does not end on an event boundary: %q", w)
		}
		reassembled = append(reassembled, w...)
	}
	if !bytes.Equal(reassembled, big.Bytes()) {
		t.Error("windows do not reassemble to the original stream")
	}
}

func scenarioSSESeedWindows_SkipsOversizedEvent(t *testing.T) {
	// A single event larger than the cap is skipped (can't fit any window), while
	// the surrounding fitting events are still emitted.
	body := []byte("data: ok1\n\ndata: " + string(bytes.Repeat([]byte("x"), 500)) + "\n\ndata: ok2\n\n")
	windows := sseSeedWindows(body, 100)
	joined := string(bytes.Join(windows, nil))
	if !bytes.Contains([]byte(joined), []byte("ok1")) || !bytes.Contains([]byte(joined), []byte("ok2")) {
		t.Errorf("fitting events dropped: %q", joined)
	}
	if bytes.Contains([]byte(joined), bytes.Repeat([]byte("x"), 500)) {
		t.Error("oversized event was not skipped")
	}
}
