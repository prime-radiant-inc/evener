//go:build linux || darwin

package execenv

import (
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestWaitForStreamPipeCloseObservesWriterLifetime(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})

	closed, canSignal, err := waitForStreamPipeClose(reader, 0)
	if err != nil {
		t.Fatal(err)
	}
	if closed || !canSignal {
		t.Fatalf("open pipe = (closed %v, can signal %v), want (false, true)", closed, canSignal)
	}

	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	closed, canSignal, err = waitForStreamPipeClose(reader, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !closed || !canSignal {
		t.Fatalf("closed pipe = (closed %v, can signal %v), want (true, true)", closed, canSignal)
	}
}

func TestWaitForStreamPipeCloseRetriesInterruptedPoll(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})

	var timeouts []int
	poll := func(fds []unix.PollFd, timeout int) (int, error) {
		timeouts = append(timeouts, timeout)
		if len(timeouts) == 1 {
			return 0, unix.EINTR
		}
		fds[0].Revents = unix.POLLHUP
		return 1, nil
	}
	closed, canSignal, err := waitForStreamPipeCloseWithPoll(reader, 0, poll)
	if err != nil {
		t.Fatal(err)
	}
	if !closed || !canSignal {
		t.Fatalf("retried poll = (closed %v, can signal %v), want (true, true)", closed, canSignal)
	}
	if len(timeouts) != 2 || timeouts[0] != 0 || timeouts[1] != 0 {
		t.Fatalf("poll timeouts = %v, want two nonblocking observations", timeouts)
	}
}
