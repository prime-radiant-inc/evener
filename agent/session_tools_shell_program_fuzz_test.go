//go:build serffuzz

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
)

// FuzzShellToolsBufferedProgram drives the real registerShellTools handlers
// through their public Registry.ExecuteCall boundary. The execution environment
// is scripted: it records calls and returns only fuzzer-decoded values, so this
// target never starts a process, accesses the host filesystem, calls a provider,
// or observes wall time.
//
// The program invokes shell (on the non-streaming buffered path), list_dir,
// grep, and glob with schema-valid arguments. It checks:
//   - shell parse precedence, timeout clamping/capping, exact buffered-output
//     rendering, and clean propagation of cancellation/timeout/executor errors;
//   - list_dir's normalized depth/offset/limit forwarding plus its bounded,
//     record-oriented page and plain-text type markers;
//   - grep/glob's argument normalization, error propagation, and result format;
//   - deterministic replay: the same finite program produces identical results
//     and identical external-boundary call traces on a fresh fake environment.
//
// The fake is the execution-environment boundary, not a Serf-internal mock. It
// deliberately does not implement StreamingExecutor, keeping shell on the
// synchronous buffered path owned by session_tools_shell.go.
//
// Registry: native:agent:.:FuzzShellToolsBufferedProgram::session_tools_shell.go#registerShellTools
func FuzzShellToolsBufferedProgram(f *testing.F) {
	for _, seed := range [][]byte{
		nil,                              // normal success, defaults
		{1},                              // background rejected on buffered env
		{2},                              // timed-out result
		{4},                              // context-cancelled executor result
		{8},                              // ordinary executor error
		{16},                             // list_dir error
		{32},                             // grep error
		{64},                             // glob error
		{0, 0, 0, 1},                     // max_runtime_ms clamps to one second
		{0, 0, 0, 6},                     // negative max_runtime_ms rejects before exec
		stpForegroundOutputSeed(4, 3, 0), // default timeout caps at max timeout
		stpForegroundOutputSeed(4, 0, 4), // runtime lowers the default timeout
		stpFilledSeed(96, 0xff),          // long background/list-error program
		stpFilledSeed(96, 0x55),          // long background/grep-error program
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		program := stpDecodeProgram(data)
		got := stpRunProgram(t, program)
		stpAssertProgram(t, program, got)

		// Replaying onto a fresh registry and external fake must preserve both the
		// model-facing results and the exact calls sent to the boundary.
		if replay := stpRunProgram(t, program); !reflect.DeepEqual(got, replay) {
			t.Fatalf("non-deterministic shell-tool replay:\nfirst=%#v\nsecond=%#v", got, replay)
		}
	})
}

func stpFilledSeed(n int, value byte) []byte {
	seed := make([]byte, n)
	for i := range seed {
		seed[i] = value
	}
	return seed
}

// stpForegroundOutputSeed makes command/description empty so the next bytes
// select nonempty no-newline stdout and stderr. This keeps fixed replay on the
// real buffered-output formatter while the three timeout selectors cover its
// precedence rules.
func stpForegroundOutputSeed(defaultTimeout, maxTimeout, maxRuntime byte) []byte {
	return []byte{
		0, defaultTimeout, maxTimeout, maxRuntime, // foreground flags and timeouts
		0, 0, // empty command/description suffixes
		2, 1, 0, // stdout: one non-newline character
		2, 1, 1, // stderr: one non-newline character
	}
}

// stpReader makes every byte slice a finite, bounded program. End-of-input is
// zero, so adding bytes only appends choices rather than changing earlier ones.
type stpReader struct {
	data []byte
	pos  int
}

func (r *stpReader) next() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	b := r.data[r.pos]
	r.pos++
	return b
}

func (r *stpReader) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.next()) % n
}

func (r *stpReader) text(max int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789_./-*"
	n := r.intn(max + 1)
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[r.intn(len(alphabet))]
	}
	return string(b)
}

