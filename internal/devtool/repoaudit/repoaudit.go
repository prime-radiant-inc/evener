// Package repoaudit holds the rules `make lint` enforces by reading the
// repository's own source, scripts, Makefile and test files: one source for a
// shared constant, one writer for a shared config, no scratch outside TMPDIR,
// no variable-fed recursive delete, no new copy-only test. They are lint
// rather than tests because they fail on what a change writes, not on what
// the code does. `evener-dev dev repo-audit` runs every check from the
// repository root, and lint-repo-audit is its gate.
package repoaudit

import (
	"fmt"
	"io"
	"strings"
	"sync"
)

// check is one rule the audit holds over the repository. name is how a
// failure is reported.
type check struct {
	name string
	run  func(a *audit)
}

// checks is every rule `make lint` holds the repository to.
var checks = []check{
	{"copy-only-tests", noNewCopyOnlyTests},
	{"error-text-assertions", noNewErrorMessageAssertions},
	{"quiet-threshold-one-source", quietThresholdHasOneSource},
	{"custom-content-selection-one-rule", customContentSelectionHasOneRule},
	{"projection-work-policy-in-host", projectionWorkPolicyLivesInTheHost},
	{"effort-vocabulary-one-source", effortVocabularyHasOneSource},
	{"canonical-temp-dir-one-helper", noCommandTestMintsItsOwnCanonicalTempDir},
	{"e2e-providers-toml-one-writer", e2eProvidersTOMLHasOneWriter},
	{"e2e-scripts-pin-providers-config", e2eScriptsPinProvidersConfig},
	{"coverage-scripts-use-covstmt", coverageScriptsUseGoCovstmt},
	{"runner-isolation-shared-unset-helper", runnerIsolationUsesSharedUnsetHelper},
	{"script-scratch-in-tmpdir", noScriptCreatesScratchOutsideTMPDIR},
	{"scratch-dir-not-substituted", scratchDirSubstitutionDoesNotSwallowFailure},
	{"script-recursive-delete", noScriptFeedsVariableToRecursiveDelete},
	{"scratch-trap-before-mkdir", scratchTrapInstalledBeforeMkdir},
	{"makefile-recursive-delete", noMakefileRecipeFeedsVariableToRecursiveDelete},
	{"makefile-scratch-in-tmpdir", noMakefileRecipeCreatesScratchOutsideTMPDIR},
}

// audit collects one check's findings. Errorf records a finding and the check
// goes on; Fatalf records one and ends the check, for a check that cannot read
// what it holds a rule over.
type audit struct {
	findings []string
}

// stopCheck is the panic value Fatal and Fatalf end a check with; runCheck
// recovers it and nothing else.
type stopCheck struct{}

func (a *audit) Error(args ...any) {
	a.findings = append(a.findings, strings.TrimSuffix(fmt.Sprintln(args...), "\n"))
}

func (a *audit) Errorf(format string, args ...any) {
	a.findings = append(a.findings, fmt.Sprintf(format, args...))
}

func (a *audit) Fatal(args ...any) {
	a.Error(args...)
	panic(stopCheck{})
}

func (a *audit) Fatalf(format string, args ...any) {
	a.Errorf(format, args...)
	panic(stopCheck{})
}

// runCheck runs one check and returns its findings.
func runCheck(c check) (findings []string) {
	a := &audit{}
	defer func() {
		if r := recover(); r != nil {
			if _, stopped := r.(stopCheck); !stopped {
				panic(r)
			}
		}
		findings = a.findings
	}()
	c.run(a)
	return a.findings
}

// Run runs every check against the repository in the working directory, which
// must be its root, and reports each finding under its check's name. It
// returns 1 when any check found something and 0 otherwise.
func Run(stdout io.Writer) int {
	return runChecks(checks, stdout)
}

// runChecks runs cs concurrently, since the copy-only scans dominate the wall
// time, and reports them in order so the output is stable.
func runChecks(cs []check, stdout io.Writer) int {
	results := make([][]string, len(cs))
	var wg sync.WaitGroup
	for i, c := range cs {
		wg.Go(func() { results[i] = runCheck(c) })
	}
	wg.Wait()
	status := 0
	for i, c := range cs {
		for _, finding := range results[i] {
			status = 1
			_, _ = fmt.Fprintf(stdout, "FAIL %s: %s\n", c.name, finding)
		}
	}
	if status == 0 {
		_, _ = fmt.Fprintf(stdout, "repo-audit: %d checks passed\n", len(cs))
	}
	return status
}
