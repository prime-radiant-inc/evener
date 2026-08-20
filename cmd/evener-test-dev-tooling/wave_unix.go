//go:build linux || darwin

package main

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// suiteSysProcAttr places a suite in its own process group so a wave signal
// reaches every forked descendant, not just the script's shell.
func suiteSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

func terminateSuiteGroup(pgid int) { _ = syscall.Kill(-pgid, syscall.SIGTERM) }

func killSuiteGroup(pgid int) { _ = syscall.Kill(-pgid, syscall.SIGKILL) }

// suiteGroupSurvivors lists the live processes still in the suite's process
// group after its direct child has been reaped, as "pid command" strings. A
// signal-0 probe answers the common case (empty group) without spawning
// anything; only a non-empty group pays for the ps listing. Zombies are
// excluded: a just-exited reparented descendant can linger in the group as a
// zombie for an instant while init reaps it, and it is already dead, not a
// leak.
func suiteGroupSurvivors(pgid int) []string {
	if err := syscall.Kill(-pgid, 0); err != nil {
		return nil // ESRCH: the group has no members at all.
	}
	out, err := exec.Command("ps", "-axo", "pid=,pgid=,stat=,command=").Output() //nolint:noctx // one bounded listing during post-Wait bookkeeping; a context adds nothing
	if err != nil {
		return []string{fmt.Sprintf("(process group %d is non-empty but ps failed: %v)", pgid, err)}
	}
	want := strconv.Itoa(pgid)
	var survivors []string
	for line := range strings.SplitSeq(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[1] != want || strings.HasPrefix(fields[2], "Z") {
			continue
		}
		survivors = append(survivors, fields[0]+" "+strings.Join(fields[3:], " "))
	}
	return survivors
}