func (r *stpReader) output() string {
	switch r.intn(5) {
	case 0:
		return ""
	case 1:
		return " \t"
	case 2:
		return r.text(24)
	case 3:
		return r.text(24) + "\n"
	default:
		return "line-" + r.text(18) + "\nnext-" + r.text(18)
	}
}

type stpProgram struct {
	background     bool
	timedOut       bool
	execErrorKind  int
	listError      bool
	grepError      bool
	globError      bool
	defaultTimeout int
	maxTimeout     int
	maxRuntime     int
	command        string
	description    string
	stdout         string
	stderr         string
	exitCode       int
	durationMS     int64

	listPath    string
	listDepth   int
	listOffset  int
	listLimit   int
	listEntries []execenv.DirEntry

	grepPattern string
	grepPath    string
	grepGlob    string
	grepCI      bool
	grepMax     int
	grepMode    string
	grepOutput  string

	globPattern string
	globPath    string
	globMatches []string
}

func stpDecodeProgram(data []byte) stpProgram {
	r := &stpReader{data: data}
	flags := r.next()
	p := stpProgram{
		background:    flags&1 != 0,
		timedOut:      flags&2 != 0,
		execErrorKind: int((flags >> 2) & 3),
		listError:     flags&16 != 0,
		grepError:     flags&32 != 0,
		globError:     flags&64 != 0,
	}

	p.defaultTimeout = []int{0, 500, 1000, 2500, 5000}[r.intn(5)]
	p.maxTimeout = []int{0, 500, 1000, 2000, 4000}[r.intn(5)]
	p.maxRuntime = []int{0, 1, 999, 1000, 1500, 5000, -1}[r.intn(7)]
	p.command = "cmd_" + r.text(20)
	p.description = r.text(20)
	p.stdout = r.output()
	p.stderr = r.output()
	p.exitCode = []int{-1, 0, 1, 42, 124}[r.intn(5)]
	p.durationMS = int64([]int{0, 1, 17, 999, 2000}[r.intn(5)])

	p.listPath = "/virtual/" + r.text(12)
	p.listDepth = []int{-2, 0, 1, 2, 5}[r.intn(5)]
	p.listOffset = []int{-2, 0, 1, 2, 7, 99}[r.intn(6)]
	p.listLimit = []int{-2, 0, 1, 2, 5, 9}[r.intn(6)]
	p.listEntries = []execenv.DirEntry{
		{Name: "directory", IsDir: true},
		{Name: "link", IsSymlink: true, Size: 7},
		{Name: "program", IsExec: true, Size: 11},
		{Name: "plain", Size: 13},
	}
	for i, n := 0, r.intn(5); i < n; i++ {
		entry := execenv.DirEntry{
			Name: "entry_" + strconv.Itoa(i) + "_" + r.text(8),
			Size: int64(r.intn(1000)),
		}
		switch r.intn(4) {
		case 1:
			entry.IsDir = true
		case 2:
			entry.IsSymlink = true
		case 3:
			entry.IsExec = true
		}
		p.listEntries = append(p.listEntries, entry)
	}

	p.grepPattern = r.text(16)
	p.grepPath = "/virtual/" + r.text(12)
	p.grepGlob = r.text(12)
	p.grepCI = r.next()%2 == 1
	p.grepMax = []int{-2, 0, 1, 7, 100, 999}[r.intn(6)]
	p.grepMode = []string{"content", "files_with_matches", "count"}[r.intn(3)]
	p.grepOutput = "grep:" + r.output()

	p.globPattern = r.text(16)
	p.globPath = "/virtual/" + r.text(12)
	for i, n := 0, r.intn(5); i < n; i++ {
		p.globMatches = append(p.globMatches, "match_"+strconv.Itoa(i)+"_"+r.text(10))
	}
	return p
}

type stpError string

func (e stpError) Error() string { return string(e) }

