//go:build !linux && !darwin

package llm

import (
	"fmt"
	"os"
)

func openPrivateAPILogFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	closeOnError := func(err error) (*os.File, error) {
		_ = file.Close()
		return nil, err
	}

	info, err := file.Stat()
	if err != nil {
		return closeOnError(err)
	}
	if !info.Mode().IsRegular() {
		return closeOnError(fmt.Errorf("API-log target %q is not a regular file", path))
	}
	if err := file.Chmod(0o600); err != nil {
		return closeOnError(err)
	}
	if err := recoverCanonicalAPILogTail(file, canonicalAPILogMaxLineBytes); err != nil {
		return closeOnError(err)
	}
	return file, nil
}
