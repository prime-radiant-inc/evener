package main

import (
	"bytes"
	"go/format"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
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
			// An agent that formats the tree must leave the fixture as it was,
			// or a check such as "tests unchanged" fails for a reason that has
			// nothing to do with the prompt.
			for _, files := range []map[string]string{probe.Fixture.Files, probe.Fixture.Untracked} {
				for name, content := range files {
					if !strings.HasSuffix(name, ".go") {
						continue
					}
					formatted, err := format.Source([]byte(content))
					if err != nil {
						t.Errorf("fixture %s does not parse: %v", name, err)
					} else if string(formatted) != content {
						t.Errorf("fixture %s is not gofmt-formatted", name)
					}
				}
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
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var probe probeFile
	if err := dec.Decode(&probe); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return probe
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
