package daemonprocess

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A preempted clock pair must not become the start estimate: retry, and use
// the first pair whose two CLOCK_BOOTTIME reads are within one tick.
func TestLinuxBootEpochBoundsClockReadGap(t *testing.T) {
	hz, err := linuxClockHZ()
	if err != nil {
		t.Fatal(err)
	}
	tick, err := linuxTicksDuration(1, hz)
	if err != nil {
		t.Fatal(err)
	}
	wall := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	bootReads := []time.Duration{0, 2 * tick, 3 * tick, 3*tick + time.Microsecond}
	next := 0
	calls := 0
	previous := linuxClockGettime
	t.Cleanup(func() { linuxClockGettime = previous })
	linuxClockGettime = func(clock int32, ts *unix.Timespec) error {
		calls++
		switch clock {
		case unix.CLOCK_BOOTTIME:
			if next >= len(bootReads) {
				t.Fatalf("boot clock read %d has no staged value", next)
			}
			*ts = unix.NsecToTimespec(int64(bootReads[next]))
			next++
		case unix.CLOCK_REALTIME:
			*ts = unix.NsecToTimespec(wall.UnixNano())
		default:
			t.Fatalf("unexpected clock id %d", clock)
		}
		return nil
	}
	got, err := linuxBootEpoch()
	if err != nil {
		t.Fatal(err)
	}
	if want := wall.Add(-3 * tick); !got.Equal(want) {
		t.Fatalf("boot epoch = %v, want the quiet pair's %v", got, want)
	}
	if calls != 6 {
		t.Fatalf("clock reads = %d, want 6 (one preempted pair, then a quiet one)", calls)
	}
}

// Every pair preempted past the tick leaves the start unbounded; refusing is
// the only answer that cannot vouch for the wrong process.
func TestLinuxBootEpochRefusesPersistentPreemption(t *testing.T) {
	hz, err := linuxClockHZ()
	if err != nil {
		t.Fatal(err)
	}
	tick, err := linuxTicksDuration(1, hz)
	if err != nil {
		t.Fatal(err)
	}
	next := 0
	previous := linuxClockGettime
	t.Cleanup(func() { linuxClockGettime = previous })
	linuxClockGettime = func(clock int32, ts *unix.Timespec) error {
		if clock != unix.CLOCK_BOOTTIME && clock != unix.CLOCK_REALTIME {
			t.Fatalf("unexpected clock id %d", clock)
		}
		if clock == unix.CLOCK_REALTIME {
			*ts = unix.NsecToTimespec(time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC).UnixNano())
			return nil
		}
		value := time.Duration(next/2) * 3 * tick
		if next%2 == 1 {
			value += tick + time.Millisecond
		}
		*ts = unix.NsecToTimespec(int64(value))
		next++
		return nil
	}
	got, err := linuxBootEpoch()
	if err == nil {
		t.Fatal("vouched with a start bound the clock reads could not justify")
	}
	if !got.IsZero() {
		t.Fatalf("boot epoch = %v with an error, want the zero time", got)
	}
	if want := 2 * linuxClockReadAttempts; next != want {
		t.Fatalf("boot clock reads = %d, want %d (every attempt preempted)", next, want)
	}
}

