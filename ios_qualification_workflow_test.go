package evener_test

import (
	"fmt"
	"strings"
	"testing"
)

// A pull_request run takes the qualification workflow from the merge ref, so
// checking out the PR head alone runs new steps against a tree that may lack
// the scripts they call (#5993). These tests pin the same-tree checkout: the
// merge ref with full history, the selector that falls back to it when the
// head predates the harness, and the switch to the chosen commit.
func TestIOSQualificationChecksOutTheWorkflowTree(t *testing.T) {
	workflow := readWorkflow(t, ".github/workflows/ios-qualification.yml")
	job, ok := workflow.Jobs["simulator-build"]
	if !ok {
		t.Fatal("ios-qualification is missing the simulator-build job")
	}
	var checkout *githubActionStep
	for i := range job.Steps {
		if strings.HasPrefix(job.Steps[i].Uses, "actions/checkout@") {
			checkout = &job.Steps[i]
			break
		}
	}
	if checkout == nil {
		t.Fatal("simulator-build does not check out the repository")
	}
	if got := fmt.Sprint(checkout.With["ref"]); got != "${{ github.sha }}" {
		t.Errorf("checkout ref = %q, want the merge ref ${{ github.sha }}", got)
	}
	if got := fmt.Sprint(checkout.With["fetch-depth"]); got != "0" {
		t.Errorf("checkout fetch-depth = %q, want 0 so the PR head is in history", got)
	}
	if !workflowRuns(job.Steps, "scripts/native/ios/qualification/checkout-ref.sh") {
		t.Error("simulator-build does not run the checkout selector")
	}
	chosen := false
	for _, step := range job.Steps {
		if strings.Contains(step.Run, `git checkout --force "$CHOSEN_REF"`) {
			chosen = true
			if got := step.Env["CHOSEN_REF"]; got != "${{ steps.tree.outputs.ref }}" {
				t.Errorf("chosen-tree CHOSEN_REF = %q, want the selector's ref output", got)
			}
		}
	}
	if !chosen {
		t.Error("simulator-build does not check out the selector's chosen tree")
	}
}
