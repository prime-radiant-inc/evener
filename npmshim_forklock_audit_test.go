package evener_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"strconv"
	"strings"
	"testing"
)

// This file pins the ForkLock discipline for the npm shim write in
// npmShimEnv (issue #270): an executable written by a test and then exec'd
// must be written while syscall.ForkLock is held for reading, and every
// acquisition must be released.
//
// The underlying race (golang/go#22315) is load-sensitive and not
// deterministically reproducible — it needs a sibling parallel test's fork
// to land inside the write's open-to-close window, leaving the child holding
// the inherited write fd that makes the kernel refuse the exec with ETXTBSY.
// So the invariant is held structurally, the same shape as
// agent/session_emit_lock_guard_test.go. The ETXTBSY retry in
// combinedOutputRetryingETXTBSY stays regardless: ForkLock is Go-runtime-local
// and gives no protection for grandchild execs (tsc written by `npm ci`,
// exec'd inside a make subtree).
//
// Issue #1204 reworked this audit. It used to pin one call site's *spelling*:
// it looked for the identifiers "npm" and fakeBin to decide which
// writeExecutable call mattered, it inspected only os.WriteFile's literal
// mode (so a raw write at 0o644 followed by os.Chmod(..., 0o755) passed), and
// it read the ForkLock state at the write without ever requiring the release
// (so deleting `defer syscall.ForkLock.RUnlock()` passed). All three made the
// audit read stronger than it was. It now asserts the discipline instead:
//
//   - every write reachable from npmShimEnv — directly, or through same-file
//     helpers, function values, and callbacks it references — that can produce
//     an executable file must hold ForkLock for reading across the write and
//     release every acquisition afterwards;
//   - no write may produce an executable without that guard: a literal
//     execute mode, a non-literal mode that cannot prove either way, a chmod
//     to an execute mode through os.Chmod OR a file handle (the
//     write-then-chmod evasion), or the modeless
//     os.Create/OpenFile/CreateTemp;
//   - indirection the call walk cannot follow fails closed rather than
//     silently passing: a call through a slice/map element or another call's
//     result, and a known function value stored in a composite literal.
//
// The walk threads the lock depth through every statement list — blocks, if /
// for / range bodies, switch and select cases, and function literals — so a
// write buried in a case or a closure cannot hide behind a guarded decoy, and
// a balanced nested lock pair leaves the depth alone. Releases must match the
// depth: an acquisition is closed by a deferred RUnlock registered before the
// write, or by a plain RUnlock later in the list that established the lock and
// that no early exit (return, t.Fatal*, panic) can skip.
// Identifiers are deliberately not matched: renaming fakeBin, spelling the
// path through filepath.Join, hoisting it into a variable, or extracting the
// write into a helper all keep passing.
//
// What this audit does NOT prove, honestly: it cannot show that the guarded
// executable write targets the npm shim rather than some other file. Its
// reachability check therefore only rules out a *vacuous* audit — reaching no
// guarded executable write at all. That the shim is written executable at all
// is proven by the install test actually running it.
//
// Scope: this audit covers the npm shim write path and the copies of the
// guarded helper the issue names. It does not lint every test in the repo for
// executable writes; routing all of those through one shared guarded helper
// (or a repo-wide lint) is the open architecture question the issue raises and
// this change does not decide.

// duplicatedWriteExecutableSites names the copies of the guarded write helper
// that carry the same comment ("matching install_test.go's writeExecutable")
// but sit in other packages, where the audit above cannot see them. They
// duplicate the discipline, so they are checked with the same predicate.
var duplicatedWriteExecutableSites = []struct{ file, fn string }{
	{"internal/binresolve/sibling_test.go", "writeExecutable"},
	{"cmd/evener-tui/internal/hubstart/hub_start_test.go", "writeExecutable"},
}

// TestNpmShimWriteGoesThroughForkLock asserts that the npm shim write in
// npmShimEnv is guarded: every executable write reachable from npmShimEnv
// holds and releases syscall.ForkLock, and no executable write bypasses that
// guard.
func TestNpmShimWriteGoesThroughForkLock(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("install_test.go")
	if err != nil {
		t.Fatalf("read install_test.go: %v", err)
	}
	for _, finding := range npmShimWriteFindings("install_test.go", src) {
		t.Error(finding)
	}
}

// TestDuplicatedWriteExecutableHelpersHoldForkLock pins the same discipline at
// the copies of the helper the audit above cannot reach across package
// boundaries. Each is checked directly: it must hold ForkLock across its write
// and release it.
func TestDuplicatedWriteExecutableHelpersHoldForkLock(t *testing.T) {
	t.Parallel()

	for _, site := range duplicatedWriteExecutableSites {
		src, err := os.ReadFile(site.file)
		if err != nil {
			t.Errorf("read %s: %v", site.file, err)
			continue
		}
		for _, finding := range guardedWriterFindings(site.file, src, site.fn) {
			t.Error(finding)
		}
	}
}

