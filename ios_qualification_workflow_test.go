package evener_test

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// A pull_request run takes the qualification workflow from the merge ref, so
// checking out the PR head alone runs new steps against a tree that may lack
// the scripts they call (#5993). These tests pin the same-tree checkout: the
// merge ref fetched deep enough for both of its parents and for the snapshot
// window the lane budgets should a session open at this checkout, the selector
// that falls back to it when the head predates the harness, and the switch to
// the chosen commit.
func TestIOSQualificationChecksOutTheWorkflowTree(t *testing.T) {
	workflow := readWorkflow(t, ".github/workflows/ios-qualification.yml")
	job, ok := workflow.Jobs["simulator-build"]
	if !ok {
		t.Fatal("ios-qualification is missing the simulator-build job")
	}
	checkout := checkoutStep(t, "simulator-build", job.Steps)
	if got := fmt.Sprint(checkout.With["ref"]); got != "${{ github.sha }}" {
		t.Errorf("checkout ref = %q, want the merge ref ${{ github.sha }}", got)
	}
	checkHistoryFloor(t, "simulator-build", checkout)
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

	// Every other job that checks the repository out runs the same selector and
	// needs its own floor: journey-setup builds the runtime pair from its tree,
	// and ui-journeys tests it.
	for _, name := range []string{"journey-setup", "ui-journeys"} {
		other, ok := workflow.Jobs[name]
		if !ok {
			t.Fatalf("ios-qualification is missing the %s job", name)
		}
		checkHistoryFloor(t, name, checkoutStep(t, name, other.Steps))
	}
}

// checkoutStep returns the checkout action step in a job, failing the test when
// the job does not check the repository out.
func checkoutStep(t *testing.T, job string, steps []githubActionStep) *githubActionStep {
	t.Helper()
	for i := range steps {
		if strings.HasPrefix(steps[i].Uses, "actions/checkout@") {
			return &steps[i]
		}
	}
	t.Fatalf("%s does not check out the repository", job)
	return nil
}

// snapshotHistoryDepth is the launch git snapshot's title count:
// agent/git_snapshot.go runs `git log -n 5`.
const snapshotHistoryDepth = 5

