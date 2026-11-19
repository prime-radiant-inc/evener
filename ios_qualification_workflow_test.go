package evener_test

import (
	"fmt"
	"slices"
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

// The lane splits the cold Release build from the installed-app journeys so
// the build signal lands without waiting for the UI run. The build job owns
// the test products the journeys need; the journeys job needs that job and
// restores the artifact it uploads.
func TestIOSQualificationSplitsBuildFromJourneys(t *testing.T) {
	workflow := readWorkflow(t, ".github/workflows/ios-qualification.yml")
	build, ok := workflow.Jobs["simulator-build"]
	if !ok {
		t.Fatal("ios-qualification is missing the simulator-build job")
	}
	journeys, ok := workflow.Jobs["ui-journeys"]
	if !ok {
		t.Fatal("ios-qualification is missing the ui-journeys job")
	}
	if !workflowNeeds(journeys.Needs, "simulator-build") {
		t.Fatal("ui-journeys does not need simulator-build")
	}
	if !workflowRuns(build.Steps, "build-simulator.sh") {
		t.Error("simulator-build does not run the build runner")
	}
	if workflowRuns(build.Steps, "run-ui-journeys.sh --products") {
		t.Error("simulator-build still runs the installed-app journeys")
	}
	if workflowRuns(build.Steps, "--with-ui") {
		t.Error("simulator-build still asks the build runner for the journeys")
	}
	if !workflowRuns(journeys.Steps, "run-ui-journeys.sh --products") {
		t.Error("ui-journeys does not run the journeys entry")
	}
	upload, download := false, false
	for _, step := range build.Steps {
		if strings.HasPrefix(step.Uses, "actions/upload-artifact@") && strings.Contains(fmt.Sprint(step.With["name"]), "ios-build-products-") {
			upload = true
			if got := fmt.Sprint(step.With["if-no-files-found"]); got != "error" {
				t.Errorf("products artifact if-no-files-found = %q, want error", got)
			}
		}
	}
	for _, step := range journeys.Steps {
		if strings.HasPrefix(step.Uses, "actions/download-artifact@") && strings.Contains(fmt.Sprint(step.With["name"]), "ios-build-products-") {
			download = true
		}
	}
	if !upload || !download {
		t.Errorf("products artifact handoff missing: upload=%v download=%v", upload, download)
	}
}

// A push to main runs the lane so simulator-build can save the DerivedData
// build tree every run restores. It must take the same changes as a pull
// request, or main would save a generation for a tree the PR lane never builds.
func TestIOSQualificationSavesOnMainWithThePullRequestPaths(t *testing.T) {
	workflow := readWorkflow(t, ".github/workflows/ios-qualification.yml")
	if !slices.Contains(workflow.On.Push.Branches, "main") {
		t.Fatalf("push branches = %v, want main", workflow.On.Push.Branches)
	}
	if len(workflow.On.Push.Paths) == 0 {
		t.Fatal("push trigger has no paths filter")
	}
	if !slices.Equal(workflow.On.Push.Paths, workflow.On.PullRequest.Paths) {
		t.Errorf("push paths differ from pull_request paths:\npush:  %v\nPR:    %v", workflow.On.Push.Paths, workflow.On.PullRequest.Paths)
	}
	// The main-push run is the one that saves and the post-merge verification;
	// a newer push must queue behind it, not cancel it.
	if !strings.Contains(workflow.Concurrency.CancelInProgress, "github.event_name == 'pull_request'") {
		t.Errorf("cancel-in-progress = %q, want pull requests only", workflow.Concurrency.CancelInProgress)
	}
}

// The build job restores before xcodebuild and saves after the products leave
// the job. The key hashes both dependency locks and the Xcode version, so a
// change to either lock misses instead of restoring a tree built against
// different pods. Only a push to main may save: a pull request's save is
// readable only by its ref, so it would crowd main's entry out without serving
// the next pull request.
func TestIOSQualificationRestoresAndSavesTheDerivedDataBuild(t *testing.T) {
	workflow := readWorkflow(t, ".github/workflows/ios-qualification.yml")
	job, ok := workflow.Jobs["simulator-build"]
	if !ok {
		t.Fatal("ios-qualification is missing the simulator-build job")
	}
	key, restore, save := -1, -1, -1
	build, products, mark := -1, -1, -1
	for i, step := range job.Steps {
		switch {
		case step.ID == "deriveddata-cache":
			key = i
		case step.ID == "deriveddata-restore":
			restore = i
		case step.Uses == "actions/cache/save@v6":
			save = i
		case strings.Contains(step.Run, "build-simulator.sh"):
			build = i
		case strings.Contains(step.Run, "tar -czf") && strings.Contains(step.Run, "Build/Products"):
			products = i
		case strings.Contains(step.Run, "IOS_DERIVED_DATA_READY=true"):
			mark = i
		}
	}
	for name, idx := range map[string]int{
		"cache-key": key, "restore": restore, "save": save,
		"build": build, "products": products, "save-ready": mark,
	} {
		if idx == -1 {
			t.Fatalf("simulator-build has no %s step", name)
		}
	}
	if !(key < restore && restore < build && build < products && products < mark && mark < save) {
		t.Fatalf("cache steps out of order: key=%d restore=%d build=%d products=%d save-ready=%d save=%d",
			key, restore, build, products, mark, save)
	}

	keyStep := job.Steps[key]
	for env, want := range map[string]string{
		"POD_LOCK_HASH":     "${{ hashFiles('mobile-native/Podfile.lock') }}",
		"PACKAGE_LOCK_HASH": "${{ hashFiles('mobile-native/package-lock.json') }}",
	} {
		if got := keyStep.Env[env]; got != want {
			t.Errorf("cache key %s = %q, want %q", env, got, want)
		}
	}
	for _, want := range []string{"ios-deriveddata-", xcodeKeyFragment, podLockKeyFragment, packageLockKeyFragment} {
		if !strings.Contains(keyStep.Run, want) {
			t.Errorf("cache key step lacks %q:\n%s", want, keyStep.Run)
		}
	}
	if got := workflow.Env["XCODE_VERSION"]; got != "26.6" {
		t.Errorf("XCODE_VERSION = %q, want the pinned 26.6", got)
	}

	restoreStep := job.Steps[restore]
	if restoreStep.If != "" {
		t.Errorf("restore if = %q, want no condition: every run restores", restoreStep.If)
	}
	wantKey := "${{ steps.deriveddata-cache.outputs.prefix }}${{ github.sha }}"
	wantPrefix := "${{ steps.deriveddata-cache.outputs.prefix }}"
	if got := fmt.Sprint(restoreStep.With["key"]); got != wantKey {
		t.Errorf("restore key = %q, want %q", got, wantKey)
	}
	if got := fmt.Sprint(restoreStep.With["restore-keys"]); got != wantPrefix {
		t.Errorf("restore-keys = %q, want %q", got, wantPrefix)
	}
	wantPath := "${{ env.IOS_DERIVED_DATA }}/Build"
	if got := fmt.Sprint(restoreStep.With["path"]); got != wantPath {
		t.Errorf("restore path = %q, want %q", got, wantPath)
	}

	saveStep := job.Steps[save]
	if got := fmt.Sprint(saveStep.With["key"]); got != wantKey {
		t.Errorf("save key = %q, want %q", got, wantKey)
	}
	if got := fmt.Sprint(saveStep.With["path"]); got != wantPath {
		t.Errorf("save path = %q, want %q, the tree the run restored", got, wantPath)
	}
	for _, want := range []string{
		"!cancelled()",
		"github.event_name == 'push'",
		"github.ref == 'refs/heads/main'",
		"steps.deriveddata-restore.outputs.cache-hit != 'true'",
		"env.IOS_DERIVED_DATA_READY == 'true'",
	} {
		if !strings.Contains(saveStep.If, want) {
			t.Errorf("save if %q lacks %q", saveStep.If, want)
		}
	}
	if !workflowRuns(job.Steps, "IOS_DERIVED_DATA=%s/ios-deriveddata") {
		t.Error("simulator-build does not set the stable IOS_DERIVED_DATA path")
	}
	// The handoff is the exported variable, not a flag: a pull-request head
	// from before the flag exists must still parse its own build script.
	if strings.Contains(job.Steps[build].Run, "--derived-data") {
		t.Errorf("build step passes --derived-data, which an older PR head's script rejects:\n%s", job.Steps[build].Run)
	}
	if !strings.Contains(job.Steps[products].Run, `derived="$IOS_DERIVED_DATA"`) ||
		!strings.Contains(job.Steps[products].Run, `-C "$derived" Build/Products`) {
		t.Errorf("products step does not pack the restored build tree with the pre-cache fallback:\n%s", job.Steps[products].Run)
	}
}

const (
	xcodeKeyFragment       = "xcode-${XCODE_VERSION}"
	podLockKeyFragment     = "${POD_LOCK_HASH}"
	packageLockKeyFragment = "${PACKAGE_LOCK_HASH}"
)