// TestNpmShimForkLockAuditRejectsEvasions drives the audit with the evasions
// issue #1204 lists. Each mutation leaves the hazardous write in place; the
// audit must produce a finding for every one.
func TestNpmShimForkLockAuditRejectsEvasions(t *testing.T) {
	t.Parallel()

	if findings := npmShimWriteFindings("fixture_test.go", []byte(shimFixture)); len(findings) != 0 {
		t.Fatalf("the unmutated fixture must be clean, got: %v", findings)
	}

	cases := []struct {
		name   string
		mutate func(string) string
	}{
		{
			// Way 1: the hazardous write is raw at a non-executable mode and
			// made executable by chmod, while a decoy writeExecutable call on
			// another file satisfies a "some guarded call exists" check.
			name: "raw write behind a decoy, made executable by chmod",
			mutate: func(s string) string {
				return strings.Replace(s, shimCall,
					`	shim := filepath.Join(fakeBin, "npm")
	if err := os.WriteFile(shim, []byte(npmShim), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shim, 0o755); err != nil {
		t.Fatal(err)
	}
`+decoyCall, 1)
			},
		},
		{
			// Way 1 variant: the raw executable write's path expression avoids
			// the tokens the old audit matched on.
			name: "raw executable write spelled without the npm tokens",
			mutate: func(s string) string {
				return strings.Replace(s, shimCall,
					`	dest := filepath.Join(fakeBin, shimName)
	if err := os.WriteFile(dest, []byte(npmShim), 0o755); err != nil {
		t.Fatal(err)
	}
`+decoyCall, 1)
			},
		},
		{
			// Way 3: the lock is still held at the write, but nothing ever
			// releases it. The old audit read the state at the write only.
			name: "helper keeps the write but drops the deferred release",
			mutate: func(s string) string {
				return strings.Replace(s, "	defer syscall.ForkLock.RUnlock()\n", "", 1)
			},
		},
		{
			name: "helper keeps the write but drops the lock",
			mutate: func(s string) string {
				return strings.Replace(s, "	syscall.ForkLock.RLock()\n", "", 1)
			},
		},
		{
			// The modeless write calls cannot prove the file non-executable.
			name: "raw executable write through os.CreateTemp",
			mutate: func(s string) string {
				return strings.Replace(s, shimCall,
					`	f, err := os.CreateTemp(fakeBin, "npm")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(npmShim); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()`, 1)
			},
		},
		{
			// A guarded write on another file cannot stand in for the shim's.
			name: "guarded helper no longer reachable from npmShimEnv",
			mutate: func(s string) string {
				return strings.Replace(s, shimCall,
					`	writeRaw(t, filepath.Join(fakeBin, "npm"), npmShim)`, 1)
			},
		},
		{
			// A raw executable write in a switch case is not a nested
			// BlockStmt, so a traversal that only descends blocks would never
			// see it.
			name: "raw executable write inside a switch case beside a decoy",
			mutate: func(s string) string {
				return strings.Replace(s, shimCall,
					`	switch os.Getenv("SHIM_MODE") {
	case "decoy":
`+decoyCall+`
	default:
		if err := os.WriteFile(filepath.Join(fakeBin, "npm"), []byte(npmShim), 0o755); err != nil {
			t.Fatal(err)
		}
	}`, 1)
			},
		},
		{
			// A closure body does not inherit the enclosing hold: it may run
			// later, or on another goroutine.
			name: "raw executable write inside a closure beside a decoy",
			mutate: func(s string) string {
				return strings.Replace(s, shimCall,
					`	func() {
		if err := os.WriteFile(filepath.Join(fakeBin, "npm"), []byte(npmShim), 0o755); err != nil {
			t.Fatal(err)
		}
	}()
`+decoyCall, 1)
			},
		},
		{
			// Calling the unguarded writer through a local function value must
			// not hide it from the call graph.
			name: "raw executable write reached through a local function value",
			mutate: func(s string) string {
				return strings.Replace(s, shimCall,
					`	write := writeRaw
	write(t, filepath.Join(fakeBin, "npm"), npmShim)
`+decoyCall, 1) + rawWriterFixture
			},
		},
		{
			// A raw writer passed as a callback argument is still reachable.
			name: "raw executable write reached through a callback argument",
			mutate: func(s string) string {
				return strings.Replace(s, shimCall,
					`	run(t, writeRaw, filepath.Join(fakeBin, "npm"))
`+decoyCall, 1) + rawWriterFixture + runCallbackFixture
			},
		},
		{
			// A raw writer held in a package-level function value is still
			// reachable.
			name: "raw executable write reached through a package-level function value",
			mutate: func(s string) string {
				return strings.Replace(s, shimCall,
					`	writeRawValue(t, filepath.Join(fakeBin, "npm"))
`+decoyCall, 1) +
					"\nvar writeRawValue = func(t *testing.T, path string) {\n" +
					"\tif err := os.WriteFile(path, []byte(npmShim), 0o755); err != nil {\n" +
					"\t\tt.Fatal(err)\n\t}\n}\n"
			},
		},
		{
			// A conditional deferred release does not cover every exit, so it
			// is not a release the discipline can rely on.
			name: "helper only releases the lock on one branch",
			mutate: func(s string) string {
				return strings.Replace(s,
					`	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()`,
					`	syscall.ForkLock.RLock()
	if os.Getenv("RELEASE") != "" {
		defer syscall.ForkLock.RUnlock()
	}`, 1)
			},
		},
		{
			// Two acquisitions and one release leave the lock read-held
			// forever. A boolean lock state cannot see this.
			name: "double lock with a single release",
			mutate: func(s string) string {
				return strings.Replace(s,
					`	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()`,
					`	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	syscall.ForkLock.RLock()`, 1)
			},
		},
		{
			// The last alias assignment wins for a single-target resolver, so
			// a raw writer replaced only conditionally would go unread.
			name: "raw writer replaced conditionally by a guarded one",
			mutate: func(s string) string {
				return strings.Replace(s, shimCall,
					`	write := writeRaw
	if os.Getenv("GUARDED") != "" {
		write = writeExecutable
	}
	write(t, filepath.Join(fakeBin, "npm"), npmShim)
`+decoyCall, 1) + rawWriterFixture
			},
		},
		{
			// A release inside the branch beside the write does not close a
			// lock acquired outside it: the other path leaks the read lock.
			name: "release inside the branch instead of after it",
			mutate: func(s string) string {
				return strings.Replace(s, `	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}`, `	syscall.ForkLock.RLock()
	if os.Getenv("WRITE_AS") != "" {
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatalf("write executable %s: %v", path, err)
		}
		syscall.ForkLock.RUnlock()
	}`, 1)
			},
		},
		{
			// A raw writer behind a local var-declared alias still has to be
			// audited.
			name: "raw writer aliased with a local var declaration",
			mutate: func(s string) string {
				return strings.Replace(s, shimCall,
					`	var write = writeRaw
	write(t, filepath.Join(fakeBin, "npm"), npmShim)
`+decoyCall, 1) + rawWriterFixture
			},
		},
		{
			// Releasing one acquisition of two leaves the lock read-held.
			name: "partial release under a deeper lock",
			mutate: func(s string) string {
				return strings.Replace(s,
					`	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()`,
					`	syscall.ForkLock.RLock()
	syscall.ForkLock.RLock()
	syscall.ForkLock.RUnlock()
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()`, 1)
			},
		},
		{
			// A defer registered after a write that can return early never
			// runs on the failure path, so the lock leaks exactly there.
			name: "deferred release registered after the write",
			mutate: func(s string) string {
				return strings.Replace(s, `	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}`, `	syscall.ForkLock.RLock()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		return
	}
	defer syscall.ForkLock.RUnlock()`, 1)
			},
		},
		{
			// A plain release after a t.Fatal cannot run: t.Fatalf calls
			// runtime.Goexit, so the plain RUnlock is skipped and
			// syscall.ForkLock is leaked permanently.
			name: "plain release after a fatal that skips it",
			mutate: func(s string) string {
				return strings.Replace(s, `	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}`, `	syscall.ForkLock.RLock()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
	syscall.ForkLock.RUnlock()`, 1)
			},
		},
		{
			// (*os.File).Chmod is how a raw non-executable write is made
			// executable afterwards, and it is not os.Chmod.
			name: "executable chmod through a file handle",
			mutate: func(s string) string {
				return strings.Replace(s, shimCall,
					`	f, err := os.Open(filepath.Join(fakeBin, "npm"))
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Chmod(0o755)
`+decoyCall, 1)
			},
		},
		{
			// A raw writer stored in a container the call walk cannot follow.
			name: "raw writer stored in a function slice",
			mutate: func(s string) string {
				return strings.Replace(s, shimCall,
					`	writers := []func(*testing.T, string, string){writeRaw}
	writers[0](t, filepath.Join(fakeBin, "npm"), npmShim)
`+decoyCall, 1) + rawWriterFixture
			},
		},
		{
			// A writer returned from a helper is bound to an identifier the
			// alias walk cannot resolve, so the call fails closed.
			name: "raw writer returned from a helper",
			mutate: func(s string) string {
				return strings.Replace(s, shimCall,
					`	write := makeWriter()
	write(t, filepath.Join(fakeBin, "npm"), npmShim)
`+decoyCall, 1) + rawWriterFixture + `
func makeWriter() func(*testing.T, string, string) {
	return writeRaw
}
`
			},
		},
		{
			// A release and an acquisition in opposite branches: the depth
			// after the branch is path-dependent, so the audit refuses it.
			name: "release and acquire in opposite branches",
			mutate: func(s string) string {
				return strings.Replace(s, `	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}`, `	syscall.ForkLock.RLock()
	if os.Getenv("RELEASE") != "" {
		syscall.ForkLock.RUnlock()
	} else {
		syscall.ForkLock.RLock()
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}`, 1)
			},
		},
		{
			// The pre-write RUnlock is already reflected in the depth; it must
			// not be credited again as the release the error path skips.
			name: "pre-write release credited to a later skipped release",
			mutate: func(s string) string {
				return strings.Replace(s, `	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}`, `	syscall.ForkLock.RLock()
	syscall.ForkLock.RLock()
	syscall.ForkLock.RUnlock()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		return
	}
	syscall.ForkLock.RUnlock()`, 1)
			},
		},
		{
			// An aliased import must not hide the write.
			name: "executable write through an aliased import",
			mutate: func(s string) string {
				return strings.Replace(s, shimCall,
					`	if err := o.WriteFile(filepath.Join(fakeBin, "npm"), []byte(npmShim), 0o755); err != nil {
		t.Fatal(err)
	}
`+decoyCall, 1)
			},
		},
		{
			// A deferred write runs after the release.
			name: "executable write deferred until after the release",
			mutate: func(s string) string {
				return strings.Replace(s, `	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}`, `	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	defer os.WriteFile(path, []byte(content), 0o755)`, 1)
			},
		},
		{
			// An unreadable mode proves nothing either way.
			name: "non-literal write mode",
			mutate: func(s string) string {
				return strings.Replace(s, shimCall,
					`	dest := filepath.Join(fakeBin, "npm")
	if err := os.WriteFile(dest, []byte(npmShim), shimMode); err != nil {
		t.Fatal(err)
	}
`+decoyCall, 1)
			},
		},
		{
			// A keyed map literal hides the function behind a KeyValueExpr,
			// and the ranged binding hides the call target.
			name: "raw writer stored in a map literal and called from a range",
			mutate: func(s string) string {
				return strings.Replace(s, shimCall,
					`	writers := map[string]func(*testing.T, string, string){"raw": writeRaw}
	for _, w := range writers {
		w(t, filepath.Join(fakeBin, "npm"), npmShim)
	}
`+decoyCall, 1) + rawWriterFixture
			},
		},
		{
			// The lock is taken unconditionally but the release is only
			// registered when the branch runs, so the empty branch leaks it.
			name: "lock taken outside the branch that registers the release",
			mutate: func(s string) string {
				return strings.Replace(s, `	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}`, `	syscall.ForkLock.RLock()
	if os.Getenv("GUARD") != "" {
		defer syscall.ForkLock.RUnlock()
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatalf("write executable %s: %v", path, err)
		}
	}`, 1)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := tc.mutate(shimFixture)
			if src == shimFixture {
				t.Fatal("the mutation changed nothing: the fixture no longer matches the text this case mutates")
			}
			if findings := npmShimWriteFindings("fixture_test.go", []byte(src)); len(findings) == 0 {
				t.Fatal("the audit accepted an executable write that bypasses the ForkLock discipline")
			}
		})
	}
}

