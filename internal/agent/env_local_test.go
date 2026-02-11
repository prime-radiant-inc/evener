package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalExecutionEnvironment_ExecCommand_TimesOutAndKillsProcessGroup(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	res, err := env.ExecCommand(ctx, "sleep 30", 50, "", nil)
	dur := time.Since(start)

	if err == nil {
		t.Fatalf("expected error, got nil (res=%+v)", res)
	}
	if !res.TimedOut {
		t.Fatalf("expected timed_out=true, got %+v", res)
	}
	if res.ExitCode != 124 {
		t.Fatalf("exit_code: got %d want 124", res.ExitCode)
	}
	if dur > 3*time.Second {
		t.Fatalf("expected timeout handling to return quickly; took %s", dur)
	}
}

func TestLocalExecutionEnvironment_ExecCommand_ContextCancel_KillsProcessGroup(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	var res ExecResult
	var err error
	start := time.Now()
	go func() {
		res, err = env.ExecCommand(ctx, "sleep 30", 30_000, "", nil)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("ExecCommand did not return promptly after context cancel")
	}
	if err == nil {
		t.Fatalf("expected error, got nil (res=%+v)", res)
	}
	if !res.TimedOut {
		t.Fatalf("expected timed_out=true on cancel, got %+v", res)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("expected cancel handling to return quickly; took %s", time.Since(start))
	}
}

func TestFilteredEnv_ExcludesSensitiveVars(t *testing.T) {
	t.Setenv("MY_API_KEY", "secret")
	t.Setenv("MY_SECRET", "secret2")
	env := filteredEnv(nil)
	for _, kv := range env {
		if strings.HasPrefix(kv, "MY_API_KEY=") || strings.HasPrefix(kv, "MY_SECRET=") {
			t.Fatalf("sensitive env var leaked: %q", kv)
		}
	}
	// sanity check: PATH should be present in most environments
	foundPath := false
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			foundPath = true
		}
	}
	if !foundPath {
		t.Fatalf("expected PATH to be present in filtered env")
	}
}

func TestLocalExecutionEnvironment_ReadWriteEditFile(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	if _, err := env.WriteFile("a.txt", "hello\nworld\n"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := env.ReadFile("a.txt", nil, nil)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(got, "1 | hello") {
		t.Fatalf("expected line numbers, got:\n%s", got)
	}
	if _, err := env.EditFile("a.txt", "world", "WORLD", false); err != nil {
		t.Fatalf("EditFile: %v", err)
	}
	b, _ := os.ReadFile(dir + "/a.txt")
	if !strings.Contains(string(b), "WORLD") {
		t.Fatalf("edit did not apply: %q", string(b))
	}
}

func TestReadFile_ImageReturnsBase64(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)

	// Write a minimal PNG (8-byte header is enough to detect).
	pngHeader := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x01}
	os.WriteFile(filepath.Join(dir, "test.png"), pngHeader, 0644)

	result, err := env.ReadFile("test.png", nil, nil)
	if err != nil {
		t.Fatalf("ReadFile for PNG should not error: %v", err)
	}
	if !strings.Contains(result, "base64") {
		t.Fatalf("expected base64 in output, got: %q", result)
	}
	if !strings.Contains(result, "image") {
		t.Fatalf("expected 'image' indicator in output, got: %q", result)
	}
}

func TestReadFile_NonImageBinaryStillErrors(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)

	// Write a generic binary file (not an image).
	os.WriteFile(filepath.Join(dir, "data.bin"), []byte{0x00, 0x01, 0x02, 0x03}, 0644)

	_, err := env.ReadFile("data.bin", nil, nil)
	if err == nil {
		t.Fatal("expected error for non-image binary file")
	}
	if !strings.Contains(err.Error(), "binary") {
		t.Fatalf("expected 'binary' in error message, got: %v", err)
	}
}

