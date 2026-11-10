package repoaudit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// fakeTimerTrees are the TypeScript trees whose tests run on Vitest's fake
// timers. Each is scanned whole, so a Vitest config's fakeTimers defaults are
// held to the rule as well as each test's own call.
var fakeTimerTrees = []string{"cmd/evener-hub/frontend", "appwire-client/typescript", "mobile-native"}

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
	for _, tree := range trees {
		if _, err := os.Stat(filepath.Join(root, tree)); err != nil {
			return nil, fmt.Errorf("audited tree %s: %w", tree, err)
		}
	}
	args := append([]string{"-C", root, "grep", "-n", "-I", "--fixed-strings", "-e", fakeTimerDriftOption, "--"}, trees...)
	out, err := exec.CommandContext(context.Background(), "git", args...).Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return nil, nil
	}
	if exit != nil {
		return nil, fmt.Errorf("git grep: %w: %s", err, strings.TrimSpace(string(exit.Stderr)))
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
