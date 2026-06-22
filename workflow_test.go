package serf_test

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestBinariesWorkflowPublishesSnapshotFromMain(t *testing.T) {
	data, err := os.ReadFile(".github/workflows/binaries.yml")
	if err != nil {
		t.Fatalf("read binaries workflow: %v", err)
	}

	var workflow githubActionsWorkflow
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse binaries workflow: %v", err)
	}

	job, ok := workflow.Jobs["snapshot"]
	if !ok {
		t.Fatal("binaries workflow is missing snapshot job")
	}
	if !strings.Contains(job.If, "refs/heads/main") {
		t.Fatalf("snapshot job condition = %q, want main branch only", job.If)
	}
	if !workflowNeeds(job.Needs, "build") {
		t.Fatalf("snapshot job needs = %#v, want build", job.Needs)
	}
	if job.Permissions["contents"] != "write" {
		t.Fatalf("snapshot contents permission = %q, want write", job.Permissions["contents"])
	}
	if !workflowUses(job.Steps, "actions/download-artifact@v4") {
		t.Fatal("snapshot job does not download build artifacts")
	}
	if !workflowRuns(job.Steps, "refs/tags/snapshot") {
		t.Fatal("snapshot job does not move the snapshot tag")
	}
	if !workflowRuns(job.Steps, `gh release upload "snapshot"`) {
		t.Fatal("snapshot job does not upload assets to the snapshot release")
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
	Uses string `yaml:"uses"`
	Run  string `yaml:"run"`
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

func workflowUses(steps []githubActionStep, want string) bool {
	for _, step := range steps {
		if step.Uses == want {
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
