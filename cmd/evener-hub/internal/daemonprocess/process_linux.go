package daemonprocess

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

type linuxProcess struct{ pid, fd int }

// NewController creates the native daemon identity verifier.
func NewController() Controller { return controller{bind: openLinuxProcess} }
func openLinuxProcess(pid int) (processHandle, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil, ErrExited
	}
	if err != nil {
		return nil, fmt.Errorf("open daemon pidfd: %w", err)
	}
	return &linuxProcess{pid: pid, fd: fd}, nil
}
func (p *linuxProcess) close() error { return unix.Close(p.fd) }
func (p *linuxProcess) kill() error {
	err := unix.PidfdSendSignal(p.fd, unix.SIGKILL, nil, 0)
	if errors.Is(err, unix.ESRCH) {
		return ErrExited
	}
	return err
}
func (p *linuxProcess) exited() (bool, error) {
	return p.exitedWith(unix.Poll)
}

func (p *linuxProcess) exitedWith(poll func([]unix.PollFd, int) (int, error)) (bool, error) {
	fds := []unix.PollFd{{Fd: int32(p.fd), Events: unix.POLLIN}}
	var err error
	for {
		_, err = poll(fds, 0)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if err != nil {
		return false, err
	}
	if fds[0].Revents&unix.POLLNVAL != 0 {
		return false, os.ErrClosed
	}
	return fds[0].Revents&(unix.POLLIN|unix.POLLHUP) != 0, nil
}

func (p *linuxProcess) inspect(t Target) (identity, error) {
	gone, err := p.exited()
	if err != nil {
		return identity{}, err
	}
	if gone {
		return identity{}, ErrExited
	}
	root := "/proc/" + strconv.Itoa(p.pid)
	var procStat unix.Stat_t
	if err := unix.Stat(root, &procStat); err != nil {
		return identity{}, p.inspectionError(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "stat"))
	if err != nil {
		return identity{}, p.inspectionError(err)
	}
	end := bytes.LastIndexByte(raw, ')')
	if end < 0 {
		return identity{}, errors.New("malformed process stat")
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 20 {
		return identity{}, errors.New("incomplete process stat")
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return identity{}, err
	}
	auxv, err := os.ReadFile("/proc/self/auxv")
	if err != nil {
		return identity{}, err
	}
	hz, err := linuxClockTicks(auxv, int(unsafe.Sizeof(uintptr(0))))
	if err != nil {
		return identity{}, err
	}
	var boot, wall unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &boot); err != nil {
		return identity{}, err
	}
	if err := unix.ClockGettime(unix.CLOCK_REALTIME, &wall); err != nil {
		return identity{}, err
	}
	// The end of the kernel's reported clock tick is a conservative upper bound
	// on process start. Do not accept a reused PID within that tick.
	offset, err := linuxStartOffset(ticks, hz)
	if err != nil {
		return identity{}, err
	}
	started := time.Unix(wall.Sec, wall.Nsec).Add(-time.Duration(boot.Nano())).Add(offset)
	argv, err := os.ReadFile(filepath.Join(root, "cmdline"))
	if err != nil {
		return identity{}, p.inspectionError(err)
	}
	owns, err := linuxOwnsLog(root, t)
	if err != nil {
		return identity{}, p.inspectionError(err)
	}
	gone, err = p.exited()
	if err != nil {
		return identity{}, err
	}
	if gone {
		return identity{}, ErrExited
	}
	return identity{generation: fields[19], uid: int(procStat.Uid), startedAt: started, argv: strings.Split(strings.TrimSuffix(string(argv), "\x00"), "\x00"), ownsLog: owns}, nil
}
func (p *linuxProcess) inspectionError(err error) error {
	if gone, e := p.exited(); e == nil && gone {
		return ErrExited
	}
	return err
}

func linuxOwnsLog(root string, t Target) (bool, error) {
	var expected unix.Stat_t
	path := filepath.Join(t.StateDir, "sessions", t.SessionID+".api.jsonl")
	if err := unix.Lstat(path, &expected); err != nil {
		return false, err
	}
	if expected.Mode&unix.S_IFMT != unix.S_IFREG {
		return false, nil
	}
	entries, err := os.ReadDir(filepath.Join(root, "fd"))
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		var candidate unix.Stat_t
		if err := unix.Stat(filepath.Join(root, "fd", entry.Name()), &candidate); err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue
			}
			return false, err
		}
		if candidate.Dev != expected.Dev || candidate.Ino != expected.Ino {
			continue
		}
		evidence, err := os.ReadFile(filepath.Join(root, "fdinfo", entry.Name()))
		if err != nil {
			return false, err
		}
		if linuxOwnsLock(evidence, t.PID, expected.Dev, expected.Ino) {
			return true, nil
		}
	}
	return false, nil
}

func linuxOwnsLock(data []byte, pid int, dev, ino uint64) bool {
	writable, locked := false, false
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "flags:" {
			flags, err := strconv.ParseUint(fields[1], 8, 64)
			writable = err == nil && flags&unix.O_ACCMODE != unix.O_RDONLY && flags&unix.O_CLOEXEC != 0
		}
		if len(fields) != 9 || fields[0] != "lock:" || fields[2] != "FLOCK" || fields[3] != "ADVISORY" || fields[4] != "WRITE" || fields[5] != strconv.Itoa(pid) || fields[7] != "0" || fields[8] != "EOF" {
			continue
		}
		parts := strings.Split(fields[6], ":")
		if len(parts) != 3 {
			continue
		}
		major, e1 := strconv.ParseUint(parts[0], 16, 32)
		minor, e2 := strconv.ParseUint(parts[1], 16, 32)
		inode, e3 := strconv.ParseUint(parts[2], 10, 64)
		if e1 == nil && e2 == nil && e3 == nil && unix.Mkdev(uint32(major), uint32(minor)) == dev && inode == ino {
			locked = true
		}
	}
	return writable && locked
}

func linuxClockTicks(data []byte, word int) (uint64, error) {
	for len(data) >= word*2 {
		var tag, value uint64
		switch word {
		case 8:
			tag = binary.NativeEndian.Uint64(data)
			value = binary.NativeEndian.Uint64(data[word:])
		case 4:
			tag = uint64(binary.NativeEndian.Uint32(data))
			value = uint64(binary.NativeEndian.Uint32(data[word:]))
		default:
			return 0, errors.New("unsupported auxiliary vector word size")
		}
		if tag == 17 && value > 0 {
			return value, nil
		}
		if tag == 0 {
			break
		}
		data = data[word*2:]
	}
	return 0, errors.New("process clock frequency unavailable")
}

func linuxStartOffset(ticks, hz uint64) (time.Duration, error) {
	if hz == 0 || ticks == math.MaxUint64 {
		return 0, errors.New("invalid process start ticks or clock frequency")
	}
	// Keep the full product until division: even valid durations exceed a
	// uint64 nanosecond intermediate on long-running hosts at ordinary HZ.
	high, low := bits.Mul64(ticks+1, uint64(time.Second))
	if high >= hz {
		return 0, errors.New("process start offset exceeds duration range")
	}
	nanos, _ := bits.Div64(high, low, hz)
	if nanos > math.MaxInt64 {
		return 0, errors.New("process start offset exceeds duration range")
	}
	return time.Duration(nanos), nil
}
