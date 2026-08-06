//go:build serffuzz

package execenv

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// FuzzProcessRuntimeProgram exercises the public command, grep-ripgrep, stream,
// environment, and lifecycle plumbing against a scripted command runtime. No
// fuzz replay launches a shell, child process, Git command, provider, or network
// client. The runtime records the real LocalExecutionEnvironment configuration;
// the oracle checks command result semantics, cancellation classification,
// descriptor-safe command preparation, PATH lookup, and the inherited-environment
// policy rather than merely requiring that the program return.
func FuzzProcessRuntimeProgram(f *testing.F) {
	for _, seed := range [][]byte{
		nil,
		{0},
		{1, 2, 3},
		{0xff, 0x00, 0x4a, 0x91},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, program []byte) {
		if len(program) > 48 {
			program = program[:48]
		}
		first := runProcessRuntimeProgram(t, program)
		second := runProcessRuntimeProgram(t, program)
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("process runtime program is not deterministic:\nfirst=%#v\nsecond=%#v", first, second)
		}
	})
}

type processRuntimeTrace struct {
	Results  []processRuntimeResult
	Commands []processRuntimeCommandTrace
	Streams  []string
}

type processRuntimeResult struct {
	Name     string
	Stdout   string
	Stderr   string
	ExitCode int
	TimedOut bool
	Err      string
}

type processRuntimeCommandTrace struct {
	Name           string
	Kind           string
	Request        string
	Args           []string
	Dir            string
	Env            []string
	ExecutablePath string
	StartCalls     int
	WaitCalls      int
	TerminateCalls int
	KillCalls      int
}

type processRuntimePlan struct {
	name             string
	stdout           string
	stderr           string
	startErr         error
	waitErr          error
	exitCode         int
	hasExitCode      bool
	waitForTerminate bool
	ignoreTerminate  bool
}

type processRuntimeFactory struct {
	t       *testing.T
	plans   []processRuntimePlan
	next    int
	byName  map[string]*processRuntimeCommand
	trace   *processRuntimeTrace
	created int
}

func newProcessRuntimeFactory(t *testing.T, token string, trace *processRuntimeTrace) *processRuntimeFactory {
	return &processRuntimeFactory{
		t: t,
		plans: []processRuntimePlan{
			{name: "argv-outside"},
			{name: "argv-none", stdout: "argv-none:" + token, stderr: "argv-none-stderr"},
			{name: "argv-default", stdout: "argv-default:" + token},
			{name: "argv-all", stdout: "argv-all:" + token},
			{name: "argv-core", stdout: "argv-core:" + token},
			{name: "argv-cancel", waitErr: errors.New("scripted cancellation"), waitForTerminate: true},
			{name: "argv-deadline", waitErr: errors.New("scripted deadline"), waitForTerminate: true},
			{name: "argv-start-failure", startErr: errors.New("scripted start failure")},
			{name: "argv-default-timeout", stdout: "default-timeout:" + token},
			{name: "argv-generic-wait", waitErr: errors.New("scripted generic wait failure")},
			{name: "grep-success", stdout: "one\ntwo\nthree"},
			{name: "grep-no-match", waitErr: errors.New("scripted no match"), exitCode: 1, hasExitCode: true},
			{name: "grep-failure", stdout: "partial", stderr: "diagnostic", waitErr: errors.New("scripted grep failure"), exitCode: 2, hasExitCode: true},
			{name: "grep-default-cap", stdout: "one\ntwo"},
			{name: "stream-success", stdout: "stream-out", stderr: "stream-err"},
			{name: "stream-signal", waitErr: errors.New("scripted stream signal"), waitForTerminate: true},
			{name: "stream-context", waitErr: errors.New("scripted stream context"), waitForTerminate: true},
			{name: "stream-detached", stdout: "detached"},
			{name: "stream-default-dir", stdout: "default-dir"},
			{name: "stream-start-failure", startErr: errors.New("scripted stream start failure")},
			{name: "stream-exit-code", waitErr: errors.New("scripted stream exit"), exitCode: 23, hasExitCode: true},
			{name: "child-argv", stdout: "child:" + token},
		},
		byName: map[string]*processRuntimeCommand{},
		trace:  trace,
	}
}

