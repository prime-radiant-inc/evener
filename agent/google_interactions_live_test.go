package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	_ "primeradiant.com/evener/llm/providers/all"
	"primeradiant.com/evener/llm/providers/google"
	"primeradiant.com/evener/llm/registry"
)

// liveGoogleDefaultModel is the model the live session suite drives unless
// EVENER_LIVE_GOOGLE_MODEL names another one. gemini-3.8-flash is served by
// /interactions on both surfaces.
const liveGoogleDefaultModel = "gemini-3.8-flash"

type liveGoogleTarget struct {
	name     string
	instance string
}

func liveGoogleTargets() []liveGoogleTarget {
	return []liveGoogleTarget{
		{name: "google", instance: "google"},
		{name: "google-vertex-global", instance: "google-vertex"},
	}
}

// skipUnlessLiveGoogle is the first check of every live session test: the
// default offline suite must issue no requests even when a key is exported.
func skipUnlessLiveGoogle(t *testing.T, instance string) {
	t.Helper()
	if os.Getenv("EVENER_LIVE_TESTS") != "1" {
		t.Skip("set EVENER_LIVE_TESTS=1 to run the live Google Interactions session suite")
	}
	switch instance {
	case "google":
		if os.Getenv("GEMINI_API_KEY") == "" && os.Getenv("GOOGLE_API_KEY") == "" {
			t.Skip("set GEMINI_API_KEY or GOOGLE_API_KEY to run the live google surface")
		}
	case "google-vertex":
		if os.Getenv("GOOGLE_VERTEX_PROJECT") == "" {
			t.Skip("set GOOGLE_VERTEX_PROJECT to run the live google-vertex surface")
		}
		if loc := os.Getenv("GOOGLE_VERTEX_LOCATION"); loc != "global" {
			t.Skipf("live google-vertex coverage requires GOOGLE_VERTEX_LOCATION=global, got %q", loc)
		}
	default:
		t.Fatalf("unknown live google instance %q", instance)
	}
}

func liveGoogleModel() string {
	if model := strings.TrimSpace(os.Getenv("EVENER_LIVE_GOOGLE_MODEL")); model != "" {
		return model
	}
	return liveGoogleDefaultModel
}

// liveGoogleRecorder captures the outbound /interactions request bodies a live
// session sends, without changing them.
type liveGoogleRecorder struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (r *liveGoogleRecorder) snapshot() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]byte, len(r.bodies))
	copy(out, r.bodies)
	return out
}

type liveGoogleRecordingTransport struct {
	recorder *liveGoogleRecorder
	base     http.RoundTripper
}

func (t *liveGoogleRecordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodPost && strings.Contains(req.URL.Path, "/interactions") && req.Body != nil {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(raw))
		t.recorder.mu.Lock()
		t.recorder.bodies = append(t.recorder.bodies, append([]byte(nil), raw...))
		t.recorder.mu.Unlock()
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

func installLiveGoogleRecorder(t *testing.T, recorder *liveGoogleRecorder) {
	t.Helper()
	old := google.DefaultProtocol.Client
	google.DefaultProtocol.Client = &http.Client{
		Transport: &liveGoogleRecordingTransport{recorder: recorder, base: http.DefaultTransport},
	}
	t.Cleanup(func() { google.DefaultProtocol.Client = old })
}

// newLiveGoogleSession builds a real session on the developer's live registry.
// The session namer gets its own scripted provider so its background cheap
// call never competes with the live surface.
func newLiveGoogleSession(t *testing.T, instance string) (*Session, *provider.Profile, *liveGoogleRecorder) {
	t.Helper()
	r, err := registry.Load()
	if err != nil {
		t.Fatalf("registry.Load: %v", err)
	}
	client := llm.NewClient(llm.WithRegistry(r))
	profile, err := provider.Resolve(client.Registry(), instance+"/"+liveGoogleModel())
	if err != nil {
		t.Fatalf("resolve %s: %v", instance, err)
	}

	dir := t.TempDir()
	recorder := &liveGoogleRecorder{}
	installLiveGoogleRecorder(t, recorder)

	sess, err := NewSession(client, withTestSessionNamer(client, profile), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: dir,
		testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })

	go func() {
		for range sess.Events() {
		}
	}()

	return sess, profile, recorder
}

