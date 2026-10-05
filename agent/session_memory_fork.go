package agent

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
)

const (
	maxForkMemoryCopyBytes   = 16 << 20
	maxForkMemoryCopyEntries = 2000
)

// seedForkedSessionMemory gives a forked root session a copy of its parent's
// session memory the first time the scope is opened. The copy happens only
// while the child's directory does not exist yet, so the branches diverge
// after the fork. A failure is reported and leaves an empty scope; it never
// blocks the session.
func (s *Session) seedForkedSessionMemory() {
	parent, child := s.fork.parentID, s.memorySessionID()
	if s.fork.divergence == 0 || parent == "" || s.depth > 0 || schema.ValidateSessionID(parent) != nil || schema.ValidateSessionID(child) != nil {
		return
	}
	base := filepath.Join(s.cfg.MemoryStateRoot, "memory", "sessions")
	dst := filepath.Join(base, child)
	if _, err := os.Lstat(dst); !errors.Is(err, os.ErrNotExist) {
		return
	}
	src := filepath.Join(base, parent)
	// Only a real directory is a session scope. A symlink or file in its place
	// is never followed, and starts the fork empty without a warning.
	if info, err := os.Lstat(src); err == nil && !info.IsDir() || errors.Is(err, os.ErrNotExist) {
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
	if err := copyRegularMemoryTree(tmp, src); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// copyRegularMemoryTree copies directories and regular files from src into
// dst. Anything else (a symlink, device or socket) would carry content from
// outside the parent's memory into the fork's model-visible scope, so it stops
// the copy. The size caps keep a fork's first use from stalling on a runaway
// parent directory.
func copyRegularMemoryTree(dst, src string) error {
	var entries int
	var bytes int64
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		entries++
		if entries > maxForkMemoryCopyEntries {
			return fmt.Errorf("parent session memory exceeds the fork copy limit (%d entries)", maxForkMemoryCopyEntries)
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.Mkdir(target, 0o700)
		case d.Type().IsRegular():
			info, err := d.Info()
			if err != nil {
				return err
			}
			bytes += info.Size()
			if bytes > maxForkMemoryCopyBytes {
				return fmt.Errorf("parent session memory exceeds the fork copy limit (%d bytes)", maxForkMemoryCopyBytes)
			}
			return copyRegularFile(target, path)
		default:
			return fmt.Errorf("parent session memory holds %s, which is not a regular file or directory", rel)
		}
	})
}

func copyRegularFile(dst, src string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()
	_, err = io.Copy(out, in)
	return err
}