const (
	errSTPExec stpError = "scripted exec failure"
	errSTPList stpError = "scripted list failure"
	errSTPGrep stpError = "scripted grep failure"
	errSTPGlob stpError = "scripted glob failure"
)

type stpExecCall struct {
	command   string
	timeoutMS int
	workDir   string
}

type stpListCall struct {
	path  string
	depth int
}

type stpGrepCall struct {
	pattern string
	path    string
	glob    string
	ci      bool
	max     int
	mode    string
}

type stpGlobCall struct {
	pattern string
	path    string
}

// stpEnv is an external-boundary fake. It is intentionally non-streaming: the
// presence of StreamCommand would take shell through the job-manager path rather
// than session_tools_shell.go's deterministic buffered branch.
type stpEnv struct {
	agenttest.FakeEnv
	execResult  execenv.ExecResult
	execErr     error
	listEntries []execenv.DirEntry
	listErr     error
	grepOutput  string
	grepErr     error
	globMatches []string
	globErr     error

	execCalls []stpExecCall
	listCalls []stpListCall
	grepCalls []stpGrepCall
	globCalls []stpGlobCall
}

func (e *stpEnv) ExecCommand(_ context.Context, command string, timeoutMS int, workDir string, _ map[string]string) (execenv.ExecResult, error) {
	e.execCalls = append(e.execCalls, stpExecCall{command: command, timeoutMS: timeoutMS, workDir: workDir})
	return e.execResult, e.execErr
}

func (e *stpEnv) ListDirectory(path string, depth int) ([]execenv.DirEntry, error) {
	e.listCalls = append(e.listCalls, stpListCall{path: path, depth: depth})
	return append([]execenv.DirEntry(nil), e.listEntries...), e.listErr
}

func (e *stpEnv) Grep(pattern, path, globFilter string, caseInsensitive bool, maxResults int, outputMode string) (string, error) {
	e.grepCalls = append(e.grepCalls, stpGrepCall{
		pattern: pattern,
		path:    path,
		glob:    globFilter,
		ci:      caseInsensitive,
		max:     maxResults,
		mode:    outputMode,
	})
	return e.grepOutput, e.grepErr
}

func (e *stpEnv) Glob(pattern, path string) ([]string, error) {
	e.globCalls = append(e.globCalls, stpGlobCall{pattern: pattern, path: path})
	return append([]string(nil), e.globMatches...), e.globErr
}

type stpToolResult struct {
	toolName   string
	callID     string
	output     string
	fullOutput string
	isError    bool
}

func stpProjectResult(result tool.ExecResult) stpToolResult {
	return stpToolResult{
		toolName:   result.ToolName,
		callID:     result.CallID,
		output:     result.Output,
		fullOutput: result.FullOutput,
		isError:    result.IsError,
	}
}

type stpRunResult struct {
	shell stpToolResult
	list  stpToolResult
	grep  stpToolResult
	glob  stpToolResult

	execCalls []stpExecCall
	listCalls []stpListCall
	grepCalls []stpGrepCall
	globCalls []stpGlobCall
}

