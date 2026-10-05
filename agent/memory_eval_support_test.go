package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/responses"
	"primeradiant.com/evener/llm/providers/tokenauth"
	"primeradiant.com/evener/llm/registry"
)

type memoryEvalStage struct {
	name string
	cap  int
}

var memoryRecallStages = []memoryEvalStage{{"A", 8}, {"B", 10}}
var memoryCorrectionStages = []memoryEvalStage{{"A", 8}, {"B", 12}, {"C", 10}}

// Capture before TestMain replaces HOME. Trimpath removes the compiled-in root,
// and a directly launched installed Go does not export GOROOT to its tests.
// Let Go resolve the exact compiling version offline, not a guessed cache path.
var memoryEvalToolchainRoot, memoryEvalToolchainError = memoryEvalCaptureToolchain()

func memoryEvalCaptureToolchain() (string, error) {
	// TRIPWIRE: offline go env normally returns immediately, twenty seconds only detects a wedged process.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	goBinary, selection := "go", runtime.Version()
	if root := runtime.GOROOT(); root != "" { //nolint:staticcheck // SA1019: this local verifier intentionally selects its compiling Go installation, not a relocated binary's PATH toolchain.
		goBinary, selection = filepath.Join(root, "bin", "go"), "local"
	}
	cmd := exec.CommandContext(ctx, goBinary, "env", "GOROOT", "GOVERSION")
	cmd.Env = []string{"GOENV=off", "GOTOOLCHAIN=" + selection, "GOPROXY=off", "GOSUMDB=off", "GOWORK=off"}
	for _, key := range []string{"PATH", "HOME", "GOPATH", "GOMODCACHE", "GOCACHE"} {
		if value, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("memory eval exact offline Go resolution failed: %w", err)
	}
	fields := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(fields) != 2 || !filepath.IsAbs(fields[0]) || fields[1] != runtime.Version() {
		return "", errors.New("memory eval offline Go does not match compiling toolchain")
	}
	return fields[0], nil
}

type memoryEvalAdmission struct {
	mu                                                                   sync.Mutex
	logical, httpAttempts, stageLogical, stageHTTP, toolRounds, stageCap int
	deadline                                                             time.Time
	active                                                               sync.WaitGroup
	runStart                                                             time.Time
	evidenceRoot                                                         string
	usage                                                                llm.Usage
	authRoot                                                             string
	sensitive                                                            []string
	boundModel                                                           bool
	failure                                                              error
	runContext                                                           context.Context
	runCancel                                                            context.CancelFunc
}

func (b *memoryEvalAdmission) terminalFailure() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.failure
}

