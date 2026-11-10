package evener_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A lane's Go cache save must not be gated behind the timing budget check.
// The budget fails on wall time while the build cache the run just warmed is
// good, and a save after a failed step keeps everything it restored instead
// of trimming to the job's own build, so a main run that failed the budget
// would save an ever larger entry.
func TestGoCacheSavePrecedesTheTimingBudgetGate(t *testing.T) {
	data, err := os.ReadFile(".github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Uses string `yaml:"uses"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	for _, job := range []string{"web-unit", "tests-parts"} {
		saveIndex, budgetIndex := -1, -1
		for i, step := range workflow.Jobs[job].Steps {
			switch {
			case step.Uses == "./.github/actions/go-cache-save":
				saveIndex = i
			case strings.HasPrefix(step.Name, "Timing budget"):
				budgetIndex = i
			}
		}
		if budgetIndex == -1 {
			t.Fatalf("%s: no timing budget step", job)
		}
		if saveIndex == -1 {
			t.Fatalf("%s: no go-cache-save step", job)
		}
		if saveIndex > budgetIndex {
			t.Errorf("%s: go-cache-save (step %d) runs after the timing budget (step %d); a failed budget check would save the cache untrimmed", job, saveIndex, budgetIndex)
		}
	}
}

// The workflows that restore Go caches through go-cache-restore. Every one
// runs on pushes to main, where go-cache-save acts.
var goCacheWorkflows = []string{".github/workflows/ci.yml", ".github/workflows/docs.yml"}

type cacheWorkflowStep struct {
	Name string            `yaml:"name"`
	Uses string            `yaml:"uses"`
	If   string            `yaml:"if"`
	With map[string]string `yaml:"with"`
}

type cacheWorkflowJob struct {
	Strategy struct {
		Matrix map[string]any `yaml:"matrix"`
	} `yaml:"strategy"`
	Steps []cacheWorkflowStep `yaml:"steps"`
}

func readCacheWorkflow(t *testing.T, path string) map[string]cacheWorkflowJob {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]cacheWorkflowJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return workflow.Jobs
}

var matrixReference = regexp.MustCompile(`\$\{\{\s*matrix\.(\w+)\s*\}\}`)

// matrixCells lists the job's matrix cells, one map of variable to value per
// cell, or a single empty cell for a job without a matrix.
func matrixCells(job cacheWorkflowJob) []map[string]string {
	cells := []map[string]string{{}}
	for name, values := range job.Strategy.Matrix {
		list, ok := values.([]any)
		if !ok {
			continue
		}
		var next []map[string]string
		for _, cell := range cells {
			for _, value := range list {
				grown := map[string]string{name: toString(value)}
				for k, v := range cell {
					grown[k] = v
				}
				next = append(next, grown)
			}
		}
		cells = next
	}
	return cells
}

func toString(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	out, _ := yaml.Marshal(value)
	return strings.TrimSpace(string(out))
}

func expandMatrix(text string, cell map[string]string) string {
	return matrixReference.ReplaceAllStringFunc(text, func(ref string) string {
		return cell[matrixReference.FindStringSubmatch(ref)[1]]
	})
}

var matrixCondition = regexp.MustCompile(`matrix\.(\w+)\s*==\s*'([^']*)'`)

// stepRunsInCell reports whether a step's matrix conditions, if any, hold in
// the cell. Other conditions are runtime facts the cell cannot decide.
func stepRunsInCell(step cacheWorkflowStep, cell map[string]string) bool {
	for _, match := range matrixCondition.FindAllStringSubmatch(step.If, -1) {
		if cell[match[1]] != match[2] {
			return false
		}
	}
	return true
}

// restoredKind is the go-cache-restore kind a step restores, directly or
// through setup-toolchain, or "" when it restores none.
func restoredKind(step cacheWorkflowStep) string {
	switch step.Uses {
	case "./.github/actions/go-cache-restore":
		return step.With["kind"]
	case "./.github/actions/setup-toolchain":
		return step.With["go-cache-kind"]
	}
	return ""
}

