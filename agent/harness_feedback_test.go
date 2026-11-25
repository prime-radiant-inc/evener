package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestHarnessFeedback_OmittedWhenUnconfigured(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		cfg  HarnessFeedbackConfig
	}{
		{"zero value", HarnessFeedbackConfig{}},
		{"whitespace only", HarnessFeedbackConfig{WebhookURL: "  \t ", Command: " \n ", FilePath: "   "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var capturedReq llm.Request
			s := newSession(t, withConfig(SessionConfig{
				StateDir:        t.TempDir(),
				HarnessFeedback: tc.cfg,
			}), withSteps(func(req llm.Request) llm.Response {
				capturedReq = req
				return agenttest.FinalResponse("done")
			}))

			if s.reg.Get("harness_feedback") != nil {
				t.Fatal("harness_feedback must not be registered when unconfigured")
			}
			if hasToolDef(s.ToolDefinitions(), "harness_feedback") {
				t.Fatal("harness_feedback must not be advertised in ToolDefinitions when unconfigured")
			}
			if _, err := s.ProcessInput(context.Background(), "hello", nil); err != nil {
				t.Fatalf("ProcessInput: %v", err)
			}
			if hasToolDef(capturedReq.Tools, "harness_feedback") {
				t.Fatal("harness_feedback must not be sent in provider request tools when unconfigured")
			}
			for _, msg := range capturedReq.Messages {
				if strings.Contains(msg.Text(), "harness_feedback") {
					t.Fatal("system/user prompt must not mention harness_feedback when unconfigured")
				}
			}
		})
	}
}

