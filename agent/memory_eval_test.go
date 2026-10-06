package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/responses"
	"primeradiant.com/evener/llm/providers/tokenauth"
)

type memoryEvalRoundTripFunc func(*http.Request) (*http.Response, error)

func (f memoryEvalRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMemoryEvalToolchainCapture(t *testing.T) {
	t.Parallel()
	if memoryEvalToolchainError != nil {
		t.Fatal(memoryEvalToolchainError)
	}
	cmd := exec.Command(filepath.Join(memoryEvalToolchainRoot, "bin", "go"), "version")
	cmd.Env = []string{"GOENV=off", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "GOWORK=off"}
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "go version "+runtime.Version()+" "+runtime.GOOS+"/"+runtime.GOARCH {
		t.Fatalf("captured Go version = %q, error = %v", out, err)
	}
}

func TestMemoryEvalToolchain(t *testing.T) {
	// Serial: fixture HOME/XDG isolation is process-global.
	home := memoryEvalIsolateProcess(t)
	workspace := t.TempDir()
	memoryEvalFixture(t, "recall", workspace)
	memoryEvalWrite(t, filepath.Join(workspace, "main_test.go"), memoryEvalVisible("A"))
	raw, err := os.ReadFile(filepath.Join(workspace, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	memoryEvalWrite(t, filepath.Join(workspace, "main.go"), strings.Replace(string(raw), "return v", "return strings.TrimSpace(v)", 1))
	env := memoryEvalLocal(t, home, workspace, t.TempDir())
	defer env.Cleanup()
	result, err := env.ExecCommand(context.Background(), memoryEvalGoEnv()+"sh -c 'go version && sh scripts/check.sh'", 180000, workspace, nil)
	if err != nil || result.ExitCode != 0 || !strings.Contains(result.Stdout, runtime.Version()) {
		t.Fatalf("fixture checker lost compiling toolchain %s: exit=%d error=%v output=%s%s", runtime.Version(), result.ExitCode, err, result.Stdout, result.Stderr)
	}
}

func TestMemoryEvalVerifierInfrastructure(t *testing.T) {
	// Serial: fixture HOME/XDG isolation is process-global.
	home := memoryEvalIsolateProcess(t)
	for _, mode := range []string{"compile", "module"} {
		t.Run(mode, func(t *testing.T) {
			workspace := t.TempDir()
			memoryEvalFixture(t, "recall", workspace)
			if mode == "compile" {
				memoryEvalWrite(t, filepath.Join(workspace, "main.go"), "package main\nfunc normalize(v string) string { return missingSymbol }\n")
			} else {
				memoryEvalWrite(t, filepath.Join(workspace, "go.mod"), "module memoryfixture\ngo 99.0.0\n")
			}
			pass, output, err := memoryEvalVerify(context.Background(), t, home, workspace, "A")
			if err == nil || pass {
				t.Fatalf("real Go setup/compiler failure accepted: pass=%t error=%v output=%s", pass, err, output)
			}
		})
	}
}

func TestMemoryEvalVerifierStopsRunner(t *testing.T) {
	// Serial: fixture HOME and Responses client are process-global.
	for _, mode := range []string{"initial-module", "initial-compile", "final-compile"} {
		t.Run(mode, func(t *testing.T) {
			home := memoryEvalIsolateProcess(t)
			b := &memoryEvalAdmission{}
			adapter := &memoryEvalAdapter{}
			c, p := memoryEvalFixtureClient(t, b, adapter)
			stages, calls := 0, 0
			episodes, runErr := memoryEvalRunPairs(t, b, c, p, home, func(_ string, _ bool, _ memoryEvalStage, _ string, workspace string) {
				stages++
				if mode == "initial-module" {
					memoryEvalWrite(t, filepath.Join(workspace, "go.mod"), "module memoryfixture\ngo 99.0.0\n")
				}
				broken := "package main\nfunc normalize(v string) string { return missingSymbol }\n"
				if mode == "initial-compile" {
					memoryEvalWrite(t, filepath.Join(workspace, "main.go"), broken)
				}
				adapter.complete = func(context.Context, llm.Request) (llm.Response, error) {
					calls++
					if mode == "final-compile" && calls == 1 {
						return memoryEvalCall("write_file", map[string]any{"file_path": "main.go", "content": broken}), nil
					}
					return finalResponse("opaque-verifier-stop-707"), nil
				}
			})
			if stages != 1 || runErr == nil || !errors.Is(b.terminalFailure(), runErr) || len(episodes) != 0 {
				t.Fatalf("verifier infrastructure did not stop runner: stages=%d calls=%d %s", stages, calls, memoryEvalSnapshot(b))
			}
			if strings.HasPrefix(mode, "initial-") && (calls != 0 || b.logical != 0 || b.httpAttempts != 0) {
				t.Fatalf("initial verifier allowed dispatch: calls=%d %s", calls, memoryEvalSnapshot(b))
			}
			if mode == "final-compile" && (calls != 2 || b.logical < 2) {
				t.Fatalf("final verifier control never executed candidate write and completion: calls=%d %s", calls, memoryEvalSnapshot(b))
			}
			before := b.logical
			if b.beginStage(8, time.Now().Add(time.Minute)) == nil {
				t.Fatal("verifier failure admitted another stage")
			}
			_, dispatchErr := c.Complete(context.Background(), llm.Request{Provider: "codex-jesse-at-pr", Model: "gpt-6.1-sol", Messages: []llm.Message{llm.User("opaque-refused-dispatch-708")}})
			if !errors.Is(dispatchErr, runErr) || b.logical != before {
				t.Fatalf("verifier terminal error lost before another dispatch: returned=%v cause=%v logical=%d want=%d", dispatchErr, runErr, b.logical, before)
			}
		})
	}
}

func TestMemoryEvalVerifierBehavior(t *testing.T) {
	// Serial: fixture HOME/XDG isolation is process-global.
	home := memoryEvalIsolateProcess(t)
	for _, stage := range []string{"A", "B", "C"} {
		for _, wantPass := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/pass=%t", stage, wantPass), func(t *testing.T) {
				workspace := t.TempDir()
				memoryEvalFixture(t, "recall", workspace)
				if wantPass {
					memoryEvalWrite(t, filepath.Join(workspace, "main.go"), `package main
import "strings"
func normalize(v string) string { return strings.TrimSpace(v) }
func split(v string) []string { r:=strings.Split(v,","); for i:=range r { r[i]=strings.TrimSpace(r[i]) }; return r }
func join(v []string) string { r:=make([]string,len(v)); for i:=range v { r[i]=strings.TrimSpace(v[i]) }; return strings.Join(r,",") }
func main() {}
`)
				}
				pass, output, err := memoryEvalVerify(context.Background(), t, home, workspace, stage)
				if err != nil || pass != wantPass {
					t.Fatalf("real held-out behavior: pass=%t want=%t error=%v output=%s", pass, wantPass, err, output)
				}
			})
		}
	}
}

