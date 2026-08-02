package serf_test

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	lintMakefilePath   = "Makefile"
	lintCIWorkflowPath = ".github/workflows/ci.yml"
)

// TestCIRunsEveryFamilyOfTheLintGate keeps the Makefile the only list of lint
// families. CI used to re-type the families one step at a time, and a family
// added to the local gate (`lint-serffuzz`, `lint-generated`) silently never
// ran on a pull request — no error, no annotation, nothing to notice. The
// audit passes as soon as CI invokes the aggregate `make lint`, which is what
// the fix does; it still accepts a CI that names every family individually
// through make, so the shape stays free while the list stays single-sourced.
// A family whose command CI inlines by hand (`go run ./cmd/serf-namingcheck`
// instead of `make lint-naming`) counts as missing on purpose: an inlined copy
// is the same silent drift one layer down. Kata wcch.
func TestCIRunsEveryFamilyOfTheLintGate(t *testing.T) {
	families := aggregateLintFamilies(t)
	invoked := makeTargetsRunByWorkflow(t, lintCIWorkflowPath)
	if invoked["lint"] {
		return
	}

	var missing []string
	for _, family := range families {
		if !invoked[family] {
			missing = append(missing, family)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%s never runs `make lint`, and the families it does run are a "+
			"hand-copied subset — these are prerequisites of the Makefile's "+
			"`lint:` rule that no CI step invokes:\n%s\nRun `make lint` so the "+
			"Makefile stays the only place the family list lives.",
			lintCIWorkflowPath, strings.Join(missing, "\n"))
	}
}

// TestCISetsStrictGitleaksModeOnScanSteps keeps CI's required-tool policy at
// the workflow boundary. Local callers intentionally retain the optional skip;
// only the two CI steps that own real secret scans must opt into failure.
func TestCISetsStrictGitleaksModeOnScanSteps(t *testing.T) {
	data, err := os.ReadFile(lintCIWorkflowPath)
	if err != nil {
		t.Fatalf("read %s: %v", lintCIWorkflowPath, err)
	}
	var workflow githubActionsWorkflow
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse %s: %v", lintCIWorkflowPath, err)
	}

	scanSteps := 0
	for jobName, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if !strings.Contains(step.Run, "make lint") && !strings.Contains(step.Run, "make fuzz-corpus-scan") {
				continue
			}
			scanSteps++
			if step.Env["SERF_GITLEAKS_REQUIRED"] != "1" {
				t.Fatalf("job %s step %q runs a CI secret scan without SERF_GITLEAKS_REQUIRED=1", jobName, step.Name)
			}
		}
	}
	if scanSteps != 2 {
		t.Fatalf("found %d CI secret-scan steps; expected exactly 2", scanSteps)
	}
}

func TestMakeTargetsRunByWorkflowUsesShellCommandBoundaries(t *testing.T) {
	path := t.TempDir() + "/workflow.yml"
	const workflow = `jobs:
  probe:
    steps:
      - run: |
          # make comment-only should not count
          echo make echoed
          printf '%s\n' "make quoted"
          echo ready # make inline-comment
          make lint
          true && make vet
          make fuzz # a real command with a trailing comment
`
	if err := os.WriteFile(path, []byte(workflow), 0o600); err != nil {
		t.Fatalf("write workflow fixture: %v", err)
	}

	targets := makeTargetsRunByWorkflow(t, path)
	for _, want := range []string{"lint", "vet", "fuzz"} {
		if !targets[want] {
			t.Fatalf("fixture lost real make target %q: %v", want, targets)
		}
	}
	for _, unwanted := range []string{"echoed", "quoted", "inline-comment"} {
		if targets[unwanted] {
			t.Fatalf("fixture treated shell text as make target %q: %v", unwanted, targets)
		}
	}
}

// TestEveryLintFamilyJoinsTheAggregateGate catches the other end of the same
// drift: a `lint-*` rule that exists in the Makefile but hangs off nothing, so
// neither `make lint` nor CI ever runs it. Name-based by necessity — a family
// called something other than `lint-*` (secret-scan is the one today) can only
// be found by reading — so this is a floor, not a proof. Kata wcch.
func TestEveryLintFamilyJoinsTheAggregateGate(t *testing.T) {
	families := map[string]bool{}
	for _, family := range aggregateLintFamilies(t) {
		families[family] = true
	}

	var orphans []string
	for _, target := range definedLintFamilyTargets(t) {
		if !families[target] {
			orphans = append(orphans, target)
		}
	}
	if len(orphans) > 0 {
		t.Fatalf("%s defines lint families that the `lint:` rule does not depend "+
			"on, so nothing runs them — not the local gate, not CI:\n%s\nAdd them "+
			"to `lint:`.", lintMakefilePath, strings.Join(orphans, "\n"))
	}
}

// aggregateLintFamilies returns the prerequisites of the Makefile's `lint:`
// rule — the canonical list of lint families.
func aggregateLintFamilies(t *testing.T) []string {
	t.Helper()
	for _, line := range makefileLines(t) {
		if !strings.HasPrefix(line, "lint:") {
			continue
		}
		families := strings.Fields(strings.TrimPrefix(line, "lint:"))
		if len(families) == 0 {
			t.Fatalf("%s: the `lint:` rule has no prerequisites", lintMakefilePath)
		}
		return families
	}
	t.Fatalf("%s: no `lint:` rule to read the family list from", lintMakefilePath)
	return nil
}

// definedLintFamilyTargets returns every `lint-*` rule the Makefile defines.
func definedLintFamilyTargets(t *testing.T) []string {
	t.Helper()
	var targets []string
	for _, line := range makefileLines(t) {
		if !strings.HasPrefix(line, "lint-") {
			continue
		}
		name, _, found := strings.Cut(line, ":")
		if !found || strings.ContainsAny(name, " \t") {
			continue
		}
		targets = append(targets, name)
	}
	return targets
}

func makefileLines(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(lintMakefilePath)
	if err != nil {
		t.Fatalf("read %s: %v", lintMakefilePath, err)
	}
	return strings.Split(string(raw), "\n")
}

// makeTargetsRunByWorkflow returns every make target the workflow's run: steps
// invoke. It reads the token after each bare `make`, so a flagged invocation
// (`make -C dir target`) records the flag and not the target — deliberately
// conservative, because a missed target is a loud audit failure while an
// invented one would be a silent pass.
func makeTargetsRunByWorkflow(t *testing.T, path string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var workflow githubActionsWorkflow
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	targets := map[string]bool{}
	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			// Inspect only the executable word at each shell command boundary.
			// A `make lint` written in a comment or echoed as output is prose,
			// and prose is exactly the state this audit rejects.
			for _, command := range shellCommandInvocations(step.Run) {
				if len(command) >= 2 && command[0] == "make" {
					targets[command[1]] = true
				}
			}
		}
	}
	return targets
}
