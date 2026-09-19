package dev

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureResearchStderr runs runResearch while capturing os.Stderr, which is
// where the dev subcommands write usage (see covstmt's flag usage). runResearch
// takes no writer, so the test captures the stream it actually writes to
// instead of a buffer it would never touch.
func captureResearchStderr(t *testing.T, args []string) (int, string) {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()
	code := runResearch(args)
	w.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	return code, buf.String()
}

func TestResearchDispatch_RequiresSubcommand(t *testing.T) {
	code, stderr := captureResearchStderr(t, nil)
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	for _, name := range []string{"oracle", "rollout"} {
		if !strings.Contains(stderr, name) {
			t.Errorf("usage does not name %q: %q", name, stderr)
		}
	}
}

func TestResearchDispatchUnknownSubcommand(t *testing.T) {
	code, stderr := captureResearchStderr(t, []string{"frobnicate"})
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(stderr, "frobnicate") {
		t.Errorf("stderr does not name the unknown subcommand: %q", stderr)
	}
}

func TestResearchOracleCmd_MissingStateDirFails(t *testing.T) {
	// No --state-dir and no default resolution in this environment:
	// the cmd must fail with exit 1 and a message, not panic.
	code := researchOracleCmd([]string{"--state-dir", "/nonexistent-definitely-missing"})
	if code == 0 {
		t.Fatal("oracle on missing state dir returned 0")
	}
}

func TestResearchRolloutCmd_FlagErrorsFailFast(t *testing.T) {
	// A live pass without the opt-in env var must exit nonzero without
	// executing anything, and the guard's refusal must reach stderr. The
	// env is pinned empty so ambient EVENER_LIVE_TESTS cannot make the
	// guard pass (agent/internal/liveeval.OptInEnv); this also forbids
	// t.Parallel, which is fine — the dispatch path is process-global.
	t.Setenv("EVENER_LIVE_TESTS", "")
	code, stderr := captureResearchStderr(t, []string{
		"rollout", "--env-dir", "research/environments",
		"--env", "smoke-fix",
		"--model", "lunaroute/deepseek-4.1-flash",
		"--live",
		"--run-dir", t.TempDir(),
	})
	if code == 0 {
		t.Fatal("live rollout without EVENER_LIVE_TESTS must not exit 0")
	}
	if !strings.Contains(stderr, "EVENER_LIVE_TESTS") {
		t.Errorf("stderr does not name the guard env var: %q", stderr)
	}
}

func TestResearchRolloutCmd_NonLivePassRequiresOptIn(t *testing.T) {
	// The missing-binary evidence: without the guard, a rollout launched
	// WITHOUT --live still execs the harness binary, records an infra-fail
	// row, and exits 0. With the guard the refusal is a hard stop: nonzero
	// exit, the guard's message on stderr, and no row written.
	t.Setenv("EVENER_LIVE_TESTS", "")
	runDir := t.TempDir()
	code, stderr := captureResearchStderr(t, []string{
		"rollout", "--env-dir", "../../research/environments",
		"--env", "smoke-fix",
		"--binary", "/nonexistent/definitely-missing-evener",
		"--run-dir", runDir,
	})
	if code == 0 {
		t.Fatal("non-live rollout without EVENER_LIVE_TESTS must not exit 0")
	}
	if !strings.Contains(stderr, "EVENER_LIVE_TESTS") {
		t.Errorf("stderr does not name the guard env var: %q", stderr)
	}
	if _, err := os.Stat(filepath.Join(runDir, "runs.jsonl")); !os.IsNotExist(err) {
		t.Errorf("runs.jsonl was written despite the refusal")
	}
}

func TestResearchOracleCmd_EmptyStateBaseFails(t *testing.T) {
	// An existing-but-empty base must refuse (nonzero) with a message
	// naming the layouts tried — never emit a verdict on an empty corpus.
	// The existing missing-directory case is covered by
	// TestResearchOracleCmd_MissingStateDirFails.
	code := researchOracleCmd([]string{"--state-dir", t.TempDir()})
	if code == 0 {
		t.Fatal("oracle on an existing-but-empty state base returned 0")
	}
}