func (f *processRuntimeFactory) Shell(command string) commandRuntime {
	return f.nextCommand("shell", command, []string{"/scripted/shell", "-c", command})
}

func (f *processRuntimeFactory) Argv(name string, args ...string) commandRuntime {
	return f.nextCommand("argv", name, append([]string{name}, args...))
}

func (f *processRuntimeFactory) nextCommand(kind, request string, args []string) commandRuntime {
	f.t.Helper()
	if f.next >= len(f.plans) {
		f.t.Fatalf("unexpected scripted command %s %q after %d planned commands", kind, request, len(f.plans))
	}
	plan := f.plans[f.next]
	f.next++
	f.created++
	command := &processRuntimeCommand{
		plan:       plan,
		kind:       kind,
		request:    request,
		args:       append([]string(nil), args...),
		pid:        50_000 + f.created,
		terminated: make(chan struct{}),
		trace:      f.trace,
	}
	f.byName[plan.name] = command
	return command
}

func (f *processRuntimeFactory) command(name string) *processRuntimeCommand {
	f.t.Helper()
	command := f.byName[name]
	if command == nil {
		f.t.Fatalf("scripted command %q was not created", name)
	}
	return command
}

func (f *processRuntimeFactory) assertConsumed() {
	f.t.Helper()
	if f.next != len(f.plans) {
		f.t.Fatalf("scripted commands consumed=%d, want %d", f.next, len(f.plans))
	}
}

type processRuntimeCommand struct {
	plan       processRuntimePlan
	kind       string
	request    string
	args       []string
	pid        int
	config     commandRuntimeConfig
	startCalls int
	waitCalls  int
	terminate  int
	kill       int
	terminated chan struct{}
	doneOnce   sync.Once
	trace      *processRuntimeTrace
}

func (c *processRuntimeCommand) Args() []string { return c.args }

func (c *processRuntimeCommand) Configure(config commandRuntimeConfig) {
	c.config = commandRuntimeConfig{
		Dir:            config.Dir,
		Env:            append([]string(nil), config.Env...),
		ExecutablePath: config.ExecutablePath,
		Stdout:         config.Stdout,
		Stderr:         config.Stderr,
		Wrapper:        config.Wrapper,
	}
}

func (c *processRuntimeCommand) Start() error {
	c.startCalls++
	if c.plan.startErr != nil {
		return c.plan.startErr
	}
	if c.config.Stdout != nil {
		_, _ = c.config.Stdout.Write([]byte(c.plan.stdout))
	}
	if c.config.Stderr != nil {
		_, _ = c.config.Stderr.Write([]byte(c.plan.stderr))
	}
	return nil
}

func (c *processRuntimeCommand) Wait() error {
	c.waitCalls++
	if c.plan.waitForTerminate {
		<-c.terminated
	}
	return c.plan.waitErr
}

func (c *processRuntimeCommand) PID() int { return c.pid }

func (c *processRuntimeCommand) ExitCode(error) (int, bool) {
	return c.plan.exitCode, c.plan.hasExitCode
}

func (c *processRuntimeCommand) Terminate() {
	c.terminate++
	if !c.plan.ignoreTerminate {
		c.doneOnce.Do(func() { close(c.terminated) })
	}
}

func (c *processRuntimeCommand) Kill() {
	c.kill++
	c.doneOnce.Do(func() { close(c.terminated) })
}

