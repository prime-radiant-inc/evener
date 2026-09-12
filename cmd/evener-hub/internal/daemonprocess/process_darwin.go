package daemonprocess

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Darwin's proc_info ABI is declared in XNU's proc_info.h and
// proc_info_private.h. Audit-token signaling atomically checks pidversion;
// kill(2) cannot provide that guarantee.
const (
	darwinProcInfo         = 336
	darwinPIDInfo          = 2
	darwinPIDFDInfo        = 3
	darwinSignalAuditToken = 0x11
	darwinListFDs          = 1
	darwinBSDInfo          = 3
	darwinUniqueInfo       = 17
	darwinVnodeInfo        = 1
)

type darwinProcess struct {
	pid     int
	unique  uint64
	version uint32
}

// NewController creates the native daemon identity verifier.
func NewController() Controller { return controller{bind: openDarwinProcess} }
func openDarwinProcess(pid int) (processHandle, error) {
	unique, version, err := darwinGeneration(pid)
	if err != nil {
		return nil, err
	}
	return &darwinProcess{pid: pid, unique: unique, version: version}, nil
}
func (p *darwinProcess) close() error { return nil }

func darwinProcCall(call, pid, flavor int, arg uintptr, buf []byte) (int, error) {
	var ptr unsafe.Pointer
	if len(buf) > 0 {
		ptr = unsafe.Pointer(&buf[0])
	}
	n, _, errno := syscall.Syscall6(darwinProcInfo, uintptr(call), uintptr(pid), uintptr(flavor), arg, uintptr(ptr), uintptr(len(buf)))
	runtime.KeepAlive(buf)
	if errno != 0 {
		if errno == syscall.ESRCH {
			return 0, ErrExited
		}
		return 0, errno
	}
	return int(n), nil
}
func darwinReadInfo(pid, flavor, size int) ([]byte, error) {
	data := make([]byte, size)
	n, err := darwinProcCall(darwinPIDInfo, pid, flavor, 0, data)
	if err != nil {
		return nil, err
	}
	if n != size {
		return nil, fmt.Errorf("darwin process info flavor %d returned %d bytes, expected %d", flavor, n, size)
	}
	return data, nil
}
func darwinGeneration(pid int) (uint64, uint32, error) {
	data, err := darwinReadInfo(pid, darwinUniqueInfo, 56)
	if err != nil {
		return 0, 0, err
	}
	return binary.NativeEndian.Uint64(data[16:]), binary.NativeEndian.Uint32(data[32:]), nil
}
func (p *darwinProcess) kill() error {
	token := make([]byte, 32)
	binary.NativeEndian.PutUint32(token[20:], uint32(p.pid))
	binary.NativeEndian.PutUint32(token[28:], p.version)
	_, err := darwinProcCall(darwinSignalAuditToken, 0, int(unix.SIGKILL), 0, token)
	return err
}
func (p *darwinProcess) exited() (bool, error) {
	unique, version, err := darwinGeneration(p.pid)
	if errors.Is(err, ErrExited) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if unique != p.unique || version != p.version {
		return true, nil
	}
	info, err := darwinReadInfo(p.pid, darwinBSDInfo, 136)
	if errors.Is(err, ErrExited) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return binary.NativeEndian.Uint32(info[4:]) == 5, nil // SZOMB
}
func (p *darwinProcess) inspect(t Target) (identity, error) {
	gone, err := p.exited()
	if err != nil {
		return identity{}, err
	}
	if gone {
		return identity{}, ErrExited
	}
	info, err := darwinReadInfo(p.pid, darwinBSDInfo, 136)
	if err != nil {
		return identity{}, err
	}
	args, err := unix.SysctlRaw("kern.procargs2", p.pid)
	if err != nil {
		return identity{}, err
	}
	argv, err := darwinArguments(args)
	if err != nil {
		return identity{}, err
	}
	owns, err := darwinOwnsLog(p.pid, t)
	if err != nil {
		return identity{}, err
	}
	gone, err = p.exited()
	if err != nil {
		return identity{}, err
	}
	if gone {
		return identity{}, ErrExited
	}
	return identity{generation: strconv.FormatUint(p.unique, 10) + ":" + strconv.FormatUint(uint64(p.version), 10), uid: int(binary.NativeEndian.Uint32(info[20:])), startedAt: time.Unix(int64(binary.NativeEndian.Uint64(info[120:])), int64(binary.NativeEndian.Uint64(info[128:]))*1000), argv: argv, ownsLog: owns}, nil
}

