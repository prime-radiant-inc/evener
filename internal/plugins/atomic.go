package plugins

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

type atomicFile interface {
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

var (
	atomicMkdirAll    = os.MkdirAll
	atomicReadEntropy = rand.Read
	atomicOpenFile    = func(name string, flag int, perm os.FileMode) (atomicFile, error) {
		return os.OpenFile(name, flag, perm)
	}
	atomicRemove  = os.Remove
	atomicRename  = os.Rename
	atomicOpenDir = func(name string) (atomicFile, error) { return os.Open(name) }
)

// atomicWriteFile writes data to path atomically: it creates an O_EXCL temp
// file in the same directory, fsyncs it, renames it over path, then fsyncs the
// parent directory so the rename is durable. The temp file is removed on any
// error.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	parent := filepath.Dir(path)
	if err := atomicMkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", parent, err)
	}
	suf := make([]byte, 6)
	if _, err := atomicReadEntropy(suf); err != nil {
		return fmt.Errorf("entropy: %w", err)
	}
	tmp := fmt.Sprintf("%s.tmp.%d.%s", path, os.Getpid(), hex.EncodeToString(suf))

	f, err := atomicOpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return fmt.Errorf("creating temp %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = atomicRemove(tmp)
		return fmt.Errorf("writing temp %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = atomicRemove(tmp)
		return fmt.Errorf("syncing temp %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = atomicRemove(tmp)
		return fmt.Errorf("closing temp %s: %w", tmp, err)
	}
	if err := atomicRename(tmp, path); err != nil {
		_ = atomicRemove(tmp)
		return fmt.Errorf("renaming %s -> %s: %w", tmp, path, err)
	}
	if dir, err := atomicOpenDir(parent); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}
