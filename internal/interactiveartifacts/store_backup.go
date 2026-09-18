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
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
		return err
	}
	if err := requirePrivatePath(filepath.Dir(absolute), true); err != nil {
		return err
	}
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
	return errors.Join(directory.Sync(), directory.Close())
}
