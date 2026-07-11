//go:build serffuzz

package command

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
)

// FuzzExpandTemplateProgram drives a complete slash-command template through
// argument substitution, literal command spans, and local-file inlining. The
// command runner is a recording fake and every file lives below t.TempDir, so
// fuzz input cannot execute a shell command or read ambient files.
//
// Its oracle is intentionally about directive provenance, not merely no-panic:
// only the three literal template commands may reach the fake runner; directive
// syntax arriving through arguments, command stdout, or file contents must stay
// inert. The program also asserts positional quoting semantics, containment,
// output bounds, and deterministic replay of one template against one fixture.
func FuzzExpandTemplateProgram(f *testing.F) {
	for _, seed := range [][]byte{
		nil,
		{0},
		{1, 2, 3, 4},
		{0xff, 0x00, 0x4a, 0x91},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 64 {
			raw = raw[:64]
		}
		fixture := newExpandTemplateProgramFixture(t, raw)

		first := fixture.run(t)
		second := fixture.run(t)
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("template expansion was not deterministic:\nfirst=%#v\nsecond=%#v", first, second)
		}

		fixture.assertTrace(t, first)
		fixture.assertAtFileBoundaries(t)
		fixture.assertShellSplitContract(t)
		fixture.assertCancelledContext(t)
	})
}

type expandTemplateProgramFixture struct {
	root   string
	token  string
	args   string
	note   string
	secret string
	long   string
}

func newExpandTemplateProgramFixture(t *testing.T, raw []byte) expandTemplateProgramFixture {
	t.Helper()
	token := "empty"
	if len(raw) > 0 {
		token = hex.EncodeToString(raw)
	}
	fixture := expandTemplateProgramFixture{
		root:   t.TempDir(),
		token:  token,
		args:   "alpha \"bravo charlie\" 'delta echo' " + token + " \"!`injected command`\" @secret.txt",
		note:   "note:" + token + " !`file command` @secret.txt",
		secret: "SECRET-MUST-NOT-BE-INLINED-" + token,
		long:   strings.Repeat("f", maxInlineBytes+23),
	}

	expandTemplateProgramWriteFile(t, filepath.Join(fixture.root, "note.txt"), []byte(fixture.note))
	expandTemplateProgramWriteFile(t, filepath.Join(fixture.root, "secret.txt"), []byte(fixture.secret))
	expandTemplateProgramWriteFile(t, filepath.Join(fixture.root, "binary.bin"), append([]byte("binary-"+token), 0))
	expandTemplateProgramWriteFile(t, filepath.Join(fixture.root, "long.txt"), []byte(fixture.long))
	if err := os.Mkdir(filepath.Join(fixture.root, "directory"), 0o700); err != nil {
		t.Fatalf("create directory fixture: %v", err)
	}
	return fixture
}

func expandTemplateProgramWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write fixture %s: %v", path, err)
	}
}

type expandTemplateProgramTrace struct {
	Output string
	Calls  []expandTemplateProgramCall
}

type expandTemplateProgramCall struct {
	Command    string
	TimeoutMS  int
	WorkingDir string
	NilEnv     bool
}

type expandTemplateProgramEnv struct {
	*agenttest.DenyEnv
	responses map[string]expandTemplateProgramResponse
	calls     []expandTemplateProgramCall
}

type expandTemplateProgramResponse struct {
	result execenv.ExecResult
	err    error
}

var _ execenv.ExecutionEnvironment = (*expandTemplateProgramEnv)(nil)

func (e *expandTemplateProgramEnv) ExecCommand(_ context.Context, command string, timeoutMS int, workingDir string, env map[string]string) (execenv.ExecResult, error) {
	e.calls = append(e.calls, expandTemplateProgramCall{
		Command:    command,
		TimeoutMS:  timeoutMS,
		WorkingDir: workingDir,
		NilEnv:     env == nil,
	})
	response, ok := e.responses[command]
	if !ok {
		return execenv.ExecResult{}, fmt.Errorf("unexpected literal command %q", command)
	}
	return response.result, response.err
}

func (f expandTemplateProgramFixture) newEnv() *expandTemplateProgramEnv {
	longCommandOutput := strings.Repeat("c", maxInlineBytes+17) + "\n"
	return &expandTemplateProgramEnv{
		DenyEnv: &agenttest.DenyEnv{WorkDir: f.root},
		responses: map[string]expandTemplateProgramResponse{
			"literal command": {
				result: execenv.ExecResult{Stdout: "command:" + f.token + "\n@secret.txt !`output directive`"},
			},
			"partial command": {
				result: execenv.ExecResult{Stdout: "partial:" + f.token + "\n", ExitCode: 1},
				err:    errors.New("scripted command failure"),
			},
			"large command": {
				result: execenv.ExecResult{Stdout: longCommandOutput},
			},
		},
	}
}

func (f expandTemplateProgramFixture) body() string {
	return "args=<$ARGUMENTS>; pos=<$1|$2|$3|$4|$5|$6|$9|$10>; " +
		"cmd=!`literal command`; partial=!`partial command`; large=!`large command`; " +
		"note= @note.txt; binary= @binary.bin; long= @long.txt; missing= @missing.txt; directory= @directory; " +
		"blocked= @../secret.txt; parenthesized=(@note.txt); email=author@secret.txt."
}

