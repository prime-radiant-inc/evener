//go:build unix

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestDelegateControllerCloseDrainRetriesStaleEvidence(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerRunning(t, c, "dlg_unrelated", "")
	result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	stop := c.stopForResult(result)
	transcriptPath := filepath.Join(c.stateDir, sessionsSubdir, "child-dlg_target.transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(transcriptPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := syscall.Mkfifo(transcriptPath, 0o600); err != nil {
		t.Fatalf("Mkfifo: %v", err)
	}
	nextTranscriptPath := transcriptPath + ".next"
	if err := syscall.Mkfifo(nextTranscriptPath, 0o600); err != nil {
		t.Fatalf("Mkfifo next pass: %v", err)
	}
	readerOpened := make(chan struct{}, 2)
	releaseFirst := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		for pass := range 2 {
			file, openErr := os.OpenFile(transcriptPath, os.O_WRONLY, 0)
			if openErr != nil {
				writerDone <- openErr
				return
			}
			readerOpened <- struct{}{}
			if pass == 0 {
				<-releaseFirst
				if renameErr := os.Rename(nextTranscriptPath, transcriptPath); renameErr != nil {
					writerDone <- renameErr
					return
				}
			}
			_, writeErr := file.WriteString(`{"kind":"header","format_version":2,"session_id":"child-dlg_target","created_at":"0001-01-01T00:00:00Z","profile_id":"","model":""}` + "\n")
			closeErr := file.Close()
			if writeErr != nil || closeErr != nil {
				writerDone <- errors.Join(writeErr, closeErr)
				return
			}
		}
		writerDone <- nil
	}()

	drainResult := make(chan error, 1)
	go func() { drainResult <- c.drainStopForClose(context.Background(), stop) }()
	<-readerOpened
	if _, err := c.BeginShellWork(delegateLease{delegateID: "dlg_unrelated", generation: 1}); err != nil {
		t.Fatalf("BeginShellWork unrelated evidence mutation: %v", err)
	}
	close(releaseFirst)
	if err := <-drainResult; err != nil {
		var writerErr error
		select {
		case writerErr = <-writerDone:
		default:
			// Release the second FIFO writer on the old early-return path.
			reader, openErr := os.Open(transcriptPath)
			if openErr == nil {
				_ = reader.Close()
			}
			writerErr = <-writerDone
		}
		t.Fatalf("drainStopForClose after stale evidence = %v (writer: %v)", err, writerErr)
	}
	if err := <-writerDone; err != nil {
		t.Fatalf("transcript FIFO writer: %v", err)
	}
	select {
	case <-stop.done:
	default:
		t.Fatal("stop remained pending after stale evidence recollection")
	}
}