func darwinArguments(data []byte) ([]string, error) {
	if len(data) < 4 {
		return nil, errors.New("missing Darwin process arguments")
	}
	count := int(binary.NativeEndian.Uint32(data))
	data = data[4:]
	end := bytes.IndexByte(data, 0)
	if end < 0 {
		return nil, errors.New("missing Darwin executable path")
	}
	data = data[end+1:]
	data = bytes.TrimLeft(data, "\x00")
	if count < 1 || count > len(data) {
		return nil, errors.New("invalid Darwin argument count")
	}
	args := make([]string, 0, count)
	for range count {
		end = bytes.IndexByte(data, 0)
		if end < 0 {
			return nil, errors.New("truncated Darwin arguments")
		}
		args = append(args, string(data[:end]))
		data = data[end+1:]
	}
	return args, nil
}

func darwinOwnsLog(pid int, t Target) (bool, error) {
	path := filepath.Join(t.StateDir, "sessions", t.SessionID+".api.jsonl")
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return false, err
	}
	defer func() { _ = unix.Close(fd) }()
	var expected unix.Stat_t
	if err := unix.Fstat(fd, &expected); err != nil {
		return false, err
	}
	if expected.Mode&unix.S_IFMT != unix.S_IFREG {
		return false, nil
	}
	size, err := darwinProcCall(darwinPIDInfo, pid, darwinListFDs, 0, nil)
	if err != nil {
		return false, err
	}
	// A changing FD table can outgrow this snapshot; refusal is safe, and a later
	// explicit force-stop request takes a fresh snapshot.
	entries := make([]byte, size+32*8)
	n, err := darwinProcCall(darwinPIDInfo, pid, darwinListFDs, 0, entries)
	if err != nil {
		return false, err
	}
	if n > len(entries) || n%8 != 0 {
		return false, errors.New("invalid Darwin descriptor list")
	}
	for offset := 0; offset < n; offset += 8 {
		if binary.NativeEndian.Uint32(entries[offset+4:]) != 1 {
			continue
		} // vnode
		targetFD := binary.NativeEndian.Uint32(entries[offset:])
		data := make([]byte, 176)
		got, err := darwinProcCall(darwinPIDFDInfo, pid, darwinVnodeInfo, uintptr(targetFD), data)
		if errors.Is(err, unix.EBADF) || errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			return false, err
		}
		if got != len(data) {
			return false, errors.New("incomplete Darwin vnode descriptor info")
		}
		flags := binary.NativeEndian.Uint32(data)
		status := binary.NativeEndian.Uint32(data[4:])
		if flags&2 == 0 || flags&0x4000 == 0 || status&2 == 0 {
			continue
		} // FWRITE, FWASLOCKED, PROC_FP_CLEXEC
		if binary.NativeEndian.Uint32(data[24:]) != uint32(expected.Dev) || binary.NativeEndian.Uint64(data[32:]) != expected.Ino {
			continue
		}
		// FWASLOCKED is historical, not proof of current ownership. Evener's
		// APILogger never unlocks a retained descriptor: Close closes it, and exec
		// closes its O_CLOEXEC descriptor. Together with live exclusive contention,
		// a still-open matching descriptor proves ownership within that lifecycle.
		// This does not defend against a same-user process impersonating Evener.
		err = unix.Flock(fd, unix.LOCK_SH|unix.LOCK_NB)
		if err == nil {
			if err := unix.Flock(fd, unix.LOCK_UN); err != nil {
				return false, err
			}
			return false, nil
		}
		if errors.Is(err, unix.EWOULDBLOCK) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}