// Every Go cache kind a job restores must be saved on main by some job that
// restores it, and the shared module cache by exactly one job. A kind nobody
// saves restores nothing, so its lanes build cold on every run while looking
// cached; a module cache nobody saves leaves every kind downloading modules.
func TestEveryRestoredGoCacheKindIsSaved(t *testing.T) {
	restored := map[string]string{}
	saved := map[string]bool{}
	var moduleSavers []string
	for _, path := range goCacheWorkflows {
		for name, job := range readCacheWorkflow(t, path) {
			for _, cell := range matrixCells(job) {
				kind := ""
				for _, step := range job.Steps {
					if k := restoredKind(step); k != "" {
						kind = expandMatrix(k, cell)
						restored[kind] = path + " " + name
					}
					if step.Uses != "./.github/actions/go-cache-save" || !stepRunsInCell(step, cell) {
						continue
					}
					if kind == "" {
						t.Errorf("%s %s: go-cache-save runs with no go-cache-restore before it", path, name)
						continue
					}
					saved[kind] = true
					if step.With["modules"] == "true" {
						moduleSavers = append(moduleSavers, path+" "+name)
					}
				}
			}
		}
	}
	if len(restored) == 0 {
		t.Fatal("no job restores a Go cache kind")
	}
	for kind, where := range restored {
		if kind == "modules" {
			t.Errorf("%s: kind %q collides with the shared module cache's key", where, kind)
		}
		if !saved[kind] {
			t.Errorf("Go cache kind %q is restored (%s) but no job restoring it saves it", kind, where)
		}
	}
	if len(moduleSavers) != 1 {
		t.Errorf("the shared Go module cache is saved by %d jobs (%v), want exactly one", len(moduleSavers), moduleSavers)
	}
}

// A failed step must not skip a cache save. A lane that fails on main,
// whether on a finding, a timing budget or a timeout from running cold,
// has still filled its caches; a save behind the implicit success() would
// leave the lane restoring nothing, so it runs cold, fails again and never
// saves. go-cache-save's own steps carry !cancelled() as well, since a
// composite step's implicit success() is evaluated on its own.
func TestCacheSavesRunAfterAFailedStep(t *testing.T) {
	for _, path := range goCacheWorkflows {
		for name, job := range readCacheWorkflow(t, path) {
			for _, step := range job.Steps {
				switch {
				case step.Uses == "./.github/actions/go-cache-save":
					if !strings.Contains(step.If, "!cancelled()") {
						t.Errorf("%s %s: go-cache-save if %q lacks !cancelled()", path, name, step.If)
					}
					if step.With["job-status"] != "${{ job.status }}" {
						t.Errorf("%s %s: go-cache-save job-status %q, want ${{ job.status }}", path, name, step.With["job-status"])
					}
				case strings.HasPrefix(step.Uses, "actions/cache/save@"):
					if !strings.Contains(step.If, "!cancelled()") {
						t.Errorf("%s %s: actions/cache/save if %q lacks !cancelled()", path, name, step.If)
					}
				}
			}
		}
	}

	data, err := os.ReadFile(".github/actions/go-cache-save/action.yml")
	if err != nil {
		t.Fatal(err)
	}
	var action struct {
		Runs struct {
			Steps []cacheWorkflowStep `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(data, &action); err != nil {
		t.Fatal(err)
	}
	if len(action.Runs.Steps) == 0 {
		t.Fatal("go-cache-save has no steps")
	}
	for _, step := range action.Runs.Steps {
		if !strings.Contains(step.If, "!cancelled()") {
			t.Errorf("go-cache-save step %q: if %q lacks !cancelled()", step.Name, step.If)
		}
	}
}

// setup-go's and setup-node's built-in caches save from whichever ref first
// misses their key, pull requests and merge-queue branches included, and an
// entry saved there serves only that ref. Those saves crowd main's entries
// out of the repository's cache budget, and each eviction on main makes the
// next pull requests save their own copies. Go caches go through
// go-cache-restore and go-cache-save, which save only on main.
func TestBuiltInToolchainCachesStayOff(t *testing.T) {
	paths := []string{
		".github/workflows/ci.yml",
		".github/workflows/docs.yml",
		".github/workflows/hub-lock-proof.yml",
		".github/workflows/ios-qualification.yml",
	}
	check := func(where string, steps []cacheWorkflowStep) {
		for _, step := range steps {
			switch {
			case strings.HasPrefix(step.Uses, "actions/setup-go@"):
				if step.With["cache"] != "false" {
					t.Errorf("%s: setup-go cache %q, want false", where, step.With["cache"])
				}
			case strings.HasPrefix(step.Uses, "actions/setup-node@"):
				if c, ok := step.With["cache"]; ok {
					t.Errorf("%s: setup-node cache %q, want none", where, c)
				}
			}
		}
	}
	for _, path := range paths {
		for name, job := range readCacheWorkflow(t, path) {
			check(path+" "+name, job.Steps)
		}
	}
	data, err := os.ReadFile(".github/actions/setup-toolchain/action.yml")
	if err != nil {
		t.Fatal(err)
	}
	var action struct {
		Runs struct {
			Steps []cacheWorkflowStep `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(data, &action); err != nil {
		t.Fatal(err)
	}
	check("setup-toolchain", action.Runs.Steps)
}
