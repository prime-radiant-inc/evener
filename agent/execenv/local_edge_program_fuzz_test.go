//go:build serffuzz

package execenv

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/afero"
)

// FuzzLocalEdgeContractProgram drives pure local-file and command-preparation
// edge contracts against a fresh temp fixture. The only os/exec probe names a
// guaranteed-missing absolute file, so it fails before any child process can be
// started; all other work stays inside the fixture filesystem.
func FuzzLocalEdgeContractProgram(f *testing.F) {
	for _, seed := range [][]byte{
		nil,
		{0},
		{1, 2, 3},
		{0xff, 0x4a, 0x00},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, program []byte) {
		if len(program) > 48 {
			program = program[:48]
		}
		first := runLocalEdgeContractProgram(t, program)
		second := runLocalEdgeContractProgram(t, program)
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("local edge contracts are not deterministic:\nfirst=%#v\nsecond=%#v", first, second)
		}
	})
}

type localEdgeTrace struct {
	Fuzzy      []string
	Formats    []string
	Lookups    []bool
	VenvPaths  []string
	GrepError  string
	OSFallback string
}

func runLocalEdgeContractProgram(t *testing.T, program []byte) localEdgeTrace {
	t.Helper()
	root := t.TempDir()
	trace := localEdgeTrace{}
	token := localEdgeToken(program)

	if got := findFuzzyMatch("first\n  fuzzy    "+token+"  \nlast", "fuzzy "+token); got != "  fuzzy    "+token+"  " {
		t.Fatalf("single-line fuzzy match = %q", got)
	}
	if got := findFuzzyMatch("first\n  multi "+token+"\nsecond line\nlast", "multi "+token+"\nsecond   line"); got != "  multi "+token+"\nsecond line" {
		t.Fatalf("multi-line fuzzy match = %q", got)
	}
	if got := findFuzzyMatch("content", " \t\n "); got != "" {
		t.Fatalf("blank fuzzy old string = %q", got)
	}
	trace.Fuzzy = append(trace.Fuzzy, "single", "multi", "blank")

	if got := nearestFileRegion("alpha beta gamma\nlast line", "alpha beta\nrequested second line"); got != "alpha beta gamma\nlast line" {
		t.Fatalf("nearest region clamp = %q", got)
	}
	if got := nearestFileRegion("unrelated words", "different tokens"); got != "" {
		t.Fatalf("unrelated nearest region = %q", got)
	}
	if got := nearestFileRegion("content", "\t "); got != "" {
		t.Fatalf("blank nearest region = %q", got)
	}
	if got := nearestFileRegion("first line\nalpha beta gamma", "alpha beta\nrequested trailing line"); got != "alpha beta gamma" {
		t.Fatalf("nearest region tail clamp = %q", got)
	}
	if lineSimilarity("", "x") != 0 || lineSimilarity("alpha beta", "alpha") != 1 || lineSimilarity("alpha", "beta") != 0 || lineSimilarity(" ", "\t") != 0 {
		t.Fatal("line similarity boundary contract failed")
	}

	formatCases := []struct {
		path string
		data []byte
		want string
	}{
		{"unknown.bin", []byte{0xff, 0xd8, 0xff}, "jpeg"},
		{"unknown.bin", []byte("GIF89a"), "gif"},
		{"unknown.bin", []byte("not-media"), ""},
		{"report.unknown", []byte("%PDF-1.7"), "pdf"},
		{"report.pdf", []byte("not a magic header"), "pdf"},
	}
	for _, tc := range formatCases {
		got := detectImageFormat(tc.path, tc.data)
		if tc.want == "pdf" {
			got = detectDocumentFormat(tc.path, tc.data)
		}
		if got != tc.want {
			t.Fatalf("format %q = %q, want %q", tc.path, got, tc.want)
		}
		trace.Formats = append(trace.Formats, got)
	}

	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("make lookup fixture: %v", err)
	}
	nonExecutable := filepath.Join(bin, "not-executable")
	if err := os.WriteFile(nonExecutable, []byte("fixture"), 0o644); err != nil {
		t.Fatalf("write non-executable: %v", err)
	}
	if err := os.Mkdir(filepath.Join(bin, "directory"), 0o755); err != nil {
		t.Fatalf("make lookup directory: %v", err)
	}
	executable := filepath.Join(bin, "tool")
	if err := os.WriteFile(executable, []byte("fixture"), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	lookupCases := []struct {
		name string
		env  []string
		want bool
	}{
		{"", []string{"PATH=" + bin}, false},
		{"dir/tool", []string{"PATH=" + bin}, false},
		{"tool", nil, false},
		{"not-executable", []string{"PATH=" + bin}, false},
		{"directory", []string{"PATH=" + bin}, false},
		{"tool", []string{"PATH=" + bin}, true},
	}
	for _, tc := range lookupCases {
		got, ok := lookPathInEnv(tc.name, tc.env)
		if ok != tc.want || ok && got != executable {
			t.Fatalf("lookPathInEnv(%q,%v)=(%q,%t), want executable=%q ok=%t", tc.name, tc.env, got, ok, executable, tc.want)
		}
		trace.Lookups = append(trace.Lookups, ok)
	}
	cwdTool := filepath.Join(root, "cwd-tool")
	if err := os.WriteFile(cwdTool, []byte("fixture"), 0o755); err != nil {
		t.Fatalf("write cwd lookup tool: %v", err)
	}
	t.Chdir(root)
	if got, ok := lookPathInEnv("cwd-tool", []string{"PATH=" + string(os.PathListSeparator)}); !ok || got != "cwd-tool" {
		t.Fatalf("lookPathInEnv empty PATH component = (%q,%t), want cwd-tool/true", got, ok)
	}

	venvRoot := filepath.Join(root, "venv-root")
	if err := os.MkdirAll(filepath.Join(venvRoot, ".venv", "bin"), 0o755); err != nil {
		t.Fatalf("make venv fixture: %v", err)
	}
	if got := injectLocalVenvPath(nil, []string{venvRoot}); got != nil {
		t.Fatalf("empty env injection = %v, want nil", got)
	}
	if env := []string{"PATH=/base"}; !reflect.DeepEqual(injectLocalVenvPath(append([]string(nil), env...), []string{" ", ""}), env) {
		t.Fatalf("blank roots unexpectedly changed PATH")
	}
	withPath := injectLocalVenvPath([]string{"PATH=/base"}, []string{venvRoot})
	if want := filepath.Join(venvRoot, ".venv", "bin") + string(os.PathListSeparator) + "/base"; !reflect.DeepEqual(withPath, []string{"PATH=" + want}) {
		t.Fatalf("venv PATH = %v, want %q", withPath, want)
	}
	withoutPath := injectLocalVenvPath([]string{"VISIBLE=" + token}, []string{venvRoot})
	if len(withoutPath) != 2 || !strings.HasPrefix(withoutPath[1], "PATH="+filepath.Join(venvRoot, ".venv", "bin")) {
		t.Fatalf("venv PATH without existing path = %v", withoutPath)
	}
	// The assertions above retain the exact fixture paths. Keep only semantic
	// facts in the double-run trace because t.TempDir intentionally changes its
	// generated absolute directory on every invocation.
	trace.VenvPaths = append(trace.VenvPaths, "prepends-existing-path", "creates-path")

	env := NewLocalExecutionEnvironment(root)
	if _, ok := env.commands().(systemCommandRuntimeFactory); !ok {
		t.Fatalf("default command factory = %T, want system factory", env.commands())
	}
	zero := &LocalExecutionEnvironment{}
	if zero.filesystem() == nil {
		t.Fatal("zero-value environment did not install its default filesystem")
	}
	// Native grep is explicitly best effort: WalkDir errors are ignored by its
	// callback, including a missing requested base. A regression must not turn
	// this into an ambient filesystem-dependent failure.
	if got, err := env.grepNative("needle", filepath.Join(root, "missing"), "", false, 1, ""); err != nil || got != "" {
		t.Fatalf("grepNative missing root = (%q, %v), want empty success", got, err)
	} else {
		trace.GrepError = "best-effort-empty"
	}
	if _, err := env.Glob("[", ""); err == nil {
		t.Fatal("Glob malformed pattern unexpectedly succeeded")
	}

	// Mutator error paths use an injected filesystem instead of host permissions.
	// Read-only wrapping preserves the source file for reads while rejecting all
	// creates, rewrites, and destination-parent construction.
	mutatorRoot := "/fixture/mutator"
	mem := afero.NewMemMapFs()
	if err := mem.MkdirAll(mutatorRoot, 0o755); err != nil {
		t.Fatalf("make mutator root: %v", err)
	}
	if err := afero.WriteFile(mem, filepath.Join(mutatorRoot, "existing.txt"), []byte("old value"), 0o644); err != nil {
		t.Fatalf("write mutator source: %v", err)
	}
	mutator := NewLocalExecutionEnvironment(mutatorRoot).SetFs(afero.NewReadOnlyFs(mem))
	if err := mutator.WriteFileRaw("nested/new.txt", []byte("new"), 0o644); err == nil {
		t.Fatal("read-only WriteFileRaw unexpectedly succeeded")
	}
	if _, err := mutator.EditFile("existing.txt", "old value", "new value", false); err == nil {
		t.Fatal("read-only EditFile unexpectedly succeeded")
	}
	if err := mutator.RenamePath("", "nested/new.txt"); err == nil {
		t.Fatal("RenamePath empty source unexpectedly succeeded")
	}
	if err := mutator.RenamePath("existing.txt", "nested/new.txt"); err == nil {
		t.Fatal("read-only RenamePath unexpectedly succeeded")
	}

	// The probe path is deliberately absent. exec.Cmd.Start therefore returns an
	// error without launching a child, exercising the documented fallback result.
	originalCommandContext := execCommandContext
	defer func() { execCommandContext = originalCommandContext }()
	missing := filepath.Join(root, "missing-os-version-probe")
	execCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, missing)
	}
	if got, want := resolveOSVersion(), runtime.GOOS+"/"+runtime.GOARCH; got != want {
		t.Fatalf("resolveOSVersion fallback = %q, want %q", got, want)
	} else {
		trace.OSFallback = got
	}
	// OSVersion is intentionally shared across environments. Reset its test-only
	// cache for each replay, then prove the injected failed probe is cached rather
	// than reaching the host or issuing another command.
	osVersionOnce = sync.Once{}
	osVersionValue = ""
	if got, want := env.OSVersion(), runtime.GOOS+"/"+runtime.GOARCH; got != want {
		t.Fatalf("OSVersion fallback = %q, want %q", got, want)
	}
	if got, want := env.OSVersion(), runtime.GOOS+"/"+runtime.GOARCH; got != want {
		t.Fatalf("OSVersion cached fallback = %q, want %q", got, want)
	}
	return trace
}

func localEdgeToken(program []byte) string {
	if len(program) == 0 {
		return "empty"
	}
	return base64.RawURLEncoding.EncodeToString(program)
}
