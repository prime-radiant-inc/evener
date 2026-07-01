package cmdutil

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

func TestModelRefQualifiedWithMissingPart(t *testing.T) {
	if got := (ModelRef{Provider: "openai", Model: "gpt"}).Qualified(); got != "openai/gpt" {
		t.Fatalf("Qualified full=%q", got)
	}
	// Missing model trims to just the provider (no dangling slash).
	if got := (ModelRef{Provider: "openai"}).Qualified(); got != "openai" {
		t.Fatalf("Qualified provider-only=%q, want openai", got)
	}
	if got := (ModelRef{Model: "gpt"}).Qualified(); got != "gpt" {
		t.Fatalf("Qualified model-only=%q, want gpt", got)
	}
	if got := (ModelRef{}).Qualified(); got != "" {
		t.Fatalf("Qualified empty=%q, want empty", got)
	}
}

func TestResolveModelRefNoModel(t *testing.T) {
	_, err := ResolveModelRef("", "", "", "")
	if err == nil || !strings.Contains(err.Error(), "provider/model") {
		t.Fatalf("err=%v, want no-model guidance", err)
	}
}

func TestResolveResumeModelRefNoModel(t *testing.T) {
	_, err := ResolveResumeModelRef("", "", "", "")
	if err == nil || !strings.Contains(err.Error(), "provider/model") {
		t.Fatalf("err=%v, want no-model guidance", err)
	}
}

func TestInstanceEndpoint(t *testing.T) {
	cfg := providercfg.Config{
		Instances: []providercfg.InstanceConfig{
			{Name: "kc", BaseURL: " https://api.kimi.com/coding/v1 ", APIKey: " sk-abc "},
		},
	}
	baseURL, apiKey := instanceEndpoint(cfg, "kc")
	if baseURL != "https://api.kimi.com/coding/v1" || apiKey != "sk-abc" {
		t.Fatalf("instanceEndpoint=%q,%q (must trim)", baseURL, apiKey)
	}
	baseURL, apiKey = instanceEndpoint(cfg, "missing")
	if baseURL != "" || apiKey != "" {
		t.Fatalf("instanceEndpoint(missing)=%q,%q, want empty", baseURL, apiKey)
	}
}

func TestGitOriginURLFromDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()

	// A non-repo directory yields "".
	if got := GitOriginURLFromDir(dir); got != "" {
		t.Fatalf("GitOriginURLFromDir(non-repo)=%q, want empty", got)
	}

	run := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	// A repo without an origin remote still yields "".
	if got := GitOriginURLFromDir(dir); got != "" {
		t.Fatalf("GitOriginURLFromDir(no-origin)=%q, want empty", got)
	}
	run("remote", "add", "origin", "https://example.com/repo.git")
	if got := GitOriginURLFromDir(dir); got != "https://example.com/repo.git" {
		t.Fatalf("GitOriginURLFromDir=%q, want the origin URL", got)
	}
}

func TestResolveSessionMeta(t *testing.T) {
	dir := t.TempDir()

	// Empty state dir: resume-last has nothing to resume.
	if _, err := ResolveSessionMeta(dir, "", true); err == nil || !strings.Contains(err.Error(), "no saved sessions") {
		t.Fatalf("resumeLast empty err=%v, want no-saved-sessions", err)
	}
	// Unknown session id surfaces the load error.
	if _, err := ResolveSessionMeta(dir, "01MISSING", false); err == nil {
		t.Fatal("expected error loading unknown session")
	}

	meta := schema.SessionMeta{ID: "01SESSIONAAAAAAAAAAAAAAAAAA"}
	if err := schema.SaveSessionMeta(dir, meta); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}

	got, err := ResolveSessionMeta(dir, meta.ID, false)
	if err != nil {
		t.Fatalf("ResolveSessionMeta by id: %v", err)
	}
	if got.ID != meta.ID {
		t.Fatalf("by id got %q, want %q", got.ID, meta.ID)
	}

	last, err := ResolveSessionMeta(dir, "", true)
	if err != nil {
		t.Fatalf("ResolveSessionMeta resumeLast: %v", err)
	}
	if last.ID != meta.ID {
		t.Fatalf("resumeLast got %q, want %q", last.ID, meta.ID)
	}
}

