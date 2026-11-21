package evener_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
// capture must cover that window, not a fixed interval before it.
func TestRunnerCapturesTheSimulatorSystemLogForTheMeasuredRun(t *testing.T) {
	text := runnerScriptText(t)

	runAt := strings.Index(text, uiRunnerXcodebuildRun)
	if runAt < 0 {
		t.Fatalf("%s no longer runs xcodebuild where the measured journeys start", uiRunnerScript)
	}
	call := `capture_simulator_system_log "$output" "$sim_udid" "$run_start"`
	callAt := strings.Index(text, call)
	if callAt < 0 {
		t.Fatalf("%s does not capture the simulator's system log, so issue #5992's window has no device record", uiRunnerScript)
	}
	if callAt < runAt {
		t.Fatalf("%s reads the system log before the measured journeys run", uiRunnerScript)
	}
	startAt := strings.Index(text, `run_start="$(date`)
	if startAt < 0 || startAt > runAt {
		t.Fatalf("%s does not record when the measured run started before running it", uiRunnerScript)
	}
	body := strings.Index(text, "capture_simulator_system_log() {")
	if body < 0 {
		t.Fatalf("%s does not define the capture, so the run has no device record to write", uiRunnerScript)
	}
	for _, want := range []string{
		`>"$1/simulator-system.log"`,
		`--start "$3"`,
		`--end "$(date`,
	} {
		if !strings.Contains(text[body:], want) {
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

// TestRunnerCapturesTheSimulatorSystemLogAsBestEffort drives the capture with
// fixture xcrun and date binaries, so issue #5992's promise is behavior: a
// successful capture writes the device log into the uploaded set, and a failed
// one leaves the run's exit status alone while naming its error log.
func TestRunnerCapturesTheSimulatorSystemLogAsBestEffort(t *testing.T) {
	bin := t.TempDir()
	writeTestExecutable(t, filepath.Join(bin, "xcrun"), `#!/usr/bin/env bash
if [ "${CAPTURE_FAILS:-0}" = "1" ]; then
	echo "fixture simctl failure" >&2
	exit 1
fi
printf '%s\n' "fixture simulator log line"
`)
	writeTestExecutable(t, filepath.Join(bin, "date"), `#!/usr/bin/env bash
printf '%s\n' "2026-01-01 00:00:01"
`)
	runner, err := filepath.Abs(uiRunnerScript)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name               string
		captureFails       string
		wantLog, wantError bool
	}{
		{name: "success", captureFails: "0", wantLog: true},
		{name: "failure", captureFails: "1", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := t.TempDir()
			cmd := exec.Command("bash", "-c",
				`source "$1" || exit 9; capture_simulator_system_log "$2" "UDID" "2026-01-01 00:00:00"`,
				"bash", runner, out)
			cmd.Env = append(os.Environ(),
				"RUN_UI_TESTS_LIB_ONLY=1",
				"PATH="+bin+":"+os.Getenv("PATH"),
				"CAPTURE_FAILS="+tc.captureFails,
			)
			var stderr strings.Builder
			cmd.Stderr = &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("a failed capture must not fail the run: %v\n%s", err, stderr.String())
			}
			logText := readTestFile(t, filepath.Join(out, "simulator-system.log"))
			errText := readTestFile(t, filepath.Join(out, "simulator-system-capture.log"))
			if tc.wantLog && !strings.Contains(logText, "fixture simulator log line") {
				t.Fatalf("simulator-system.log = %q, want the captured device log line", logText)
			}
			if !tc.wantLog && strings.TrimSpace(logText) != "" {
				t.Fatalf("simulator-system.log = %q, want it empty when the capture fails", logText)
			}
			if tc.wantError && !strings.Contains(errText, "fixture simctl failure") {
				t.Fatalf("simulator-system-capture.log = %q, want the capture error", errText)
			}
			if !tc.wantError && strings.TrimSpace(errText) != "" {
				t.Fatalf("simulator-system-capture.log = %q, want it empty when the capture succeeds", errText)
			}
		})
	}
}

func writeTestExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}