func (b *memoryEvalAdmission) fail(err error) {
	if err == nil || memoryEvalBudgetError(err) || errors.Is(err, llm.ErrStreamUnsupported) {
		return
	}
	b.mu.Lock()
	if b.failure == nil {
		b.failure = err
	}
	cancel := b.runCancel
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (b *memoryEvalAdmission) bindContext(parent context.Context) (context.Context, context.CancelFunc) {
	b.mu.Lock()
	if b.runContext == nil {
		b.runContext, b.runCancel = context.WithCancel(context.Background())
	}
	run := b.runContext
	failed := b.failure != nil
	b.mu.Unlock()
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(run, cancel)
	if failed {
		cancel()
	}
	return ctx, func() { stop(); cancel() }
}

func (b *memoryEvalAdmission) admit(httpAttempt bool, now time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failure != nil {
		return b.failure
	}
	if !now.Before(b.deadline) {
		return errors.New("memory eval deadline reached")
	}
	if httpAttempt {
		if b.httpAttempts >= 96 || b.stageHTTP >= b.stageCap {
			return errors.New("memory eval HTTP cap reached")
		}
		b.httpAttempts++
		b.stageHTTP++
	} else {
		if b.logical >= 96 || b.stageLogical >= b.stageCap {
			return errors.New("memory eval logical cap reached")
		}
		b.logical++
		b.stageLogical++
	}
	return nil
}
func (b *memoryEvalAdmission) admitToolRound(now time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failure != nil {
		return b.failure
	}
	if !now.Before(b.deadline) {
		return errors.New("memory eval deadline reached")
	}
	if b.toolRounds >= b.stageCap {
		return errors.New("memory eval tool round cap reached")
	}
	b.toolRounds++
	return nil
}

// The caller must cancel, close and join the preceding stage before this reset.
func (b *memoryEvalAdmission) beginStage(stageCap int, deadline time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failure != nil {
		return b.failure
	}
	b.stageLogical, b.stageHTTP, b.toolRounds, b.stageCap, b.deadline = 0, 0, 0, stageCap, deadline
	return nil
}
func (b *memoryEvalAdmission) middleware(auth llm.Authenticator) llm.MiddlewareFunc {
	return llm.MiddlewareFunc{
		Complete: func(ctx context.Context, req llm.Request, next llm.CompleteFunc) (llm.Response, error) {
			if err := b.terminalFailure(); err != nil {
				return llm.Response{}, err
			}
			ctx, cancel := b.bindContext(ctx)
			defer cancel()
			if err := ctx.Err(); err != nil {
				return llm.Response{}, err
			}
			if err := b.approvedRequest(req); err != nil {
				b.fail(err)
				return llm.Response{}, err
			}
			if err := b.admit(false, time.Now()); err != nil {
				return llm.Response{}, err
			}
			b.active.Add(1)
			defer b.active.Done()
			resp, err := next(llm.WithAuthenticatorOverride(ctx, auth), req)
			b.fail(err)
			if err == nil {
				b.observeUsage(resp.Usage)
			}
			if err == nil && len(resp.ToolCalls()) > 0 {
				err = b.admitToolRound(time.Now())
			}
			if err != nil {
				return llm.Response{}, err
			}
			return resp, nil
		},
		Stream: func(ctx context.Context, req llm.Request, next llm.StreamFunc) (llm.Stream, error) {
			if err := b.terminalFailure(); err != nil {
				return nil, err
			}
			ctx, cancel := b.bindContext(llm.WithAuthenticatorOverride(ctx, auth))
			if err := ctx.Err(); err != nil {
				cancel()
				return nil, err
			}
			if err := b.approvedRequest(req); err != nil {
				b.fail(err)
				cancel()
				return nil, err
			}
			if err := b.admit(false, time.Now()); err != nil {
				cancel()
				return nil, err
			}
			b.active.Add(1)
			original, err := next(ctx, req)
			if err != nil {
				b.fail(err)
				cancel()
				b.active.Done()
				return nil, err
			}
			s := &memoryEvalStream{original: original, cancel: cancel, events: make(chan llm.StreamEvent), done: make(chan struct{})}
			go func() {
				defer b.active.Done()
				defer close(s.done)
				defer close(s.events)
				defer original.Close()
				defer cancel()
				admitted := false
				usageObserved := false
				for {
					select {
					case <-ctx.Done():
						return
					case ev, ok := <-original.Events():
						if !ok {
							return
						}
						if ev.Err != nil {
							b.fail(ev.Err)
						}
						if !usageObserved && ev.Type == llm.StreamEventFinish && ev.Response != nil {
							b.observeUsage(ev.Response.Usage)
							usageObserved = true
						}
						if !admitted && (ev.ToolCall != nil || (ev.Response != nil && len(ev.Response.ToolCalls()) > 0)) {
							if err := b.admitToolRound(time.Now()); err != nil {
								original.Close()
								select {
								case s.events <- llm.StreamEvent{Type: llm.StreamEventError, Err: err}:
								case <-ctx.Done():
								}
								return
							}
							admitted = true
						}
						select {
						case s.events <- ev:
						case <-ctx.Done():
							return
						}
					}
				}
			}()
			return s, nil
		},
	}
}

type memoryEvalStream struct {
	original llm.Stream
	cancel   context.CancelFunc
	events   chan llm.StreamEvent
	done     chan struct{}
}

func (s *memoryEvalStream) Events() <-chan llm.StreamEvent { return s.events }
func (s *memoryEvalStream) Close() error                   { s.cancel(); err := s.original.Close(); <-s.done; return err }

type memoryEvalTransport struct {
	budget   *memoryEvalAdmission
	base     http.RoundTripper
	endpoint string
}

func (r *memoryEvalTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodPost || req.URL.String() != r.endpoint {
		err := errors.New("memory eval uninstrumented completion route")
		r.budget.fail(err)
		return nil, err
	}
	if err := r.budget.admit(true, time.Now()); err != nil {
		return nil, err
	}
	resp, err := r.base.RoundTrip(req)
	r.budget.fail(err)
	return resp, err
}

const memoryEvalModel = "codex-jesse-at-pr/gpt-6.1-sol"

// No ambient registry, user docs, plugin/MCP config, shell variables or wiki roots.
func memoryEvalIsolateProcess(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(key, filepath.Join(home, key))
	}
	for _, key := range []string{"EVENER_PROVIDERS_CONFIG", "EVENER_CREDENTIALS_CONFIG"} {
		t.Setenv(key, "")
	}
	return home
}
func memoryEvalRegistry(t *testing.T, root, config string) *registry.Registry {
	t.Helper()
	r, err := registry.Load(registry.WithStateRoot(root), registry.WithConfigPath(config), registry.WithOffline(true), registry.WithoutCache(), registry.WithEnv(func(string) (string, bool) { return "", false }))
	if err != nil {
		t.Fatal("memory eval fixture registry failed")
	}
	return r
}
func memoryEvalLocal(t *testing.T, home, workspace, scratch string) *execenv.LocalExecutionEnvironment {
	t.Helper()
	facts := sandbox.RealProber{}.Probe()
	memoryEvalRequireHost(t, false, facts)
	facts.Home = home
	net := false
	if memoryEvalToolchainError != nil {
		t.Fatal(memoryEvalToolchainError)
	}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted, Network: &net, ExtraReadRoots: []string{memoryEvalToolchainRoot}}, facts, workspace)
	if err != nil {
		t.Fatal(err)
	}
	w, err := sandbox.NewWrapper(rp, facts.BwrapPath, scratch)
	if err != nil {
		t.Fatal(err)
	}
	env := execenv.NewLocalExecutionEnvironment(workspace)
	env.EnvPolicy = execenv.EnvPolicyNone
	env.Sandbox = &rp
	env.Wrapper = w
	return env
}

