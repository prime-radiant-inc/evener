package evener_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
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
	Env  map[string]string `yaml:"env"`
}

type cacheWorkflowJob struct {
	If          string            `yaml:"if"`
	Permissions map[string]string `yaml:"permissions"`
	Strategy    struct {
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

// fallbackKind is the retired kind a setup-toolchain step also restores when
// its own kind has no entry, or "" when it restores no fallback.
func fallbackKind(step cacheWorkflowStep) string {
	if step.Uses == "./.github/actions/setup-toolchain" {
		return step.With["go-cache-fallback-kind"]
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
// agent-1 compiles. Two kinds duplicate that build per main push: the
// duplicate added about 0.75GB to a two-generation peak that already exceeds
// the 10GB budget, so dropping it lowers the pressure that evicts small
// never-restored kinds.
func TestAgentRaceLanesShareOneCacheKind(t *testing.T) {
	jobs := readCacheWorkflow(t, ".github/workflows/ci.yml")
	job, ok := jobs["race-module-shards"]
	if !ok {
		t.Fatal("ci.yml has no race-module-shards job")
	}
	restored := map[string]map[string]bool{}
	saved := map[string]map[string]bool{}
	fallback := map[string]string{}
	for _, cell := range matrixCells(job) {
		scope := cell["scope"]
		restored[scope] = map[string]bool{}
		saved[scope] = map[string]bool{}
		kind := ""
		for _, step := range job.Steps {
			if !stepRunsInCell(step, cell) {
				continue
			}
			if k := fallbackKind(step); k != "" {
				fallback[scope] = expandMatrix(k, cell)
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
	// The old race-agent-2 entry is the only warm cache either shard can use
	// until agent-2 saves the shared kind on main, so both agent cells restore
	// it as a fallback; otherwise every run between the change and that save
	// builds the whole race binary cold.
	for _, scope := range []string{"agent-1", "agent-2"} {
		if fallback[scope] != "race-agent-2" {
			t.Errorf("race-module-shards %s restores fallback %q, want the retired kind race-agent-2", scope, fallback[scope])
		}
	}
	if fallback["nonagent"] != "" {
		t.Errorf("race-module-shards nonagent restores fallback %q, want none", fallback["nonagent"])
	}
	if got := restored["nonagent"]; len(got) != 1 || !got["race-nonagent"] {
		t.Errorf("race-module-shards nonagent restores %v, want race-nonagent", got)
	}
	if !saved["nonagent"]["race-nonagent"] {
		t.Error("nonagent does not save race-nonagent, the only kind it restores")
	}
}

// The shared race-agent kind has no main entry until agent-2 saves one, so
// go-cache-restore must also try the retired kind's prefix. The test runs the
// action's own key-resolution script with the input the shards pass, so a
// restore-keys expression that ignores the fallback fails it.
func TestGoCacheRestoreTriesTheFallbackKind(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash not installed: %v", err)
	}
	steps := readActionSteps(t, ".github/actions/go-cache-restore/action.yml")
	var resolve, restore cacheWorkflowStep
	for _, step := range steps {
		switch step.Name {
		case "Resolve Go cache keys":
			resolve = step
		case "Restore Go build cache":
			restore = step
		}
	}
	if resolve.Run == "" || restore.Uses == "" {
		t.Fatal("go-cache-restore has no key-resolution or restore step")
	}
	if got := resolve.Env["FALLBACK_KIND"]; got != "${{ inputs.fallback-kind }}" {
		t.Errorf("key resolution reads FALLBACK_KIND %q, want the fallback-kind input", got)
	}
	if got := restore.With["restore-keys"]; got != "${{ steps.keys.outputs.restore-keys }}" {
		t.Errorf("Restore Go build cache restore-keys %q, want the resolved keys", got)
	}
	goEnv := func(name string) string {
		t.Helper()
		out, err := exec.Command("go", "env", name).Output()
		if err != nil {
			t.Fatalf("go env %s: %v", name, err)
		}
		return strings.TrimSpace(string(out))
	}
	suffix := "ubuntu24-" + goEnv("GOARCH") + "-" + goEnv("GOVERSION") + "-"
	resolved := func(fallback string) string {
		t.Helper()
		dir := t.TempDir()
		outputPath := filepath.Join(dir, "output")
		cmd := exec.Command("bash", "-c", resolve.Run)
		cmd.Env = append(os.Environ(),
			"KIND=race-agent",
			"FALLBACK_KIND="+fallback,
			"ImageOS=ubuntu24",
			"GITHUB_OUTPUT="+outputPath,
			"GITHUB_ENV="+filepath.Join(dir, "env"),
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("key resolution with fallback %q: %v\n%s", fallback, err, out)
		}
		data, err := os.ReadFile(outputPath)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	shared, fallback := "go-race-agent-"+suffix, "go-race-agent-2-"+suffix
	if got, want := resolved("race-agent-2"), "restore-keys<<EOF\n"+shared+"\n"+fallback+"\nEOF\n"; !strings.Contains(got, want) {
		t.Errorf("keys with a fallback:\n%s\nwant a restore-keys block\n%s", got, want)
	}
	if got, want := resolved(""), "restore-keys="+shared+"\n"; !strings.Contains(got, want) {
		t.Errorf("keys without a fallback:\n%s\nwant %q", got, want)
	}
}

// A generation transition changes more of a key than the commit: a Go version
// bump rewrites the Go component of every go-* and gomod-* key, and a lint
// config bump rewrites the config hash of a golangci-lint key. cache-prune
// must group those keys by kind so a transition still leaves one generation,
// or the two generations ride the budget for 7 days. The iOS DerivedData kind
// keeps its per-lock grouping: a lock change is a different build, and two
// such entries are not generations of one key. The test feeds a fixed cache
// listing through the job's own jq and awk programs, so a pipeline that keeps
// an older entry or selects the wrong ids fails it.
func TestCachePruneKeepsOneGenerationPerKind(t *testing.T) {
	jqPath, err := exec.LookPath("jq")
	if err != nil {
		t.Skipf("jq not installed: %v", err)
	}
	awkPath, err := exec.LookPath("awk")
	if err != nil {
		t.Skipf("awk not installed: %v", err)
	}
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
	var jqProgram string
	for _, match := range regexp.MustCompile(`--jq '([^']*)'`).FindAllStringSubmatch(script.String(), -1) {
		if strings.Contains(match[1], "select(") {
			jqProgram = match[1]
		}
	}
	if jqProgram == "" {
		t.Fatalf("cache-prune has no jq key filter:\n%s", script.String())
	}
	awkMatch := regexp.MustCompile(`awk '([^']*)'`).FindStringSubmatch(script.String())
	if awkMatch == nil {
		t.Fatalf("cache-prune has no awk superseded filter:\n%s", script.String())
	}

	// Real hashes are hex, so the job's filters must see hex.
	hash := func(n int) string { return strings.Repeat("0", 38) + fmt.Sprintf("%02x", n) }
	config := func(n int) string { return strings.Repeat("0", 62) + fmt.Sprintf("%02x", n) }
	derivedData := func(pod, pkg, commit int) string {
		return "ios-deriveddata-macos26-xcode-26.6-" + hash(pod) + "-" + hash(pkg) + "-" + hash(commit)
	}
	type cacheEntry struct {
		ID  int64  `json:"id"`
		Key string `json:"key"`
	}
	// Newest first, as the job lists them, so the first entry of each kind is
	// the one it keeps.
	listing := []cacheEntry{
		{101, "go-tests-root-ubuntu24-amd64-go1.27.4-" + hash(1)},
		{100, "go-tests-root-ubuntu24-amd64-go1.27.4-" + hash(2)},
		{99, "go-tests-root-ubuntu24-amd64-go1.28.0-" + hash(3)},
		{98, "gomod-ubuntu24-amd64-go1.28.0-" + hash(4)},
		{97, "gomod-ubuntu24-amd64-go1.27.4-" + hash(5)},
		{96, "golangci-lint-ubuntu24-go1.28.0-" + config(6) + "-" + hash(7)},
		{95, "golangci-lint-ubuntu24-go1.27.4-" + config(8) + "-" + hash(9)},
		{94, "golangci-lint-tagged-ubuntu24-go1.27.4-" + config(10) + "-" + hash(11)},
		{93, "go-race-agent-ubuntu24-amd64-go1.27.4-" + hash(12)},
		{92, "go-race-agent-2-ubuntu24-amd64-go1.27.4-" + hash(13)},
		{91, "go-race-agent-1-ubuntu24-amd64-go1.27.4-" + hash(14)},
		{90, "go-race-nonagent-ubuntu24-amd64-go1.27.4-" + hash(15)},
		{89, "go-static-build-ubuntu24-amd64-go1.27.4-" + hash(16)},
		{88, derivedData(17, 18, 19)},
		{87, derivedData(17, 18, 20)},
		{86, derivedData(21, 22, 23)},
		{85, "setup-ruby-bundler-cache-v6-macos-16-arm64-ruby-3.3.6-" + hash(24)},
	}
	payload, err := json.Marshal(listing)
	if err != nil {
		t.Fatal(err)
	}
	listed := exec.Command(jqPath, "-r", jqProgram)
	listed.Stdin = bytes.NewReader(payload)
	entries, err := listed.CombinedOutput()
	if err != nil {
		t.Fatalf("jq %s: %v\n%s", jqProgram, err, entries)
	}
	superseded := exec.Command(awkPath, awkMatch[1])
	superseded.Stdin = bytes.NewReader(entries)
	out, err := superseded.CombinedOutput()
	if err != nil {
		t.Fatalf("awk %s: %v\n%s", awkMatch[1], err, out)
	}
	var deleted []int64
	for field := range strings.FieldsSeq(string(out)) {
		id, err := strconv.ParseInt(field, 10, 64)
		if err != nil {
			t.Fatalf("prune printed %q, not a cache id: %v", field, err)
		}
		deleted = append(deleted, id)
	}
	// The newest entry of each kind survives, including one derived-data entry
	// per lock pair; older entries of a kind go, whether the commit, the Go
	// version, the lint config, or the retired race shard kind differs.
	want := []int64{100, 99, 97, 95, 92, 91, 87}
	if !slices.Equal(deleted, want) {
		t.Errorf("cache-prune deletes %v, want %v", deleted, want)
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

// readWorkflowGrants parses a workflow's top-level permissions.
func readWorkflowGrants(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Permissions map[string]string `yaml:"permissions"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return workflow.Permissions
}

// grantOf is the scope's permission at job level, or the workflow's when the
// job declares no block of its own.
func grantOf(job cacheWorkflowJob, workflow map[string]string, scope string) string {
	if value, ok := job.Permissions[scope]; ok {
		return value
	}
	return workflow[scope]
}

// Every job that saves through go-cache-save deletes the entry its save
// supersedes first, so a main push holds one generation per kind instead of
// two: at this fleet's size that is the difference between ~8.9GB and the
// 16.02GB peak that made GitHub evict entries no run had restored, the iOS
// DerivedData tree among them (issue #7025). Deleting needs actions: write,
// and go-cache-save downgrades a refused delete to a warning, so a missing
// grant silently restores the two-generation peak. The delete must also
// precede the save, in the action and in the iOS lane.
func TestCacheSavesReplaceTheirPredecessor(t *testing.T) {
	index := func(steps []cacheWorkflowStep, name string) int {
		for i, step := range steps {
			if step.Name == name {
				return i
			}
		}
		return -1
	}
	saving := 0
	for _, path := range goCacheWorkflows {
		workflowGrants := readWorkflowGrants(t, path)
		for name, job := range readCacheWorkflow(t, path) {
			for _, step := range job.Steps {
				if step.Uses != "./.github/actions/go-cache-save" {
					continue
				}
				saving++
				if got := grantOf(job, workflowGrants, "actions"); got != "write" {
					t.Errorf("%s %s: go-cache-save deletes a predecessor, actions %q, want write", path, name, got)
				}
				// A job-level permissions block replaces every scope, so the
				// grant must keep the checkout's contents access too.
				if got := grantOf(job, workflowGrants, "contents"); got != "read" && got != "write" {
					t.Errorf("%s %s: go-cache-save job needs contents to check out, contents %q", path, name, got)
				}
			}
		}
	}
	if saving == 0 {
		t.Fatal("no job saves through go-cache-save, so the grants below are unchecked")
	}

	steps := readActionSteps(t, ".github/actions/go-cache-save/action.yml")
	deleted := index(steps, "Delete the cache entries these saves supersede")
	if deleted == -1 {
		t.Fatal("go-cache-save has no delete step")
	}
	if got := steps[deleted].If; !strings.Contains(got, "github.event_name == 'push'") || !strings.Contains(got, "refs/heads/main") {
		t.Errorf("go-cache-save delete if = %q, want a main-push guard", got)
	}
	// The delete has to name the keys go-cache-restore exported, or it lists
	// nothing and deletes nothing while the pipeline stays green.
	for variable, want := range map[string]string{
		"BUILD_PREFIX":   "${{ env.GO_CACHE_PREFIX }}",
		"BUILD_KEY":      "${{ env.GO_CACHE_KEY }}",
		"MODULES_PREFIX": "${{ env.GO_MODULES_PREFIX }}",
		"MODULES_KEY":    "${{ env.GO_MODULES_CACHE_KEY }}",
	} {
		if got := steps[deleted].Env[variable]; got != want {
			t.Errorf("go-cache-save delete %s = %q, want %q", variable, got, want)
		}
	}
	for _, save := range []string{"Save Go build cache", "Save Go module cache"} {
		at := index(steps, save)
		if at == -1 {
			t.Errorf("go-cache-save has no %q step", save)
		} else if at < deleted {
			t.Errorf("go-cache-save saves %q at step %d, before the delete at %d", save, at, deleted)
		}
	}

	ios := readCacheWorkflow(t, ".github/workflows/ios-qualification.yml")["simulator-build"]
	deleted = index(ios.Steps, "Delete the DerivedData cache entry this save supersedes")
	save := index(ios.Steps, "Save the DerivedData build cache")
	if deleted == -1 || save == -1 {
		t.Fatalf("ios-qualification simulator-build: delete step %d, save step %d, want both", deleted, save)
	}
	if save < deleted {
		t.Errorf("ios-qualification simulator-build: save at %d runs before the delete at %d", save, deleted)
	}
	if got := ios.Permissions["actions"]; got != "write" {
		t.Errorf("ios-qualification simulator-build: actions %q, want write for the delete", got)
	}
	if got := ios.Steps[deleted].If; !strings.Contains(got, "github.event_name == 'push'") || !strings.Contains(got, "refs/heads/main") {
		t.Errorf("ios-qualification delete if = %q, want a main-push guard", got)
	}
	if env := ios.Steps[deleted].Env; env["PREFIX"] != "${{ steps.deriveddata-cache.outputs.prefix }}" ||
		env["KEPT_KEY"] != "${{ steps.deriveddata-cache.outputs.prefix }}${{ github.sha }}" {
		t.Errorf("ios-qualification delete env = %v, want the restore prefix and this commit's key", env)
	}
}