func TestHarnessFeedback_WebhookSinkAndEnrichment(t *testing.T) {
	t.Parallel()
	const secretToken = "T0000/B0000/SECRET_WEBHOOK_TOKEN_8912"

	var (
		mu          sync.Mutex
		gotMethod   string
		gotCT       string
		gotBody     []byte
		gotRequests int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotMethod = r.Method
		gotCT = r.Header.Get("Content-Type")
		gotBody = body
		gotRequests++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	fixedTime := time.Date(2026, 11, 25, 14, 30, 0, 0, time.UTC)
	workspace := filepath.Join(t.TempDir(), "my-project-dir")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	webhookURL := srv.URL + "/services/" + secretToken

	var toolResultMsg string
	var toolResultErr bool
	s := newSession(t, withDir(workspace), withConfig(SessionConfig{
		StateDir:  t.TempDir(),
		AgentName: "reviewer",
		clock:     agenttest.NewFakeClockAt(fixedTime),
		HarnessFeedback: HarnessFeedbackConfig{
			WebhookURL: webhookURL,
		},
	}), withSteps(
		func(req llm.Request) llm.Response {
			if !hasToolDef(req.Tools, "harness_feedback") {
				t.Error("expected harness_feedback in advertised provider request tools")
			}
			return agenttest.ToolCallResponse(llm.ToolCallData{
				ID:   "call-fb-1",
				Name: "harness_feedback",
				Arguments: json.RawMessage(`{
					"summary": "grep_search truncated without line count",
					"details": "Expected total match count when output caps; worked around with rg -c.",
					"category": "tool_error",
					"tool_name": "grep_search",
					"intent": "Reporting grep truncation rough edge"
				}`),
			})
		},
		func(req llm.Request) llm.Response {
			for _, m := range req.Messages {
				for _, p := range m.Content {
					if p.Kind == llm.ContentToolResult && p.ToolResult != nil && p.ToolResult.ToolCallID == "call-fb-1" {
						toolResultMsg = fmt.Sprint(p.ToolResult.Content)
						toolResultErr = p.ToolResult.IsError
					}
				}
			}
			return agenttest.FinalResponse("reported")
		},
	))

	rt := s.reg.Get("harness_feedback")
	if rt == nil {
		t.Fatal("harness_feedback not registered")
	}
	if rt.ReadOnly {
		t.Fatal("harness_feedback must have ReadOnly=false")
	}

	if _, err := s.ProcessInput(context.Background(), "report bump", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if toolResultErr {
		t.Fatalf("tool call returned unexpected error: %s", toolResultMsg)
	}
	if strings.Contains(toolResultMsg, secretToken) || strings.Contains(toolResultMsg, srv.URL) {
		t.Fatalf("tool result leaked webhook URL/secret: %s", toolResultMsg)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotRequests != 1 {
		t.Fatalf("webhook requests = %d, want 1", gotRequests)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", gotMethod)
	}
	if !strings.HasPrefix(gotCT, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", gotCT)
	}

	var payload map[string]any
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("unmarshal webhook body: %v\nbody: %s", err, gotBody)
	}
	text, _ := payload["text"].(string)
	if !strings.Contains(text, "grep_search truncated without line count") || !strings.Contains(text, "worked around with rg -c") {
		t.Errorf("Slack text field missing summary/details: %q", text)
	}
	if got, _ := payload["timestamp"].(string); got != fixedTime.Format(time.RFC3339) {
		t.Errorf("timestamp = %q, want %q", got, fixedTime.Format(time.RFC3339))
	}
	if got, _ := payload["session_id"].(string); got != s.ID() {
		t.Errorf("session_id = %q, want %q", got, s.ID())
	}
	if got, _ := payload["model"].(string); got == "" {
		t.Error("model must be populated")
	}
	if got, _ := payload["provider"].(string); got == "" {
		t.Error("provider must be populated")
	}
	if got, _ := payload["agent_name"].(string); got != "reviewer" {
		t.Errorf("agent_name = %q, want %q", got, "reviewer")
	}
	if got, _ := payload["working_dir"].(string); got != "my-project-dir" {
		t.Errorf("working_dir = %q, want %q", got, "my-project-dir")
	}
	if got, _ := payload["summary"].(string); got != "grep_search truncated without line count" {
		t.Errorf("summary = %q", got)
	}
	if got, _ := payload["category"].(string); got != "tool_error" {
		t.Errorf("category = %q, want tool_error", got)
	}
	if got, _ := payload["tool_name"].(string); got != "grep_search" {
		t.Errorf("tool_name = %q, want grep_search", got)
	}
}

func TestHarnessFeedback_FileSinkAppendsJSONL(t *testing.T) {
	t.Parallel()
	outPath := filepath.Join(t.TempDir(), "nested", "dir", "feedback.jsonl")
	s := newSession(t, withConfig(SessionConfig{
		StateDir: t.TempDir(),
		HarnessFeedback: HarnessFeedbackConfig{
			FilePath: outPath,
		},
	}))

	for i, summary := range []string{"first friction", "second friction"} {
		res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
			ID:        "fb-" + string(rune('1'+i)),
			Name:      "harness_feedback",
			Arguments: json.RawMessage(`{"summary":"` + summary + `","details":"detail line","category":"schema_gap"}`),
		})
		if res.IsError {
			t.Fatalf("call %d failed: %s", i, res.Output)
		}
	}

	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read JSONL file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("JSONL line count = %d, want 2\n%s", len(lines), raw)
	}
	for i, wantSummary := range []string{"first friction", "second friction"} {
		var row map[string]any
		if err := json.Unmarshal([]byte(lines[i]), &row); err != nil {
			t.Fatalf("line %d invalid JSON: %v", i, err)
		}
		if row["summary"] != wantSummary {
			t.Errorf("line %d summary = %v, want %q", i, row["summary"], wantSummary)
		}
		if row["session_id"] != s.ID() {
			t.Errorf("line %d session_id = %v, want %q", i, row["session_id"], s.ID())
		}
		if ts, _ := row["timestamp"].(string); ts == "" {
			t.Errorf("line %d missing timestamp", i)
		}
	}
}

