package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/diagnostic"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// These journeys catch lost response warnings, repair leaking into durable input,
// duplicate tool execution, and repair-only 413s bypassing configured fallback.
func TestProviderImageRecovery(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		profile     string
		expanded413 bool
		wantWarning bool
	}{
		{name: "managed", profile: "p3"},
		{name: "unmanaged", profile: "malformed", wantWarning: true},
		{name: "unsupported", profile: "unsupported", wantWarning: true},
		{name: "expanded413", expanded413: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var profileBytes []byte
			switch tc.profile {
			case "p3", "unsupported":
				var err error
				profileBytes, err = os.ReadFile("../llm/internal/imagerepair/testdata/p3.icc")
				if err != nil {
					t.Fatal(err)
				}
				if tc.profile == "unsupported" {
					copy(profileBytes[12:16], "prtr") // Valid ICC class outside the input/display subset.
				}
			case "malformed":
				profileBytes = []byte("synthetic-invalid-icc")
			}
			original, untagged := providerRecoveryJPEG(t, profileBytes)
			originalHash := sha256.Sum256(original)
			input := ImageAttachment{Name: "synthetic.jpg", MediaType: "image/jpeg", Data: original}
			dir := t.TempDir()
			var mu sync.Mutex
			var requests []map[string]any
			toolRequest := 2
			if tc.expanded413 {
				toolRequest = 3
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && r.URL.Path == "/models" {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"object":"list","data":[{"id":"primary","object":"model"},{"id":"fallback","object":"model"}]}`)
					return
				}
				if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
					t.Errorf("unexpected endpoint %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
					return
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				mu.Lock()
				requests = append(requests, body)
				n := len(requests)
				mu.Unlock()
				if n == 1 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprint(w, `{"error":{"message":"Image normalization failed","type":"invalid_request_error"}}`)
					return
				}
				if n == 2 && tc.expanded413 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusRequestEntityTooLarge)
					fmt.Fprint(w, `{"error":{"message":"request too large","type":"invalid_request_error"}}`)
					return
				}
				switch n {
				case toolRequest:
					providerRecoveryToolStream(t, w, body["model"].(string), "effect-call", "image_effect", `{"value":"input-sentinel"}`)
				case toolRequest + 1, toolRequest + 2:
					providerRecoveryToolStream(t, w, body["model"].(string), fmt.Sprintf("done-%d", n), "communicate", `{"message":"output-sentinel","end_turn":true,"output":{"message":"","data":{},"artifacts":[]}}`)
				default:
					t.Errorf("unexpected request number %d", n)
					w.WriteHeader(http.StatusInternalServerError)
				}
			}))
			t.Cleanup(srv.Close)
			client := registryClientAt(t, dir, map[string]registry.Provider{"image-test": {
				Base: "openai-compatible", APIKey: "test",
				Transport: registry.Transport{BaseURL: srv.URL},
			}}, []string{"image-test"})
			profile := resolveClientProfile(t, client, "image-test/primary")
			cfg := SessionConfig{StateDir: dir}
			if tc.expanded413 {
				cfg.ModelFallbacks = []string{"fallback"}
			}
			sess, err := NewSession(client, withTestSessionNamer(client, profile), execenv.NewLocalExecutionEnvironment(dir), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(sess.Close)
			var toolInputs []any
			sess.RegisterTool("image_effect", "Record a synthetic image result", map[string]any{
				"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}, "required": []string{"value"},
			}, func(_ context.Context, value any) (any, error) {
				toolInputs = append(toolInputs, value)
				return map[string]any{"result": "effect-sentinel"}, nil
			})
			collect := drainEvents(sess)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: bounded loopback script, only a hang reaches this deadline.
			defer cancel()
			if _, err := sess.ProcessInput(ctx, "user-sentinel", []ImageAttachment{input}); err != nil {
				t.Fatal(err)
			}
			meta, path := sess.Meta(), sess.TranscriptPath()
			sess.Close()
			evs := collect()
			if len(toolInputs) != 1 || !reflect.DeepEqual(toolInputs[0], map[string]any{"value": "input-sentinel"}) {
				t.Fatalf("tool effects = %#v, want one original input", toolInputs)
			}
			providerRecoveryHistory(t, sess.history, original)
			_, entries, skipped, err := readTranscript(context.Background(), path, "")
			if err != nil || skipped != 0 {
				t.Fatalf("transcript read: skipped=%d err=%v", skipped, err)
			}
			completions, communications := 0, 0
			for _, entry := range entries {
				if entry.Turn.Completion != nil {
					completions++
					if entry.Turn.Completion.Status != schema.TurnCompleted {
						t.Errorf("completion status = %s", entry.Turn.Completion.Status)
					}
				}
				if entry.Turn.Communicate != nil {
					communications++
					if !entry.Turn.Communicate.EndTurn || entry.Turn.Communicate.Message != "output-sentinel" {
						t.Errorf("communicate = %+v", entry.Turn.Communicate)
					}
				}
			}
			if completions != 1 || communications != 1 {
				t.Errorf("durable completions/communications = %d/%d, want 1/1", completions, communications)
			}
			providerRecoveryHistory(t, ResumeHistory(entries), original)
			stored, err := os.ReadFile(expectedAttachmentPath(t, dir, meta.ID, input))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(stored, original) || sha256.Sum256(stored) != originalHash || sha256.Sum256(input.Data) != originalHash {
				t.Fatal("original attachment bytes/hash changed")
			}
			var warnings []events.WarningData
			userEvents := 0
			for _, ev := range evs {
				switch ev.Kind {
				case events.EventError, events.EventContextCompaction:
					t.Errorf("unexpected failure or compaction event: %s", ev.Kind)
				case events.EventUserInput:
					userEvents++
				}
				if w, ok := ev.Data.(events.WarningData); ok && w.Code == llm.WarningCodeImageColorUnmanaged {
					warnings = append(warnings, w)
				}
			}
			if userEvents != 1 {
				t.Errorf("user input events = %d, want 1", userEvents)
			}
			wantWarnings := 0
			if tc.wantWarning {
				wantWarnings = 1
			}
			if len(warnings) != wantWarnings {
				t.Errorf("image_color_unmanaged diagnostics = %d, want %d", len(warnings), wantWarnings)
			}
			for _, w := range warnings {
				if w.Source != string(diagnostic.SourceProvider) {
					t.Errorf("warning source = %q", w.Source)
				}
			}

			restored, err := RestoreSessionFromMetaWithConfig(client, profile, execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{
				StateDir: dir, testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
			})
			if err != nil {
				t.Fatal(err)
			}
			restoredCollect := drainEvents(restored)
			defer func() { restored.Close(); restoredCollect() }()
			providerRecoveryHistory(t, restored.history, original)
			if _, err := restored.ProcessInput(ctx, "restore-sentinel", nil); err != nil {
				t.Fatal(err)
			}

			mu.Lock()
			bodies := append([]map[string]any(nil), requests...)
			mu.Unlock()
			if len(bodies) != toolRequest+2 {
				t.Fatalf("requests = %d, want %d", len(bodies), toolRequest+2)
			}
			for i, body := range bodies {
				wantModel := "primary"
				if tc.expanded413 && i == 2 {
					wantModel = "fallback"
				}
				if body["model"] != wantModel {
					t.Errorf("request %d model = %v, want %s", i+1, body["model"], wantModel)
				}
				mime, data, _, _ := providerRecoveryImage(t, body)
				wantMIME := "image/jpeg"
				if i == 1 && !tc.wantWarning {
					wantMIME = "image/png"
				}
				if mime != wantMIME {
					t.Errorf("request %d MIME = %s, want %s", i+1, mime, wantMIME)
				}
				if i != 1 && !bytes.Equal(data, original) {
					t.Errorf("request %d lost original JPEG", i+1)
				}
			}
			_, repaired, parts, imageIndex := providerRecoveryImage(t, bodies[1])
			if tc.wantWarning {
				if !bytes.Equal(repaired, untagged) {
					t.Error("unmanaged recovery changed bytes outside ICC")
				}
				_, _, originalParts, originalIndex := providerRecoveryImage(t, bodies[0])
				if len(parts) != len(originalParts)+1 || imageIndex != originalIndex+1 || parts[imageIndex-1].(map[string]any)["type"] != "text" {
					t.Fatal("unmanaged image has no adjacent text caveat")
				}
			} else {
				decoded, err := png.Decode(bytes.NewReader(repaired))
				if err != nil {
					t.Fatal(err)
				}
				if decoded.Bounds() != image.Rect(0, 0, 64, 64) {
					t.Errorf("repaired bounds = %v", decoded.Bounds())
				}
				if tc.expanded413 && len(repaired) <= len(original) {
					t.Fatal("fixture did not expand")
				}
			}
			// After removing precisely the permitted image/caveat changes, every
			// request field (including text, tools and options) must be identical.
			providerRecoveryNormalizeImage(t, bodies[1], bodies[0], tc.wantWarning)
			if !reflect.DeepEqual(bodies[0], bodies[1]) {
				t.Error("repair changed non-image request content")
			}
			providerRecoveryToolResult(t, bodies[toolRequest])
			providerRecoveryToolResult(t, bodies[toolRequest+1])
		})
	}
}

func providerRecoveryJPEG(t *testing.T, profile []byte) (original, untagged []byte) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	var seed uint32 = 19
	for i := range img.Pix {
		seed = seed*1664525 + 1013904223
		img.Pix[i] = byte(seed >> 24)
		if i%4 == 3 {
			img.Pix[i] = 255
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	untagged = bytes.Clone(buf.Bytes())
	original = bytes.Clone(untagged)
	if profile != nil {
		payload := append([]byte("ICC_PROFILE\x00\x01\x01"), profile...)
		segment := []byte{0xff, 0xe2, 0, 0}
		binary.BigEndian.PutUint16(segment[2:], uint16(len(payload)+2))
		original = append(append(append(bytes.Clone(untagged[:2]), segment...), payload...), untagged[2:]...)
	}
	return original, untagged
}

func providerRecoveryToolStream(t *testing.T, w http.ResponseWriter, model, id, name, args string) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	payload := map[string]any{"id": "completion-sentinel", "object": "chat.completion.chunk", "model": model, "choices": []any{
		map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "tool_calls": []any{
			map[string]any{"index": 0, "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": args}},
		}}, "finish_reason": "tool_calls"},
	}}
	fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", mustJSON(t, payload))
}

func providerRecoveryImage(t *testing.T, body map[string]any) (string, []byte, []any, int) {
	t.Helper()
	var found []any
	index := -1
	for _, raw := range body["messages"].([]any) {
		msg := raw.(map[string]any)
		parts, _ := msg["content"].([]any)
		for i, rawPart := range parts {
			part := rawPart.(map[string]any)
			if part["type"] == "image_url" {
				if index >= 0 {
					t.Fatal("duplicate image in request")
				}
				found, index = parts, i
			}
		}
	}
	if index < 0 {
		t.Fatal("request lost image")
	}
	uri := found[index].(map[string]any)["image_url"].(map[string]any)["url"].(string)
	header, encoded, ok := strings.Cut(uri, ";base64,")
	if !ok {
		t.Fatal("image is not inline base64")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimPrefix(header, "data:"), data, found, index
}

func providerRecoveryNormalizeImage(t *testing.T, repaired, original map[string]any, caveat bool) {
	t.Helper()
	_, _, sourceParts, sourceIndex := providerRecoveryImage(t, original)
	for _, raw := range repaired["messages"].([]any) {
		msg := raw.(map[string]any)
		parts, _ := msg["content"].([]any)
		for i, rawPart := range parts {
			if rawPart.(map[string]any)["type"] != "image_url" {
				continue
			}
			sourceURL := sourceParts[sourceIndex].(map[string]any)["image_url"].(map[string]any)["url"]
			rawPart.(map[string]any)["image_url"].(map[string]any)["url"] = sourceURL
			if caveat {
				msg["content"] = append(parts[:i-1:i-1], parts[i:]...)
			}
			return
		}
	}
}

func providerRecoveryHistory(t *testing.T, history []schema.Turn, original []byte) {
	t.Helper()
	users, images, effects := 0, 0, 0
	for _, turn := range history {
		switch turn.Kind {
		case schema.TurnUserInput:
			users++
			if len(turn.Message.Content) == 0 || turn.Message.Content[0].Kind != llm.ContentText || turn.Message.Content[0].Text != "user-sentinel" {
				t.Error("original user text changed")
			}
		case schema.TurnFailure, schema.TurnCheckpoint, schema.TurnSummary:
			t.Errorf("unexpected history kind %s", turn.Kind)
		}
		for _, part := range turn.Message.Content {
			if part.Kind == llm.ContentImage {
				images++
				if part.Image.MediaType != "image/jpeg" || !bytes.Equal(part.Image.Data, original) {
					t.Error("history image changed")
				}
			}
			if part.ToolResult != nil && part.ToolResult.ToolCallID == "effect-call" {
				effects++
				if part.ToolResult.IsError {
					t.Error("tool result failed")
				}
			}
		}
	}
	if users != 1 || images != 1 || effects != 1 {
		t.Errorf("history users/images/effects = %d/%d/%d, want 1/1/1", users, images, effects)
	}
	call, ok := findToolCallInHistory(history, "effect-call")
	if !ok || call.Name != "image_effect" {
		t.Fatal("history lost original tool call")
	}
	var arguments map[string]any
	if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(arguments, map[string]any{"value": "input-sentinel"}) {
		t.Errorf("history tool arguments = %#v", arguments)
	}
}

func providerRecoveryToolResult(t *testing.T, body map[string]any) {
	t.Helper()
	count := 0
	for _, raw := range body["messages"].([]any) {
		msg := raw.(map[string]any)
		if msg["role"] != "tool" || msg["tool_call_id"] != "effect-call" {
			continue
		}
		count++
		var result map[string]any
		content, _ := msg["content"].(string)
		if err := json.Unmarshal([]byte(content), &result); err != nil {
			t.Fatal(err)
		}
		if result["result"] != "effect-sentinel" {
			t.Errorf("tool result = %#v", result)
		}
	}
	if count != 1 {
		t.Errorf("tool results on wire = %d, want 1", count)
	}
}
