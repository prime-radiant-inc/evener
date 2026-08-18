// Package scratch is the Go twin of the shell scratch discipline the dev
// tooling settled on after the 2026-08-17 incident: TMPDIR-rooted, pid-named
// scratch that the owning tool reclaims itself on its next run
// (scripts/covscratch-lib.sh is the shell spelling; docs/testing.md holds the
// rules). There is no janitor: a directory abandoned by SIGKILL, an OOM kill,
// or a power cut lives exactly until the same tool's next Acquire.
//
// The API takes no path arguments anywhere: Acquire decides the path, Release
// removes only what Acquire minted, and ReclaimOwn touches only direct
// children of TMPDIR that carry the caller's own prefix and a dead owner pid.
package scratch

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Dir is one acquired scratch directory.
type Dir struct {
	path     string
	keep     bool
	warn     io.Writer
	released bool
}

// tmpBase resolves and vets TMPDIR. Refusing "/" and $HOME here is what makes
// every downstream removal in this package aim only at real scratch space.
func tmpBase() (string, error) {
	base, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return "", fmt.Errorf("scratch: TMPDIR is unusable: %w", err)
	}
	info, err := os.Stat(base)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("scratch: TMPDIR %s is not a directory", base)
	}
	if base == "/" {
		return "", errors.New("scratch: refusing TMPDIR resolving to /")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if resolvedHome, err := filepath.EvalSymlinks(home); err == nil && base == resolvedHome {
			return "", errors.New("scratch: refusing TMPDIR resolving to the home directory")
		}
	}
	return base, nil
}

// Acquire reclaims the prefix's abandoned scratch, then mints
// "<TMPDIR>/<prefix>.<pid>". The mkdir is loud on collision: a stale same-pid
// leftover is indistinguishable from a live sibling, so it is never deleted
// here and the caller hears about it instead. Reclaim warnings go to warn
// (nil means stderr).
func Acquire(prefix string, warn io.Writer) (*Dir, error) {
	if warn == nil {
		warn = os.Stderr
	}
	if prefix == "" || strings.ContainsAny(prefix, "/.") {
		return nil, fmt.Errorf("scratch: invalid prefix %q", prefix)
	}
	base, err := tmpBase()
	if err != nil {
		return nil, err
	}
	reclaimOwn(base, prefix, warn)
	path := filepath.Join(base, fmt.Sprintf("%s.%d", prefix, os.Getpid()))
	if err := os.Mkdir(path, 0o700); err != nil {
		return nil, fmt.Errorf("scratch: %s already exists or cannot be created: %w", path, err)
	}
	return &Dir{path: path, warn: warn}, nil
}

// Path returns the acquired directory's canonical path.
func (d *Dir) Path() string { return d.path }

// KeepOnFailure flips Release from removal to retain-and-report: the run
// failed and the directory's contents are the only record of why.
func (d *Dir) KeepOnFailure() { d.keep = true }

// Release removes the acquired directory, or — after KeepOnFailure — retains
// it and prints the retained-logs pointer. Safe to call more than once.
func (d *Dir) Release() {
	if d.released {
		return
	}
	d.released = true
	if d.keep {
		_, _ = fmt.Fprintf(d.warn, "full logs: %s\n", d.path)
		return
	}
	if err := os.RemoveAll(d.path); err != nil {
		_, _ = fmt.Fprintf(d.warn, "scratch: could not remove %s: %v\n", d.path, err)
	}
}

// ReclaimOwn removes every direct child of TMPDIR named "<prefix>.<pid>"
// whose pid is no longer running. Never fatal: a leftover that cannot be
// removed is reported to warn and skipped, and a TMPDIR this package refuses
// to mint under is likewise only reported.
func ReclaimOwn(prefix string, warn io.Writer) {
	if warn == nil {
		warn = os.Stderr
	}
	base, err := tmpBase()
	if err != nil {
		_, _ = fmt.Fprintf(warn, "%s: not reclaiming: %v\n", prefix, err)
		return
	}
	reclaimOwn(base, prefix, warn)
}

// reclaimOwn is scripts/covscratch-lib.sh's reclaim_own_scratch, rule for
// rule: directories only, direct children only, exact prefix match, symlinks
// never matched or followed, and the one thing that must never be touched is
// a live run's scratch — a live pid (including our own) always keeps its
// directory. Pid reuse only ever keeps a leftover one round longer, which is
// the right direction for a recursive delete to fail.
func reclaimOwn(base, prefix string, warn io.Writer) {
	entries, err := os.ReadDir(base)
	if err != nil {
		_, _ = fmt.Fprintf(warn, "%s: not reclaiming: %v\n", prefix, err)
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix+".") {
			continue
		}
		// ReadDir types via Lstat, so a symlink is a symlink here, never
		// the directory it points at.
		if !entry.IsDir() {
			continue
		}
		pid := ownerPid(name)
		if pid == 0 {
			continue
		}
		if pidAlive(pid) {
			continue
		}
		leftover := filepath.Join(base, name)
		if err := os.RemoveAll(leftover); err != nil {
			_, _ = fmt.Fprintf(warn, "%s: could not reclaim abandoned scratch %s\n", prefix, leftover)
		}
	}
}

// ownerPid parses the pid from the basename's final dot-suffix, the way the
// shell reclaimer reads "${leftover##*.}". Zero means "not a pid suffix":
// only plain digits qualify, so "notapid" and strconv's "+1"/"-1" spellings
// all keep their directories.
func ownerPid(name string) int {
	suffix := name[strings.LastIndexByte(name, '.')+1:]
	if suffix == "" {
		return 0
	}
	pid := 0
	for _, r := range suffix {
		if r < '0' || r > '9' {
			return 0
		}
		pid = pid*10 + int(r-'0')
		if pid > 1<<30 {
			return 0
		}
	}
	return pid
}

// pidAlive reports whether a pid answers signal 0. EPERM is an answer: the
// process exists, it just isn't ours, and an existing process keeps its
// directory.
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
