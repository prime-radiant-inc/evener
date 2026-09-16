//go:build linux || darwin

package execenv_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/clock"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
)

// The read timeout is a deadlock safety bound, never a readiness/ordering oracle.
const detachedObservedCommand = `IFS= read -r -t 20 line < retirement-gate && printf '%s' "$line" > retirement-finished`

func detachedObservedFixture(t *testing.T) (*execenv.DetachedRetirementEnvironmentForTest, *execenv.DetachedRetirementObserverForTest, *os.File) {
	t.Helper()
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "retirement-gate"), 0600); err != nil {
		t.Fatal(err)
	}
	gate, err := os.OpenFile(filepath.Join(dir, "retirement-gate"), os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	env, observer := execenv.NewDetachedRetirementObserverForTest(dir)
	// Cleanup runs only after each test freezes and asserts its measured window.
	// The launcher alone owns Wait. Natural FIFO completion needs no PID lookup.
	t.Cleanup(func() {
		if _, err := gate.WriteString("original-cooperative-completion\n"); err != nil {
			t.Error(err)
		}
		for _, captured := range observer.Snapshot() {
			detachedObservedAwait(t, captured.Receipt.Done)
		}
		if err := gate.Close(); err != nil {
			t.Error(err)
		}
	})
	return env, observer, gate
}

func detachedObservedAwait(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	// TRIPWIRE: hang guard only; the real signal is the fixture process's own
	// completion receipt, which settles in milliseconds absent a deadlock.
	case <-time.After(5 * time.Second):
		t.Fatal("original fixture process did not settle")
	}
}

func detachedObservedCapture(t *testing.T, observer *execenv.DetachedRetirementObserverForTest) execenv.DetachedRetirementSnapshotForTest {
	t.Helper()
	captures := observer.Snapshot()
	if len(captures) != 1 {
		t.Fatalf("actual detached launcher capture count = %d", len(captures))
	}
	captured := captures[0]
	if captured.Runtime == nil || captured.Process == nil || captured.Receipt.PID <= 0 || captured.Process.Pid != captured.Receipt.PID || captured.Receipt.Done == nil || captured.Managed || captured.CombinedOutput {
		t.Fatalf("invalid original real detached runtime/handle/receipt: %+v", captured)
	}
	return captured
}

func TestDetachedRetirementObserverPositiveControls(t *testing.T) {
	for _, signal := range []string{"terminate", "kill"} {
		t.Run(signal, func(t *testing.T) {
			env, observer, _ := detachedObservedFixture(t)
			receipt, err := env.DetachCommand(context.Background(), detachedObservedCommand, env.RootDir, nil)
			if err != nil {
				t.Fatal(err)
			}
			before := detachedObservedCapture(t, observer)
			if before.Receipt != receipt || len(before.Signals) != 0 {
				t.Fatal("capture lost exact original successful launch")
			}
			select {
			case <-receipt.Done:
				t.Fatal("fixture exited before positive control")
			default:
			}
			if signal == "terminate" {
				before.Runtime.Terminate()
			} else {
				before.Runtime.Kill()
			}
			detachedObservedAwait(t, receipt.Done)
			after := detachedObservedCapture(t, observer)
			if after.Runtime != before.Runtime || after.Process != before.Process || after.Receipt != receipt || !reflect.DeepEqual(after.Signals, []string{signal}) {
				t.Fatalf("actual %s control missed original runtime/handle: %+v", signal, after)
			}
			t.Logf("actual %s attempts=%d on exact runtime, OS handle, PID and Done; original process reaped", signal, len(after.Signals))
		})
	}
}

func TestDetachedShellRetirementClaimDoesNotSignal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	env, observer, gate := detachedObservedFixture(t)
	client := llm.NewClient()
	var calls atomic.Int32
	adapter := &agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response {
		call := llm.ToolCallData{ID: "detached-proof-complete", Name: "communicate", Type: "function", Arguments: []byte(`{"message":"launch settled","end_turn":true,"output":{"message":"","data":{},"artifacts":[]}}`)}
		if calls.Add(1) == 1 {
			args, _ := json.Marshal(map[string]string{"command": detachedObservedCommand, "mode": "detached"})
			call = llm.ToolCallData{ID: "original-observed-detached", Name: "shell", Type: "function", Arguments: args}
		}
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}}
	}}
	client.Register(adapter)
	root, err := agent.NewSession(client, provider.NewOpenAIProfile("gpt-5.2"), env, agent.SessionConfig{StateDir: t.TempDir(), AgentsDocPath: filepath.Join(t.TempDir(), "absent-AGENTS.md"), NoProjectPrompts: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)
	// Actual manual naming prevents an unrelated autonomous naming launch.
	if err := root.Rename("Detached retirement proof"); err != nil {
		t.Fatal(err)
	}
	controller, err := agent.NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	if _, err := root.ProcessInput(context.Background(), "launch the independent fixture", nil); err != nil {
		t.Fatal(err)
	}
	before := detachedObservedCapture(t, observer)
	if len(before.Signals) != 0 {
		t.Fatalf("launch signaled original detached runtime: %+v", before.Signals)
	}
	sawReceipt := false
	for _, req := range adapter.Requests() {
		for _, message := range req.Messages {
			for _, part := range message.Content {
				if result := part.ToolResult; result != nil && result.ToolCallID == "original-observed-detached" {
					var receipt struct {
						PID          int
						Mode, Status string
					}
					if err := json.Unmarshal([]byte(fmt.Sprint(result.Content)), &receipt); err != nil || result.IsError || receipt.PID != before.Process.Pid || receipt.Mode != "detached" || receipt.Status != "started" {
						t.Fatalf("original registered tool receipt: %+v %v", result, err)
					}
					sawReceipt = true
				}
			}
		}
	}
	if !sawReceipt {
		t.Fatal("actual registered detached tool result did not reach provider")
	}
	select {
	case <-before.Receipt.Done:
		t.Fatal("fixture exited before claim window")
	default:
	}
	claim, state, err := controller.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("settled launch with independent live process not eligible: %+v %v", state, err)
	}
	if err := controller.Abort(claim, ""); err != nil {
		t.Fatal(err)
	}
	// Freeze the actual runtime-bound observation before natural completion,
	// Session.Close and fixture cleanup. This is not a syscall-wide oracle.
	measured := detachedObservedCapture(t, observer)
	select {
	case <-before.Receipt.Done:
		t.Fatal("claim/Abort ended original independent process")
	default:
	}
	if measured.Runtime != before.Runtime || measured.Process != before.Process || measured.Receipt != before.Receipt || len(measured.Signals) != 0 {
		t.Fatalf("claim/Abort changed or signaled original runtime: %+v", measured)
	}
	if _, err := gate.WriteString("original-cooperative-completion\n"); err != nil {
		t.Fatal(err)
	}
	detachedObservedAwait(t, before.Receipt.Done)
	content, err := os.ReadFile(filepath.Join(env.RootDir, "retirement-finished"))
	if err != nil || string(content) != "original-cooperative-completion" {
		t.Fatalf("original process failed cooperative completion: %q %v", content, err)
	}
	settled := detachedObservedCapture(t, observer)
	if settled.Runtime != before.Runtime || settled.Process != before.Process || settled.Receipt != before.Receipt || len(settled.Signals) != 0 {
		t.Fatal("original detached Wait settlement signaled or replaced the process")
	}
	t.Log("actual public Session -> registered shell -> real observed system runtime: exact live PID/Done survived eligible TryClaim/Abort, terminate=0 kill=0; original process completed cooperatively")
}
