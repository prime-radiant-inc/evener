//go:build linux

package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/execsupport/procgroup"
	"primeradiant.com/evener/rendezvous"
)

// TestHubRosterDepartureReapsSurvivingBackgroundGroup drives the real wiring: a
// registry built against a roster that holds a session, with the session's
// journal naming a live background process group. When the roster's next
// publication finds that session departed, the session-gone hook the registry
// installs must end the group (#4843).
func TestHubRosterDepartureReapsSurvivingBackgroundGroup(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	const sessionID = "sess_depart_reap"
	leader := startReapProcessGroup(t)
	writeReapBackgroundRecord(t, stateDir, sessionID, "job_depart_reap", leader.pid, leader.started)

	// The hub's own pid stands in for a daemon still running, so the roster keeps
	// the session until the next publication finds it gone.
	entry := rendezvous.Entry{PID: os.Getpid(), StateDir: stateDir}
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{Entry: entry, SessionID: sessionID})
	_ = newHubSourceRegistry(hubcore.WebConfig{Roster: roster, ReapSessionJobs: agent.ReapSessionBackgroundJobs})

	// An unconfigured roster scans no directory, so this publication finds no
	// entries and the seeded session departs: the shape a dead daemon's file
	// leaving the rendezvous directory takes.
	roster.Refresh()

	if !leader.waitWithin(t, 10*time.Second) {
		t.Fatal("a surviving background group outlived the roster's departure announcement")
	}
}

// TestHubStartupSweepReapsACrashedSessionsBackgroundGroup drives startup itself:
// a rendezvous entry left by a daemon that died while the hub was down, whose
// session journal names a live background process group. No session-gone
// announcement fires for that entry — the hub's first publication has no
// earlier listing to leave — so only the startup sweep can end the group.
func TestHubStartupSweepReapsACrashedSessionsBackgroundGroup(t *testing.T) {
	_, cfg, deps := newTraceMainTestDeps(t)
	stateDir := t.TempDir()
	const sessionID = "sess_startup_reap"
	leader := startReapProcessGroup(t)
	writeReapBackgroundRecord(t, stateDir, sessionID, "job_startup_reap", leader.pid, leader.started)

	// A stand-in for the daemon's pid: started so it is a real pid, then reaped so
	// the roster confirms it gone and publishes the session as crashed.
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatalf("start and reap the departed daemon's stand-in: %v", err)
	}
	if _, err := rendezvous.Write(cfg.RunDir, rendezvous.Entry{
		PID: gone.Process.Pid, StateDir: stateDir, SessionID: sessionID, StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("publish the crashed daemon's entry: %v", err)
	}

	deps.serve = func(context.Context, hubHTTPServer) error {
		if !leader.waitWithin(t, 20*time.Second) {
			t.Error("a crashed session's background group survived the hub's startup sweep")
		}
		return nil
	}
	var stderr bytes.Buffer
	if err := runMain([]string{"-addr", cfg.Addr, "-evener", "/bin/evener"}, &stderr, deps); err != nil {
		t.Fatalf("runMain: %v\n%s", err, stderr.String())
	}
}

// reapProcessGroup is a real command in its own process group, the shape a
// background shell job takes.
type reapProcessGroup struct {
	pid     int
	started time.Time
	cmd     *exec.Cmd
}

func startReapProcessGroup(t *testing.T) *reapProcessGroup {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = procgroup.SysProcAttr()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start process group: %v", err)
	}
	proc := &reapProcessGroup{pid: cmd.Process.Pid, started: reapProcessStartedAt(t, cmd.Process.Pid), cmd: cmd}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return proc
}

// waitWithin reports whether the process ended within d. A process that never
// ends is killed so the test reports the failure instead of hanging.
func (p *reapProcessGroup) waitWithin(t *testing.T, d time.Duration) bool {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		_ = p.cmd.Process.Kill()
		<-done
		return false
	}
}

// writeReapBackgroundRecord plants the durable state a crashed daemon leaves
// behind: a running background shell job naming pid and its start time. The
// journal here is the one agent.ReapSessionBackgroundJobs reads, and the record
// carries the same JSON the store writes.
func writeReapBackgroundRecord(t *testing.T, stateDir, sessionID, jobID string, pid int, started time.Time) {
	t.Helper()
	record := map[string]any{
		"kind":                  "job_started",
		"ts":                    time.Now().UTC().Format(time.RFC3339Nano),
		"job_id":                jobID,
		"type":                  "shell",
		"background":            true,
		"owner_session_id":      sessionID,
		"visible_to_session_id": sessionID,
		"process_group_id":      pid,
		"process_start_time":    started.UTC().Format(time.RFC3339Nano),
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	dir := schema.SessionWorkDir(stateDir, sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "jobs.jsonl"), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// reapProcessStartedAt reads a live process's start time the way the agent's
// reaper does, so the durable record and the kernel agree. It is this test's
// own oracle, independent of the code under test.
func reapProcessStartedAt(t *testing.T, pid int) time.Time {
	t.Helper()
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		t.Fatalf("read process stat: %v", err)
	}
	// comm (field 2) is parenthesized and may contain spaces or parentheses, so
	// the fields after it are read from the last closing paren.
	last := strings.LastIndexByte(string(stat), ')')
	if last < 0 {
		t.Fatalf("process stat has no command field: %q", stat)
	}
	fields := strings.Fields(string(stat[last+2:]))
	const starttimeField = 19 // field 22, after the fields that restart at 3
	if len(fields) <= starttimeField {
		t.Fatalf("process stat has %d fields after comm, want more than %d", len(fields), starttimeField)
	}
	ticks, err := strconv.ParseUint(fields[starttimeField], 10, 64)
	if err != nil {
		t.Fatalf("parse starttime: %v", err)
	}
	return reapBootTime(t).Add(time.Duration(ticks) * (time.Second / 100))
}

func reapBootTime(t *testing.T) time.Time {
	t.Helper()
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		t.Fatalf("read system stat: %v", err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if !strings.HasPrefix(line, "btime ") {
			continue
		}
		seconds, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "btime ")), 10, 64)
		if err != nil {
			t.Fatalf("parse btime: %v", err)
		}
		return time.Unix(seconds, 0)
	}
	t.Fatal("system stat carries no btime")
	return time.Time{}
}