func (c *processRuntimeCommand) appendTrace(root string) {
	env := append([]string(nil), c.config.Env...)
	for i := range env {
		env[i] = strings.ReplaceAll(env[i], root, "$ROOT")
	}
	sort.Strings(env)
	c.trace.Commands = append(c.trace.Commands, processRuntimeCommandTrace{
		Name:           c.plan.name,
		Kind:           c.kind,
		Request:        strings.ReplaceAll(c.request, root, "$ROOT"),
		Args:           processRuntimeNormalizeStrings(root, c.args),
		Dir:            strings.ReplaceAll(c.config.Dir, root, "$ROOT"),
		Env:            env,
		ExecutablePath: strings.ReplaceAll(c.config.ExecutablePath, root, "$ROOT"),
		StartCalls:     c.startCalls,
		WaitCalls:      c.waitCalls,
		TerminateCalls: c.terminate,
		KillCalls:      c.kill,
	})
}

func runProcessRuntimeProgram(t *testing.T, program []byte) processRuntimeTrace {
	t.Helper()
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	venvBin := filepath.Join(sub, ".venv", "bin")
	if err := os.MkdirAll(venvBin, 0o755); err != nil {
		t.Fatalf("make scripted venv: %v", err)
	}
	tool := filepath.Join(venvBin, "tool")
	if err := os.WriteFile(tool, []byte("not executed"), 0o755); err != nil {
		t.Fatalf("write scripted executable: %v", err)
	}

	token := processRuntimeToken(program)
	trace := processRuntimeTrace{}
	factory := newProcessRuntimeFactory(t, token, &trace)
	inherited := []string{
		"PATH=/fixture/base-bin",
		"HOME=/fixture/home",
		"USER=fixture-user",
		"LANG=C",
		"SAFE=kept",
		"API_KEY=inherited-secret",
		"BROKEN",
	}
	env := &LocalExecutionEnvironment{
		RootDir:        root,
		commandFactory: factory,
		inheritedEnv:   func() []string { return append([]string(nil), inherited...) },
	}
	if err := env.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if env.WorkingDirectory() != root || env.KernelWrapper() != nil || env.SandboxReRootError() != nil {
		t.Fatalf("fresh command environment lifecycle state is wrong")
	}
	if got := env.WithSandboxInvocationGrant(filepath.Join(root, "grant")); got != env {
		t.Fatal("off environment unexpectedly created a sandbox grant clone")
	}
	if err := env.UseControlPolicy(root); err != nil {
		t.Fatalf("UseControlPolicy on off environment: %v", err)
	}
	if platform := env.Platform(); platform != "linux" && platform != "darwin" && platform != "windows" {
		t.Fatalf("Platform = %q", platform)
	}

	if _, err := env.ExecArgv(context.Background(), "tool", nil, 50, filepath.Join(filepath.Dir(root), "outside"), nil); err == nil {
		t.Fatal("ExecArgv outside the root unexpectedly succeeded")
	}
	if command := factory.command("argv-outside"); command.startCalls != 0 || command.waitCalls != 0 || command.config.Dir != "" {
		t.Fatalf("outside-root command was configured or started: %+v", command)
	}

	env.EnvPolicy = EnvPolicyNone
	result, err := env.ExecArgv(context.Background(), "tool", []string{"--check"}, 50, "sub", map[string]string{
		"PATH":    "/fixture/base-bin",
		"VISIBLE": token,
	})
	processRuntimeRequireResult(t, &trace, "argv-none", result, err, "argv-none:"+token, "argv-none-stderr", 0, false, "")
	processRuntimeAssertCommand(t, factory.command("argv-none"), root, sub, tool, map[string]string{
		"VISIBLE": token,
	}, []string{"API_KEY", "SAFE", "HOME"})

	env.EnvPolicy = EnvPolicyDefault
	result, err = env.ExecArgv(context.Background(), "missing", []string{"--default"}, 50, "", map[string]string{
		"VISIBLE":     token,
		"MODEL_TOKEN": "must-not-leak",
	})
	processRuntimeRequireResult(t, &trace, "argv-default", result, err, "argv-default:"+token, "", 0, false, "")
	processRuntimeAssertEnv(t, factory.command("argv-default").config.Env, map[string]string{
		"SAFE":    "kept",
		"VISIBLE": token,
	}, []string{"API_KEY", "MODEL_TOKEN"})
	if factory.command("argv-default").config.ExecutablePath != "" {
		t.Fatalf("missing executable path = %q, want empty", factory.command("argv-default").config.ExecutablePath)
	}

	env.EnvPolicy = EnvPolicyAll
	result, err = env.ExecArgv(context.Background(), "missing", []string{"--all"}, 50, "", map[string]string{"VISIBLE": token})
	processRuntimeRequireResult(t, &trace, "argv-all", result, err, "argv-all:"+token, "", 0, false, "")
	processRuntimeAssertEnv(t, factory.command("argv-all").config.Env, map[string]string{
		"API_KEY": "inherited-secret",
		"SAFE":    "kept",
		"VISIBLE": token,
	}, nil)

	env.EnvPolicy = EnvPolicyCoreOnly
	result, err = env.ExecArgv(context.Background(), "missing", []string{"--core"}, 50, "", map[string]string{
		"VISIBLE":      token,
		"EXTRA_SECRET": "explicit-is-allowed",
	})
	processRuntimeRequireResult(t, &trace, "argv-core", result, err, "argv-core:"+token, "", 0, false, "")
	processRuntimeAssertEnv(t, factory.command("argv-core").config.Env, map[string]string{
		"HOME":         "/fixture/home",
		"USER":         "fixture-user",
		"LANG":         "C",
		"VISIBLE":      token,
		"EXTRA_SECRET": "explicit-is-allowed",
	}, []string{"SAFE", "API_KEY"})

	env.EnvPolicy = EnvPolicyNone
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = env.ExecArgv(cancelled, "tool", []string{"--cancel"}, 50, "sub", nil)
	processRuntimeRequireResult(t, &trace, "argv-cancel", result, err, "", "", 130, false, context.Canceled.Error())
	if command := factory.command("argv-cancel"); command.terminate != 1 || command.kill != 0 {
		t.Fatalf("cancelled command signals terminate=%d kill=%d, want 1/0", command.terminate, command.kill)
	}

	result, err = env.ExecArgv(processRuntimeDeadlineContext{}, "tool", []string{"--deadline"}, 50, "sub", nil)
	processRuntimeRequireResult(t, &trace, "argv-deadline", result, err, "", "", 124, true, context.DeadlineExceeded.Error())
	if command := factory.command("argv-deadline"); command.terminate != 1 || command.kill != 0 {
		t.Fatalf("deadline command signals terminate=%d kill=%d, want 1/0", command.terminate, command.kill)
	}

	result, err = env.ExecArgv(context.Background(), "tool", []string{"--start-failure"}, 50, "sub", nil)
	processRuntimeRequireResult(t, &trace, "argv-start-failure", result, err, "", "", 127, false, "scripted start failure")
	if command := factory.command("argv-start-failure"); command.waitCalls != 0 {
		t.Fatalf("start-failed command Wait calls=%d, want 0", command.waitCalls)
	}
	result, err = env.ExecArgv(context.Background(), "tool", []string{"--default-timeout"}, 0, "sub", nil)
	processRuntimeRequireResult(t, &trace, "argv-default-timeout", result, err, "default-timeout:"+token, "", 0, false, "")
	result, err = env.ExecArgv(context.Background(), "tool", []string{"--generic-wait"}, 50, "sub", nil)
	processRuntimeRequireResult(t, &trace, "argv-generic-wait", result, err, "", "", 1, false, "scripted generic wait failure")

	env.lookPath = func(name string) (string, error) {
		if name != "rg" {
			t.Fatalf("unexpected lookup %q", name)
		}
		return "/fixture/rg", nil
	}
	grep, err := env.Grep("x; touch never", "sub", "*.go", true, 2, "")
	if err != nil || grep != "one\ntwo" {
		t.Fatalf("Grep ripgrep success = %q, %v", grep, err)
	}
	if request := factory.command("grep-success").request; !strings.Contains(request, "'x; touch never'") || !strings.Contains(request, "'*.go'") {
		t.Fatalf("Grep did not shell-quote adversarial arguments: %q", request)
	}
	grep, err = env.Grep("nothing", "sub", "", false, 1, "")
	if err != nil || grep != "" {
		t.Fatalf("Grep no-match = %q, %v", grep, err)
	}
	grep, err = env.Grep("broken", "sub", "", false, 1, "")
	if err == nil || grep != "partialdiagnostic" {
		t.Fatalf("Grep failure = %q, %v", grep, err)
	}
	grep, err = env.Grep("default-cap", "sub", "", false, 0, "")
	if err != nil || grep != "one\ntwo" {
		t.Fatalf("Grep default cap = %q, %v", grep, err)
	}

	var stream bytes.Buffer
	handle, err := env.StreamCommand(context.Background(), "stream-success", "sub", nil, &stream)
	if err != nil {
		t.Fatalf("StreamCommand success: %v", err)
	}
	if code, waitErr := handle.Wait(); waitErr != nil || code != 0 || stream.String() != "stream-outstream-err" {
		t.Fatalf("StreamCommand success wait=(%d, %v) output=%q", code, waitErr, stream.String())
	}
	// A completed handle must make later signals inert; the command has already
	// left the PID tracker and no termination may be attempted.
	handle.Signal()
	if command := factory.command("stream-success"); command.terminate != 0 {
		t.Fatalf("completed stream received terminate=%d", command.terminate)
	}
	trace.Streams = append(trace.Streams, stream.String())

	handle, err = env.StreamCommand(context.Background(), "stream-signal", "sub", nil, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("StreamCommand signal: %v", err)
	}
	handle.Signal()
	handle.Signal()
	if code, waitErr := handle.Wait(); waitErr == nil || code != 127 {
		t.Fatalf("StreamCommand signal wait=(%d, %v), want 127/error", code, waitErr)
	}
	if command := factory.command("stream-signal"); command.terminate != 1 || command.kill != 0 {
		t.Fatalf("stream Signal calls terminate=%d kill=%d, want 1/0", command.terminate, command.kill)
	}

	streamCtx, streamCancel := context.WithCancel(context.Background())
	handle, err = env.StreamCommand(streamCtx, "stream-context", "sub", nil, &bytes.Buffer{})
	if err != nil {
		streamCancel()
		t.Fatalf("StreamCommand context: %v", err)
	}
	streamCancel()
	if code, waitErr := handle.Wait(); waitErr == nil || code != 127 {
		t.Fatalf("StreamCommand context wait=(%d, %v), want 127/error", code, waitErr)
	}
	if command := factory.command("stream-context"); command.terminate != 1 || command.kill != 0 {
		t.Fatalf("stream context calls terminate=%d kill=%d, want 1/0", command.terminate, command.kill)
	}

	detachedCtx := &processRuntimeDetachedContext{Context: context.Background()}
	handle, err = env.StreamCommand(detachedCtx, "stream-detached", "sub", nil, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("StreamCommand detached: %v", err)
	}
	if !detachedCtx.detached {
		t.Fatal("StreamCommand did not detach the detachable context after start")
	}
	if code, waitErr := handle.Wait(); waitErr != nil || code != 0 {
		t.Fatalf("StreamCommand detached wait=(%d, %v)", code, waitErr)
	}

	// The stream path initializes its PID tracker lazily and applies the same root
	// containment before creating a runtime. Invalid roots therefore cannot consume
	// a factory plan or reach Start.
	env.runningPIDs = nil
	if handle, err := env.StreamCommand(context.Background(), "never", filepath.Join(filepath.Dir(root), "outside"), nil, &bytes.Buffer{}); err == nil || handle != nil {
		t.Fatalf("outside-root StreamCommand = (%v, %v), want nil/error", handle, err)
	}
	handle, err = env.StreamCommand(context.Background(), "stream-default-dir", "", nil, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("StreamCommand default directory: %v", err)
	}
	if code, waitErr := handle.Wait(); waitErr != nil || code != 0 {
		t.Fatalf("StreamCommand default directory wait=(%d, %v)", code, waitErr)
	}
	if command := factory.command("stream-default-dir"); command.config.Dir != root {
		t.Fatalf("default stream dir = %q, want %q", command.config.Dir, root)
	}
	if handle, err := env.StreamCommand(context.Background(), "stream-start-failure", "sub", nil, &bytes.Buffer{}); err == nil || handle != nil {
		t.Fatalf("start-failed StreamCommand = (%v, %v), want nil/error", handle, err)
	}
	handle, err = env.StreamCommand(context.Background(), "stream-exit-code", "sub", nil, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("StreamCommand exit code: %v", err)
	}
	if code, waitErr := handle.Wait(); waitErr != nil || code != 23 {
		t.Fatalf("StreamCommand exit-code wait=(%d, %v), want 23/nil", code, waitErr)
	}

	child := env.WithWorkingDirectory(sub)
	result, err = child.ExecArgv(context.Background(), "tool", []string{"--child"}, 50, "", map[string]string{"VISIBLE": token})
	processRuntimeRequireResult(t, &trace, "child-argv", result, err, "child:"+token, "", 0, false, "")
	if command := factory.command("child-argv"); command.config.Dir != sub {
		t.Fatalf("child command dir = %q, want %q", command.config.Dir, sub)
	}

	preCancelled, preCancel := context.WithCancel(context.Background())
	preCancel()
	if handle, err := env.StreamCommand(preCancelled, "must-not-start", "sub", nil, &bytes.Buffer{}); err == nil || handle != nil {
		t.Fatalf("pre-cancelled StreamCommand = (%v, %v), want nil/context error", handle, err)
	}

	// Cleanup owns scripted runtime teardown too. Store a legacy zero-PID marker
	// alongside it to retain the historical safe fallback without ever signalling
	// a host process group; an instance-local zero grace keeps this replay
	// wall-clock independent without racing stream timer goroutines.
	cleanupRuntime := &processRuntimeCommand{pid: 0, terminated: make(chan struct{})}
	cleanupEnv := NewLocalExecutionEnvironment(root)
	zeroGrace := time.Duration(0)
	cleanupEnv.terminationGrace = &zeroGrace
	cleanupEnv.runningPIDs.Store(0, cleanupRuntime)
	cleanupEnv.runningPIDs.Store(-1, struct{}{})
	cleanupEnv.Cleanup()
	if cleanupRuntime.terminate != 1 || cleanupRuntime.kill != 1 {
		t.Fatalf("Cleanup scripted runtime signals terminate=%d kill=%d, want 1/1", cleanupRuntime.terminate, cleanupRuntime.kill)
	}

	processRuntimeCheckSystemAdapter(t)
	factory.assertConsumed()
	for _, command := range factory.byName {
		command.appendTrace(root)
	}
	sort.Slice(trace.Commands, func(i, j int) bool { return trace.Commands[i].Name < trace.Commands[j].Name })
	env.Cleanup()
	env.DisposeSandboxScratch()
	return trace
}

