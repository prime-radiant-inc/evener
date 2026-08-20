package evener_test

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The release workflow's publishing jobs run only on a push to main or on a
// version tag. A pull request build reports them as "skipping", so CI green on
// a PR proves nothing about them, and a mistake there is discovered by a
// broken release. These two tests are the whole successor to the deleted
// workflow_test.go: they pin only the shape whose absence CI structurally
// cannot see.

// A job that shells out to git or gh needs the repository on disk: git refuses
// to run outside a work tree ("fatal: not in a git directory", exit 128) and
// gh resolves the target repo from the origin remote. Dropping the checkout
// from the snapshot job broke exactly this way and nothing said so.
func TestBinariesWorkflowJobsRunningGitOrGhCheckOutTheRepo(t *testing.T) {
	workflow := readWorkflow(t, ".github/workflows/binaries.yml")

	for name, job := range workflow.Jobs {
		needsRepo := ""
		for _, step := range job.Steps {
			if cmd := firstRepoCommand(step.Run); cmd != "" {
				needsRepo = cmd
				break
			}
		}
		if needsRepo == "" {
			continue
		}
		if !jobChecksOutRepo(job) {
			t.Errorf("job %q runs %q but has no actions/checkout step; git exits 128 and gh cannot resolve the repo without one", name, needsRepo)
		}
	}
}

// The snapshot channel is the default install path (install.sh's "snapshot"
// version), and it is republished by this job alone.
func TestBinariesWorkflowSnapshotJobRefreshesTheChannel(t *testing.T) {
	workflow := readWorkflow(t, ".github/workflows/binaries.yml")

	job, ok := workflow.Jobs["snapshot"]
	if !ok {
		t.Fatal("binaries workflow is missing the snapshot job")
	}
	if job.If != "github.ref == 'refs/heads/main'" {
		t.Errorf("snapshot job condition = %q, want the exact main-branch guard", job.If)
	}
	if !workflowNeeds(job.Needs, "build") {
		t.Errorf("snapshot job needs = %#v, want build (it publishes that job's artifacts)", job.Needs)
	}
	if job.Permissions["contents"] != "write" {
		t.Errorf("snapshot contents permission = %q, want write", job.Permissions["contents"])
	}
	if !jobChecksOutRepo(job) {
		t.Error("snapshot job has no actions/checkout step")
	}
	if !workflowUsesPrefix(job.Steps, "actions/download-artifact@") {
		t.Error("snapshot job does not download the build job's artifacts")
	}
	if !workflowRuns(job.Steps, "refs/tags/snapshot") {
		t.Error("snapshot job does not move the snapshot tag")
	}
	if !workflowRuns(job.Steps, `gh release upload "snapshot"`) {
		t.Error("snapshot job does not upload assets to the snapshot release")
	}
}

type githubActionsWorkflow struct {
	Jobs map[string]githubActionsJob `yaml:"jobs"`
}

type githubActionsJob struct {
	If          string             `yaml:"if"`
	Needs       any                `yaml:"needs"`
	Permissions map[string]string  `yaml:"permissions"`
	Steps       []githubActionStep `yaml:"steps"`
}

type githubActionStep struct {
	Name string            `yaml:"name"`
	Uses string            `yaml:"uses"`
	Env  map[string]string `yaml:"env"`
	Run  string            `yaml:"run"`
}

func readWorkflow(t *testing.T, path string) githubActionsWorkflow {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var workflow githubActionsWorkflow
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if len(workflow.Jobs) == 0 {
		t.Fatalf("%s declares no jobs", path)
	}
	return workflow
}

// firstRepoCommand returns the first git or gh invocation in a run script, or
// "" when it has none. Matching is per word so "gh release" is a hit and
// "github.ref" or "straight" are not.
func firstRepoCommand(run string) string {
	for line := range strings.Lines(run) {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		for _, field := range strings.Fields(trimmed) {
			if field == "git" || field == "gh" {
				return trimmed
			}
		}
	}
	return ""
}

func jobChecksOutRepo(job githubActionsJob) bool {
	return workflowUsesPrefix(job.Steps, "actions/checkout@")
}

func workflowNeeds(needs any, want string) bool {
	switch values := needs.(type) {
	case string:
		return values == want
	case []any:
		for _, value := range values {
			if value == want {
				return true
			}
		}
	}
	return false
}

func workflowUsesPrefix(steps []githubActionStep, prefix string) bool {
	for _, step := range steps {
		if strings.HasPrefix(step.Uses, prefix) {
			return true
		}
	}
	return false
}

func workflowRuns(steps []githubActionStep, want string) bool {
	for _, step := range steps {
		if strings.Contains(step.Run, want) {
			return true
		}
	}
	return false
}
