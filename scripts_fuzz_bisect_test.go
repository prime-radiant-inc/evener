package evener_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestFuzzBisectFindsTheIntroducingCommit is the integration contract for
// scripts/fuzz/fuzz-bisect.sh, ported from the shell selftest the dev-tooling
// selftest wave used to run (deleted with the rest of that wave; see kata
// dev-tooling-selftest-removal). A bisect tool cannot be honestly tested with
// stubs -- it IS git bisect plus a real `go test` replay -- so this builds a
// throwaway git repo whose fuzz target crashes on one input only AFTER a known
// commit, and drives the real script against it. The only stub is the
// registry source (EVENER_FUZZ_RUNNER, standing in for run-fuzz.sh --list);
// git history, the git bisect run itself, and every go test replay are real.
func TestFuzzBisectFindsTheIntroducingCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("fuzz-bisect integration test: builds a throwaway git repo and drives real git bisect")
	}
	requireFuzzScriptTools(t)

	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.email", "fuzzbisect-test@example.com")
	runGit(t, repo, "config", "user.name", "fuzzbisect-test")

	writeAuditScriptFixture(t, filepath.Join(repo, "go.mod"), "module example.com/bisecttest\n\ngo 1.25.0\n")
	docGo := "package bisecttest\n"
	writeAuditScriptFixture(t, filepath.Join(repo, "doc.go"), docGo)

	// writeTarget writes the fuzz target; boomBody is injected only from the
	// culprit commit on. The target also carries a "tripwire" crash on a
	// DIFFERENT input than the one under bisection, present at EVERY revision
	// including the good one, with a matching corpus entry committed from the
	// base commit on. If fuzz-bisect's -run filter ever stopped isolating to
	// the probe function alone, the tripwire corpus entry would fire at
	// --good too and the bracket-confirmation step below would misjudge the
	// good end as bad -- a real filter-isolation regression this guards.
	writeTarget := func(boomBody string) {
		src := fmt.Sprintf(`package bisecttest

import "testing"

func FuzzBoom(f *testing.F) {
	f.Fuzz(func(t *testing.T, b []byte) {
		if string(b) == "tripwire" {
			panic("tripwire")
		}
		%s
	})
}
`, boomBody)
		writeAuditScriptFixture(t, filepath.Join(repo, "fuzz_test.go"), src)
	}

	writeTarget("_ = b")
	writeAuditScriptFixture(t, filepath.Join(repo, "testdata", "fuzz", "FuzzBoom", "sibling_tripwire"),
		"go test fuzz v1\n[]byte(\"tripwire\")\n")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "base: harmless fuzz target + tripwire sibling")
	good := gitOutput(t, repo, "rev-parse", "HEAD")

	// The crash message deliberately contains "unknown" -- an old, over-broad
	// skip heuristic grepped crash text for that word rather than go's own
	// [build failed]/[setup failed] markers. If skip detection ever regresses
	// to matching arbitrary failure text, this crash misreads as "skip" and
	// the bracket-confirmation step misjudges the bad end -- a real
	// regression this guards.
	writeTarget(`if string(b) == "boom" { panic("unknown message type: boom") }`)
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "introduce the boom crash")
	culprit := gitOutput(t, repo, "rev-parse", "HEAD")

	// A couple of later, innocent commits so the bad end is not the culprit itself.
	docGo += "// later\n"
	writeAuditScriptFixture(t, filepath.Join(repo, "doc.go"), docGo)
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "later: unrelated change")

	docGo += "// later2\n"
	writeAuditScriptFixture(t, filepath.Join(repo, "doc.go"), docGo)
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "later2: unrelated change")

	// The crasher corpus file (the boom input), in Go's fuzz format.
	crasherDir := t.TempDir()
	crasher := filepath.Join(crasherDir, "crasher")
	writeAuditScriptFixture(t, crasher, "go test fuzz v1\n[]byte(\"boom\")\n")

	// Stub registry: the one native target, module "." package "." name FuzzBoom.
	runner := filepath.Join(t.TempDir(), "runner-stub.sh")
	writeExecutableFixture(t, runner, "#!/usr/bin/env bash\n"+
		"for a in \"$@\"; do [ \"$a\" = \"--list\" ] && { echo \"native:.:.:FuzzBoom\"; exit 0; }; done\n"+
		"exit 0\n")

	// EVENER_FUZZ_TAGS is emptied: the throwaway module has no build tags,
	// and the real default (evenerfuzz) would still build, but empty keeps
	// this test independent of that tag.
	script := repoScriptPath(t, "scripts/fuzz/fuzz-bisect.sh")
	cmd := exec.Command("bash", script,
		"--target", ".:FuzzBoom",
		"--crasher", crasher,
		"--good", good,
		"--bad", "HEAD",
	)
	cmd.Env = append(os.Environ(),
		"EVENER_FUZZ_REPO_ROOT="+repo,
		"EVENER_FUZZ_RUNNER="+runner,
		"EVENER_FUZZ_TAGS=",
	)
	out, err := cmd.CombinedOutput()
	t.Logf("fuzz-bisect.sh output:\n%s", out)
	if err != nil {
		t.Fatalf("fuzz-bisect.sh: %v", err)
	}

	if !strings.Contains(string(out), culprit) {
		t.Errorf("fuzz-bisect.sh did not name the introducing commit (%s) in its output", culprit)
	}
	// git >=2.55 quotes the bisect term ("is the first 'bad' commit", git.git
	// 0c0f93e7fa); older gits print "is the first bad commit". Match both, as
	// the script itself does.
	if !regexp.MustCompile(`(?i)is the first '?bad'? commit`).MatchString(string(out)) {
		t.Errorf("fuzz-bisect.sh did not report bisect convergence")
	}

	// The tree is restored afterward: no lingering bisect state (git bisect
	// reset ran). "git bisect log" fails once no bisect is in progress.
	if err := exec.Command("git", "-C", repo, "bisect", "log").Run(); err == nil {
		t.Error("fuzz-bisect.sh left bisect state behind (git bisect log still succeeds)")
	}

	// Self-containment (the per-step replay does not depend on a repo script
	// git's own checkouts would delete at old commits) is inherent to this
	// fixture: fuzz-bisect.sh and run-fuzz.sh are never written into repo, so
	// the convergence proven above already exercises that property.
}