func TestEditFile_FuzzyMatchWhitespace(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	// Write a file with specific indentation.
	original := "func foo() {\n\tbar := 1\n\tbaz  :=  2\n}\n"
	if _, err := env.WriteFile("a.go", original); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Try to edit with slightly different whitespace (spaces vs tabs, extra spaces).
	// Exact match fails, but fuzzy (whitespace-normalized) match should succeed.
	result, err := env.EditFile("a.go", "baz := 2", "baz := 42", false)
	if err != nil {
		t.Fatalf("EditFile with whitespace difference should succeed via fuzzy match: %v", err)
	}
	if !strings.Contains(result, "whitespace normalization") {
		t.Errorf("expected note about whitespace normalization in result: %q", result)
	}
	// Verify the replacement was applied.
	b, _ := os.ReadFile(filepath.Join(dir, "a.go"))
	if !strings.Contains(string(b), "baz := 42") {
		t.Fatalf("edit did not apply fuzzy match: %q", string(b))
	}
}

func TestEditFile_FuzzyMatch_CompletelyWrongString_StillFails(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	if _, err := env.WriteFile("a.go", "func foo() {}\n"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := env.EditFile("a.go", "completely_nonexistent_string", "replacement", false)
	if err == nil {
		t.Fatal("expected error for completely wrong old_string, got nil")
	}
}

func TestLocalExecutionEnvironment_ListDirectory_Depth(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	if _, err := env.WriteFile("a.txt", "a"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := env.WriteFile("sub/b.txt", "b"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ents1, err := env.ListDirectory("", 1)
	if err != nil {
		t.Fatalf("ListDirectory depth=1: %v", err)
	}
	seen := map[string]bool{}
	for _, e := range ents1 {
		seen[e.Name] = true
	}
	if !seen["a.txt"] || !seen["sub"] {
		t.Fatalf("depth=1 entries: %+v", ents1)
	}
	if seen["sub/b.txt"] {
		t.Fatalf("did not expect nested entries at depth=1: %+v", ents1)
	}

	ents2, err := env.ListDirectory("", 2)
	if err != nil {
		t.Fatalf("ListDirectory depth=2: %v", err)
	}
	seen = map[string]bool{}
	for _, e := range ents2 {
		seen[e.Name] = true
	}
	if !seen["sub/b.txt"] {
		t.Fatalf("expected nested entry at depth=2: %+v", ents2)
	}
}

func TestEnvVarPolicy_InheritNone(t *testing.T) {
	t.Setenv("SERF_TEST_MARKER", "should_not_appear")

	env := NewLocalExecutionEnvironment(t.TempDir())
	env.EnvPolicy = EnvPolicyNone

	ctx := context.Background()
	res, err := env.ExecCommand(ctx, "env", 5000, "", nil)
	if err != nil {
		t.Fatalf("ExecCommand: %v", err)
	}
	if strings.Contains(res.Stdout, "SERF_TEST_MARKER=") {
		t.Fatal("inherit-none should not pass through parent env vars")
	}
	// PATH won't be inherited either (bash may set its own default)
	if strings.Contains(res.Stdout, "HOME=") {
		t.Fatal("inherit-none should not include HOME")
	}
}

func TestEnvVarPolicy_InheritNone_AllowsExtraVars(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	env.EnvPolicy = EnvPolicyNone

	ctx := context.Background()
	extra := map[string]string{"MY_CUSTOM": "hello"}
	res, err := env.ExecCommand(ctx, "env", 5000, "", extra)
	if err != nil {
		t.Fatalf("ExecCommand: %v", err)
	}
	if !strings.Contains(res.Stdout, "MY_CUSTOM=hello") {
		t.Fatal("inherit-none should still allow explicitly passed vars")
	}
}

func TestEnvVarPolicy_CoreOnly(t *testing.T) {
	t.Setenv("SERF_TEST_MARKER", "should_not_appear")

	env := NewLocalExecutionEnvironment(t.TempDir())
	env.EnvPolicy = EnvPolicyCoreOnly

	ctx := context.Background()
	res, err := env.ExecCommand(ctx, "env", 5000, "", nil)
	if err != nil {
		t.Fatalf("ExecCommand: %v", err)
	}
	out := res.Stdout
	if !strings.Contains(out, "PATH=") {
		t.Fatal("core-only should include PATH")
	}
	if !strings.Contains(out, "HOME=") {
		t.Fatal("core-only should include HOME")
	}
	if strings.Contains(out, "SERF_TEST_MARKER=") {
		t.Fatal("core-only should not include arbitrary parent vars")
	}
}

func TestEnvVarPolicy_CoreOnly_AllowsExtraVars(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	env.EnvPolicy = EnvPolicyCoreOnly

	ctx := context.Background()
	extra := map[string]string{"MY_CUSTOM": "hello"}
	res, err := env.ExecCommand(ctx, "env", 5000, "", extra)
	if err != nil {
		t.Fatalf("ExecCommand: %v", err)
	}
	if !strings.Contains(res.Stdout, "MY_CUSTOM=hello") {
		t.Fatal("core-only should allow explicitly passed extra vars")
	}
}

func TestEnvVarPolicy_All(t *testing.T) {
	t.Setenv("MY_API_KEY", "secret123")

	env := NewLocalExecutionEnvironment(t.TempDir())
	env.EnvPolicy = EnvPolicyAll

	ctx := context.Background()
	res, err := env.ExecCommand(ctx, "env", 5000, "", nil)
	if err != nil {
		t.Fatalf("ExecCommand: %v", err)
	}
	// All policy should include even sensitive vars that Default would filter
	if !strings.Contains(res.Stdout, "MY_API_KEY=secret123") {
		t.Fatal("all policy should include sensitive vars like API keys")
	}
}

func TestEnvVarPolicy_Default_FiltersSensitive(t *testing.T) {
	t.Setenv("MY_API_KEY", "secret123")
	t.Setenv("SERF_TEST_MARKER", "should_appear")

	env := NewLocalExecutionEnvironment(t.TempDir())
	// EnvPolicy zero value is EnvPolicyDefault

	ctx := context.Background()
	res, err := env.ExecCommand(ctx, "env", 5000, "", nil)
	if err != nil {
		t.Fatalf("ExecCommand: %v", err)
	}
	if strings.Contains(res.Stdout, "MY_API_KEY=") {
		t.Fatal("default policy should filter sensitive vars")
	}
	if !strings.Contains(res.Stdout, "SERF_TEST_MARKER=should_appear") {
		t.Fatal("default policy should pass through non-sensitive vars")
	}
}

func TestGrep_FallbackWithoutRipgrep(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello world\ngoodbye world\nhello again\n"), 0o644)

	env := NewLocalExecutionEnvironment(dir)
	// Test the native fallback directly
	result, err := env.grepNative("hello", dir, "", false, 100)
	if err != nil {
		t.Fatalf("grepNative: %v", err)
	}
	if !strings.Contains(result, "hello.txt") {
		t.Fatalf("expected match in hello.txt, got: %q", result)
	}
	if !strings.Contains(result, "hello world") {
		t.Fatalf("expected 'hello world' in output, got: %q", result)
	}
}

func TestGrepNative_CaseInsensitiveAndGlob(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.go"), []byte("Hello World\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello world\n"), 0o644)

	env := NewLocalExecutionEnvironment(dir)

	// Case insensitive should match both files
	result, err := env.grepNative("HELLO", dir, "", true, 100)
	if err != nil {
		t.Fatalf("case-insensitive: %v", err)
	}
	if !strings.Contains(result, "test.go") || !strings.Contains(result, "test.txt") {
		t.Fatalf("case-insensitive should match both files, got: %q", result)
	}

	// Glob filter should restrict to *.go only
	result, err = env.grepNative("hello", dir, "*.go", false, 100)
	if err != nil {
		t.Fatalf("glob filter: %v", err)
	}
	if strings.Contains(result, "test.txt") {
		t.Fatalf("glob *.go should not match .txt, got: %q", result)
	}
}

func TestGrepNative_SkipsHiddenDirs(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".hidden"), 0o755)
	os.WriteFile(filepath.Join(dir, ".hidden", "secret.txt"), []byte("hello hidden\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "visible.txt"), []byte("hello visible\n"), 0o644)

	env := NewLocalExecutionEnvironment(dir)
	result, err := env.grepNative("hello", dir, "", false, 100)
	if err != nil {
		t.Fatalf("grepNative: %v", err)
	}
	if strings.Contains(result, "secret.txt") {
		t.Fatalf("should skip hidden dirs, got: %q", result)
	}
	if !strings.Contains(result, "visible.txt") {
		t.Fatalf("should match visible.txt, got: %q", result)
	}
}

func TestGrepNative_SkipsBinaryFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "binary.bin"), []byte("hello\x00world\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "text.txt"), []byte("hello world\n"), 0o644)

	env := NewLocalExecutionEnvironment(dir)
	result, err := env.grepNative("hello", dir, "", false, 100)
	if err != nil {
		t.Fatalf("grepNative: %v", err)
	}
	if strings.Contains(result, "binary.bin") {
		t.Fatalf("should skip binary files, got: %q", result)
	}
	if !strings.Contains(result, "text.txt") {
		t.Fatalf("should match text.txt, got: %q", result)
	}
}

func TestGrepNative_MaxResults(t *testing.T) {
	dir := t.TempDir()
	// Create file with many matching lines
	var content strings.Builder
	for i := 0; i < 20; i++ {
		content.WriteString("match line\n")
	}
	os.WriteFile(filepath.Join(dir, "many.txt"), []byte(content.String()), 0o644)

	env := NewLocalExecutionEnvironment(dir)
	result, err := env.grepNative("match", dir, "", false, 5)
	if err != nil {
		t.Fatalf("grepNative: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(result), "\n")
	if len(lines) > 5 {
		t.Fatalf("expected at most 5 results, got %d", len(lines))
	}
}

func TestGrepNative_InvalidRegex(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644)

	env := NewLocalExecutionEnvironment(dir)
	_, err := env.grepNative("[invalid", dir, "", false, 100)
	if err == nil {
		t.Fatal("expected error for invalid regex")
	}
	if !strings.Contains(err.Error(), "invalid regex") {
		t.Fatalf("expected 'invalid regex' error, got: %v", err)
	}
}

func TestExecCommand_UsesNonLoginShell(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	ctx := context.Background()

	// $0 in a login shell is "-bash" (with leading dash), in a non-login shell it's "/bin/bash" or "bash".
	// A login shell also sources ~/.bash_profile which can introduce side effects.
	res, err := env.ExecCommand(ctx, "echo $0", 5000, "", nil)
	if err != nil {
		t.Fatalf("ExecCommand: %v", err)
	}
	// Non-login shell should report /bin/bash, not -bash.
	out := strings.TrimSpace(res.Stdout)
	if strings.HasPrefix(out, "-") {
		t.Fatalf("expected non-login shell, but $0 = %q (leading dash = login shell)", out)
	}
}

func TestEnvVarPolicy_CoreOnly_IncludesLanguageToolchainPaths(t *testing.T) {
	// Set language toolchain vars and verify they pass through CoreOnly policy.
	t.Setenv("CARGO_HOME", "/home/user/.cargo")
	t.Setenv("NVM_DIR", "/home/user/.nvm")
	t.Setenv("RUSTUP_HOME", "/home/user/.rustup")
	t.Setenv("PYENV_ROOT", "/home/user/.pyenv")

	env := NewLocalExecutionEnvironment(t.TempDir())
	env.EnvPolicy = EnvPolicyCoreOnly

	ctx := context.Background()
	res, err := env.ExecCommand(ctx, "env", 5000, "", nil)
	if err != nil {
		t.Fatalf("ExecCommand: %v", err)
	}
	for _, v := range []string{"CARGO_HOME=", "NVM_DIR=", "RUSTUP_HOME=", "PYENV_ROOT="} {
		if !strings.Contains(res.Stdout, v) {
			t.Errorf("CoreOnly policy should include %s", v)
		}
	}
}

func TestLocalExecutionEnvironment_InitializeCleanup(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	if err := env.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	env.Cleanup()
	// Should not panic or error
}