func memoryEvalRequireHost(t *testing.T, live bool, facts sandbox.HostFacts) {
	t.Helper()
	if facts.OS != "linux" || !facts.BwrapCapable || facts.BwrapPath == "" {
		if !live {
			t.Skip("offline memory isolation qualification requires working Linux bwrap")
		}
		t.Fatal("memory eval requires working Linux bwrap, no isolation fallback")
	}
}
func memoryEvalConfig(ctx context.Context, root string, disabled bool, stageCap int) SessionConfig {
	return SessionConfig{LifetimeContext: ctx, LLMRetryPolicy: &llm.RetryPolicy{MaxRetries: 0}, MemoryStateRoot: filepath.Join(root, "wiki"), MemoryProjectID: "fixture-project", DisableMemory: disabled, StateDir: filepath.Join(root, "history"), AgentsDocPath: filepath.Join(root, "no-user-AGENTS.md"), ReasoningEffort: "high", NonInteractive: true, MaxToolRoundsPerInput: stageCap, MaxSubagentDepth: 1, TurnEndsProcess: true, VisionModel: "off", DefaultCommandTimeoutMS: 10000, MaxCommandTimeoutMS: 180000}
}
func memoryEvalWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}
func memoryEvalFixtureClient(t *testing.T, b *memoryEvalAdmission, adapter llm.ProviderAdapter) (*llm.Client, *provider.Profile) {
	t.Helper()
	b.boundModel = true
	root := t.TempDir()
	config := filepath.Join(root, "providers.toml")
	memoryEvalWrite(t, config, "[providers.codex-jesse-at-pr]\nbase = \"openai-codex\"\n")
	// Registry's OAuth existence gate sees only this decoy, never operator auth.
	memoryEvalWrite(t, filepath.Join(root, "auth", "codex-jesse-at-pr.json"), "opaque-fixture-auth-601")
	r := memoryEvalRegistry(t, root, config)
	p, err := provider.Resolve(r, memoryEvalModel)
	if err != nil {
		t.Fatal(err)
	}
	p = provider.WithCheapModel(p, memoryEvalModel)
	c := llm.NewClient(llm.WithRegistry(r), llm.WithClientStateDir(root))
	c.Register(adapter)
	c.Use(b.middleware(tokenauth.ScopedCodex(root)))
	old := responses.DefaultProtocol.Client
	responses.DefaultProtocol.Client = &http.Client{Transport: memoryEvalRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("scripted memory evaluator attempted completion HTTP")
		return nil, errors.New("offline memory evaluator forbids completion transport")
	})}
	t.Cleanup(func() { b.active.Wait(); responses.DefaultProtocol.Client = old })
	return c, p
}

type memoryEvalAdapter struct {
	complete llm.CompleteFunc
	stream   llm.StreamFunc
}

func (*memoryEvalAdapter) Name() string { return "codex-jesse-at-pr" }
func (a *memoryEvalAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if len(req.Tools) == 0 {
		return llm.Response{Message: llm.Assistant(`{"name":"Fixture"}`)}, nil
	}
	return a.complete(ctx, req)
}
func (a *memoryEvalAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	if a.stream != nil {
		return a.stream(ctx, req)
	}
	resp, err := a.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	s := llm.NewChanStream(nil)
	s.Send(llm.StreamEvent{Type: llm.StreamEventFinish, Response: &resp})
	s.CloseSend()
	return s, nil
}

func memoryEvalDeadline(stage, arm, run time.Time) time.Time {
	d := stage.Add(3 * time.Minute)
	for _, limit := range []time.Time{arm.Add(6 * time.Minute), run.Add(24 * time.Minute)} {
		if limit.Before(d) {
			d = limit
		}
	}
	return d
}

func memoryEvalSnapshot(b *memoryEvalAdmission) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return fmt.Sprintf("logical=%d http=%d stageLogical=%d stageHTTP=%d toolRounds=%d", b.logical, b.httpAttempts, b.stageLogical, b.stageHTTP, b.toolRounds)
}