func stpRunProgram(t *testing.T, p stpProgram) stpRunResult {
	t.Helper()
	env := &stpEnv{
		FakeEnv:     agenttest.FakeEnv{WorkDir: "/virtual/work"},
		execResult:  execenv.ExecResult{Stdout: p.stdout, Stderr: p.stderr, ExitCode: p.exitCode, TimedOut: p.timedOut, DurationMS: p.durationMS},
		listEntries: append([]execenv.DirEntry(nil), p.listEntries...),
		grepOutput:  p.grepOutput,
		globMatches: append([]string(nil), p.globMatches...),
	}
	switch p.execErrorKind {
	case 1:
		env.execErr = context.Canceled
	case 2:
		env.execErr = errSTPExec
	case 3:
		env.execErr = context.DeadlineExceeded
	}
	if p.listError {
		env.listErr = errSTPList
	}
	if p.grepError {
		env.grepErr = errSTPGrep
	}
	if p.globError {
		env.globErr = errSTPGlob
	}

	reg := tool.NewRegistry()
	if err := registerShellTools(reg, nil, &toolDeps{
		cmdTimeouts: func() (int, int) { return p.defaultTimeout, p.maxTimeout },
	}); err != nil {
		t.Fatalf("registerShellTools: %v", err)
	}

	call := func(name string, args map[string]any) stpToolResult {
		raw, err := json.Marshal(args)
		if err != nil {
			t.Fatalf("marshal %s args: %v", name, err)
		}
		return stpProjectResult(reg.ExecuteCall(context.Background(), env, llm.ToolCallData{
			ID:        "stp-" + name,
			Name:      name,
			Arguments: raw,
			Type:      "function",
		}))
	}

	result := stpRunResult{}
	result.shell = call("shell", map[string]any{
		"command":        p.command,
		"description":    p.description,
		"background":     p.background,
		"max_runtime_ms": p.maxRuntime,
	})
	result.list = call("list_dir", map[string]any{
		"path":   p.listPath,
		"depth":  p.listDepth,
		"offset": p.listOffset,
		"limit":  p.listLimit,
	})
	result.grep = call("grep", map[string]any{
		"pattern":          p.grepPattern,
		"path":             p.grepPath,
		"glob_filter":      p.grepGlob,
		"case_insensitive": p.grepCI,
		"max_results":      p.grepMax,
		"output_mode":      p.grepMode,
	})
	result.glob = call("glob", map[string]any{
		"pattern": p.globPattern,
		"path":    p.globPath,
	})
	result.execCalls = append([]stpExecCall(nil), env.execCalls...)
	result.listCalls = append([]stpListCall(nil), env.listCalls...)
	result.grepCalls = append([]stpGrepCall(nil), env.grepCalls...)
	result.globCalls = append([]stpGlobCall(nil), env.globCalls...)
	return result
}

func stpAssertProgram(t *testing.T, p stpProgram, got stpRunResult) {
	t.Helper()
	stpAssertShell(t, p, got.shell, got.execCalls)
	stpAssertList(t, p, got.list, got.listCalls)
	stpAssertGrep(t, p, got.grep, got.grepCalls)
	stpAssertGlob(t, p, got.glob, got.globCalls)
	for _, result := range []stpToolResult{got.shell, got.list, got.grep, got.glob} {
		if result.callID == "" || result.toolName == "" {
			t.Fatalf("partial tool result: %#v", result)
		}
		if !utf8.ValidString(result.output) || !utf8.ValidString(result.fullOutput) {
			t.Fatalf("tool result is not valid UTF-8: %#v", result)
		}
	}
}

func stpAssertShell(t *testing.T, p stpProgram, result stpToolResult, calls []stpExecCall) {
	t.Helper()
	if result.toolName != "shell" || result.callID != "stp-shell" {
		t.Fatalf("shell identity = %#v", result)
	}
	if p.maxRuntime < 0 {
		if !result.isError || !strings.Contains(result.fullOutput, "max_runtime_ms must be non-negative") {
			t.Fatalf("negative max runtime result = %#v", result)
		}
		if len(calls) != 0 {
			t.Fatalf("negative max runtime called executor: %#v", calls)
		}
		return
	}
	if p.background {
		if !result.isError || !strings.Contains(result.fullOutput, "background requires a streaming") {
			t.Fatalf("buffered background result = %#v", result)
		}
		if len(calls) != 0 {
			t.Fatalf("buffered background called executor: %#v", calls)
		}
		return
	}
	if len(calls) != 1 {
		t.Fatalf("shell executor calls = %#v, want one", calls)
	}
	wantTimeout := stpExpectedTimeout(p.defaultTimeout, p.maxTimeout, p.maxRuntime)
	if call := calls[0]; call.command != p.command || call.timeoutMS != wantTimeout || call.workDir != "" {
		t.Fatalf("shell executor call = %#v, want command=%q timeout=%d workdir=empty", call, p.command, wantTimeout)
	}
	wantError := p.execErrorKind != 0
	if result.isError != wantError {
		t.Fatalf("shell IsError = %v, want %v: %#v", result.isError, wantError, result)
	}
	wantOutput := stpExpectedBufferedOutput(p, wantTimeout)
	if result.fullOutput != wantOutput || result.output != wantOutput {
		t.Fatalf("shell output = %q / %q, want %q", result.output, result.fullOutput, wantOutput)
	}
}