// TestNpmShimForkLockAuditAcceptsBenignRefactors drives the audit with
// refactors that keep the discipline. The old audit false-RED'd on every one:
// they change the spelling, not the guard.
func TestNpmShimForkLockAuditAcceptsBenignRefactors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(string) string
	}{
		{
			name: "directory and shim name hoisted into differently named variables",
			mutate: func(s string) string {
				return strings.Replace(s, `	fakeBin := t.TempDir()
`+shimCall, `	binDir := t.TempDir()
	shimName := "npm"
	writeExecutable(t, filepath.Join(binDir, shimName), npmShim)`, 1)
			},
		},
		{
			name: "write extracted into a helper that still calls writeExecutable",
			mutate: func(s string) string {
				return strings.Replace(s, shimCall, `	writeNpmShim(t, fakeBin)`, 1) +
					`

func writeNpmShim(t *testing.T, dir string) {
	t.Helper()
	writeExecutable(t, filepath.Join(dir, "npm"), npmShim)
}
`
			},
		},
		{
			name: "lock taken and released inside a retry loop body",
			mutate: func(s string) string {
				return strings.Replace(s, `	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}`, `	for attempt := 0; ; attempt++ {
		syscall.ForkLock.RLock()
		err := os.WriteFile(path, []byte(content), 0o755)
		syscall.ForkLock.RUnlock()
		if err == nil {
			return
		}
		if attempt == 0 {
			continue
		}
		t.Fatalf("write executable %s: %v", path, err)
	}`, 1)
			},
		},
		{
			name: "guarded write reached through a local function value",
			mutate: func(s string) string {
				return strings.Replace(s, shimCall, `	write := writeExecutable
	write(t, filepath.Join(fakeBin, "npm"), npmShim)`, 1)
			},
		},
		{
			name: "guarded write inside a nested block with a deferred release",
			mutate: func(s string) string {
				return strings.Replace(s, `	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}`, `	func() {
		syscall.ForkLock.RLock()
		defer syscall.ForkLock.RUnlock()
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatalf("write executable %s: %v", path, err)
		}
	}()`, 1)
			},
		},
		{
			// A provably non-executable literal write needs no ForkLock guard,
			// and must keep passing.
			name: "literal non-executable config write beside the shim",
			mutate: func(s string) string {
				return strings.Replace(s, shimCall, shimCall+`
	if err := os.WriteFile(filepath.Join(fakeBin, ".npmrc"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}`, 1)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := tc.mutate(shimFixture)
			if src == shimFixture {
				t.Fatal("the mutation changed nothing: the fixture no longer matches the text this case mutates")
			}
			if findings := npmShimWriteFindings("fixture_test.go", []byte(src)); len(findings) != 0 {
				t.Fatalf("the audit false-RED'd on a refactor that keeps the discipline: %v", findings)
			}
		})
	}
}