// Evidence lives outside every agent grant. Bool grades mean supported by these
// narrow executable/tool/file oracles, not a model's statement of learning.
type memoryEvalStageEvidence struct {
	Name                                                                           string
	SessionID, PriorSessionID                                                      string
	Task, BeforeTask, Capture, Retrieval, Application, Counterevidence, Correction bool
	Verifier                                                                       string
	Files, WikiBefore, WikiAfter                                                   map[string]string
	Trace                                                                          []events.SessionEvent
	Metrics                                                                        evalMetrics
	Observations                                                                   []memoryEvalToolObservation
	Unproven                                                                       []string
	Counters                                                                       string
	Elapsed                                                                        time.Duration
	Limitation                                                                     string
	Transcript                                                                     string
}
type memoryEvalEpisode struct {
	Pair     string
	Disabled bool
	Stages   []memoryEvalStageEvidence
	Elapsed  time.Duration
}

func memoryEvalJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(raw)
}
func memoryEvalGoEnv() string {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	return fmt.Sprintf("PATH=%s GOROOT=%s GOENV=off HOME=/tmp GOCACHE=/tmp/memory-go-cache GOMODCACHE=/tmp/memory-go-mod GOTOOLCHAIN=local GOPROXY=off GOWORK=off GOMAXPROCS=2 ", quote(filepath.Join(memoryEvalToolchainRoot, "bin")+":/usr/bin:/bin"), quote(memoryEvalToolchainRoot))
}
func memoryEvalFixture(t *testing.T, pair, workspace string) {
	t.Helper()
	for _, name := range []string{"main.go", "go.mod", "scripts/check.sh"} {
		raw, err := os.ReadFile(filepath.Join("testdata", "memory-eval", pair, "task", name))
		if err != nil {
			t.Fatal(err)
		}
		memoryEvalWrite(t, filepath.Join(workspace, name), string(raw))
	}
}
func memoryEvalVisible(stage string) string {
	s := `package main
import ("testing"; "reflect")
func TestNormalizeVisible(t *testing.T) { if got:=normalize("  visible alpha \n"); got!="visible alpha" { t.Fatalf("normalize=%q",got) } }
var _ = reflect.DeepEqual
`
	if stage != "A" {
		s += `func TestSplitVisible(t *testing.T) { if got:=split(" one , two "); !reflect.DeepEqual(got,[]string{"one","two"}) { t.Fatalf("split=%q",got) } }
`
	}
	if stage == "C" {
		s += `func TestJoinVisible(t *testing.T) { if got:=join([]string{" one "," two "}); got!="one,two" { t.Fatalf("join=%q",got) } }
`
	}
	return s
}
func memoryEvalHeldOut(stage string) string {
	s := `package main
import ("testing"; "reflect")
func TestNormalizeHeldOut(t *testing.T) {
 for _,c:=range []struct{in,want string}{{"\t\u2003opaque held 647\r\n","opaque held 647"},{" \n\t",""},{"inner  gap","inner  gap"}} {
  if got:=normalize(c.in); got!=c.want { t.Fatalf("normalize(%q)=%q want %q",c.in,got,c.want) }
 }
}
var _ = reflect.DeepEqual
`
	if stage != "A" {
		s += `func TestSplitHeldOut(t *testing.T) {
 for _,c:=range []struct{in string;want []string}{{" \tq , ,\n r\u2003,",[]string{"q","","r",""}},{"",[]string{""}},{"u  v",[]string{"u  v"}}} {
  if got:=split(c.in); !reflect.DeepEqual(got,c.want) { t.Fatalf("split(%q)=%q want %q",c.in,got,c.want) }
 }
}
`
	}
	if stage == "C" {
		s += `func TestJoinHeldOut(t *testing.T) {
 for _,c:=range []struct{in []string;want string}{{[]string{"\u2003 q\t","", " r  s\n"},"q,,r  s"},{nil,""},{[]string{" \t"},""}} {
  if got:=join(c.in); got!=c.want { t.Fatalf("join(%q)=%q want %q",c.in,got,c.want) }
 }
}
`
	}
	return s
}
func memoryEvalVerify(ctx context.Context, t *testing.T, home, workspace, stage string) (bool, string, error) {
	t.Helper()
	verifier, scratch := memoryEvalDisposable(t), memoryEvalDisposable(t)
	// Confined file reads reject candidate symlinks. Copy only production source,
	// never agent-written tests, which cannot replace the verifier's assertions.
	sourceEnv := memoryEvalLocal(t, home, workspace, t.TempDir())
	defer sourceEnv.Cleanup()
	for _, name := range []string{"main.go", "go.mod"} {
		raw, err := sourceEnv.ReadFileRaw(name)
		if err != nil {
			return false, "candidate source unavailable", errors.New("memory eval verifier candidate source unavailable")
		}
		memoryEvalWrite(t, filepath.Join(verifier, name), string(raw))
	}
	memoryEvalWrite(t, filepath.Join(verifier, "held_out_test.go"), memoryEvalHeldOut(stage))
	env := memoryEvalLocal(t, home, verifier, scratch)
	defer env.Cleanup()
	result, err := env.ExecCommand(ctx, memoryEvalGoEnv()+"go test -json -count=1 ./...", 180000, verifier, nil)
	if result.TimedOut || ctx.Err() != nil {
		return false, "held-out verifier deadline exhausted", nil
	}
	output := result.Stdout + result.Stderr
	if (err != nil && result.ExitCode != 1) || (result.ExitCode != 0 && result.ExitCode != 1) {
		return false, output, errors.New("memory eval verifier execution infrastructure failure")
	}
	passed, evidenceErr := memoryEvalVerifierResult(result.Stdout, result.ExitCode, stage)
	return passed, output, evidenceErr
}

