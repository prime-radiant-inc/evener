package hostlock

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
)

// ErrNonlocalStorage distinguishes storage refusal from an absent/stopped hub.
var ErrNonlocalStorage = errors.New("hub state requires local storage")

// StorageError identifies the selected path and the OS filesystem classification.
type StorageError struct{ Path, Filesystem string }

func (e *StorageError) Error() string {
	return fmt.Sprintf("hub state %q is on %s: %v, manually copy durable hub state to local storage before selecting it, see docs/evener-hub-state.md", e.Path, e.Filesystem, ErrNonlocalStorage)
}
func (e *StorageError) Unwrap() error { return ErrNonlocalStorage }

// FilesystemInspector is the OS classification boundary. A nil inspector uses
// the platform's fstatfs implementation. Admission is not locality certification.
type FilesystemInspector func(*os.File) (classification string, nonlocal bool, err error)

// filesystemInspector is replaced only by serial OS-boundary package tests.
var filesystemInspector FilesystemInspector = inspectFilesystem

// CheckStorage inspects the existing selected directory or its nearest existing
// directory ancestor, without creation. It preserves kernel symlink/.. lookup
// and whitespace, rather than cleaning the selection into another pathname.
func CheckStorage(root string, inspect FilesystemInspector) error {
	path := root
	for {
		file, err := openStorageDirectory(path)
		if err == nil {
			info, err := file.Stat()
			if err != nil {
				_ = file.Close()
				return fmt.Errorf("inspect hub state %q: %w", root, err)
			}
			if !info.IsDir() {
				_ = file.Close()
				return fmt.Errorf("inspect hub state %q: %w", root, &os.PathError{Op: "open", Path: path, Err: syscall.ENOTDIR})
			}
			err = checkStorageFile(file, root, inspect)
			_ = file.Close()
			return err
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect hub state %q: %w", root, err)
		}
		// A dangling symlink is not an absent fresh directory. Nor may an error
		// looking up a component silently send us to another lexical ancestor.
		trimmed := strings.TrimRight(path, string(os.PathSeparator))
		if _, linkErr := os.Lstat(trimmed); !errors.Is(linkErr, os.ErrNotExist) {
			if linkErr != nil {
				return fmt.Errorf("inspect hub state %q: %w", root, linkErr)
			}
			return fmt.Errorf("inspect hub state %q: %w", root, err)
		}
		i := strings.LastIndexByte(trimmed, os.PathSeparator)
		switch {
		case i < 0:
			if trimmed == "." || trimmed == "" {
				return fmt.Errorf("inspect hub state %q: %w", root, err)
			}
			path = "."
		case i == 0:
			path = string(os.PathSeparator)
		default:
			path = trimmed[:i]
		}
	}
}

func checkStorageFile(file *os.File, selected string, inspect FilesystemInspector) error {
	if inspect == nil {
		inspect = filesystemInspector
	}
	classification, nonlocal, err := inspect(file)
	if err != nil {
		return fmt.Errorf("inspect filesystem for hub state %q: %w", selected, err)
	}
	if nonlocal {
		return &StorageError{Path: selected, Filesystem: classification}
	}
	return nil
}