// shimCall is the guarded write in the fixture, and decoyCall a guarded write
// on an unrelated file. The evasion cases replace the first with an unguarded
// write and keep the second, so a "some guarded call exists" audit is
// satisfied while the shim is written raw.
const (
	shimCall    = `	writeExecutable(t, filepath.Join(fakeBin, "npm"), npmShim)`
	decoyCall   = `	writeExecutable(t, filepath.Join(fakeBin, "decoy"), "decoy")`
	shimFixture = `package evener_test

const npmShim = ` + "`#!/bin/sh\nexit 0\n`" + `

func npmShimEnv(t *testing.T, env []string) []string {
	t.Helper()

	fakeBin := t.TempDir()
` + shimCall + `
	return env
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()

	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}
`
)

// rawWriterFixture and runCallbackFixture are appended to the fixture by the
// callback/reachability evasion cases.
const (
	rawWriterFixture = `
func writeRaw(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}
`
	runCallbackFixture = `
func run(t *testing.T, fn func(*testing.T, string, string), path string) {
	fn(t, path, npmShim)
}
`
)

// npmShimWriteFindings audits the npm shim write path in one source file and
// returns a message per violation. It is the whole audit: the real-file test
// and the mutation tests above differ only in what source they hand it.
func npmShimWriteFindings(filename string, src []byte) []string {
	file, err := parser.ParseFile(token.NewFileSet(), filename, src, 0)
	if err != nil {
		return []string{filename + ": parse: " + err.Error()}
	}
	if findFuncDecl(file, "npmShimEnv") == nil {
		return []string{filename + ": no function npmShimEnv: the ForkLock audit has lost its subject (issue #270)"}
	}

	var findings []string
	guardedExecutable := 0
	reachable, known := reachableBodies(file, "npmShimEnv")
	for _, fn := range reachable {
		fnFindings, guarded := writeGuardFindings(filename, fn.name, fn.body, false, known)
		findings = append(findings, fnFindings...)
		guardedExecutable += guarded
	}
	if guardedExecutable == 0 {
		findings = append(findings, filename+": no write reachable from npmShimEnv holds syscall.ForkLock across an executable write and releases it: "+
			"the npm shim write has regressed to a raw write that holds no ForkLock, and a sibling parallel test's fork can leave a child holding the shim's write fd (golang/go#22315) — "+
			"route the write through a helper that holds and releases the lock across it")
	}
	return findings
}

// guardedWriterFindings audits one helper that is expected to be a guarded
// executable writer, by name, in one source file. A renamed or deleted helper
// fails loudly rather than silently passing.
func guardedWriterFindings(filename string, src []byte, name string) []string {
	file, err := parser.ParseFile(token.NewFileSet(), filename, src, 0)
	if err != nil {
		return []string{filename + ": parse: " + err.Error()}
	}
	fn := findFuncDecl(file, name)
	if fn == nil {
		return []string{filename + ": no function " + name + ": the duplicated ForkLock guard it anchors can no longer be checked"}
	}
	_, known := reachableBodies(file, name)
	findings, _ := writeGuardFindings(filename, name, fn.Body, true, known)
	return findings
}

