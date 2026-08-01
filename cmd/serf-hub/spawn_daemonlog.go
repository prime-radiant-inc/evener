package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// daemonLogDirName is the directory under the hub's run dir that holds one log
// file per spawned daemon. It sits beside the rendezvous files because that is
// the directory a hub already owns per-daemon state in, and rendezvous.List
// reads only <pid>.json entries there, so a subdirectory is invisible to it.
const daemonLogDirName = "logs"

// daemonLog is the file one spawned daemon's stdout and stderr are wired to.
//
// The hub used to forward both into its own stderr, the log every daemon on
// that hub shares. A daemon line there names no session and no time, so it
// belongs to whichever daemon wrote last: "[serve] error: model returned empty
// response after 3 retries" sat as the last line of a hub log directly under a
// "[serve] listening…" line for a DIFFERENT session and read as the smoking
// gun for the session under investigation. Disproving it took
// cross-referencing API logs across all three live sessions sharing that hub
// (kata vca1). Each daemon now says what it has to say in a file of its own.
//
// The file goes to the child as an inherited descriptor and never through a
// writer of the hub's own: os/exec interposes a pipe for any io.Writer that is
// not an *os.File, and a hub-owned pipe kills the daemon on its first write
// after the hub exits — Go raises SIGPIPE to the default handler for writes to
// fd 1 and 2, and spawned daemons are documented to outlive a hub restart
// (SpawnDaemon, hubcore.Roster).
type daemonLog struct {
	file *os.File
	path string
	// pending is true until a session id is known for this log. Only a pending
	// log may be deleted: see removeIfPending.
	pending bool
}

// openDaemonLog opens the file a daemon's output goes to, under runDir.
//
// sessionID names the file when the caller already knows it: a resume keeps
// its session's id, so it keeps — and appends to — that session's log. An
// empty id opens a pending file instead, which adopt names once the daemon
// reports the id it minted for itself.
func openDaemonLog(runDir, sessionID string) (*daemonLog, error) {
	if runDir == "" {
		return nil, errors.New("daemon log: no run dir to write it under")
	}
	dir := filepath.Join(runDir, daemonLogDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("daemon log: %w", err)
	}
	if sessionID == "" {
		f, err := os.CreateTemp(dir, "daemon-pending-*.log")
		if err != nil {
			return nil, fmt.Errorf("daemon log: %w", err)
		}
		return &daemonLog{file: f, path: f.Name(), pending: true}, nil
	}
	path := filepath.Join(dir, daemonLogName(sessionID))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("daemon log: %w", err)
	}
	return &daemonLog{file: f, path: path}, nil
}

// daemonLogName is the file name a session's log goes by. A session id reaches
// the hub from a client on the resume path, so anything that is not an id
// character is folded away: the name has to stay one component of the log
// directory and cannot be talked into naming a file outside it.
func daemonLogName(sessionID string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, sessionID)
	return "daemon-" + safe + ".log"
}

// attach wires cmd's stdout and stderr to this log.
func (l *daemonLog) attach(cmd *exec.Cmd) {
	cmd.Stdout = l.file
	cmd.Stderr = l.file
}

// adopt renames the log to the session that turned out to own it. A fresh
// spawn cannot know the id when it opens the file — the daemon mints it and
// reports it through rendezvous — and a descriptor follows its file through a
// rename, so nothing the daemon has written or is about to write is lost. A
// rename that fails leaves a log that still works and a path that still names
// it.
func (l *daemonLog) adopt(sessionID string) {
	if sessionID == "" {
		return
	}
	// A session owns this file from here on, whatever the rename does next: a
	// failed rename leaves the pending NAME, not a log nobody can account for.
	l.pending = false
	target := filepath.Join(filepath.Dir(l.path), daemonLogName(sessionID))
	if target == l.path {
		return
	}
	if err := os.Rename(l.path, target); err != nil {
		return
	}
	l.path = target
}

// tail returns the last limit bytes of the log, which is what a failed launch
// quotes back to the operator as the reason the daemon would not start.
func (l *daemonLog) tail(limit int) string {
	f, err := os.Open(l.path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	var b tailBuffer
	b.limit = limit
	if _, err := io.Copy(&b, f); err != nil {
		return ""
	}
	return b.String()
}

// removeIfPending deletes a log no session ever claimed. A launch that fails
// before rendezvous has already quoted this file's tail into its error, and
// nothing can name it after a session afterwards — the id arrives only with
// the rendezvous entry the launch did not get. Left behind, it is an opaque
// daemon-pending-*.log under <run-dir>/logs that no hub code reads, lists, or
// removes, one more per failed launch, forever (kata dd8d).
//
// A log a session HAS its name on is never removed here, whoever calls this:
// that file is an operator's account of a daemon that ran, and a resume
// appends to it.
func (l *daemonLog) removeIfPending() {
	if !l.pending {
		return
	}
	_ = os.Remove(l.path)
}

// close releases the hub's own descriptor. Call it once the child has been
// started and holds its own: the hub has no reason to keep a descriptor open
// for the whole life of a daemon it does not manage.
func (l *daemonLog) close() {
	_ = l.file.Close()
}

// daemonSpawnBanner is the one line the hub log keeps about a launched daemon.
// It has to say where that daemon's own words went, because they no longer
// arrive here.
func daemonSpawnBanner(sessionID string, pid int, logPath string) string {
	return fmt.Sprintf("[hub] daemon session=%s pid=%d log=%s\n", sessionID, pid, logPath)
}
