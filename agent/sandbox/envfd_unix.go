//go:build unix

package sandbox

import (
	"os"
)

// childEnvFile writes childEnv's payload to a private, unnamed file and returns
// it open at offset 0, ready to inherit as the child-env fd. The file has no
// name once this returns, so only a process holding the fd can read the
// assignments; the launcher (bwrap, or the Seatbelt reader) reads them at
// startup and the spawn site closes them afterwards.
func childEnvFile(backend Backend, childEnv ChildEnvDelta) (*os.File, error) {
	return privateFile(childEnvPayload(backend, childEnv))
}

// unlinkedTempFile is the portable private-file fallback: create, unlink, then
// write. The inode survives only while a holder keeps the fd open.
func unlinkedTempFile(payload []byte) (*os.File, error) {
	f, err := os.CreateTemp("", "evener-child-env-*")
	if err != nil {
		return nil, err
	}
	if err := os.Remove(f.Name()); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := writeAtStart(f, payload); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// writeAtStart writes payload at the start of f without moving its offset, so a
// reader that inherits the fd starts at the first record.
func writeAtStart(f *os.File, payload []byte) error {
	_, err := f.WriteAt(payload, 0)
	return err
}
