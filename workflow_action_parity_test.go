package evener_test

import (
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
	workflowPins := actionPins(t, yamlFilesInDir(t, ".github/workflows"))
	compositePins := actionPins(t, compositeActionFiles(t))

	names := make([]string, 0, len(compositePins))
	for name := range compositePins {
		if _, ok := workflowPins[name]; ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	for _, name := range names {
		wantMajor, ok := majorVersion(workflowPins[name])
		if !ok {
			continue
		}
		gotMajor, ok := majorVersion(compositePins[name])
		if !ok {
			continue
		}
		if gotMajor != wantMajor {
			t.Errorf("%s is pinned at v%d in a workflow but v%d in a composite action; dependabot cannot see composite pins, so the pair must be bumped together (workflows: %s, composite: %s)",
				name, wantMajor, gotMajor, workflowPins[name], compositePins[name])
		}
	}
}

// compositeActionFiles returns every local composite action manifest.
func compositeActionFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".github/actions")
	if err != nil {
		t.Fatalf("read .github/actions: %v", err)
	}
	var files []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(".github", "actions", entry.Name(), "action.yml")
		if _, err := os.Stat(path); err == nil {
			files = append(files, path)
		}
	}
	if len(files) == 0 {
		t.Fatal("no composite actions found under .github/actions")
	}
	return files
}

func yamlFilesInDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yml") && !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		files = append(files, filepath.Join(dir, entry.Name()))
	}
	if len(files) == 0 {
		t.Fatalf("no YAML files found in %s", dir)
	}
	return files
}

// actionPins parses each file and maps every "actions/<name>" reference to
// the ref it is pinned at. Workflows hold steps under jobs.<id>.steps while
// composite actions hold them under runs.steps; both shapes are accepted.
func actionPins(t *testing.T, files []string) map[string]string {
	t.Helper()
	pins := make(map[string]string)
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var doc struct {
			Jobs map[string]githubActionsJob `yaml:"jobs"`
			Runs compositeActionRuns         `yaml:"runs"`
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
			name, ref, ok := splitActionRef(step.Uses)
			if ok {
				pins[name] = ref
			}
		}
	}
	return pins
}

type compositeActionRuns struct {
	Steps []githubActionStep `yaml:"steps"`
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