func memoryEvalVerifierResult(output string, exit int, stage string) (bool, error) {
	names := []string{"TestNormalizeHeldOut"}
	if stage != "A" {
		names = append(names, "TestSplitHeldOut")
	}
	if stage == "C" {
		names = append(names, "TestJoinHeldOut")
	}
	states := make(map[string]string, len(names))
	for _, name := range names {
		states[name] = ""
	}
	failure := errors.New("memory eval verifier missing complete held-out execution evidence")
	decoder := json.NewDecoder(strings.NewReader(output))
	packageResult := ""
	for {
		var event struct{ Action, Package, Test string }
		if err := decoder.Decode(&event); err != nil {
			if err != io.EOF {
				return false, failure
			}
			break
		}
		if event.Package != "memoryfixture" {
			continue
		}
		if event.Test == "" {
			if event.Action == "pass" || event.Action == "fail" {
				if packageResult != "" {
					return false, failure
				}
				packageResult = event.Action
			}
			continue
		}
		state, expected := states[event.Test]
		if !expected {
			continue
		}
		switch event.Action {
		case "run":
			if state != "" {
				return false, failure
			}
			states[event.Test] = "run"
		case "pass", "fail":
			if state != "run" {
				return false, failure
			}
			states[event.Test] = event.Action
		}
	}
	passed := true
	for _, state := range states {
		if state != "pass" && state != "fail" {
			return false, failure
		}
		passed = passed && state == "pass"
	}
	if (passed && (exit != 0 || packageResult != "pass")) || (!passed && (exit != 1 || packageResult != "fail")) {
		return false, failure
	}
	return passed, nil
}
func memoryEvalWiki(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("memory eval wiki symlink refused")
		}
		if d.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out[rel] = string(raw)
		return nil
	})
	if err != nil {
		t.Fatal("memory eval evidence read failed")
	}
	return out
}
func memoryEvalCheckerInWiki(wiki map[string]string, current bool) bool {
	// A supported literal invocation is sufficient evidence, not a mandatory wiki
	// schema. Unrecognized phrasing remains unproven and needs human review.
	for _, body := range wiki {
		for line := range strings.SplitSeq(body, "\n") {
			if strings.Contains(line, "repository root") && strings.Contains(line, "sh scripts/check.sh") && strings.Contains(line, "--current") == current {
				return true
			}
		}
	}
	return false
}
func memoryEvalGrade(e *memoryEvalStageEvidence, current bool) {
	e.Capture, e.Retrieval, e.Application, e.Counterevidence, e.Correction = false, false, false, false, false
	e.Unproven = nil
	appliedAt, readAt, failedAt, correctedAt, completedAt := -1, -1, -1, -1, len(e.Trace)
	failedObservation, correctedObservation := -1, -1
	for i, ev := range e.Trace {
		if ev.Kind == events.EventAssistantTextEnd {
			data, ok := ev.Data.(events.AssistantTextEndData)
			terminal := ok && data.Text != ""
			for _, later := range e.Trace[i+1:] {
				if later.Kind == events.EventRoundEnded {
					break
				}
				if later.Kind == events.EventToolCallStart {
					terminal = false
					break
				}
			}
			if terminal && completedAt == len(e.Trace) {
				completedAt = i
			}
		}
		if ev.Kind == events.EventCommunicate {
			d := ev.Data.(events.CommunicateData)
			if d.EndTurn {
				if completedAt == len(e.Trace) {
					completedAt = i
				}
			}
		}
		if ev.Kind != events.EventToolCallEnd {
			continue
		}
		d := ev.Data.(events.ToolCallEndData)
		if readAt < 0 && (d.ToolName == "memory_read" || d.ToolName == "memory_search") && d.Error == "" && (memoryEvalCheckerInWiki(map[string]string{"returned": d.Output}, current) || (current && memoryEvalCheckerInWiki(map[string]string{"returned": d.Output}, false))) {
			readAt = i
		}
		obs, ordinal := memoryEvalObservationFor(e.Observations, d)
		if ordinal >= 0 && d.Error == "" && (d.ToolName == "memory_write" || d.ToolName == "memory_edit") {
			if memoryEvalCheckerInWiki(map[string]string{"committed": obs.After}, false) {
				e.Capture = true
			}
			if memoryEvalCheckerInWiki(map[string]string{"before": obs.Before}, false) && memoryEvalCheckerInWiki(map[string]string{"after": obs.After}, true) && !memoryEvalCheckerInWiki(map[string]string{"after": obs.After}, false) {
				correctedAt, correctedObservation = i, ordinal
			}
		}
		if d.ToolName == "shell" {
			var args struct{ Command, Cwd string }
			_ = json.Unmarshal([]byte(d.ArgumentsJSON), &args)
			var state struct {
				ExitCode *int `json:"exit_code"`
			}
			_ = json.Unmarshal(d.ToolState, &state)
			root := args.Cwd == "" || args.Cwd == e.Files["workspace"]
			command := strings.TrimPrefix(args.Command, memoryEvalGoEnv())
			oldInvocation := command == "sh scripts/check.sh"
			newInvocation := command == "sh scripts/check.sh --current"
			verified := ordinal >= 0 && root && obs.Checker == memoryEvalCheckerSource(current)
			if verified && current && oldInvocation && state.ExitCode != nil && *state.ExitCode == 2 && strings.Contains(d.Output, "Checker now requires --current") {
				failedAt, failedObservation = i, ordinal
				e.Counterevidence = true
			}
			if verified && ((oldInvocation && !current) || (newInvocation && current)) && d.Error == "" && state.ExitCode != nil && *state.ExitCode == 0 && strings.Contains(d.Output, "ok") && strings.Contains(d.Output, "memoryfixture") {
				appliedAt = i
				e.Application = true
			}
		}
	}
	e.Retrieval = readAt >= 0 && appliedAt > readAt
	e.Correction = completedAt < len(e.Trace) && failedAt >= 0 && correctedAt > failedAt && correctedAt < completedAt && correctedObservation > failedObservation && memoryEvalCheckerInWiki(e.WikiBefore, false) && memoryEvalCheckerInWiki(e.WikiAfter, true) && !memoryEvalCheckerInWiki(e.WikiAfter, false)
	for _, grade := range []struct {
		name   string
		proven bool
	}{{"capture", e.Capture}, {"retrieval", e.Retrieval}, {"application", e.Application}, {"counterevidence", e.Counterevidence}, {"correction", e.Correction}} {
		if !grade.proven {
			e.Unproven = append(e.Unproven, grade.name)
		}
	}
}

