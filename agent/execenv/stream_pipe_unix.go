//go:build linux || darwin

package execenv

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func waitForStreamPipeClose(reader *os.File, delay time.Duration) (bool, bool, error) {
	return waitForStreamPipeCloseWithPoll(reader, delay, unix.Poll)
}

func waitForStreamPipeCloseWithPoll(
	reader *os.File,
	delay time.Duration,
	poll func([]unix.PollFd, int) (int, error),
) (bool, bool, error) {
	deadline := time.Now().Add(delay)
	timeout := streamPipePollTimeout(delay)
	pollFD := []unix.PollFd{{
		Fd:     int32(reader.Fd()),
		Events: unix.POLLHUP,
	}}
	for {
		if _, err := poll(pollFD, timeout); err != nil {
			if errors.Is(err, unix.EINTR) {
				timeout = streamPipePollTimeout(time.Until(deadline))
				continue
			}
			return false, true, err
		}
		break
	}
	if pollFD[0].Revents&unix.POLLNVAL != 0 {
		return false, true, errors.New("poll streamed command output: invalid pipe")
	}
	return pollFD[0].Revents&unix.POLLHUP != 0, true, nil
}

func streamPipePollTimeout(delay time.Duration) int {
	if delay <= 0 {
		return 0
	}
	timeout := int(delay / time.Millisecond)
	if delay%time.Millisecond != 0 {
		timeout++
	}
	return timeout
}
