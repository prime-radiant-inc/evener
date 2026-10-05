package agent

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
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
	if s.fork.divergence == 0 || parent == "" || s.isMemoryDelegate() || schema.ValidateSessionID(parent) != nil || schema.ValidateSessionID(child) != nil {
		return
	}
	// A parent that never wrote session memory is the common case. Absence
	// carries nothing to copy, so it is settled without opening the memory
	// root; everything that exists goes through the confined copy below.
	if _, err := os.Lstat(filepath.Join(s.cfg.MemoryStateRoot, "memory", "sessions", parent)); errors.Is(err, os.ErrNotExist) {
		return
	}
	if err := copySessionMemory(s.cfg.MemoryStateRoot, parent, child); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("could not copy the parent session's memory into this fork; session memory starts empty: %v", err)})
	}
}

// copySessionMemory copies the parent's session scope into the child's
// through a confined environment rooted at memory/sessions, the same
// fd-anchored layer the memory tools use: no path component, including
// memory/ and memory/sessions/ themselves, is followed through a symlink, so
// the copy can neither read nor write outside the memory state root. The copy
// lands in a sibling temporary directory and is renamed into place, so a
// partial copy is never mistaken for a finished one.
func copySessionMemory(stateRoot, parent, child string) error {
	sessions, err := execenv.NewConfinedFileEnvironment(stateRoot, filepath.Join("memory", "sessions"))
	if err != nil {
		return err
	}
	defer sessions.Cleanup()
	if _, err := sessions.ListDirectory(child, 1); !errors.Is(err, os.ErrNotExist) {
		return nil
	}
	entries, err := sessions.ListDirectory(parent, 1)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("parent session memory is not a readable directory: %w", err)
	}
	c := &forkMemoryCopy{sessions: sessions, src: parent, dst: ".fork-copy-" + rand.Text()}
	err = c.copyEntries("", entries)
	if err == nil && len(c.files) > 0 {
		err = sessions.RenamePath(c.dst, child)
	}
	if err != nil || len(c.files) == 0 {
		c.removeCopied()
	}
	return err
}

// forkMemoryCopy copies directories and regular files from src into dst,
// both relative to the confined sessions environment. Anything else (a
// symlink, device or socket) would carry content from outside the parent's
// memory into the fork's model-visible scope, so it stops the copy. The size
// caps keep a fork's first use from stalling on a runaway parent directory.
// Empty directories hold no memory and are not copied.
type forkMemoryCopy struct {
	sessions *execenv.LocalExecutionEnvironment
	src, dst string
	entries  int
	bytes    int64
	// files and dirs record what the copy created, relative to dst, so a
	// failed copy can remove exactly that through the confined layer.
	files, dirs []string
}

func (c *forkMemoryCopy) copyEntries(rel string, entries []execenv.DirEntry) error {
	for _, entry := range entries {
		c.entries++
		if c.entries > maxForkMemoryCopyEntries {
			return fmt.Errorf("parent session memory exceeds the fork copy limit (%d entries)", maxForkMemoryCopyEntries)
		}
		name := filepath.Join(rel, entry.Name)
		switch {
		case entry.IsSymlink:
			return fmt.Errorf("parent session memory holds %s, which is not a regular file or directory", name)
		case entry.IsDir:
			children, err := c.sessions.ListDirectory(filepath.Join(c.src, name), 1)
			if err != nil {
				return err
			}
			c.dirs = append(c.dirs, name)
			if err := c.copyEntries(name, children); err != nil {
				return err
			}
		default:
			if err := c.copyFile(name); err != nil {
				return err
			}
		}
	}
	return nil
}

// copyFile copies one file. OpenConfinedFile refuses anything but a regular
// file, so a device or socket in the parent stops the copy here.
func (c *forkMemoryCopy) copyFile(name string) error {
	in, err := c.sessions.OpenConfinedFile(filepath.Join(c.src, name))
	if err != nil {
		return err
	}
	data, err := readWithinBudget(in, maxForkMemoryCopyBytes-c.bytes)
	_ = in.Close()
	if err != nil {
		return err
	}
	c.bytes += int64(len(data))
	c.files = append(c.files, name)
	return c.sessions.WriteFileRaw(filepath.Join(c.dst, name), data, 0o600)
}

// readWithinBudget reads r but never more than budget+1 bytes, so a file that
// grew after it was listed cannot push the copy past its byte limit.
func readWithinBudget(r io.Reader, budget int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, budget+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > budget {
		return nil, fmt.Errorf("parent session memory exceeds the fork copy limit (%d bytes)", maxForkMemoryCopyBytes)
	}
	return data, nil
}

// removeCopied removes the temporary copy, deepest entries first. It is best
// effort: a leftover dot-prefixed directory is never a session scope.
func (c *forkMemoryCopy) removeCopied() {
	for _, name := range slices.Backward(c.files) {
		_ = c.sessions.RemovePath(filepath.Join(c.dst, name))
	}
	for _, name := range slices.Backward(c.dirs) {
		_ = c.sessions.RemovePath(filepath.Join(c.dst, name))
	}
	_ = c.sessions.RemovePath(c.dst)
}