func memoryEvalCheckerSource(current bool) string {
	return "#!/bin/sh\n# Initial scripts/check.sh in both fixtures, invoked from the fixture root.\nset -eu\ntest -f go.mod || { printf '%s\\n' 'Run this checker from the repository root' >&2; exit 2; }\n" + memoryEvalCheckerCondition(current) + "\nexec go test ./...\n"
}

func memoryEvalCheckerCondition(current bool) string {
	if current {
		return `test "$#" -eq 1 && test "$1" = --current || { printf '%s\n' 'Checker now requires --current' >&2; exit 2; }`
	}
	return `test "$#" -eq 0 || { printf '%s\n' 'This checker takes no arguments' >&2; exit 2; }`
}

type memoryEvalToolObservation struct {
	ToolName, Arguments, Before, After, Checker string
}

func memoryEvalObservationKey(raw string) string {
	var args map[string]any
	if json.Unmarshal([]byte(raw), &args) != nil {
		return ""
	}
	delete(args, "intent")
	return memoryEvalJSON(args)
}

func memoryEvalObservationFor(observations []memoryEvalToolObservation, d events.ToolCallEndData) (memoryEvalToolObservation, int) {
	key, found := memoryEvalObservationKey(d.ArgumentsJSON), -1
	for i, obs := range observations {
		if obs.ToolName == d.ToolName && obs.Arguments == key {
			if found >= 0 {
				return memoryEvalToolObservation{}, -1
			} // Ambiguous repeated calls stay unproven.
			found = i
		}
	}
	if found < 0 {
		return memoryEvalToolObservation{}, -1
	}
	return observations[found], found
}

