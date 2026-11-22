//go:build darwin

package agent

import (
	"encoding/binary"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

// Darwin's proc_info ABI is declared in XNU's proc_info.h. PROC_PIDTBSDINFO
// answers a proc_bsdinfo, whose pbi_start_tvsec/pbi_start_tvusec (offsets 120
// and 128) name when the process began.
const (
	darwinProcInfo            = 336
	darwinPIDInfo             = 2
	darwinPIDTBSDInfo         = 3
	darwinPIDTBSDInfoSize     = 136
	darwinStartTimeSecOffset  = 120
	darwinStartTimeUSecOffset = 128
)

// processStartedAt reports when the process named by pid began, read from
// proc_pidinfo. It is the PID-reuse guard for reaping a dead daemon's
// background job groups: a signal is sent only when this time still matches the
// one the durable job record carries, so a recycled pid/pgid is never signaled.
//
// ok=false means the start time could not be established (the process is gone,
// or the kernel refused the query); callers must treat that as "not the
// recorded process", never as proof either way.
func processStartedAt(pid int) (time.Time, bool) {
	if pid <= 0 {
		return time.Time{}, false
	}
	info := make([]byte, darwinPIDTBSDInfoSize)
	n, _, errno := syscall.Syscall6(darwinProcInfo, darwinPIDInfo, uintptr(pid),
		darwinPIDTBSDInfo, 0, uintptr(unsafe.Pointer(&info[0])), uintptr(len(info)))
	runtime.KeepAlive(info)
	if errno != 0 || int(n) != len(info) {
		return time.Time{}, false
	}
	seconds := int64(binary.NativeEndian.Uint64(info[darwinStartTimeSecOffset:]))
	micros := int64(binary.NativeEndian.Uint64(info[darwinStartTimeUSecOffset:]))
	if seconds <= 0 {
		return time.Time{}, false
	}
	return time.Unix(seconds, micros*1000), true
}