func TestMemoryEvalVerifierExecutionEvidence(t *testing.T) {
	// Serial: fixture HOME/XDG isolation is process-global.
	home := memoryEvalIsolateProcess(t)
	for _, mode := range []string{"missing", "skipped", "incomplete"} {
		t.Run(mode, func(t *testing.T) {
			workspace := t.TempDir()
			memoryEvalFixture(t, "recall", workspace)
			source := "package main\n"
			if mode == "skipped" {
				source += "import \"testing\"\nfunc TestNormalizeHeldOut(t *testing.T) { t.Skip() }\n"
			}
			if mode == "incomplete" {
				source = memoryEvalHeldOut("A")
			}
			memoryEvalWrite(t, filepath.Join(workspace, "held_out_test.go"), source)
			env := memoryEvalLocal(t, home, workspace, t.TempDir())
			defer env.Cleanup()
			result, execErr := env.ExecCommand(context.Background(), memoryEvalGoEnv()+"go test -json -count=1 ./...", 180000, workspace, nil)
			wantExit := 0
			if mode == "incomplete" {
				wantExit = 1 // The real assertion fails before its output is truncated.
			}
			if result.ExitCode != wantExit || (wantExit == 0 && execErr != nil) {
				t.Fatalf("real Go evidence control failed: exit=%d err=%v output=%s%s", result.ExitCode, execErr, result.Stdout, result.Stderr)
			}
			output := result.Stdout
			if mode == "incomplete" {
				// Drop only the terminal package event from actual Go output.
				lines := strings.Split(strings.TrimSpace(output), "\n")
				output = strings.Join(lines[:len(lines)-1], "\n")
			}
			pass, err := memoryEvalVerifierResult(output, result.ExitCode, "A")
			if err == nil || pass {
				t.Fatalf("%s held-out execution evidence accepted: pass=%t error=%v", mode, pass, err)
			}
		})
	}
}

func TestMemoryEvalAdmission(t *testing.T) {
	// Serial: actual Responses client seam and fixture HOME are process-global.
	now := time.Now()
	for _, httpOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("independent-http=%t", httpOnly), func(t *testing.T) {
			b := &memoryEvalAdmission{stageCap: 100, deadline: now.Add(time.Minute)}
			var reached int
			base := memoryEvalRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				reached++
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
			})
			tr := &memoryEvalTransport{budget: b, base: base, endpoint: "http://fixture.invalid/responses"}
			complete := b.middleware(nil).WrapComplete(func(context.Context, llm.Request) (llm.Response, error) { reached++; return llm.Response{}, nil })
			for i := range 97 {
				var err error
				if httpOnly {
					req, _ := http.NewRequest(http.MethodPost, tr.endpoint, nil)
					var resp *http.Response
					resp, err = tr.RoundTrip(req)
					if resp != nil {
						resp.Body.Close()
					}
				} else {
					_, err = complete(context.Background(), llm.Request{})
				}
				if (i < 96) != (err == nil) {
					t.Errorf("attempt %d err=%v", i+1, err)
				}
			}
			if reached != 96 {
				t.Errorf("external boundary reached %d want 96", reached)
			}
			if httpOnly && (b.httpAttempts != 96 || b.logical != 0) {
				t.Errorf("independent counters=%d/%d", b.logical, b.httpAttempts)
			}
			if !httpOnly && (b.logical != 96 || b.httpAttempts != 0) {
				t.Errorf("independent counters=%d/%d", b.logical, b.httpAttempts)
			}
		})
	}
	t.Run("stage-deadline-reset", func(t *testing.T) {
		b := &memoryEvalAdmission{}
		for _, kind := range []string{"logical", "http", "tool"} {
			b.beginStage(2, now.Add(time.Minute))
			for i := range 3 {
				var err error
				switch kind {
				case "logical":
					err = b.admit(false, now)
				case "http":
					err = b.admit(true, now)
				case "tool":
					err = b.admitToolRound(now)
				}
				if (i < 2) != (err == nil) {
					t.Errorf("%s %d err=%v", kind, i, err)
				}
			}
		}
		b.beginStage(2, now)
		if b.admit(false, now) == nil || b.admit(true, now) == nil || b.admitToolRound(now) == nil {
			t.Error("expired admitted")
		}
		if b.logical != 2 || b.httpAttempts != 2 {
			t.Errorf("globals reset %d/%d", b.logical, b.httpAttempts)
		}
	})
	t.Run("concurrent", func(t *testing.T) {
		b := &memoryEvalAdmission{stageCap: 100, deadline: now.Add(time.Minute)}
		var reached atomic.Int32
		var wg sync.WaitGroup
		for range 194 {
			wg.Go(func() {
				if b.admit(false, now) == nil {
					reached.Add(1)
				}
			})
		}
		wg.Wait()
		if reached.Load() != 96 {
			t.Fatalf("concurrent admissions=%d", reached.Load())
		}
	})
	t.Run("complete-multiple-calls-one-round", func(t *testing.T) {
		b := &memoryEvalAdmission{stageCap: 2, deadline: now.Add(time.Minute)}
		response := toolCallResponse(llm.ToolCallData{ID: "1", Name: "read_file", Arguments: []byte("{}")}, llm.ToolCallData{ID: "2", Name: "read_file", Arguments: []byte("{}")})
		call := b.middleware(nil).WrapComplete(func(context.Context, llm.Request) (llm.Response, error) { return response, nil })
		if _, err := call(context.Background(), llm.Request{}); err != nil {
			t.Fatal(err)
		}
		if b.toolRounds != 1 {
			t.Fatalf("rounds=%d want 1", b.toolRounds)
		}
		// Consume the remaining round without consuming a logical call.
		if err := b.admitToolRound(now); err != nil {
			t.Fatal(err)
		}
		resp, err := call(context.Background(), llm.Request{})
		if err == nil || len(resp.ToolCalls()) != 0 {
			t.Fatal("tool response delivered over cap")
		}
	})
	for _, finishOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream-finish-only=%t", finishOnly), func(t *testing.T) {
			b := &memoryEvalAdmission{stageCap: 2, deadline: now.Add(time.Minute)}
			resp := toolCallResponse(llm.ToolCallData{ID: "1", Name: "read_file", Arguments: []byte("{}")}, llm.ToolCallData{ID: "2", Name: "read_file", Arguments: []byte("{}")})
			var closes atomic.Int32
			call := b.middleware(nil).WrapStream(func(context.Context, llm.Request) (llm.Stream, error) {
				s := llm.NewChanStream(func() { closes.Add(1) })
				if !finishOnly {
					s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallData{ID: "1"}})
					s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallData{ID: "2"}})
				}
				s.Send(llm.StreamEvent{Type: llm.StreamEventFinish, Response: &resp})
				s.CloseSend()
				return s, nil
			})
			s, err := call(context.Background(), llm.Request{})
			if err != nil {
				t.Fatal(err)
			}
			for range s.Events() {
			}
			s.Close()
			if b.toolRounds != 1 {
				t.Fatalf("rounds=%d want 1", b.toolRounds)
			}
			b.admitToolRound(now)
			s, err = call(context.Background(), llm.Request{})
			if err != nil {
				t.Fatal(err)
			}
			errors := 0
			for ev := range s.Events() {
				if ev.ToolCall != nil || (ev.Response != nil && len(ev.Response.ToolCalls()) > 0) {
					t.Error("tool data escaped refusal")
				}
				if ev.Err != nil {
					errors++
				}
			}
			s.Close()
			if errors != 1 || closes.Load() != 2 {
				t.Fatalf("errors/closed=%d/%d", errors, closes.Load())
			}
		})
	}
	t.Run("approved-literal-math", func(t *testing.T) {
		if 2*(8+10)+2*(8+12+10) != 96 || 4*6 != 24 {
			t.Fatal("approved math")
		}
		if fmt.Sprint(memoryRecallStages) != "[{A 8} {B 10}]" || fmt.Sprint(memoryCorrectionStages) != "[{A 8} {B 12} {C 10}]" {
			t.Fatal("stage caps differ from approval")
		}
	})
	t.Run("real-transport-retry", memoryEvalTransportRetry)
	t.Run("root-child-auxiliary-cancel-join", memoryEvalCancellation)
	t.Run("nested-deadlines", func(t *testing.T) {
		run := time.Unix(100, 0)
		arm := run.Add(10 * time.Minute)
		stage := arm.Add(5 * time.Minute)
		if d := memoryEvalDeadline(stage, arm, run); !d.Equal(arm.Add(6 * time.Minute)) {
			t.Fatal("correction arm received nine minutes")
		}
		if d := memoryEvalDeadline(run.Add(23*time.Minute), run.Add(22*time.Minute), run); !d.Equal(run.Add(24 * time.Minute)) {
			t.Fatal("run ceiling widened")
		}
	})

}