func processRuntimeToken(program []byte) string {
	if len(program) == 0 {
		return "empty"
	}
	return base64.RawURLEncoding.EncodeToString(program)
}

func processRuntimeRequireResult(t *testing.T, trace *processRuntimeTrace, name string, result ExecResult, err error, stdout, stderr string, exitCode int, timedOut bool, wantErr string) {
	t.Helper()
	gotErr := ""
	if err != nil {
		gotErr = err.Error()
	}
	trace.Results = append(trace.Results, processRuntimeResult{
		Name: name, Stdout: result.Stdout, Stderr: result.Stderr, ExitCode: result.ExitCode, TimedOut: result.TimedOut, Err: gotErr,
	})
	if result.Stdout != stdout || result.Stderr != stderr || result.ExitCode != exitCode || result.TimedOut != timedOut || gotErr != wantErr {
		t.Fatalf("%s result=%+v err=%q, want stdout=%q stderr=%q exit=%d timedOut=%t err=%q", name, result, gotErr, stdout, stderr, exitCode, timedOut, wantErr)
	}
}

func processRuntimeAssertCommand(t *testing.T, command *processRuntimeCommand, root, dir, executable string, required map[string]string, absent []string) {
	t.Helper()
	if command.config.Dir != dir || command.config.ExecutablePath != executable {
		t.Fatalf("%s config dir=%q executable=%q, want %q/%q", command.plan.name, command.config.Dir, command.config.ExecutablePath, dir, executable)
	}
	processRuntimeAssertEnv(t, command.config.Env, required, absent)
	path := processRuntimeEnvMap(command.config.Env)["PATH"]
	if !strings.HasPrefix(path, filepath.Join(dir, ".venv", "bin")+string(os.PathListSeparator)) {
		t.Fatalf("%s PATH = %q, want venv prefix", command.plan.name, path)
	}
	if !strings.HasPrefix(command.config.Dir, root) {
		t.Fatalf("%s dir escaped root: %q", command.plan.name, command.config.Dir)
	}
}