func TestHarnessFeedback_CommandSinkReceivesJSONOnStdin(t *testing.T) {
	t.Parallel()
	outFile := filepath.Join(t.TempDir(), "captured.json")
	s := newSession(t, withConfig(SessionConfig{
		StateDir: t.TempDir(),
		HarnessFeedback: HarnessFeedbackConfig{
			Command: `cat > "` + outFile + `"`,
		},
	}))

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "fb-cmd",
		Name:      "harness_feedback",
		Arguments: json.RawMessage(`{"summary":"cmd sink works","details":"stdin json check","category":"bug","tool_name":"shell"}`),
	})
	if res.IsError {
		t.Fatalf("command sink failed: %s", res.Output)
	}

	raw, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read captured command stdin: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(raw), &payload); err != nil {
		t.Fatalf("unmarshal command stdin: %v\nraw: %s", err, raw)
	}
	if payload["summary"] != "cmd sink works" || payload["tool_name"] != "shell" || payload["session_id"] != s.ID() {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

// Not parallel: uses t.Setenv to verify the command sink scrubs ambient secrets.
func TestHarnessFeedback_CommandSinkScrubsSecretEnv(t *testing.T) {
	const secretVal = "SENTINEL_SECRET_VALUE_4491"
	t.Setenv("OPENAI_API_KEY", secretVal)
	t.Setenv("EVENER_HARNESS_FEEDBACK_WEBHOOK", "https://hooks.example.test/"+secretVal)

	envOut := filepath.Join(t.TempDir(), "env.txt")
	s := newSession(t, withConfig(SessionConfig{
		StateDir: t.TempDir(),
		HarnessFeedback: HarnessFeedbackConfig{
			Command: `env > "` + envOut + `"`,
		},
	}))
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "fb-cmd-env",
		Name:      "harness_feedback",
		Arguments: json.RawMessage(`{"summary":"env check","details":"must scrub secrets"}`),
	})
	if res.IsError {
		t.Fatalf("command sink failed: %s", res.Output)
	}
	raw, err := os.ReadFile(envOut)
	if err != nil {
		t.Fatalf("read env dump: %v", err)
	}
	if strings.Contains(string(raw), secretVal) {
		t.Fatalf("command sink inherited secret env var:\n%s", raw)
	}
}

func TestHarnessFeedback_DeliveryErrorsNonFatalAndSanitized(t *testing.T) {
	t.Parallel()
	const secret = "SECRET_HOOK_TOKEN_7721"

	t.Run("http 500 does not leak url or body", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("internal echo: " + secret))
		}))
		defer srv.Close()

		filePath := filepath.Join(t.TempDir(), "backup.jsonl")
		s := newSession(t, withConfig(SessionConfig{
			StateDir: t.TempDir(),
			HarnessFeedback: HarnessFeedbackConfig{
				WebhookURL: srv.URL + "/services/" + secret + "?token=" + secret,
				FilePath:   filePath,
			},
		}))
		res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
			ID:        "fb-500",
			Name:      "harness_feedback",
			Arguments: json.RawMessage(`{"summary":"webhook down","details":"file sink should still record"}`),
		})
		if !res.IsError {
			t.Fatalf("expected error on HTTP 500, got: %s", res.Output)
		}
		if strings.Contains(res.Output, secret) || strings.Contains(res.Output, srv.URL) {
			t.Fatalf("error output leaked webhook URL or secret: %s", res.Output)
		}
		// Partial failure must still deliver to healthy sibling sinks.
		raw, err := os.ReadFile(filePath)
		if err != nil || !strings.Contains(string(raw), "webhook down") {
			t.Fatalf("file sink did not record report when webhook failed: err=%v raw=%q", err, raw)
		}
	})

	t.Run("connection refused does not leak url", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.NotFoundHandler())
		closedURL := srv.URL + "/services/" + secret
		srv.Close()

		s := newSession(t, withConfig(SessionConfig{
			StateDir:        t.TempDir(),
			HarnessFeedback: HarnessFeedbackConfig{WebhookURL: closedURL},
		}))
		res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
			ID:        "fb-refused",
			Name:      "harness_feedback",
			Arguments: json.RawMessage(`{"summary":"conn refused","details":"details"}`),
		})
		if !res.IsError {
			t.Fatalf("expected error on closed server, got: %s", res.Output)
		}
		if strings.Contains(res.Output, secret) || strings.Contains(res.Output, closedURL) {
			t.Fatalf("connection error leaked webhook secret/URL: %s", res.Output)
		}
	})

	t.Run("unsupported url scheme does not leak url", func(t *testing.T) {
		t.Parallel()
		s := newSession(t, withConfig(SessionConfig{
			StateDir:        t.TempDir(),
			HarnessFeedback: HarnessFeedbackConfig{WebhookURL: "ftp://example.com/" + secret},
		}))
		res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
			ID:        "fb-scheme",
			Name:      "harness_feedback",
			Arguments: json.RawMessage(`{"summary":"bad scheme","details":"details"}`),
		})
		if !res.IsError {
			t.Fatalf("expected error on unsupported scheme, got: %s", res.Output)
		}
		if strings.Contains(res.Output, secret) {
			t.Fatalf("scheme error leaked secret: %s", res.Output)
		}
	})

	t.Run("timeout cancels slow webhook without leaking url", func(t *testing.T) {
		t.Parallel()
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-release
		}))
		defer func() {
			close(release)
			srv.Close()
		}()

		s := newSession(t, withConfig(SessionConfig{
			StateDir: t.TempDir(),
			HarnessFeedback: HarnessFeedbackConfig{
				WebhookURL: srv.URL + "/" + secret,
				Timeout:    50 * time.Millisecond,
			},
		}))
		res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
			ID:        "fb-timeout",
			Name:      "harness_feedback",
			Arguments: json.RawMessage(`{"summary":"slow hook","details":"details"}`),
		})
		if !res.IsError {
			t.Fatalf("expected timeout error, got: %s", res.Output)
		}
		if strings.Contains(res.Output, secret) {
			t.Fatalf("timeout error leaked secret: %s", res.Output)
		}
	})

	t.Run("blank summary rejected", func(t *testing.T) {
		t.Parallel()
		filePath := filepath.Join(t.TempDir(), "should-not-exist.jsonl")
		s := newSession(t, withConfig(SessionConfig{
			StateDir:        t.TempDir(),
			HarnessFeedback: HarnessFeedbackConfig{FilePath: filePath},
		}))
		res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
			ID:        "fb-blank",
			Name:      "harness_feedback",
			Arguments: json.RawMessage(`{"summary":"   ","details":"real details"}`),
		})
		if !res.IsError {
			t.Fatalf("expected error on blank summary, got: %s", res.Output)
		}
		if _, err := os.Stat(filePath); !os.IsNotExist(err) {
			t.Fatalf("file sink should not be written when summary is blank (stat err: %v)", err)
		}
	})

	t.Run("command non-zero exit is non-fatal", func(t *testing.T) {
		t.Parallel()
		s := newSession(t, withConfig(SessionConfig{
			StateDir:        t.TempDir(),
			HarnessFeedback: HarnessFeedbackConfig{Command: "exit 3"},
		}))
		res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
			ID:        "fb-cmd-fail",
			Name:      "harness_feedback",
			Arguments: json.RawMessage(`{"summary":"cmd fail","details":"details"}`),
		})
		if !res.IsError {
			t.Fatalf("expected error on exit 3, got: %s", res.Output)
		}
	})
}

