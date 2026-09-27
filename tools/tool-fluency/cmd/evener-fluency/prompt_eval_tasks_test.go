package main

import (
	"go/format"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// promptEvalTasksDir holds the prompt rewrite's evaluation tasks.
const promptEvalTasksDir = "../../../prompt-eval/tasks"

// TestPromptEvalTasks checks every evaluation task offline. Each manifest
// decodes with no unknown fields, so a misspelled field cannot silently turn
// a check off. When a task has a reference solution, its checks fail on the
// untouched fixture and pass after the solution. A task with no reference
// asks for no change, so its checks pass untouched. A task whose checks pass
// before the agent acts, or that no solution can pass, would measure nothing.
func TestPromptEvalTasks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("task checks run under bash")
	}
	paths, err := filepath.Glob(filepath.Join(promptEvalTasksDir, "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatalf("no tasks under %s", promptEvalTasksDir)
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			probe := decodeTaskStrict(t, path)
			if !strings.HasPrefix(probe.ID, "prose.") {
				t.Errorf("id %q, want the prose. prefix", probe.ID)
			}
			if strings.TrimSpace(probe.Prompt) == "" {
				t.Fatal("empty prompt")
			}
			if bad := unformattedFixtureFiles(probe.Fixture); len(bad) > 0 {
				t.Errorf("fixture Go files %v do not parse or are not gofmt-formatted, so an agent that formats the tree would change them", bad)
			}
			for _, c := range probe.Expect.Checks {
				if c.Name == "" || c.Run == "" {
					t.Fatalf("check %+v needs a name and a command", c)
				}
			}
			if len(probe.Expect.Checks) == 0 {
				return
			}
			untouched := filepath.Join(t.TempDir(), "work")
			if err := materializeFixture(untouched, probe.Fixture); err != nil {
				t.Fatal(err)
			}
			failing := failingChecks(untouched, probe.Expect.Checks)
			if probe.Reference == "" {
				if len(failing) > 0 {
					t.Fatalf("checks %v fail on the untouched fixture, and the task has no reference solution", failing)
				}
				return
			}
			if len(failing) == 0 {
				t.Fatal("every check passes before the agent acts, so the task measures nothing")
			}
			solved := filepath.Join(t.TempDir(), "work")
			if err := materializeFixture(solved, probe.Fixture); err != nil {
				t.Fatal(err)
			}
			if ok, detail := runCheck(solved, checkSpec{Name: "reference", Run: probe.Reference}, 3*time.Minute); !ok {
				t.Fatalf("reference solution failed: %s", detail)
			}
			if failing := failingChecks(solved, probe.Expect.Checks); len(failing) > 0 {
				t.Fatalf("checks %v fail after the reference solution", failing)
			}
		})
	}
}

func decodeTaskStrict(t *testing.T, path string) probeFile {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := decodeProbe(data)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return probe
}

// unformattedFixtureFiles names the fixture's Go files that do not parse or
// that gofmt would change. An agent that formats the tree must leave the
// fixture as it was, or a check such as "tests unchanged" fails for a reason
// that has nothing to do with the prompt.
func unformattedFixtureFiles(fixture fixtureSpec) []string {
	var bad []string
	for _, files := range []map[string]string{fixture.Files, fixture.Untracked} {
		for name, content := range files {
			if !strings.HasSuffix(name, ".go") {
				continue
			}
			if formatted, err := format.Source([]byte(content)); err != nil || string(formatted) != content {
				bad = append(bad, name)
			}
		}
	}
	slices.Sort(bad)
	return bad
}

func TestUnformattedFixtureFilesFindsSpacesAndParseErrors(t *testing.T) {
	t.Parallel()
	got := unformattedFixtureFiles(fixtureSpec{
		Files: map[string]string{
			"spaces.go": "package a\n\nfunc F() {\n    return\n}\n",
			"broken.go": "package b\n\nfunc {\n",
			"fine.go":   "package c\n",
			"notes.txt": "  not Go\n",
		},
		Untracked: map[string]string{"loose.go": "package d\n\nfunc   G() {}\n"},
	})
	if want := []string{"broken.go", "loose.go", "spaces.go"}; !slices.Equal(got, want) {
		t.Errorf("unformattedFixtureFiles = %v, want %v", got, want)
	}
}

// TestShippedProbesDecodeStrictly: the runner refuses unknown fields, so every
// probe manifest in the repository has to decode strictly.
func TestShippedProbesDecodeStrictly(t *testing.T) {
	t.Parallel()
	probes, err := loadProbes(filepath.Join("..", "..", "probes"), "all")
	if err != nil || len(probes) == 0 {
		t.Fatalf("loadProbes = %d probes, %v", len(probes), err)
	}
}

// TestLoadProbesRefusesAMisspelledField: the runner decodes every manifest
// strictly, including the copies a lab runs from, so a misspelled field
// cannot silently turn a check off.
func TestLoadProbesRefusesAMisspelledField(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "task.yaml"), "schema: 1\nid: prose.x\nprompt: p\nexpect:\n  allow_tool_error: true\n")
	if _, err := loadProbes(dir, "all"); err == nil || !strings.Contains(err.Error(), "allow_tool_error") {
		t.Fatalf("loadProbes = %v, want an error naming the misspelled field", err)
	}
}

func failingChecks(workDir string, checks []checkSpec) []string {
	var names []string
	for _, c := range checks {
		if ok, _ := runCheck(workDir, c, checkTimeout); !ok {
			names = append(names, c.Name)
		}
	}
	return names
}
