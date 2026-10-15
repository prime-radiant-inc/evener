package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
)

// Serial because the daemon uses the process logger. This exercises the real
// private file owner, not an API-body logger or a mocked event consumer.
func TestSummaryFailureDaemonEvidence(t *testing.T) {
	stateDir := t.TempDir()
	log, err := openDaemonLog(stateDir, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(log.close)
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(log.file, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	adapter := &summaryFailureAdapter{}
	client := llm.NewClient()
	client.Register(adapter)
	sess, err := agent.NewSession(client, provider.WithCheapModel(provider.NewOpenAIProfile("gpt-5.2"), "cheap-fixture"), execenv.NewLocalExecutionEnvironment(stateDir), agent.SessionConfig{
		StateDir: stateDir, AgentsDocPath: filepath.Join(stateDir, "absent-AGENTS.md"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sess.Close)
	if err := sess.Rename("daemon-evidence"); err != nil {
		t.Fatal(err)
	}
	for i := range 8 {
		if _, err := sess.ProcessInput(context.Background(), fmt.Sprintf("human-fixture-%d", i), nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := sess.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !adapter.summaryCalled.Load() {
		t.Fatal("test did not reach summarization")
	}
	beforeRecovery := readSummaryDaemonRecords(t, log.path, sess.ID())
	if beforeRecovery != 1 {
		t.Errorf("retained summary failures = %d, want 1", beforeRecovery)
	}
	adapter.recovered.Store(true)
	if err := sess.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.ProcessInput(context.Background(), "after-recovery-a3d2", nil); err != nil {
		t.Fatal(err)
	}
	sess.Close()
	log.close()
	if after := readSummaryDaemonRecords(t, log.path, sess.ID()); after != beforeRecovery {
		t.Errorf("recovery added a failure: before %d, after %d", beforeRecovery, after)
	}
	for _, path := range []string{log.path, filepath.Dir(log.path)} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("diagnostic owner permissions = %o", info.Mode().Perm())
		}
	}
}

func readSummaryDaemonRecords(t *testing.T, path, sessionID string) int {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "private-provider-body-72ce") {
		t.Fatal("private response escaped to daemon log")
	}
	count := 0
	for line := range strings.SplitSeq(string(body), "\n") {
		if !strings.Contains(line, " code=summary_failure ") {
			continue
		}
		count++
		prefix, encoded, found := strings.Cut(line, " summary_failure=")
		if !found || !strings.HasSuffix(prefix, " session_id="+sessionID) {
			t.Fatalf("missing session identity or payload: %s", line)
		}
		payload, err := strconv.Unquote(encoded)
		if err != nil {
			t.Fatal(err)
		}
		var failure struct {
			Terminal struct {
				Route, Provider, Model, Kind string
				Status                       int
				Retryable                    *bool
			}
		}
		if err := json.Unmarshal([]byte(payload), &failure); err != nil {
			t.Fatal(err)
		}
		got := failure.Terminal
		if got.Route != "cheap" || got.Provider != "openai" || got.Model != "cheap-fixture" || got.Kind != "rate_limit" || got.Status != 429 || got.Retryable == nil || !*got.Retryable {
			t.Errorf("wrong retained attempt: %+v", got)
		}
	}
	return count
}

type summaryFailureAdapter struct {
	recovered     atomic.Bool
	summaryCalled atomic.Bool
}

func (*summaryFailureAdapter) Name() string { return "openai" }
func (*summaryFailureAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}
func (a *summaryFailureAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	if req.Model == "cheap-fixture" {
		a.summaryCalled.Store(true)
		if !a.recovered.Load() {
			return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 429, "private-provider-body-72ce", nil, nil)
		}
		return llm.Response{Message: llm.Assistant("## Progress\ndaemon-summary-26e7")}, nil
	}
	return activityRelayTool("communicate", map[string]any{
		"message": "turn-completed-46d1", "end_turn": true,
		"output": map[string]any{"message": "", "data": map[string]any{}, "artifacts": []string{}},
	}), nil
}
