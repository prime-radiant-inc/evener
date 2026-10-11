package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// TestFatalLaunchPluginError: an inventory that could not be built completely
// stops a launch only when there is a selection to honour or the caller left.
// An explicit empty selection (--enabled-plugins "") selects nothing, so a
// broken plugin store must not stop it, the same as no selection at all.
func TestFatalLaunchPluginError(t *testing.T) {
	t.Parallel()
	broken := errors.New("plugin store unreadable")
	empty := []string{}
	named := []string{"superpowers"}
	for _, test := range []struct {
		name      string
		err       error
		selection *[]string
		wantFatal bool
	}{
		{name: "no error", err: nil, selection: &named, wantFatal: false},
		{name: "no selection", err: broken, selection: nil, wantFatal: false},
		{name: "empty selection", err: broken, selection: &empty, wantFatal: false},
		{name: "named selection", err: broken, selection: &named, wantFatal: true},
		{name: "canceled", err: fmt.Errorf("list: %w", context.Canceled), selection: nil, wantFatal: true},
		{name: "deadline", err: fmt.Errorf("list: %w", context.DeadlineExceeded), selection: &empty, wantFatal: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := fatalLaunchPluginError(test.err, test.selection)
			if (got != nil) != test.wantFatal {
				t.Fatalf("fatalLaunchPluginError = %v, wantFatal=%v", got, test.wantFatal)
			}
		})
	}
}