func memoryEvalCall(name string, args map[string]any) llm.Response {
	raw, _ := json.Marshal(args)
	return toolCallResponse(llm.ToolCallData{ID: name, Name: name, Arguments: raw})
}
func TestMemoryEvalIsolation(t *testing.T) {
	// Serial: HOME/XDG decoys and environment isolation are process-global.
	home := memoryEvalIsolateProcess(t)
	decoyRoot := t.TempDir()
	authPath := filepath.Join(decoyRoot, "auth", "codex-jesse-at-pr.json")
	verifierPath := filepath.Join(decoyRoot, "verifier", "hidden_test.go")
	memoryEvalWrite(t, authPath, "fixture auth decoy 611")
	memoryEvalWrite(t, verifierPath, "fixture verifier decoy 612")
	memoryEvalWrite(t, filepath.Join(home, "HOME", ".local", "state", "evener", "memory", "personal", "MEMORY.md"), "fixture ambient memory 613")
	memoryEvalWrite(t, filepath.Join(home, "XDG_CONFIG_HOME", "evener", "AGENTS.md"), "fixture ambient config 614")
	ambient := []string{
		filepath.Join(home, "HOME", ".local", "state", "evener", "memory", "personal", "MEMORY.md"),
		filepath.Join(home, "XDG_CONFIG_HOME", "evener", "AGENTS.md"),
		filepath.Join(home, "XDG_CONFIG_HOME", "evener", "providers.toml"),
		filepath.Join(home, "XDG_STATE_HOME", "evener", "projects", "ambient", "sessions", "ambient", "transcript.jsonl"),
	}
	memoryEvalWrite(t, ambient[2], "fixture ambient provider 674")
	memoryEvalWrite(t, ambient[3], "fixture ambient history 675")
	untouched := memoryEvalTrackAmbientReads(t, ambient)
	var ordinary []string
	for _, disabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("disabled=%t", disabled), func(t *testing.T) {
			root, workspace, scratch := t.TempDir(), t.TempDir(), t.TempDir()
			memoryEvalWrite(t, filepath.Join(workspace, "visible.txt"), "opaque-visible-615")
			memoryEvalWrite(t, filepath.Join(root, "wiki", "memory", "personal", "MEMORY.md"), "opaque-native-616")
			b := &memoryEvalAdmission{}
			b.beginStage(30, time.Now().Add(time.Minute))
			var ioCount atomic.Int32
			adapter := &memoryEvalAdapter{complete: func(context.Context, llm.Request) (llm.Response, error) {
				return finalResponse("opaque-prior-617"), nil
			}}
			c, p := memoryEvalFixtureClient(t, b, adapter)
			cfg := memoryEvalConfig(context.Background(), root, disabled, 30)
			cfg.testOnly.memoryBeforeIO = func(string, string) error { ioCount.Add(1); return nil }
			first, err := NewSession(c, p, memoryEvalLocal(t, home, workspace, scratch), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = first.ProcessInput(context.Background(), "opaque-history-input-618", nil); err != nil {
				t.Fatal(err)
			}
			priorID := first.ID()
			first.Close()
			var n int
			adapter.complete = func(ctx context.Context, req llm.Request) (llm.Response, error) {
				for _, msg := range req.Messages {
					for _, bad := range []string{"fixture auth decoy 611", "fixture verifier decoy 612", "fixture ambient memory 613", "fixture ambient config 614", "fixture ambient provider 674", "fixture ambient history 675"} {
						if strings.Contains(msg.Text(), bad) {
							t.Errorf("private/ambient data reached provider: %s", bad)
						}
					}
				}
				switch n {
				case 0:
					var tools []string
					for _, def := range req.Tools {
						if !strings.HasPrefix(def.Name, "memory_") {
							tools = append(tools, def.Name)
						}
					}
					slices.Sort(tools)
					if ordinary == nil {
						ordinary = tools
					} else if !slices.Equal(ordinary, tools) {
						t.Error("non-memory tool difference")
					}
					n++
					return memoryEvalCall("read_file", map[string]any{"file_path": authPath}), nil
				case 1:
					n++
					return memoryEvalCall("read_file", map[string]any{"file_path": verifierPath}), nil
				case 2:
					n++
					return memoryEvalCall("exec_command", map[string]any{"command": "cat '" + authPath + "' '" + verifierPath + "'", "mode": "foreground"}), nil
				case 3:
					n++
					return memoryEvalCall("read_transcript", map[string]any{"transcript_ref": priorID}), nil
				case 4:
					memoryRequireResult(t, req, "read_transcript", "opaque-history-input-618")
					n++
					if !disabled {
						return memoryEvalCall("memory_read", map[string]any{"scope": "personal", "file_path": "MEMORY.md"}), nil
					}
					return finalResponse("ordinary complete"), nil
				default:
					memoryRequireResult(t, req, "memory_read", "opaque-native-616")
					return finalResponse("enabled complete"), nil
				}
			}
			second, err := NewSession(c, p, memoryEvalLocal(t, home, workspace, t.TempDir()), cfg)
			if err != nil {
				t.Fatal(err)
			}
			seen, stop := captureEvents(second)
			if _, err = second.ProcessInput(context.Background(), "opaque-fresh-input-619", nil); err != nil {
				t.Fatal(err)
			}
			second.Close()
			stop()
			denied := 0
			history := false
			for _, ev := range *seen {
				if ev.Kind != events.EventToolCallEnd {
					continue
				}
				d := ev.Data.(events.ToolCallEndData)

				if d.ToolName == "read_file" {
					if d.Error == "" {
						t.Errorf("decoy read not denied: %+v", d)
					} else {
						denied++
					}
				}
				if d.ToolName == "shell" {
					if strings.Contains(d.Output, "fixture auth decoy 611") || strings.Contains(d.Output, "fixture verifier decoy 612") {
						t.Fatal("shell leaked decoy")
					}
					if !strings.Contains(d.Output, "exit 1") && !strings.Contains(d.Output, "exit_code") && d.Error == "" {
						t.Errorf("shell refusal not recorded: %+v", d)
					}
					denied++
				}
				if d.ToolName == "read_transcript" && d.Error == "" {
					history = true
				}
			}
			if denied != 3 || !history {
				t.Fatalf("denied=%d history=%t", denied, history)
			}
			if disabled && ioCount.Load() != 0 {
				t.Fatalf("disabled native I/O=%d", ioCount.Load())
			}
			if !disabled && ioCount.Load() == 0 {
				t.Fatal("enabled had no native I/O")
			}
			for _, decoy := range []struct{ path, want string }{{authPath, "fixture auth decoy 611"}, {verifierPath, "fixture verifier decoy 612"}} {
				got, err := os.ReadFile(decoy.path)
				if err != nil || string(got) != decoy.want {
					t.Fatal("decoy changed")
				}
			}
		})
	}
	untouched()
}