func liveGoogleBodyHasKey(body []byte, key string) bool {
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return false
	}
	_, ok := decoded[key]
	return ok
}

func TestLiveGoogleInteractionsSessionTurnEmitsSafetySettings(t *testing.T) {
	if os.Getenv("EVENER_LIVE_TESTS") != "1" {
		t.Skip("set EVENER_LIVE_TESTS=1 to run the live Google Interactions session suite")
	}
	for _, target := range liveGoogleTargets() {
		t.Run(target.name, func(t *testing.T) {
			skipUnlessLiveGoogle(t, target.instance)
			sess, _, recorder := newLiveGoogleSession(t, target.instance)

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			got, err := sess.ProcessInput(ctx, "Reply with the single word pong.", nil)
			if err != nil {
				t.Fatalf("ProcessInput: %v", err)
			}
			if strings.TrimSpace(got) == "" {
				t.Fatal("session turn produced no text")
			}

			bodies := recorder.snapshot()
			if len(bodies) == 0 {
				t.Fatal("session issued no /interactions request")
			}
			hasSafety := liveGoogleBodyHasKey(bodies[0], "safety_settings")
			wantSafety := target.instance == "google"
			if hasSafety != wantSafety {
				t.Fatalf("request safety_settings present = %t, want %t for %s", hasSafety, wantSafety, target.instance)
			}
		})
	}
}

func TestLiveGoogleInteractionsSettlesLegacySignedToolCallHistory(t *testing.T) {
	if os.Getenv("EVENER_LIVE_TESTS") != "1" {
		t.Skip("set EVENER_LIVE_TESTS=1 to run the live Google Interactions session suite")
	}
	for _, target := range liveGoogleTargets() {
		t.Run(target.name, func(t *testing.T) {
			skipUnlessLiveGoogle(t, target.instance)
			sess, _, _ := newLiveGoogleSession(t, target.instance)

			now := time.Now().UTC()
			sess.history = append(sess.history,
				schema.Turn{
					Kind:      schema.TurnUserInput,
					Message:   llm.User("Check the file, then answer."),
					Timestamp: now.Add(-4 * time.Minute),
				},
				schema.Turn{
					Kind: schema.TurnAssistant,
					Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
						{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
							ID:               "call_read",
							Name:             "read_file",
							Arguments:        json.RawMessage(`{"path":"a.txt"}`),
							ThoughtSignature: "legacy_sig",
						}},
						{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
							ID:        "call_old_search",
							Name:      "web_search",
							Arguments: json.RawMessage(`{"query":"legacy"}`),
						}},
					}},
					Timestamp:        now.Add(-3 * time.Minute),
					ResponseProvider: target.instance,
					ResponseModel:    liveGoogleModel(),
				},
				schema.Turn{
					Kind: schema.TurnToolResults,
					Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
						{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
							ToolCallID: "call_old_search",
							Name:       "web_search",
							Content:    "legacy search result",
						}},
						{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
							ToolCallID: "call_read",
							Name:       "read_file",
							Content:    "file body",
						}},
					}},
					Timestamp: now.Add(-2 * time.Minute),
				},
				schema.Turn{
					Kind: schema.TurnAssistant,
					Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
						{Kind: llm.ContentText, Text: "Earlier answer."},
					}},
					Timestamp:        now.Add(-time.Minute),
					ResponseProvider: target.instance,
					ResponseModel:    liveGoogleModel(),
				},
			)

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			got, err := sess.ProcessInput(ctx, "Reply with the single word done.", nil)
			if err != nil {
				t.Fatalf("ProcessInput over legacy-signed history: %v", err)
			}
			if strings.TrimSpace(got) == "" {
				t.Fatal("post-upgrade turn produced no text")
			}
		})
	}
}