// writeGuardFindings returns a message for every executable write in body that
// can be produced without holding and releasing syscall.ForkLock, for every
// ForkLock acquisition that is never released, and the count of executable
// writes that ARE guarded. requireWrite additionally demands that body performs
// an os.WriteFile at all — true for the helper copies, false for the functions
// reachable from npmShimEnv, where a helper that only reads is unremarkable.
// known names the file's functions, so a function value escaping into a
// container the call walk cannot follow is reported rather than ignored.
func writeGuardFindings(filename, name string, body *ast.BlockStmt, requireWrite bool, known map[string]bool) ([]string, int) {
	var findings []string
	guarded := 0
	writes := writesIn(body)
	if requireWrite && len(writes) == 0 {
		findings = append(findings, filename+": "+name+" no longer performs an os.WriteFile: "+
			"the executable the ForkLock guard exists for has moved or vanished — update the audit alongside whatever replaced it")
	}

	for _, w := range writes {
		class, mode := classifyMode(w.call)
		releases := releasesAfter(w)
		if class == modeExecutable && releases && balancedLocks(body) {
			guarded++
		}

		switch {
		case class == modeUnprovable:
			findings = append(findings, filename+": "+name+" passes a non-literal mode to os.WriteFile: "+
				"an unreadable mode cannot prove the file non-executable, and an executable written raw bypasses the ForkLock guard — "+
				"write it as a literal (0o755 for executables through the guarded helper, 0o644 for configs)")
		case class == modeExecutable && w.depth == 0:
			findings = append(findings, filename+": "+name+" writes an executable ("+mode+") without holding syscall.ForkLock: "+
				"an executable written raw bypasses the ForkLock guard (golang/go#22315); use the guarded helper")
		case w.depth > 0 && !releases:
			findings = append(findings, unreleasedFinding(filename, name))
		}
	}

	if !balancedLocks(body) {
		findings = append(findings, filename+": "+name+" acquires and releases syscall.ForkLock an unequal number of times: "+
			"a read lock left held after a write blocks every later ForkLock writer (a second RLock under a single deferred RUnlock is the silent form)")
	}
	if raw := rawWritePathNonWriteFile(body); raw != "" {
		findings = append(findings, filename+": "+name+" writes via os."+raw+" instead of the guarded helper: "+
			"a write call with no readable mode cannot prove the file non-executable, and an executable written raw bypasses the ForkLock guard (golang/go#22315)")
	}
	findings = append(findings, chmodFindings(filename, name, body)...)
	findings = append(findings, indirectValueFindings(filename, name, body, known)...)
	return findings, guarded
}

// chmodFindings reports every chmod to an executable mode. The receiver is not
// checked: `os.Chmod` and `(*os.File).Chmod` both turn a file executable, and
// the second is exactly how a raw 0o644 write is made executable afterwards.
func chmodFindings(filename, name string, body *ast.BlockStmt) []string {
	var findings []string
	for _, call := range callsIn(body, isChmodCall) {
		class, mode := classifyMode(call)
		if class != modeNonExecutable {
			findings = append(findings, filename+": "+name+" chmods a file to "+mode+": "+
				"an executable created by a raw write and made executable afterwards bypasses the ForkLock guard — write it through the guarded helper instead")
		}
	}
	return findings
}

// indirectValueFindings fails the audit closed on indirections the call walk
// cannot follow: a call through a slice/map element or another call's result,
// and a known function value stored in a composite literal. Each can hide an
// unguarded writer behind a guarded decoy, so an audit that stayed silent on
// them would read stronger than it is.
func indirectValueFindings(filename, name string, body *ast.BlockStmt, known map[string]bool) []string {
	var findings []string
	// A local bound from a call or other non-function value is a value the
	// alias walk cannot resolve to a function; calling it could reach
	// anything. Range bindings are included: a writer read out of a map or
	// slice in a range is exactly that.
	opaque := map[string]bool{}
	markOpaque := func(target ast.Expr) {
		if ident, ok := target.(*ast.Ident); ok {
			opaque[ident.Name] = true
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range v.Lhs {
				if i >= len(v.Rhs) {
					continue
				}
				switch v.Rhs[i].(type) {
				case *ast.Ident, *ast.FuncLit:
				default:
					markOpaque(lhs)
				}
			}
		case *ast.RangeStmt:
			markOpaque(v.Key)
			markOpaque(v.Value)
		case *ast.DeclStmt:
			gen, ok := v.Decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				return true
			}
			for _, spec := range gen.Specs {
				valueSpec, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range valueSpec.Names {
					if i >= len(valueSpec.Values) {
						continue
					}
					switch valueSpec.Values[i].(type) {
					case *ast.Ident, *ast.FuncLit:
					default:
						markOpaque(name)
					}
				}
			}
		}
		return true
	})
	for _, call := range callsIn(body, func(*ast.CallExpr) bool { return true }) {
		switch fun := call.Fun.(type) {
		case *ast.IndexExpr, *ast.CallExpr:
			findings = append(findings, filename+": "+name+" calls through an indirect target the audit cannot follow: "+
				"a function value in a slice, map, or call result can hide an unguarded writer — call the guarded helper by name, or extend this audit")
		case *ast.Ident:
			if opaque[fun.Name] {
				findings = append(findings, filename+": "+name+" calls "+fun.Name+", which holds a value the audit cannot resolve to a function: "+
					"a returned, stored, or ranged-over writer can hide an unguarded write — call the guarded helper by name, or extend this audit")
			}
		}
	}
	for _, literal := range compositeLiterals(body) {
		for _, element := range literal.Elts {
			// Keyed literals (maps, keyed structs) wrap the value.
			if keyValue, ok := element.(*ast.KeyValueExpr); ok {
				element = keyValue.Value
			}
			ident, ok := element.(*ast.Ident)
			if ok && known[ident.Name] {
				findings = append(findings, filename+": "+name+" stores function "+ident.Name+" in a composite literal: "+
					"the audit cannot follow a function value through a container — call the guarded helper by name, or extend this audit")
			}
		}
	}
	return findings
}

func compositeLiterals(body *ast.BlockStmt) []*ast.CompositeLit {
	var literals []*ast.CompositeLit
	ast.Inspect(body, func(n ast.Node) bool {
		if literal, ok := n.(*ast.CompositeLit); ok {
			literals = append(literals, literal)
		}
		return true
	})
	return literals
}

// isChmodCall reports whether call is `<expr>.Chmod(...)` on any receiver.
func isChmodCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Chmod"
}

func unreleasedFinding(filename, name string) string {
	return filename + ": " + name + " holds syscall.ForkLock across its write but never releases it: " +
		"delete the deferred `syscall.ForkLock.RUnlock()` and a read lock held forever blocks every later ForkLock writer"
}

// modeClass classifies an os call's final (mode) argument.
type modeClass int

const (
	// modeExecutable: a literal mode with an execute bit.
	modeExecutable modeClass = iota
	// modeNonExecutable: a literal mode without one.
	modeNonExecutable
	// modeUnprovable: not a literal, so it proves nothing either way.
	modeUnprovable
)