func stpExpectedTimeout(defaultTimeout, maxTimeout, maxRuntime int) int {
	timeout := defaultTimeout
	if maxTimeout > 0 && timeout > maxTimeout {
		timeout = maxTimeout
	}
	if maxRuntime > 0 && maxRuntime < minShellMaxRuntimeMS {
		maxRuntime = minShellMaxRuntimeMS
	}
	if maxRuntime > 0 && (timeout == 0 || maxRuntime < timeout) {
		timeout = maxRuntime
	}
	return timeout
}

func stpExpectedBufferedOutput(p stpProgram, timeout int) string {
	var b strings.Builder
	if strings.TrimSpace(p.stdout) != "" {
		b.WriteString(p.stdout)
		if !strings.HasSuffix(p.stdout, "\n") {
			b.WriteByte('\n')
		}
	}
	if strings.TrimSpace(p.stderr) != "" {
		b.WriteString(p.stderr)
		if !strings.HasSuffix(p.stderr, "\n") {
			b.WriteByte('\n')
		}
	}
	if p.execErrorKind == 1 && !p.timedOut {
		b.WriteString("[ERROR: Command was canceled before completion. Partial output is shown above.]\n")
	} else if p.timedOut {
		fmt.Fprintf(&b, "[ERROR: Command timed out after %dms. Partial output is shown above.\nYou can retry with a longer timeout by setting the max_runtime_ms parameter.]\n", timeout)
	}
	fmt.Fprintf(&b, "exit_code=%d duration_ms=%d timed_out=%t\n", p.exitCode, p.durationMS, p.timedOut)
	return b.String()
}

func stpAssertList(t *testing.T, p stpProgram, result stpToolResult, calls []stpListCall) {
	t.Helper()
	if result.toolName != "list_dir" || result.callID != "stp-list_dir" {
		t.Fatalf("list identity = %#v", result)
	}
	if len(calls) != 1 {
		t.Fatalf("list calls = %#v, want one", calls)
	}
	wantDepth := p.listDepth
	if wantDepth <= 0 {
		wantDepth = 1
	}
	if call := calls[0]; call.path != p.listPath || call.depth != wantDepth {
		t.Fatalf("list call = %#v, want path=%q depth=%d", call, p.listPath, wantDepth)
	}
	if p.listError {
		if !result.isError || !strings.Contains(result.fullOutput, errSTPList.Error()) {
			t.Fatalf("list error result = %#v", result)
		}
		return
	}
	if result.isError {
		t.Fatalf("list success became error: %#v", result)
	}
	stpAssertBoundedListing(t, p, result.fullOutput)
	if result.output != result.fullOutput {
		t.Fatalf("small list was unexpectedly truncated: %#v", result)
	}
}

