//go:build unix

package agent

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
)

// TestProcessInput_NonRegularAttachmentEntry_NotFollowed pins the last entry
// shape the dedupe gate must refuse without opening: a FIFO at the
// content-addressed leaf must neither block the turn (os.ReadFile opens a
// FIFO read-only and blocks until a writer appears) nor be announced. The
// guard timer bounds the test, not the production path: with the lstat gate
// the refusal is immediate; without it, the open blocks.
func TestProcessInput_NonRegularAttachmentEntry_NotFollowed(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sess := newImagePersistenceSession(t, stateDir, replyStep("reply"))
	png := validPNGFixture(t)
	img := ImageAttachment{MediaType: "image/png", Data: png, Name: "shot.png"}

	wantPath := expectedAttachmentPath(t, stateDir, sess.ID(), img)
	if err := os.MkdirAll(filepath.Dir(wantPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := syscall.Mkfifo(wantPath, 0o600); err != nil {
		t.Fatalf("plant fifo: %v", err)
	}
	warnCh := collectWarnings(sess)

	type inputResult struct {
		err error
	}
	done := make(chan inputResult, 1)
	go func() {
		_, err := sess.ProcessInput(context.Background(), "look at this", []ImageAttachment{img})
		done <- inputResult{err: err}
	}()
	// TRIPWIRE: bounds the missing-refusal case only — with the lstat gate the
	// refusal is immediate and this case never selects; without it the open
	// blocks, and the test must fail fast rather than hang.
	timeout := time.After(10 * time.Second)
	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("ProcessInput: %v", res.err)
		}
	case <-timeout:
		t.Fatal("ProcessInput did not refuse the non-regular entry promptly (it blocked opening it)")
	}

	turn := lastTurnOfKind(t, sess, schema.TurnUserInput)
	if _, ok := findSystemNotificationPart(turn.Message); ok {
		t.Errorf("a non-regular entry must not be announced as the attachment: %+v", turn.Message.Content)
	}
	awaitWarningNaming(t, warnCh, "shot.png")
}