func processRuntimeAssertEnv(t *testing.T, env []string, required map[string]string, absent []string) {
	t.Helper()
	values := processRuntimeEnvMap(env)
	for key, want := range required {
		if got := values[key]; got != want {
			t.Fatalf("environment %s=%q, want %q in %v", key, got, want, env)
		}
	}
	for _, key := range absent {
		if _, ok := values[key]; ok {
			t.Fatalf("environment leaked %s in %v", key, env)
		}
	}
}

func processRuntimeEnvMap(env []string) map[string]string {
	values := make(map[string]string, len(env))
	for _, kv := range env {
		key, value, ok := strings.Cut(kv, "=")
		if ok {
			values[key] = value
		}
	}
	return values
}

func processRuntimeNormalizeStrings(root string, values []string) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = strings.ReplaceAll(value, root, "$ROOT")
	}
	return result
}

// processRuntimeDeadlineContext is already done with DeadlineExceeded, reaching
// the timeout classification without a wall-clock timer or sleep.
type processRuntimeDeadlineContext struct{}

func (processRuntimeDeadlineContext) Deadline() (time.Time, bool) { return time.Time{}, true }

func (processRuntimeDeadlineContext) Done() <-chan struct{} { return processRuntimeClosedDone }

func (processRuntimeDeadlineContext) Err() error { return context.DeadlineExceeded }

