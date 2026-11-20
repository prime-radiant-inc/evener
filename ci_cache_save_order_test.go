package evener_test

import (
	"fmt"
	"maps"
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
	jobs := readCacheWorkflow(t, ".github/workflows/ci.yml")
	for _, job := range []string{"web-unit", "tests-parts"} {
		saveIndex, budgetIndex := -1, -1
		for i, step := range jobs[job].Steps {
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
	Run  string            `yaml:"run"`
	With map[string]string `yaml:"with"`
}

type cacheWorkflowJob struct {
	If       string `yaml:"if"`
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
	if len(workflow.Jobs) == 0 {
		t.Fatalf("%s declares no jobs", path)
	}
	return workflow.Jobs
}

// readActionSteps parses a composite action's steps.
func readActionSteps(t *testing.T, path string) []cacheWorkflowStep {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var action struct {
		Runs struct {
			Steps []cacheWorkflowStep `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(data, &action); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if len(action.Runs.Steps) == 0 {
		t.Fatalf("%s has no steps", path)
	}
	return action.Runs.Steps
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
				grown := map[string]string{name: fmt.Sprint(value)}
				maps.Copy(grown, cell)
				next = append(next, grown)
			}
		}
		cells = next
	}
	return cells
}

func expandMatrix(text string, cell map[string]string) string {
	return matrixReference.ReplaceAllStringFunc(text, func(ref string) string {
		return cell[matrixReference.FindStringSubmatch(ref)[1]]
	})
}

var (
	matrixCondition    = regexp.MustCompile(`matrix\.(\w+)\s*==\s*'([^']*)'`)
	matrixNegCondition = regexp.MustCompile(`matrix\.(\w+)\s*!=\s*'([^']*)'`)
)

// stepRunsInCell reports whether a step's matrix conditions, if any, hold in
// the cell. Other conditions are runtime facts the cell cannot decide.
func stepRunsInCell(step cacheWorkflowStep, cell map[string]string) bool {
	for _, match := range matrixCondition.FindAllStringSubmatch(step.If, -1) {
		if cell[match[1]] != match[2] {
			return false
		}
	}
	for _, match := range matrixNegCondition.FindAllStringSubmatch(step.If, -1) {
		if cell[match[1]] == match[2] {
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
					if !stepRunsInCell(step, cell) {
						continue
					}
					if k := restoredKind(step); k != "" {
						kind = expandMatrix(k, cell)
						restored[kind] = path + " " + name
					}
					if step.Uses != "./.github/actions/go-cache-save" {
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
		if !saved[kind] {
			t.Errorf("Go cache kind %q is restored (%s) but no job restoring it saves it", kind, where)
		}
	}
	if len(moduleSavers) != 1 {
		t.Errorf("the shared Go module cache is saved by %d jobs (%v), want exactly one", len(moduleSavers), moduleSavers)
	}
}

// agent-1 and agent-2 build the same agent -race test binary and the same
// evener-dev runner; they differ only in which shards run, and only agent-2
// also compiles the module's subpackages (run-module-tests.sh sends them to
// the last part). One kind saved by agent-2 therefore holds every entry
// agent-1 compiles. Two kinds duplicate that build per main push, and the
// duplicate is what pushes the fleet past the budget and gets a small
// never-restored kind evicted.
func TestAgentRaceLanesShareOneCacheKind(t *testing.T) {
	jobs := readCacheWorkflow(t, ".github/workflows/ci.yml")
	job, ok := jobs["race-module-shards"]
	if !ok {
		t.Fatal("ci.yml has no race-module-shards job")
	}
	restored := map[string]map[string]bool{}
	saved := map[string]map[string]bool{}
	for _, cell := range matrixCells(job) {
		scope := cell["scope"]
		restored[scope] = map[string]bool{}
		saved[scope] = map[string]bool{}
		kind := ""
		for _, step := range job.Steps {
			if !stepRunsInCell(step, cell) {
				continue
			}
			if k := restoredKind(step); k != "" {
				kind = expandMatrix(k, cell)
				restored[scope][kind] = true
			}
			if step.Uses == "./.github/actions/go-cache-save" {
				if kind == "" {
					t.Errorf("race-module-shards %s: go-cache-save runs with no go-cache-restore before it", scope)
					continue
				}
				saved[scope][kind] = true
			}
		}
	}
	for _, scope := range []string{"agent-1", "agent-2"} {
		if got := restored[scope]; len(got) != 1 || !got["race-agent"] {
			t.Errorf("race-module-shards %s restores %v, want the one shared kind race-agent", scope, got)
		}
	}
	if saved["agent-1"]["race-agent"] {
		t.Error("agent-1 saves race-agent; agent-2 holds the superset of agent-1's build, so only agent-2 should save it")
	}
	if !saved["agent-2"]["race-agent"] {
		t.Error("agent-2 does not save race-agent, the only kind the agent shards restore")
	}
	if got := restored["nonagent"]; len(got) != 1 || !got["race-nonagent"] {
		t.Errorf("race-module-shards nonagent restores %v, want race-nonagent", got)
	}
	if !saved["nonagent"]["race-nonagent"] {
		t.Error("nonagent does not save race-nonagent, the only kind it restores")
	}
}

// jqSub is jq's sub/2: replace the first match of re in s with repl.
func jqSub(re *regexp.Regexp, repl, s string) string {
	if loc := re.FindStringIndex(s); loc != nil {
		return s[:loc[0]] + repl + s[loc[1]:]
	}
	return s
}

// A generation transition changes more of a key than the commit: a Go version
// bump rewrites the Go component of every go-* and gomod-* key, and a lint
// config bump rewrites the config hash of a golangci-lint key. cache-prune
// must group those keys by kind so a transition still leaves one generation,
// or the two generations ride the budget for 7 days. The iOS DerivedData kind
// keeps its per-lock grouping: a lock change is a different build, and two
// such entries are not generations of one key. The test reads the sub()
// expressions the job runs, not a copy of them.
func TestCachePruneKeepsOneGenerationPerKind(t *testing.T) {
	jobs := readCacheWorkflow(t, ".github/workflows/ci.yml")
	prune, ok := jobs["cache-prune"]
	if !ok {
		t.Fatal("ci.yml has no cache-prune job")
	}
	var script strings.Builder
	for _, step := range prune.Steps {
		script.WriteString(step.Run)
		script.WriteString("\n")
	}
	matches := regexp.MustCompile(`sub\("([^"]*)";\s*"([^"]*)"\)`).FindAllStringSubmatch(script.String(), -1)
	if len(matches) == 0 {
		t.Fatalf("cache-prune has no key grouping:\n%s", script.String())
	}
	type rewrite struct {
		re   *regexp.Regexp
		repl string
	}
	var subs []rewrite
	for _, match := range matches {
		re, err := regexp.Compile(match[1])
		if err != nil {
			t.Fatalf("cache-prune sub %q does not compile: %v", match[1], err)
		}
		subs = append(subs, rewrite{re, match[2]})
	}
	kindOf := func(key string) string {
		for _, sub := range subs {
			key = jqSub(sub.re, sub.repl, key)
		}
		return key
	}
	hash := func(c byte) string { return strings.Repeat(string(c), 40) }
	config := func(c byte) string { return strings.Repeat(string(c), 64) }
	derivedData := func(pod, pkg, sha byte) string {
		return "ios-deriveddata-macos26-xcode-26.6-" + hash(pod) + "-" + hash(pkg) + "-" + hash(sha)
	}
	cases := []struct {
		name string
		a, b string
		same bool
	}{
		{
			name: "go build cache across a Go version bump",
			a:    "go-tests-root-ubuntu24-amd64-go1.27.4-" + hash('a'),
			b:    "go-tests-root-ubuntu24-amd64-go1.28.0-" + hash('b'),
			same: true,
		},
		{
			name: "module cache across a Go version bump",
			a:    "gomod-ubuntu24-amd64-go1.27.4-" + hash('a'),
			b:    "gomod-ubuntu24-amd64-go1.28.0-" + hash('b'),
			same: true,
		},
		{
			name: "lint cache across a Go version and config bump",
			a:    "golangci-lint-ubuntu24-go1.27.4-" + config('a') + "-" + hash('b'),
			b:    "golangci-lint-ubuntu24-go1.28.0-" + config('c') + "-" + hash('d'),
			same: true,
		},
		{
			name: "tagged lint cache across a Go version and config bump",
			a:    "golangci-lint-tagged-ubuntu24-go1.27.4-" + config('a') + "-" + hash('b'),
			b:    "golangci-lint-tagged-ubuntu24-go1.28.0-" + config('c') + "-" + hash('d'),
			same: true,
		},
		{
			name: "retired race-agent shard kinds",
			a:    "go-race-agent-1-ubuntu24-amd64-go1.27.4-" + hash('a'),
			b:    "go-race-agent-ubuntu24-amd64-go1.28.0-" + hash('b'),
			same: true,
		},
		{
			name: "derived data across a commit",
			a:    derivedData('a', 'b', 'c'),
			b:    derivedData('a', 'b', 'd'),
			same: true,
		},
		{
			name: "derived data across a lock change",
			a:    derivedData('a', 'b', 'c'),
			b:    derivedData('d', 'e', 'f'),
			same: false,
		},
	}
	for _, tc := range cases {
		ga, gb := kindOf(tc.a), kindOf(tc.b)
		if tc.same && ga != gb {
			t.Errorf("%s: cache-prune groups %q as %q and %q as %q, want one generation", tc.name, tc.a, ga, tc.b, gb)
		}
		if !tc.same && ga == gb {
			t.Errorf("%s: cache-prune groups %q and %q as %q, want separate generations", tc.name, tc.a, tc.b, ga)
		}
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

	for _, step := range readActionSteps(t, ".github/actions/go-cache-save/action.yml") {
		if !strings.Contains(step.If, "!cancelled()") {
			t.Errorf("go-cache-save step %q: if %q lacks !cancelled()", step.Name, step.If)
		}
	}
}

// The runner evaluates ${{ }} anywhere in a composite action's metadata,
// descriptions included, and refuses to load the action when an expression
// there uses a function it does not allow in that place: every job calling
// the action then fails at that step. Descriptions name expressions in plain
// words instead.
func TestCompositeActionDescriptionsHoldNoExpressions(t *testing.T) {
	paths := []string{
		".github/actions/go-cache-restore/action.yml",
		".github/actions/go-cache-save/action.yml",
		".github/actions/setup-toolchain/action.yml",
		".github/actions/enforcing-sandbox/action.yml",
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var action struct {
			Description string `yaml:"description"`
			Inputs      map[string]struct {
				Description string `yaml:"description"`
			} `yaml:"inputs"`
		}
		if err := yaml.Unmarshal(data, &action); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		if strings.Contains(action.Description, "${{") {
			t.Errorf("%s: description holds an expression: %q", path, action.Description)
		}
		for name, input := range action.Inputs {
			if strings.Contains(input.Description, "${{") {
				t.Errorf("%s: input %s description holds an expression: %q", path, name, input.Description)
			}
		}
	}
}

// setup-go's and setup-node's built-in caches save from whichever ref first
// misses their key, pull requests and merge-queue branches included, and an
// entry saved there serves only that ref. Those saves crowd main's entries
// out of the repository's cache budget, and each eviction on main makes the
// next pull requests save their own copies. Go caches go through
// go-cache-restore and go-cache-save, which save only on main. A documented
// kind saved by an explicit actions/cache step on main (the iOS DerivedData
// tree) is not one of these built-in toolchain caches.
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
	check("setup-toolchain", readActionSteps(t, ".github/actions/setup-toolchain/action.yml"))
}

// Every kind keyed by commit needs its prefix in cache-prune's filter, or its
// previous generation is never deleted and rides the budget until it expires.
// The test reads the jq filter the job runs, not a copy of it.
func TestCachePruneCoversTheDerivedDataKind(t *testing.T) {
	jobs := readCacheWorkflow(t, ".github/workflows/ci.yml")
	prune, ok := jobs["cache-prune"]
	if !ok {
		t.Fatal("ci.yml has no cache-prune job")
	}
	if !strings.Contains(prune.If, "github.event_name == 'push'") || !strings.Contains(prune.If, "github.ref == 'refs/heads/main'") {
		t.Errorf("cache-prune if = %q, want a main-push guard", prune.If)
	}
	var script strings.Builder
	for _, step := range prune.Steps {
		script.WriteString(step.Run)
		script.WriteString("\n")
	}
	match := regexp.MustCompile(`test\("(\^[^"]+)"\)`).FindStringSubmatch(script.String())
	if match == nil {
		t.Fatalf("cache-prune has no commit-key filter:\n%s", script.String())
	}
	filter, err := regexp.Compile(match[1])
	if err != nil {
		t.Fatalf("cache-prune filter %q does not compile: %v", match[1], err)
	}
	sha := strings.Repeat("a", 40)
	podHash := strings.Repeat("b", 40)
	packageHash := strings.Repeat("c", 40)
	configHash := strings.Repeat("d", 64)
	for _, key := range []string{
		"go-tests-root-ubuntu24-amd64-go1.27.4-" + sha,
		"gomod-ubuntu24-amd64-go1.27.4-" + sha,
		"golangci-lint-tagged-ubuntu24-go1.27.4-" + configHash + "-" + sha,
		"ios-deriveddata-macos26-xcode-26.6-" + podHash + "-" + packageHash + "-" + sha,
	} {
		if !filter.MatchString(key) {
			t.Errorf("cache-prune filter %q does not match saved key %q", match[1], key)
		}
	}
	for _, key := range []string{
		"setup-ruby-bundler-cache-v6-macos-16-arm64-" + sha,
		"ios-deriveddata-macos26-xcode-26.6-" + podHash + "-" + packageHash + "-" + sha + "-extra",
	} {
		if filter.MatchString(key) {
			t.Errorf("cache-prune filter %q matches unkeyed entry %q", match[1], key)
		}
	}
}