// checkHistoryFloor requires the depth the lane budgets: five ancestry levels
// from the merge ref, which resolves the checkout selector's two parents (depth
// >= 2). The launch git snapshot reads `git log -n 5`
// (agent/git_snapshot.go); switching to the PR head can truncate it (four
// titles for a linear head, more when the head is itself a merge), and today's
// journeys open their session in a TMPDIR workspace outside this checkout, so
// the selector is the only live consumer. Depth 1 leaves the selector nothing
// to check; depths 0 and 5 or more pass.
func checkHistoryFloor(t *testing.T, job string, checkout *githubActionStep) {
	t.Helper()
	depth := fmt.Sprint(checkout.With["fetch-depth"])
	if depth == "0" {
		return
	}
	n, err := strconv.Atoi(depth)
	if err != nil || n < snapshotHistoryDepth {
		t.Errorf("%s checkout fetch-depth = %q, want 0 or at least %d: both merge parents for the selector, and the launch snapshot's %d-title window", job, depth, snapshotHistoryDepth, snapshotHistoryDepth)
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
	// a newer push must run instead of replacing a pending shared-group run.
	if !strings.Contains(workflow.Concurrency.CancelInProgress, "github.event_name == 'pull_request'") {
		t.Errorf("cancel-in-progress = %q, want pull requests only", workflow.Concurrency.CancelInProgress)
	}
	if !strings.Contains(workflow.Concurrency.Group, "github.run_id") {
		t.Errorf("concurrency group = %q, want a per-run group outside pull requests", workflow.Concurrency.Group)
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
	if key >= restore || restore >= build || build >= products || products >= mark || mark >= save {
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
	for _, want := range []string{"ios-deriveddata-", "xcode-${XCODE_VERSION}", "${POD_LOCK_HASH}", "${PACKAGE_LOCK_HASH}"} {
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
	// A restored tree alone must not save: the mark step needs the release
	// build's log, which exists only once xcodebuild started.
	if !strings.Contains(job.Steps[mark].Run, "xcodebuild-release.log") {
		t.Errorf("save-ready step does not require the release build to have started:\n%s", job.Steps[mark].Run)
	}
	// The handoff is the exported variable, not a command argument: a
	// pull-request head from before the cache change must still parse its own
	// build script.
	if strings.Contains(job.Steps[build].Run, "--derived-data") {
		t.Errorf("build step passes --derived-data, which an older PR head's script rejects:\n%s", job.Steps[build].Run)
	}
	packaging := job.Steps[products].Run
	runOwned := strings.Index(packaging, `derived="$QUALIFICATION_OUTPUT/DerivedData"`)
	external := strings.Index(packaging, `derived="$IOS_DERIVED_DATA"`)
	if runOwned == -1 || external == -1 || runOwned > external ||
		!strings.Contains(packaging, `-C "$derived" Build/Products`) {
		t.Errorf("products step does not prefer the run-owned tree and fall back to the external root:\n%s", packaging)
	}
}

// journey-setup overlaps the journeys job's pre-test runtime build with the
// app build: it has no needs, so it starts beside simulator-build, and
// ui-journeys waits for both. The pair is usable only when it was built from
// the tree the journeys run against, so the setup job selects its tree with
// the same selector and records the commit and checksums it built for
// run-ui-tests.sh to verify before running either binary.
func TestIOSQualificationOverlapsTheRuntimeSetupWithTheBuild(t *testing.T) {
	workflow := readWorkflow(t, ".github/workflows/ios-qualification.yml")
	setup, ok := workflow.Jobs["journey-setup"]
	if !ok {
		t.Fatal("ios-qualification is missing the journey-setup job")
	}
	if setup.Needs != nil {
		t.Errorf("journey-setup needs %v, want no needs so it overlaps the build", setup.Needs)
	}
	if !workflowRuns(setup.Steps, "make build-runtime") {
		t.Error("journey-setup does not build the runtime pair")
	}
	if !workflowRuns(setup.Steps, `git rev-parse HEAD > "$RUNTIME_PAIR_OUTPUT/revision"`) {
		t.Error("journey-setup does not record the commit it built")
	}
	packaged := false
	for _, step := range setup.Steps {
		if !strings.Contains(step.Run, "$RUNTIME_PAIR_OUTPUT/evener") {
			continue
		}
		packaged = true
		for _, want := range []string{"$RUNTIME_PAIR_OUTPUT/fixture-provider", "$RUNTIME_PAIR_OUTPUT/revision", "SHA256SUMS", "ios-runtime-pair.tar.gz"} {
			if !strings.Contains(step.Run, want) {
				t.Errorf("runtime pair packaging lacks %q:\n%s", want, step.Run)
			}
		}
	}
	if !packaged {
		t.Error("journey-setup does not build the two runtime binaries")
	}

	// The recipe invokes no Xcode tool, but the Go binaries compile cgo, so
	// their clang must come from the same Xcode the journeys job selects.
	var upload *githubActionStep
	for i := range setup.Steps {
		if strings.HasPrefix(setup.Steps[i].Uses, "actions/upload-artifact@") {
			upload = &setup.Steps[i]
			break
		}
	}
	if upload == nil {
		t.Fatal("journey-setup does not upload the runtime pair")
	}
	if got := fmt.Sprint(upload.With["name"]); !strings.HasPrefix(got, "ios-runtime-pair-") {
		t.Errorf("runtime pair artifact name = %q, want a per-run ios-runtime-pair- name", got)
	}
	if name := fmt.Sprint(upload.With["name"]); strings.Contains(name, "github.run_attempt") {
		t.Errorf("runtime pair artifact name = %q, want a run-scoped name a failed-jobs re-run can still find", name)
	}
	if got := fmt.Sprint(upload.With["if-no-files-found"]); got != "error" {
		t.Errorf("runtime pair if-no-files-found = %q, want error: a missing pair must fail the job", got)
	}
	if got := fmt.Sprint(upload.With["overwrite"]); got != "true" {
		t.Errorf("runtime pair overwrite = %q, want true so a re-run replaces the earlier attempt's pair", got)
	}

	build, ok := workflow.Jobs["simulator-build"]
	if !ok {
		t.Fatal("ios-qualification is missing the simulator-build job")
	}
	var products *githubActionStep
	for i := range build.Steps {
		if strings.HasPrefix(build.Steps[i].Uses, "actions/upload-artifact@") {
			products = &build.Steps[i]
			break
		}
	}
	if products == nil {
		t.Fatal("simulator-build does not upload the build products")
	}
	if name := fmt.Sprint(products.With["name"]); strings.Contains(name, "github.run_attempt") {
		t.Errorf("products artifact name = %q, want a run-scoped name the journeys job can find after a rerun", name)
	}
	if got := fmt.Sprint(products.With["overwrite"]); got != "true" {
		t.Errorf("products overwrite = %q, want true so a re-run replaces the earlier attempt's products", got)
	}
	journeys, ok := workflow.Jobs["ui-journeys"]
	if !ok {
		t.Fatal("ios-qualification is missing the ui-journeys job")
	}
	for _, want := range []string{"simulator-build", "journey-setup"} {
		if !workflowNeeds(journeys.Needs, want) {
			t.Errorf("ui-journeys needs %v, want it to wait for %s", journeys.Needs, want)
		}
	}
	// A head whose run-ui-tests.sh predates the handoff builds its own pair in
	// this job, which embeds the frontend: the toolchain pin must survive the
	// move of the pair build to journey-setup.
	nodePinned := false
	for _, step := range journeys.Steps {
		if strings.HasPrefix(step.Uses, "actions/setup-node@") && fmt.Sprint(step.With["node-version"]) == "22.13.1" {
			nodePinned = true
		}
	}
	if !nodePinned {
		t.Error("ui-journeys does not pin Node 22.13.1, which the harness that builds its own pair needs")
	}

	// runStep normalizes the command of the first step that contains needle,
	// so two jobs' copies of one recipe compare verbatim.
	runStep := func(job githubActionsJob, needle string) string {
		for _, step := range job.Steps {
			if strings.Contains(step.Run, needle) {
				return strings.Join(strings.Fields(step.Run), " ")
			}
		}
		return ""
	}
	// Same inputs must select the same tree in both jobs; the revision guard
	// covers whatever the selector still cannot pin.
	first := journeys.Steps[0]
	for _, want := range []string{"needs.simulator-build.result", "needs.journey-setup.result"} {
		if !strings.Contains(first.If, want) {
			t.Errorf("first journeys step if = %q, want it to test %s", first.If, want)
		}
	}
	if !strings.Contains(first.Run, "exit 1") {
		t.Errorf("first journeys step does not fail the job on a failed prerequisite:\n%s", first.Run)
	}
	selector := "scripts/native/ios/qualification/checkout-ref.sh"
	if got, want := runStep(setup, selector), runStep(journeys, selector); got == "" || got != want {
		t.Errorf("journey-setup selects its tree with %q, want ui-journeys' %q", got, want)
	}
	const xcode = "xcode-select --switch"
	if got, want := runStep(setup, xcode), runStep(build, xcode); got == "" || got != want {
		t.Errorf("journey-setup selects Xcode with %q, want the build job's %q", got, want)
	}

	downloaded := map[string]bool{}
	var ran bool
	for _, step := range journeys.Steps {
		switch {
		case strings.HasPrefix(step.Uses, "actions/download-artifact@"):
			downloaded[fmt.Sprint(step.With["name"])] = true
		case strings.Contains(step.Run, "run-ui-journeys.sh --products"):
			ran = true
			if got := step.Env["EVENER_QUALIFICATION_RUNTIME_BINARIES"]; got != "${{ env.JOURNEY_RUNTIME_PAIR }}" {
				t.Errorf("journey run passes EVENER_QUALIFICATION_RUNTIME_BINARIES = %q, want the downloaded pair's path", got)
			}
		}
	}
	for _, want := range []string{"ios-build-products-", "ios-runtime-pair-"} {
		found := false
		for name := range downloaded {
			if !strings.HasPrefix(name, want) {
				continue
			}
			found = true
			if strings.Contains(name, "github.run_attempt") {
				t.Errorf("download name = %q, want the run-scoped name its producer uploads", name)
			}
		}
		if !found {
			t.Errorf("ui-journeys does not download the %s artifact: %v", want, downloaded)
		}
	}
	// The prerequisite gate can fail before the output path exists, so the
	// always-on diagnostics upload must not glob from the filesystem root.
	for _, step := range journeys.Steps {
		if strings.HasPrefix(step.Uses, "actions/upload-artifact@") && !strings.Contains(step.If, "env.JOURNEY_OUTPUT") {
			t.Errorf("diagnostics upload if = %q, want it gated on the output path existing", step.If)
		}
	}
	if !ran {
		t.Error("ui-journeys does not run the installed-app journeys")
	}
}