func (processRuntimeDeadlineContext) Value(any) any { return nil }

var processRuntimeClosedDone = func() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}()

type processRuntimeDetachedContext struct {
	context.Context
	detached bool
}

func (c *processRuntimeDetachedContext) DetachAfterStart() { c.detached = true }

// processRuntimeCheckSystemAdapter covers the production adapter only through a
// guaranteed-missing absolute executable. Start fails before any child can be
// created; Wait/PID/signal paths then operate on a zero PID and cannot signal the
// host. Public command execution above always uses the scripted runtime.
func processRuntimeCheckSystemAdapter(t *testing.T) {
	t.Helper()
	missing := filepath.Join(t.TempDir(), "missing-command")
	runtime := systemCommandRuntimeFactory{}.Argv(missing, "--never")
	if got, want := runtime.Args(), []string{missing, "--never"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("system runtime args = %v, want %v", got, want)
	}
	runtime.Configure(commandRuntimeConfig{Dir: t.TempDir(), Env: []string{"PATH=/fixture/bin"}, ExecutablePath: missing})
	if runtime.PID() != 0 {
		t.Fatalf("unstarted system runtime pid = %d, want 0", runtime.PID())
	}
	if _, ok := runtime.ExitCode(errors.New("not an exit status")); ok {
		t.Fatal("generic error unexpectedly had a system-process exit code")
	}
	runtime.Terminate()
	runtime.Kill()
	if err := runtime.Start(); err == nil {
		t.Fatalf("missing system runtime executable %q unexpectedly started", missing)
	}
	if err := runtime.Wait(); err == nil {
		t.Fatal("Wait after a failed Start unexpectedly succeeded")
	}
}
