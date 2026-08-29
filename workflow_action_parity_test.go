package evener_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Dependabot's github-actions updater watches only the repository root
// (directory: / in .github/dependabot.yml), so it never rewrites pins inside
// .github/actions/*/action.yml. When #284 bumped ci.yml's direct
// actions/setup-node reference to v7, the setup-toolchain composite kept its
// v6 pin and the pair drifted silently. This audit pins the invariant that
// failed: every action used both directly in a workflow and inside a local
// composite action must sit on the same major version.

func TestWorkflowAndCompositeActionPinsShareAMajorVersion(t *testing.T) {
	for _, err := range actionPinParity(t, ".github/workflows", ".github/actions") {
		t.Error(err)
	}
}

// Reviewer reproduction: the audit collapsed per-action workflow pins to
// whichever file was read last, so a v6 in ci.yml hidden behind a v7 in a
// later workflow file matched the composite's v7 and the real drift passed
// silently.
func TestWorkflowAndCompositeActionPinsFlagConflictingRefsAcrossWorkflows(t *testing.T) {
	workflowDir := t.TempDir()
	compositeDir := t.TempDir()

	writeActionFixture(t, filepath.Join(workflowDir, "ci.yml"),
		"jobs:\n  build:\n    steps:\n      - uses: actions/setup-node@v6\n")
	writeActionFixture(t, filepath.Join(workflowDir, "nightly.yml"),
		"jobs:\n  build:\n    steps:\n      - uses: actions/setup-node@v7\n")
	writeActionFixture(t, filepath.Join(compositeDir, "setup-toolchain", "action.yml"),
		"runs:\n  using: composite\n  steps:\n    - uses: actions/setup-node@v7\n")

	errs := actionPinParity(t, workflowDir, compositeDir)
	if len(errs) == 0 {
		t.Fatal("conflicting workflow refs for actions/setup-node were not flagged")
	}
	diagnostic := strings.Join(errs, "; ")
	for _, ref := range []string{"v6", "v7"} {
		if !strings.Contains(diagnostic, ref) {
			t.Errorf("drift diagnostic does not name ref %s: %s", ref, diagnostic)
		}
	}
}

// Reviewer reproduction: refs without a v<n> major (commit SHAs, branch
// names) were silently skipped on both sides, so a SHA-pinned workflow ref
// escaped the invariant entirely.
func TestWorkflowAndCompositeActionPinsRejectRefsWithoutAMajorVersion(t *testing.T) {
	const shaRef = "0123456789abcdef0123456789abcdef01234567"
	workflowDir := t.TempDir()
	compositeDir := t.TempDir()

	writeActionFixture(t, filepath.Join(workflowDir, "ci.yml"),
		"jobs:\n  build:\n    steps:\n      - uses: actions/setup-node@"+shaRef+"\n")
	writeActionFixture(t, filepath.Join(compositeDir, "setup-toolchain", "action.yml"),
		"runs:\n  using: composite\n  steps:\n    - uses: actions/setup-node@v7\n")

	errs := actionPinParity(t, workflowDir, compositeDir)
	if len(errs) == 0 {
		t.Fatal("a SHA-pinned workflow ref was silently accepted")
	}
	if diagnostic := strings.Join(errs, "; "); !strings.Contains(diagnostic, shaRef) {
		t.Errorf("diagnostic does not name the unparseable ref %s: %s", shaRef, diagnostic)
	}
}

// GitHub accepts action.yaml as well as action.yml; discovery must read both
// or a .yaml-only composite escapes the audit entirely.
func TestWorkflowAndCompositeActionPinsReadCompositeActionDotYaml(t *testing.T) {
	workflowDir := t.TempDir()
	compositeDir := t.TempDir()

	writeActionFixture(t, filepath.Join(workflowDir, "ci.yml"),
		"jobs:\n  build:\n    steps:\n      - uses: actions/setup-node@v7\n")
	writeActionFixture(t, filepath.Join(compositeDir, "setup-toolchain", "action.yaml"),
		"runs:\n  using: composite\n  steps:\n    - uses: actions/setup-node@v6\n")

	errs := actionPinParity(t, workflowDir, compositeDir)
	if len(errs) == 0 {
		t.Fatal("an action.yaml composite pinning v6 against a workflow v7 was not flagged")
	}
}

// actionPinParity collects action pins from the workflow and composite trees
// and returns the parity violations between them.
func actionPinParity(t *testing.T, workflowDir, compositeDir string) []string {
	t.Helper()
	return pinParityErrors(
		actionPinRefs(t, workflowYAMLFiles(t, workflowDir)),
		actionPinRefs(t, compositeActionFiles(t, compositeDir)),
	)
}

// workflowYAMLFiles returns every workflow YAML in dir.
func workflowYAMLFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if ext := filepath.Ext(entry.Name()); ext != ".yml" && ext != ".yaml" {
			continue
		}
		files = append(files, filepath.Join(dir, entry.Name()))
	}
	if len(files) == 0 {
		t.Fatalf("no workflow YAML files found in %s", dir)
	}
	return files
}