func memoryEvalHTTPClient(t *testing.T, b *memoryEvalAdmission, url string) (*llm.Client, *provider.Profile) {
	t.Helper()
	root := t.TempDir()
	config := filepath.Join(root, "providers.toml")
	memoryEvalWrite(t, config, "[providers.codex-jesse-at-pr]\nbase = \"openai-codex\"\nbase_url = "+fmt.Sprintf("%q", url)+"\n")
	record := openai.AuthRecord{Version: 1, Provider: "openai", Source: "oauth", ObtainedAt: time.Now(), TokenType: "Bearer", Scope: "openid profile email offline_access", AccessToken: "opaque-access-decoy", RefreshToken: "opaque-refresh-decoy", Expiry: time.Now().Add(time.Hour), AccountID: "opaque-account-decoy", WorkspaceID: "opaque-workspace-decoy"}
	if err := openai.SaveAuth(root, "codex-jesse-at-pr", record); err != nil {
		t.Fatal(err)
	}
	r := memoryEvalRegistry(t, root, config)
	p, err := provider.Resolve(r, memoryEvalModel)
	if err != nil {
		t.Fatal(err)
	}
	p = provider.WithCheapModel(p, memoryEvalModel)
	c := llm.NewClient(llm.WithRegistry(r), llm.WithClientStateDir(root))
	c.Use(b.middleware(tokenauth.ScopedCodex(root)))
	old := responses.DefaultProtocol.Client
	responses.DefaultProtocol.Client = &http.Client{Transport: &memoryEvalTransport{budget: b, base: http.DefaultTransport, endpoint: url + "/responses"}}
	t.Cleanup(func() { b.active.Wait(); responses.DefaultProtocol.Client = old })
	return c, p
}
func memoryEvalTransportRetry(t *testing.T) {
	var reached atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer opaque-access-decoy" {
			t.Error("fixture scoped auth mismatch")
		}
		reached.Add(1)
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"message":"Unknown parameter: 'store'.","type":"invalid_request_error","param":"store","code":"unknown_parameter"}}`)
	}))
	defer srv.Close()
	b := &memoryEvalAdmission{}
	b.beginStage(1, time.Now().Add(time.Minute))
	c, _ := memoryEvalHTTPClient(t, b, srv.URL)
	_, err := c.Complete(context.Background(), llm.Request{Provider: "codex-jesse-at-pr", Model: "gpt-6.1-sol", Messages: []llm.Message{llm.User("opaque-preflight")}})
	if err == nil {
		t.Fatal("retry incorrectly passed")
	}
	if reached.Load() != 1 || b.logical != 1 || b.httpAttempts != 1 {
		t.Fatalf("retry escaped cap, external=%d %s", reached.Load(), memoryEvalSnapshot(b))
	}
	// An unexpected URL fails before even an HTTP admission.
	tr := responses.DefaultProtocol.Client.Transport
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/not-responses", nil)
	if _, err := tr.RoundTrip(req); err == nil {
		t.Fatal("uninstrumented route admitted")
	}
	if reached.Load() != 1 {
		t.Fatal("unmatched route dispatched")
	}
}
func memoryEvalCancellation(t *testing.T) {
	home := memoryEvalIsolateProcess(t)
	arrivals := make(chan struct{}, 10)
	settled := make(chan struct{}, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer opaque-access-decoy" {
			t.Error("fixture scoped auth mismatch")
		}
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		arrivals <- struct{}{}
		<-r.Context().Done()
		settled <- struct{}{}
	}))
	defer srv.Close()
	originalAuthRoot := tokenauth.DefaultCodex.StateDir
	b := &memoryEvalAdmission{}
	// Root, auxiliary and child occupy exactly three held admissions. A child's
	// advisory naming call must not race this fixture's cancellation into a fourth.
	b.beginStage(3, time.Now().Add(time.Minute))
	c, p := memoryEvalHTTPClient(t, b, srv.URL)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Minute))
	defer cancel()
	root, workspace := t.TempDir(), t.TempDir()
	s, err := NewSession(c, p, memoryEvalLocal(t, home, workspace, t.TempDir()), memoryEvalConfig(ctx, root, false, 20))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); s.ProcessInput(ctx, "opaque-held-root", nil) }()
	// Root's Stream and its real session-name Complete are both admitted.
	for range 2 {
		select {
		case <-arrivals:
		case <-ctx.Done():
			t.Fatal("root/auxiliary did not reach HTTP")
		}
	}
	child := s.createDelegate(ctx, delegateArgs{Task: "opaque-held-child", AgentType: "explorer", DelegationAllowance: new(0)})
	if child.Err != nil {
		t.Fatal(child.Err)
	}
	select {
	case <-arrivals:
	case <-ctx.Done():
		t.Fatal("real child did not reach HTTP")
	}
	actual := s.subagents.get(child.ChildSessionID)
	if actual == nil {
		t.Fatal("real child missing before Close")
	}
	actual.mu.Lock()
	childDone := actual.done
	actual.mu.Unlock()
	cancel()
	<-done
	s.Close()
	<-childDone
	b.active.Wait()
	for range 3 {
		<-settled
	}
	if b.logical != 3 || b.httpAttempts != 3 {
		t.Fatalf("root/child/auxiliary counters %s", memoryEvalSnapshot(b))
	}
	if tokenauth.DefaultCodex.StateDir != originalAuthRoot {
		t.Fatal("global Codex auth root changed")
	}
	// Reset only after real root, descendant, auxiliary and HTTP handlers joined.
	b.beginStage(8, time.Now().Add(time.Minute))
	if b.logical != 3 || b.httpAttempts != 3 || b.stageLogical != 0 || b.stageHTTP != 0 {
		t.Fatal("stage reset lost global accounting")
	}
}

func TestMemoryEvalEpisodes(t *testing.T) {
	home := memoryEvalIsolateProcess(t)
	b := &memoryEvalAdmission{}
	adapter := &memoryEvalAdapter{}
	c, p := memoryEvalFixtureClient(t, b, adapter)
	episodes, err := memoryEvalRunPairs(t, b, c, p, home, func(pair string, disabled bool, stage memoryEvalStage, prior, _ string) {
		var steps []llm.Response
		checker := "sh scripts/check.sh"
		shell := func(command string) llm.Response {
			return memoryEvalCall("exec_command", map[string]any{"command": memoryEvalGoEnv() + command, "mode": "foreground"})
		}
		if stage.name == "A" {
			steps = append(steps, shell("cd scripts && sh check.sh"))
		} else if disabled {
			steps = append(steps, memoryEvalCall("read_transcript", map[string]any{"transcript_ref": prior}))
		} else {
			steps = append(steps, memoryEvalCall("memory_read", map[string]any{"scope": "project", "file_path": "MEMORY.md"}))
		}
		if pair == "correction" && stage.name != "A" {
			checker += " --current"
			if stage.name == "B" {
				steps = append(steps, shell("sh scripts/check.sh"))
			}
		}
		source := `package main