func TestHarnessFeedback_NeverPersistedAndReSuppliedOnRestore(t *testing.T) {
	t.Parallel()
	const secret = "SECRET_PERSIST_SENTINEL_5190"
	stateDir := realTempDirForTest(t)
	workspace := t.TempDir()
	fileSink := filepath.Join(t.TempDir(), "restore-feedback.jsonl")
	fbCfg := HarnessFeedbackConfig{
		WebhookURL: "https://hooks.example.test/services/" + secret,
		Command:    "cat >/dev/null # " + secret,
		FilePath:   fileSink,
	}

	s := newSession(t, withDir(workspace), withConfig(SessionConfig{
		StateDir:        stateDir,
		HarnessFeedback: fbCfg,
	}), withSteps(func(llm.Request) llm.Response {
		return agenttest.FinalResponse("ok")
	}))
	if _, err := s.ProcessInput(context.Background(), "hello", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	meta := s.Meta()
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
	s.Close()

	// Verify neither .meta.json nor transcript contains the secret or fileSink.
	metaBytes, err := os.ReadFile(schema.SessionMetaPath(stateDir, meta.ID))
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	if strings.Contains(string(metaBytes), secret) || strings.Contains(string(metaBytes), fileSink) {
		t.Fatalf("meta.json persisted harness feedback config:\n%s", metaBytes)
	}
	transcriptBytes, err := os.ReadFile(schema.SessionTranscriptPath(stateDir, meta.ID))
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if strings.Contains(string(transcriptBytes), secret) {
		t.Fatalf("transcript persisted harness feedback secret:\n%s", transcriptBytes)
	}

	// Restore WITHOUT HarnessFeedback: tool must be omitted.
	restoredOff, err := RestoreSessionFromMetaWithConfig(
		s.client, s.profile, execenv.NewLocalExecutionEnvironment(workspace), meta,
		RestoreSessionConfig{StateDir: stateDir},
	)
	if err != nil {
		t.Fatalf("restore without feedback: %v", err)
	}
	defer restoredOff.Close()
	if restoredOff.reg.Get("harness_feedback") != nil {
		t.Fatal("restored session without HarnessFeedback must omit harness_feedback")
	}

	// Restore WITH HarnessFeedback: tool is re-registered and delivers.
	restoredOn, err := RestoreSessionFromMetaWithConfig(
		s.client, s.profile, execenv.NewLocalExecutionEnvironment(workspace), meta,
		RestoreSessionConfig{
			StateDir:        stateDir,
			HarnessFeedback: HarnessFeedbackConfig{FilePath: fileSink},
		},
	)
	if err != nil {
		t.Fatalf("restore with feedback: %v", err)
	}
	defer restoredOn.Close()
	res := restoredOn.reg.ExecuteCall(context.Background(), restoredOn.env, llm.ToolCallData{
		ID:        "fb-restored",
		Name:      "harness_feedback",
		Arguments: json.RawMessage(`{"summary":"after restore","details":"still delivers"}`),
	})
	if res.IsError {
		t.Fatalf("restored harness_feedback failed: %s", res.Output)
	}
	raw, err := os.ReadFile(fileSink)
	if err != nil || !strings.Contains(string(raw), "after restore") {
		t.Fatalf("file sink missing restored report: err=%v raw=%q", err, raw)
	}
}

func TestHarnessFeedback_DelegateInheritance(t *testing.T) {
	t.Parallel()
	t.Run("omitted on delegates when parent unconfigured", func(t *testing.T) {
		t.Parallel()
		workspace := t.TempDir()
		s := newSession(t, withDir(workspace), withConfig(SessionConfig{
			StateDir:         realTempDirForTest(t),
			MaxSubagentDepth: 2,
			testOnly: testConfig{
				skipGitSnapshot:     true,
				minimalSystemPrompt: true,
				noSyncJobStore:      true,
				sandboxProber:       bwrapCapableProber(workspace),
			},
		}))
		for _, agentType := range []string{"subagent", "explorer"} {
			prepared := prepareStableChild(t, s, delegateArgs{Task: "investigate", AgentType: agentType})
			if prepared.sub.sess.reg.Get("harness_feedback") != nil {
				t.Errorf("unconfigured parent delegate %q unexpectedly registered harness_feedback", agentType)
			}
			if hasToolDef(prepared.sub.sess.ToolDefinitions(), "harness_feedback") {
				t.Errorf("unconfigured parent delegate %q unexpectedly advertised harness_feedback", agentType)
			}
			prepared.sub.sess.Close()
		}
	})
	for _, agentType := range []string{"subagent", "explorer"} {
		t.Run(agentType, func(t *testing.T) {
			t.Parallel()
			workspace := t.TempDir()
			stateDir := realTempDirForTest(t)
			fileSink := filepath.Join(t.TempDir(), "delegate-feedback.jsonl")

			s := newSession(t, withDir(workspace), withConfig(SessionConfig{
				StateDir:         stateDir,
				MaxSubagentDepth: 2,
				HarnessFeedback:  HarnessFeedbackConfig{FilePath: fileSink},
				testOnly: testConfig{
					skipGitSnapshot:     true,
					minimalSystemPrompt: true,
					noSyncJobStore:      true,
					sandboxProber:       bwrapCapableProber(workspace),
				},
			}))
			prepared := prepareStableChild(t, s, delegateArgs{Task: "investigate", AgentType: agentType})
			defer prepared.sub.sess.Close()

			child := prepared.sub.sess
			if child.reg.Get("harness_feedback") == nil {
				t.Fatalf("delegate %q missing harness_feedback tool", agentType)
			}
			if !hasToolDef(child.ToolDefinitions(), "harness_feedback") {
				t.Fatalf("delegate %q ToolDefinitions missing harness_feedback", agentType)
			}
			res := child.reg.ExecuteCall(context.Background(), child.env, llm.ToolCallData{
				ID:        "child-fb",
				Name:      "harness_feedback",
				Arguments: json.RawMessage(`{"summary":"from delegate","details":"child hit bump"}`),
			})
			if res.IsError {
				t.Fatalf("delegate %q harness_feedback failed: %s", agentType, res.Output)
			}

			raw, err := os.ReadFile(fileSink)
			if err != nil {
				t.Fatalf("read file sink: %v", err)
			}
			var row map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(raw), &row); err != nil {
				t.Fatalf("unmarshal delegate report: %v", err)
			}
			if row["session_id"] != child.ID() {
				t.Errorf("report session_id = %v, want child %q", row["session_id"], child.ID())
			}
			if row["agent_name"] != agentType {
				t.Errorf("report agent_name = %v, want %q", row["agent_name"], agentType)
			}
		})
	}
}