// compositeActionFiles returns every local composite action manifest. GitHub
// accepts both action.yml and action.yaml, so both spellings are discovered.
func compositeActionFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var files []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if path := existingManifest(t, filepath.Join(dir, entry.Name())); path != "" {
			files = append(files, path)
		}
	}
	if len(files) == 0 {
		t.Fatalf("no composite actions found under %s", dir)
	}
	return files
}

// existingManifest returns the composite action manifest path under dir, or
// "" when the directory declares neither action.yml nor action.yaml.
func existingManifest(t *testing.T, dir string) string {
	t.Helper()
	for _, name := range []string{"action.yml", "action.yaml"} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

// actionPinRefs parses each file and maps every "actions/<name>" reference to
// the set of refs it is pinned at across those files. Workflows hold steps
// under jobs.<id>.steps while composite actions hold them under
// runs.steps; both shapes are accepted.
func actionPinRefs(t *testing.T, files []string) map[string][]string {
	t.Helper()
	pins := make(map[string][]string)
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var doc struct {
			Jobs map[string]githubActionsJob `yaml:"jobs"`
			Runs struct {
				Steps []githubActionStep `yaml:"steps"`
			} `yaml:"runs"`
		}
		if err := yaml.Unmarshal(data, &doc); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		steps := doc.Runs.Steps
		for _, job := range doc.Jobs {
			steps = append(steps, job.Steps...)
		}
		for _, step := range steps {
			if step.Uses == "" {
				continue
			}
			if name, ref, ok := splitActionRef(step.Uses); ok {
				pins[name] = append(pins[name], ref)
			}
		}
	}
	return pins
}

// pinParityErrors compares the refs each action is pinned at inside the
// workflow tree against the refs it is pinned at inside the composite tree.
// An action used in both places must: sit at exactly one ref across all
// workflows, sit at exactly one ref across all composites, and those refs
// must share a major version. Refs without a v<n> major (commit SHAs,
// branches) cannot be compared by major, so they fail loudly instead of
// silently escaping the invariant.
func pinParityErrors(workflowPins, compositePins map[string][]string) []string {
	names := make([]string, 0, len(compositePins))
	for name := range compositePins {
		if _, ok := workflowPins[name]; ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	var errs []string
	for _, name := range names {
		for _, side := range []struct {
			label string
			refs  []string
		}{
			{"workflow", workflowPins[name]},
			{"composite", compositePins[name]},
		} {
			if distinct := distinctRefs(side.refs); len(distinct) > 1 {
				errs = append(errs, fmt.Sprintf("%s is pinned at conflicting refs across %s files (%s); the %s side must agree on a single pin",
					name, side.label, strings.Join(distinct, ", "), side.label))
			}
		}

		workflowMajor, workflowOK := singleMajorVersion(workflowPins[name])
		compositeMajor, compositeOK := singleMajorVersion(compositePins[name])
		switch {
		case !workflowOK || !compositeOK:
			errs = append(errs, fmt.Sprintf("%s is pinned at a ref without a v<n> major version (workflows: %s, composite: %s); the parity audit only understands major-version pins, so switch both sides to one",
				name, strings.Join(workflowPins[name], ", "), strings.Join(compositePins[name], ", ")))
		case workflowMajor != compositeMajor:
			errs = append(errs, fmt.Sprintf("%s is pinned at v%d in a workflow but v%d in a composite action; dependabot cannot see composite pins, so the pair must be bumped together (workflows: %s, composite: %s)",
				name, workflowMajor, compositeMajor, strings.Join(workflowPins[name], ", "), strings.Join(compositePins[name], ", ")))
		}
	}
	return errs
}

// distinctRefs returns the sorted unique refs in refs.
func distinctRefs(refs []string) []string {
	seen := make(map[string]bool, len(refs))
	distinct := make([]string, 0, len(refs))
	for _, ref := range refs {
		if !seen[ref] {
			seen[ref] = true
			distinct = append(distinct, ref)
		}
	}
	sort.Strings(distinct)
	return distinct
}

// singleMajorVersion reports the shared major version of refs, or ok=false
// when refs is empty, holds more than one distinct ref, or any ref has no
// v<n> major.
func singleMajorVersion(refs []string) (int, bool) {
	distinct := distinctRefs(refs)
	if len(distinct) != 1 {
		return 0, false
	}
	return majorVersion(distinct[0])
}

var actionRefPattern = regexp.MustCompile(`^actions/([a-z0-9-]+)@(.+)$`)

// splitActionRef decomposes "actions/setup-node@v7" into name and ref,
// reporting ok only for official actions pinned at a ref.
func splitActionRef(uses string) (name, ref string, ok bool) {
	match := actionRefPattern.FindStringSubmatch(uses)
	if match == nil {
		return "", "", false
	}
	return "actions/" + match[1], match[2], true
}

var majorVersionPattern = regexp.MustCompile(`^v(\d+)`)

// majorVersion extracts the major from a ref like "v7" or "v7.1.2".
func majorVersion(ref string) (int, bool) {
	match := majorVersionPattern.FindStringSubmatch(ref)
	if match == nil {
		return 0, false
	}
	major, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, false
	}
	return major, true
}

// writeActionFixture writes a workflow or composite-action YAML fixture.
func writeActionFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
