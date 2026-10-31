package evener_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type ciDiagnosticStep struct {
	Uses string `yaml:"uses"`
	If   string `yaml:"if"`
	Run  string `yaml:"run"`
	With struct {
		Name           string `yaml:"name"`
		Path           string `yaml:"path"`
		RetentionDays  int    `yaml:"retention-days"`
		IfNoFilesFound string `yaml:"if-no-files-found"`
	} `yaml:"with"`
}

func ciDiagnosticUploads(t *testing.T) map[string]ciDiagnosticStep {
	t.Helper()
	data, err := os.ReadFile(".github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []ciDiagnosticStep `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	uploads := make(map[string]ciDiagnosticStep)
	for _, job := range []string{"tests-parts", "race-root-parts", "race-module-shards"} {
		testIndex := -1
		for i, step := range workflow.Jobs[job].Steps {
			if strings.Contains(step.Run, "make test") {
				testIndex = i
			}
			if !strings.HasPrefix(step.Uses, "actions/upload-artifact@") {
				continue
			}
			if _, exists := uploads[job]; exists {
				t.Fatalf("%s: multiple diagnostic uploads", job)
			}
			if testIndex < 0 || testIndex >= i {
				t.Fatalf("%s: upload precedes its test producer", job)
			}
			uploads[job] = step
		}
	}
	return uploads
}

func TestCIFailureArtifactsWorkflowPolicy(t *testing.T) {
	uploads := ciDiagnosticUploads(t)
	names := make(map[string]bool)
	for _, tc := range []struct {
		job, guard, matrixKey string
		matrixValues          []string
	}{
		{"tests-parts", "failure()", "scope", []string{"root", "nonroot"}},
		{"race-root-parts", "failure() && matrix.part == 'hub'", "part", []string{"hub"}},
		{"race-module-shards", "failure() && matrix.scope == 'agent'", "scope", []string{"agent"}},
	} {
		t.Run(tc.job, func(t *testing.T) {
			step, ok := uploads[tc.job]
			if !ok {
				t.Fatal("missing failure diagnostic upload")
			}
			if step.Uses != "actions/upload-artifact@v7" {
				t.Errorf("action = %q, want existing pinned upload action", step.Uses)
			}
			if step.If != tc.guard {
				t.Errorf("if = %q, want %q", step.If, tc.guard)
			}
			if step.With.RetentionDays != 3 || step.With.IfNoFilesFound != "warn" {
				t.Errorf("retention-days = %d, if-no-files-found = %q", step.With.RetentionDays, step.With.IfNoFilesFound)
			}
			if !strings.Contains(step.With.Name, "${{ github.run_id }}") || !strings.Contains(step.With.Name, "${{ github.run_attempt }}") {
				t.Errorf("artifact name lacks run/attempt identity: %q", step.With.Name)
			}
			for _, value := range tc.matrixValues {
				name := strings.NewReplacer("${{ matrix."+tc.matrixKey+" }}", value, "${{ github.run_id }}", "42", "${{ github.run_attempt }}", "2").Replace(step.With.Name)
				if name == "" || strings.Contains(name, "${{") || names[name] {
					t.Errorf("empty, unresolved or colliding artifact name: %q", name)
				}
				names[name] = true
			}
		})
	}
}

func TestCIFailureArtifactsSelectOnlyDiagnosticLogs(t *testing.T) {
	// Mirror the producer paths, not the workflow globs. All contents are harmless
	// canaries, and no helper, application process or upload action is launched.
	root := t.TempDir()
	wanted := make(map[string][]string)
	for _, scratchRoot := range []string{"dev/shm", "tmp"} {
		base := scratchRoot + "/evener-module-tests.seed"
		fixtures := []struct {
			path string
			job  string
		}{
			{"tmp/_/hub-test-shards.101/survey.log", "tests-parts"},
			{"tmp/agent/agent-test-shards.102/survey.log", "tests-parts"},
			{"json/shards/hub/hub-shard0.v", "tests-parts"},
			{"json/shards/hub/hub-shard11.v", "tests-parts"},
			{"json/shards/agent/agent-shard0.v", "tests-parts"},
			{"json/shards/agent/agent-shard11.v", "tests-parts"},
			{"tmp/_/hub-test-shards.103/shard0.log", "race-root-parts"},
			{"tmp/_/hub-test-shards.103/shard11.log", "race-root-parts"},
			{"agent.log", "race-module-shards"},
			{"tmp/_/home/.config/evener/credentials.toml", ""},
			{"tmp/agent/home/daemon.log", ""},
			{"tmp/_/hub-test-shards.101/hub.test", ""},
			{"tmp/_/hub-test-shards.101/build.log", ""},
			{"tmp/_/hub-test-shards.103/shard-credentials.log", ""},
			{"tmp/agent/agent-test-shards.102/agent.test", ""},
			{"tmp/llm/agent-test-shards.102/survey.log", ""},
			{"tmp/_/cli-test-shards.104/survey.log", ""},
			{"json/shards/hub/hub-shard-secret.v", ""},
			{"json/root.jsonl", ""},
			{"agent-shards.bin", ""},
			{"other.log", ""},
		}
		for _, fixture := range fixtures {
			path := filepath.Join(base, fixture.path)
			full := filepath.Join(root, path)
			if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte("diagnostic-selection-canary\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if fixture.job != "" {
				wanted[fixture.job] = append(wanted[fixture.job], path)
			}
		}
	}
	uploads := ciDiagnosticUploads(t)
	for _, job := range []string{"tests-parts", "race-root-parts", "race-module-shards"} {
		t.Run(job, func(t *testing.T) {
			step, ok := uploads[job]
			if !ok {
				t.Fatal("missing failure diagnostic upload")
			}
			selected := make(map[string]bool)
			// These allowlisted patterns use only single-component glob syntax
			// shared by filepath.Glob and upload-artifact, not recursive ** rules.
			for pattern := range strings.Lines(step.With.Path) {
				pattern = strings.TrimSpace(pattern)
				if pattern == "" {
					continue
				}
				if (!strings.HasPrefix(pattern, "/dev/shm/evener-module-tests.") && !strings.HasPrefix(pattern, "/tmp/evener-module-tests.")) || strings.Contains(pattern, "**") || strings.Contains(pattern, "..") {
					t.Fatalf("unsafe or unsupported diagnostic pattern %q", pattern)
				}
				matches, err := filepath.Glob(filepath.Join(root, strings.TrimPrefix(pattern, "/")))
				if err != nil {
					t.Fatal(err)
				}
				if len(matches) == 0 {
					t.Errorf("pattern selects no producer fixture: %s", pattern)
				}
				for _, match := range matches {
					info, err := os.Stat(match)
					if err != nil {
						t.Fatal(err)
					}
					if !info.Mode().IsRegular() {
						t.Fatalf("pattern selects a directory/non-file: %s", match)
					}
					rel, err := filepath.Rel(root, match)
					if err != nil {
						t.Fatal(err)
					}
					selected[rel] = true
				}
			}
			var got []string
			for path := range selected {
				got = append(got, path)
			}
			slices.Sort(got)
			slices.Sort(wanted[job])
			if !slices.Equal(got, wanted[job]) {
				t.Errorf("selected paths = %q\nwant only producer text logs = %q", got, wanted[job])
			}
		})
	}
}