import "strings"
func normalize(v string) string { return strings.TrimSpace(v) }
func split(v string) []string { return strings.Split(v, ",") }
func join(v []string) string { return strings.Join(v, ",") }
func main() {}
`
		if stage.name != "A" {
			source = strings.Replace(source, `return strings.Split(v, ",")`, `r:=strings.Split(v, ","); for i:=range r { r[i]=strings.TrimSpace(r[i]) }; return r`, 1)
		}
		if stage.name == "C" {
			source = strings.Replace(source, `return strings.Join(v, ",")`, `r:=make([]string,len(v)); for i:=range v { r[i]=strings.TrimSpace(v[i]) }; return strings.Join(r, ",")`, 1)
		}
		steps = append(steps, memoryEvalCall("write_file", map[string]any{"file_path": "main.go", "content": source}), shell(checker))
		if !disabled && stage.name == "A" {
			steps = append(steps, memoryEvalCall("memory_write", map[string]any{"scope": "project", "file_path": "MEMORY.md", "content": "From the repository root run sh scripts/check.sh\n"}))
		}
		if !disabled && pair == "correction" && stage.name == "B" {
			steps = append(steps, memoryEvalCall("memory_edit", map[string]any{"scope": "project", "file_path": "MEMORY.md", "old_string": "sh scripts/check.sh", "new_string": "sh scripts/check.sh --current"}))
		}
		steps = append(steps, finalResponse("opaque-episode-completion-631"))
		n := 0
		adapter.complete = func(ctx context.Context, req llm.Request) (llm.Response, error) {
			if n >= len(steps) {
				return llm.Response{}, errors.New("unexpected scripted call")
			}
			if n > 0 && (stage.name != "A" || n != 1) && (pair != "correction" || stage.name != "B" || n != 2) { // Successful tool results come from real execution.
				name := steps[n-1].ToolCalls()[0].Name
				if name == "exec_command" {
					name = "shell"
				}
				memoryRequireResult(t, req, name, "")
			}
			r := steps[n]
			n++
			return r, nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(episodes) != 4 {
		t.Fatalf("arm episodes=%d want 4", len(episodes))
	}
	for i, e := range episodes {
		wantPair := "recall"
		wantStages := 2
		if i >= 2 {
			wantPair = "correction"
			wantStages = 3
		}
		if e.Pair != wantPair || e.Disabled != (i%2 == 1) || len(e.Stages) != wantStages {
			t.Fatalf("chronology=%+v", e)
		}
		for j, s := range e.Stages {
			if j > 0 && s.PriorSessionID != e.Stages[j-1].SessionID {
				t.Fatal("fresh stage did not preserve actual prior history")
			}
			if j == 0 && s.PriorSessionID != "" {
				t.Fatal("arm inherited another arm history")
			}
			if !s.Task || !s.Application {
				t.Errorf("%s disabled=%t %s task=%t application=%t check=%s", e.Pair, e.Disabled, s.Name, s.Task, s.Application, s.Verifier)
			}
			if !e.Disabled && s.Name == "A" && !s.Capture {
				t.Error("capture not proven")
			}
			if !e.Disabled && s.Name != "A" && !s.Retrieval {
				t.Error("retrieval not proven")
			}
			if e.Pair == "correction" && s.Name == "B" && (!s.Counterevidence || (!e.Disabled && !s.Correction)) {
				t.Errorf("counterevidence/correction=%+v", s)
			}
			if s.BeforeTask {
				t.Error("held-out oracle accepted prior stub/new stage")
			}
		}
	}
	if b.logical == 0 || b.logical > 96 || b.httpAttempts != 0 {
		t.Fatal(memoryEvalSnapshot(b))
	}
	for _, e := range episodes {
		for _, s := range e.Stages {
			t.Logf("%s disabled=%t stage=%s task=%t capture=%t retrieval=%t application=%t counterevidence=%t correction=%t %s", e.Pair, e.Disabled, s.Name, s.Task, s.Capture, s.Retrieval, s.Application, s.Counterevidence, s.Correction, s.Counters)
		}
	}
}

func TestMemoryEvalChildBoundaries(t *testing.T) {
	home := memoryEvalIsolateProcess(t)
	root, workspace, private := t.TempDir(), t.TempDir(), t.TempDir()
	auth := filepath.Join(private, "auth.json")
	verifier := filepath.Join(private, "held_out_test.go")
	memoryEvalWrite(t, auth, "opaque-child-auth-671")
	memoryEvalWrite(t, verifier, "opaque-child-verifier-672")
	memoryEvalWrite(t, filepath.Join(workspace, "visible.txt"), "opaque-child-visible-673")
	b := &memoryEvalAdmission{}
	b.beginStage(12, time.Now().Add(time.Minute))
	release := make(chan struct{})
	arrived := make(chan struct{})
	adapter := &memoryEvalAdapter{complete: func(ctx context.Context, req llm.Request) (llm.Response, error) {
		close(arrived)
		select {
		case <-release:
			return memoryEvalCall("read_file", map[string]any{"file_path": "visible.txt"}), nil
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
	}}
	c, p := memoryEvalFixtureClient(t, b, adapter)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := NewSession(c, p, memoryEvalLocal(t, home, workspace, t.TempDir()), memoryEvalConfig(ctx, root, false, 12))
	if err != nil {
		t.Fatal(err)
	}
	// Fill the shared round allowance independently of the child's per-input cap.
	for range 12 {
		if err := b.admitToolRound(time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	seen, stop := captureEvents(s)
	result := s.createDelegate(ctx, delegateArgs{Task: "opaque-child-denied-round", AgentType: "explorer", DelegationAllowance: new(0)})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	sub := s.subagents.get(result.ChildSessionID)
	if sub == nil {
		t.Fatal("actual child absent")
	}
	sub.mu.Lock()
	done := sub.done
	sub.mu.Unlock()
	<-arrived
	// Real child's file tool inherits the actual confined environment, not a fake
	// resolver. These calls prove kernel/file grants exclude both private roots.
	for _, path := range []string{auth, verifier} {
		if got, err := sub.sess.env.ReadFile(path, nil, nil); err == nil || strings.Contains(got, "opaque-child-") {
			t.Fatal("child private read not denied")
		}
	}
	shellResult, shellErr := sub.sess.env.ExecCommand(ctx, "cat '"+auth+"' '"+verifier+"'", 10000, workspace, nil)
	if shellErr == nil || shellResult.ExitCode == 0 || strings.Contains(shellResult.Stdout, "opaque-child-") {
		t.Fatal("child shell private read not denied")
	}
	close(release)
	<-done
	cancel()
	stop()
	b.active.Wait()
	for _, ev := range *seen {
		if ev.Kind == events.EventToolCallStart && ev.Data.(events.ToolCallStartData).ToolName == "read_file" {
			t.Fatal("real child delivered tool data over shared round cap")
		}
	}
	if b.logical != 2 || b.toolRounds != 12 { // Child call plus its real auxiliary name call.
		t.Fatalf("child escaped shared admission: %s", memoryEvalSnapshot(b))
	}
}

// Use a Linux-only helper program rather than importing Linux syscalls into
// this cross-platform test package. No mount atime assumption and no product
// hook: every watched fixture file passes an actual IN_OPEN/IN_ACCESS control.
func memoryEvalTrackAmbientReads(t *testing.T, paths []string) func() {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("offline ambient-read observation requires Linux inotify")
	}
	root := t.TempDir()
	path := filepath.Join(root, "watch.go")
	source := `package main