// Observe real executors, without replacing their outputs. Serialized observed
// root operations give snapshots at the specific commit, not a later final wiki.
// Unobserved descendants and ambiguous repeated calls remain unproven.
func memoryEvalObserveTools(t *testing.T, s *Session) func() []memoryEvalToolObservation {
	t.Helper()
	var mu sync.Mutex
	var observations []memoryEvalToolObservation
	for _, name := range []string{"memory_write", "memory_edit", "shell"} {
		registered := s.reg.Get(name)
		if registered == nil {
			continue
		}
		wrapped := *registered
		execute := wrapped.Exec
		wrapped.Execute = nil
		wrapped.Exec = func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			mu.Lock()
			defer mu.Unlock()
			obs := memoryEvalToolObservation{ToolName: name, Arguments: memoryEvalObservationKey(memoryEvalJSON(args))}
			var memoryEnv *execenv.LocalExecutionEnvironment
			path, _ := args["file_path"].(string)
			if name == "shell" {
				if local, ok := env.(*execenv.LocalExecutionEnvironment); ok {
					if raw, err := local.ReadFileRaw("scripts/check.sh"); err == nil {
						obs.Checker = string(raw)
					}
				}
			} else {
				scope, _ := args["scope"].(string)
				if candidate, err := s.memoryEnvironment(scope); err == nil && filepath.IsLocal(path) {
					memoryEnv = candidate
					if raw, err := memoryEnv.ReadFileRaw(path); err == nil {
						obs.Before = string(raw)
					}
				}
			}
			result, err := execute(ctx, env, args)
			if err == nil {
				if memoryEnv != nil {
					if raw, readErr := memoryEnv.ReadFileRaw(path); readErr == nil {
						obs.After = string(raw)
					}
				}
				observations = append(observations, obs)
			} else if name == "shell" {
				observations = append(observations, obs)
			}
			return result, err
		}
		if err := s.reg.Register(wrapped); err != nil {
			t.Fatal(err)
		}
	}
	return func() []memoryEvalToolObservation {
		mu.Lock()
		defer mu.Unlock()
		return append([]memoryEvalToolObservation(nil), observations...)
	}
}
func memoryEvalRunPairs(t *testing.T, b *memoryEvalAdmission, c *llm.Client, p *provider.Profile, home string, hook func(string, bool, memoryEvalStage, string, string)) ([]memoryEvalEpisode, error) {
	t.Helper()
	runStart := b.runStart
	if runStart.IsZero() {
		runStart = time.Now()
	}
	var episodes []memoryEvalEpisode
	for _, pair := range []string{"recall", "correction"} {
		stages := memoryRecallStages
		if pair == "correction" {
			stages = memoryCorrectionStages
		}
		for _, disabled := range []bool{false, true} {
			root, workspace := memoryEvalDisposable(t), memoryEvalDisposable(t)
			memoryEvalFixture(t, pair, workspace)
			armStart := time.Now()
			prior := ""
			episode := memoryEvalEpisode{Pair: pair, Disabled: disabled}
			for _, stage := range stages {
				err := func() error {
					start := time.Now()
					deadline := memoryEvalDeadline(start, armStart, runStart)
					stageContext, stopStage := b.bindContext(context.Background())
					ctx, cancel := context.WithDeadline(stageContext, deadline)
					defer stopStage()
					defer cancel()
					if err := b.beginStage(stage.cap, deadline); err != nil {
						t.Fatal("memory eval infrastructure failure, next stage refused")
					}
					memoryEvalWrite(t, filepath.Join(workspace, "main_test.go"), memoryEvalVisible(stage.name))
					if pair == "correction" && stage.name == "B" {
						script, err := os.ReadFile(filepath.Join(workspace, "scripts", "check.sh"))
						if err != nil {
							t.Fatal(err)
						}
						revised := strings.Replace(string(script), memoryEvalCheckerCondition(false), memoryEvalCheckerCondition(true), 1)
						if revised == string(script) {
							t.Fatal("checker revision source mismatch")
						}
						memoryEvalWrite(t, filepath.Join(workspace, "scripts", "check.sh"), revised)
					}
					// Inspection before execution verifies this exact script runs Go tests.
					script, err := os.ReadFile(filepath.Join(workspace, "scripts", "check.sh"))
					if err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(string(script), "exec go test ./...") || !strings.Contains(string(script), "set -eu") {
						t.Fatal("fixture checker does not execute tests")
					}
					evidence := memoryEvalStageEvidence{Name: stage.name, PriorSessionID: prior, WikiBefore: memoryEvalWiki(t, filepath.Join(root, "wiki")), Files: map[string]string{"workspace": workspace}}
					if hook != nil {
						hook(pair, disabled, stage, prior, workspace)
					}
					var verifyErr error
					evidence.BeforeTask, _, verifyErr = memoryEvalVerify(ctx, t, home, workspace, stage.name)
					if verifyErr != nil {
						err := fmt.Errorf("memory eval initial verifier infrastructure failure: %w", verifyErr)
						b.fail(err)
						cancel()
						stopStage()
						return err
					}
					cfg := memoryEvalConfig(ctx, root, disabled, stage.cap)
					s, err := NewSession(c, p, memoryEvalLocal(t, home, workspace, t.TempDir()), cfg)
					if err != nil {
						cancel()
						t.Fatal("memory eval session infrastructure failure")
					}
					defer func() { cancel(); s.Close() }()
					evidence.SessionID = s.ID()
					observedTools := memoryEvalObserveTools(t, s)
					seen, stop := captureEvents(s)
					goal := "Implement normalize to trim surrounding whitespace."
					if stage.name == "B" {
						goal = "Implement split to trim each comma-separated item, preserving normalize."
					}
					if stage.name == "C" {
						goal = "Implement join to trim each input element before joining with commas, preserving normalize and split."
					}
					prompt := goal + " Run the fixture checker and finish the task. Shell toolchain prefix: " + memoryEvalGoEnv()
					if prior != "" {
						prompt += " Previous actual session transcript: " + prior
					}
					_, err = s.ProcessInput(ctx, prompt, nil)
					cancel()
					var childDone []<-chan struct{}
					for _, sub := range s.subagents.directSubagents() {
						sub.mu.Lock()
						if sub.done != nil {
							childDone = append(childDone, sub.done)
						}
						sub.mu.Unlock()
					}
					stop() // Session Close and event drain precede counter/transport reset.
					for _, done := range childDone {
						<-done
					}
					b.active.Wait()
					if b.terminalFailure() != nil {
						t.Fatal("memory eval shared infrastructure failure, run stopped without retry")
					}
					if err != nil {
						if !memoryEvalBudgetError(err) {
							t.Fatal("memory eval session infrastructure failure, run stopped without retry")
						}
						evidence.Limitation = "stage budget stopped, no retry"
					}
					collector := newEvalCollector("memory-pair", memoryEvalModel, pair+"/"+stage.name)
					for _, ev := range *seen {
						collector.ProcessEvent(ev)
					}
					evidence.Trace = *seen
					evidence.Observations = observedTools()
					evidence.Metrics = collector.Metrics()
					// Verifier keeps the same absolute stage deadline, with no extra time.
					verifyCtx, verifyCancel := context.WithDeadline(context.Background(), deadline)
					evidence.Task, evidence.Verifier, verifyErr = memoryEvalVerify(verifyCtx, t, home, workspace, stage.name)
					verifyCancel()
					if verifyErr != nil {
						err := fmt.Errorf("memory eval final verifier infrastructure failure: %w", verifyErr)
						b.fail(err)
						stopStage()
						return err
					}
					evidence.WikiAfter = memoryEvalWiki(t, filepath.Join(root, "wiki"))
					evidenceEnv := memoryEvalLocal(t, home, workspace, t.TempDir())
					for _, name := range []string{"main.go", "go.mod", "main_test.go", "scripts/check.sh"} {
						raw, err := evidenceEnv.ReadFileRaw(name)
						if err != nil {
							evidence.Files[name] = "unavailable through confined read"
							continue
						}
						evidence.Files[name] = string(raw)
					}
					evidenceEnv.Cleanup()
					raw, err := os.ReadFile(transcriptPath(s.stateDir, s.id))
					if err != nil {
						t.Fatal("memory eval transcript evidence unavailable")
					}
					evidence.Transcript = string(raw)
					memoryEvalGrade(&evidence, pair == "correction" && stage.name != "A")
					evidence.Counters = memoryEvalSnapshot(b)
					evidence.Elapsed = time.Since(start)
					episode.Stages = append(episode.Stages, evidence)
					if b.evidenceRoot != "" {
						memoryEvalWriteEvidence(t, b, filepath.Join(b.evidenceRoot, fmt.Sprintf("%s-disabled-%t-%s.json", pair, disabled, stage.name)), evidence)
					}
					prior = s.ID()
					return nil
				}()
				if err != nil {
					return episodes, err
				}
			}
			episode.Elapsed = time.Since(armStart)
			episodes = append(episodes, episode)
		}
	}
	return episodes, nil
}