func stpAssertBoundedListing(t *testing.T, p stpProgram, output string) {
	t.Helper()
	offset := p.listOffset
	if offset <= 0 {
		offset = 0
	}
	limit := p.listLimit
	if limit <= 0 {
		limit = defaultListDirLimit
	}
	start := offset
	if start > len(p.listEntries) {
		start = len(p.listEntries)
	}
	end := start + limit
	if end > len(p.listEntries) {
		end = len(p.listEntries)
	}
	entries := p.listEntries[start:end]
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		lines = append(lines, stpRenderedDirEntry(entry))
	}
	prefix := strings.Join(lines, "\n")
	if prefix != "" {
		if !strings.HasPrefix(output, prefix+"\n\n") {
			t.Fatalf("listing prefix = %q, want records %q", output, prefix)
		}
		output = strings.TrimPrefix(output, prefix+"\n\n")
	}
	if end < len(p.listEntries) {
		if !strings.Contains(output, fmt.Sprintf("%d of %d entries (offset %d)", len(entries), len(p.listEntries), offset)) ||
			!strings.Contains(output, fmt.Sprintf("list_dir(offset=%d)", offset+len(entries))) {
			t.Fatalf("truncated listing footer = %q", output)
		}
		return
	}
	if offset > 0 {
		want := fmt.Sprintf("%d of %d entries (offset %d)", len(entries), len(p.listEntries), offset)
		if output != want {
			t.Fatalf("offset listing footer = %q, want %q", output, want)
		}
		return
	}
	want := fmt.Sprintf("%d entries", len(p.listEntries))
	if output != want {
		t.Fatalf("complete listing footer = %q, want %q", output, want)
	}
}

func stpRenderedDirEntry(entry execenv.DirEntry) string {
	line := entry.Name
	switch {
	case entry.IsDir:
		line += "/"
	case entry.IsSymlink:
		line += "@"
	case entry.IsExec:
		line += "*"
	}
	if !entry.IsDir {
		line += "\t" + strconv.FormatInt(entry.Size, 10)
	}
	return line
}

func stpAssertGrep(t *testing.T, p stpProgram, result stpToolResult, calls []stpGrepCall) {
	t.Helper()
	if result.toolName != "grep" || result.callID != "stp-grep" {
		t.Fatalf("grep identity = %#v", result)
	}
	if len(calls) != 1 {
		t.Fatalf("grep calls = %#v, want one", calls)
	}
	wantMax := p.grepMax
	if wantMax <= 0 {
		wantMax = 100
	}
	wantCall := stpGrepCall{pattern: p.grepPattern, path: p.grepPath, glob: p.grepGlob, ci: p.grepCI, max: wantMax, mode: p.grepMode}
	if calls[0] != wantCall {
		t.Fatalf("grep call = %#v, want %#v", calls[0], wantCall)
	}
	if p.grepError {
		// ExecuteCall preserves a handler's nonempty value as FullOutput even
		// when that handler also returns an error. grep deliberately returns the
		// partial search result with its error, unlike list_dir/glob's empty/nil
		// error values.
		if !result.isError || result.fullOutput != p.grepOutput || result.output != p.grepOutput {
			t.Fatalf("grep partial-error result = %#v, want %q", result, p.grepOutput)
		}
		return
	}
	if result.isError || result.fullOutput != p.grepOutput || result.output != p.grepOutput {
		t.Fatalf("grep result = %#v, want %q", result, p.grepOutput)
	}
}

func stpAssertGlob(t *testing.T, p stpProgram, result stpToolResult, calls []stpGlobCall) {
	t.Helper()
	if result.toolName != "glob" || result.callID != "stp-glob" {
		t.Fatalf("glob identity = %#v", result)
	}
	if len(calls) != 1 {
		t.Fatalf("glob calls = %#v, want one", calls)
	}
	wantCall := stpGlobCall{pattern: p.globPattern, path: p.globPath}
	if calls[0] != wantCall {
		t.Fatalf("glob call = %#v, want %#v", calls[0], wantCall)
	}
	if p.globError {
		if !result.isError || !strings.Contains(result.fullOutput, errSTPGlob.Error()) {
			t.Fatalf("glob error result = %#v", result)
		}
		return
	}
	want := strings.Join(p.globMatches, "\n")
	if result.isError || result.fullOutput != want || result.output != want {
		t.Fatalf("glob result = %#v, want %q", result, want)
	}
}