func (f expandTemplateProgramFixture) run(t *testing.T) expandTemplateProgramTrace {
	t.Helper()
	env := f.newEnv()
	output, err := Expand(context.Background(), f.body(), f.args, env)
	if err != nil {
		t.Fatalf("Expand program: %v", err)
	}
	return expandTemplateProgramTrace{
		Output: output,
		Calls:  append([]expandTemplateProgramCall(nil), env.calls...),
	}
}

func (f expandTemplateProgramFixture) assertTrace(t *testing.T, trace expandTemplateProgramTrace) {
	t.Helper()
	output := trace.Output
	if !strings.Contains(output, "args=<"+f.args+">") {
		t.Fatalf("full argument substitution lost inert input: %q", output)
	}
	wantPositions := "pos=<alpha|bravo charlie|delta echo|" + f.token + "|!`injected command`|@secret.txt||$10>"
	if !strings.Contains(output, wantPositions) {
		t.Fatalf("positional substitution = %q, want segment %q", output, wantPositions)
	}
	if !strings.Contains(output, "cmd=command:"+f.token+"\n@secret.txt !`output directive`") {
		t.Fatalf("literal command output was not substituted exactly: %q", output)
	}
	if !strings.Contains(output, "partial=partial:"+f.token) {
		t.Fatalf("nonzero command stdout was not retained: %q", output)
	}

	longCommand := strings.Repeat("c", maxInlineBytes+17)
	wantCommandBound := longCommand[:maxInlineBytes] + fmt.Sprintf("\n...[truncated, %d bytes total]", len(longCommand))
	if !strings.Contains(output, "large="+wantCommandBound) {
		t.Fatalf("large command output was not bounded with its size marker")
	}
	wantFileBound := f.long[:maxInlineBytes] + fmt.Sprintf("\n...[truncated, %d bytes total]", len(f.long))
	if !strings.Contains(output, "long= "+wantFileBound) {
		t.Fatalf("large file output was not bounded with its size marker")
	}

	if strings.Count(output, f.note) != 2 {
		t.Fatalf("literal note file was not inlined twice without re-scanning: count=%d output=%q", strings.Count(output, f.note), output)
	}
	if strings.Contains(output, f.secret) {
		t.Fatalf("argument, command-output, or file-content directive read secret data: %q", output)
	}
	for _, marker := range []string{
		"[binary file binary.bin:",
		"[error reading missing.txt:",
		"[error reading directory:",
		"[refusing @../secret.txt: escapes working directory]",
		"parenthesized=(" + f.note + ");",
		"email=author@secret.txt.",
	} {
		if !strings.Contains(output, marker) {
			t.Fatalf("expansion missing semantic marker %q in %q", marker, output)
		}
	}

	wantCalls := []string{"literal command", "partial command", "large command"}
	if len(trace.Calls) != len(wantCalls) {
		t.Fatalf("executed %d commands, want only literal template commands %q: %#v", len(trace.Calls), wantCalls, trace.Calls)
	}
	for i, call := range trace.Calls {
		if call.Command != wantCalls[i] || call.TimeoutMS != 10_000 || call.WorkingDir != f.root || !call.NilEnv {
			t.Fatalf("command call %d = %#v, want literal command=%q timeout=10000 workdir=%q nil-env", i, call, wantCalls[i], f.root)
		}
	}
}

func (f expandTemplateProgramFixture) assertAtFileBoundaries(t *testing.T) {
	t.Helper()
	for _, prefix := range []string{"", " ", "\t", "\n", "\r", "("} {
		got, err := Expand(context.Background(), prefix+"@note.txt", "", f.newEnv())
		if err != nil {
			t.Fatalf("Expand boundary %q: %v", prefix, err)
		}
		if want := prefix + f.note; got != want {
			t.Fatalf("Expand boundary %q = %q, want %q", prefix, got, want)
		}
	}

	got, err := Expand(context.Background(), "author@note.txt", "", f.newEnv())
	if err != nil {
		t.Fatalf("Expand email boundary: %v", err)
	}
	if got != "author@note.txt" {
		t.Fatalf("mid-token @file candidate was not left literal: %q", got)
	}
}

func (f expandTemplateProgramFixture) assertShellSplitContract(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		input string
		want  []string
	}{
		{input: "", want: nil},
		{input: " \t\n", want: nil},
		{input: "alpha beta", want: []string{"alpha", "beta"}},
		{input: `"alpha beta" 'gamma delta'`, want: []string{"alpha beta", "gamma delta"}},
		{input: `'' ""`, want: []string{"", ""}},
		{input: `alpha\ beta`, want: []string{"alpha beta"}},
		{input: `"quote\" dollar\$ slash\\ other\q"`, want: []string{"quote\" dollar$ slash\\ other\\q"}},
		{input: `'unterminated`, want: []string{"unterminated"}},
		{input: `"unterminated`, want: []string{"unterminated"}},
		{input: `trailing\`, want: []string{`trailing\`}},
		{input: "token-" + f.token + ` "two words"`, want: []string{"token-" + f.token, "two words"}},
	} {
		if got := shellSplit(tc.input); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("shellSplit(%q) = %#v, want %#v", tc.input, got, tc.want)
		}
	}
}

func (f expandTemplateProgramFixture) assertCancelledContext(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	env := f.newEnv()
	got, err := Expand(ctx, "!`literal command` @note.txt", f.args, env)
	if err == nil || got != "" {
		t.Fatalf("cancelled Expand = (%q, %v), want empty output and context error", got, err)
	}
	if len(env.calls) != 0 {
		t.Fatalf("cancelled Expand executed literal commands: %#v", env.calls)
	}
}
