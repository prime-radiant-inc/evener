package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
)

// seedForkedSessionMemory gives a forked root session a copy of its parent's
// session memory the first time the scope is opened. The copy happens only
// while the child's directory does not exist yet, so the branches diverge
// after the fork. A failure is reported and leaves an empty scope; it never
// blocks the session.
func (s *Session) seedForkedSessionMemory() {
	parent, child := s.forkParentSessionID, s.memorySessionID()
	if parent == "" || s.depth > 0 || schema.ValidateSessionID(parent) != nil || schema.ValidateSessionID(child) != nil {
		return
	}
	base := filepath.Join(s.cfg.MemoryStateRoot, "memory", "sessions")
	dst := filepath.Join(base, child)
	if _, err := os.Lstat(dst); !errors.Is(err, os.ErrNotExist) {
		return
	}
	src := filepath.Join(base, parent)
	if _, err := os.Lstat(src); errors.Is(err, os.ErrNotExist) {
		return
	}
	if err := copySessionMemory(src, dst); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("could not copy the parent session's memory into this fork; session memory starts empty: %v", err)})
	}
}

// copySessionMemory copies into a sibling temporary directory and renames it
// into place, so a partial copy is never mistaken for a finished one.
func copySessionMemory(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dst), ".fork-copy-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if err := os.CopyFS(tmp, os.DirFS(src)); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