func memoryEvalDisposable(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	// LIFO runs this inspection before TempDir's automatic exact-root cleanup.
	t.Cleanup(func() {
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Error("disposable root inspection failed")
			return
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Logf("inspected disposable root %s, entries=%v", root, names)
	})
	return root
}

func (b *memoryEvalAdmission) observeUsage(u llm.Usage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.usage = b.usage.Add(u)
}
func (b *memoryEvalAdmission) observedUsage() llm.Usage {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.usage.Add(llm.Usage{})
}
func memoryEvalWriteEvidence(t *testing.T, b *memoryEvalAdmission, path string, value any) {
	t.Helper()
	if b.authRoot != "" {
		record, err := openai.LoadAuth(b.authRoot, "codex-jesse-at-pr")
		if err != nil {
			t.Fatal("memory eval private evidence auth read failed")
		}
		b.sensitive = append(b.sensitive, record.AccessToken, record.RefreshToken, record.IDToken, record.Email, record.AccountID, record.WorkspaceID)
	}
	text := memoryEvalJSON(value)
	for _, value := range b.sensitive {
		if value != "" {
			for range 4 {
				text = strings.ReplaceAll(text, value, "[redacted]")
				encoded := memoryEvalJSON(value)
				value = encoded[1 : len(encoded)-1]
			}
		}
	}
	memoryEvalWrite(t, path, text)
}

func (b *memoryEvalAdmission) approvedRequest(req llm.Request) error {
	if b.boundModel && (req.Provider != "codex-jesse-at-pr" || req.Model != "gpt-6.1-sol") {
		return errors.New("memory eval unapproved model route")
	}
	return nil
}

func memoryEvalBudgetError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	for _, message := range []string{"memory eval deadline reached", "memory eval logical cap reached", "memory eval HTTP cap reached", "memory eval tool round cap reached"} {
		if strings.Contains(err.Error(), message) {
			return true
		}
	}
	return false
}
