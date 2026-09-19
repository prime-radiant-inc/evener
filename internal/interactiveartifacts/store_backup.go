package interactiveartifacts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// Backup writes a coherent SQLite snapshot, including committed WAL content,
// into a new private file. It syncs the file and its directory before returning.
// The caller separately owns Hub realm/association backup and restore ordering.
func (s *Store) Backup(ctx context.Context, path string) (resultErr error) {
	directoryPath, name := filepath.Split(path)
	root, err := PrepareStoreDirectory(directoryPath)
	if err != nil {
		return err
	}
	absolute := filepath.Join(root, name)
	file, err := os.OpenFile(absolute, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, file.Close())
		if resultErr != nil {
			_ = os.Remove(absolute)
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if err := requirePrivateInfo(info, false); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", absolute); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(absolute))
	if err != nil {
		return err
	}
	info, statErr := directory.Stat()
	if statErr == nil {
		statErr = requirePrivateInfo(info, true)
	}
	if statErr != nil {
		return errors.Join(statErr, directory.Close())
	}
	return errors.Join(directory.Sync(), directory.Close())
}
