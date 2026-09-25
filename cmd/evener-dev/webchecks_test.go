package dev

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func startWebChecks(t *testing.T, launcher *fakeLauncher) *testGate {
	t.Helper()
	return startGate(t, newWebChecksGate(), launcher, make(chan os.Signal, 4))
}

// typecheck, test and lint are independent readers of the same sources, so all
// three start at once and the verdicts print in that fixed order.
func TestWebChecksRunTogetherAndReportInOrder(t *testing.T) {
	launcher := newFakeLauncher()
	tg := startWebChecks(t, launcher)
	var started []*fakeGuard
	for range webChecks {
		started = append(started, launcher.awaitStart(t))
	}
	launcher.assertNoStart(t, "every check had started")
	// Finish them in reverse: the verdict order is fixed, not arrival order.
	for _, g := range slices.Backward(started) {
		g.exit <- 0
	}
	if r := tg.await(t); r.status != 0 || r.keep {
		t.Fatalf("result = %+v, want 0 and the scratch removed", r)
	}
	if want := "PASS  web-typecheck\nPASS  web-test\nPASS  web-lint\n"; tg.stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", tg.stdout.String(), want)
	}
}

// An interrupt TERMs every running check: none of them is exempt.
func TestWebChecksInterruptTermsEveryCheck(t *testing.T) {
	launcher := newFakeLauncher()
	tg := startWebChecks(t, launcher)
	var started []*fakeGuard
	for range webChecks {
		started = append(started, launcher.awaitStart(t))
	}
	tg.signals <- syscall.SIGTERM
	for _, g := range started {
		select {
		case <-g.terminated:
		case <-time.After(tripwire):
			t.Fatalf("the interrupted gate never TERMed web-%s", g.name)
		}
		g.exit <- 143
	}
	if r := tg.await(t); r.status != 143 || !r.keep {
		t.Fatalf("result = %+v, want 143 and the scratch kept", r)
	}
}

func TestWebChecksFailureReplaysOnlyTheFailingLog(t *testing.T) {
	launcher := newFakeLauncher()
	launcher.output["typecheck"] = "src/app.tsx(3,1): error TS2322\n"
	launcher.output["lint"] = "lint chatter\n"
	tg := startWebChecks(t, launcher)
	for range webChecks {
		g := launcher.awaitStart(t)
		status := 0
		if g.name == "typecheck" {
			status = 2
		}
		g.exit <- status
	}
	r := tg.await(t)
	if r.status != 2 || !r.keep {
		t.Fatalf("result = %+v, want typecheck's 2 and the scratch kept", r)
	}
	if !strings.Contains(tg.stderr.String(), "FAIL  web-typecheck (exit 2)") || !strings.Contains(tg.stdout.String(), "error TS2322") {
		t.Fatalf("stdout = %s\nstderr = %s", tg.stdout.String(), tg.stderr.String())
	}
	if strings.Contains(tg.stdout.String(), "lint chatter") {
		t.Fatalf("a passing check's log was replayed: %s", tg.stdout.String())
	}
}

// Each check is `npm run <check>` in the frontend, contained in private roots
// under its own scratch directory, with Node's compile cache off. Nothing is
// built first and nothing needs a private Go home.
func TestWebChecksLaunchEachCheckContained(t *testing.T) {
	launcher := newFakeLauncher()
	tg := startWebChecks(t, launcher)
	for range webChecks {
		launcher.awaitStart(t).exit <- 0
	}
	tg.await(t)
	if launcher.built {
		t.Fatal("the web checks built the frontend")
	}
	for _, check := range webChecks {
		spec := launcher.specs[check]
		if !slices.Equal(spec.argv, []string{"npm", "run", check}) || spec.dir != frontendDir || spec.privateGoHome {
			t.Errorf("%s spec = %+v", check, spec)
		}
		env := map[string]string{}
		for _, entry := range spec.env {
			k, v, _ := strings.Cut(entry, "=")
			env[k] = v
		}
		if env["NODE_DISABLE_COMPILE_CACHE"] != "1" {
			t.Errorf("%s NODE_DISABLE_COMPILE_CACHE = %q", check, env["NODE_DISABLE_COMPILE_CACHE"])
		}
		root := filepath.Join(tg.gate.scratch, check)
		for _, name := range []string{"HOME", "TMPDIR", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
			if !strings.HasPrefix(env[name], root+string(os.PathSeparator)) {
				t.Errorf("%s %s = %q, want inside %s", check, name, env[name], root)
			}
		}
	}
}

func TestWebChecksRunInTheirOwnProcessGroups(t *testing.T) {
	for _, check := range webChecks {
		if !webCheckSpec(check, t.TempDir()).group {
			t.Errorf("web-%s is not started in its own process group", check)
		}
	}
}
