package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
)

// neverEndingShellExecutor answers every command with a process that never
// exits until it is signalled — a service, the shape of #297's Flask server.
// The session's own Close() is what signals it, which is the point: nothing
// short of the run ending will ever finalize this job.
type neverEndingShellExecutor struct {
	signalled chan struct{}
	once      sync.Once
}

func newNeverEndingShellExecutor() *neverEndingShellExecutor {
	return &neverEndingShellExecutor{signalled: make(chan struct{})}
}

func (e *neverEndingShellExecutor) StreamCommand(_ context.Context, _, _ string, _ map[string]string, _ io.Writer) (*execenv.StreamHandle, error) {
	return &execenv.StreamHandle{
		Wait: func() (int, error) {
			<-e.signalled
			return 0, nil
		},
		Signal: e.signal,
	}, nil
}

// signal releases the process. Close()'s kill path and the test cleanup can
// both reach it, and from different goroutines, so the close is once-only.
func (e *neverEndingShellExecutor) signal() {
	e.once.Do(func() { close(e.signalled) })
}

// DetachSupported forwards the wrapped environment's answer. Optional
// capabilities are type-asserted, and an embedded INTERFACE promotes only that
// interface's own methods, so without this the wrapper silently answers
// "cannot detach" for a plain local environment — and the scenario below would
// assert against a message production never renders.
func (e *shellExecutorEnvironment) DetachSupported() bool {
	reporter, ok := e.ExecutionEnvironment.(execenv.DetachSupportReporter)
	return ok && reporter.DetachSupported()
}

// installShellExecutorEnvironment routes the session's shell tool at executor,
// leaving the rest of the execution environment real.
func installShellExecutorEnvironment(t *testing.T, executor execenv.StreamingExecutor) {
	t.Helper()
	oldNewSession := runNewSession
	runNewSession = func(client *llm.Client, profile *provider.Profile, env execenv.ExecutionEnvironment, cfg agent.SessionConfig) (*agent.Session, error) {
		return oldNewSession(client, profile, &shellExecutorEnvironment{ExecutionEnvironment: env, executor: executor}, cfg)
	}
	t.Cleanup(func() { runNewSession = oldNewSession })
}

// TestRunExitsWhenTheModelLeavesAServiceRunning is #297 end to end, through
// the real `evener run`: the model starts a background service, ends its turn,
// declines both announcements, and the process must exit anyway instead of
// sitting until something external kills it. In the trial this reproduces, the
// agent's own work finished in 149 seconds and the process then sat idle for
// 751 more.
//
// The escalation is paced by the model's own turns — announce, final warning,
// exit — so there is no grace to pin and no clock anywhere in this test.
func TestRunExitsWhenTheModelLeavesAServiceRunning(t *testing.T) {
	executor := newNeverEndingShellExecutor()
	installShellExecutorEnvironment(t, executor)
	t.Cleanup(executor.signal)

	const serviceCommand = "python3 -m flask run --host 0.0.0.0 --port 5000"
	var announcements []string
	adapter := &scriptedProvider{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return scriptedToolCalls(scriptedShellCall("serve_1", serviceCommand, "background"))
		},
		func(llm.Request) llm.Response {
			return scriptedCommunicate("server is up on port 5000")
		},
		func(req llm.Request) llm.Response {
			announcements = append(announcements, requestDeliveredText(req))
			// The model insists the service must keep running and ends the
			// turn again. Nothing is disposed of.
			return scriptedCommunicate("the server has to stay up")
		},
		func(req llm.Request) llm.Response {
			announcements = append(announcements, requestDeliveredText(req))
			return scriptedCommunicate("still refusing")
		},
	}}
	installRunScriptedProvider(t, adapter)

	var stdout, stderr bytes.Buffer
	// TRIPWIRE: a correct run returns in milliseconds — the escalation is
	// turn-paced and every turn is scripted. 60s only fires on the #297 hang
	// itself, which is the regression this test exists to catch.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, runConfig{
			prompt:  "start the API server in the background and report when it is up",
			model:   "openai/gpt-test",
			workDir: t.TempDir(),
			stdout:  &stdout,
			stderr:  &stderr,
		})
	}()

	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		t.Fatal("run() never returned with a live background job: the drain is blocked exactly as in #297, and only an external kill would end this process")
	}
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
	}
	if len(announcements) != 2 {
		t.Fatalf("announcement turns = %d, want exactly 2 (announce, then final warning)", len(announcements))
	}
	// The first announcement names the job's remedies with the names THIS
	// profile gives the model: exec_command is the OpenAI rename of shell, and
	// this run is unsandboxed so the detached relaunch really is available.
	first := announcements[0]
	for _, want := range []string{"cannot finish", "job_stop", "job_watch", "exec_command", `mode="detached"`} {
		if !strings.Contains(first, want) {
			t.Errorf("first announcement is missing %q:\n%s", want, first)
		}
	}
	if !strings.Contains(announcements[1], "Final notice") {
		t.Errorf("second announcement is not the final warning:\n%s", announcements[1])
	}

	// The run's answer is the model's real answer — never its reply to the
	// housekeeping turns.
	got := stdout.String()
	if !strings.Contains(got, "server is up on port 5000") {
		t.Fatalf("stdout = %q, want the model's actual answer", got)
	}
	if strings.Contains(got, "the server has to stay up") || strings.Contains(got, "still refusing") {
		t.Fatalf("stdout = %q: an announcement reply replaced the run's answer", got)
	}
	// The kill is reported where the operator can see what died: id, command
	// text, runtime.
	if !strings.Contains(stderr.String(), "undisposed background job") || !strings.Contains(stderr.String(), serviceCommand) {
		t.Fatalf("stderr does not report the killed job with its command text:\n%s", stderr.String())
	}
}
