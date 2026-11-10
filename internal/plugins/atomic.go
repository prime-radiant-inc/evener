package plugins

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"primeradiant.com/evener/execsupport/fsatomic"
)

// atomicWriteFile writes data to path atomically and durably, creating the
// parent directory. A directory sync that fails after the rename leaves the
// new file in place, matching what the store's state already says, so it is
// not reported as a failed write.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", parent, err)
	}
	if err := fsatomic.Write(path, data, perm); err != nil && !errors.Is(err, fsatomic.ErrPublished) {
		return err
	}
	return nil
}