// classifyMode classifies an os call's last argument as a mode. Anything that
// is not an integer literal — a variable, a call like sourceInfo.Mode().Perm()
// — is unprovable, and the audit refuses it rather than assuming the file is
// not executable.
func classifyMode(call *ast.CallExpr) (modeClass, string) {
	if len(call.Args) == 0 {
		return modeUnprovable, "a mode with no argument to read"
	}
	lit, ok := call.Args[len(call.Args)-1].(*ast.BasicLit)
	if !ok || lit.Kind != token.INT {
		return modeUnprovable, "a non-literal mode"
	}
	mode, err := strconv.ParseInt(lit.Value, 0, 32)
	if err != nil {
		return modeUnprovable, lit.Value
	}
	if mode&0o111 != 0 {
		return modeExecutable, lit.Value
	}
	return modeNonExecutable, lit.Value
}

// callsIn returns the calls under root that match, in source order.
func callsIn(root ast.Node, match func(*ast.CallExpr) bool) []*ast.CallExpr {
	var calls []*ast.CallExpr
	ast.Inspect(root, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && match(call) {
			calls = append(calls, call)
		}
		return true
	})
	return calls
}

// isWriteCall reports whether call is a call of the named write function —
// os.WriteFile, an aliased import's WriteFile, or a same-named wrapper. The
// package qualifier is deliberately NOT matched: requiring `os` would make a
// raw executable write through an aliased import invisible, so the audit
// matches the selector name and fails closed instead.
func isWriteCall(call *ast.CallExpr, name string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == name
}

// rawWritePathNonWriteFile returns the modeless os write-call name body uses —
// Create, OpenFile, CreateTemp — or "" when it uses none. These calls cannot
// prove the written file non-executable the way os.WriteFile's literal mode
// can, so any use is named; the fake-bin directory's exec'd content belongs to
// the guarded helper.
func rawWritePathNonWriteFile(body *ast.BlockStmt) string {
	for _, name := range []string{"Create", "OpenFile", "CreateTemp"} {
		if callsIn(body, func(call *ast.CallExpr) bool { return isWriteCall(call, name) }) != nil {
			return name
		}
	}
	return ""
}

// findFuncDecl returns the named plain (non-method) function declaration, or
// nil.
func findFuncDecl(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == name && fn.Recv == nil {
			return fn
		}
	}
	return nil
}

// namedBody is one unit of the audit: a function or a package-level function
// value, by the name the audit reports it under.
type namedBody struct {
	name string
	body *ast.BlockStmt
}

// reachableBodies returns root and every same-file body transitively reachable
// from it — plain functions, package-level function values, and function
// identifiers passed as callback arguments or held in variables — together
// with the set of every function name the file defines, which the audit uses
// to report a function value escaping into a container it cannot follow.
func reachableBodies(file *ast.File, root string) ([]namedBody, map[string]bool) {
	bodies, aliases := fileFuncBodies(file)
	known := make(map[string]bool, len(bodies))
	for name := range bodies {
		known[name] = true
	}
	if bodies[root] == nil {
		return nil, known
	}

	visited := map[string]bool{root: true}
	out := []namedBody{{name: root, body: bodies[root]}}
	for i := 0; i < len(out); i++ {
		resolve := func(name string) []string {
			return aliasTargets(name, mergeAliases(aliases, funcAliases(out[i].body)))
		}
		for _, name := range referencedFuncNames(out[i].body) {
			for _, target := range resolve(name) {
				if visited[target] || bodies[target] == nil {
					continue
				}
				visited[target] = true
				out = append(out, namedBody{name: target, body: bodies[target]})
			}
		}
	}
	return out, known
}

// fileFuncBodies maps every body the file can deliver by name: function
// declarations, package-level function values, and package-level aliases
// (`var h = writeExecutable`).
func fileFuncBodies(file *ast.File) (map[string]*ast.BlockStmt, map[string][]string) {
	bodies := map[string]*ast.BlockStmt{}
	aliases := map[string][]string{}
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil && d.Body != nil {
				bodies[d.Name.Name] = d.Body
			}
		case *ast.GenDecl:
			if d.Tok != token.VAR {
				continue
			}
			for _, spec := range d.Specs {
				valueSpec, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, ident := range valueSpec.Names {
					if i >= len(valueSpec.Values) {
						continue
					}
					switch v := valueSpec.Values[i].(type) {
					case *ast.FuncLit:
						if v.Body != nil {
							bodies[ident.Name] = v.Body
						}
					case *ast.Ident:
						aliases[ident.Name] = append(aliases[ident.Name], v.Name)
					}
				}
			}
		}
	}
	return bodies, aliases
}

// funcAliases records every local variable of body that is assigned a function
// identifier — by `:=`, `=`, or a local `var` declaration — keeping EVERY
// assignment rather than the last one: a writer assigned first and replaced
// only conditionally under a guarded name still has to be audited, so the raw
// target must not be dropped, and a `var`-declared alias must not be missed.
func funcAliases(body *ast.BlockStmt) map[string][]string {
	aliases := map[string][]string{}
	record := func(lhs, rhs ast.Expr) {
		ident, ok := lhs.(*ast.Ident)
		if !ok {
			return
		}
		if target, ok := rhs.(*ast.Ident); ok {
			aliases[ident.Name] = append(aliases[ident.Name], target.Name)
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range v.Lhs {
				if i < len(v.Rhs) {
					record(lhs, v.Rhs[i])
				}
			}
		case *ast.DeclStmt:
			gen, ok := v.Decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				return true
			}
			for _, spec := range gen.Specs {
				valueSpec, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range valueSpec.Names {
					if i < len(valueSpec.Values) {
						record(name, valueSpec.Values[i])
					}
				}
			}
		}
		return true
	})
	return aliases
}

func mergeAliases(a, b map[string][]string) map[string][]string {
	merged := make(map[string][]string, len(a)+len(b))
	maps.Copy(merged, a)
	for k, v := range b {
		merged[k] = append(merged[k], v...)
	}
	return merged
}

// aliasTargets returns every function name a name can resolve to, following
// assignments transitively and bounded so a cycle cannot spin. Returning all
// targets rather than one is what keeps a conditional reassignment from
// hiding the unguarded branch.
func aliasTargets(name string, aliases map[string][]string) []string {
	seen := map[string]bool{name: true}
	targets := []string{name}
	for i := 0; i < len(targets); i++ {
		for _, next := range aliases[targets[i]] {
			if seen[next] {
				continue
			}
			seen[next] = true
			targets = append(targets, next)
		}
	}
	return targets
}

