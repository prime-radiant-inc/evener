package hub

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

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
	t.Setenv("EVENER_OFFLINE", "0")
	for _, key := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "EVENER_PROVIDERS_CONFIG", "EVENER_CREDENTIALS_CONFIG", "OPENAI_BASE_URL", "ANTHROPIC_BASE_URL", "AWS_SHARED_CREDENTIALS_FILE", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(key, "canary")
	}
	root := t.TempDir()
	env := tutorialChildEnvironment(t, root)
	child := exec.Command("sh", "-c", `test "$EVENER_OFFLINE" = 1`)
	child.Env = env
	if err := child.Run(); err != nil {
		t.Fatalf("tutorial child must disable registry refresh despite ambient override: %v", err)
	}
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
		stack := startHubStackWithOptions(t, tutorialProviderConfig(provider.BaseURL()), "fake/"+fakellm.ModelID, filepath.Join(liveStackBinaries(t, repo), "evener"), hubStackOptions{ChildEnv: tutorialChildEnvironment(t, owned)})
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

func TestTutorialCheckpointRecordsEvidence(t *testing.T) {
	out := t.TempDir()
	t.Setenv("EVENER_TUTORIAL_OUT", out)
	checkpoint(t, "entry", []tourEvidence{{Name: "independent-oracle", Passed: true}}, func() error { return nil })
	body, err := os.ReadFile(filepath.Join(out, "entry.evidence.json"))
	if err != nil {
		t.Fatal(err)
	}
	var evidence []struct {
		Name   string `json:"name"`
		Passed bool   `json:"passed"`
	}
	if err := json.Unmarshal(body, &evidence); err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 1 || evidence[0].Name != "independent-oracle" || !evidence[0].Passed {
		t.Fatal("missing checkpoint evidence")
	}
}

type tourEvidence struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
}

func captureCheckpoint(evidence []tourEvidence, capture func() error) error {
	if len(evidence) == 0 {
		return errors.New("checkpoint has no evidence")
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
	if out := os.Getenv("EVENER_TUTORIAL_OUT"); out != "" {
		body, err := json.Marshal(evidence)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, id+".evidence.json"), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("checkpoint %s passed", id)
}
func tutorialChildEnvironment(t *testing.T, root string) []string {
	t.Helper()
	base := filepath.Join(root, "host-temp")
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	return []string{"PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "HOME=" + root, "EVENER_OFFLINE=1", "EVENER_HOST_TEMP_BASES=" + base,
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
	if (tc.Scenario != "receipt" && tc.Scenario != "daily" && tc.Scenario != "configuration" && tc.Scenario != "extensions") || (tc.Surface != "cli" && tc.Surface != "web") {
		t.Fatal("unsupported tutorial row", tc)
	}
	coverage := "receipt-repair"
	if tc.Scenario == "extensions" {
		coverage = "local-extensions-stdio"
		if tc.Surface == "cli" {
			runExtensionsCLI(t, candidateDir)
		} else {
			runExtensionsWeb(t, candidateDir)
		}
	} else if tc.Scenario == "configuration" {
		coverage = "effective-launch-bounds"
		if tc.Surface == "cli" {
			runConfigCLI(t, candidateDir)
		} else {
			runConfigWeb(t, candidateDir)
		}
	} else if tc.Scenario == "daily" {
		if tc.Surface == "cli" {
			coverage = "sequential-resume"
			runDailyCLI(t, candidateDir)
		} else {
			coverage = "interactive-turn-controls"
			runDailyWeb(t, candidateDir)
		}
	} else if tc.Surface == "cli" {
		runReceiptCLI(t, candidateDir)
	} else {
		runReceiptWeb(t, candidateDir)
	}
	body, err := json.Marshal(map[string]any{"scenario": tc.Scenario, "surface": tc.Surface, "coverage": coverage, "executed": true, "passed": true, "checkpoints": []string{"entry", "action", "verified"}})
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
	root := filepath.Join(string(filepath.Separator), "tmp", fmt.Sprintf("evener-tutorial-%x", id))
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

var tutorialBrowserCache = os.Getenv("PLAYWRIGHT_BROWSERS_PATH")

// The driver receives private control data over pipes, never argv or candidates.
type tutorialDriver struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	encode *json.Encoder
	decode *json.Decoder
	id     int
	waited bool
}

func openTutorialDriver(ctx context.Context, t *testing.T, args map[string]any) *tutorialDriver {
	t.Helper()
	// Keep the deadline, but let cleanup close stdin before killing the driver
	// when a scenario assertion fails and cancels its observer context.
	deadline, _ := ctx.Deadline()
	driverCtx, cancel := context.WithDeadline(context.Background(), deadline)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(driverCtx, "node", "../../docs-site/scripts/tours/driver.mjs")
	cmd.Env = tutorialChildEnvironment(t, t.TempDir())
	cmd.Env = append(cmd.Env, "PLAYWRIGHT_BROWSERS_PATH="+tutorialBrowserCache)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = io.Discard // Browser diagnostics can contain private URLs.
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	d := &tutorialDriver{cmd: cmd, input: input, encode: json.NewEncoder(input), decode: json.NewDecoder(output)}
	t.Cleanup(func() {
		if d.waited {
			return
		}
		_ = input.Close()
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Error("tutorial driver exit:", err)
			}
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Error("tutorial driver did not close")
		}
	})
	d.act(t, "open", args)
	return d
}
func (d *tutorialDriver) act(t *testing.T, action string, args any) {
	t.Helper()
	d.id++
	if err := d.encode.Encode(map[string]any{"id": d.id, "action": action, "args": args}); err != nil {
		t.Fatal("driver input:", err)
	}
	var reply struct {
		ID   int  `json:"id"`
		OK   bool `json:"ok"`
		Data struct {
			Error string `json:"error"`
		} `json:"data"`
	}
	if err := d.decode.Decode(&reply); err != nil {
		t.Fatal("driver reply:", err)
	}
	if reply.ID != d.id || !reply.OK {
		t.Fatalf("web driver %s failed: %s", action, reply.Data.Error)
	}
	if action == "close" {
		_ = d.input.Close()
		err := d.cmd.Wait()
		d.waited = true
		if err != nil {
			t.Fatal("tutorial driver exit:", err)
		}
	}
}
func (d *tutorialDriver) capture(t *testing.T, out, id string) func() error {
	return func() error {
		d.act(t, "capture", map[string]any{"id": id})
		f, err := os.Open(filepath.Join(out, id+".png"))
		if err != nil {
			return err
		}
		defer f.Close()
		img, err := png.Decode(f)
		if err != nil {
			return err
		}
		if img.Bounds().Dx() != 1280 || img.Bounds().Dy() != 900 {
			return errors.New("wrong screenshot dimensions")
		}
		return nil
	}
}