func TestDefaultStateRoot(t *testing.T) {
	t.Setenv(envvars.SERFStateDir.Name, "/explicit/state")
	if got := DefaultStateRoot(); got != "/explicit/state" {
		t.Fatalf("DefaultStateRoot with env=%q", got)
	}
	t.Setenv(envvars.SERFStateDir.Name, "")
	t.Setenv("HOME", "/home/tester")
	if got := DefaultStateRoot(); got != "/home/tester/.serf" {
		t.Fatalf("DefaultStateRoot home fallback=%q", got)
	}
}

func TestDefaultConfigRootAndSubdirs(t *testing.T) {
	t.Setenv(envvars.XDGConfigHome.Name, "/xdg")
	if got := DefaultConfigRoot(); got != "/xdg/serf" {
		t.Fatalf("DefaultConfigRoot with XDG=%q", got)
	}
	if got := DefaultSkillsDir(); got != "/xdg/serf/skills" {
		t.Fatalf("DefaultSkillsDir=%q", got)
	}
	if got := DefaultPluginsRoot(); got != "/xdg/serf/plugins" {
		t.Fatalf("DefaultPluginsRoot=%q", got)
	}

	t.Setenv(envvars.XDGConfigHome.Name, "")
	t.Setenv("HOME", "/home/tester")
	if got := DefaultConfigRoot(); got != "/home/tester/.config/serf" {
		t.Fatalf("DefaultConfigRoot home fallback=%q", got)
	}
}

func TestEnsureUserConfigDirs(t *testing.T) {
	root := t.TempDir()
	t.Setenv(envvars.XDGConfigHome.Name, root)

	if err := EnsureUserConfigDirs(); err != nil {
		t.Fatalf("EnsureUserConfigDirs: %v", err)
	}
	for _, dir := range []string{DefaultConfigRoot(), DefaultSkillsDir(), DefaultPluginsRoot()} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Fatalf("expected dir %q to exist: err=%v", dir, err)
		}
	}
}

func TestEnsureUserConfigDirsSurfacesMkdirError(t *testing.T) {
	// Point the config root at a path whose parent is a regular file so
	// MkdirAll cannot create it.
	base := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(base, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Setenv(envvars.XDGConfigHome.Name, base)
	if err := EnsureUserConfigDirs(); err == nil {
		t.Fatal("expected mkdir error under a regular file")
	}
}

func TestStartCPUProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cpu.prof")
	stop, err := StartCPUProfile(path)
	if err != nil {
		t.Fatalf("StartCPUProfile: %v", err)
	}
	stop()
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		t.Fatalf("cpu profile not written: err=%v", err)
	}

	if _, err := StartCPUProfile(filepath.Join(t.TempDir(), "missing", "cpu.prof")); err == nil {
		t.Fatal("expected error creating profile under a missing dir")
	}
}

func TestStartTrace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.out")
	stop, err := StartTrace(path)
	if err != nil {
		t.Fatalf("StartTrace: %v", err)
	}
	stop()
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		t.Fatalf("trace not written: err=%v", err)
	}

	if _, err := StartTrace(filepath.Join(t.TempDir(), "missing", "trace.out")); err == nil {
		t.Fatal("expected error creating trace under a missing dir")
	}
}

// AttachAPILogger installs a logger and returns a closer; when the log path
// cannot be created it degrades to a no-op closer and warns instead of failing.
func TestAttachAPILogger(t *testing.T) {
	client := llm.NewClient()
	stateDir := t.TempDir()
	var warnings bytes.Buffer

	closeFn, err := AttachAPILogger(client, stateDir, &warnings)
	if err != nil {
		t.Fatalf("AttachAPILogger: %v", err)
	}
	if closeFn == nil {
		t.Fatal("expected a non-nil close function")
	}
	if err := closeFn(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "api.jsonl")); err != nil {
		t.Fatalf("api.jsonl not created: %v", err)
	}
}

func TestAttachAPILoggerDegradesWhenPathUnusable(t *testing.T) {
	client := llm.NewClient()
	// stateDir is a regular file, so filepath.Join(stateDir, "api.jsonl")
	// cannot be opened; logging must degrade to a warn + no-op closer.
	stateFile := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(stateFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var warnings bytes.Buffer

	closeFn, err := AttachAPILogger(client, stateFile, &warnings)
	if err != nil {
		t.Fatalf("AttachAPILogger must not hard-fail: %v", err)
	}
	if err := closeFn(); err != nil {
		t.Fatalf("no-op close: %v", err)
	}
	if !strings.Contains(warnings.String(), "API logging disabled") {
		t.Fatalf("warnings=%q, want a disabled-logging warning", warnings.String())
	}
}
