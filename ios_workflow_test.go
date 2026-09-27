package evener_test

import (
	"reflect"
	"strings"
	"testing"
)

// TestFlight uploads are expensive and land in a real Apple distribution
// channel, so they ship only from tagged releases on main rather than on every
// commit that touches the mobile trees. A pull request reports this workflow as
// "skipping", so CI green on a PR proves nothing about these conditions, and
// the mutable `snapshot` tag that binaries.yml force-moves on every green main
// build must not be mistaken for a release. These tests pin the trigger shape
// whose absence the rest of CI structurally cannot see.

func TestIOSWorkflowTriggersOnlyOnVersionTags(t *testing.T) {
	workflow := readWorkflow(t, ".github/workflows/ios-testflight.yml")

	push := workflow.On.Push
	if !reflect.DeepEqual(push.Tags, []string{"v*"}) {
		t.Errorf("ios-testflight push tags = %#v, want exactly [\"v*\"] so the snapshot tag cannot trigger a build", push.Tags)
	}
	if len(push.Branches) != 0 {
		t.Errorf("ios-testflight push branches = %#v, want none (branch pushes must not build)", push.Branches)
	}
	if len(push.Paths) != 0 {
		t.Errorf("ios-testflight push paths = %#v, want none (a tag push ignores path filters)", push.Paths)
	}
}

func TestIOSWorkflowArchiveJobRunsForTagsAndMainDispatch(t *testing.T) {
	workflow := readWorkflow(t, ".github/workflows/ios-testflight.yml")

	job, ok := workflow.Jobs["archive-and-upload"]
	if !ok {
		t.Fatal("ios-testflight workflow is missing the archive-and-upload job")
	}
	if !strings.Contains(job.If, "startsWith(github.ref, 'refs/tags/v')") {
		t.Errorf("archive-and-upload condition does not allow version-tag pushes: %q", job.If)
	}
	if !strings.Contains(job.If, "github.ref == 'refs/heads/main'") {
		t.Errorf("archive-and-upload condition does not keep the main-only manual path: %q", job.If)
	}
	if !workflowRuns(job.Steps, "git fetch --no-tags origin main") {
		t.Error("archive-and-upload does not fetch main to verify the tag's ancestry")
	}
	if !workflowRuns(job.Steps, "git merge-base --is-ancestor") {
		t.Error("archive-and-upload does not verify the release tag is on main")
	}
}

func TestIOSWorkflowRejectsOnlyNonMainDispatches(t *testing.T) {
	workflow := readWorkflow(t, ".github/workflows/ios-testflight.yml")

	job, ok := workflow.Jobs["validate-dispatch"]
	if !ok {
		t.Fatal("ios-testflight workflow is missing the validate-dispatch job")
	}
	if !strings.Contains(job.If, "github.event_name == 'workflow_dispatch'") {
		t.Errorf("validate-dispatch condition must be scoped to workflow_dispatch so a tag push is not rejected: %q", job.If)
	}
}

func TestIOSWorkflowRecoveryJobIsDispatchOnly(t *testing.T) {
	workflow := readWorkflow(t, ".github/workflows/ios-testflight.yml")

	job, ok := workflow.Jobs["verify-upload"]
	if !ok {
		t.Fatal("ios-testflight workflow is missing the verify-upload job")
	}
	if !strings.Contains(job.If, "github.event_name == 'workflow_dispatch'") {
		t.Errorf("verify-upload condition must be scoped to workflow_dispatch so a tag push cannot enter the recovery lane: %q", job.If)
	}
	if !strings.Contains(job.If, "inputs.recovery_run_id != ''") {
		t.Errorf("verify-upload condition does not require a recovery run id: %q", job.If)
	}
}
