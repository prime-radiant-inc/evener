package hub

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"primeradiant.com/evener/test/e2e/fakellm"
)

func TestTutorialCheckpointRejectsFailedEvidence(t *testing.T) {
	captureCalled := false
	err := captureCheckpoint([]tourEvidence{{Name: "checker", Passed: false}}, func() error { captureCalled = true; return nil })
	if captureCalled {
		t.Fatal("capture accepted failed evidence")
	}
	if err == nil {
		t.Fatal("failed evidence accepted")
	}
}

func TestTutorialChildEnvironment(t *testing.T) {
	for _, key := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "EVENER_PROVIDERS_CONFIG", "EVENER_CREDENTIALS_CONFIG", "OPENAI_BASE_URL", "ANTHROPIC_BASE_URL", "AWS_SHARED_CREDENTIALS_FILE", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(key, "canary")
	}
	root := t.TempDir()
	env := tutorialChildEnvironment(root)
	seen := map[string]bool{}
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		seen[key] = true
		if strings.Contains(value, "canary") {
			t.Fatalf("ambient variable leaked: %s", key)
		}
		if key == "HOME" || strings.HasPrefix(key, "XDG_") || key == "TMPDIR" {
			rel, err := filepath.Rel(root, value)
			if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
				t.Fatalf("unowned %s", key)
			}
		}
	}
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "TMPDIR"} {
		if !seen[key] {
			t.Fatalf("missing isolated root: %s", key)
		}
	}
}

func TestTutorialCleanupOwnership(t *testing.T) {
	sibling := t.TempDir()
	decoy := filepath.Join(sibling, "keep")
	if err := os.WriteFile(decoy, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	proc := exec.Command("sleep", "120")
	if err := proc.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = proc.Process.Kill(); _ = proc.Wait() }()
	var owned string
	var ownedPID int
	t.Run("owned", func(t *testing.T) {
		owned = tutorialOwnedRoot(t)
		provider, err := fakellm.New()
		if err != nil {
			t.Fatal(err)
		}
		defer provider.Close()
		repo, err := filepath.Abs("../..")
		if err != nil {
			t.Fatal(err)
		}
		stack := startHubStackWithOptions(t, fmt.Sprintf("default = \"fake\"\n[providers.fake]\nbase = \"openai-compatible\"\nbase_url = %q\napi_key = \"fakellm-not-a-secret\"\n", provider.BaseURL()), "fake/"+fakellm.ModelID, filepath.Join(liveStackBinaries(t, repo), "evener"), hubStackOptions{ChildEnv: tutorialChildEnvironment(owned)})
		ownedPID = stack.pid
	})
	if proc, err := os.FindProcess(ownedPID); err == nil && proc.Signal(syscall.Signal(0)) == nil {
		t.Fatal("owned hub survived cleanup")
	}

	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Fatal("owned root remains")
	}
	if _, err := os.Stat(decoy); err != nil {
		t.Fatal(err)
	}
	if err := proc.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("sibling process stopped", err)
	}
}

type tourCase struct{ Scenario, Surface string }
type tourEvidence struct {
	Name   string
	Passed bool
}

func captureCheckpoint(evidence []tourEvidence, capture func() error) error {
	if len(evidence) == 0 {
		return fmt.Errorf("checkpoint has no evidence")
	}
	for _, e := range evidence {
		if !e.Passed {
			return fmt.Errorf("failed evidence: %s", e.Name)
		}
	}
	return capture()
}
func checkpoint(t *testing.T, id string, evidence []tourEvidence, capture func() error) {
	t.Helper()
	if err := captureCheckpoint(evidence, capture); err != nil {
		t.Fatalf("checkpoint %s: %v", id, err)
	}
	t.Logf("checkpoint %s passed", id)
}
func tutorialChildEnvironment(root string) []string {
	return []string{"PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "HOME=" + root,
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "XDG_STATE_HOME=" + filepath.Join(root, "state"),
		"XDG_CACHE_HOME=" + filepath.Join(root, "cache"), "XDG_DATA_HOME=" + filepath.Join(root, "data"),
		"TMPDIR=" + root, "PYTHONDONTWRITEBYTECODE=1"}
}
func TestTutorialTours(t *testing.T) {
	if os.Getenv("EVENER_TUTORIAL_TOURS") != "1" {
		t.Skip("opt-in tutorial tours")
	}
	if testing.Short() {
		t.Fatal("explicit tours cannot run with -short")
	}
	tc := tourCase{Scenario: os.Getenv("EVENER_TUTORIAL_SCENARIO"), Surface: os.Getenv("EVENER_TUTORIAL_SURFACES")}
	out := os.Getenv("EVENER_TUTORIAL_OUT")
	if out == "" {
		t.Fatal("candidate directory required")
	}
	runTour(t, tc, out)
}
func runTour(t *testing.T, tc tourCase, candidateDir string) {
	if tc != (tourCase{Scenario: "receipt", Surface: "cli"}) {
		t.Fatal("unsupported tutorial row", tc)
	}
	runReceiptCLI(t, candidateDir)
	body, err := json.Marshal(map[string]any{"scenario": tc.Scenario, "surface": tc.Surface, "executed": true, "passed": true, "checkpoints": []string{"entry", "action", "verified"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidateDir, "result.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
}

// Fixed-width owned paths keep CLI prompt-size diagnostics reproducible without
// rewriting observed output. Only this minted root is registered for cleanup.
func tutorialOwnedRoot(t *testing.T) string {
	t.Helper()
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join("/tmp", fmt.Sprintf("evener-tutorial-%x", id))
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	return root
}