import ("fmt";"os";"syscall")
func main() {
 fd,err:=syscall.InotifyInit1(syscall.IN_NONBLOCK|syscall.IN_CLOEXEC);if err!=nil {panic(err)};defer syscall.Close(fd)
 buffer:=make([]byte,65536)
 for _,path:=range os.Args[1:] {
  if _,err:=syscall.InotifyAddWatch(fd,path,syscall.IN_OPEN|syscall.IN_ACCESS);err!=nil {panic(err)}
  if _,err:=os.ReadFile(path);err!=nil {panic(err)}
  n,err:=syscall.Read(fd,buffer);if err!=nil||n==0 {panic("positive inotify read control failed")}
 }
 fmt.Println("READY")
 var signal [1]byte;os.Stdin.Read(signal[:])
 n,err:=syscall.Read(fd,buffer)
 if err!=nil && err!=syscall.EAGAIN {panic(err)}
 if n>0 {fmt.Fprintln(os.Stderr,"ambient fixture file content was opened or accessed");os.Exit(1)}
}
`
	memoryEvalWrite(t, path, source)
	// TRIPWIRE: readiness and shutdown use the watcher pipe and process exit, two minutes only bounds a wedged offline build or watcher.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	if memoryEvalToolchainError != nil {
		cancel()
		t.Fatal(memoryEvalToolchainError)
	}
	cmd := exec.CommandContext(ctx, filepath.Join(memoryEvalToolchainRoot, "bin", "go"), append([]string{"run", path}, paths...)...)
	cmd.Env = []string{"PATH=" + filepath.Join(memoryEvalToolchainRoot, "bin") + ":/usr/bin:/bin", "GOROOT=" + memoryEvalToolchainRoot, "GOENV=off", "HOME=" + root, "GOCACHE=" + filepath.Join(root, "cache"), "GOTOOLCHAIN=local", "GOPROXY=off", "GOWORK=off", "GOMAXPROCS=2"}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "READY" {
		stdin.Close()
		cmd.Wait()
		cancel()
		t.Fatalf("ambient watcher did not start: %s", stderr.String())
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			stdin.Close()
			err := cmd.Wait()
			cancel()
			if err != nil {
				t.Errorf("ambient read observation failed: %s", stderr.String())
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func TestMemoryEvalObservedUsage(t *testing.T) {
	b := &memoryEvalAdmission{stageCap: 4, deadline: time.Now().Add(time.Minute)}
	cache := 2
	complete := b.middleware(nil).WrapComplete(func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{Usage: llm.Usage{InputTokens: 5, OutputTokens: 3, CacheReadTokens: &cache}}, nil
	})
	if _, err := complete(context.Background(), llm.Request{}); err != nil {
		t.Fatal(err)
	}
	stream := b.middleware(nil).WrapStream(func(context.Context, llm.Request) (llm.Stream, error) {
		s := llm.NewChanStream(nil)
		r := llm.Response{Usage: llm.Usage{InputTokens: 7, OutputTokens: 4}}
		s.Send(llm.StreamEvent{Type: llm.StreamEventFinish, Response: &r})
		s.CloseSend()
		return s, nil
	})
	s, err := stream(context.Background(), llm.Request{})
	if err != nil {
		t.Fatal(err)
	}
	for range s.Events() {
	}
	s.Close()
	b.active.Wait()
	b.beginStage(8, time.Now().Add(time.Minute))
	usage := b.observedUsage()
	if usage.InputTokens != 12 || usage.OutputTokens != 7 || usage.CacheReadTokens == nil || *usage.CacheReadTokens != 2 {
		t.Fatalf("observed usage=%+v", usage)
	}
}
func TestMemoryEvalEvidenceRedaction(t *testing.T) {
	b := &memoryEvalAdmission{sensitive: []string{"opaque-secret-681", "quoted\"secret-682"}}
	path := filepath.Join(t.TempDir(), "evidence.json")
	memoryEvalWriteEvidence(t, b, path, map[string]string{"transcript": "opaque-secret-681 and quoted\"secret-682", "ordinary": "opaque-evidence-683"})
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "681") || strings.Contains(string(raw), "682") || decoded["ordinary"] != "opaque-evidence-683" || decoded["transcript"] != "[redacted] and [redacted]" {
		t.Fatal("evidence redaction failed")
	}
}

func TestMemoryEvalModelGuard(t *testing.T) {
	b := &memoryEvalAdmission{boundModel: true, stageCap: 8, deadline: time.Now().Add(time.Minute)}
	reached := 0
	complete := b.middleware(nil).WrapComplete(func(context.Context, llm.Request) (llm.Response, error) { reached++; return llm.Response{}, nil })
	stream := b.middleware(nil).WrapStream(func(context.Context, llm.Request) (llm.Stream, error) {
		reached++
		s := llm.NewChanStream(nil)
		s.CloseSend()
		return s, nil
	})
	for _, req := range []llm.Request{{Provider: "other", Model: "gpt-6.1-sol"}, {Provider: "codex-jesse-at-pr", Model: "other"}} {
		if _, err := complete(context.Background(), req); err == nil {
			t.Error("unapproved Complete route dispatched")
		}
		s, err := stream(context.Background(), req)
		if err == nil {
			t.Error("unapproved Stream route dispatched")
			s.Close()
		}
	}
	b.active.Wait()
	if reached != 0 || b.logical != 0 {
		t.Fatalf("wrong-route external=%d logical=%d", reached, b.logical)
	}
}

func TestMemoryEvalFailureClassification(t *testing.T) {
	for _, message := range []string{"memory eval deadline reached", "memory eval logical cap reached", "memory eval HTTP cap reached", "memory eval tool round cap reached"} {
		if !memoryEvalBudgetError(fmt.Errorf("wrapped: %s", message)) {
			t.Fatal("budget failure not recognized")
		}
	}
	for _, message := range []string{"memory eval unapproved model route", "memory eval uninstrumented completion route", "provider connection refused"} {
		if memoryEvalBudgetError(fmt.Errorf("wrapped: %s", message)) {
			t.Fatal("infrastructure failure permitted another stage")
		}
	}
}
func TestMemoryEvalVerifierIsolation(t *testing.T) {
	home := memoryEvalIsolateProcess(t)
	workspace, private := t.TempDir(), t.TempDir()
	memoryEvalFixture(t, "recall", workspace)
	secret := filepath.Join(private, "private.go")
	memoryEvalWrite(t, secret, "opaque-verifier-source-decoy-691")
	original := filepath.Join(workspace, "main.go")
	if err := os.Remove(original); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, original); err != nil {
		t.Fatal(err)
	}
	pass, result, err := memoryEvalVerify(context.Background(), t, home, workspace, "A")
	if pass || err == nil || strings.Contains(result, "691") || result != "candidate source unavailable" {
		t.Fatal("verifier followed candidate source symlink")
	}
}

func TestMemoryEvalTerminalFailure(t *testing.T) {
	t.Run("actual-auxiliary-retry", func(t *testing.T) {
		b := &memoryEvalAdmission{stageCap: 20, deadline: time.Now().Add(time.Minute)}
		c, p := memoryEvalFixtureClient(t, b, &memoryEvalAdapter{})
		var calls atomic.Int32
		cause := transientRateLimit429()
		// Override the external adapter only, keeping nameSession's real retry policy.
		c.Register(&memoryEvalFailureAdapter{cause: cause, calls: &calls})
		_, err := nameSession(context.Background(), c, p, sessionNameSourcePrompt, "opaque-terminal-703", "", noNamerSleep)
		b.active.Wait()
		if err == nil || calls.Load() != 1 {
			t.Errorf("auxiliary infrastructure retried, calls=%d error=%v", calls.Load(), err)
		}
		b.beginStage(8, time.Now().Add(time.Minute))
		_, _ = c.Complete(context.Background(), llm.Request{Provider: "codex-jesse-at-pr", Model: "gpt-6.1-sol"})
		if calls.Load() != 1 {
			t.Errorf("next stage dispatched after auxiliary failure, calls=%d", calls.Load())
		}
		var httpCalls int
		tr := &memoryEvalTransport{budget: b, endpoint: "http://fixture.invalid/responses", base: memoryEvalRoundTripFunc(func(*http.Request) (*http.Response, error) { httpCalls++; return nil, cause })}
		req, _ := http.NewRequest(http.MethodPost, tr.endpoint, nil)
		_, _ = tr.RoundTrip(req)
		if httpCalls != 0 {
			t.Error("HTTP dispatched after terminal failure")
		}
		if !errors.Is(b.terminalFailure(), cause) || b.beginStage(8, time.Now().Add(time.Minute)) == nil {
			t.Fatal("joined auxiliary failure lost its cause or admitted next stage")
		}
	})
	for _, mode := range []string{"immediate", "event"} {
		t.Run("stream-"+mode, func(t *testing.T) {
			b := &memoryEvalAdmission{stageCap: 8, deadline: time.Now().Add(time.Minute)}
			cause := errors.New("opaque-stream-infrastructure-704")
			var calls int
			stream := b.middleware(nil).WrapStream(func(context.Context, llm.Request) (llm.Stream, error) {
				calls++
				if mode == "immediate" {
					return nil, cause
				}
				s := llm.NewChanStream(nil)
				s.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: cause})
				s.CloseSend()
				return s, nil
			})
			s, err := stream(context.Background(), llm.Request{})
			if s != nil {
				for range s.Events() {
				}
				s.Close()
			} else if err == nil {
				t.Fatal("missing immediate error")
			}
			b.active.Wait()
			b.beginStage(8, time.Now().Add(time.Minute))
			s, _ = stream(context.Background(), llm.Request{})
			if s != nil {
				for range s.Events() {
				}
				s.Close()
			}
			if calls != 1 {
				t.Errorf("stream failure did not stop subsequent dispatch, calls=%d", calls)
			}
			if !errors.Is(b.terminalFailure(), cause) {
				t.Fatal("stream terminal cause not retained after join")
			}
		})
	}
	t.Run("cancel-admitted-sibling", func(t *testing.T) {
		b := &memoryEvalAdmission{stageCap: 8, deadline: time.Now().Add(time.Minute)}
		entered, settled := make(chan struct{}), make(chan struct{})
		call := b.middleware(nil).WrapComplete(func(ctx context.Context, _ llm.Request) (llm.Response, error) {
			close(entered)
			<-ctx.Done()
			return llm.Response{}, ctx.Err()
		})
		go func() { defer close(settled); _, _ = call(context.Background(), llm.Request{}) }()
		<-entered
		cause := errors.New("opaque-sibling-infrastructure-705")
		failure := b.middleware(nil).WrapComplete(func(context.Context, llm.Request) (llm.Response, error) { return llm.Response{}, cause })
		_, _ = failure(context.Background(), llm.Request{})
		select {
		case <-settled:
		// TRIPWIRE: cancellation wakes the admitted sibling directly, five seconds detects a missing wakeup rather than pacing it.
		case <-time.After(5 * time.Second):
			t.Fatal("terminal failure did not cancel admitted sibling")
		}
		b.active.Wait()
		if !errors.Is(b.terminalFailure(), cause) {
			t.Fatal("sibling context cancellation replaced infrastructure cause")
		}
	})
	t.Run("planned-stops", func(t *testing.T) {
		for _, cause := range []error{context.Canceled, context.DeadlineExceeded, errors.New("memory eval HTTP cap reached")} {
			b := &memoryEvalAdmission{}
			b.fail(cause)
			if b.terminalFailure() != nil || b.beginStage(8, time.Now().Add(time.Minute)) != nil {
				t.Fatal("planned stop classified as infrastructure")
			}
		}
	})
}

type memoryEvalFailureAdapter struct {
	cause error
	calls *atomic.Int32
}

func (*memoryEvalFailureAdapter) Name() string { return "codex-jesse-at-pr" }
func (a *memoryEvalFailureAdapter) Complete(context.Context, llm.Request) (llm.Response, error) {
	a.calls.Add(1)
	return llm.Response{}, a.cause
}
func (*memoryEvalFailureAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func TestMemoryEvalGradeFalsePositives(t *testing.T) {
	old, revised := "From the repository root run sh scripts/check.sh\n", "From the repository root run sh scripts/check.sh --current\n"
	toolEvent := func(name string, args map[string]any, output string, exit int) events.SessionEvent {
		return events.SessionEvent{Kind: events.EventToolCallEnd, Data: events.ToolCallEndData{ToolName: name, ArgumentsJSON: memoryEvalJSON(args), Output: output, ToolState: json.RawMessage(fmt.Sprintf(`{"exit_code":%d}`, exit))}}
	}
	read := func(output string) events.SessionEvent {
		return toolEvent("memory_read", map[string]any{"scope": "project", "file_path": "MEMORY.md"}, output, 0)
	}
	write := func(path, body string) events.SessionEvent {
		return toolEvent("memory_write", map[string]any{"scope": "project", "file_path": path, "content": body}, "", 0)
	}
	shell := func(command, output string, exit int) events.SessionEvent {
		return toolEvent("shell", map[string]any{"command": command, "cwd": "/fixture"}, output, exit)
	}
	finish := events.SessionEvent{Kind: events.EventCommunicate, Data: events.CommunicateData{EndTurn: true}}
	for _, tc := range []struct {
		name    string
		trace   []events.SessionEvent
		after   map[string]string
		grade   string
		current bool
	}{
		{"unrelated-read", []events.SessionEvent{read("opaque-unrelated-note"), shell("sh scripts/check.sh", "ok\tmemoryfixture\t0.1s", 0)}, map[string]string{}, "retrieval", false},
		{"empty-read", []events.SessionEvent{read(""), shell("sh scripts/check.sh", "ok\tmemoryfixture\t0.1s", 0)}, map[string]string{}, "retrieval", false},
		{"printf-checker", []events.SessionEvent{shell("printf '%s\\n' 'scripts/check.sh'", "scripts/check.sh", 0)}, map[string]string{}, "application", false},
		{"hidden-failed-checker", []events.SessionEvent{shell("sh scripts/check.sh --current || true", "FAIL", 0)}, map[string]string{}, "application", true},
		{"correction-before-counterevidence", []events.SessionEvent{write("MEMORY.md", revised), shell("sh scripts/check.sh", "Checker now requires --current", 2), write("notes.md", "opaque-unrelated-note"), finish}, map[string]string{"projects/fixture-project/MEMORY.md": revised}, "correction", true},
		{"incomplete-root-capture", []events.SessionEvent{write("MEMORY.md", "sh scripts/check.sh\n")}, map[string]string{"projects/fixture-project/MEMORY.md": "sh scripts/check.sh\n"}, "capture", false},
		{"unrelated-capture-write", []events.SessionEvent{write("notes.md", "opaque-unrelated-note")}, map[string]string{"projects/fixture-project/MEMORY.md": old}, "capture", false},
		{"successful-counterevidence-text", []events.SessionEvent{shell("sh scripts/check.sh", "Checker now requires --current", 0)}, map[string]string{}, "counterevidence", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := memoryEvalStageEvidence{Trace: tc.trace, Files: map[string]string{"workspace": "/fixture"}, WikiBefore: map[string]string{"projects/fixture-project/MEMORY.md": old}, WikiAfter: tc.after}
			body := old
			for _, ev := range tc.trace {
				if ev.Kind != events.EventToolCallEnd {
					continue
				}
				d := ev.Data.(events.ToolCallEndData)
				var args map[string]any
				_ = json.Unmarshal([]byte(d.ArgumentsJSON), &args)
				obs := memoryEvalToolObservation{ToolName: d.ToolName, Arguments: memoryEvalObservationKey(d.ArgumentsJSON)}
				if d.ToolName == "shell" {
					obs.Checker = memoryEvalCheckerSource(tc.current)
				}
				if d.ToolName == "memory_write" {
					obs.After, _ = args["content"].(string)
					if args["file_path"] == "MEMORY.md" {
						obs.Before = body
						body = obs.After
					}
				}
				e.Observations = append(e.Observations, obs)
			}
			memoryEvalGrade(&e, tc.current)
			positive := map[string]bool{"retrieval": e.Retrieval, "application": e.Application, "correction": e.Correction, "capture": e.Capture, "counterevidence": e.Counterevidence}[tc.grade]
			if positive {
				t.Fatalf("false positive %s from unsupported evidence", tc.grade)
			}
			if !slices.Contains(e.Unproven, tc.grade) {
				t.Fatal("unsupported grade not marked unproven")
			}
			if tc.grade == "retrieval" && !e.Application {
				t.Fatal("retrieval negative lost its independent valid application")
			}
		})
	}
}

func TestMemoryEvalCheckerOracle(t *testing.T) {
	for _, pair := range []string{"recall", "correction"} {
		raw, err := os.ReadFile(filepath.Join("testdata", "memory-eval", pair, "task", "scripts", "check.sh"))
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != memoryEvalCheckerSource(false) {
			t.Fatal("checker oracle differs from approved fixture bytes")
		}
	}
}

func TestMemoryEvalGradeCompletion(t *testing.T) {
	old, newRule := "From the repository root run sh scripts/check.sh\n", "From the repository root run sh scripts/check.sh --current\n"
	for _, completion := range []events.SessionEvent{{Kind: events.EventCommunicate, Data: events.CommunicateData{EndTurn: true}}, {Kind: events.EventAssistantTextEnd, Data: events.AssistantTextEndData{Text: "opaque-final-706"}}} {
		e := memoryEvalStageEvidence{Files: map[string]string{"workspace": "/fixture"}, WikiBefore: map[string]string{"lesson": old}, WikiAfter: map[string]string{"lesson": newRule}}
		add := func(name string, args map[string]any, output string, exit int, obs memoryEvalToolObservation) {
			e.Trace = append(e.Trace, events.SessionEvent{Kind: events.EventAssistantTextEnd, Data: events.AssistantTextEndData{}}, events.SessionEvent{Kind: events.EventToolCallEnd, Data: events.ToolCallEndData{ToolName: name, ArgumentsJSON: memoryEvalJSON(args), Output: output, ToolState: json.RawMessage(fmt.Sprintf(`{"exit_code":%d}`, exit))}})
			obs.ToolName, obs.Arguments = name, memoryEvalJSON(args)
			e.Observations = append(e.Observations, obs)
		}
		add("memory_read", map[string]any{"file_path": "MEMORY.md", "scope": "project"}, old, 0, memoryEvalToolObservation{})
		add("shell", map[string]any{"command": "sh scripts/check.sh"}, "Checker now requires --current", 2, memoryEvalToolObservation{Checker: memoryEvalCheckerSource(true)})
		add("memory_edit", map[string]any{"file_path": "MEMORY.md", "scope": "project", "old_string": "sh scripts/check.sh", "new_string": "sh scripts/check.sh --current"}, "", 0, memoryEvalToolObservation{Before: old, After: newRule})
		add("shell", map[string]any{"command": "sh scripts/check.sh --current"}, "ok\tmemoryfixture\t0.1s", 0, memoryEvalToolObservation{Checker: memoryEvalCheckerSource(true)})
		e.Trace = append(e.Trace, completion)
		memoryEvalGrade(&e, true)
		if !e.Retrieval || !e.Application || !e.Counterevidence || !e.Correction {
			t.Fatalf("supported chronology not proven: %+v", e.Unproven)
		}
	}
}

func TestMemoryEvalPrerequisites(t *testing.T) {
	for _, mode := range []string{"offline-darwin", "offline-no-bwrap", "live-darwin", "live-no-bwrap", "available"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestMemoryEvalPrerequisiteProbe$", "-test.v")
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "EVENER_LIVE_TESTS=") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, "MEMORY_EVAL_PREREQUISITE_MODE="+mode)
			out, err := cmd.CombinedOutput()
			t.Log(string(out))
			if strings.HasPrefix(mode, "live-") {
				if err == nil || strings.Contains(string(out), "qualified-before-discovery") {
					t.Fatal("unsupported live prerequisite did not refuse before discovery")
				}
			} else if err != nil {
				t.Fatalf("offline unavailable qualification failed package: %v", err)
			} else if mode == "available" {
				if !strings.Contains(string(out), "qualified-before-discovery") {
					t.Fatal("available qualification skipped")
				}
			} else if !strings.Contains(string(out), "--- SKIP:") {
				t.Fatal("offline unsupported qualification not skipped")
			}
		})
	}
}

func TestMemoryEvalPrerequisiteProbe(t *testing.T) {
	mode := os.Getenv("MEMORY_EVAL_PREREQUISITE_MODE")
	if mode == "" {
		t.Skip("fixture subprocess only")
	}
	facts := sandbox.HostFacts{OS: "linux", BwrapCapable: true, BwrapPath: "/fixture/bwrap"}
	if strings.HasSuffix(mode, "darwin") {
		facts.OS = "darwin"
	}
	if strings.HasSuffix(mode, "no-bwrap") {
		facts.BwrapCapable = false
	}
	if strings.HasPrefix(mode, "live-") {
		memoryEvalLivePairsOnHost(t, func() (string, string, error) {
			t.Fatal("qualified-before-discovery, unexpected source discovery")
			return "", "", nil
		}, facts)
	} else {
		memoryEvalRequireHost(t, false, facts)
	}
	t.Log("qualified-before-discovery")
}