func TestLinuxProcessExitedHandlesInterruptedPoll(t *testing.T) {
	realErr := errors.New("poll failed")
	for _, tc := range []struct {
		name        string
		revents     int16
		injectEINTR bool
		wantRealErr bool
		wantGone    bool
		wantErr     error
		wantCalls   int
	}{
		{name: "EINTR then live", injectEINTR: true, wantCalls: 2},
		{name: "EINTR then exited", injectEINTR: true, revents: unix.POLLIN, wantGone: true, wantCalls: 2},
		{name: "EINTR then hangup", injectEINTR: true, revents: unix.POLLHUP, wantGone: true, wantCalls: 2},
		{name: "EINTR then real error", injectEINTR: true, wantRealErr: true, wantErr: realErr, wantCalls: 2},
		{name: "closed descriptor", revents: unix.POLLNVAL, wantErr: os.ErrClosed, wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			poll := func(fds []unix.PollFd, timeout int) (int, error) {
				calls++
				if timeout != 0 {
					t.Fatalf("poll timeout = %d; want 0", timeout)
				}
				if calls == 1 && tc.injectEINTR {
					return 0, unix.EINTR
				}
				fds[0].Revents = tc.revents
				if tc.wantRealErr {
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
// A Go test helper can start inside that tick, unlike a fully initialized
// daemon, so its start is only settled when the next tick begins. The wait
// runs on CLOCK_BOOTTIME, the clock the start tick counts, not on a wall-clock
// conversion of it: linuxBootEpoch bounds the gap between its two clock reads
// to one tick, so an estimate of the start cannot pass a rendezvous read one
// tick after the start tick ends. The old wall-clock wait sampled the
// conversion itself, and a preempted read (tens of milliseconds under load)
// pushed the estimate past the rendezvous (#5746).
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
	ticks, err := strconv.ParseUint(facts.generation, 10, 64)
	if err != nil {
		t.Fatalf("parse process start ticks %q: %v", facts.generation, err)
	}
	hz, err := linuxClockHZ()
	if err != nil {
		t.Fatal(err)
	}
	_, upper, err := linuxStartOffsetBounds(ticks, hz)
	if err != nil {
		t.Fatal(err)
	}
	tick, err := linuxTicksDuration(1, hz)
	if err != nil {
		t.Fatal(err)
	}
	settle := upper + tick
	for {
		var boot unix.Timespec
		if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &boot); err != nil {
			t.Fatal(err)
		}
		remaining := settle - time.Duration(boot.Nano())
		if remaining <= 0 {
			return time.Now()
		}
		timer := time.NewTimer(remaining)
		<-timer.C
		timer.Stop()
	}
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
			_, got, err := linuxStartOffsetBounds(tc.ticks, tc.hz)
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
	_, offset, err := linuxStartOffsetBounds(20_000_000_000, 100)
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

// The tick's two bounds: ticks/hz and (ticks+1)/hz.
func TestLinuxStartOffsetBounds(t *testing.T) {
	lower, upper, err := linuxStartOffsetBounds(150, 100)
	if err != nil {
		t.Fatal(err)
	}
	if lower != 1500*time.Millisecond || upper != 1510*time.Millisecond {
		t.Fatalf("bounds = %v..%v, want 1.5s..1.51s", lower, upper)
	}
}

// /proc/<pid>/stat carries a process's start ticks (field 22) and its flags
// (field 9) in one read; PF_EXITING (0x4) marks a process that has begun
// exiting before its pidfd reports it gone (#3339). The command field may
// hold spaces and parentheses, so fields count from the last ')'.
func TestLinuxStatFacts(t *testing.T) {
	stat := func(flags string) []byte {
		// pid (comm) state ppid pgrp session tty tpgid flags … starttime(22)
		return []byte("4242 (evener (serve) x) S 1 4242 4242 0 -1 " + flags + " 0 0 0 0 1 2 3 4 20 0 1 0 987654 0 0")
	}
	ticks, exiting, err := linuxStatFacts(stat("4194560"))
	if err != nil || ticks != 987654 || exiting {
		t.Fatalf("live stat: ticks=%d exiting=%v err=%v", ticks, exiting, err)
	}
	ticks, exiting, err = linuxStatFacts(stat("4194564"))
	if err != nil || ticks != 987654 || !exiting {
		t.Fatalf("exiting stat: ticks=%d exiting=%v err=%v", ticks, exiting, err)
	}
	if _, _, err := linuxStatFacts([]byte("4242 (evener) S 1")); err == nil {
		t.Fatal("incomplete stat accepted")
	}
}

// A daemon closing its files can drop a descriptor between the fd directory
// read and its fdinfo read (#3339). That descriptor is skipped, as a failed
// Stat of it already is, rather than failing the whole inspection.
func TestLinuxOwnsLogSkipsADescriptorClosedMidScan(t *testing.T) {
	state := t.TempDir()
	if err := os.MkdirAll(filepath.Join(state, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(state, "sessions", "session.api.jsonl")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, dir := range []string{"fd", "fdinfo"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// fd/3 names the log, but its fdinfo is already gone.
	if err := os.Symlink(logPath, filepath.Join(root, "fd", "3")); err != nil {
		t.Fatal(err)
	}
	owns, err := linuxOwnsLog(root, Target{PID: 4242, SessionID: "session", StateDir: state})
	if err != nil || owns {
		t.Fatalf("owns=%v err=%v, want the closed descriptor skipped", owns, err)
	}
}
