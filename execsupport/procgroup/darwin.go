//go:build darwin

package procgroup

import (
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

// Darwin's proc_info ABI is declared in XNU's proc_info.h and
// proc_info_private.h. PROC_PIDINFO answers a proc_bsdinfo, whose
// pbi_start_tvsec/pbi_start_tvusec (offsets 120 and 128) name when the process
// began. Callers that issue their own proc_info calls (signaling by audit
// token, listing descriptors) share ProcInfo, so the syscall plumbing and the
// ABI constants live here once.
const (
	procInfoCall          = 336
	procInfoPID           = 2
	procInfoTBSDInfo      = 3
	procInfoTBSDInfoSize  = 136
	procInfoStartTimeSec  = 120
	procInfoStartTimeUSec = 128
)

// ErrNoSuchProcess reports that the kernel has no process with the pid. It is
// the one proc_info failure a caller may read as "it exited"; every other error
// is doubt.
var ErrNoSuchProcess = errors.New("procgroup: no such process")

// ProcInfo issues one proc_info(2) query: call names the operation, flavor its
// variant, arg an operation-specific argument, and buf receives up to len(buf)
// bytes. It returns the byte count the kernel wrote.
func ProcInfo(call, pid, flavor int, arg uintptr, buf []byte) (int, error) {
	var ptr unsafe.Pointer
	if len(buf) > 0 {
		ptr = unsafe.Pointer(&buf[0])
	}
	n, _, errno := syscall.Syscall6(procInfoCall, uintptr(call), uintptr(pid),
		uintptr(flavor), arg, uintptr(ptr), uintptr(len(buf)))
	runtime.KeepAlive(buf)
	if errno != 0 {
		if errno == syscall.ESRCH {
			return 0, ErrNoSuchProcess
		}
		return 0, errno
	}
	return int(n), nil
}

// ProcInfoSnapshot reads exactly size bytes of one PROC_PIDINFO flavor. A short
// answer is an error: the caller sized the buffer from the ABI, so it describes
// a kernel that did not fill it.
func ProcInfoSnapshot(pid, flavor, size int) ([]byte, error) {
	data := make([]byte, size)
	n, err := ProcInfo(procInfoPID, pid, flavor, 0, data)
	if err != nil {
		return nil, err
	}
	if n != size {
		return nil, fmt.Errorf("procgroup: darwin process info flavor %d returned %d bytes, expected %d", flavor, n, size)
	}
	return data, nil
}

// StartTimeFromBSDInfo decodes when the process named by a proc_bsdinfo began.
// ok=false means the bytes do not hold a start time.
func StartTimeFromBSDInfo(info []byte) (time.Time, bool) {
	if len(info) < procInfoStartTimeUSec+8 {
		return time.Time{}, false
	}
	seconds := int64(binary.NativeEndian.Uint64(info[procInfoStartTimeSec:]))
	micros := int64(binary.NativeEndian.Uint64(info[procInfoStartTimeUSec:]))
	if seconds <= 0 {
		return time.Time{}, false
	}
	return time.Unix(seconds, micros*1000), true
}

// ProcessStartTime reports when the process named by pid began. It exists for
// the PID-reuse guard: a signal is sent only when this time still matches the
// one a durable record carries, so a recycled pid/pgid is never signaled.
//
// ok=false means the start time could not be established (the process is gone,
// or the kernel refused the query); callers must treat that as "not the
// recorded process", never as proof either way.
func ProcessStartTime(pid int) (time.Time, bool) {
	if pid <= 0 {
		return time.Time{}, false
	}
	info, err := ProcInfoSnapshot(pid, procInfoTBSDInfo, procInfoTBSDInfoSize)
	if err != nil {
		return time.Time{}, false
	}
	return StartTimeFromBSDInfo(info)
}