// referencedFuncNames returns the identifiers body uses as calls or as call
// arguments — the places a function value can enter the graph.
func referencedFuncNames(body *ast.BlockStmt) []string {
	var names []string
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if ident, ok := call.Fun.(*ast.Ident); ok {
			names = append(names, ident.Name)
		}
		for _, arg := range call.Args {
			ast.Inspect(arg, func(a ast.Node) bool {
				if ident, ok := a.(*ast.Ident); ok {
					names = append(names, ident.Name)
				}
				return true
			})
		}
		return true
	})
	return names
}

// scopePos is one statement list on the path to a write, with the index of the
// statement that leads toward it.
type scopePos struct {
	list  []ast.Stmt
	index int
}

// writeInfo records one os.WriteFile call and the lock state it executes in.
type writeInfo struct {
	call      *ast.CallExpr
	chain     []scopePos // enclosing statement lists, outermost first
	depth     int        // ForkLock read lock acquisitions held at the write
	lockScope scopePos   // the list that established the outermost held lock
	pathIndex int        // index in lockScope.list of the statement leading here
}

// writesIn returns every os.WriteFile call in body, in source order, with the
// ForkLock depth each one executes under and the scope chain it sits in. The
// walk threads the depth through every statement list — blocks, if/for/range
// bodies, switch and select cases, and function literals — so a write in a
// case or a closure is found and judged, not skipped. A function literal
// starts unprotected: it may run later or on another goroutine, so the
// enclosing hold does not carry into it.
func writesIn(body *ast.BlockStmt) []writeInfo {
	var out []writeInfo
	if body == nil {
		return out
	}

	var walkList func(list []ast.Stmt, depth int, lockScope scopePos, pathIndex int, inLockList bool, ancestors []scopePos)
	walkList = func(list []ast.Stmt, depth int, lockScope scopePos, pathIndex int, inLockList bool, ancestors []scopePos) {
		for i, s := range list {
			if inLockList {
				// The path to anything in this statement runs through it.
				pathIndex = i
			}
			chain := append(append([]scopePos{}, ancestors...), scopePos{list: list, index: i})
			for _, call := range directWrites(s) {
				out = append(out, writeInfo{call: call, chain: chain, depth: depth, lockScope: lockScope, pathIndex: pathIndex})
			}
			// A deferred or spawned write runs outside the hold.
			for _, call := range detachedWrites(s) {
				out = append(out, writeInfo{call: call, chain: chain})
			}
			// A closure body is judged on its own, unprotected.
			for _, closure := range closureBodies(s) {
				walkList(closure.List, 0, scopePos{}, 0, false, nil)
			}
			// Nested statement lists (block, if/else, loop, switch/select
			// case) inherit the depth at the statement.
			for _, nested := range nestedLists(s) {
				walkList(nested, depth, lockScope, pathIndex, false, chain)
			}
			switch {
			case isForkLockCall(s, "RLock"):
				if depth == 0 {
					lockScope = scopePos{list: list, index: i}
					inLockList = true
				}
				depth++
			case isForkLockCall(s, "RUnlock"):
				// Decrement, never zero: a release closes one acquisition, so
				// RLock/RLock/RUnlock/RLock leaves one read lock held.
				if depth > 0 {
					depth--
				}
			default:
				// A compound statement can drop the depth without a
				// top-level RUnlock: `if c { RUnlock() }` releases on the
				// taken path. This is deliberately fail-closed. Modelling it
				// per path would need a control-flow analysis this audit is
				// not; the cost is that a balanced RLock/RUnlock pair inside
				// a branch also clears the depth (a loud false-RED), which
				// is the safe direction for a hazard audit.
				if containsPlainRUnlock(s) {
					depth = 0
				}
			}
		}
	}
	walkList(body.List, 0, scopePos{}, 0, false, nil)
	return out
}

// containsPlainRUnlock reports whether any statement nested inside s releases
// the ForkLock read lock with a plain (non-deferred) RUnlock.
func containsPlainRUnlock(s ast.Stmt) bool {
	found := false
	ast.Inspect(s, func(n ast.Node) bool {
		if found {
			return false
		}
		expr, ok := n.(*ast.ExprStmt)
		if !ok {
			return true
		}
		call, ok := expr.X.(*ast.CallExpr)
		if !ok {
			return true
		}
		if isForkLockSelector(call.Fun, "RUnlock") {
			found = true
			return false
		}
		return true
	})
	return found
}

// directWrites returns the write calls in stmt that are not inside a nested
// statement list, function literal, or deferred/spawned call — those are
// walked separately, so each write is attributed to its innermost scope
// exactly once.
func directWrites(stmt ast.Stmt) []*ast.CallExpr {
	var calls []*ast.CallExpr
	ast.Inspect(stmt, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.BlockStmt, *ast.CaseClause, *ast.CommClause, *ast.FuncLit, *ast.DeferStmt, *ast.GoStmt:
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok && isWriteCall(call, "WriteFile") {
			calls = append(calls, call)
		}
		return true
	})
	return calls
}

// detachedWrites returns the write calls stmt defers or spawns. Neither holds
// the enclosing lock across the write: a deferred call runs at function exit,
// after any release, and a spawned call runs on another goroutine that was
// never protected by it. They are judged at depth 0.
func detachedWrites(stmt ast.Stmt) []*ast.CallExpr {
	switch stmt.(type) {
	case *ast.DeferStmt, *ast.GoStmt:
	default:
		return nil
	}
	var calls []*ast.CallExpr
	ast.Inspect(stmt, func(n ast.Node) bool {
		if _, isLit := n.(*ast.FuncLit); isLit {
			return false // a spawned/deferred closure is walked as its own body
		}
		if call, ok := n.(*ast.CallExpr); ok && isWriteCall(call, "WriteFile") {
			calls = append(calls, call)
		}
		return true
	})
	return calls
}

// closureBodies returns the function-literal bodies directly inside stmt.
func closureBodies(stmt ast.Stmt) []*ast.BlockStmt {
	var bodies []*ast.BlockStmt
	ast.Inspect(stmt, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.BlockStmt, *ast.CaseClause, *ast.CommClause:
			return false
		}
		if lit, ok := n.(*ast.FuncLit); ok && lit.Body != nil {
			bodies = append(bodies, lit.Body)
			return false
		}
		return true
	})
	return bodies
}

