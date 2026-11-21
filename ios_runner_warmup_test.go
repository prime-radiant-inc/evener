package evener_test

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

const (
	uiRunnerScript = "scripts/native/ios/run-ui-tests.sh"
	// uiRunnerXcodebuildRun starts the measured journeys; the warmup steps
	// above it must complete first, and the diagnostics below it describe
	// that run.
	uiRunnerXcodebuildRun = `run_xcodebuild "$output/xcodebuild-test.log"`
)

// runnerScriptText reads the UI qualification runner for the script-text
// audits below. They hold the script's sequence, not its wording.
func runnerScriptText(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(uiRunnerScript)
	if err != nil {
		t.Fatalf("reading %s: %v", uiRunnerScript, err)
	}
	return string(data)
}

// The qualification journeys pay issue #5992's cold-device cost whenever the
// first journey starts on a simulator still settling from its first boot:
// first launches of 19-74s against 1-3s relaunches in the same run, 3-10x
// slow UI queries, and the only observed first-journey failure inside that
// window. run-ui-tests.sh therefore boots the disposable device, waits for
// the boot to finish, installs the app under test and gives it one launch
// before the measured xcodebuild run starts.
//
// The order is the mechanism. A boot placed after the test run, an install
// without the boot, or a warm app instance left running into the measured
// journeys all leave the script looking warm while the first journey still
// pays the cold-device cost. This test holds the sequence and the settle
// duration, not the wording: every step below must appear, and in this order,
// before the xcodebuild invocation.
func TestRunnerWarmsTheSimulatorBeforeTheJourneys(t *testing.T) {
	text := runnerScriptText(t)

	steps := []string{
		"xcrun simctl boot ",
		"xcrun simctl bootstatus",
		"xcrun simctl install",
		"xcrun simctl launch",
		"sleep 15",
		"xcrun simctl terminate",
		uiRunnerXcodebuildRun,
	}
	at := make([]int, len(steps))
	for i, step := range steps {
		at[i] = strings.Index(text, step)
		if at[i] < 0 {
			t.Fatalf("%s does not carry %q, so the cold-device warmup is incomplete", uiRunnerScript, step)
		}
	}
	for i := 1; i < len(at); i++ {
		if at[i] <= at[i-1] {
			t.Fatalf("%s runs %q before %q; the warmup must boot, wait, install, launch and terminate before the measured journeys",
				uiRunnerScript, steps[i], steps[i-1])
		}
	}
}

// The diagnostic half of issue #5992 is the device's own record of the run:
// the uploaded ui/ set must carry the simulator's unified log for the window
// the measured journeys ran in, so a slow first launch can be compared with a
// normal one instead of inferred from XCTest's activity lines alone. The
// capture must cover that window, not a fixed interval before it, and must
// not be able to fail an otherwise green run.
func TestRunnerCapturesTheSimulatorSystemLogForTheMeasuredRun(t *testing.T) {
	text := runnerScriptText(t)

	runAt := strings.Index(text, uiRunnerXcodebuildRun)
	if runAt < 0 {
		t.Fatalf("%s no longer runs xcodebuild where the measured journeys start", uiRunnerScript)
	}
	capture := `xcrun simctl spawn "$sim_udid" log show`
	captureAt := strings.Index(text, capture)
	if captureAt < 0 {
		t.Fatalf("%s does not capture the simulator's system log, so issue #5992's window has no device record", uiRunnerScript)
	}
	if captureAt < runAt {
		t.Fatalf("%s reads the system log before the measured journeys run", uiRunnerScript)
	}
	startAt := strings.Index(text, `run_start="$(date`)
	if startAt < 0 || startAt > runAt {
		t.Fatalf("%s does not record when the measured run started before running it", uiRunnerScript)
	}
	window := text[captureAt:]
	for _, want := range []string{
		`>"$output/simulator-system.log"`,
		`--start "$run_start"`,
		`--end "$(date`,
		`simulator system log capture failed, see`,
	} {
		if !strings.Contains(window, want) {
			t.Fatalf("%s does not capture the run window with %s", uiRunnerScript, want)
		}
	}

	// The capture only helps if its file is uploaded with the rest of the ui
	// diagnostics; the workflow's glob, not the script, decides that.
	workflow := readWorkflow(t, ".github/workflows/ios-qualification.yml")
	journeys, ok := workflow.Jobs["ui-journeys"]
	if !ok {
		t.Fatal("ios-qualification is missing the ui-journeys job")
	}
	var upload *githubActionStep
	for i := range journeys.Steps {
		if strings.HasPrefix(journeys.Steps[i].Uses, "actions/upload-artifact@") {
			upload = &journeys.Steps[i]
			break
		}
	}
	if upload == nil {
		t.Fatal("ui-journeys does not upload its journey diagnostics")
	}
	if paths := fmt.Sprint(upload.With["path"]); !strings.Contains(paths, `${{ env.JOURNEY_OUTPUT }}/ui/*.log`) {
		t.Fatalf("the journey diagnostics upload does not include the ui/ logs, so %s never reaches the artifact", `${{ env.JOURNEY_OUTPUT }}/ui/*.log`)
	}
}
