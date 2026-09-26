//go:build unix

package hub

import (
	"context"
	"fmt"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/orphanpipe/orphanpipetest"
)

// TestEvenerLaunchCheckDeadlineDoesNotWaitForAnOrphanedPipeHolder pins that the
// launch-check budget bounds the call even when the checked binary leaves a
// child behind holding its output: a wrapper script around evener is enough.
// Killing the direct child does not close a pipe the grandchild inherited, so
// without a WaitDelay the hub's "timeout" lasts as long as the grandchild does.
//
// The grandchild blocks on a FIFO only the test releases, and the test cancels
// the call only once the grandchild holds the pipe, so a call that waits for
// the grandchild cannot return at all (see orphanpipetest).
func TestEvenerLaunchCheckDeadlineDoesNotWaitForAnOrphanedPipeHolder(t *testing.T) {
	t.Parallel()
	for name, call := range map[string]func(ctx context.Context, evenerBinary string) error{
		"validate": func(ctx context.Context, evenerBinary string) error {
			return validateEvenerLaunchContract(ctx, evenerBinary, "openrouter/free", nil)
		},
		"models": func(ctx context.Context, evenerBinary string) error {
			_, err := listEvenerLaunchModelContract(ctx, evenerBinary, nil)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := orphanpipetest.New(t)
			evenerBinary := h.WriteScript(t, "fake-evener", h.Spawn()+"\nwait\n")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() {
				h.AwaitStarted()
				cancel()
			}()
			done := make(chan error, 1)
			go func() { done <- call(ctx, evenerBinary) }()
			assertHubLaunchError(t, orphanpipetest.Await(t, h, done))
		})
	}
}

// TestEvenerLaunchCheckSuccessSurvivesAnOrphanedPipeHolder is the other side
// of the WaitDelay bound: a check that answered and exited 0 while leaving a
// child on its output pipe still answered. Once the delay closes the pipe,
// exec reports ErrWaitDelay over the complete output, and that must read as
// the response it is, not as a failed check quoting its own JSON.
func TestEvenerLaunchCheckSuccessSurvivesAnOrphanedPipeHolder(t *testing.T) {
	t.Parallel()
	response := fmt.Sprintf(`{"protocol":%q,"launch_flags":[%q],"models":[{"provider":"openai","model":"gpt-5"}]}`, appwire.ProtocolVersion, requiredLaunchFlag)
	for name, call := range map[string]func(evenerBinary string) error{
		"validate": func(evenerBinary string) error {
			return validateEvenerLaunchContract(context.Background(), evenerBinary, "openai/gpt-5", nil)
		},
		"models": func(evenerBinary string) error {
			resp, err := listEvenerLaunchModelContract(context.Background(), evenerBinary, nil)
			if err == nil && (len(resp.Data) != 1 || resp.Data[0].Model != "gpt-5") {
				return fmt.Errorf("models = %+v, want the one the check answered", resp.Data)
			}
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := orphanpipetest.New(t)
			evenerBinary := h.WriteScript(t, "fake-evener", h.Spawn()+"\nprintf '%s\\n' '"+response+"'\n")
			done := make(chan error, 1)
			go func() { done <- call(evenerBinary) }()
			if err := orphanpipetest.Await(t, h, done); err != nil {
				t.Fatalf("a check that answered and exited 0 was reported as: %v", err)
			}
		})
	}
}