// nestedLists returns the statement lists that stmt directly introduces.
func nestedLists(stmt ast.Stmt) [][]ast.Stmt {
	switch v := stmt.(type) {
	case *ast.BlockStmt:
		return [][]ast.Stmt{v.List}
	case *ast.IfStmt:
		lists := [][]ast.Stmt{v.Body.List}
		switch e := v.Else.(type) {
		case *ast.BlockStmt:
			lists = append(lists, e.List)
		case *ast.IfStmt:
			lists = append(lists, nestedLists(e)...)
		}
		return lists
	case *ast.ForStmt:
		return [][]ast.Stmt{v.Body.List}
	case *ast.RangeStmt:
		return [][]ast.Stmt{v.Body.List}
	case *ast.SwitchStmt:
		return [][]ast.Stmt{v.Body.List}
	case *ast.TypeSwitchStmt:
		return [][]ast.Stmt{v.Body.List}
	case *ast.SelectStmt:
		return [][]ast.Stmt{v.Body.List}
	case *ast.CaseClause:
		return [][]ast.Stmt{v.Body}
	case *ast.CommClause:
		return [][]ast.Stmt{v.Body}
	case *ast.LabeledStmt:
		return nestedLists(v.Stmt)
	}
	return nil
}

// releasesAfter reports whether the acquisitions a write executes under are
// closed. Releases are counted two ways: an unconditional deferred RUnlock
// registered at the top level of a scope on the write's chain *before* the
// statement that leads to the write, and a plain RUnlock later in the
// statement list that established the lock.
//
// Both restrictions matter. The plain release must be in the lock's own list:
// a release inside the branch beside the write does not close an acquisition
// made outside that branch, so it would leak on every other path. The deferred
// release must be registered before the write: a defer placed after a write
// whose error branch can return never runs on that path, so the lock leaks
// exactly where the write failed. At least as many releases as the write's
// depth are required, which is what catches a partial release under a deeper
// lock.
func releasesAfter(w writeInfo) bool {
	if w.depth == 0 {
		return false
	}
	// The deferred release must be registered in the SAME statement list that
	// established the lock, before the write: a defer inside a conditional
	// block is only registered on that path, so it cannot close an
	// acquisition taken unconditionally outside it.
	releases := deferredUnlocksBefore(w.lockScope.list, w.pathIndex)
	// A plain release must likewise be in the lock's own list, after the
	// write's leading statement (not merely after the lock: a pre-write
	// RUnlock is already reflected in w.depth, and crediting it again would
	// let a release the error path skips appear covered).
	for i := max(w.lockScope.index+1, w.pathIndex+1); i < len(w.lockScope.list); i++ {
		if !isForkLockCall(w.lockScope.list[i], "RUnlock") {
			continue
		}
		if terminatesEarlyBetween(w.lockScope.list, w.pathIndex, i) {
			// t.Fatal* runs runtime.Goexit and a return leaves the
			// function; either skips the plain RUnlock on exactly the path
			// where the write failed, leaking the lock.
			continue
		}
		releases++
	}
	return releases >= w.depth
}

// terminatesEarlyBetween reports whether any statement in list[from:to] can
// terminate the function before the next statement runs — a return, panic, or
// a t.Fatal*/FailNow/Goexit/os.Exit call.
func terminatesEarlyBetween(list []ast.Stmt, from, to int) bool {
	for i := max(0, from); i < to && i < len(list); i++ {
		if terminatesEarly(list[i]) {
			return true
		}
	}
	return false
}

// terminatesEarly reports whether stmt can end the function or goroutine
// before the following statement runs.
func terminatesEarly(stmt ast.Stmt) bool {
	early := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		if early {
			return false
		}
		switch v := n.(type) {
		case *ast.ReturnStmt:
			early = true
		case *ast.CallExpr:
			switch fun := v.Fun.(type) {
			case *ast.Ident:
				early = fun.Name == "panic"
			case *ast.SelectorExpr:
				switch fun.Sel.Name {
				case "Fatal", "Fatalf", "FailNow", "Goexit", "Exit":
					early = true
				}
			}
		}
		return !early
	})
	return early
}

// deferredUnlocksBefore counts the top-level `defer syscall.ForkLock.RUnlock()`
// statements registered before position limit in one statement list.
func deferredUnlocksBefore(list []ast.Stmt, limit int) int {
	count := 0
	for i, s := range list {
		if i >= limit {
			break
		}
		if deferStmt, ok := s.(*ast.DeferStmt); ok && isForkLockSelector(deferStmt.Call.Fun, "RUnlock") {
			count++
		}
	}
	return count
}

// balancedLocks reports whether body acquires and releases the ForkLock read
// lock the same number of times, so no acquisition is left held.
func balancedLocks(body *ast.BlockStmt) bool {
	locks, unlocks := 0, 0
	ast.Inspect(body, func(n ast.Node) bool {
		expr, ok := n.(*ast.ExprStmt)
		if !ok {
			return true
		}
		call, ok := expr.X.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch {
		case isForkLockSelector(call.Fun, "RLock"):
			locks++
		case isForkLockSelector(call.Fun, "RUnlock"):
			unlocks++
		}
		return true
	})
	ast.Inspect(body, func(n ast.Node) bool {
		deferStmt, ok := n.(*ast.DeferStmt)
		if ok && isForkLockSelector(deferStmt.Call.Fun, "RUnlock") {
			unlocks++
		}
		return true
	})
	return locks == unlocks
}

// isForkLockCall reports whether stmt is `syscall.ForkLock.<method>()`.
func isForkLockCall(stmt ast.Stmt, method string) bool {
	expr, ok := stmt.(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := expr.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	return isForkLockSelector(call.Fun, method)
}

// isForkLockSelector reports whether expr is `syscall.ForkLock.<method>`.
func isForkLockSelector(expr ast.Expr, method string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != method {
		return false
	}
	field, ok := sel.X.(*ast.SelectorExpr)
	if !ok || field.Sel.Name != "ForkLock" {
		return false
	}
	pkg, ok := field.X.(*ast.Ident)
	return ok && pkg.Name == "syscall"
}
