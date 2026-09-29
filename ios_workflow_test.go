package evener_test

import (
	"reflect"
	"testing"
)

// TestFlight uploads are expensive and land in a real Apple distribution
// channel, so they ship only from version tags on main rather than on every
// commit that touches the mobile trees. A pull request reports this workflow as
// "skipping", so CI green on a PR proves nothing about these conditions, and
// no mutable ref — the `snapshot` channel binaries.yml refreshes on every
// green main build — may be mistaken for a release. These tests pin the
// trigger shape and the exact job guards, which the rest of CI structurally
// cannot see.

const (
	iosWorkflowPath = ".github/workflows/ios-testflight.yml"

	// A tag push archives; a manual dispatch archives only from main with no
	// recovery id. The tag clause must be gated on the push event: a
	// workflow_dispatch can name a `v...` tag as its ref, and without that guard
	// the archive lane would upload to Apple while validate-dispatch failed the
	// same run.
	iosArchiveGuard = "(github.event_name == 'push' && startsWith(github.ref, 'refs/tags/v')) || (github.event_name == 'workflow_dispatch' && inputs.recovery_run_id == '' && github.ref == 'refs/heads/main')"

	iosValidateDispatchGuard = "github.event_name == 'workflow_dispatch' && github.ref != 'refs/heads/main'"
	iosVerifyUploadGuard     = "github.event_name == 'workflow_dispatch' && inputs.recovery_run_id != '' && github.ref == 'refs/heads/main'"
)

func TestIOSWorkflowTriggersOnlyOnVersionTags(t *testing.T) {
	workflow := readWorkflow(t, iosWorkflowPath)

	push := workflow.On.Push
	if !reflect.DeepEqual(push.Tags, []string{"v[0-9]*"}) {
		t.Errorf("ios-testflight push tags = %#v, want exactly [\"v[0-9]*\"] so no non-version tag can build", push.Tags)
	}
	if len(push.Branches) != 0 {
		t.Errorf("ios-testflight push branches = %#v, want none (branch pushes must not build)", push.Branches)
	}
	if len(push.Paths) != 0 {
		t.Errorf("ios-testflight push paths = %#v, want none (a tag push ignores path filters)", push.Paths)
	}
}

func TestIOSWorkflowJobGuards(t *testing.T) {
	workflow := readWorkflow(t, iosWorkflowPath)

	for _, want := range []struct {
		job  string
		cond string
	}{
		{"archive-and-upload", iosArchiveGuard},
		{"validate-dispatch", iosValidateDispatchGuard},
		{"verify-upload", iosVerifyUploadGuard},
	} {
		job, ok := workflow.Jobs[want.job]
		if !ok {
			t.Fatalf("ios-testflight workflow is missing the %s job", want.job)
		}
		if job.If != want.cond {
			t.Errorf("%s condition = %q, want %q", want.job, job.If, want.cond)
		}
	}
}

func TestIOSWorkflowArchiveVerifiesTagOnMain(t *testing.T) {
	workflow := readWorkflow(t, iosWorkflowPath)

	job, ok := workflow.Jobs["archive-and-upload"]
	if !ok {
		t.Fatal("ios-testflight workflow is missing the archive-and-upload job")
	}
	if !workflowRuns(job.Steps, "git fetch --no-tags origin main") {
		t.Error("archive-and-upload does not fetch main to check the tag's ancestry")
	}
	if !workflowRuns(job.Steps, "git merge-base --is-ancestor") {
		t.Error("archive-and-upload does not check that the release tag is on main")
	}
	if !workflowRuns(job.Steps, "git fetch --no-tags --unshallow origin main") {
		t.Error("archive-and-upload does not deepen a shallow checkout before rejecting a tag")
	}
}

func TestIOSWorkflowRecoveryHandlesTagPushRuns(t *testing.T) {
	workflow := readWorkflow(t, iosWorkflowPath)

	job, ok := workflow.Jobs["verify-upload"]
	if !ok {
		t.Fatal("ios-testflight workflow is missing the verify-upload job")
	}
	if !workflowRuns(job.Steps, `run["event"] == "push"`) {
		t.Error("verify-upload cannot recover a tag-push run, which is now the primary upload lane")
	}
	if !workflowRuns(job.Steps, "head_branch.match?") {
		t.Error("verify-upload accepts any push run, including the pre-change main-branch lane")
	}
	if !workflowRuns(job.Steps, "git fetch --no-tags --unshallow origin main") {
		t.Error("verify-upload rejects a valid tag whose commit is beyond the shallow checkout boundary")
	}
	if !workflowRuns(job.Steps, "git merge-base --is-ancestor") {
		t.Error("verify-upload does not confirm a tag-push run's commit is on main")
	}
}
