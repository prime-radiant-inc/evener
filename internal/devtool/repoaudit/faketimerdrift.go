package repoaudit

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

// fakeTimerTrees are the TypeScript trees whose tests run on Vitest's fake
// timers.
var fakeTimerTrees = []string{"cmd/evener-hub/frontend/src", "appwire-client/typescript", "mobile-native/src"}

// fakeTimerDriftOption is the Vitest fake-timer option that lets the fake
// clock follow wall time.
const fakeTimerDriftOption = "shouldAdvanceTime"

// fakeTimersDoNotFollowWallTime keeps every fake clock in the TypeScript
// trees under the test's control. With shouldAdvanceTime the fake clock also
// advances with real elapsed time, so a debounce, poll or countdown fires
// sooner or later depending on how loaded the host is, and the test's outcome
// does too. A test drives the clock itself (vi.advanceTimersByTimeAsync and
// friends); the frontend's useTestDrivenFakeTimers keeps Testing Library's
// async utilities working on a clock that only the test moves.
func fakeTimersDoNotFollowWallTime(a *audit) {
	hits, err := fakeTimerDriftHits(".", fakeTimerTrees)
	if err != nil {
		a.Fatalf("search for %s: %v", fakeTimerDriftOption, err)
	}
	for _, hit := range hits {
		a.Errorf("%s uses %s; drive the fake clock from the test instead (vi.advanceTimersByTimeAsync, vi.runOnlyPendingTimersAsync)", hit, fakeTimerDriftOption)
	}
}

// fakeTimerDriftHits lists each tracked line under trees, in the repository
// at root, that names the drift option, as path:line.
func fakeTimerDriftHits(root string, trees []string) ([]string, error) {
	args := append([]string{"-C", root, "grep", "-n", "-I", "--fixed-strings", "-e", fakeTimerDriftOption, "--"}, trees...)
	out, err := exec.CommandContext(context.Background(), "git", args...).Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var hits []string
	for line := range strings.Lines(string(out)) {
		path, rest, _ := strings.Cut(strings.TrimSpace(line), ":")
		lineNo, _, _ := strings.Cut(rest, ":")
		hits = append(hits, path+":"+lineNo)
	}
	return hits, nil
}
