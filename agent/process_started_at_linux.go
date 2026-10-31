//go:build linux

package agent

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

// processStartedAt reports when the process named by pid began, read from
// /proc. It exists so the owner-liveness oracle can verify a rendezvous
// entry's daemon identity (review round 6, finding 2): an entry records when
// its daemon registered, so a pid whose current process began AFTER that
// instant is a reused pid wearing a dead daemon's entry — signal-0 liveness
// alone would read the unrelated process as the daemon and hide an abandoned
// lane from recovery.
//
// ok=false means the start time could not be established (the process is
// gone, or /proc could not be read); callers must treat that as doubt about
// the entry's identity, never as proof either way.
func processStartedAt(pid int) (time.Time, bool) {
	if pid <= 0 {
		return time.Time{}, false
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return time.Time{}, false
	}
	// comm (field 2) is parenthesized and may contain spaces or parentheses
	// itself, so the fields after it are read from the LAST closing paren.
	last := strings.LastIndexByte(string(stat), ')')
	if last < 0 || last+2 > len(stat) {
		return time.Time{}, false
	}
	fields := strings.Fields(string(stat[last+2:]))
	// After ") " the fields restart at 3 (state); starttime is field 22.
	const starttimeField = 19 // 22 - 3
	if len(fields) <= starttimeField {
		return time.Time{}, false
	}
	ticks, err := strconv.ParseUint(fields[starttimeField], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	boot, err := systemBootTime()
	if err != nil {
		return time.Time{}, false
	}
	return boot.Add(time.Duration(ticks) * (time.Second / 100)), true
}

// systemBootTime reads the kernel's btime from /proc/stat. It is reported to
// whole seconds, so a derived start time can read up to a second EARLY —
// never late, which is the safe direction for the identity comparison the
// caller makes (see processStartedAt).
func systemBootTime() (time.Time, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, err
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if !strings.HasPrefix(line, "btime ") {
			continue
		}
		seconds, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "btime ")), 10, 64)
		if err != nil {
			return time.Time{}, err
		}
		return time.Unix(seconds, 0), nil
	}
	return time.Time{}, errors.New("/proc/stat carries no btime")
}
