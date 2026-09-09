package daemonprocess

import (
	"encoding/binary"
	"errors"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestLinuxProcessExitedHandlesInterruptedPoll(t *testing.T) {
	realErr := errors.New("poll failed")
	for _, tc := range []struct {
		name      string
		revents   int16
		pollErr   error
		wantGone  bool
		wantErr   error
		wantCalls int
	}{
		{name: "EINTR then live", pollErr: unix.EINTR, wantCalls: 2},
		{name: "EINTR then exited", pollErr: unix.EINTR, revents: unix.POLLIN, wantGone: true, wantCalls: 2},
		{name: "EINTR then hangup", pollErr: unix.EINTR, revents: unix.POLLHUP, wantGone: true, wantCalls: 2},
		{name: "EINTR then real error", pollErr: unix.EINTR, wantErr: realErr, wantCalls: 2},
		{name: "closed descriptor", revents: unix.POLLNVAL, wantErr: os.ErrClosed, wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			poll := func(fds []unix.PollFd, timeout int) (int, error) {
				calls++
				if timeout != 0 {
					t.Fatalf("poll timeout = %d; want 0", timeout)
				}
				if calls == 1 && tc.pollErr == unix.EINTR {
					return 0, unix.EINTR
				}
				fds[0].Revents = tc.revents
				if tc.wantErr == realErr {
					return 0, realErr
				}
				return 1, nil
			}
			gotGone, gotErr := (&linuxProcess{fd: 1}).exitedWith(poll)
			if gotGone != tc.wantGone || !errors.Is(gotErr, tc.wantErr) {
				t.Fatalf("exited = (%v, %v), want (%v, %v)", gotGone, gotErr, tc.wantGone, tc.wantErr)
			}
			if calls != tc.wantCalls {
				t.Fatalf("poll calls = %d, want %d", calls, tc.wantCalls)
			}
		})
	}
}

func TestLinuxLockEvidence(t *testing.T) {
	for _, tt := range []struct {
		name, text string
		want       bool
	}{
		{"exclusive owner", "flags:\t02100002\nlock:\t1: FLOCK  ADVISORY  WRITE  123  08:01:55 0 EOF\n", true},
		{"foreign owner", "flags:\t02100002\nlock:\t1: FLOCK  ADVISORY  WRITE  124  08:01:55 0 EOF\n", false},
		{"shared lock", "flags:\t02100002\nlock:\t1: FLOCK  ADVISORY  READ  123  08:01:55 0 EOF\n", false},
		{"read only", "flags:\t02100000\nlock:\t1: FLOCK  ADVISORY  WRITE  123  08:01:55 0 EOF\n", false},
		{"different inode", "flags:\t02100002\nlock:\t1: FLOCK  ADVISORY  WRITE  123  08:01:56 0 EOF\n", false},
		{"no lock", "flags:\t02100002\n", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := linuxOwnsLock([]byte(tt.text), 123, unix.Mkdev(8, 1), 55); got != tt.want {
				t.Fatalf("owns lock = %v", got)
			}
		})
	}
}

func TestLinuxClockTicks(t *testing.T) {
	buf := make([]byte, 32)
	binary.NativeEndian.PutUint64(buf, 17)
	binary.NativeEndian.PutUint64(buf[8:], 100)
	if ticks, err := linuxClockTicks(buf, 8); err != nil || ticks != 100 {
		t.Fatalf("ticks=%d err=%v", ticks, err)
	}
	if _, err := linuxClockTicks(buf[:8], 8); err == nil {
		t.Fatal("truncated auxiliary vector accepted")
	}
}

// The fixture publishes its rendezvous after the kernel start tick has ended.
// A Go test helper can start inside that tick, unlike a fully initialized daemon.
func fixtureRendezvousTime(t *testing.T, target Target) time.Time {
	t.Helper()
	h, err := openLinuxProcess(target.PID)
	if err != nil {
		t.Fatal(err)
	}
	defer h.close()
	facts, err := h.inspect(target)
	if err != nil {
		t.Fatal(err)
	}
	if delay := time.Until(facts.startedAt); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		<-timer.C
	}
	return time.Now()
}

func TestLinuxStartOffsetLargeTicks(t *testing.T) {
	for _, tc := range []struct {
		name      string
		ticks, hz uint64
		want      time.Duration
		wantError bool
	}{
		{name: "before multiplication overflow", ticks: 18_446_744_072, hz: 100, want: 184_467_440_730_000_000},
		{name: "after multiplication overflow", ticks: 18_446_744_073, hz: 100, want: 184_467_440_740_000_000},
		{name: "long lived host", ticks: 20_000_000_000, hz: 100, want: 200_000_000_010_000_000},
		{name: "maximum duration", ticks: 1<<63 - 2, hz: 1_000_000_000, want: 1<<63 - 1},
		{name: "duration overflow", ticks: 1<<63 - 1, hz: 1_000_000_000, wantError: true},
		{name: "tick overflow", ticks: 1<<64 - 1, hz: 100, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := linuxStartOffset(tc.ticks, tc.hz)
			if tc.wantError {
				if err == nil {
					t.Fatalf("accepted overflowing offset %d", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("offset=%d err=%v; want %d", got, err, tc.want)
			}
		})
	}
}

func TestLinuxLargeStartTicksRefuseNewerProcess(t *testing.T) {
	offset, err := linuxStartOffset(20_000_000_000, 100)
	if err != nil {
		t.Fatal(err)
	}
	// The rendezvous predates the actual start by one second; a wrapped offset
	// would instead place the same verified session owner years before it.
	target := validTarget()
	target.StartedAt = time.Unix(199_999_999, 0)
	facts := validIdentity()
	facts.startedAt = time.Unix(0, 0).Add(offset)
	k := &kernelProcess{facts: facts}
	p, err := testController(k).Open(target)
	if err == nil {
		_ = p.Close()
		t.Fatal("accepted a process newer than its rendezvous after tick overflow")
	}
	if k.signals != 0 {
		t.Fatal("signaled a newer process")
	}
}
