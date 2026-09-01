package evener_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestFuzzOracleAuditClassifiesMutations is the integration contract for
// scripts/fuzz/fuzz-oracle-audit.sh, ported from the shell selftest the
// dev-tooling selftest wave used to run (deleted with the rest of that wave;
// see kata dev-tooling-selftest-removal). Like fuzz-bisect, the audit IS a
// real git worktree plus a real `go test`, so stubs would prove nothing: this
// builds a throwaway module with a fuzz target whose oracle catches one
// injected fault and is blind to another, plus a non-applying ("rotted")
// patch and a non-compiling one, then asserts the audit classifies all four
// correctly and flags a target with no mutation at all. Only the registry
// source (EVENER_FUZZ_RUNNER) is stubbed.
//
// Dropped from the old shell selftest: its SIGPIPE/pipe-buffer regression
// check (issue #277) pinned a bug in the *selftest harness's own* substring
// helper (`printf | grep -q` exiting at the first match while the producer
// was still writing, so pipefail read a true match as a false negative on
// large output) -- not a behavior of fuzz-oracle-audit.sh itself.
// strings.Contains on a fully buffered CombinedOutput() has no equivalent
// hazard, so there is nothing to port.
func TestFuzzOracleAuditClassifiesMutations(t *testing.T) {
	if testing.Short() {
		t.Skip("fuzz-oracle-audit integration test: builds a throwaway git worktree and drives real go test")
	}
	requireFuzzScriptTools(t)

	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.email", "fuzzoracleaudit-test@example.com")
	runGit(t, repo, "config", "user.name", "fuzzoracleaudit-test")

	writeAuditScriptFixture(t, filepath.Join(repo, "go.mod"), "module example.com/audittest\n\ngo 1.25.0\n")

	// The fuzz target + its oracle: Double(n) must equal n*2. Seed corpus is {3}.
	writeAuditScriptFixture(t, filepath.Join(repo, "double_fuzz_test.go"), `package audittest

import "testing"

func FuzzDouble(f *testing.F) {
	f.Add(3)
	f.Fuzz(func(t *testing.T, n int) {
		if got := Double(n); got != n*2 {
			t.Fatalf("Double(%d) = %d, want %d", n, got, n*2)
		}
	})
}
`)

	// writeDouble writes the system under test body-by-body so each mutation
	// below is a clean, minimal diff.
	doublePath := filepath.Join(repo, "double.go")
	writeDouble := func(body string) {
		writeAuditScriptFixture(t, doublePath, "package audittest\n\nfunc Double(n int) int {\n"+body+"\n}\n")
	}

	writeDouble("\treturn n + n")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "base: correct Double + oracle")

	mutDir := filepath.Join(repo, "fuzz", "mutations")

	// caught.patch: Double is wrong for ALL n. The seed {3} reaches it; the
	// oracle must redden -> the audit reports "caught".
	writeDouble("\treturn n + 1")
	writeAuditScriptFixture(t, filepath.Join(mutDir, "caught.patch"), gitOutput(t, repo, "diff")+"\n")
	runGit(t, repo, "checkout", "-q", "--", "double.go")

	// blind.patch: Double is wrong ONLY for n==7, which the seed {3} never
	// reaches, so the target stays green -> the audit reports "BLIND" (the
	// machinery correctly surfaces a non-reddening oracle; the seed-pairing
	// rule is what prevents this in real use).
	writeDouble("\tif n == 7 {\n\t\treturn 999\n\t}\n\treturn n + n")
	writeAuditScriptFixture(t, filepath.Join(mutDir, "blind.patch"), gitOutput(t, repo, "diff")+"\n")
	runGit(t, repo, "checkout", "-q", "--", "double.go")

	// broken.patch: applies cleanly but does not COMPILE (calls an undefined
	// symbol). It must score ERR, never "caught" -- a non-zero `go test` from
	// a build failure is not the oracle reddening.
	writeDouble("\treturn undefinedSymbol(n)")
	writeAuditScriptFixture(t, filepath.Join(mutDir, "broken.patch"), gitOutput(t, repo, "diff")+"\n")
	runGit(t, repo, "checkout", "-q", "--", "double.go")

	// rot.patch: references context that does not exist in double.go -> git
	// apply fails -> "ROT". Deliberately synthetic (not derived from a real
	// diff): the whole point is that it does not match reality.
	writeAuditScriptFixture(t, filepath.Join(mutDir, "rot.patch"), `diff --git a/double.go b/double.go
index 1111111..2222222 100644
--- a/double.go
+++ b/double.go
@@ -1,2 +1,2 @@
 package audittest
-func ThisLineDoesNotExist() {}
+func ThisLineDoesNotExist() { panic("x") }
`)

	// Manifest: id <TAB> module:FuzzName <TAB> patchfile <TAB> description
	manifest := "caught\t.:FuzzDouble\tcaught.patch\tDouble off by all\n" +
		"blind\t.:FuzzDouble\tblind.patch\tDouble off only at n==7\n" +
		"rot\t.:FuzzDouble\trot.patch\tstale patch\n" +
		"broken\t.:FuzzDouble\tbroken.patch\tdoes not compile\n"
	writeAuditScriptFixture(t, filepath.Join(mutDir, "manifest.tsv"), manifest)

	// The mutation-fixture commit above must not itself carry fuzz/mutations/
	// into the repo's tracked tree: fuzz-oracle-audit.sh reads it straight off
	// disk in $repo_root (never committed), matching production (fuzz/mutations
	// is real, committed content there; here it is scratch the test owns).

	// Registry stub: FuzzDouble has mutations; FuzzUnaudited has none (gap report).
	runner := filepath.Join(t.TempDir(), "runner-stub.sh")
	writeExecutableFixture(t, runner, "#!/usr/bin/env bash\n"+
		"for a in \"$@\"; do\n"+
		"\tif [ \"$a\" = \"--list\" ]; then\n"+
		"\t\techo \"native:.:.:FuzzDouble\"\n"+
		"\t\techo \"native:.:.:FuzzUnaudited\"\n"+
		"\t\texit 0\n"+
		"\tfi\n"+
		"done\n"+
		"exit 0\n")

	script := repoScriptPath(t, "scripts/fuzz/fuzz-oracle-audit.sh")
	runAudit := func(args ...string) (string, int) {
		t.Helper()
		cmd := exec.Command("bash", append([]string{script}, args...)...)
		cmd.Env = append(os.Environ(),
			"EVENER_FUZZ_REPO_ROOT="+repo,
			"EVENER_FUZZ_RUNNER="+runner,
			"EVENER_FUZZ_TAGS=",
		)
		out, err := cmd.CombinedOutput()
		code := 0
		if err != nil {
			exitErr, ok := errors.AsType[*exec.ExitError](err)
			if !ok {
				t.Fatalf("fuzz-oracle-audit.sh %v: %v", args, err)
			}
			code = exitErr.ExitCode()
		}
		return string(out), code
	}

	// --- full audit ---------------------------------------------------------
	out, code := runAudit()
	t.Logf("fuzz-oracle-audit.sh output:\n%s", out)

	assertContains(t, out, "ok   caught", "caught mutation: oracle reddened")
	assertContains(t, out, "BLIND blind", "blind mutation: non-reddening oracle reported")
	assertContains(t, out, "ROT  rot", "rotted patch: reported, not silently skipped")
	assertContains(t, out, "ERR  broken", "non-compiling mutation: scored ERR, not a false catch")
	assertContains(t, out, "does not compile", "non-compiling mutation: diagnosed as a build failure")
	assertContains(t, out, "UNAUDITED: .:FuzzUnaudited", "gap report flags the unaudited target")
	if code != 1 {
		t.Errorf("fuzz-oracle-audit.sh exit code = %d, want 1 (an oracle is blind and a patch rotted)", code)
	}

	// The disposable worktree (mktemp'd as fuzz-oracle-audit.XXXX) is gone;
	// only the throwaway repo's own main worktree should remain registered.
	worktrees := gitOutput(t, repo, "worktree", "list")
	if strings.Contains(worktrees, "fuzz-oracle-audit.") {
		t.Errorf("fuzz-oracle-audit.sh left a worktree behind:\n%s", worktrees)
	}

	// --- gap-only mode -------------------------------------------------------
	out2, code2 := runAudit("--gap-only")
	assertContains(t, out2, "UNAUDITED: .:FuzzUnaudited", "gap-only: lists the unaudited target")
	if code2 != 0 {
		t.Errorf("fuzz-oracle-audit.sh --gap-only exit code = %d, want 0 (a report, not a gate)", code2)
	}
}

// assertContains fails the test with desc and the full haystack if haystack
// does not contain needle.
func assertContains(t *testing.T, haystack, needle, desc string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("%s: output does not contain %q\n%s", desc, needle, haystack)
	}
}
