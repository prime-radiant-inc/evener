package evener_test

import (
	"os"
	"strings"
	"testing"
)

// The qualification journeys pay issue #5992's cold-device cost whenever the
// first journey starts on a simulator still settling from its first boot:
// first launches of 22-74s against 1-3s relaunches in the same run, 3-10x
// slow UI queries, and the only observed first-journey failure inside that
// window. run-ui-tests.sh therefore boots the disposable device, waits for
// the boot to finish, installs the app under test and gives it one launch
// before the measured xcodebuild run starts.
//
// The order is the mechanism. A boot placed after the test run, an install
// without the boot, or a warm app instance left running into the measured
// journeys all leave the script looking warm while the first journey still
// pays the cold-device cost. This test holds the sequence, not the wording:
// every step below must appear, and in this order, before the xcodebuild
// invocation.
func TestRunnerWarmsTheSimulatorBeforeTheJourneys(t *testing.T) {
	const script = "scripts/native/ios/run-ui-tests.sh"
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatalf("reading %s: %v", script, err)
	}
	text := string(data)

	steps := []string{
		"xcrun simctl boot ",
		"xcrun simctl bootstatus",
		"xcrun simctl install",
		"xcrun simctl launch",
		"sleep 15",
		"xcrun simctl terminate",
		`run_xcodebuild "$output/xcodebuild-test.log"`,
	}
	at := make([]int, len(steps))
	for i, step := range steps {
		at[i] = strings.Index(text, step)
		if at[i] < 0 {
			t.Fatalf("%s does not carry %q, so the cold-device warmup is incomplete", script, step)
		}
	}
	for i := 1; i < len(at); i++ {
		if at[i] <= at[i-1] {
			t.Fatalf("%s runs %q before %q; the warmup must boot, wait, install, launch and terminate before the measured journeys",
				script, steps[i], steps[i-1])
		}
	}
}
